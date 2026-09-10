package brokerserver

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

// attachmentPublisher owns wire-byte accounting, not authorization. The app
// supplies one authorized release; the handler commits only a successful flush.
// Its caller serializes use and transfers the line buffers; publish clears
// them on return. No other owner may read or mutate these buffers concurrently.
type attachmentPublisher struct {
	ctx         context.Context
	writer      http.ResponseWriter
	scanner     *AttachmentCredentialScanner
	correlation string
	now         func() time.Time
	started     bool
	wireBytes   int64
	failed      bool
}

func (p *attachmentPublisher) publish(lines [][]byte, deadline time.Time) ([]string, error) {
	defer func() {
		for _, line := range lines {
			clear(line)
		}
	}()
	if p == nil || p.ctx == nil || p.writer == nil || p.scanner == nil || p.now == nil || p.failed {
		return nil, domain.ErrCheckFailed
	}
	fail := func() ([]string, error) {
		p.failed = true
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		return nil, domain.ErrCheckFailed
	}
	if len(lines) == 0 || len(lines) > 3 || p.correlation == "" || len(p.correlation) > 64 ||
		p.scanner.CheckSerializedLine([]byte(p.correlation)) != nil {
		return fail()
	}
	for _, current := range []byte(p.correlation) {
		if current < 0x21 || current > 0x7e {
			return fail()
		}
	}
	if parent, ok := p.ctx.Deadline(); !ok {
		return fail()
	} else if parent.Before(deadline) {
		deadline = parent
	}
	current := func() bool { return p.ctx.Err() == nil && !deadline.IsZero() && p.now().Before(deadline) }
	if !current() {
		return fail()
	}
	// Inspect the complete proposed release before any of its bytes are sent.
	// The app's canonical codecs own semantic shape; this boundary additionally
	// forbids embedded wire delimiters and accounts for each extra LF explicitly.
	wire := make([][]byte, len(lines))
	defer func() {
		for _, line := range wire {
			clear(line)
		}
	}()
	digests := make([]string, len(lines))
	var releaseBytes int64
	for index, line := range lines {
		if len(line) == 0 || int64(len(line)) > brokercontract.MaxAttachmentDataLineBytesV3 || bytes.ContainsAny(line, "\r\n") {
			return fail()
		}
		wire[index] = append(line, '\n')
		if p.scanner.CheckSerializedLine(wire[index]) != nil {
			return fail()
		}
		var err error
		digests[index], err = brokercontract.AttachmentExactLineSHA256V3(wire[index])
		if err != nil {
			return fail()
		}
		releaseBytes += int64(len(wire[index]))
	}
	if p.wireBytes > brokercontract.MaxAttachmentFramedResponseBytesV3-releaseBytes || !current() {
		return fail()
	}
	controller := http.NewResponseController(p.writer)
	if controller.SetWriteDeadline(deadline) != nil || !current() {
		return fail()
	}
	if !p.started {
		header := p.writer.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Type", "application/x-ndjson")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-ATL-Correlation-ID", p.correlation)
		header.Del("Content-Length")
		header.Del("Content-Encoding")
		p.started = true
		p.writer.WriteHeader(http.StatusOK)
	}
	for _, line := range wire {
		if !current() {
			return fail()
		}
		written, err := p.writer.Write(line)
		if written > 0 && written <= len(line) {
			p.wireBytes += int64(written)
		}
		if err != nil || written != len(line) {
			return fail()
		}
	}
	if !current() || controller.Flush() != nil || !current() {
		return fail()
	}
	return digests, nil
}
