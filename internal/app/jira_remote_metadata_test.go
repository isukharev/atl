package app

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type jiraMetadataBatchTracker struct {
	domain.Tracker
	batchSize     int
	requests      [][]string
	requestFields [][]string
	exactCalls    []string
	singleAttempt []bool
	redactedTrace []bool
	plan          func([]string) ([][]string, error)
	read          func([]string, []string, int) (domain.JiraIssueMetadataBatch, error)
	exact         map[string]*domain.Issue
}

func (t *jiraMetadataBatchTracker) PlanIssueMetadataBatches(keys []string) ([][]string, error) {
	if t.plan != nil {
		return t.plan(keys)
	}
	size := t.batchSize
	if size <= 0 {
		size = 100
	}
	var batches [][]string
	for start := 0; start < len(keys); start += size {
		end := min(start+size, len(keys))
		batches = append(batches, append([]string(nil), keys[start:end]...))
	}
	return batches, nil
}

func (t *jiraMetadataBatchTracker) ReadIssueMetadataBatch(ctx context.Context, keys, fields []string) (domain.JiraIssueMetadataBatch, error) {
	t.requests = append(t.requests, append([]string(nil), keys...))
	t.requestFields = append(t.requestFields, append([]string(nil), fields...))
	t.singleAttempt = append(t.singleAttempt, domain.SingleAttempt(ctx))
	t.redactedTrace = append(t.redactedTrace, domain.RedactedHTTPTrace(ctx))
	if t.read != nil {
		return t.read(keys, fields, len(t.requests)-1)
	}
	issues := make([]domain.Issue, 0, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		issues = append(issues, jiraBatchTestIssue(strings.ToLower(keys[i]), jiraBatchTestNumericID(keys[i]), "base"))
	}
	return domain.JiraIssueMetadataBatch{Issues: issues, Complete: true}, nil
}

func (t *jiraMetadataBatchTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	t.exactCalls = append(t.exactCalls, key)
	return t.exact[key], nil
}

func jiraBatchTestIssue(key, id, description string) domain.Issue {
	return domain.Issue{ID: id, Key: key, Body: description, Fields: map[string]any{"description": description}}
}

func jiraBatchTestNumericID(key string) string {
	parts := strings.Split(key, "-")
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return strconv.Itoa(10000 + n)
}

func setupJiraMetadataMirror(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	issues := make([]domain.Issue, count)
	for i := range issues {
		key := fmt.Sprintf("PROJ-%03d", i+1)
		issues[i] = domain.Issue{Key: key, Project: "PROJ", Summary: key, Status: "Open", Type: "Task", Body: "base"}
	}
	seed := &syncTracker{searchIssues: issues}
	if _, err := (&JiraService{tr: seed, baseURL: jiraMirrorTestBackendURL}).Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root, Limit: count}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	return root
}

func jiraBatchRequestLengths(batches [][]string) []int {
	out := make([]int, len(batches))
	for i := range batches {
		out[i] = len(batches[i])
	}
	return out
}

func TestJiraMetadataBatchFieldsUnionIsDeterministic(t *testing.T) {
	fields := jiraMetadataBatchFields([]string{"PROJ-1", "PROJ-2"}, map[string][]string{
		"PROJ-1": {"customfield_2", "customfield_1"},
		"PROJ-2": {"customfield_1", "customfield_3"},
	})
	if !reflect.DeepEqual(fields, []string{"description", "customfield_2", "customfield_1", "customfield_3"}) {
		t.Fatalf("fields=%v", fields)
	}
}

func TestReadJiraRemoteMetadataBatchesAcceptsReversedCaseOnlyKeys(t *testing.T) {
	tracker := &jiraMetadataBatchTracker{}
	got := readJiraRemoteMetadataBatches(context.Background(), tracker, []string{"PROJ-1", "PROJ-2"}, nil)
	if !got["PROJ-1"].available || got["PROJ-1"].issue.Key != "proj-1" || !got["PROJ-2"].available || len(tracker.requests) != 1 {
		t.Fatalf("evidence=%+v requests=%v", got, tracker.requests)
	}
}

