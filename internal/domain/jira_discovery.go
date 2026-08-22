package domain

import (
	"context"
	"fmt"
	"strings"
)

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

// ResolveJiraIssueType selects an exact Jira issue type by immutable id first,
// then by an exact unambiguous display name. Callers use the returned id for a
// create request and retain the name only for presentation and readback checks.
func ResolveJiraIssueType(types []JiraIssueType, selector string) (JiraIssueType, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return JiraIssueType{}, fmt.Errorf("%w: Jira issue type is required", ErrUsage)
	}
	for _, issueType := range types {
		if issueType.ID == selector {
			return issueType, nil
		}
	}
	matches := make([]JiraIssueType, 0, 1)
	for _, issueType := range types {
		if issueType.Name == selector {
			matches = append(matches, issueType)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return JiraIssueType{}, fmt.Errorf("%w: issue type was not found in project create metadata; run 'atl jira issue types --project PROJECT' and use an exact id or name", ErrNotFound)
	default:
		return JiraIssueType{}, fmt.Errorf("%w: issue type selector is ambiguous", ErrCheckFailed)
	}
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

// JiraCreateFieldSchema is the allowlisted, content-free subset of Jira's
// create-field JsonTypeBean. Default values, autocomplete URLs, operations,
// and backend-controlled option values never enter this domain contract.
type JiraCreateFieldSchema struct {
	Type     string `json:"type"`
	Items    string `json:"items,omitempty"`
	System   string `json:"system,omitempty"`
	Custom   string `json:"custom,omitempty"`
	CustomID *int64 `json:"custom_id,omitempty"`
}

// JiraQualifiedCreateField preserves metadata presence separately from false.
// AllowedValuesPresent distinguishes an explicitly advertised empty inline
// enumeration from a field for which Jira advertised no inline enumeration.
type JiraQualifiedCreateField struct {
	FieldID              string
	Name                 string
	Required             *bool
	Schema               *JiraCreateFieldSchema
	HasDefaultValue      *bool
	AllowedValuesPresent bool
	AllowedValuesCount   int
	HasAutocomplete      bool
}

// JiraQualifiedCreateMetadata is one terminal, project/type-scoped create
// metadata inventory. Implementations return an error instead of this value
// when pagination or a hard collection bound is not qualified.
type JiraQualifiedCreateMetadata struct {
	Project   string
	IssueType JiraIssueType
	Fields    []JiraQualifiedCreateField
}

// JiraProjectReader is the optional atomic Jira Data Center project inventory.
type JiraProjectReader interface {
	ReadProjects(ctx context.Context, includeArchived bool) ([]JiraProject, error)
}

// JiraCreateIssueTypeReader exposes a project-scoped issue-type inventory
// without growing the broad Tracker port.
type JiraCreateIssueTypeReader interface {
	ReadCreateIssueTypes(ctx context.Context, project string) ([]JiraIssueType, error)
}

// JiraCreateMetadataReader exposes content-free create-screen schema without
// growing the broad Tracker port.
type JiraCreateMetadataReader interface {
	JiraCreateIssueTypeReader
	ReadCreateMetadata(ctx context.Context, project, issueType string) (*JiraCreateMetadata, error)
}

// JiraQualifiedCreateMetadataReader exposes the safe presence-qualified
// create-screen facts used by bounded preflight. It remains separate from the
// legacy create-check reader so that projection stays byte-compatible.
type JiraQualifiedCreateMetadataReader interface {
	ReadQualifiedCreateMetadata(ctx context.Context, project, issueType string) (*JiraQualifiedCreateMetadata, error)
}

// JiraEpicFieldLinker writes an already-resolved Epic Link field. The adapter
// retains target authorization and transport clearance.
type JiraEpicFieldLinker interface {
	LinkEpicWithField(ctx context.Context, issue, epic, fieldID string) error
}
