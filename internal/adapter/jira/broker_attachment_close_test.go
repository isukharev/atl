package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the private handle through its real budgeted HTTP body. The HTTP
// owner separately proves the exact Read/Close scheduling and accounting edge.
func TestBrokerJiraAttachmentHandleCloseCancelsBodyRead(t *testing.T) {
	var response []byte
	var bodyCalls atomic.Int32
	_, reader := newBrokerAttachmentTLSJira(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case brokerAttachmentMetadataURI:
			_, _ = writer.Write(response)
		case brokerAttachmentBodyURI:
			bodyCalls.Add(1)
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("abc"))
			_ = http.NewResponseController(writer).Flush()
			<-request.Context().Done()
		default:
			t.Error("unexpected attachment fixture route")
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	response = brokerAttachmentResponse(t, brokerAttachmentTestIssueID, brokerAttachmentTestIssueKey, "PROJ", brokerAttachmentTestUpdated, []brokerAttachmentTestRecord{brokerAttachmentRecord(brokerAttachmentRelativeURI)})
	setupCtx, setupCancel, _ := brokerAttachmentContext(t, 2, 4<<20)
	defer setupCancel()
	snapshot, err := reader.QualifyBrokerJiraAttachment(setupCtx, brokerAttachmentTestIssueKey, brokerAttachmentTestID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := reader.PrepareBrokerJiraAttachment(setupCtx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	bodyCtx, bodyCancel, budget := brokerAttachmentContext(t, 1, 1<<20)
	defer bodyCancel()
	stream, err := handle.Open(bodyCtx, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var first [3]byte
	if n, readErr := io.ReadFull(stream, first[:]); n != len(first) || readErr != nil || string(first[:]) != "abc" {
		t.Fatalf("initial read = (%d, %v), body_match=%t", n, readErr, string(first[:]) == "abc")
	}
	type readResult struct {
		n   int
		err error
	}
	readDone := make(chan readResult, 1)
	readJoined := make(chan struct{})
	go func() {
		defer close(readJoined)
		var next [1]byte
		n, readErr := stream.Read(next[:])
		readDone <- readResult{n, readErr}
	}()
	t.Cleanup(func() {
		bodyCancel()
		select {
		case <-readJoined:
		case <-time.After(time.Second):
			t.Error("attachment reader did not stop after cancellation")
		}
	})
	select {
	case result := <-readDone:
		t.Fatalf("body read unexpectedly completed before close: (%d, %v)", result.n, result.err)
	case <-time.After(20 * time.Millisecond):
	}
	closeDone := make(chan error, 1)
	closeJoined := make(chan struct{})
	go func() {
		defer close(closeJoined)
		closeDone <- handle.Close()
	}()
	t.Cleanup(func() {
		bodyCancel()
		select {
		case <-closeJoined:
		case <-time.After(time.Second):
			t.Error("attachment closer did not stop after cancellation")
		}
	})
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("handle close: %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("handle close did not cancel and join the body read")
	}
	select {
	case result := <-readDone:
		if result.n != 0 || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("closed body read = (%d, %v), want cancellation", result.n, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("body read did not return after handle close")
	}
	if usage := budget.Usage(); usage.Attempts != 1 || usage.ResponseBytes != 3 {
		t.Fatalf("body usage = %+v, want one attempt and three bytes", usage)
	}
	if bodyCalls.Load() != 1 {
		t.Fatalf("body calls = %d, want one", bodyCalls.Load())
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("reader close after handle close: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("repeated handle close: %v", err)
	}
}
