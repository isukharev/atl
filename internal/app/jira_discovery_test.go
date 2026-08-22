package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type discoveryTracker struct {
	*recordingTracker
	projects   []domain.JiraProject
	issueTypes []domain.JiraIssueType
	metadata   *domain.JiraCreateMetadata
	qualified  *domain.JiraQualifiedCreateMetadata
}

type privateCreateMetadataCause struct {
	message string
}

func (e *privateCreateMetadataCause) Error() string { return e.message }

func (t *discoveryTracker) ReadProjects(context.Context, bool) ([]domain.JiraProject, error) {
	return append([]domain.JiraProject(nil), t.projects...), t.err
}

func (t *discoveryTracker) ReadCreateIssueTypes(context.Context, string) ([]domain.JiraIssueType, error) {
	return append([]domain.JiraIssueType(nil), t.issueTypes...), t.err
}

func (t *discoveryTracker) ReadCreateMetadata(context.Context, string, string) (*domain.JiraCreateMetadata, error) {
	return t.metadata, t.err
}

func (t *discoveryTracker) ReadQualifiedCreateMetadata(context.Context, string, string) (*domain.JiraQualifiedCreateMetadata, error) {
	return t.qualified, t.err
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

func TestProjectQualifiedCreateFieldOmittabilityAndAllowedModes(t *testing.T) {
	boolp := func(value bool) *bool { return &value }
	tests := []struct {
		name       string
		field      domain.JiraQualifiedCreateField
		omit       string
		basis      string
		defaultVal string
		mode       string
		exhaustive bool
	}{
		{name: "optional", field: domain.JiraQualifiedCreateField{Required: boolp(false)}, omit: "omittable", basis: "not_required", defaultVal: "unknown", mode: "not_advertised"},
		{name: "required default", field: domain.JiraQualifiedCreateField{Required: boolp(true), HasDefaultValue: boolp(true), AllowedValuesPresent: true, AllowedValuesCount: 2}, omit: "omittable", basis: "backend_default", defaultVal: "present", mode: "inline", exhaustive: true},
		{name: "required no default", field: domain.JiraQualifiedCreateField{Required: boolp(true), HasDefaultValue: boolp(false), HasAutocomplete: true}, omit: "must_supply", basis: "required_without_default", defaultVal: "absent", mode: "autocomplete"},
		{name: "unknown required", field: domain.JiraQualifiedCreateField{HasDefaultValue: boolp(false), AllowedValuesPresent: true, HasAutocomplete: true}, omit: "unknown", basis: "metadata_unqualified", defaultVal: "absent", mode: "inline_and_autocomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := projectQualifiedCreateField(test.field)
			if got.Omittability != test.omit || got.OmittabilityBasis != test.basis || got.DefaultState != test.defaultVal ||
				got.AllowedValues.Mode != test.mode || got.AllowedValues.Exhaustive != test.exhaustive {
				t.Fatalf("field=%+v", got)
			}
		})
	}
}

func TestInspectCreateMetadataQualifiesFactsAndRedactsFailure(t *testing.T) {
	trueValue, falseValue := true, false
	customID := int64(42)
	tracker := &discoveryTracker{recordingTracker: &recordingTracker{}, qualified: &domain.JiraQualifiedCreateMetadata{
		Project: "OPS", IssueType: domain.JiraIssueType{ID: "10", Name: "Task"},
		Fields: []domain.JiraQualifiedCreateField{
			{FieldID: "summary", Name: "Summary", Required: &trueValue, HasDefaultValue: &falseValue, Schema: &domain.JiraCreateFieldSchema{Type: "string", System: "summary"}},
			{FieldID: "customfield_42", Name: "Choice", Required: &falseValue, HasDefaultValue: &trueValue, Schema: &domain.JiraCreateFieldSchema{Type: "array", Items: "option", CustomID: &customID}, AllowedValuesPresent: true},
		},
	}}
	result, err := (&JiraService{tr: tracker}).InspectCreateMetadata(context.Background(), " OPS ", " Task ")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Count != 2 || !result.Qualification.SchemaComplete || !result.Qualification.DefaultComplete ||
		!result.Qualification.OmittabilityComplete || result.Bounds.MaxRequests != 64 || result.Bounds.MaxResponseBytes != 16<<20 ||
		result.Fields[0].FieldID != "customfield_42" || result.Fields[1].Omittability != "must_supply" {
		t.Fatalf("result=%+v", result)
	}

	tracker.err = errors.New("private backend response canary")
	_, err = (&JiraService{tr: tracker}).InspectCreateMetadata(context.Background(), "OPS", "Task")
	if err == nil || strings.Contains(err.Error(), "private backend response canary") || err.Error() != "jira create metadata read failed" {
		t.Fatalf("content-free error=%v", err)
	}
}

func TestInspectCreateMetadataRejectsExpiredContextAfterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tracker := &discoveryTracker{recordingTracker: &recordingTracker{}, qualified: &domain.JiraQualifiedCreateMetadata{}}
	_, err := (&JiraService{tr: tracker}).InspectCreateMetadata(ctx, "OPS", "Task")
	if !errors.Is(err, context.Canceled) || err.Error() != "jira create metadata read failed" {
		t.Fatalf("deadline error=%v", err)
	}
}

func TestContentFreeCreateMetadataErrorPreservesSentinels(t *testing.T) {
	causes := []error{
		domain.ErrReadAttemptBudgetExhausted,
		domain.ErrReadResponseBudgetExhausted,
		context.Canceled,
		context.DeadlineExceeded,
		domain.ErrForbidden,
	}
	for _, cause := range causes {
		err := contentFreeCreateMetadataError(cause)
		if !errors.Is(err, cause) || err.Error() != "jira create metadata read failed" {
			t.Fatalf("cause=%v error=%v", cause, err)
		}
	}

	private := &privateCreateMetadataCause{message: "private backend response canary"}
	err := contentFreeCreateMetadataError(private)
	var exposed *privateCreateMetadataCause
	if errors.Unwrap(err) != nil || errors.As(err, &exposed) {
		t.Fatalf("private cause escaped the content-free boundary: %#v", err)
	}
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err)} {
		if strings.Contains(rendered, private.message) {
			t.Fatalf("formatted error leaked private cause: %s", rendered)
		}
	}
}
