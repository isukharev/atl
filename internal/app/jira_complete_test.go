package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type jiraCompleteTracker struct {
	domain.Tracker
	passIssues [][]domain.Issue
	searchCall int
	getCalls   []string
	getIssues  map[string]*domain.Issue
	getErrorAt string
}

func (t *jiraCompleteTracker) SearchQualified(_ context.Context, _ string, fields []string, limit int, cursor string) (domain.IssueSearchPage, error) {
	if limit != 100 || len(fields) != 1 || fields[0] != "project" {
		return domain.IssueSearchPage{}, errors.New("unexpected qualified projection")
	}
	pass := t.searchCall / 2
	if pass >= len(t.passIssues) {
		pass = len(t.passIssues) - 1
	}
	issues := t.passIssues[pass]
	t.searchCall++
	switch cursor {
	case "":
		if len(issues) == 0 {
			return domain.IssueSearchPage{Complete: true, TotalKnown: true}, nil
		}
		return domain.IssueSearchPage{Issues: issues[:1], Next: "1", Total: len(issues), TotalKnown: true}, nil
	case "1":
		return domain.IssueSearchPage{Issues: issues[1:], Complete: true, Total: len(issues), TotalKnown: true}, nil
	default:
		return domain.IssueSearchPage{}, errors.New("unexpected cursor")
	}
}

func (t *jiraCompleteTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	t.getCalls = append(t.getCalls, key)
	if key == t.getErrorAt {
		return nil, errors.New("injected read interruption")
	}
	issue := t.getIssues[key]
	if issue == nil {
		return nil, errors.New("missing issue fixture")
	}
	copy := *issue
	return &copy, nil
}

func completeJiraIssues() []domain.Issue {
	return []domain.Issue{
		{ID: "9", Key: "PROJ-9", Project: "PROJ", Summary: "nine", Body: "native nine", Fields: map[string]any{
			"project": map[string]any{"key": "PROJ"}, "summary": "nine", "description": "native nine",
		}},
		{ID: "10", Key: "PROJ-10", Project: "PROJ", Summary: "ten", Body: "native ten", Fields: map[string]any{
			"project": map[string]any{"key": "PROJ"}, "summary": "ten", "description": "native ten",
		}},
	}
}

func newCompleteJiraTracker() *jiraCompleteTracker {
	issues := completeJiraIssues()
	return &jiraCompleteTracker{
		passIssues: [][]domain.Issue{issues, issues},
		getIssues:  map[string]*domain.Issue{"9": &issues[0], "10": &issues[1]},
	}
}