func TestReadJiraRemoteMetadataBatchesContinuesAfterFailedBatch(t *testing.T) {
	for name, batchErr := range map[string]error{"forbidden": domain.ErrForbidden, "not found": domain.ErrNotFound} {
		t.Run(name, func(t *testing.T) {
			tracker := &jiraMetadataBatchTracker{batchSize: 2}
			tracker.read = func(keys, _ []string, call int) (domain.JiraIssueMetadataBatch, error) {
				if call == 0 {
					return domain.JiraIssueMetadataBatch{}, batchErr
				}
				return domain.JiraIssueMetadataBatch{Complete: true, Issues: []domain.Issue{
					jiraBatchTestIssue(keys[1], "4", "base"), jiraBatchTestIssue(keys[0], "3", "base"),
				}}, nil
			}
			got := readJiraRemoteMetadataBatches(context.Background(), tracker, []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"}, nil)
			if got["PROJ-1"].reason != jiraRemoteEvidenceIncomplete || got["PROJ-2"].reason != jiraRemoteEvidenceIncomplete ||
				!got["PROJ-3"].available || !got["PROJ-4"].available || len(tracker.requests) != 2 {
				t.Fatalf("evidence=%+v requests=%v", got, tracker.requests)
			}
		})
	}
}

func TestReadJiraRemoteMetadataBatchesInvalidatesWholeMalformedBatch(t *testing.T) {
	valid1 := jiraBatchTestIssue("PROJ-1", "1", "one")
	valid2 := jiraBatchTestIssue("PROJ-2", "2", "two")
	nullDescription := jiraBatchTestIssue("PROJ-2", "2", "")
	nullDescription.Fields["description"] = nil
	tests := map[string]domain.JiraIssueMetadataBatch{
		"omitted":                     {Complete: true, Issues: []domain.Issue{valid1}},
		"duplicate key":               {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("proj-1", "2", "two")}},
		"unexpected key":              {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("PROJ-9", "2", "two")}},
		"spaced key":                  {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue(" PROJ-2 ", "2", "two")}},
		"duplicate id":                {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("PROJ-2", "1", "two")}},
		"empty id":                    {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("PROJ-2", "", "two")}},
		"noncanonical id":             {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("PROJ-2", "02", "two")}},
		"spaced id":                   {Complete: true, Issues: []domain.Issue{valid1, jiraBatchTestIssue("PROJ-2", " 2 ", "two")}},
		"nil fields":                  {Complete: true, Issues: []domain.Issue{valid1, {ID: "2", Key: "PROJ-2"}}},
		"missing description":         {Complete: true, Issues: []domain.Issue{valid1, {ID: "2", Key: "PROJ-2", Fields: map[string]any{}}}},
		"nonstring description":       {Complete: true, Issues: []domain.Issue{valid1, {ID: "2", Key: "PROJ-2", Fields: map[string]any{"description": map[string]any{}}}}},
		"partial":                     {Issues: []domain.Issue{valid1, valid2}, PartialReason: domain.IssueSearchPartialPaginationStalled},
		"contradictory qualification": {Complete: true, Issues: []domain.Issue{valid1, valid2}, PartialReason: domain.IssueSearchPartialPaginationUnqualified},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			tracker := &jiraMetadataBatchTracker{read: func([]string, []string, int) (domain.JiraIssueMetadataBatch, error) { return response, nil }}
			got := readJiraRemoteMetadataBatches(context.Background(), tracker, []string{"PROJ-1", "PROJ-2"}, nil)
			if got["PROJ-1"].available || got["PROJ-2"].available || got["PROJ-1"].reason != jiraRemoteEvidenceIncomplete || got["PROJ-2"].reason != jiraRemoteEvidenceIncomplete {
				t.Fatalf("evidence=%+v", got)
			}
		})
	}
	tracker := &jiraMetadataBatchTracker{read: func([]string, []string, int) (domain.JiraIssueMetadataBatch, error) {
		return domain.JiraIssueMetadataBatch{Complete: true, Issues: []domain.Issue{valid1, nullDescription}}, nil
	}}
	if got := readJiraRemoteMetadataBatches(context.Background(), tracker, []string{"PROJ-1", "PROJ-2"}, nil); !got["PROJ-1"].available || !got["PROJ-2"].available {
		t.Fatalf("explicit null description must be valid empty: %+v", got)
	}
}

