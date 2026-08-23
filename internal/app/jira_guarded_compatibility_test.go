package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedStandaloneCompatibilityKnownHashes(t *testing.T) {
	link, err := NewJiraService(JiraDependencies{Tracker: guardedLinkFixture(false), BaseURL: "https://jira.example.test"}).GuardedLink(
		t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
	if err != nil {
		t.Fatal(err)
	}
	label, err := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot("2026-08-23T10:00:00Z", "old")}}).GuardedLabels(
		t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	if err != nil {
		t.Fatal(err)
	}
	comment := previewGuardedComment(t, guardedCommentFixture(), "body", jiraCommentSatisfactionAppendAlways)
	field := guardedFieldPreview(t, guardedFieldProposals(), map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}})
	expected := map[string]struct{ proposal, output string }{
		"link":    {"a99b57c28690d106fc2894b2699af3fc3b40ed195f63eb8848b8e9ef27abde6d", "ef851646075eec181475702c35d93896c850b30cb5912615c26ed62af1d6cddc"},
		"label":   {"ff9b79f3bc1a3e373d7ae910fe7ea1f4c80d19c64c678afe0cc983e2da1db7fc", "b41f74bcd56999c36bfe4bd6884d8ac1b91483d38017d98a464843aca5df8a99"},
		"comment": {"2f19dbab01089b13bb0a6b4110c5fd306794e19ce925faf38017df940e28d4aa", "e5a15727b63e2d144bc3a66716ae6f83bbde4d4d22a4c7e293478f45bc3fa1d5"},
		"field":   {"6469640102027a7a06073af75bd343ad8ce61a957609c485baa97f7361f4ee29", "4e57882ef8f0cbf0d485417dc75ff36c6d95805faf11e3e6b5f73df9d9eb353d"},
	}
	for name, value := range map[string]any{"link": link, "label": label, "comment": comment, "field": field} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if proposal, output := proposalHashForCompatibility(value), sha256Hex(encoded); proposal != expected[name].proposal || output != expected[name].output {
			t.Fatalf("%s proposal=%s output=%s", name, proposal, output)
		}
		if name == "link" && strings.Contains(string(encoded), "updated") {
			t.Fatalf("link public wire gained internal updated evidence: %s", encoded)
		}
	}
}

