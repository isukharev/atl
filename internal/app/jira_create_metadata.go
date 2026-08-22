package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraCreateMetadataSchemaVersion = 1
	jiraCreateMetadataMaxItems      = 1000
	jiraCreateMetadataMaxRequests   = 64
	jiraCreateMetadataMaxBytes      = int64(16 << 20)
	jiraCreateMetadataDeadline      = 60 * time.Second
)

type JiraCreateMetadataQualification struct {
	SchemaComplete       bool `json:"schema_complete"`
	DefaultComplete      bool `json:"default_complete"`
	OmittabilityComplete bool `json:"omittability_complete"`
}

type JiraCreateMetadataBounds struct {
	MaxIssueTypes     int   `json:"max_issue_types"`
	MaxFields         int   `json:"max_fields"`
	MaxRequests       int   `json:"max_requests"`
	MaxResponseBytes  int64 `json:"max_response_bytes"`
	DeadlineMillis    int64 `json:"deadline_millis"`
	RequestsUsed      int   `json:"requests_used"`
	ResponseBytesUsed int64 `json:"response_bytes_used"`
}

type JiraCreateAllowedValues struct {
	Mode        string `json:"mode"`
	InlineCount int    `json:"inline_count"`
	Exhaustive  bool   `json:"exhaustive"`
}

type JiraCreateMetadataField struct {
	FieldID           string                        `json:"field_id"`
	Name              string                        `json:"name"`
	Required          *bool                         `json:"required"`
	Schema            *domain.JiraCreateFieldSchema `json:"schema"`
	DefaultState      string                        `json:"default_state"`
	AllowedValues     JiraCreateAllowedValues       `json:"allowed_values"`
	Omittability      string                        `json:"omittability"`
	OmittabilityBasis string                        `json:"omittability_basis"`
}

type JiraQualifiedCreateMetadataResult struct {
	SchemaVersion int                             `json:"schema_version"`
	Project       string                          `json:"project"`
	IssueType     domain.JiraIssueType            `json:"issue_type"`
	Count         int                             `json:"count"`
	Complete      bool                            `json:"complete"`
	Qualification JiraCreateMetadataQualification `json:"qualification"`
	Bounds        JiraCreateMetadataBounds        `json:"bounds"`
	Fields        []JiraCreateMetadataField       `json:"fields"`
}

// jiraCreateMetadataError keeps backend-controlled diagnostics out of the
// public error string while preserving the original sentinel identity.
type jiraCreateMetadataError struct {
	cause error
}

func (e *jiraCreateMetadataError) Error() string { return "jira create metadata read failed" }

func (e *jiraCreateMetadataError) Is(target error) bool {
	return e != nil && errors.Is(e.cause, target)
}

func (e *jiraCreateMetadataError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		safe = strconv.Quote(safe)
	}
	_, _ = io.WriteString(state, safe)
}

func (s *JiraService) InspectCreateMetadata(ctx context.Context, project, issueType string) (*JiraQualifiedCreateMetadataResult, error) {
	project, issueType = strings.TrimSpace(project), strings.TrimSpace(issueType)
	if project == "" || issueType == "" {
		return nil, fmt.Errorf("%w: Jira project and issue type are required", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.JiraQualifiedCreateMetadataReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira qualified create metadata discovery is unavailable", domain.ErrConfig)
	}
	budget, err := domain.NewReadBudget(jiraCreateMetadataMaxRequests, jiraCreateMetadataMaxBytes)
	if err != nil {
		return nil, contentFreeCreateMetadataError(err)
	}
	bounded, cancel := context.WithTimeout(domain.WithReadBudget(domain.WithRedactedHTTPTrace(ctx), budget), jiraCreateMetadataDeadline)
	defer cancel()
	metadata, err := reader.ReadQualifiedCreateMetadata(bounded, project, issueType)
	if err != nil {
		return nil, contentFreeCreateMetadataError(err)
	}
	if err := bounded.Err(); err != nil {
		return nil, contentFreeCreateMetadataError(err)
	}
	if metadata == nil {
		return nil, contentFreeCreateMetadataError(domain.ErrCheckFailed)
	}

	fields := make([]JiraCreateMetadataField, 0, len(metadata.Fields))
	qualification := JiraCreateMetadataQualification{SchemaComplete: true, DefaultComplete: true, OmittabilityComplete: true}
	for _, raw := range metadata.Fields {
		field := projectQualifiedCreateField(raw)
		if field.Schema == nil {
			qualification.SchemaComplete = false
		}
		if raw.HasDefaultValue == nil {
			qualification.DefaultComplete = false
		}
		if field.Omittability == "unknown" {
			qualification.OmittabilityComplete = false
		}
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].FieldID < fields[j].FieldID })
	usage := budget.Usage()
	return &JiraQualifiedCreateMetadataResult{
		SchemaVersion: jiraCreateMetadataSchemaVersion,
		Project:       metadata.Project,
		IssueType:     metadata.IssueType,
		Count:         len(fields),
		Complete:      true,
		Qualification: qualification,
		Bounds: JiraCreateMetadataBounds{
			MaxIssueTypes:     jiraCreateMetadataMaxItems,
			MaxFields:         jiraCreateMetadataMaxItems,
			MaxRequests:       jiraCreateMetadataMaxRequests,
			MaxResponseBytes:  jiraCreateMetadataMaxBytes,
			DeadlineMillis:    jiraCreateMetadataDeadline.Milliseconds(),
			RequestsUsed:      usage.Attempts,
			ResponseBytesUsed: usage.ResponseBytes,
		},
		Fields: fields,
	}, nil
}

