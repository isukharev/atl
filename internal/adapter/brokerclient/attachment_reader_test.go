package brokerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type cancelableAttachmentBody struct {
	prefix  []byte
	done    <-chan struct{}
	blocked chan struct{}
	once    sync.Once
	closed  atomic.Int32
}

func (b *cancelableAttachmentBody) Read(buffer []byte) (int, error) {
	if len(b.prefix) > 0 {
		n := copy(buffer, b.prefix)
		b.prefix = b.prefix[n:]
		return n, nil
	}
	b.once.Do(func() { close(b.blocked) })
	<-b.done
	return 0, context.Canceled
}

func (b *cancelableAttachmentBody) Close() error {
	b.closed.Add(1)
	return nil
}

func TestHeldAttachmentReaderConcurrentCloseCancelsAndJoinsRead(t *testing.T) {
	fixture := newHeldAttachmentFixture(t, []byte("PRIVATE-PAYLOAD-CANARY"))
	manifestEnd := bytes.IndexByte(fixture.wire, '\n') + 1
	if manifestEnd <= 0 {
		t.Fatal("manifest delimiter missing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	deadline, _ := ctx.Deadline()
	body := &cancelableAttachmentBody{prefix: bytes.Clone(fixture.wire[:manifestEnd]), done: ctx.Done(), blocked: make(chan struct{})}
	client := &Client{config: Config{Session: &countingSessionLoader{value: testSession()}}, now: time.Now}
	invocation := &heldAttachmentInvocation{client: client, definition: fixture.definition, selected: testSession(), ctx: ctx, deadline: deadline, cancel: cancel}
	reader, _, err := newHeldAttachmentReader(invocation, httpx.BoundedResponseStream{
		Status: http.StatusOK, ContentType: brokertransport.ExecutionStreamMediaTypeV3, CorrelationID: fixture.correlation, Body: body,
	}, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 32)
		n, readErr := reader.Read(buffer)
		if n != 0 {
			readDone <- fmt.Errorf("late bytes=%d", n)
			return
		}
		readDone <- readErr
	}()
	select {
	case <-body.blocked:
	case <-time.After(time.Second):
		t.Fatal("reader did not reach blocked body")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- reader.Close() }()
	select {
	case readErr := <-readDone:
		if !errors.Is(readErr, context.Canceled) || errors.Is(readErr, io.EOF) {
			t.Fatalf("read error=%v", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active read")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join active read")
	}
	if err := reader.Close(); err != nil || body.closed.Load() != 1 {
		t.Fatalf("second close=%v body closes=%d", err, body.closed.Load())
	}
	formatted := fmt.Sprintf("%v|%+v|%#v|%s|%q|%d", reader, reader, reader, reader, reader, reader)
	if strings.Contains(formatted, "PRIVATE-PAYLOAD-CANARY") || strings.Contains(formatted, string(testSession().Credential)) || !strings.Contains(formatted, heldAttachmentReaderLabel) {
		t.Fatalf("unsafe reader format=%q", formatted)
	}
}

func TestHeldAttachmentReaderStopsBufferedPayloadAfterCancellationOrDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline_%t", expired), func(t *testing.T) {
			fixture := newHeldAttachmentFixture(t, []byte("buffered-attachment-body"))
			body := &heldAttachmentBody{data: bytes.Clone(fixture.wire)}
			reader, _, err := newHeldAttachmentFixtureReader(t, fixture, &countingSessionLoader{value: testSession()}, body)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			var one [1]byte
			if n, err := reader.Read(one[:]); n != 1 || err != nil || reader.payloadOffset >= len(reader.payload) {
				t.Fatalf("initial buffered read = (%d, %v)", n, err)
			}
			want := context.Canceled
			if expired {
				reader.client.now = func() time.Time { return reader.deadline }
				want = context.DeadlineExceeded
			} else {
				reader.cancel()
			}
			if n, err := reader.Read(one[:]); n != 0 || !errors.Is(err, want) || errors.Is(err, io.EOF) {
				t.Fatalf("late buffered read = (%d, %v), want %v", n, err, want)
			}
			if body.closed.Load() != 1 || reader.eof.Load() {
				t.Fatal("canceled payload retained the body or became verified EOF")
			}
		})
	}
}

