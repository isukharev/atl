package brokerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const (
	heldAttachmentReaderLabel = "Broker attachment stream reader"
	heldAttachmentReadBuffer  = 32 << 10
)

type heldAttachmentReader struct {
	readMu sync.Mutex
	closed atomic.Bool
	eof    atomic.Bool

	cleanupOnce sync.Once
	closeOnce   sync.Once
	closeErr    error
	terminalErr error

	client   *Client
	selected Session
	http     *httpx.Client
	ctx      context.Context
	deadline time.Time
	cancel   context.CancelFunc
	body     io.ReadCloser
	lines    attachmentLineReader

	request       domain.BrokerAttachmentRequestV3
	definition    domain.BrokerAttachmentOperationDefinitionV3
	manifest      domain.BrokerAttachmentManifestLineV3
	resourceSHA   string
	priorRelease  string
	decisionSHA   string
	releaseHashes []string
	firstRelease  bool
	nextIndex     int
	totalBytes    int64
	cumulative    hash.Hash
	terminalOnly  bool

	payload       []byte
	payloadOffset int
}

type attachmentLineReader struct {
	source   io.Reader
	pending  []byte
	scratch  []byte
	terminal error
}

func newHeldAttachmentReader(invocation *heldAttachmentInvocation, response httpx.BoundedResponseStream, request domain.BrokerAttachmentRequestV3) (*heldAttachmentReader, string, error) {
	reader := &heldAttachmentReader{
		client: invocation.client, selected: invocation.selected, http: invocation.http, ctx: invocation.ctx, deadline: invocation.deadline, cancel: invocation.cancel,
		body: response.Body, request: request, definition: invocation.definition, cumulative: sha256.New(), firstRelease: true,
	}
	invocation.selected = Session{}
	invocation.http = nil
	invocation.cancel = nil
	reader.lines = attachmentLineReader{source: reader.body, scratch: make([]byte, heldAttachmentReadBuffer)}
	line, lineSHA256, err := reader.lines.readLine(brokercontract.MaxAttachmentManifestLineBytesV3)
	if err != nil {
		return nil, "", reader.abort(attachmentStreamError(err))
	}
	manifest, decodeErr := brokercontract.DecodeAttachmentManifestLineV3(line)
	clear(line)
	if decodeErr != nil || !validHeldAttachmentManifest(manifest, request, invocation.definition, response.CorrelationID) {
		return nil, "", reader.abort(clientError(domain.ErrCheckFailed))
	}
	resourceSHA256, err := brokercontract.AttachmentResourcesSHA256V3(manifest.Core.Snapshot)
	if err != nil {
		return nil, "", reader.abort(clientError(domain.ErrCheckFailed))
	}
	priorRelease, err := brokercontract.AttachmentReleaseRootSHA256V3(manifest.Core.AnchorSHA256, manifest.ManifestCoreSHA256)
	if err != nil {
		return nil, "", reader.abort(clientError(domain.ErrCheckFailed))
	}
	reader.manifest = manifest
	reader.resourceSHA = resourceSHA256
	reader.priorRelease = priorRelease
	reader.decisionSHA = manifest.ReleaseDecisionSHA256
	reader.releaseHashes = []string{lineSHA256}
	if err := reader.client.requireCurrentSession(reader.selected); err != nil {
		return nil, "", reader.abort(err)
	}
	if err := reader.current(); err != nil {
		return nil, "", reader.abort(err)
	}
	return reader, manifest.Core.Snapshot.Filename, nil
}

