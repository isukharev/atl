package brokerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type heldAttachmentFixture struct {
	definition  domain.BrokerAttachmentOperationDefinitionV3
	request     domain.BrokerAttachmentRequestV3
	correlation string
	filename    string
	wire        []byte
}

type heldAttachmentBody struct {
	data    []byte
	maximum int
	closed  atomic.Int32
}

type advancingAttachmentSessionLoader struct {
	value   Session
	advance func()
}

func (l advancingAttachmentSessionLoader) Load() (Session, error) {
	if l.advance != nil {
		l.advance()
	}
	value := l.value
	value.Credential = bytes.Clone(value.Credential)
	return value, nil
}

func (b *heldAttachmentBody) Read(buffer []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	limit := len(buffer)
	if b.maximum > 0 && limit > b.maximum {
		limit = b.maximum
	}
	n := copy(buffer[:limit], b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *heldAttachmentBody) Close() error {
	b.closed.Add(1)
	return nil
}

func TestJiraAttachmentClientRejectsInvalidSelectorsBeforeIO(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	jira, err := NewJira(client)
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range [][2]string{{"bad", "1"}, {"PROJ-1", "0"}, {"PROJ-1", "01"}, {"PROJ-1", "attachment"}} {
		body, name, callErr := jira.DownloadAttachment(t.Context(), selector[0], selector[1])
		if body != nil || name != "" || !errors.Is(callErr, domain.ErrUsage) {
			t.Fatalf("selector=%q body=%v name=%q err=%v", selector, body, name, callErr)
		}
	}
	if loader.calls.Load() != 0 {
		t.Fatalf("session loads=%d", loader.calls.Load())
	}
}

func TestJiraAttachmentClientRequiresDiscoveryBeforeExecute(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var discoveryCalls, otherCalls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != brokertransport.DiscoveryNegotiatePathV4 {
					otherCalls.Add(1)
				} else {
					discoveryCalls.Add(1)
				}
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(`{}`))
			}))
			t.Cleanup(server.Close)
			loader := &countingSessionLoader{value: testSession()}
			client := newTestClient(t, server, loader, "broker-1")
			jira, err := NewJira(client)
			if err != nil {
				t.Fatal(err)
			}
			body, name, err := jira.DownloadAttachment(t.Context(), "PROJ-1", "7")
			if err == nil || body != nil || name != "" || discoveryCalls.Load() != 1 || otherCalls.Load() != 0 || loader.calls.Load() != 1 {
				t.Fatalf("body=%v name=%q err=%v discovery=%d other=%d sessions=%d", body, name, err, discoveryCalls.Load(), otherCalls.Load(), loader.calls.Load())
			}
		})
	}
}

func TestHeldAttachmentReaderAcceptsZeroOneTwoAndMaximumFrames(t *testing.T) {
	for _, size := range []int{0, 23, int(brokercontract.MaxAttachmentDecodedFrameBytesV3) + 17, int(brokercontract.MaxAttachmentNativeBodyBytesV3)} {
		name := fmt.Sprintf("bytes_%d", size)
		t.Run(name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, size)
			fixture := newHeldAttachmentFixture(t, payload)
			loader := &countingSessionLoader{value: testSession()}
			body := &heldAttachmentBody{data: bytes.Clone(fixture.wire), maximum: 7}
			reader, filename, err := newHeldAttachmentFixtureReader(t, fixture, loader, body)
			if err != nil {
				t.Fatal(err)
			}
			actual, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || filename != fixture.filename || !bytes.Equal(actual, payload) {
				t.Fatalf("bytes=%d filename=%q read=%v close=%v match=%t", len(actual), filename, readErr, closeErr, bytes.Equal(actual, payload))
			}
			frames := 0
			if size > 0 {
				frames = (size + int(brokercontract.MaxAttachmentDecodedFrameBytesV3) - 1) / int(brokercontract.MaxAttachmentDecodedFrameBytesV3)
			}
			if loader.calls.Load() != int32(frames+2) || body.closed.Load() != 1 {
				t.Fatalf("session loads=%d body closes=%d frames=%d", loader.calls.Load(), body.closed.Load(), frames)
			}
		})
	}
}

