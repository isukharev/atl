//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const guardedProcessOutputLimit = 256 << 10

type guardedProcessCommentResult struct {
	SchemaVersion         int    `json:"schema_version"`
	Operation             string `json:"operation"`
	QualificationProfile  string `json:"qualification_profile"`
	ArgumentsSHA256       string `json:"arguments_sha256"`
	OperationTicket       string `json:"operation_ticket"`
	Mode                  string `json:"mode"`
	Status                string `json:"status"`
	ProposalHash          string `json:"proposal_hash"`
	NativeCandidateSHA256 string `json:"native_candidate_sha256"`
	VersionEvidenceSHA256 string `json:"version_evidence_sha256"`
	CommentID             string `json:"comment_id,omitempty"`
	WriteAttempted        bool   `json:"write_attempted"`
	Complete              bool   `json:"complete"`
	Reconciled            bool   `json:"reconciled"`
}

type guardedProcessOutcomeResult struct {
	SchemaVersion        int                         `json:"schema_version"`
	Operation            string                      `json:"operation"`
	QualificationProfile string                      `json:"qualification_profile"`
	OperationTicket      string                      `json:"operation_ticket"`
	TicketSHA256         string                      `json:"ticket_sha256"`
	Phase                domain.BrokerOperationPhase `json:"phase"`
	ObservedAtMillis     int64                       `json:"observed_at_millis"`
	ResultSHA256         string                      `json:"result_sha256,omitempty"`
	Complete             bool                        `json:"complete"`
	Reconciled           bool                        `json:"reconciled"`
}

type guardedProcessResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func TestSelectedGuardedCommentDaemonProcessOracle(t *testing.T) {
	// This build deliberately precedes every authority server, session lifetime,
	// journal reservation, and operation clock used below.
	binary := buildSelectedGuardedCommentATL(t)

	t.Run("initialize preview apply outcome drain restart", func(t *testing.T) {
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)

		before := fixture.snapshot()
		previewRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", fixture.bodyPath)
		t.Logf("preview phases: %+v", fixture.snapshot())
		fixture.assertNoViolations(t)
		preview := requireGuardedProcessCommentSuccess(t, previewRun, "preview", "proposed")
		fixture.approveGuardedProcessProposal(t, preview)
		if preview.OperationTicket == "" || preview.ProposalHash == "" || preview.WriteAttempted || !preview.Complete || preview.Reconciled {
			t.Fatalf("preview result violated the durable zero-write contract: %+v", preview)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentPreview, 1, 1, 1, 0,
			guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))

		before = fixture.snapshot()
		applyRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		apply := requireGuardedProcessCommentSuccess(t, applyRun, "apply", "applied")
		if apply.OperationTicket != preview.OperationTicket || apply.ProposalHash != preview.ProposalHash || apply.CommentID != "10" ||
			!apply.WriteAttempted || !apply.Complete || !apply.Reconciled {
			t.Fatalf("apply result violated the exact-one-POST contract: %+v", apply)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 3, 2,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 2, Inventory: 3, Post: 1}))

		before = fixture.snapshot()
		outcomeRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationApplied)
		if !outcome.Complete || !outcome.Reconciled || outcome.ResultSHA256 == "" {
			t.Fatalf("applied outcome omitted durable reconciliation proof: %+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))

		stopGuardedProcessDaemon(t, daemon, fixture)
		before = fixture.snapshot()
		daemon = startGuardedProcessDaemon(t, binary, fixture)
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("admin", 1, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))

		before = fixture.snapshot()
		reopenedRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		reopened := requireGuardedProcessOutcomeSuccess(t, reopenedRun, preview.OperationTicket, domain.BrokerOperationApplied)
		if !reopened.Complete || !reopened.Reconciled || reopened.ResultSHA256 != outcome.ResultSHA256 {
			t.Fatalf("reopened outcome changed durable terminal evidence: before=%+v after=%+v", outcome, reopened)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))

		t.Run("closed denials and binding mismatches", func(t *testing.T) {
			assertGuardedProcessClosedRows(t, binary, fixture, preview)
		})
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})

	t.Run("SIGKILL after durable Admit reopens not applied", func(t *testing.T) {
		barrier := newGuardedProcessBarrier()
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{blockApplyOperationCall: 2, operationBarrier: barrier})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)

		before := fixture.snapshot()
		apply := startGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		waitGuardedProcessBarrier(t, barrier, "post-Admit authorization")
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 2, 1,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 1, Inventory: 2}))
		killGuardedProcessDaemon(t, daemon, barrier, fixture)
		applyRun := waitGuardedProcessCLI(t, apply, 8*time.Second)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun)
		requireGuardedProcessFailure(t, applyRun, preview.OperationTicket, guardedProcessBody)

		before = fixture.snapshot()
		daemon = startGuardedProcessDaemon(t, binary, fixture)
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("admin", 1, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
		before = fixture.snapshot()
		outcomeRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationNotApplied)
		if !outcome.Complete || outcome.Reconciled || outcome.ResultSHA256 != "" {
			t.Fatalf("post-Admit recovery was not a definitive no-dispatch outcome: %+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})

	t.Run("SIGKILL after durable Claim reopens unknown and fences duplicate", func(t *testing.T) {
		barrier := newGuardedProcessBarrier()
		fixture := newGuardedProcessFixture(t, guardedProcessFixtureOptions{postBarrier: barrier})
		initializeGuardedProcessJournal(t, binary, fixture)
		daemon := startGuardedProcessDaemon(t, binary, fixture)
		preview := previewGuardedProcessComment(t, binary, fixture)

		before := fixture.snapshot()
		apply := startGuardedProcessCLI(t, binary, fixture.clientEnvironment(),
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		waitGuardedProcessBarrier(t, barrier, "post-Claim Jira POST")
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 2, 2,
			guardedProcessJiraCounts{Qualification: 1, Actor: 2, Issue: 1, Inventory: 2, Post: 1}))
		killGuardedProcessDaemon(t, daemon, barrier, fixture)
		applyRun := waitGuardedProcessCLI(t, apply, 8*time.Second)
		assertGuardedTransportRunPrivacy(t, fixture, applyRun)
		requireGuardedProcessFailure(t, applyRun, preview.OperationTicket, guardedProcessBody)

		before = fixture.snapshot()
		daemon = startGuardedProcessDaemon(t, binary, fixture)
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("admin", 1, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))

		before = fixture.snapshot()
		outcomeRun := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
		outcome := requireGuardedProcessOutcomeSuccess(t, outcomeRun, preview.OperationTicket, domain.BrokerOperationOutcomeUnknown)
		if outcome.Complete || outcome.Reconciled || outcome.ResultSHA256 != "" {
			t.Fatalf("post-Claim recovery did not preserve ambiguity: %+v", outcome)
		}
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))

		// Keep the original proposal observable despite the accepted POST. A
		// refusal must come from durable unknown ownership, not incidental Jira
		// inventory/update drift; the fixture still retains its one actual POST.
		fixture.hideAcceptedPostFromReadback()
		before = fixture.snapshot()
		duplicate := fixture.runCLI(t, binary,
			"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
			"--apply", "--expected-proposal-hash", preview.ProposalHash, "--operation-ticket", preview.OperationTicket)
		requireGuardedProcessFailure(t, duplicate, preview.OperationTicket, guardedProcessBody)
		fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 1, 0,
			guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))
		stopGuardedProcessDaemon(t, daemon, fixture)
		fixture.assertNoViolations(t)
	})
}

