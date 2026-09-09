//go:build !windows

package brokerserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type projectPageProcessCompletionCounters struct {
	brokerNegotiate atomic.Int32
	brokerDiscovery atomic.Int32
	brokerExecute   atomic.Int32
}

type projectPageProcessCompletionSnapshot struct {
	brokerNegotiate, brokerDiscovery, brokerExecute int32
}

func (c *projectPageProcessCompletionCounters) snapshot() projectPageProcessCompletionSnapshot {
	return projectPageProcessCompletionSnapshot{
		brokerNegotiate: c.brokerNegotiate.Load(),
		brokerDiscovery: c.brokerDiscovery.Load(),
		brokerExecute:   c.brokerExecute.Load(),
	}
}

func (f *projectPageProcessFixture) observeDataHandler(data http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case brokertransport.DiscoveryNegotiatePathV3:
			f.counters.brokerNegotiate.Add(1)
		case brokertransport.DiscoveryPathV3:
			f.counters.brokerDiscovery.Add(1)
		case brokertransport.ExecutePathV2:
			f.counters.brokerExecute.Add(1)
		}
		observed := newProjectPageProcessObservedWriter(writer)
		if request.URL.Path != brokertransport.ExecutePathV2 || !f.options.replaceAfterBuffer {
			data.ServeHTTP(observed, request)
			f.completeDataRequest(request.URL.Path, observed)
			return
		}
		buffered := newProjectPageProcessBufferedWriter(observed)
		data.ServeHTTP(buffered, request)
		f.bufferHookOnce.Do(func() {
			if err := f.writeSession(projectPageProcessReplacementCredential); err != nil {
				f.violationf("replace session after buffered response: %v", err)
			}
		})
		if err := buffered.publish(observed); err != nil {
			f.violationf("publish buffered Broker response: %v", err)
		}
		f.completeDataRequest(request.URL.Path, observed)
	})
}

func (f *projectPageProcessFixture) completeDataRequest(path string, observed *projectPageProcessObservedWriter) {
	f.observeClosedFailure(path, observed)
	switch path {
	case brokertransport.DiscoveryNegotiatePathV3:
		f.completions.brokerNegotiate.Add(1)
	case brokertransport.DiscoveryPathV3:
		f.completions.brokerDiscovery.Add(1)
	case brokertransport.ExecutePathV2:
		f.completions.brokerExecute.Add(1)
	default:
		return
	}
	select {
	case f.completionWake <- struct{}{}:
	default:
	}
}

func (f *projectPageProcessFixture) observeClosedFailure(path string, observed *projectPageProcessObservedWriter) {
	if observed.overflow || observed.statusCode() < http.StatusMultipleChoices {
		return
	}
	var failure brokertransport.Failure
	var err error
	switch path {
	case brokertransport.DiscoveryNegotiatePathV3, brokertransport.DiscoveryPathV3:
		failure, err = brokertransport.DecodeDiscoveryFailureV3(observed.body)
	case brokertransport.ExecutePathV2:
		failure, err = brokertransport.DecodeExecutionFailureV2(observed.body)
	default:
		return
	}
	if err != nil {
		return
	}
	f.failuresMu.Lock()
	f.failureReasons[failure.Reason]++
	f.failuresMu.Unlock()
}

func (f *projectPageProcessFixture) waitForCompletions(want projectPageProcessCompletionSnapshot) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 3*time.Second)
	defer cancel()
	if got, ok := f.awaitCompletions(ctx, want); !ok {
		f.t.Fatalf("project-page handler completion boundary not reached: got=%+v want=%+v witness=%s", got, want, f.diagnosticWitness())
	}
}

func (f *projectPageProcessFixture) awaitCompletions(ctx context.Context, want projectPageProcessCompletionSnapshot) (projectPageProcessCompletionSnapshot, bool) {
	for {
		got := f.completions.snapshot()
		if got == want {
			return got, true
		}
		if got.brokerNegotiate > want.brokerNegotiate || got.brokerDiscovery > want.brokerDiscovery || got.brokerExecute > want.brokerExecute {
			return got, false
		}
		select {
		case <-ctx.Done():
			return f.completions.snapshot(), false
		case <-f.completionWake:
		}
	}
}

func (f *projectPageProcessFixture) diagnosticWitness() string {
	counts := f.counters.snapshot()
	completions := f.completions.snapshot()
	f.failuresMu.Lock()
	reasons := make([]string, 0, len(f.failureReasons))
	for reason, count := range f.failureReasons {
		reasons = append(reasons, fmt.Sprintf("%s=%d", reason, count))
	}
	f.failuresMu.Unlock()
	sort.Strings(reasons)
	f.violationsMu.Lock()
	violationCount := len(f.violations)
	f.violationsMu.Unlock()
	return fmt.Sprintf(
		"routes=(negotiate=%d discovery=%d execute=%d) phases=(authentication=%d discovery=%d admission=%d qualification=%d operation=%d) backend=(project=%d identity=%d business=%d direct=%d) completions=%+v closed_reasons=%v violations=%d",
		counts.brokerNegotiate, counts.brokerDiscovery, counts.brokerExecute,
		counts.authentication, counts.discovery, counts.admission, counts.qualification, counts.operation,
		counts.jiraProject, counts.jiraIdentity, counts.jiraBusiness, counts.directJira,
		completions, reasons, violationCount,
	)
}