func TestJiraCompletePullPublishesTwoPassNumericSelection(t *testing.T) {
	root := t.TempDir()
	tracker := newCompleteJiraTracker()
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.searchCall != 4 || len(tracker.getCalls) != 2 || tracker.getCalls[0] != "9" || tracker.getCalls[1] != "10" {
		t.Fatalf("search_calls=%d get_calls=%v", tracker.searchCall, tracker.getCalls)
	}
	if result.Complete == nil || !result.Complete.Complete || result.Complete.Total != 2 || result.Complete.Completed != 2 || result.Complete.CheckpointActive {
		t.Fatalf("complete result=%+v", result.Complete)
	}
	states, err := mirror.New(root).SyncStates()
	if err != nil || len(states) != 2 || states[0].Identity == "" || states[1].Identity == "" {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	for _, key := range []string{"PROJ-9", "PROJ-10"} {
		for _, ext := range []string{".wiki", ".md", ".json"} {
			if _, err := os.Stat(filepath.Join(root, "PROJ", key+ext)); err != nil {
				t.Fatalf("missing %s%s: %v", key, ext, err)
			}
		}
	}
}

func TestJiraCompletePullResumesWithoutRepublishingAcceptedPrefix(t *testing.T) {
	root := t.TempDir()
	tracker := newCompleteJiraTracker()
	tracker.getErrorAt = "10"
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	options := JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root}
	first, err := service.Pull(t.Context(), options)
	if err == nil || first.Complete == nil || first.Complete.Completed != 1 || !first.Complete.CheckpointActive {
		t.Fatalf("first=%+v err=%v", first.Complete, err)
	}
	searchCalls := tracker.searchCall
	tracker.getErrorAt = ""
	tracker.getCalls = nil
	second, err := service.Pull(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.searchCall != searchCalls || len(tracker.getCalls) != 1 || tracker.getCalls[0] != "10" {
		t.Fatalf("resume search_calls=%d want=%d get_calls=%v", tracker.searchCall, searchCalls, tracker.getCalls)
	}
	if second.Complete == nil || second.Complete.Source != "resumed" || !second.Complete.Complete || second.Complete.Completed != 2 {
		t.Fatalf("second=%+v", second.Complete)
	}
}

func TestJiraCompletePullOptionDriftFailsClosedAndRestartReplacesSnapshot(t *testing.T) {
	root := t.TempDir()
	tracker := newCompleteJiraTracker()
	tracker.getErrorAt = "10"
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	base := JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root}
	if _, err := service.Pull(t.Context(), base); err == nil {
		t.Fatal("seed pull unexpectedly completed")
	}
	m := mirror.New(root)
	selector, err := jiraCompleteSelectorHash("PROJ")
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := m.CompletePullCheckpoint(selector)
	if err != nil || !found || before.NextIndex != 1 {
		t.Fatalf("before=%+v found=%t err=%v", before, found, err)
	}
	searchCalls := tracker.searchCall
	getCalls := len(tracker.getCalls)
	drifted := base
	drifted.MaxIssues = 3
	if _, err := service.Pull(t.Context(), drifted); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("option drift err=%v", err)
	}
	if tracker.searchCall != searchCalls || len(tracker.getCalls) != getCalls {
		t.Fatalf("option drift made backend reads: search=%d get=%v", tracker.searchCall, tracker.getCalls)
	}
	after, found, err := m.CompletePullCheckpoint(selector)
	if err != nil || !found || !reflect.DeepEqual(after, before) {
		t.Fatalf("after=%+v before=%+v found=%t err=%v", after, before, found, err)
	}
	tracker.getErrorAt = ""
	drifted.RestartComplete = true
	restarted, err := service.Pull(t.Context(), drifted)
	if err != nil {
		t.Fatalf("restart result=%+v local=%+v err=%v", restarted, restarted.LocalSafety, err)
	}
	if restarted.Complete == nil || restarted.Complete.Source != "restarted" || !restarted.Complete.Complete || restarted.Complete.Total != 2 {
		t.Fatalf("restarted=%+v", restarted.Complete)
	}
}

func TestJiraCompletePullSelectionDriftWritesNoIssuePayload(t *testing.T) {
	root := t.TempDir()
	first := completeJiraIssues()
	second := completeJiraIssues()
	second[1].Key = "PROJ-11"
	tracker := newCompleteJiraTracker()
	tracker.passIssues = [][]domain.Issue{first, second}
	_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "PROJ")); !os.IsNotExist(err) {
		t.Fatalf("selection drift published issue directory: %v", err)
	}
}

