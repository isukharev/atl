//go:build !windows

package cli

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	guardedTransportDropApplyResponse = "drop-apply-response"
	guardedTransportReplaceObserver   = "replace-observer"
)

type guardedTransportProxyCounts struct {
	Routes      map[string]int
	Credentials map[string]int
	Operations  map[domain.BrokerOperationID]int
	Dropped     int
	Replaced    int
}

type guardedTransportProxy struct {
	target          string
	mode            string
	replaceObserver func() error
	upstream        *http.Client
	server          *httptest.Server

	mu         sync.Mutex
	counts     guardedTransportProxyCounts
	violations []string
}

func TestSelectedGuardedCommentTransportFaults(t *testing.T) {
	binary := buildSelectedGuardedCommentATL(t)

	t.Run("definitive Jira rejection publishes not applied", func(t *testing.T) {
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{postMode: "reject"})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)
		assertGuardedTransportValuePrivacy(t, fixture, preview)

		before := fixture.snapshot()
		applyRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun)
		apply := requireGuardedTransportTerminalComment(t, applyRun, "not_applied")
		if apply.OperationTicket != preview.OperationTicket || apply.ProposalHash != preview.ProposalHash ||
			!apply.WriteAttempted || !apply.Complete || apply.Reconciled || apply.CommentID != "" {
			t.Fatalf("definitive Jira rejection result=%+v", apply)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 2, 2,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 1, Inventory: 2, Post: 1}))

		before = fixture.snapshot()
		outcomeRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, outcomeRun)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationNotApplied)
		if !outcome.Complete || outcome.Reconciled || outcome.ResultSHA256 == "" {
			t.Fatalf("durable not-applied observation=%+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})

	t.Run("lost Jira reply publishes unknown and fences duplicate", func(t *testing.T) {
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{postMode: "lose-hidden"})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)
		assertGuardedTransportValuePrivacy(t, fixture, preview)

		before := fixture.snapshot()
		applyRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun)
		apply := requireGuardedTransportTerminalComment(t, applyRun, "outcome_unknown")
		if apply.OperationTicket != preview.OperationTicket || apply.ProposalHash != preview.ProposalHash ||
			!apply.WriteAttempted || apply.Complete || !apply.Reconciled || apply.CommentID != "" {
			t.Fatalf("lost Jira reply result=%+v", apply)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 3, 2,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 2, Inventory: 3, Post: 1}))

		before = fixture.snapshot()
		outcomeRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, outcomeRun)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationOutcomeUnknown)
		if outcome.Complete || outcome.Reconciled || outcome.ResultSHA256 != "" {
			t.Fatalf("durable unknown observation=%+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))

		before = fixture.snapshot()
		duplicateRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, duplicateRun)
		requireGuardedProcessFailure(t, duplicateRun, preview.OperationTicket, guardedProcessBody)
		if duplicateRun.exitCode != exitCheckFailed {
			t.Fatalf("durable unknown duplicate exit=%d want=%d", duplicateRun.exitCode, exitCheckFailed)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 1, 0,
			guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})

	t.Run("completed apply response loss remains observable", func(t *testing.T) {
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)
		actualOrigin := guardedTransportSetBrokerOrigin(t, fixture, "")
		proxy := newGuardedTransportProxy(t, fixture, actualOrigin, guardedTransportDropApplyResponse, nil)
		guardedTransportSetBrokerOrigin(t, fixture, proxy.server.URL)
		assertGuardedTransportValuePrivacy(t, fixture, preview, actualOrigin, proxy.server.URL)

		before := fixture.snapshot()
		applyRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun, actualOrigin, proxy.server.URL)
		requireGuardedProcessFailure(t, applyRun, preview.OperationTicket, guardedProcessBody)
		if applyRun.exitCode != exitCheckFailed {
			t.Fatalf("lost completed apply response exit=%d want=%d", applyRun.exitCode, exitCheckFailed)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 3, 2,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 2, Inventory: 3, Post: 1}))
		proxy.assertCounts(t, guardedTransportProxyCounts{
			Routes:      map[string]int{brokertransport.DiscoveryNegotiatePathV2: 1, brokertransport.DiscoveryPathV2: 1, brokertransport.ExecutePath: 1},
			Credentials: map[string]int{"writer": 3}, Operations: map[domain.BrokerOperationID]int{domain.BrokerOperationJiraCommentApply: 1}, Dropped: 1,
		})
		// Observe disk-recovered state, not merely the still-running daemon's
		// memory, after the completed response was lost to the workload.
		killGuardedProcessDaemon(t, daemon, nil, fixture)
		before = fixture.snapshot()
		daemon = startGuardedProcessDaemon(t, binary, fixture)
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("admin", 1, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))

		before = fixture.snapshot()
		outcomeRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, outcomeRun, actualOrigin, proxy.server.URL)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationApplied)
		if !outcome.Complete || !outcome.Reconciled || outcome.ResultSHA256 == "" {
			t.Fatalf("completed apply was not durably observable: %+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))
		proxy.assertCounts(t, guardedTransportProxyCounts{
			Routes:      map[string]int{brokertransport.DiscoveryNegotiatePathV2: 2, brokertransport.DiscoveryPathV2: 2, brokertransport.ExecutePath: 2},
			Credentials: map[string]int{"writer": 3, "observer": 3},
			Operations:  map[domain.BrokerOperationID]int{domain.BrokerOperationJiraCommentApply: 1, domain.BrokerOperationOutcomeLookup: 1}, Dropped: 1,
		})
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})

	t.Run("observer replacement after buffering suppresses outcome", func(t *testing.T) {
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)
		actualOrigin := guardedTransportSetBrokerOrigin(t, fixture, "")
		applyRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun, actualOrigin)
		apply := requireGuardedProcessCommentSuccess(t, applyRun, "apply", "applied")
		if !apply.WriteAttempted || !apply.Complete || !apply.Reconciled {
			t.Fatalf("applied setup result=%+v", apply)
		}

		replacementBody, err := guardedTransportSessionBody("replacement")
		if err != nil {
			t.Fatal(err)
		}
		proxy := newGuardedTransportProxy(t, fixture, actualOrigin, guardedTransportReplaceObserver, func() error {
			return safepath.WriteFileAtomicPrivate(fixture.observerSession, replacementBody, 0o600)
		})
		guardedTransportSetBrokerOrigin(t, fixture, proxy.server.URL)
		assertGuardedTransportValuePrivacy(t, fixture, preview, actualOrigin, proxy.server.URL)

		before := fixture.snapshot()
		outcomeRun := runGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		assertGuardedTransportRunPrivacy(t, fixture, outcomeRun, actualOrigin, proxy.server.URL)
		requireGuardedProcessFailure(t, outcomeRun, preview.OperationTicket, guardedProcessBody)
		if outcomeRun.exitCode != exitCheckFailed {
			t.Fatalf("post-buffer observer replacement exit=%d want=%d", outcomeRun.exitCode, exitCheckFailed)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))
		proxy.assertCounts(t, guardedTransportProxyCounts{
			Routes:      map[string]int{brokertransport.DiscoveryNegotiatePathV2: 1, brokertransport.DiscoveryPathV2: 1, brokertransport.ExecutePath: 1},
			Credentials: map[string]int{"observer": 3}, Operations: map[domain.BrokerOperationID]int{domain.BrokerOperationOutcomeLookup: 1}, Replaced: 1,
		})
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})
}