func validHeldAttachmentManifest(value domain.BrokerAttachmentManifestLineV3, request domain.BrokerAttachmentRequestV3, definition domain.BrokerAttachmentOperationDefinitionV3, correlation string) bool {
	argumentsSHA256, err := brokercontract.AttachmentArgumentsSHA256V3(request.Arguments)
	if err != nil {
		return false
	}
	core := value.Core
	limits := definition.Definition.Limits
	return core.CorrelationID == correlation && core.ArgumentsSHA256 == argumentsSHA256 &&
		core.Snapshot.IssueKey == request.Arguments.IssueKey && core.Snapshot.AttachmentID == request.Arguments.AttachmentID &&
		core.MaxDataFrames == limits.MaxStreamChunks && core.MaxDecodedFrameBytes == limits.MaxStreamChunkBytes && core.MaxNativeBodyBytes == limits.MaxNativeBodyBytes &&
		core.MaxMetadataItems == definition.MaxMetadataItems && core.MaxManifestLineBytes == definition.MaxManifestLineBytes && core.MaxDataLineBytes == definition.MaxDataLineBytes &&
		core.MaxTerminalLineBytes == definition.MaxTerminalLineBytes && core.MaxFramedBytes == definition.MaxFramedResponseBytes && core.MaxJiraAttempts == definition.MaxJiraAttempts &&
		core.MaxAuthenticationAttempts == definition.MaxAuthenticationAttempts && core.MaxDecisionAttempts == definition.MaxDecisionAttempts && core.MaxTotalHostOutboundAttempts == definition.MaxTotalHostOutboundAttempts &&
		core.MaxCommandHostOutboundAttempts == definition.MaxCommandHostOutboundAttempts && core.MaxJiraResponseBytes == definition.MaxJiraResponseBytes && core.MaxAuthorityResponseBytes == definition.MaxAuthorityResponseBytes &&
		core.MaxTotalHostResponseBytes == definition.MaxTotalHostResponseBytes && core.MaxOperationMillis == limits.MaxOperationMillis && core.MaxDecisionLeaseMillis == limits.MaxDecisionLeaseMillis
}