func TestJiraCompletePullRelocatesChangedKeyByStableIdentity(t *testing.T) {
	root := t.TempDir()
	oldIssue := domain.Issue{ID: "10001", Key: "OLD-1", Project: "OLD", Summary: "old", Status: "Open", Type: "Task", Body: "native old", Fields: map[string]any{
		"summary": "old", "description": "native old", "status": map[string]any{"name": "Open"},
		"issuetype": map[string]any{"name": "Task"}, "project": map[string]any{"key": "OLD"},
	}}
	tracker := &jiraCompleteTracker{
		passIssues: [][]domain.Issue{{oldIssue}, {oldIssue}},
		getIssues:  map[string]*domain.Issue{"10001": &oldIssue},
	}
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	if _, err := service.Pull(t.Context(), JiraPullOpts{Complete: true, Project: "OLD", MaxIssues: 1, Into: root}); err != nil {
		t.Fatal(err)
	}
	newIssue := domain.Issue{ID: "10001", Key: "NEW-2", Project: "NEW", Summary: "new", Status: "Open", Type: "Task", Body: "native new", Fields: map[string]any{
		"summary": "new", "description": "native new", "status": map[string]any{"name": "Open"},
		"issuetype": map[string]any{"name": "Task"}, "project": map[string]any{"key": "NEW"},
	}}
	tracker.passIssues = [][]domain.Issue{{newIssue}, {newIssue}}
	tracker.getIssues = map[string]*domain.Issue{"10001": &newIssue}
	tracker.searchCall = 0
	tracker.getCalls = nil
	trackedBefore, trackedFound, trackedErr := mirror.New(root).JiraCompletePullStateByIdentity("10001")
	if trackedErr != nil || !trackedFound {
		t.Fatalf("tracked before relocation found=%t err=%v", trackedFound, trackedErr)
	}
	wantOldMD, wantOldErr := jiraCompleteOldView(mirror.New(root), trackedBefore)
	gotOldMD, gotOldErr := os.ReadFile(filepath.Join(root, "OLD", "OLD-1.md"))
	if wantOldErr != nil || gotOldErr != nil || string(wantOldMD) != string(gotOldMD) {
		t.Fatalf("old view reconstruction mismatch want_err=%v got_err=%v\nwant=%q\ngot=%q", wantOldErr, gotOldErr, wantOldMD, gotOldMD)
	}
	if _, err := service.Pull(t.Context(), JiraPullOpts{Complete: true, Project: "NEW", MaxIssues: 1, Into: root}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := mirror.New(root).SyncStateOf("OLD-1"); err != nil || found {
		t.Fatalf("old state found=%t err=%v", found, err)
	}
	state, found, err := mirror.New(root).SyncStateOf("NEW-2")
	if err != nil || !found || state.Identity != "10001" {
		t.Fatalf("new state=%+v found=%t err=%v", state, found, err)
	}
	for _, rel := range []string{"OLD/OLD-1.wiki", "OLD/OLD-1.md", "OLD/OLD-1.json"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("old artifact remains: %s err=%v", rel, err)
		}
	}
}

func TestJiraCompletePullRejectsIncompleteTerminalPage(t *testing.T) {
	tracker := &jiraCompleteTracker{passIssues: [][]domain.Issue{{}, {}}}
	// Override the empty-page helper's complete result with a closed partial page.
	searcher := &jiraCompletePartialTracker{jiraCompleteTracker: tracker}
	_, err := collectJiraCompletePass(t.Context(), searcher, "PROJ", 10)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
	var incomplete *jiraCompleteIncompleteError
	if !errors.As(err, &incomplete) || incomplete.PartialReason != domain.IssueSearchPartialPaginationStalled {
		t.Fatalf("incomplete=%+v err=%v", incomplete, err)
	}
}

type jiraCompletePartialTracker struct{ *jiraCompleteTracker }

func (*jiraCompletePartialTracker) SearchQualified(context.Context, string, []string, int, string) (domain.IssueSearchPage, error) {
	return domain.IssueSearchPage{PartialReason: domain.IssueSearchPartialPaginationStalled}, nil
}

type jiraCompleteTotalDriftTracker struct{ calls int }

func (t *jiraCompleteTotalDriftTracker) SearchQualified(context.Context, string, []string, int, string) (domain.IssueSearchPage, error) {
	t.calls++
	if t.calls == 1 {
		return domain.IssueSearchPage{Issues: completeJiraIssues()[:1], Next: "1", Total: 3, TotalKnown: true}, nil
	}
	return domain.IssueSearchPage{Issues: completeJiraIssues()[1:], Complete: true, Total: 2, TotalKnown: true}, nil
}

func TestJiraCompletePullRejectsCrossPageTotalDrift(t *testing.T) {
	_, err := collectJiraCompletePass(t.Context(), &jiraCompleteTotalDriftTracker{}, "PROJ", 10)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
}

type jiraCompleteFixedPageTracker struct{ page domain.IssueSearchPage }

func (t *jiraCompleteFixedPageTracker) SearchQualified(context.Context, string, []string, int, string) (domain.IssueSearchPage, error) {
	return t.page, nil
}

func TestJiraCompletePullRequiresQualifiedExactTotal(t *testing.T) {
	searcher := &jiraCompleteFixedPageTracker{page: domain.IssueSearchPage{Complete: true}}
	_, err := collectJiraCompletePass(t.Context(), searcher, "PROJ", 10)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
}

func TestJiraCompletePullRejectsTerminalCountDifferentFromTotal(t *testing.T) {
	searcher := &jiraCompleteFixedPageTracker{page: domain.IssueSearchPage{
		Issues: completeJiraIssues()[:1], Complete: true, Total: 2, TotalKnown: true,
	}}
	_, err := collectJiraCompletePass(t.Context(), searcher, "PROJ", 10)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
}