func newGuardedTransportProxy(t *testing.T, fixture *guardedProcessFixture, target, mode string, replaceObserver func() error) *guardedTransportProxy {
	t.Helper()
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme != "https" || targetURL.Host == "" || targetURL.Path != "" || targetURL.RawQuery != "" || targetURL.Fragment != "" {
		t.Fatal("invalid fixed Broker proxy target")
	}
	upstream := brokerProcessTLSClient(t, fixture.identity)
	upstream.Timeout = 8 * time.Second
	upstream.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if transport, ok := upstream.Transport.(*http.Transport); ok {
		transport.DisableKeepAlives = true
		transport.Proxy = nil
	}
	proxy := &guardedTransportProxy{
		target: target, mode: mode, replaceObserver: replaceObserver, upstream: upstream,
		counts: guardedTransportProxyCounts{Routes: map[string]int{}, Credentials: map[string]int{}, Operations: map[domain.BrokerOperationID]int{}},
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(proxy.serve))
	server.EnableHTTP2 = false
	server.TLS = &tls.Config{Certificates: []tls.Certificate{fixture.identity}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	server.StartTLS()
	proxy.server = server
	t.Cleanup(server.Close)
	return proxy
}

func (p *guardedTransportProxy) serve(writer http.ResponseWriter, request *http.Request) {
	maximum := guardedTransportRouteMaximum(request.Method, request.URL.Path, request.URL.RawQuery)
	credential := guardedTransportCredentialKind(request.Header.Get("Authorization"))
	if maximum == 0 || credential == "" || request.Header.Get("Content-Type") != "application/json" {
		p.violation("proxy rejected request envelope")
		http.Error(writer, "closed proxy failure", http.StatusBadRequest)
		return
	}
	body, err := readGuardedProcessBody(request.Body, maximum)
	if err != nil {
		p.violation("proxy rejected bounded request body")
		http.Error(writer, "closed proxy failure", http.StatusBadRequest)
		return
	}
	var operation domain.BrokerOperationID
	var executionRequest domain.BrokerRequest
	if request.URL.Path == brokertransport.ExecutePath {
		executionRequest, err = brokercontract.DecodeRequestV1(body)
		if err != nil {
			p.violation("proxy rejected execute request")
			http.Error(writer, "closed proxy failure", http.StatusBadRequest)
			return
		}
		operation = executionRequest.Operation
	}
	p.recordRequest(request.URL.Path, credential, operation)
	forward, err := http.NewRequestWithContext(request.Context(), http.MethodPost, p.target+request.URL.Path, bytes.NewReader(body))
	if err != nil {
		p.violation("proxy could not create fixed-origin request")
		http.Error(writer, "closed proxy failure", http.StatusBadGateway)
		return
	}
	forward.Close = true
	forward.Header.Set("Authorization", request.Header.Get("Authorization"))
	forward.Header.Set("Content-Type", "application/json")
	response, err := p.upstream.Do(forward)
	if err != nil {
		p.violation("proxy fixed-origin request failed")
		http.Error(writer, "closed proxy failure", http.StatusBadGateway)
		return
	}
	responseMaximum := guardedTransportResponseMaximum(request.URL.Path, operation)
	responseBody, readErr := readGuardedProcessBody(response.Body, responseMaximum)
	closeErr := response.Body.Close()
	if responseMaximum == 0 || readErr != nil || closeErr != nil {
		p.violation("proxy rejected bounded response body")
		http.Error(writer, "closed proxy failure", http.StatusBadGateway)
		return
	}
	if p.mode == guardedTransportDropApplyResponse && operation == domain.BrokerOperationJiraCommentApply {
		result, decodeErr := brokercontract.DecodeJiraCommentResultV1(responseBody)
		if response.StatusCode != http.StatusOK || !guardedTransportValidCorrelation(response.Header.Get("X-ATL-Correlation-ID")) || decodeErr != nil ||
			brokercontract.ValidateJiraCommentResultForV1(result, executionRequest) != nil || result.Status != "applied" || !result.WriteAttempted || !result.Complete || !result.Reconciled {
			p.violation("proxy did not receive a complete applied result")
			http.Error(writer, "closed proxy failure", http.StatusBadGateway)
			return
		}
		connection, _, hijackErr := http.NewResponseController(writer).Hijack()
		if hijackErr != nil {
			p.violation("proxy could not close the workload connection")
			http.Error(writer, "closed proxy failure", http.StatusBadGateway)
			return
		}
		p.mu.Lock()
		p.counts.Dropped++
		p.mu.Unlock()
		_ = connection.Close()
		return
	}
	if p.mode == guardedTransportReplaceObserver && operation == domain.BrokerOperationOutcomeLookup {
		outcome, decodeErr := brokercontract.DecodeOperationOutcomeV1(responseBody)
		if response.StatusCode != http.StatusOK || !guardedTransportValidCorrelation(response.Header.Get("X-ATL-Correlation-ID")) || decodeErr != nil ||
			outcome.Phase != domain.BrokerOperationApplied || !outcome.Complete || !outcome.Reconciled || outcome.ResultSHA256 == "" || p.replaceObserver == nil {
			p.violation("proxy did not buffer a complete applied outcome")
			http.Error(writer, "closed proxy failure", http.StatusBadGateway)
			return
		}
		if err := p.replaceObserver(); err != nil {
			p.violation("proxy could not replace observer session")
			http.Error(writer, "closed proxy failure", http.StatusBadGateway)
			return
		}
		p.mu.Lock()
		p.counts.Replaced++
		p.mu.Unlock()
	}
	for _, header := range []string{"Content-Type", "Cache-Control", "X-ATL-Correlation-ID"} {
		for _, value := range response.Header.Values(header) {
			writer.Header().Add(header, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	if _, err := writer.Write(responseBody); err != nil {
		p.violation("proxy could not publish bounded response")
	}
}

func guardedTransportRouteMaximum(method, path, rawQuery string) int64 {
	if method != http.MethodPost || rawQuery != "" {
		return 0
	}
	switch path {
	case brokertransport.DiscoveryNegotiatePathV2:
		return brokertransport.MaxDiscoveryNegotiationBytesV2
	case brokertransport.DiscoveryPathV2:
		return brokercontract.MaxDiscoveryV2Bytes
	case brokertransport.ExecutePath:
		return brokercontract.MaxEnvelopeBytes
	default:
		return 0
	}
}

func guardedTransportResponseMaximum(path string, operation domain.BrokerOperationID) int64 {
	switch path {
	case brokertransport.DiscoveryNegotiatePathV2:
		return brokertransport.MaxDiscoveryNegotiationBytesV2
	case brokertransport.DiscoveryPathV2:
		return brokercontract.MaxDiscoveryV2Bytes
	case brokertransport.ExecutePath:
		definition, ok := brokercontract.Definition(operation, brokercontract.OperationVersion)
		if ok {
			return definition.Limits.MaxResponseBytes
		}
	}
	return 0
}

func guardedTransportValidCorrelation(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func guardedTransportCredentialKind(header string) string {
	switch header {
	case "Bearer " + guardedProcessWriterCredential:
		return "writer"
	case "Bearer " + guardedProcessObserverCredential:
		return "observer"
	default:
		return ""
	}
}

func (p *guardedTransportProxy) recordRequest(path, credential string, operation domain.BrokerOperationID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts.Routes[path]++
	p.counts.Credentials[credential]++
	if operation != "" {
		p.counts.Operations[operation]++
	}
}

func (p *guardedTransportProxy) violation(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.violations = append(p.violations, message)
}

func (p *guardedTransportProxy) assertCounts(t *testing.T, want guardedTransportProxyCounts) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.violations) != 0 {
		t.Fatalf("guarded transport proxy violations=%v", p.violations)
	}
	if !reflect.DeepEqual(p.counts, want) {
		t.Fatalf("guarded transport proxy counts=%+v want=%+v", p.counts, want)
	}
}

func guardedTransportSetBrokerOrigin(t *testing.T, fixture *guardedProcessFixture, replacement string) string {
	t.Helper()
	path := filepath.Join(fixture.clientRoot, "config.json")
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > 64<<10 {
		t.Fatal("read bounded guarded client config")
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal("decode guarded client config")
	}
	broker, ok := config["broker"].(map[string]any)
	origin, originOK := broker["base_url"].(string)
	if !ok || !originOK || origin == "" {
		t.Fatal("guarded client config omitted Broker origin")
	}
	if replacement == "" {
		return origin
	}
	broker["base_url"] = replacement
	encoded, err := json.Marshal(config)
	if err != nil || len(encoded) > 64<<10 {
		t.Fatal("encode bounded guarded client config")
	}
	if err := safepath.WriteFileAtomicPrivate(path, encoded, 0o600); err != nil {
		t.Fatal("replace guarded client Broker origin")
	}
	return origin
}

func guardedTransportSessionBody(kind string) ([]byte, error) {
	credential, execution, epoch := guardedProcessSession(kind)
	if credential == "" || execution == "" || epoch == "" {
		return nil, fmt.Errorf("unknown guarded transport session")
	}
	return json.Marshal(map[string]any{
		"schema_version": 1, "credential": credential, "execution_id": execution,
		"execution_epoch": epoch, "authority_revision": "revision-1",
	})
}

func requireGuardedTransportTerminalComment(t *testing.T, run guardedProcessResult, status string) guardedProcessCommentResult {
	t.Helper()
	if run.exitCode != exitCheckFailed || run.err == nil || run.stdout == "" || run.stderr == "" {
		t.Fatalf("terminal guarded comment exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	var result guardedProcessCommentResult
	decodeGuardedProcessJSON(t, []byte(run.stdout), &result)
	if result.SchemaVersion != 1 || result.Operation != "jira.comment.apply" || result.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 ||
		result.Mode != "apply" || result.Status != status || result.ArgumentsSHA256 == "" || result.OperationTicket == "" || result.ProposalHash == "" ||
		result.NativeCandidateSHA256 == "" || result.VersionEvidenceSHA256 == "" {
		t.Fatalf("terminal guarded comment result=%+v", result)
	}
	return result
}

func assertGuardedTransportRunPrivacy(t *testing.T, fixture *guardedProcessFixture, run guardedProcessResult, origins ...string) {
	t.Helper()
	assertGuardedProcessContentFree(t, run, append([]string{
		guardedProcessBody, fixture.clientRoot, fixture.writerSession, fixture.observerSession, fixture.bodyPath,
		fixture.hostConfigPath, fixture.authority.URL, fixture.jira.URL, fixture.dataOrigin,
	}, origins...)...)
}

func assertGuardedTransportValuePrivacy(t *testing.T, fixture *guardedProcessFixture, value any, origins ...string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assertGuardedTransportRunPrivacy(t, fixture, guardedProcessResult{stdout: string(body)}, origins...)
}
