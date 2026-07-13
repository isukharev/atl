package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraWorklogStoreStub struct {
	domain.Tracker
	current       domain.User
	worklogs      []domain.IssueWorklog
	addErr        error
	addWithoutID  bool
	commitOnError bool
	addCalls      int
	listCalls     int
	incomplete    bool
}

func (s *jiraWorklogStoreStub) CurrentUser(context.Context) (*domain.User, error) {
	copy := s.current
	return &copy, nil
}

func (s *jiraWorklogStoreStub) ListIssueWorklogs(context.Context, string) (*domain.IssueWorklogList, error) {
	s.listCalls++
	copy := append([]domain.IssueWorklog(nil), s.worklogs...)
	return &domain.IssueWorklogList{Worklogs: copy, Total: len(copy), Complete: !s.incomplete}, nil
}

func (s *jiraWorklogStoreStub) AddIssueWorklog(_ context.Context, _ string, input domain.IssueWorklogCreate) (*domain.IssueWorklog, error) {
	s.addCalls++
	created := domain.IssueWorklog{
		ID: "new", Author: domain.IssueWorklogAuthor{Name: s.current.Name, Key: s.current.Key},
		Comment: input.Comment, Started: input.Started, TimeSpentSeconds: input.TimeSpentSeconds,
	}
	if s.addErr == nil || s.commitOnError {
		s.worklogs = append(s.worklogs, created)
	}
	if s.addWithoutID {
		copy := created
		copy.ID = ""
		return &copy, s.addErr
	}
	return &created, s.addErr
}

func TestNormalizeJiraWorklogDuration(t *testing.T) {
	for _, test := range []struct {
		input   string
		seconds int64
		display string
	}{
		{"1h30m", 5400, "1h 30m"},
		{"90m", 5400, "1h 30m"},
		{" 1 H 2m 3s ", 3723, "1h 2m 3s"},
	} {
		seconds, display, err := NormalizeJiraWorklogDuration(test.input)
		if err != nil || seconds != test.seconds || display != test.display {
			t.Fatalf("%q => %d %q err=%v", test.input, seconds, display, err)
		}
	}
	for _, input := range []string{"", "0m", "1d", "1.5h", "1h nope", "9223372036854775807h"} {
		if _, _, err := NormalizeJiraWorklogDuration(input); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("%q error=%v, want ErrUsage", input, err)
		}
	}
}

func TestJiraWorklogPreviewAndApply(t *testing.T) {
	store := &jiraWorklogStoreStub{
		current:  domain.User{Name: "alice", Key: "u1", DisplayName: "Alice", Email: "private@example.test", Active: true},
		worklogs: []domain.IssueWorklog{{ID: "old", Started: "2026-07-01T09:00:00.000+0000"}},
	}
	service := &JiraService{tr: store}
	preview, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "1h30m", Comment: "implemented", Started: "2026-07-01T10:00:00Z",
	})
	if err != nil || preview.Status != "would_apply" || preview.TimeSpentSeconds != 5400 || preview.Author.Name != "alice" || preview.Author.DisplayName != "Alice" || preview.CurrentCount != 1 || store.addCalls != 0 {
		t.Fatalf("preview=%+v calls=%d err=%v", preview, store.addCalls, err)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "private@example.test") {
		t.Fatalf("preview leaked email: %s", encoded)
	}
	applied, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "1h30m", Comment: "implemented", Started: "2026-07-01T10:00:00Z",
		Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || applied.Status != "applied" || applied.Created == nil || applied.Created.ID != "new" || store.addCalls != 1 {
		t.Fatalf("applied=%+v calls=%d err=%v", applied, store.addCalls, err)
	}
}

func TestJiraWorklogHashGateAndIncompleteBaseline(t *testing.T) {
	store := &jiraWorklogStoreStub{current: domain.User{Name: "alice"}}
	service := &JiraService{tr: store}
	result, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "1h", Apply: true, ExpectedProposalHash: "stale",
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || store.addCalls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.addCalls, err)
	}
	store.incomplete = true
	if _, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{Time: "1h"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("incomplete error=%v", err)
	}
}

func TestJiraWorklogReconcilesAmbiguousWriteWithoutReplay(t *testing.T) {
	store := &jiraWorklogStoreStub{current: domain.User{Name: "alice"}, addErr: errors.New("connection lost"), commitOnError: true}
	service := &JiraService{tr: store}
	preview, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{Time: "30m", Comment: "done", Started: "2026-07-01T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "30m", Comment: "done", Started: "2026-07-01T10:00:00Z", Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || applied.Status != "applied" || !applied.Reconciled || applied.Created == nil || store.addCalls != 1 || store.listCalls != 3 {
		t.Fatalf("applied=%+v addCalls=%d listCalls=%d err=%v", applied, store.addCalls, store.listCalls, err)
	}
}

func TestJiraWorklogAmbiguousWriteWithoutExplicitStartedRemainsUnknown(t *testing.T) {
	store := &jiraWorklogStoreStub{current: domain.User{Name: "alice"}, addErr: errors.New("connection lost"), commitOnError: true}
	service := &JiraService{tr: store}
	preview, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{Time: "30m", Comment: "done"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "30m", Comment: "done", Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err == nil || result.Status != "unknown" || !result.Reconciled || store.addCalls != 1 {
		t.Fatalf("result=%+v addCalls=%d err=%v", result, store.addCalls, err)
	}
}

func TestJiraWorklogMarkdownHumanizesTimeAndEscapesCells(t *testing.T) {
	result := &JiraWorklogListResult{Worklogs: []domain.IssueWorklog{{
		ID: "1", Started: "2026-07-01T10:00:00.000+0000", TimeSpentSeconds: 5400,
		Author: domain.IssueWorklogAuthor{Name: "alice"}, Comment: "first | line\ncontinued",
	}}}
	text := JiraWorklogListMarkdown(result)
	for _, want := range []string{"1h 30m", "2026-07-01 10:00 UTC", "alice", `first \| line continued`} {
		if !strings.Contains(text, want) {
			t.Fatalf("text=%q missing %q", text, want)
		}
	}
}

type worklogStatusError int

func (e worklogStatusError) Error() string   { return "rejected" }
func (e worklogStatusError) HTTPStatus() int { return int(e) }

func TestJiraWorklogDoesNotReconcileDefinitiveRejection(t *testing.T) {
	store := &jiraWorklogStoreStub{current: domain.User{Name: "alice"}, addErr: worklogStatusError(400)}
	service := &JiraService{tr: store}
	preview, _ := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{Time: "1m"})
	result, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", JiraWorklogAddOpts{
		Time: "1m", Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err == nil || result.Status != "failed" || store.addCalls != 1 || store.listCalls != 2 {
		t.Fatalf("result=%+v addCalls=%d listCalls=%d err=%v", result, store.addCalls, store.listCalls, err)
	}
}