func TestHeldAttachmentInvocationFormattingDoesNotTraverseSession(t *testing.T) {
	invocation := &heldAttachmentInvocation{selected: testSession()}
	defer invocation.close()
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		if got := fmt.Sprintf(format, invocation); got != "Broker held attachment invocation" {
			t.Fatalf("format %s traversed held state", format)
		}
	}
}

func TestHeldAttachmentReaderRejectsReleaseContinuityMutations(t *testing.T) {
	fixture := newHeldAttachmentFixture(t, bytes.Repeat([]byte{'x'}, int(brokercontract.MaxAttachmentDecodedFrameBytesV3)+17))
	lines := bytes.Split(bytes.TrimSuffix(fixture.wire, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 4 {
		t.Fatalf("lines=%d", len(lines))
	}
	first, err := brokercontract.DecodeAttachmentDataLineV3(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	second, err := brokercontract.DecodeAttachmentDataLineV3(lines[2])
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := brokercontract.DecodeAttachmentTerminalLineV3(lines[3])
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][][]byte{}
	changed := second
	changed.PriorReleaseSHA256 = heldAttachmentDigest("wrong-prior")
	mutations["prior receipt"] = replaceAttachmentLine(t, lines, 2, changed, brokercontract.EncodeAttachmentDataLineV3)
	changed = first
	changed.ReleaseDecisionSHA256 = heldAttachmentDigest("wrong-first-decision")
	mutations["first decision"] = replaceAttachmentLine(t, lines, 1, changed, brokercontract.EncodeAttachmentDataLineV3)
	changed = second
	changed.CumulativeSHA256 = heldAttachmentDigest("wrong-cumulative")
	mutations["cumulative hash"] = replaceAttachmentLine(t, lines, 2, changed, brokercontract.EncodeAttachmentDataLineV3)
	changedTerminal := terminal
	changedTerminal.ReleaseDecisionSHA256 = heldAttachmentDigest("wrong-terminal-decision")
	mutations["terminal decision"] = replaceAttachmentLine(t, lines, 3, changedTerminal, brokercontract.EncodeAttachmentTerminalLineV3)
	for name, changedLines := range mutations {
		t.Run(name, func(t *testing.T) {
			wire := bytes.Join(append(changedLines, nil), []byte{'\n'})
			changedFixture := fixture
			changedFixture.wire = wire
			reader, _, err := newHeldAttachmentFixtureReader(t, changedFixture, &countingSessionLoader{value: testSession()}, &heldAttachmentBody{data: bytes.Clone(wire), maximum: 19})
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if !errors.Is(readErr, domain.ErrCheckFailed) || errors.Is(readErr, io.EOF) {
				t.Fatalf("error=%v", readErr)
			}
		})
	}
}

func TestAttachmentV3DiscoveryRowRequiresEveryFixedField(t *testing.T) {
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok {
		t.Fatal("definition missing")
	}
	operation := domain.BrokerFamilyDiscoveryOperationV4{
		ID: definition.Definition.ID, Version: definition.Definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
		Features: append([]string(nil), definition.Definition.RequiredFeatures...), Limits: definition.Definition.Limits,
		Effects: append([]domain.BrokerEffectDefinition(nil), definition.Definition.Effects...), MaxMetadataItems: definition.MaxMetadataItems,
		MaxJiraAttempts: definition.MaxJiraAttempts, MaxAuthenticationAttempts: definition.MaxAuthenticationAttempts, MaxDecisionAttempts: definition.MaxDecisionAttempts,
		MaxTotalHostOutboundAttempts: definition.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: definition.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: definition.MaxJiraResponseBytes, MaxAuthorityResponseBytes: definition.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: definition.MaxTotalHostResponseBytes,
		MaxManifestLineBytes: definition.MaxManifestLineBytes, MaxDataLineBytes: definition.MaxDataLineBytes, MaxTerminalLineBytes: definition.MaxTerminalLineBytes,
		MaxFramedResponseBytes: definition.MaxFramedResponseBytes,
	}
	projection := domain.BrokerFamilyDiscoveryProjectionV4{Operations: []domain.BrokerFamilyDiscoveryOperationV4{operation}}
	if err := requireAllowedAttachmentV3(projection, definition); err != nil {
		t.Fatalf("exact row: %v", err)
	}
	for name, mutate := range map[string]func(*domain.BrokerFamilyDiscoveryOperationV4){
		"version":        func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.Version++ },
		"feature":        func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.Features = []string{"changed"} },
		"limit":          func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.Limits.MaxStreamChunks++ },
		"effect":         func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.Effects = nil },
		"extended total": func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.MaxTotalHostOutboundAttempts++ },
		"framed bytes":   func(value *domain.BrokerFamilyDiscoveryOperationV4) { value.MaxFramedResponseBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := operation
			mutate(&changed)
			if err := requireAllowedAttachmentV3(domain.BrokerFamilyDiscoveryProjectionV4{Operations: []domain.BrokerFamilyDiscoveryOperationV4{changed}}, definition); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("changed row err=%v", err)
			}
		})
	}
	projection.Operations[0] = operation
	projection.Operations[0].Access = domain.BrokerDiscoveryAccessRequestRequired
	projection.Operations[0].RequestAccessCorrelation = "access-request-1"
	if err := requireAllowedAttachmentV3(projection, definition); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("request access err=%v", err)
	}
	projection.Operations[0].Access = domain.BrokerDiscoveryAccessUnavailable
	projection.Operations[0].RequestAccessCorrelation = ""
	if err := requireAllowedAttachmentV3(projection, definition); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unavailable err=%v", err)
	}
}

