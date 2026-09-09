package brokerserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const auditBufferSize = 64

type AuditEvent struct {
	SchemaVersion int    `json:"schema_version"`
	CorrelationID string `json:"correlation_id"`
	Route         string `json:"route"`
	Operation     string `json:"operation,omitempty"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
	Timing        string `json:"timing"`
	RequestIndex  uint64 `json:"request_index"`
	ResponseBytes int64  `json:"response_bytes"`
	DroppedEvents uint64 `json:"dropped_events"`
	Complete      bool   `json:"complete"`
}

type Audit struct {
	guard   *CredentialGuard
	writer  io.Writer
	events  chan []byte
	done    chan struct{}
	closed  sync.Once
	sendMu  sync.RWMutex
	stopped bool
	healthy atomic.Bool
	dropped atomic.Uint64
	index   atomic.Uint64
}

type auditResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	correlation string
	operation   domain.BrokerOperationID
	reason      domain.BrokerReason
	credential  []byte
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	written, err := w.ResponseWriter.Write(body)
	w.bytes += int64(written)
	return written, err
}

func NewAudit(writer io.Writer, guard *CredentialGuard) (*Audit, error) {
	if writer == nil || guard == nil {
		return nil, domain.ErrUsage
	}
	audit := &Audit{writer: writer, guard: guard, events: make(chan []byte, auditBufferSize), done: make(chan struct{})}
	audit.healthy.Store(true)
	go audit.run()
	return audit, nil
}

func (a *Audit) Healthy() bool { return a != nil && a.healthy.Load() }

func (a *Audit) Wrap(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		captured := &auditResponseWriter{ResponseWriter: writer}
		if request != nil {
			if credential, err := workloadBearer(request); err == nil {
				captured.credential = credential
			}
		}
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				captured.status = http.StatusInternalServerError
				captured.reason = domain.BrokerReasonAuthorizationUnavailable
			}
			if captured.status == 0 {
				captured.status = http.StatusOK
			}
			correlation := captured.correlation
			if correlation == "" {
				correlation = freshAuditCorrelation()
			}
			event := AuditEvent{
				SchemaVersion: 1, CorrelationID: correlation, Route: route,
				Operation: string(captured.operation), Outcome: auditOutcome(captured.status), Reason: string(captured.reason),
				Timing: auditTiming(time.Since(started)), RequestIndex: a.index.Add(1), ResponseBytes: captured.bytes,
				DroppedEvents: a.dropped.Swap(0), Complete: true,
			}
			a.record(event, captured.credential)
			clear(captured.credential)
			if panicValue != nil {
				panic("Broker handler failed")
			}
		}()
		next.ServeHTTP(captured, request)
	})
}

func (a *Audit) record(event AuditEvent, requestCredential []byte) {
	if a == nil || !a.Healthy() || !validAuditEvent(event) {
		return
	}
	body, err := json.Marshal(event)
	if err != nil {
		a.healthy.Store(false)
		return
	}
	if a.guard.Check(app.BrokerExactReadResult{}, body, requestCredential) != nil {
		a.dropped.Add(event.DroppedEvents + 1)
		return
	}
	body = append(body, '\n')
	a.sendMu.RLock()
	defer a.sendMu.RUnlock()
	if a.stopped {
		return
	}
	select {
	case a.events <- body:
	default:
		a.dropped.Add(event.DroppedEvents + 1)
	}
}

func (a *Audit) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.closed.Do(func() {
		a.sendMu.Lock()
		a.stopped = true
		close(a.events)
		a.sendMu.Unlock()
	})
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Audit) run() {
	defer close(a.done)
	for body := range a.events {
		if !a.healthy.Load() {
			continue
		}
		if written, err := a.writer.Write(body); err != nil || written != len(body) {
			a.healthy.Store(false)
		}
	}
}

func validAuditEvent(event AuditEvent) bool {
	if event.SchemaVersion != 1 || event.CorrelationID == "" || len(event.CorrelationID) > 64 || event.RequestIndex == 0 || event.ResponseBytes < 0 || !event.Complete {
		return false
	}
	switch event.Route {
	case "data_execute", "data_execute_v2", "data_discovery_v3", "data_protocol", "data_cache_qualification", "data_unknown", "admin_health", "admin_readiness", "admin_unknown":
	default:
		return false
	}
	if event.Operation != "" && event.Operation != string(domain.BrokerOperationJiraIssueRead) && event.Operation != string(domain.BrokerOperationConfluencePageRead) {
		return false
	}
	switch event.Outcome {
	case "success", "rejected", "unavailable":
	default:
		return false
	}
	if event.Reason != "" {
		if ok, _ := brokerFailureReasonKnown(event.Reason); !ok {
			return false
		}
	}
	switch event.Timing {
	case "lt_10ms", "lt_100ms", "lt_1s", "lt_5s", "gte_5s":
		return true
	default:
		return false
	}
}

func brokerFailureReasonKnown(value string) (bool, domain.BrokerReason) {
	reason := domain.BrokerReason(value)
	switch reason {
	case domain.BrokerReasonMalformed, domain.BrokerReasonUnsupported, domain.BrokerReasonDenied, domain.BrokerReasonRevoked, domain.BrokerReasonCredentialExpired,
		domain.BrokerReasonGrantExpired, domain.BrokerReasonStaleExecution, domain.BrokerReasonStaleAuthority, domain.BrokerReasonDecisionExpired,
		domain.BrokerReasonAuthorizationUnavailable, domain.BrokerReasonUnsupportedConsistency, domain.BrokerReasonProposalClearanceRequired, domain.BrokerReasonOutcomeUnknown:
		return true, reason
	default:
		return false, ""
	}
}

func auditOutcome(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "success"
	case status >= 400 && status < 500:
		return "rejected"
	default:
		return "unavailable"
	}
}

func auditTiming(elapsed time.Duration) string {
	switch {
	case elapsed < 10*time.Millisecond:
		return "lt_10ms"
	case elapsed < 100*time.Millisecond:
		return "lt_100ms"
	case elapsed < time.Second:
		return "lt_1s"
	case elapsed < 5*time.Second:
		return "lt_5s"
	default:
		return "gte_5s"
	}
}

func freshAuditCorrelation() string {
	value := make([]byte, 18)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "unavailable-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func recordAuditCorrelation(writer http.ResponseWriter, value string) {
	if audit, ok := writer.(*auditResponseWriter); ok {
		audit.correlation = value
	}
}

func recordAuditOperation(writer http.ResponseWriter, value domain.BrokerOperationID) {
	if audit, ok := writer.(*auditResponseWriter); ok {
		audit.operation = value
	}
}

func recordAuditReason(writer http.ResponseWriter, value domain.BrokerReason) {
	if audit, ok := writer.(*auditResponseWriter); ok {
		audit.reason = value
	}
}

func recordAuditCredential(writer http.ResponseWriter, value []byte) {
	if audit, ok := writer.(*auditResponseWriter); ok {
		clear(audit.credential)
		audit.credential = bytes.Clone(value)
	}
}
