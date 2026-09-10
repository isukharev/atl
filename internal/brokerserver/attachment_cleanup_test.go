package brokerserver

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type observedAttachmentPort struct {
	*jiraadapter.Jira
	windows [][]byte
	closes  int
}

func (p *observedAttachmentPort) PrepareBrokerJiraAttachment(ctx context.Context, expected domain.BrokerJiraAttachmentSnapshotV3) (domain.BrokerJiraAttachmentOpenHandleV3, error) {
	handle, err := p.Jira.PrepareBrokerJiraAttachment(ctx, expected)
	if err != nil {
		return nil, err
	}
	return &observedAttachmentHandle{BrokerJiraAttachmentOpenHandleV3: handle, owner: p}, nil
}

type observedAttachmentHandle struct {
	domain.BrokerJiraAttachmentOpenHandleV3
	owner *observedAttachmentPort
}

func (h *observedAttachmentHandle) Open(ctx context.Context, deadline time.Time) (io.ReadCloser, error) {
	body, err := h.BrokerJiraAttachmentOpenHandleV3.Open(ctx, deadline)
	if err != nil {
		return nil, err
	}
	return &observedAttachmentBody{ReadCloser: body, owner: h.owner}, nil
}

type observedAttachmentBody struct {
	io.ReadCloser
	owner *observedAttachmentPort
}

func (b *observedAttachmentBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if n > 0 {
		// Keep an alias to the actual app candidate backing bytes, not a copy.
		b.owner.windows = append(b.owner.windows, buffer[:n])
	}
	return n, err
}

func (b *observedAttachmentBody) Close() error {
	b.owner.closes++
	return b.ReadCloser.Close()
}

type attachmentPanicReader struct {
	entered        bool
	calls, panicAt int
}

func (r *attachmentPanicReader) Read(buffer []byte) (int, error) {
	r.calls++
	if r.calls == r.panicAt {
		r.entered = true
		panic("synthetic failure after candidate transfer")
	}
	for index := range buffer {
		buffer[index] = byte(index + 1)
	}
	return len(buffer), nil
}

func TestAttachmentCandidateUnwindClearsTransferredWindowAndClosesSource(t *testing.T) {
	for _, test := range []struct {
		name    string
		panicAt int
	}{
		{name: "first candidate", panicAt: 1},
		{name: "candidate after prior release", panicAt: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkAttachmentCandidateUnwind(t, test.panicAt)
		})
	}
}

func checkAttachmentCandidateUnwind(t *testing.T, panicAt int) {
	t.Helper()
	fixture := newAttachmentChainFixture(t, bytes.Repeat([]byte{'x'}, (1<<20)+128))
	observer := &observedAttachmentPort{Jira: fixture.jiraReader}
	authorizer, ok := fixture.handler.authenticator.(domain.BrokerAttachmentAuthorizerV3)
	if !ok {
		t.Fatal("fixture authority lacks the attachment contract")
	}
	service, err := app.NewBrokerJiraAttachmentStreamService(authorizer, app.BrokerJiraAttachmentStreamReader{Backend: fixture.verified.Backend, Reader: observer})
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler.attachments = service
	failure := &attachmentPanicReader{panicAt: panicAt}
	fixture.handler.random = failure
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = runAttachmentComponentChain(t, fixture)
	}()
	if !failure.entered || failure.calls != panicAt || recovered != "Broker handler failed" || len(observer.windows) == 0 || observer.closes != 1 {
		t.Fatalf("unwind=%v observed windows=%d closes=%d", recovered, len(observer.windows), observer.closes)
	}
	for _, window := range observer.windows {
		if !bytes.Equal(window, make([]byte, len(window))) {
			t.Fatal("transferred native candidate survived exceptional unwind")
		}
	}
}