func (r *heldAttachmentReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r == nil {
		return 0, clientError(domain.ErrCheckFailed)
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if r.closed.Load() {
		return 0, r.closedResult()
	}
	// Cancellation/deadline also gates bytes already buffered from a validated
	// frame; it must not wait until the next network or terminal read.
	if err := r.current(); err != nil {
		return 0, r.failLocked(err)
	}
	for r.payloadOffset == len(r.payload) {
		clear(r.payload)
		r.payload = nil
		r.payloadOffset = 0
		if err := r.readNextLocked(); err != nil {
			return 0, err
		}
		if r.eof.Load() {
			return 0, io.EOF
		}
	}
	if r.closed.Load() {
		return 0, r.closedResult()
	}
	n := copy(buffer, r.payload[r.payloadOffset:])
	r.payloadOffset += n
	return n, nil
}

func (r *heldAttachmentReader) readNextLocked() error {
	line, lineSHA256, err := r.lines.readLine(brokercontract.MaxAttachmentDataLineBytesV3)
	if err != nil {
		return r.failLocked(attachmentStreamError(err))
	}
	defer clear(line)
	data, dataErr := brokercontract.DecodeAttachmentDataLineV3(line)
	if dataErr == nil {
		return r.acceptDataLocked(data, lineSHA256)
	}
	terminal, terminalErr := brokercontract.DecodeAttachmentTerminalLineV3(line)
	if terminalErr != nil {
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	return r.acceptTerminalLocked(terminal, lineSHA256)
}

func (r *heldAttachmentReader) acceptDataLocked(data domain.BrokerAttachmentDataLineV3, lineSHA256 string) error {
	if r.terminalOnly || data.StreamID != r.manifest.Core.StreamID || data.ResourceSHA256 != r.resourceSHA || data.Index != r.nextIndex || data.Offset != r.totalBytes ||
		data.Index >= brokercontract.MaxAttachmentDataFramesV3 || r.totalBytes >= r.manifest.Core.Snapshot.DeclaredSize {
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	if r.firstRelease {
		if len(r.releaseHashes) != 1 || data.PriorReleaseSHA256 != r.priorRelease || data.ReleaseDecisionSHA256 != r.decisionSHA {
			return r.failLocked(clientError(domain.ErrCheckFailed))
		}
		r.releaseHashes = append(r.releaseHashes, lineSHA256)
	} else {
		expectedHashes := 1
		if r.nextIndex == 1 {
			expectedHashes = 2
		}
		if r.terminalOnly || len(r.releaseHashes) != expectedHashes {
			return r.failLocked(clientError(domain.ErrCheckFailed))
		}
		receipt, err := brokercontract.AttachmentReleaseReceiptSHA256V3(r.priorRelease, r.decisionSHA, r.releaseHashes)
		if err != nil || data.PriorReleaseSHA256 != receipt || data.ReleaseDecisionSHA256 == r.decisionSHA {
			return r.failLocked(clientError(domain.ErrCheckFailed))
		}
		r.priorRelease = data.PriorReleaseSHA256
		r.decisionSHA = data.ReleaseDecisionSHA256
		r.releaseHashes = []string{lineSHA256}
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(data.PayloadBase64)
	if err != nil || int64(len(payload)) != data.DecodedBytes {
		clear(payload)
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	_, _ = r.cumulative.Write(payload)
	r.totalBytes += int64(len(payload))
	cumulativeSHA256 := hex.EncodeToString(r.cumulative.Sum(nil))
	if r.totalBytes != data.CumulativeBytes || r.totalBytes > r.manifest.Core.Snapshot.DeclaredSize || cumulativeSHA256 != data.CumulativeSHA256 {
		clear(payload)
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	if err := r.client.requireCurrentSession(r.selected); err != nil {
		clear(payload)
		return r.failLocked(err)
	}
	if err := r.current(); err != nil {
		clear(payload)
		return r.failLocked(err)
	}
	r.payload = payload
	r.nextIndex++
	r.terminalOnly = data.DecodedBytes < brokercontract.MaxAttachmentDecodedFrameBytesV3 || data.Index == brokercontract.MaxAttachmentDataFramesV3-1 || r.totalBytes == r.manifest.Core.Snapshot.DeclaredSize
	r.firstRelease = false
	return nil
}

func (r *heldAttachmentReader) acceptTerminalLocked(terminal domain.BrokerAttachmentTerminalLineV3, lineSHA256 string) error {
	if terminal.StreamID != r.manifest.Core.StreamID || terminal.PriorReleaseSHA256 != r.priorRelease || terminal.ReleaseDecisionSHA256 != r.decisionSHA ||
		terminal.ChunkCount != r.nextIndex || terminal.TotalBytes != r.totalBytes || terminal.DeclaredSize != r.manifest.Core.Snapshot.DeclaredSize ||
		terminal.WholeSHA256 != hex.EncodeToString(r.cumulative.Sum(nil)) || !terminal.EOFProven || !terminal.Complete {
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	if r.nextIndex == 0 {
		if len(r.releaseHashes) != 1 || terminal.TotalBytes != 0 || terminal.WholeSHA256 != brokercontract.AttachmentEmptyBodySHA256V3 {
			return r.failLocked(clientError(domain.ErrCheckFailed))
		}
	} else if !r.terminalOnly {
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	r.releaseHashes = append(r.releaseHashes, lineSHA256)
	if _, err := brokercontract.AttachmentReleaseReceiptSHA256V3(r.priorRelease, r.decisionSHA, r.releaseHashes); err != nil {
		return r.failLocked(clientError(domain.ErrCheckFailed))
	}
	if err := r.lines.requireEOF(); err != nil {
		return r.failLocked(attachmentStreamError(err))
	}
	if err := r.client.requireCurrentSession(r.selected); err != nil {
		return r.failLocked(err)
	}
	if err := r.current(); err != nil {
		return r.failLocked(err)
	}
	if err := r.cleanupLocked(); err != nil {
		return r.failLocked(err)
	}
	r.eof.Store(true)
	r.closed.Store(true)
	return nil
}

func (r *heldAttachmentReader) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.cancel != nil {
			r.cancel()
		}
		r.readMu.Lock()
		defer r.readMu.Unlock()
		if !r.eof.Load() && r.terminalErr == nil {
			r.terminalErr = clientError(context.Canceled)
		}
		r.closeErr = r.cleanupLocked()
	})
	return r.closeErr
}

func (r *heldAttachmentReader) abort(err error) error {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	return r.failLocked(err)
}

func (r *heldAttachmentReader) failLocked(err error) error {
	if r.terminalErr == nil {
		r.terminalErr = attachmentStreamError(err)
	}
	r.closed.Store(true)
	if r.cancel != nil {
		r.cancel()
	}
	if cleanupErr := r.cleanupLocked(); cleanupErr != nil && r.terminalErr == nil {
		r.terminalErr = cleanupErr
	}
	return r.terminalErr
}

func (r *heldAttachmentReader) cleanupLocked() error {
	r.cleanupOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.body != nil {
			if err := r.body.Close(); err != nil {
				r.closeErr = attachmentStreamError(err)
			}
		}
		clear(r.payload)
		r.payload = nil
		r.lines.clear()
		r.selected.Clear()
		if r.http != nil {
			r.http.ClearCredential()
			r.http.CloseIdleConnections()
		}
		r.releaseHashes = nil
		r.request = domain.BrokerAttachmentRequestV3{}
		r.definition = domain.BrokerAttachmentOperationDefinitionV3{}
		r.manifest = domain.BrokerAttachmentManifestLineV3{}
		r.resourceSHA = ""
		r.priorRelease = ""
		r.decisionSHA = ""
		r.cumulative = nil
	})
	return r.closeErr
}

func (r *heldAttachmentReader) closedResult() error {
	if r.eof.Load() {
		return io.EOF
	}
	if r.terminalErr != nil {
		return r.terminalErr
	}
	return clientError(context.Canceled)
}

func (r *heldAttachmentReader) current() error {
	if err := r.ctx.Err(); err != nil {
		return clientError(err)
	}
	if !r.client.now().Before(r.deadline) {
		return clientError(context.DeadlineExceeded)
	}
	return nil
}

func (*heldAttachmentReader) String() string   { return heldAttachmentReaderLabel }
func (*heldAttachmentReader) GoString() string { return heldAttachmentReaderLabel }
func (*heldAttachmentReader) Format(state fmt.State, verb rune) {
	value := heldAttachmentReaderLabel
	if verb == 'q' {
		value = strconv.Quote(value)
	}
	_, _ = io.WriteString(state, value)
}

func (r *attachmentLineReader) readLine(maximum int64) ([]byte, string, error) {
	if maximum <= 0 {
		return nil, "", domain.ErrCheckFailed
	}
	line := make([]byte, 0, int(min(int64(heldAttachmentReadBuffer), maximum)))
	for {
		if index := bytes.IndexByte(r.pending, '\n'); index >= 0 {
			if int64(len(line)+index) > maximum {
				clear(line)
				return nil, "", domain.ErrCheckFailed
			}
			line = append(line, r.pending[:index]...)
			consumed := index + 1
			copy(r.pending, r.pending[consumed:])
			clear(r.pending[len(r.pending)-consumed:])
			r.pending = r.pending[:len(r.pending)-consumed]
			emitted := append(bytes.Clone(line), '\n')
			digest, err := brokercontract.AttachmentExactLineSHA256V3(emitted)
			clear(emitted)
			if err != nil || len(line) == 0 {
				clear(line)
				return nil, "", domain.ErrCheckFailed
			}
			return line, digest, nil
		}
		if int64(len(line)+len(r.pending)) > maximum {
			clear(line)
			return nil, "", domain.ErrCheckFailed
		}
		line = append(line, r.pending...)
		clear(r.pending)
		r.pending = r.pending[:0]
		if r.terminal != nil {
			terminal := r.terminal
			clear(line)
			if errors.Is(terminal, io.EOF) {
				return nil, "", io.ErrUnexpectedEOF
			}
			return nil, "", terminal
		}
		n, err := r.source.Read(r.scratch)
		if n > 0 {
			r.pending = append(r.pending, r.scratch[:n]...)
			clear(r.scratch[:n])
		}
		if err != nil {
			r.terminal = err
		}
		if n == 0 && err == nil {
			clear(line)
			return nil, "", io.ErrNoProgress
		}
	}
}

func (r *attachmentLineReader) requireEOF() error {
	if len(r.pending) != 0 {
		return domain.ErrCheckFailed
	}
	if r.terminal != nil {
		if errors.Is(r.terminal, io.EOF) {
			return nil
		}
		return r.terminal
	}
	n, err := r.source.Read(r.scratch)
	if n > 0 {
		clear(r.scratch[:n])
		return domain.ErrCheckFailed
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrNoProgress
}

func (r *attachmentLineReader) clear() {
	clear(r.pending)
	clear(r.scratch)
	r.pending = nil
	r.scratch = nil
	r.source = nil
	r.terminal = nil
}