func TestHeldAttachmentReaderRejectsMalformedEarlyAndTrailingStreamsWithoutEOF(t *testing.T) {
	base := newHeldAttachmentFixture(t, []byte("native attachment bytes"))
	lines := bytes.Split(bytes.TrimSuffix(base.wire, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("lines=%d", len(lines))
	}
	mutatedData := bytes.Clone(lines[1])
	marker := []byte(base64.StdEncoding.EncodeToString([]byte("native attachment bytes")))
	index := bytes.Index(mutatedData, marker)
	if index < 0 {
		t.Fatal("payload marker missing")
	}
	mutatedData[index] = 'A'
	badCount := bytes.Replace(bytes.Clone(lines[2]), []byte(`"chunk_count":1`), []byte(`"chunk_count":2`), 1)
	oversized := append(bytes.Repeat([]byte{'x'}, int(brokercontract.MaxAttachmentDataLineBytesV3)+1), '\n')
	for _, test := range []struct {
		name string
		wire []byte
	}{
		{name: "early EOF", wire: append(append(bytes.Clone(lines[0]), '\n'), append(lines[1], '\n')...)},
		{name: "malformed data", wire: bytes.Join([][]byte{lines[0], mutatedData, lines[2], nil}, []byte{'\n'})},
		{name: "null data", wire: bytes.Join([][]byte{lines[0], []byte("null"), lines[2], nil}, []byte{'\n'})},
		{name: "unknown data member", wire: bytes.Join([][]byte{lines[0], []byte(`{"kind":"data","unknown":1}`), lines[2], nil}, []byte{'\n'})},
		{name: "duplicate data", wire: bytes.Join([][]byte{lines[0], lines[1], lines[1], lines[2], nil}, []byte{'\n'})},
		{name: "out of order", wire: bytes.Join([][]byte{lines[0], lines[2], lines[1], nil}, []byte{'\n'})},
		{name: "terminal count", wire: bytes.Join([][]byte{lines[0], lines[1], badCount, nil}, []byte{'\n'})},
		{name: "trailing", wire: append(bytes.Clone(base.wire), []byte("{}\n")...)},
		{name: "over limit", wire: append(append(bytes.Clone(lines[0]), '\n'), oversized...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := base
			fixture.wire = test.wire
			reader, _, err := newHeldAttachmentFixtureReader(t, fixture, &countingSessionLoader{value: testSession()}, &heldAttachmentBody{data: bytes.Clone(test.wire), maximum: 13})
			if err != nil {
				if errors.Is(err, io.EOF) {
					t.Fatalf("constructor returned EOF: %v", err)
				}
				return
			}
			_, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr == nil || errors.Is(readErr, io.EOF) || !errors.Is(readErr, domain.ErrCheckFailed) {
				t.Fatalf("read error=%v", readErr)
			}
		})
	}
}

func TestAttachmentSuccessResponseRequiresExactStatusAndMetadata(t *testing.T) {
	valid := httpx.BoundedResponseStream{
		Status: http.StatusOK, ContentType: brokertransport.ExecutionStreamMediaTypeV3,
		CorrelationID: "correlation-1", Body: io.NopCloser(strings.NewReader("body")),
	}
	if !validAttachmentSuccessResponseV3(valid) {
		t.Fatal("valid response rejected")
	}
	for _, mutate := range []func(*httpx.BoundedResponseStream){
		func(value *httpx.BoundedResponseStream) { value.Status = http.StatusCreated },
		func(value *httpx.BoundedResponseStream) { value.ContentType = "application/x-ndjson; charset=utf-8" },
		func(value *httpx.BoundedResponseStream) { value.ContentEncoding = "identity" },
		func(value *httpx.BoundedResponseStream) { value.CorrelationID = "" },
		func(value *httpx.BoundedResponseStream) { value.CorrelationID = "bad correlation" },
		func(value *httpx.BoundedResponseStream) { value.Body = nil },
	} {
		changed := valid
		mutate(&changed)
		if validAttachmentSuccessResponseV3(changed) {
			t.Fatalf("changed response accepted: %+v", changed)
		}
	}
}

func TestHeldAttachmentReaderDoesNotReturnFilenameAfterDeadlineDuringSessionReload(t *testing.T) {
	fixture := newHeldAttachmentFixture(t, []byte("body"))
	clock := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	client := &Client{now: func() time.Time { return clock }}
	client.config.Session = advancingAttachmentSessionLoader{value: testSession(), advance: func() { clock = clock.Add(2 * time.Second) }}
	ctx, cancel := context.WithCancel(t.Context())
	body := &heldAttachmentBody{data: bytes.Clone(fixture.wire), maximum: 11}
	invocation := &heldAttachmentInvocation{
		client: client, definition: fixture.definition, selected: testSession(), ctx: ctx, deadline: clock.Add(time.Second), cancel: cancel,
	}
	reader, filename, err := newHeldAttachmentReader(invocation, httpx.BoundedResponseStream{
		Status: http.StatusOK, ContentType: brokertransport.ExecutionStreamMediaTypeV3, CorrelationID: fixture.correlation, Body: body,
	}, fixture.request)
	if reader != nil || filename != "" || !errors.Is(err, context.DeadlineExceeded) || body.closed.Load() != 1 {
		t.Fatalf("reader=%v filename=%q err=%v closes=%d", reader, filename, err, body.closed.Load())
	}
}

func TestHeldAttachmentReaderSessionChecksPrecedeManifestReturnFrameYieldAndEOF(t *testing.T) {
	payload := []byte("native attachment bytes")
	fixture := newHeldAttachmentFixture(t, payload)
	replacement := testSession()
	replacement.Credential = []byte("replacement-workload-credential")
	for _, test := range []struct {
		name   string
		values []Session
		want   string
	}{
		{name: "after manifest", values: []Session{replacement}, want: "constructor"},
		{name: "before frame yield", values: []Session{testSession(), replacement}, want: "read"},
		{name: "before EOF", values: []Session{testSession(), testSession(), replacement}, want: "terminal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			loader := &sequencedSessionLoader{values: test.values}
			reader, _, err := newHeldAttachmentFixtureReader(t, fixture, loader, &heldAttachmentBody{data: bytes.Clone(fixture.wire), maximum: 9})
			if test.want == "constructor" {
				if reader != nil || attachmentReason(err) != domain.BrokerReasonStaleExecution {
					t.Fatalf("reader=%v err=%v", reader, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, len(payload)+1)
			n, readErr := reader.Read(buffer)
			if test.want == "read" {
				if n != 0 || attachmentReason(readErr) != domain.BrokerReasonStaleExecution {
					t.Fatalf("read=(%d,%v)", n, readErr)
				}
			} else {
				if n != len(payload) || readErr != nil || !bytes.Equal(buffer[:n], payload) {
					t.Fatalf("payload read=(%d,%v) match=%t", n, readErr, bytes.Equal(buffer[:n], payload))
				}
				n, readErr = reader.Read(buffer)
				if n != 0 || errors.Is(readErr, io.EOF) || attachmentReason(readErr) != domain.BrokerReasonStaleExecution {
					t.Fatalf("terminal read=(%d,%v)", n, readErr)
				}
			}
			_ = reader.Close()
		})
	}
}

func TestAttachmentFailureStreamIsStrictAndContentFree(t *testing.T) {
	denied, err := brokertransport.EncodeExecutionFailureV3(domain.BrokerReasonDenied)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		response   httpx.BoundedResponseStream
		wantReason domain.BrokerReason
	}{
		{name: "denied", response: attachmentFailureResponse(http.StatusForbidden, denied, ""), wantReason: domain.BrokerReasonDenied},
		{name: "correlated denied", response: attachmentFailureResponse(http.StatusForbidden, denied, "correlation-1"), wantReason: domain.BrokerReasonDenied},
		{name: "bad correlation", response: attachmentFailureResponse(http.StatusForbidden, denied, "bad correlation")},
		{name: "wrong media", response: httpx.BoundedResponseStream{Status: http.StatusForbidden, ContentType: "text/plain", Body: io.NopCloser(strings.NewReader("PRIVATE-BODY-CANARY"))}},
		{name: "encoded", response: httpx.BoundedResponseStream{Status: http.StatusForbidden, ContentType: "application/json", ContentEncoding: "gzip", Body: io.NopCloser(bytes.NewReader(denied))}},
		{name: "success status", response: attachmentFailureResponse(http.StatusCreated, denied, "correlation-1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := acceptedAttachmentFailureV3(test.response)
			if test.wantReason != "" {
				if reason, _ := brokercontract.Reason(err); reason != test.wantReason {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), "PRIVATE-BODY-CANARY") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func attachmentFailureResponse(status int, body []byte, correlation string) httpx.BoundedResponseStream {
	return httpx.BoundedResponseStream{Status: status, ContentType: "application/json", CorrelationID: correlation, Body: io.NopCloser(bytes.NewReader(body))}
}

func newHeldAttachmentFixtureReader(t *testing.T, fixture heldAttachmentFixture, loader SessionLoader, body io.ReadCloser) (*heldAttachmentReader, string, error) {
	t.Helper()
	client := &Client{config: Config{Session: loader}, now: func() time.Time { return time.Now() }}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	deadline, _ := ctx.Deadline()
	invocation := &heldAttachmentInvocation{client: client, definition: fixture.definition, selected: testSession(), ctx: ctx, deadline: deadline, cancel: cancel}
	return newHeldAttachmentReader(invocation, httpx.BoundedResponseStream{Status: http.StatusOK, ContentType: brokertransport.ExecutionStreamMediaTypeV3, CorrelationID: fixture.correlation, Body: body}, fixture.request)
}

func newHeldAttachmentFixture(t *testing.T, payload []byte) heldAttachmentFixture {
	t.Helper()
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok {
		t.Fatal("attachment definition missing")
	}
	request := attachmentRequestV3(definition, "request-1", testSession(), "PROJ-1", "200")
	snapshot, err := brokercontract.NewAttachmentSnapshotEvidenceV3(domain.BrokerJiraAttachmentSnapshotV3{
		IssueID: "100", IssueKey: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T00:00:00Z",
		AttachmentID: "200", ParentID: "100", Filename: "example.bin", MediaType: "application/octet-stream",
		Created: "2026-09-09T00:00:00Z", DeclaredSize: int64(len(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	correlation := "correlation-1"
	anchorSHA256 := heldAttachmentDigest("anchor")
	firstDecision := heldAttachmentDigest("decision-0")
	limits := definition.Definition.Limits
	core := domain.BrokerAttachmentManifestCoreV3{
		SchemaVersion: 3, FrameVersion: brokercontract.AttachmentFrameVersionV3, StreamID: "stream-1", CorrelationID: correlation,
		ArgumentsSHA256: mustAttachmentArgumentsSHA256(t, request.Arguments), AnchorSHA256: anchorSHA256, Snapshot: snapshot,
		ConsistencyProfile: domain.BrokerAttachmentConsistencyStepSnapshotV1,
		MaxDataFrames:      limits.MaxStreamChunks, MaxDecodedFrameBytes: limits.MaxStreamChunkBytes, MaxNativeBodyBytes: limits.MaxNativeBodyBytes,
		MaxMetadataItems: definition.MaxMetadataItems, MaxManifestLineBytes: definition.MaxManifestLineBytes, MaxDataLineBytes: definition.MaxDataLineBytes,
		MaxTerminalLineBytes: definition.MaxTerminalLineBytes, MaxFramedBytes: definition.MaxFramedResponseBytes,
		MaxJiraAttempts: definition.MaxJiraAttempts, MaxAuthenticationAttempts: definition.MaxAuthenticationAttempts, MaxDecisionAttempts: definition.MaxDecisionAttempts,
		MaxTotalHostOutboundAttempts: definition.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: definition.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: definition.MaxJiraResponseBytes, MaxAuthorityResponseBytes: definition.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: definition.MaxTotalHostResponseBytes,
		MaxOperationMillis: limits.MaxOperationMillis, MaxDecisionLeaseMillis: limits.MaxDecisionLeaseMillis,
	}
	coreSHA256, err := brokercontract.AttachmentManifestCoreSHA256V3(core)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := brokercontract.EncodeAttachmentManifestLineV3(domain.BrokerAttachmentManifestLineV3{Core: core, ManifestCoreSHA256: coreSHA256, ReleaseDecisionSHA256: firstDecision})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := brokercontract.AttachmentReleaseRootSHA256V3(anchorSHA256, coreSHA256)
	if err != nil {
		t.Fatal(err)
	}
	resourceSHA256, err := brokercontract.AttachmentResourcesSHA256V3(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	lines := [][]byte{manifest}
	cumulative := sha256.New()
	frameMaximum := int(brokercontract.MaxAttachmentDecodedFrameBytesV3)
	frames := 0
	if len(payload) > 0 {
		frames = (len(payload) + frameMaximum - 1) / frameMaximum
	}
	for index := 0; index < frames; index++ {
		start := index * frameMaximum
		end := min(start+frameMaximum, len(payload))
		frame := payload[start:end]
		decision := firstDecision
		if index > 0 {
			decision = heldAttachmentDigest(fmt.Sprintf("decision-%d", index))
		}
		_, _ = cumulative.Write(frame)
		payloadDigest := sha256.Sum256(frame)
		line, encodeErr := brokercontract.EncodeAttachmentDataLineV3(domain.BrokerAttachmentDataLineV3{
			SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData, StreamID: "stream-1", Index: index, Offset: int64(start),
			DecodedBytes: int64(len(frame)), PayloadBase64: base64.StdEncoding.EncodeToString(frame), PayloadSHA256: hex.EncodeToString(payloadDigest[:]),
			CumulativeBytes: int64(end), CumulativeSHA256: hex.EncodeToString(cumulative.Sum(nil)), ResourceSHA256: resourceSHA256,
			PriorReleaseSHA256: prior, ReleaseDecisionSHA256: decision,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		lines = append(lines, line)
		releaseStart := len(lines) - 1
		if index == 0 {
			releaseStart = 0
		}
		if index == frames-1 {
			terminal, terminalErr := brokercontract.EncodeAttachmentTerminalLineV3(domain.BrokerAttachmentTerminalLineV3{
				SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: "stream-1",
				ChunkCount: frames, TotalBytes: int64(len(payload)), DeclaredSize: int64(len(payload)), WholeSHA256: hex.EncodeToString(cumulative.Sum(nil)),
				PriorReleaseSHA256: prior, ReleaseDecisionSHA256: decision, EOFProven: true, Complete: true,
			})
			if terminalErr != nil {
				t.Fatal(terminalErr)
			}
			lines = append(lines, terminal)
		}
		if index < frames-1 {
			hashes := heldAttachmentLineHashes(t, lines[releaseStart:])
			prior, err = brokercontract.AttachmentReleaseReceiptSHA256V3(prior, decision, hashes)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if frames == 0 {
		terminal, terminalErr := brokercontract.EncodeAttachmentTerminalLineV3(domain.BrokerAttachmentTerminalLineV3{
			SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: "stream-1", WholeSHA256: brokercontract.AttachmentEmptyBodySHA256V3,
			PriorReleaseSHA256: prior, ReleaseDecisionSHA256: firstDecision, EOFProven: true, Complete: true,
		})
		if terminalErr != nil {
			t.Fatal(terminalErr)
		}
		lines = append(lines, terminal)
	}
	return heldAttachmentFixture{definition: definition, request: request, correlation: correlation, filename: snapshot.Filename, wire: bytes.Join(append(lines, nil), []byte{'\n'})}
}

func heldAttachmentLineHashes(t *testing.T, lines [][]byte) []string {
	t.Helper()
	result := make([]string, len(lines))
	for index, line := range lines {
		withLF := append(bytes.Clone(line), '\n')
		digest, err := brokercontract.AttachmentExactLineSHA256V3(withLF)
		clear(withLF)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = digest
	}
	return result
}

func heldAttachmentDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mustAttachmentArgumentsSHA256(t *testing.T, arguments domain.BrokerAttachmentArgumentsV3) string {
	t.Helper()
	digest, err := brokercontract.AttachmentArgumentsSHA256V3(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func attachmentReason(err error) domain.BrokerReason {
	reason, _ := brokercontract.Reason(err)
	return reason
}
