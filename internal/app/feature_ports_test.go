package app

import (
	"context"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type contentLabelReaderOnly struct{}

func (contentLabelReaderOnly) ListContentLabels(context.Context, string) ([]domain.ContentLabel, bool, error) {
	return nil, false, nil
}

type issueWatcherReaderOnly struct{}

func (issueWatcherReaderOnly) ListIssueWatchers(context.Context, string) (*domain.IssueWatcherList, error) {
	return &domain.IssueWatcherList{Complete: true}, nil
}

type issueWorklogReaderOnly struct{}

func (issueWorklogReaderOnly) ListIssueWorklogs(context.Context, string) (*domain.IssueWorklogList, error) {
	return &domain.IssueWorklogList{Complete: true}, nil
}

func TestFocusedFeatureReadersDoNotRequireBroadStoresOrWriters(t *testing.T) {
	labels := NewConfluenceLabelService(ConfluenceLabelDependencies{
		Reader: contentLabelReaderOnly{},
		ResolveReference: func(context.Context, string) (*ConfluencePageResolution, error) {
			return &ConfluencePageResolution{ID: "42", Kind: "id"}, nil
		},
	})
	if _, err := labels.ListLabels(context.Background(), "42"); err != nil {
		t.Fatalf("label list with reader-only port: %v", err)
	}

	watchers := NewJiraWatcherService(JiraWatcherDependencies{Reader: issueWatcherReaderOnly{}})
	if _, err := watchers.ListWatchers(context.Background(), "PROJ-1"); err != nil {
		t.Fatalf("watcher list with reader-only port: %v", err)
	}

	worklogs := NewJiraWorklogService(JiraWorklogDependencies{Reader: issueWorklogReaderOnly{}})
	if _, err := worklogs.ListWorklogs(context.Background(), "PROJ-1"); err != nil {
		t.Fatalf("worklog list with reader-only port: %v", err)
	}
}

func TestFocusedFeatureFakesDoNotImplementBroadBackendPorts(t *testing.T) {
	for name, candidate := range map[string]any{
		"confluence labels": &confluenceLabelStoreStub{},
		"jira watchers":     &jiraWatcherStoreStub{},
		"jira worklogs":     &jiraWorklogStoreStub{},
	} {
		if _, ok := candidate.(domain.DocStore); ok {
			t.Errorf("%s fake unexpectedly implements domain.DocStore", name)
		}
		if _, ok := candidate.(domain.Tracker); ok {
			t.Errorf("%s fake unexpectedly implements domain.Tracker", name)
		}
	}
}
