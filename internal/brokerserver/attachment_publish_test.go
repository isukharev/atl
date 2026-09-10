package brokerserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type attachmentPublicationWriter struct {
	deadlineResponseWriter
	flushes    int
	flushErr   error
	shortWrite bool
	afterFlush func()
}

func (w *attachmentPublicationWriter) Write(line []byte) (int, error) {
	if w.shortWrite {
		return w.body.Write(line[:len(line)-1])
	}
	return w.body.Write(line)
}

func (w *attachmentPublicationWriter) FlushError() error {
	w.flushes++
	if w.afterFlush != nil {
		w.afterFlush()
	}
	return w.flushErr
}

func newAttachmentTestPublisher(t *testing.T) (*attachmentPublisher, *attachmentPublicationWriter, time.Time) {
	t.Helper()
	guard, err := NewCredentialGuard([]byte("configured-publish-canary"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	scanner, err := guard.NewAttachmentCredentialScanner([]byte("held-publish-canary"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scanner.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	writer := &attachmentPublicationWriter{}
	return &attachmentPublisher{ctx: ctx, writer: writer, scanner: scanner, correlation: "synthetic-correlation", now: time.Now}, writer, time.Now().Add(time.Second)
}

// These short lines isolate the publication boundary. App/contract fixtures
// separately prove complete semantic manifest/data/terminal authorization.
func TestAttachmentPublisherFlushesExactLinesBeforeReturningReceiptInputs(t *testing.T) {
	publisher, writer, deadline := newAttachmentTestPublisher(t)
	lines := [][]byte{[]byte(`{"kind":"manifest"}`), []byte(`{"kind":"data"}`), []byte(`{"kind":"terminal"}`)}
	expected := make([]string, len(lines))
	var complete bytes.Buffer
	for index, line := range lines {
		wire := append(bytes.Clone(line), '\n')
		expected[index], _ = brokercontract.AttachmentExactLineSHA256V3(wire)
		complete.Write(wire)
	}
	writer.Header().Set("Content-Length", "1")
	writer.Header().Set("Content-Encoding", "gzip")
	digests, err := publisher.publish(lines, deadline)
	if err != nil || len(digests) != 3 || writer.flushes != 1 || !bytes.Equal(writer.body.Bytes(), complete.Bytes()) {
		t.Fatalf("publication: hashes=%d flushes=%d wire_match=%t err=%v", len(digests), writer.flushes, bytes.Equal(writer.body.Bytes(), complete.Bytes()), err)
	}
	for index, line := range lines {
		if digests[index] != expected[index] || !bytes.Equal(line, make([]byte, len(line))) {
			t.Fatalf("receipt or consumed buffer mismatch at line %d", index)
		}
	}
	if writer.status != http.StatusOK || writer.Header().Get("Content-Type") != "application/x-ndjson" || writer.Header().Get("Content-Length") != "" || writer.Header().Get("Content-Encoding") != "" || writer.Header().Get("X-ATL-Correlation-ID") != publisher.correlation || !writer.writeDeadline.Equal(deadline) || publisher.wireBytes != int64(complete.Len()) {
		t.Fatal("publication metadata/deadline/accounting mismatch")
	}
}

func TestAttachmentPublisherPreflightsWholeReleaseBeforeHeaders(t *testing.T) {
	for name, lines := range map[string][][]byte{
		"empty":                        nil,
		"four lines":                   {[]byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`)},
		"embedded delimiter":           {[]byte("{}\n{}")},
		"held credential in last line": {[]byte(`{}`), []byte(`{}`), []byte(`{"value":"held-publish-canary"}`)},
		"configured credential":        {[]byte(`{"value":"configured-publish-canary"}`)},
		"canonical JSON line overflow": {bytes.Repeat([]byte{'x'}, int(brokercontract.MaxAttachmentDataLineBytesV3)+1)},
	} {
		t.Run(name, func(t *testing.T) {
			publisher, writer, deadline := newAttachmentTestPublisher(t)
			if hashes, err := publisher.publish(lines, deadline); hashes != nil || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("invalid release = %v, %v", hashes, err)
			}
			if publisher.started || writer.status != 0 || writer.body.Len() != 0 || writer.flushes != 0 {
				t.Fatal("rejected release reached response publication")
			}
		})
	}
}

func TestAttachmentPublisherFailureCannotResumeOrReturnCommitEvidence(t *testing.T) {
	for _, failure := range []string{"short write", "flush failure", "expired after flush", "canceled after flush"} {
		t.Run(failure, func(t *testing.T) {
			publisher, writer, deadline := newAttachmentTestPublisher(t)
			ctx, cancel := context.WithCancel(publisher.ctx)
			defer cancel()
			publisher.ctx = ctx
			switch failure {
			case "short write":
				writer.shortWrite = true
			case "flush failure":
				writer.flushErr = io.ErrClosedPipe
			case "expired after flush":
				writer.afterFlush = func() { publisher.now = func() time.Time { return deadline } }
			case "canceled after flush":
				writer.afterFlush = cancel
			}
			if hashes, err := publisher.publish([][]byte{[]byte(`{}`)}, deadline); hashes != nil || err == nil || !publisher.failed {
				t.Fatalf("failed release returned commit evidence: %v, %v", hashes, err)
			}
			written, flushes := writer.body.Len(), writer.flushes
			if hashes, err := publisher.publish([][]byte{[]byte(`{}`)}, time.Now().Add(time.Second)); hashes != nil || err == nil || writer.body.Len() != written || writer.flushes != flushes {
				t.Fatal("failed publisher resumed")
			}
		})
	}
}

func TestAttachmentPublisherBoundsDeadlineHeadersAndCumulativeWireBytes(t *testing.T) {
	t.Run("exact JSON cap plus delimiter", func(t *testing.T) {
		publisher, writer, deadline := newAttachmentTestPublisher(t)
		line := bytes.Repeat([]byte{'x'}, int(brokercontract.MaxAttachmentDataLineBytesV3))
		if hashes, err := publisher.publish([][]byte{line}, deadline); err != nil || len(hashes) != 1 || int64(writer.body.Len()) != brokercontract.MaxAttachmentDataLineBytesV3+1 {
			t.Fatalf("maximum emitted line: hashes=%d bytes=%d err=%v", len(hashes), writer.body.Len(), err)
		}
	})
	t.Run("clip caller deadline", func(t *testing.T) {
		publisher, writer, _ := newAttachmentTestPublisher(t)
		parent, _ := publisher.ctx.Deadline()
		if _, err := publisher.publish([][]byte{[]byte(`{}`)}, parent.Add(time.Minute)); err != nil || !writer.writeDeadline.Equal(parent) {
			t.Fatalf("caller deadline was not retained: %v", err)
		}
	})
	for _, correlation := range []string{"", "bad\r\nheader", "held-publish-canary"} {
		publisher, writer, deadline := newAttachmentTestPublisher(t)
		publisher.correlation = correlation
		if _, err := publisher.publish([][]byte{[]byte(`{}`)}, deadline); err == nil || writer.status != 0 {
			t.Fatal("invalid or protected correlation was published")
		}
	}
	t.Run("exact remaining wire cap", func(t *testing.T) {
		publisher, writer, deadline := newAttachmentTestPublisher(t)
		publisher.wireBytes = brokercontract.MaxAttachmentFramedResponseBytesV3 - 3
		if _, err := publisher.publish([][]byte{[]byte(`{}`)}, deadline); err != nil || publisher.wireBytes != brokercontract.MaxAttachmentFramedResponseBytesV3 {
			t.Fatalf("exact wire cap failed: %v", err)
		}
		if _, err := publisher.publish([][]byte{[]byte(`{}`)}, deadline); err == nil || writer.body.Len() != 3 || writer.flushes != 1 {
			t.Fatal("cumulative wire overflow reached writer")
		}
	})
}
