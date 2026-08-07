package domain

import "context"

type JiraProject struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Name           string `json:"name"`
	ProjectTypeKey string `json:"project_type_key,omitempty"`
	Archived       *bool  `json:"archived,omitempty"`
}

type JiraIssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type JiraCreateField struct {
	FieldID          string `json:"field_id"`
	Name             string `json:"name"`
	Required         bool   `json:"required"`
	HasAllowedValues bool   `json:"has_allowed_values"`
}

type JiraCreateMetadata struct {
	Project   string            `json:"project"`
	IssueType JiraIssueType     `json:"issue_type"`
	Fields    []JiraCreateField `json:"fields"`
}

// JiraProjectReader is the optional atomic Jira Data Center project inventory.
type JiraProjectReader interface {
	ReadProjects(ctx context.Context, includeArchived bool) ([]JiraProject, error)
}

// JiraCreateMetadataReader exposes content-free create-screen schema without
// growing the broad Tracker port.
type JiraCreateMetadataReader interface {
	ReadCreateIssueTypes(ctx context.Context, project string) ([]JiraIssueType, error)
	ReadCreateMetadata(ctx context.Context, project, issueType string) (*JiraCreateMetadata, error)
}

// JiraEpicFieldLinker writes an already-resolved Epic Link field. The adapter
// retains target authorization and transport clearance.
type JiraEpicFieldLinker interface {
	LinkEpicWithField(ctx context.Context, issue, epic, fieldID string) error
}
