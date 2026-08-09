package compose

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
)

type overlapDetectingWriter struct {
	active  atomic.Int32
	overlap atomic.Bool
	mu      sync.Mutex
	bytes.Buffer
}

func (w *overlapDetectingWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	defer w.active.Add(-1)
	runtime.Gosched()
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.Write(p)
}

func (w *overlapDetectingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.String()
}

func TestCompositionSerializesSiblingClientsSharingTraceSink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/search":
			_, _ = io.WriteString(w, `{"results":[],"size":0,"_links":{}}`)
		case "/rest/api/2/search":
			_, _ = io.WriteString(w, `{"issues":[],"startAt":0,"maxResults":1,"total":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sink := &overlapDetectingWriter{}
	resolved := resolveOptions([]Option{WithTrace(sink)})
	confluence := confluenceadapter.New(server.URL, "token", "test", confluenceOptions(nil, resolved)...)
	jira := jiraadapter.New(server.URL, "token", "test", jiraOptions(nil, resolved)...)

	start := make(chan struct{})
	errors := make(chan error, 40)
	for range 20 {
		go func() {
			<-start
			_, _, err := confluence.Search(context.Background(), "type=page", 1, "")
			errors <- err
		}()
		go func() {
			<-start
			_, _, err := jira.Search(context.Background(), "project = X", nil, 1, "")
			errors <- err
		}()
	}
	close(start)
	for range 40 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if sink.overlap.Load() {
		t.Fatal("Confluence and Jira wrote to the composition trace sink concurrently")
	}
	trace := sink.String()
	if strings.Count(trace, "\n") != 80 || strings.Count(trace, "→ GET ") != 40 || strings.Count(trace, "← 200 ") != 40 {
		t.Fatalf("trace lines were lost or combined: %q", trace)
	}
}

func TestCompositionTraceKeepsTypedNilAndSeparateInvocationsSilentOrIsolated(t *testing.T) {
	var typedNil *bytes.Buffer
	if got := resolveOptions([]Option{WithTrace(typedNil)}).trace; got != nil {
		t.Fatalf("typed-nil trace retained as %#v", got)
	}
	if got := resolveOptions(nil).trace; got != nil {
		t.Fatalf("default composition trace = %#v, want nil", got)
	}

	var first, second bytes.Buffer
	firstResolved := resolveOptions([]Option{WithTrace(&first)})
	secondResolved := resolveOptions([]Option{WithTrace(&second)})
	_, _ = firstResolved.trace.Write([]byte("first\n"))
	_, _ = secondResolved.trace.Write([]byte("second\n"))
	if first.String() != "first\n" || second.String() != "second\n" {
		t.Fatalf("composition traces crossed invocation sinks: first=%q second=%q", first.String(), second.String())
	}
}