func projectQualifiedCreateField(raw domain.JiraQualifiedCreateField) JiraCreateMetadataField {
	field := JiraCreateMetadataField{
		FieldID:           raw.FieldID,
		Name:              raw.Name,
		Required:          raw.Required,
		Schema:            raw.Schema,
		DefaultState:      "unknown",
		AllowedValues:     qualifiedAllowedValues(raw),
		Omittability:      "unknown",
		OmittabilityBasis: "metadata_unqualified",
	}
	if raw.HasDefaultValue != nil {
		if *raw.HasDefaultValue {
			field.DefaultState = "present"
		} else {
			field.DefaultState = "absent"
		}
	}
	if raw.Required == nil {
		return field
	}
	if !*raw.Required {
		field.Omittability = "omittable"
		field.OmittabilityBasis = "not_required"
		return field
	}
	if raw.HasDefaultValue == nil {
		return field
	}
	if *raw.HasDefaultValue {
		field.Omittability = "omittable"
		field.OmittabilityBasis = "backend_default"
	} else {
		field.Omittability = "must_supply"
		field.OmittabilityBasis = "required_without_default"
	}
	return field
}

func qualifiedAllowedValues(raw domain.JiraQualifiedCreateField) JiraCreateAllowedValues {
	mode := "not_advertised"
	switch {
	case raw.AllowedValuesPresent && raw.HasAutocomplete:
		mode = "inline_and_autocomplete"
	case raw.AllowedValuesPresent:
		mode = "inline"
	case raw.HasAutocomplete:
		mode = "autocomplete"
	}
	return JiraCreateAllowedValues{
		Mode:        mode,
		InlineCount: raw.AllowedValuesCount,
		Exhaustive:  raw.AllowedValuesPresent && !raw.HasAutocomplete,
	}
}

func contentFreeCreateMetadataError(err error) error {
	if err == nil {
		return nil
	}
	return &jiraCreateMetadataError{cause: err}
}

func JiraQualifiedCreateMetadataMarkdown(result *JiraQualifiedCreateMetadataResult) string {
	rows := make([][]string, 0, len(result.Fields))
	for _, field := range result.Fields {
		required := "unknown"
		if field.Required != nil {
			required = strconv.FormatBool(*field.Required)
		}
		schema := "unknown"
		if field.Schema != nil {
			schema = field.Schema.Type
			if field.Schema.Items != "" {
				schema += "[" + field.Schema.Items + "]"
			}
		}
		allowed := field.AllowedValues.Mode + " (" + strconv.Itoa(field.AllowedValues.InlineCount) + ")"
		rows = append(rows, []string{
			markdownSingleLine(field.FieldID), markdownSingleLine(field.Name), required,
			markdownSingleLine(schema), field.DefaultState, markdownSingleLine(allowed), field.Omittability,
		})
	}
	return MarkdownTable([]string{"Field ID", "Name", "Required", "Schema", "Default", "Allowed values", "Omittability"}, rows)
}