type projectPageProcessObservedWriter struct {
	underlying http.ResponseWriter
	status     int
	body       []byte
	overflow   bool
}

func newProjectPageProcessObservedWriter(underlying http.ResponseWriter) *projectPageProcessObservedWriter {
	return &projectPageProcessObservedWriter{underlying: underlying}
}

func (w *projectPageProcessObservedWriter) Header() http.Header { return w.underlying.Header() }
func (w *projectPageProcessObservedWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.underlying.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.underlying.WriteHeader(status)
}
func (w *projectPageProcessObservedWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.underlying.Write(body)
	if w.status >= http.StatusMultipleChoices && !w.overflow && n > 0 {
		remaining := int(brokertransport.MaxTransportFailureBytes) - len(w.body)
		if n > remaining {
			w.overflow = true
		} else {
			w.body = append(w.body, body[:n]...)
		}
	}
	return n, err
}
func (w *projectPageProcessObservedWriter) Unwrap() http.ResponseWriter { return w.underlying }
func (w *projectPageProcessObservedWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func TestProjectPageProcessCompletionWitnessForwardsBeforeHandlerReturn(t *testing.T) {
	fixture := &projectPageProcessFixture{
		t: t, completionWake: make(chan struct{}, 1), failureReasons: make(map[domain.BrokerReason]int),
	}
	failure, err := brokertransport.EncodeExecutionFailureV2(domain.BrokerReasonDenied)
	if err != nil {
		t.Fatal(err)
	}
	published := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("observed handler goroutine did not stop")
		}
	})
	handler := fixture.observeDataHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		if _, writeErr := writer.Write(failure); writeErr != nil {
			fixture.violationf("write synthetic failure: %v", writeErr)
		}
		if flushErr := http.NewResponseController(writer).Flush(); flushErr != nil {
			fixture.violationf("flush synthetic failure: %v", flushErr)
		}
		close(published)
		<-release
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, brokertransport.ExecutePathV2, nil)
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("handler did not publish through the observer")
	}
	if recorder.Code != http.StatusForbidden || !bytes.Equal(recorder.Body.Bytes(), failure) || !recorder.Flushed {
		t.Fatalf("forwarded status=%d body_match=%t flushed=%t", recorder.Code, bytes.Equal(recorder.Body.Bytes(), failure), recorder.Flushed)
	}
	if got := fixture.completions.snapshot(); got != (projectPageProcessCompletionSnapshot{}) {
		t.Fatalf("completion recorded before handler return: %+v", got)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelWait()
	if got, ok := fixture.awaitCompletions(waitCtx, projectPageProcessCompletionSnapshot{brokerExecute: 1}); ok || got != (projectPageProcessCompletionSnapshot{}) {
		t.Fatalf("completion wait before return = (%+v, %t)", got, ok)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after release")
	}
	completeCtx, cancelComplete := context.WithTimeout(t.Context(), time.Second)
	defer cancelComplete()
	if got, ok := fixture.awaitCompletions(completeCtx, projectPageProcessCompletionSnapshot{brokerExecute: 1}); !ok || got.brokerExecute != 1 {
		t.Fatalf("completion after return = (%+v, %t)", got, ok)
	}
	witness := fixture.diagnosticWitness()
	if !strings.Contains(witness, "closed_reasons=[denied=1]") || strings.Contains(witness, string(failure)) {
		t.Fatalf("witness did not retain only the closed reason: %s", witness)
	}
	fixture.assertNoViolations()
}

func TestProjectPageProcessFailureWitnessOmitsUndecodableResponseBytes(t *testing.T) {
	fixture := &projectPageProcessFixture{
		t: t, completionWake: make(chan struct{}, 1), failureReasons: make(map[domain.BrokerReason]int),
	}
	const canary = "SYNTHETIC-WITNESS-BODY-CANARY"
	response := httptest.NewRecorder()
	observed := newProjectPageProcessObservedWriter(response)
	observed.WriteHeader(http.StatusInternalServerError)
	if _, err := observed.Write([]byte(canary)); err != nil {
		t.Fatal(err)
	}
	fixture.completeDataRequest(brokertransport.ExecutePathV2, observed)
	if witness := fixture.diagnosticWitness(); strings.Contains(witness, canary) || !strings.Contains(witness, "closed_reasons=[]") {
		t.Fatalf("unsafe failure witness: %s", witness)
	}
}