func TestGuardedPublicWrappersMatchPreparedPreviewCores(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		opts := JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"}
		wrapped, err := NewJiraService(JiraDependencies{Tracker: guardedLinkFixture(false), BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		port := guardedLinkFixture(false)
		service := NewJiraService(JiraDependencies{Tracker: port, BaseURL: "https://jira.example.test"})
		execution, _ := newJiraGuardedExecution(t.Context(), nil, jiraGuardedLinkMaxRequestsPreview, jiraGuardedLinkMaxResponseBytes, jiraGuardedLinkDeadline)
		defer execution.Close()
		initial, err := service.buildGuardedLinkSnapshot(execution.ctx, port, opts, opts.From, opts.To, "", "")
		if err != nil {
			t.Fatal(err)
		}
		initial.result.Mode = "preview"
		direct, err := service.guardedLinkPreparedCore(execution, port, opts, &jiraGuardedLinkPrepared{
			result: initial.result, firstID: initial.first.ID, secondID: initial.second.ID,
			sourceUpdated: initial.first.Updated, sourceUpdatedPresent: initial.first.UpdatedPresent,
		})
		requireSameGuardedResult(t, wrapped, direct, err)
	})

	t.Run("labels", func(t *testing.T) {
		opts := JiraGuardedLabelOpts{Add: []string{"new"}}
		wrapped, err := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot("2026-08-23T10:00:00Z", "old")}}).GuardedLabels(t.Context(), "OPS-1", opts)
		if err != nil {
			t.Fatal(err)
		}
		opts, _ = NormalizeJiraGuardedLabelOpts(opts)
		port := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot("2026-08-23T10:00:00Z", "old")}}
		service := guardedLabelService(port)
		execution, _ := newJiraGuardedExecution(t.Context(), nil, jiraGuardedLabelPreviewRequests, jiraGuardedLabelMaxResponseBytes, jiraGuardedLabelDeadline)
		defer execution.Close()
		initial, err := service.buildGuardedLabelSnapshot(execution.ctx, port, "OPS-1", "OPS-1", "", opts)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := service.guardedLabelsPreparedCore(execution, port, "OPS-1", opts, &jiraGuardedLabelPrepared{result: initial.result, issueID: initial.evidence.ID})
		usage := execution.Usage()
		direct.Usage = JiraGuardedLabelUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
		requireSameGuardedResult(t, wrapped, direct, err)
	})

	t.Run("comment", func(t *testing.T) {
		opts := JiraCommentAddOpts{Body: []byte("body"), SatisfactionPolicy: jiraCommentSatisfactionAppendAlways}
		wrapped, err := (&JiraService{tr: guardedCommentFixture(), baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", opts)
		if err != nil {
			t.Fatal(err)
		}
		opts, _ = normalizeJiraCommentAddOpts(opts)
		port := guardedCommentFixture()
		service := &JiraService{tr: port, baseURL: "https://jira.example.test"}
		execution, _ := newJiraGuardedExecution(t.Context(), nil, jiraGuardedCommentPreviewMaxRequests, jiraGuardedCommentMaxResponseBytes, jiraGuardedCommentDeadline)
		defer execution.Close()
		initial, err := service.buildGuardedCommentSnapshot(execution.ctx, port, "PROJ-1", "PROJ-1", "", opts)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := service.addCommentGuardedPreparedCore(execution, port, "PROJ-1", opts, &jiraGuardedCommentPrepared{result: initial.result, issueID: initial.issue.ID, body: append([]byte(nil), opts.Body...)})
		usage := execution.Usage()
		direct.Usage = JiraCommentUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
		requireSameGuardedResult(t, wrapped, direct, err)
	})

	t.Run("field", func(t *testing.T) {
		opts := JiraFieldSetOpts{Proposals: guardedFieldProposals(), AllowFields: []string{"customfield_1", "plugin.vendor"}}
		current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
		wrapped, err := (&JiraService{tr: &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", opts)
		if err != nil {
			t.Fatal(err)
		}
		proposals, values, allowlist, inputBytes, _ := normalizeGuardedFieldInputs(opts.Proposals, opts.AllowFields)
		backendHash, _ := backendid.OriginSHA256("https://jira.example.test")
		port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}
		service := &JiraService{tr: port, baseURL: "https://jira.example.test"}
		execution, _ := newJiraGuardedExecution(t.Context(), nil, domain.JiraGuardedFieldPreviewMaxRequests, domain.JiraGuardedFieldPreviewMaxResponseBytes, time.Duration(domain.JiraGuardedFieldDeadlineMillis)*time.Millisecond)
		defer execution.Close()
		initial, err := service.buildGuardedFieldSnapshot(execution.ctx, port, "PROJ-1", "PROJ-1", "", proposals, values, allowlist, backendHash, false, inputBytes)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := service.setFieldsGuardedPreparedCore(execution, port, "PROJ-1", jiraGuardedFieldExecutionOpts{}, &jiraGuardedFieldPrepared{
			result: initial.result, issueID: initial.issue.ID, backendHash: backendHash, inputBytes: inputBytes,
			satisfied: guardedFieldValuesSatisfied(initial.issue.Fields, initial.values),
		})
		usage := execution.Usage()
		direct.Usage.Requests, direct.Usage.ResponseBytes = usage.Attempts, usage.ResponseBytes
		requireSameGuardedResult(t, wrapped, direct, err)
	})
}

func requireSameGuardedResult(t *testing.T, wrapped, direct any, directErr error) {
	t.Helper()
	if directErr != nil {
		t.Fatal(directErr)
	}
	wireWrapped, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	wireDirect, err := json.Marshal(direct)
	if err != nil {
		t.Fatal(err)
	}
	if string(wireWrapped) != string(wireDirect) {
		t.Fatalf("wrapper/core mismatch\nwrapper=%s\ncore=%s", wireWrapped, wireDirect)
	}
}

func proposalHashForCompatibility(value any) string {
	switch typed := value.(type) {
	case *JiraGuardedLinkResult:
		return typed.ProposalHash
	case *JiraGuardedLabelResult:
		return typed.ProposalHash
	case *JiraCommentAddResult:
		return typed.ProposalHash
	case *JiraFieldSetResult:
		return typed.ProposalHash
	default:
		return ""
	}
}