func TestAttachmentV4DiscoveryFailureDecoderIsExact(t *testing.T) {
	v4, err := brokertransport.EncodeDiscoveryFailureV4(domain.BrokerReasonDenied)
	if err != nil {
		t.Fatal(err)
	}
	v3, err := brokertransport.EncodeDiscoveryFailureV3(domain.BrokerReasonDenied)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		response   httpx.BoundedResponse
		wantReason domain.BrokerReason
		wantBody   bool
	}{
		{name: "v4 denial", response: httpx.BoundedResponse{Status: http.StatusForbidden, Body: v4}, wantReason: domain.BrokerReasonDenied},
		{name: "wrong v3 failure", response: httpx.BoundedResponse{Status: http.StatusForbidden, Body: v3}},
		{name: "missing success correlation", response: httpx.BoundedResponse{Status: http.StatusOK, Body: []byte("body")}},
		{name: "success", response: httpx.BoundedResponse{Status: http.StatusOK, Body: []byte("body"), CorrelationID: "correlation-1"}, wantBody: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := acceptedFamilyDiscoveryBodyV4(test.response)
			if test.wantBody {
				if err != nil || string(body) != "body" {
					t.Fatalf("body=%q err=%v", body, err)
				}
				return
			}
			if test.wantReason != "" {
				if attachmentReason(err) != test.wantReason {
					t.Fatalf("reason=%s err=%v", attachmentReason(err), err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func replaceAttachmentLine[T any](t *testing.T, lines [][]byte, index int, value T, encode func(T) ([]byte, error)) [][]byte {
	t.Helper()
	replacement, err := encode(value)
	if err != nil {
		t.Fatal(err)
	}
	result := make([][]byte, len(lines))
	for lineIndex := range lines {
		result[lineIndex] = bytes.Clone(lines[lineIndex])
	}
	result[index] = replacement
	return result
}

func FuzzAttachmentLineReaderIsBounded(f *testing.F) {
	for _, seed := range [][]byte{[]byte("{}\n"), []byte("{}"), []byte("\n"), []byte("{}\ntrailing")} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, body []byte) {
		if len(body) > int(brokercontract.MaxAttachmentManifestLineBytesV3)+2 {
			return
		}
		reader := attachmentLineReader{source: bytes.NewReader(body), scratch: make([]byte, 17)}
		line, _, _ := reader.readLine(brokercontract.MaxAttachmentManifestLineBytesV3)
		clear(line)
		reader.clear()
	})
}
