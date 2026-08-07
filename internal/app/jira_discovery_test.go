package app

import (
	"context"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type discoveryTracker struct {
	*recordingTracker
	projects   []domain.JiraProject
	issueTypes []domain.JiraIssueType
	metadata   *domain.JiraCreateMetadata
}

func (t *discoveryTracker) ReadProjects(context.Context, bool) ([]domain.JiraProject, error) {
	return append([]domain.JiraProject(nil), t.projects...), t.err
}

func (t *discoveryTracker) ReadCreateIssueTypes(context.Context, string) ([]domain.JiraIssueType, error) {
	return append([]domain.JiraIssueType(nil), t.issueTypes...), t.err
}

func (t *discoveryTracker) ReadCreateMetadata(context.Context, string, string) (*domain.JiraCreateMetadata, error) {
	return t.metadata, t.err
}

func TestJiraProjectDiscoverySortsAndReportsLocalTruncation(t *testing.T) {
	tr := &discoveryTracker{recordingTracker: &recordingTracker{}, projects: []domain.JiraProject{
		{ID: "2", Key: "ZED", Name: "Zed"}, {ID: "1", Key: "OPS", Name: "Operations"},
	}}
	result, err := (&JiraService{tr: tr}).ListProjects(context.Background(), false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Total != 2 || result.Complete || !result.Truncated || result.Projects[0].Key != "OPS" {
		t.Fatalf("result=%+v", result)
	}
}

func TestJiraDiscoveryMarkdownEscapesBackendText(t *testing.T) {
	result := &JiraProjectListResult{Projects: []domain.JiraProject{{Key: "OPS|X", Name: "Line\nTwo", ProjectTypeKey: "business"}}}
	markdown := JiraProjectsMarkdown(result)
	if strings.Contains(markdown, "OPS|X") || strings.Contains(markdown, "Line\nTwo") || !strings.Contains(markdown, `OPS\|X`) {
		t.Fatalf("unsafe markdown:\n%s", markdown)
	}
}