func assertGuardedProcessClosedRows(t *testing.T, binary string, fixture *guardedProcessFixture, preview guardedProcessCommentResult) {
	t.Helper()
	unknownTicket := strings.Repeat("f", 64)
	if unknownTicket == preview.OperationTicket {
		t.Fatal("synthetic unknown ticket collided with the issued ticket")
	}
	before := fixture.snapshot()
	unknown := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "outcome", "--operation-ticket", unknownTicket)
	requireGuardedProcessFailure(t, unknown, unknownTicket, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("observer", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))

	fixture.writeSession(t, fixture.observerSession, "foreign")
	before = fixture.snapshot()
	foreign := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "outcome", "--operation-ticket", preview.OperationTicket)
	requireGuardedProcessFailure(t, foreign, preview.OperationTicket, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("foreign", 3, 1, domain.BrokerOperationOutcomeLookup, 1, 1, 1, 0, guardedProcessJiraCounts{}))
	if unknown.exitCode != foreign.exitCode || unknown.stderr != foreign.stderr {
		t.Fatalf("unknown and foreign ticket refusals differed: unknown=%d/%q foreign=%d/%q", unknown.exitCode, unknown.stderr, foreign.exitCode, foreign.stderr)
	}
	fixture.writeSession(t, fixture.observerSession, "observer")
	bindingPreview := previewGuardedProcessComment(t, binary, fixture)

	changedBodyPath := filepath.Join(fixture.clientRoot, "changed-comment.wiki")
	writeBrokerProcessFile(t, fixture.clientRoot, "changed-comment.wiki", []byte("different synthetic body\n"))
	before = fixture.snapshot()
	changedBody := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", changedBodyPath,
		"--apply", "--expected-proposal-hash", bindingPreview.ProposalHash, "--operation-ticket", bindingPreview.OperationTicket)
	requireGuardedProcessFailure(t, changedBody, bindingPreview.OperationTicket, "different synthetic body")
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 1, 0,
		guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))

	fixture.writeSession(t, fixture.writerSession, "replacement")
	before = fixture.snapshot()
	replacement := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "add", guardedProcessIssueKey, "--from-file", fixture.bodyPath,
		"--apply", "--expected-proposal-hash", bindingPreview.ProposalHash, "--operation-ticket", bindingPreview.OperationTicket)
	requireGuardedProcessFailure(t, replacement, bindingPreview.OperationTicket, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("replacement", 3, 1, domain.BrokerOperationJiraCommentApply, 1, 1, 1, 0,
		guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))
	fixture.writeSession(t, fixture.writerSession, "writer")

	fixture.setAdmissionDenial(domain.BrokerOperationJiraCommentPreview, domain.BrokerReasonUnsupportedConsistency)
	before = fixture.snapshot()
	denied := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", fixture.bodyPath)
	requireGuardedProcessFailure(t, denied, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentPreview, 1, 0, 0, 0, guardedProcessJiraCounts{}))
	fixture.setAdmissionDenial(domain.BrokerOperationJiraCommentPreview, "")

	fixture.setDiscoveryAccess(domain.BrokerOperationJiraCommentPreview, domain.BrokerDiscoveryAccessUnavailable)
	before = fixture.snapshot()
	unavailable := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", fixture.bodyPath)
	requireGuardedProcessFailure(t, unavailable, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 2, 1, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
	fixture.setDiscoveryAccess(domain.BrokerOperationJiraCommentPreview, "")

	if err := os.WriteFile(fixture.writerSession, []byte("}{"), 0o600); err != nil {
		t.Fatal(err)
	}
	before = fixture.snapshot()
	missingSession := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", fixture.bodyPath)
	requireGuardedProcessFailure(t, missingSession, guardedProcessBody)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("", 0, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
	fixture.writeSession(t, fixture.writerSession, "writer")

	whitespacePath := filepath.Join(fixture.clientRoot, "whitespace-comment.wiki")
	writeBrokerProcessFile(t, fixture.clientRoot, "whitespace-comment.wiki", []byte(" \n\t"))
	before = fixture.snapshot()
	whitespace := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", whitespacePath)
	requireGuardedProcessFailure(t, whitespace, "whitespace-comment")
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("", 0, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
}

func expectedGuardedProcessDelta(authentication string, authenticationCalls, discoveryCalls int, operation domain.BrokerOperationID, admission, qualification, final, proposal int, jira guardedProcessJiraCounts) guardedProcessCountSnapshot {
	return guardedProcessCountSnapshot{
		Authentication: guardedAuthenticationCounts(authentication, authenticationCalls), Discovery: discoveryCalls,
		Admission: guardedOperationCounts(operation, admission), Qualification: guardedOperationCounts(operation, qualification),
		Operation: guardedOperationCounts(operation, final), Proposal: guardedOperationCounts(operation, proposal), Jira: jira,
	}
}

func previewGuardedProcessComment(t *testing.T, binary string, fixture *guardedProcessFixture) guardedProcessCommentResult {
	t.Helper()
	before := fixture.snapshot()
	run := fixture.runCLI(t, binary,
		"jira", "issue", "comment", "preview", guardedProcessIssueKey, "--from-file", fixture.bodyPath)
	result := requireGuardedProcessCommentSuccess(t, run, "preview", "proposed")
	fixture.approveGuardedProcessProposal(t, result)
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("writer", 3, 1, domain.BrokerOperationJiraCommentPreview, 1, 1, 1, 0,
		guardedProcessJiraCounts{Qualification: 1, Actor: 1, Inventory: 1}))
	return result
}

func initializeGuardedProcessJournal(t *testing.T, binary string, fixture *guardedProcessFixture) {
	t.Helper()
	before := fixture.snapshot()
	run := runGuardedProcessCLI(t, binary, fixture.daemonEnvironment(),
		"broker", "journal", "initialize", "--config", fixture.hostConfigPath)
	assertGuardedTransportRunPrivacy(t, fixture, run)
	if run.exitCode != 0 || run.stderr != "" {
		t.Fatalf("journal initializer exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	var result struct {
		Status   string `json:"status"`
		Complete bool   `json:"complete"`
	}
	decodeGuardedProcessJSON(t, []byte(run.stdout), &result)
	if result.Status != "initialized" || !result.Complete {
		t.Fatalf("journal initializer result=%+v", result)
	}
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("", 0, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
}

func requireGuardedProcessCommentSuccess(t *testing.T, run guardedProcessResult, mode, status string) guardedProcessCommentResult {
	t.Helper()
	if run.exitCode != 0 || run.stderr != "" || run.stdout == "" {
		t.Fatalf("selected guarded-comment CLI exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	assertGuardedProcessContentFree(t, run, guardedProcessBody)
	var result guardedProcessCommentResult
	decodeGuardedProcessJSON(t, []byte(run.stdout), &result)
	if result.SchemaVersion != 1 || result.Operation != "jira.comment."+mode || result.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 ||
		result.Mode != mode || result.Status != status || result.ArgumentsSHA256 == "" || result.NativeCandidateSHA256 == "" || result.VersionEvidenceSHA256 == "" {
		t.Fatalf("guarded-comment result contract mismatch: %+v", result)
	}
	return result
}

func requireGuardedProcessOutcomeSuccess(t *testing.T, run guardedProcessResult, ticket string, phase domain.BrokerOperationPhase) guardedProcessOutcomeResult {
	t.Helper()
	if run.exitCode != 0 || run.stderr != "" || run.stdout == "" {
		t.Fatalf("selected outcome CLI exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	assertGuardedProcessContentFree(t, run, guardedProcessBody)
	var result guardedProcessOutcomeResult
	decodeGuardedProcessJSON(t, []byte(run.stdout), &result)
	if result.SchemaVersion != 1 || result.Operation != string(domain.BrokerOperationOutcomeLookup) || result.QualificationProfile != "operation_ticket_v1" ||
		result.OperationTicket != ticket || result.TicketSHA256 == "" || result.Phase != phase || result.ObservedAtMillis <= 0 {
		t.Fatalf("outcome result contract mismatch: %+v", result)
	}
	return result
}

func requireGuardedProcessFailure(t *testing.T, run guardedProcessResult, forbidden ...string) {
	t.Helper()
	if run.exitCode == 0 || run.err == nil || run.stdout != "" || run.stderr == "" {
		t.Fatalf("expected closed selected-CLI failure: exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	assertGuardedProcessContentFree(t, run, forbidden...)
}

func decodeGuardedProcessJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if len(body) == 0 || len(body) > guardedProcessOutputLimit {
		t.Fatalf("selected process JSON size=%d", len(body))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("strict selected-process JSON decode: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("selected-process JSON had trailing data: %v", err)
	}
}

func assertGuardedProcessContentFree(t *testing.T, run guardedProcessResult, extra ...string) {
	t.Helper()
	combined := run.stdout + run.stderr
	for _, forbidden := range append([]string{
		guardedProcessWriterCredential, guardedProcessObserverCredential, guardedProcessReplacementCredential,
		guardedProcessForeignCredential, guardedProcessAuthorityCredential, guardedProcessJiraCredential,
	}, extra...) {
		forbidden = strings.TrimSpace(forbidden)
		if forbidden != "" && strings.Contains(combined, forbidden) {
			t.Fatalf("selected process output exposed a private fixture category")
		}
	}
}

type guardedProcessDaemon struct {
	child *guardedProcessChild
}

func startGuardedProcessDaemon(t *testing.T, binary string, fixture *guardedProcessFixture) *guardedProcessDaemon {
	t.Helper()
	before := fixture.snapshot()
	child := startGuardedProcessChild(t, binary, fixture.daemonEnvironment(),
		"broker", "serve", "--config", fixture.hostConfigPath, "--enable-jira-comments")
	daemon := &guardedProcessDaemon{child: child}
	client := brokerProcessTLSClient(t, fixture.identity)
	status := waitForBrokerReadiness(t, client, fixture.adminAddress)
	if status.Status != brokertransport.AdminStatusReady {
		t.Fatalf("Broker readiness=%+v", status)
	}
	fixture.assertDelta(t, before, expectedGuardedProcessDelta("admin", 1, 0, "", 0, 0, 0, 0, guardedProcessJiraCounts{}))
	return daemon
}

func stopGuardedProcessDaemon(t *testing.T, daemon *guardedProcessDaemon, fixture *guardedProcessFixture) {
	t.Helper()
	if daemon == nil || daemon.child == nil {
		t.Fatal("missing owned Broker daemon")
	}
	if err := daemon.child.signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal Broker daemon: %v", err)
	}
	run := daemon.child.wait(t, 8*time.Second)
	if run.err != nil || run.exitCode != 0 {
		t.Fatalf("Broker daemon did not drain cleanly: exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	var result struct {
		Status   string `json:"status"`
		Complete bool   `json:"complete"`
	}
	decodeGuardedProcessJSON(t, []byte(run.stdout), &result)
	if result.Status != "stopped" || !result.Complete {
		t.Fatalf("Broker drain result=%+v", result)
	}
	assertGuardedProcessDaemonOutput(t, run, fixture)
}

func killGuardedProcessDaemon(t *testing.T, daemon *guardedProcessDaemon, barrier *guardedProcessBarrier, fixture *guardedProcessFixture) {
	t.Helper()
	if daemon == nil || daemon.child == nil {
		t.Fatal("missing owned Broker daemon")
	}
	if err := daemon.child.signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL Broker daemon: %v", err)
	}
	barrier.open()
	run := daemon.child.wait(t, 8*time.Second)
	var exitErr *exec.ExitError
	if !errors.As(run.err, &exitErr) {
		t.Fatalf("Broker daemon did not report signal exit: exit=%d err=%v", run.exitCode, run.err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("Broker daemon exit was not exact SIGKILL: %v", exitErr.Sys())
	}
	assertGuardedProcessDaemonOutput(t, run, fixture)
}

func assertGuardedProcessDaemonOutput(t *testing.T, run guardedProcessResult, fixture *guardedProcessFixture) {
	t.Helper()
	if len(run.stdout) > guardedProcessOutputLimit || len(run.stderr) > guardedProcessOutputLimit {
		t.Fatal("Broker daemon output exceeded the test bound")
	}
	assertGuardedProcessContentFree(t, run, guardedProcessBody, fixture.hostConfigPath, fixture.clientRoot, fixture.authority.URL, fixture.jira.URL)
}

func waitGuardedProcessBarrier(t *testing.T, barrier *guardedProcessBarrier, name string) {
	t.Helper()
	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatalf("selected daemon did not reach %s within five seconds", name)
	}
}

type guardedProcessLimitedBuffer struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *guardedProcessLimitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := guardedProcessOutputLimit - len(b.data)
	if remaining < len(value) {
		b.overflow = true
	}
	if remaining > 0 {
		b.data = append(b.data, value[:min(remaining, len(value))]...)
	}
	return len(value), nil
}

func (b *guardedProcessLimitedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data)), b.overflow
}

type guardedProcessChild struct {
	command *exec.Cmd
	stdout  *guardedProcessLimitedBuffer
	stderr  *guardedProcessLimitedBuffer
	done    chan error
	mu      sync.Mutex
	reaped  bool
}

func startGuardedProcessChild(t *testing.T, binary string, environment []string, arguments ...string) *guardedProcessChild {
	t.Helper()
	stdout, stderr := &guardedProcessLimitedBuffer{}, &guardedProcessLimitedBuffer{}
	command := exec.Command(binary, arguments...)
	command.Env = environment
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start selected process: %v", err)
	}
	child := &guardedProcessChild{command: command, stdout: stdout, stderr: stderr, done: make(chan error, 1)}
	go func() { child.done <- command.Wait() }()
	t.Cleanup(func() {
		child.mu.Lock()
		reaped := child.reaped
		child.mu.Unlock()
		if reaped {
			return
		}
		child.killOwnedProcessGroup()
		select {
		case <-child.done:
			child.mu.Lock()
			child.reaped = true
			child.mu.Unlock()
		case <-time.After(8 * time.Second):
			t.Errorf("owned selected process could not be reaped")
		}
	})
	return child
}

func (c *guardedProcessChild) signal(signal os.Signal) error {
	if c == nil || c.command == nil || c.command.Process == nil {
		return fmt.Errorf("selected process is unavailable")
	}
	return c.command.Process.Signal(signal)
}

func (c *guardedProcessChild) wait(t *testing.T, timeout time.Duration) guardedProcessResult {
	t.Helper()
	select {
	case err := <-c.done:
		c.mu.Lock()
		c.reaped = true
		c.mu.Unlock()
		stdout, stdoutOverflow := c.stdout.snapshot()
		stderr, stderrOverflow := c.stderr.snapshot()
		if stdoutOverflow || stderrOverflow {
			t.Fatal("selected process output exceeded the bounded capture")
		}
		exitCode := 0
		if err != nil {
			exitCode = -1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
		}
		return guardedProcessResult{stdout: stdout, stderr: stderr, exitCode: exitCode, err: err}
	case <-time.After(timeout):
		c.killOwnedProcessGroup()
		select {
		case <-c.done:
			c.mu.Lock()
			c.reaped = true
			c.mu.Unlock()
		case <-time.After(8 * time.Second):
			t.Fatal("timed-out selected process could not be reaped")
		}
		t.Fatal("selected process exceeded its test deadline")
		return guardedProcessResult{}
	}
}

func (c *guardedProcessChild) killOwnedProcessGroup() {
	if c == nil || c.command == nil || c.command.Process == nil {
		return
	}
	if err := syscall.Kill(-c.command.Process.Pid, syscall.SIGKILL); err != nil {
		_ = c.command.Process.Kill()
	}
}

func startGuardedProcessCLI(t *testing.T, binary string, environment []string, arguments ...string) *guardedProcessChild {
	t.Helper()
	return startGuardedProcessChild(t, binary, environment, arguments...)
}

func waitGuardedProcessCLI(t *testing.T, child *guardedProcessChild, timeout time.Duration) guardedProcessResult {
	t.Helper()
	return child.wait(t, timeout)
}

func runGuardedProcessCLI(t *testing.T, binary string, environment []string, arguments ...string) guardedProcessResult {
	t.Helper()
	return waitGuardedProcessCLI(t, startGuardedProcessCLI(t, binary, environment, arguments...), 20*time.Second)
}

func (f *guardedProcessFixture) runCLI(t *testing.T, binary string, arguments ...string) guardedProcessResult {
	t.Helper()
	result := runGuardedProcessCLI(t, binary, f.clientEnvironment(), arguments...)
	assertGuardedTransportRunPrivacy(t, f, result)
	return result
}

func buildSelectedGuardedCommentATL(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "atl")
	run := startGuardedProcessChild(t, "go", guardedProcessEnvironment(os.Environ(), map[string]string{
		"GOTOOLCHAIN": "auto", "GOWORK": "off",
	}), "build", "-o", binary, "../../cmd/atl").wait(t, 90*time.Second)
	if run.err != nil || run.exitCode != 0 {
		t.Fatalf("build selected ATL binary: exit=%d stdout=%q stderr=%q err=%v", run.exitCode, run.stdout, run.stderr, run.err)
	}
	return binary
}