func TestJiraBatchPendingFieldsRemainPerFieldDrift(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"absent":     {},
		"null":       {"customfield_1": nil},
		"non-string": {"customfield_1": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, valid := jiraBatchPendingStringField(fields, "customfield_1"); valid {
				t.Fatalf("fields=%v unexpectedly valid", fields)
			}
		})
	}
}

func TestJiraStatusProjectsQualifiedBatchInCanonicalOrder(t *testing.T) {
	root := setupJiraMetadataMirror(t, 3)
	tracker := &jiraMetadataBatchTracker{read: func(keys, _ []string, _ int) (domain.JiraIssueMetadataBatch, error) {
		issues := make([]domain.Issue, 0, len(keys))
		for i := len(keys) - 1; i >= 0; i-- {
			body := "base"
			if keys[i] == "PROJ-002" {
				body = "changed"
			}
			issues = append(issues, jiraBatchTestIssue(strings.ToLower(keys[i]), jiraBatchTestNumericID(keys[i]), body))
		}
		return domain.JiraIssueMetadataBatch{Issues: issues, Complete: true}, nil
	}}
	entries, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Status(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Key != "PROJ-001" || entries[1].Key != "PROJ-002" || entries[2].Key != "PROJ-003" ||
		len(tracker.requests) != 1 || !reflect.DeepEqual(tracker.requests[0], []string{"PROJ-001", "PROJ-002", "PROJ-003"}) || len(tracker.exactCalls) != 0 || !entries[1].RemoteDrifted {
		t.Fatalf("entries=%+v requests=%v exact=%v", entries, tracker.requests, tracker.exactCalls)
	}
	if !tracker.singleAttempt[0] || !tracker.redactedTrace[0] {
		t.Fatalf("status request single=%t redacted=%t", tracker.singleAttempt[0], tracker.redactedTrace[0])
	}
}

func TestJiraStatusKeepsBatchErrorCoarseAndContinuesLaterBatch(t *testing.T) {
	root := setupJiraMetadataMirror(t, 3)
	tracker := &jiraMetadataBatchTracker{batchSize: 2}
	tracker.read = func(keys, _ []string, call int) (domain.JiraIssueMetadataBatch, error) {
		if call == 0 {
			return domain.JiraIssueMetadataBatch{}, domain.ErrNotFound
		}
		return domain.JiraIssueMetadataBatch{Complete: true, Issues: []domain.Issue{
			jiraBatchTestIssue(keys[0], jiraBatchTestNumericID(keys[0]), "base"),
		}}, nil
	}
	entries, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Status(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracker.requests) != 2 || len(tracker.exactCalls) != 0 || entries[0].RemoteError != jiraRemoteEvidenceIncomplete || entries[1].RemoteError != jiraRemoteEvidenceIncomplete ||
		entries[2].RemoteError != "" || entries[2].RemoteDrifted {
		t.Fatalf("requests=%v entries=%+v", tracker.requests, entries)
	}
}

func TestJiraSnapshotKeepsFailedBatchUnavailableAndCreditsLaterBatch(t *testing.T) {
	root := setupJiraMetadataMirror(t, 3)
	tracker := &jiraMetadataBatchTracker{batchSize: 2}
	tracker.read = func(keys, _ []string, call int) (domain.JiraIssueMetadataBatch, error) {
		if call == 0 {
			return domain.JiraIssueMetadataBatch{}, domain.ErrForbidden
		}
		return domain.JiraIssueMetadataBatch{Complete: true, Issues: []domain.Issue{
			jiraBatchTestIssue(keys[0], jiraBatchTestNumericID(keys[0]), "base"),
		}}, nil
	}
	got, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracker.requests) != 2 || len(tracker.exactCalls) != 0 || got.Remote.Attempted != 3 || got.Remote.Unavailable != 2 ||
		got.Remote.Checked != 1 || got.Remote.InSync != 1 || got.Remote.Drifted != 0 || got.Complete {
		t.Fatalf("requests=%v exact=%v snapshot=%+v", tracker.requests, tracker.exactCalls, got)
	}
}

