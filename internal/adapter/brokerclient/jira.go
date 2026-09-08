package brokerclient

import (
	"context"
	"io"
	"maps"
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

// Jira exposes the existing Tracker port while admitting only its exact issue
// read. Every other method returns the closed unsupported Broker reason.
type Jira struct{ client *Client }

var _ domain.Tracker = (*Jira)(nil)
var _ domain.Agile = (*Jira)(nil)
var _ domain.StructureReader = (*Jira)(nil)

func NewJira(client *Client) (*Jira, error) {
	if client == nil {
		return nil, clientError(domain.ErrConfig)
	}
	return &Jira{client: client}, nil
}

func (j *Jira) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	requested := make([]domain.BrokerJiraIssueField, len(fields))
	for index, field := range fields {
		requested[index] = domain.BrokerJiraIssueField(field)
	}
	sort.Slice(requested, func(i, k int) bool { return requested[i] < requested[k] })
	result, err := j.client.ReadJiraIssue(ctx, key, requested)
	if err != nil {
		return nil, err
	}
	values := make(map[string]any, len(result.Fields))
	issue := &domain.Issue{ID: result.IssueID, Key: result.Key, Project: result.Project, Fields: values}
	for _, field := range result.Fields {
		var value any = field.Value
		if field.Null {
			value = nil
		}
		values[string(field.Field)] = value
		switch field.Field {
		case domain.BrokerJiraIssueFieldSummary:
			issue.Summary = field.Value
		case domain.BrokerJiraIssueFieldDescription:
			issue.Body = field.Value
		}
	}
	issue.Raw = maps.Clone(values)
	issue.FieldText = map[string]string{}
	return issue, nil
}

func (*Jira) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return nil, "", unsupported()
}
func (*Jira) Create(context.Context, string, string, string, []byte, map[string]domain.JiraFieldInput) (*domain.Issue, error) {
	return nil, unsupported()
}
func (*Jira) Update(context.Context, string, string, []byte, map[string]domain.JiraFieldInput) error {
	return unsupported()
}
func (*Jira) SetFields(context.Context, string, map[string]any) error { return unsupported() }
func (*Jira) Transition(context.Context, string, string, string, map[string]domain.JiraFieldInput) error {
	return unsupported()
}
func (*Jira) DeleteIssue(context.Context, string, bool) error { return unsupported() }
func (*Jira) UpdateLabels(context.Context, string, []string, []string) error {
	return unsupported()
}
func (*Jira) Assign(context.Context, string, string) error { return unsupported() }
func (*Jira) AddComment(context.Context, string, []byte) (*domain.Comment, error) {
	return nil, unsupported()
}
func (*Jira) ListComments(context.Context, string) ([]domain.Comment, error) {
	return nil, unsupported()
}
func (*Jira) DeleteComment(context.Context, string, string) error { return unsupported() }
func (*Jira) Link(context.Context, string, string, string) error  { return unsupported() }
func (*Jira) DeleteLink(context.Context, string) error            { return unsupported() }
func (*Jira) LinkEpic(context.Context, string, string) error      { return unsupported() }
func (*Jira) Changelog(context.Context, string) ([]domain.ChangelogEntry, error) {
	return nil, unsupported()
}
func (*Jira) CurrentUser(context.Context) (*domain.User, error) { return nil, unsupported() }
func (*Jira) SearchUsers(context.Context, string, int) ([]domain.User, error) {
	return nil, unsupported()
}
func (*Jira) GetUser(context.Context, string) (*domain.User, error) { return nil, unsupported() }
func (*Jira) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	return nil, unsupported()
}
func (*Jira) DownloadAttachment(context.Context, string, string) (io.ReadCloser, string, error) {
	return nil, "", unsupported()
}
func (*Jira) StreamAttachment(context.Context, string) (io.ReadCloser, error) {
	return nil, unsupported()
}
func (*Jira) UploadAttachment(context.Context, string, string, io.Reader, int64) (*domain.Attachment, error) {
	return nil, unsupported()
}
func (*Jira) Fields(context.Context) ([]domain.FieldDef, error) { return nil, unsupported() }
func (*Jira) FieldOptions(context.Context, string, string, string) ([]string, error) {
	return nil, unsupported()
}
func (*Jira) Transitions(context.Context, string) ([]domain.TransitionDef, error) {
	return nil, unsupported()
}
func (*Jira) LinkTypes(context.Context) ([]string, error) { return nil, unsupported() }
func (*Jira) Boards(context.Context, string, int, string) ([]domain.Board, string, error) {
	return nil, "", unsupported()
}
func (*Jira) Board(context.Context, int) (*domain.Board, error) { return nil, unsupported() }
func (*Jira) BoardConfiguration(context.Context, int) (*domain.BoardConfiguration, error) {
	return nil, unsupported()
}
func (*Jira) BoardIssues(context.Context, int, []string, string, int, string) ([]domain.Issue, string, error) {
	return nil, "", unsupported()
}
func (*Jira) BoardBacklog(context.Context, int, []string, string, int, string) ([]domain.Issue, string, error) {
	return nil, "", unsupported()
}
func (*Jira) Sprints(context.Context, int, string, int, string) ([]domain.Sprint, string, error) {
	return nil, "", unsupported()
}
func (*Jira) Sprint(context.Context, int) (*domain.Sprint, error) { return nil, unsupported() }
func (*Jira) SprintIssues(context.Context, int, []string, int, string) ([]domain.Issue, string, error) {
	return nil, "", unsupported()
}
func (*Jira) MoveIssuesToSprint(context.Context, int, []string) error { return unsupported() }
func (*Jira) MoveIssuesToBacklog(context.Context, []string) error     { return unsupported() }
func (*Jira) GetStructure(context.Context, int64) (*domain.Structure, error) {
	return nil, unsupported()
}
func (*Jira) StructureForest(context.Context, int64) (*domain.StructureForest, error) {
	return nil, unsupported()
}
func (*Jira) StructureValues(context.Context, int64, []int64, []string) (*domain.StructureValues, error) {
	return nil, unsupported()
}