func TestJiraSnapshotSplitsMoreThanOneHundredAndUsesRedactedSingleAttempts(t *testing.T) {
	root := setupJiraMetadataMirror(t, 101)
	tracker := &jiraMetadataBatchTracker{}
	got, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracker.requests) != 2 || len(tracker.requests[0]) != 100 || len(tracker.requests[1]) != 1 || len(tracker.exactCalls) != 0 ||
		got.Remote.Attempted != 101 || got.Remote.Checked != 101 || got.Remote.InSync != 101 || got.Remote.Unavailable != 0 || !got.Complete {
		t.Fatalf("requests=%v snapshot=%+v", jiraBatchRequestLengths(tracker.requests), got)
	}
	for i := range tracker.requests {
		if !tracker.singleAttempt[i] || !tracker.redactedTrace[i] {
			t.Fatalf("request %d single=%t redacted=%t", i, tracker.singleAttempt[i], tracker.redactedTrace[i])
		}
	}
}

func TestJiraRemoteMetadataKeepsSingleIdentityOnExactPath(t *testing.T) {
	root := setupJiraMetadataMirror(t, 1)
	issue := jiraBatchTestIssue("PROJ-001", "10001", "base")
	tracker := &jiraMetadataBatchTracker{exact: map[string]*domain.Issue{"PROJ-001": &issue}}
	svc := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	snapshot, err := svc.SnapshotMirror(context.Background(), root, true)
	if err != nil || !snapshot.Complete || len(tracker.exactCalls) != 1 || len(tracker.requests) != 0 {
		t.Fatalf("snapshot=%+v exact=%v batches=%v err=%v", snapshot, tracker.exactCalls, tracker.requests, err)
	}
	entries, err := svc.Status(context.Background(), root, true)
	if err != nil || len(entries) != 1 || entries[0].RemoteDrifted || len(tracker.exactCalls) != 2 || len(tracker.requests) != 0 {
		t.Fatalf("status=%+v exact=%v batches=%v err=%v", entries, tracker.exactCalls, tracker.requests, err)
	}
}

func TestJiraRemoteMetadataFallsBackWhenBatchCapabilityUnavailable(t *testing.T) {
	root := setupJiraMetadataMirror(t, 3)
	tracker := &syncTracker{serverBodies: map[string]string{"PROJ-001": "base", "PROJ-002": "base", "PROJ-003": "base"}}
	svc := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	if _, err := svc.Status(context.Background(), root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SnapshotMirror(context.Background(), root, true); err != nil {
		t.Fatal(err)
	}
	if tracker.getCalls != 6 {
		t.Fatalf("exact calls=%d want 6", tracker.getCalls)
	}
}

func TestJiraBatchNullPendingFieldIsDriftNotUnavailable(t *testing.T) {
	root := setupJiraMetadataMirror(t, 2)
	pending := &JiraPendingFields{
		Key: "PROJ-001", WikiPath: filepath.Join("PROJ", "PROJ-001.wiki"), WikiBody: "base", WikiHash: mirror.Hash([]byte("base")),
		Fields: []JiraPendingField{{ID: "customfield_1", Base: "", Value: ""}},
	}
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	tracker := &jiraMetadataBatchTracker{read: func(keys, _ []string, _ int) (domain.JiraIssueMetadataBatch, error) {
		issues := make([]domain.Issue, 0, len(keys))
		for _, key := range keys {
			issue := jiraBatchTestIssue(key, jiraBatchTestNumericID(key), "base")
			if key == "PROJ-001" {
				issue.Fields["customfield_1"] = nil
			}
			issues = append(issues, issue)
		}
		return domain.JiraIssueMetadataBatch{Issues: issues, Complete: true}, nil
	}}
	svc := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	entries, err := svc.Status(context.Background(), root, true)
	if err != nil || !entries[0].FieldDrifted || !entries[0].RemoteDrifted || entries[0].RemoteError != "" {
		t.Fatalf("status=%+v err=%v", entries, err)
	}
	snapshot, err := svc.SnapshotMirror(context.Background(), root, true)
	if err != nil || snapshot.Remote.Checked != 2 || snapshot.Remote.Drifted != 1 || snapshot.Remote.Unavailable != 0 || !snapshot.Complete {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if len(tracker.requestFields) != 2 || !reflect.DeepEqual(tracker.requestFields[0], []string{"description", "customfield_1"}) ||
		!reflect.DeepEqual(tracker.requestFields[1], []string{"description", "customfield_1"}) {
		t.Fatalf("field unions=%v", tracker.requestFields)
	}
}

var _ domain.QualifiedJiraIssueMetadataBatchReader = (*jiraMetadataBatchTracker)(nil)
