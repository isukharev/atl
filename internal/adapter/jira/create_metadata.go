package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	createMetaPageSize = 200
	createMetaMaxItems = 1000
)

type createIssueTypeDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type createFieldDTO struct {
	FieldID       string `json:"fieldId"`
	Name          string `json:"name"`
	Required      bool   `json:"required"`
	AllowedValues []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"allowedValues"`
}

type createFieldSchemaDTO struct {
	Type     string `json:"type"`
	Items    string `json:"items"`
	System   string `json:"system"`
	Custom   string `json:"custom"`
	CustomID *int64 `json:"customId"`
}

type qualifiedCreateFieldDTO struct {
	FieldID             string                            `json:"fieldId"`
	Name                string                            `json:"name"`
	Required            *bool                             `json:"required"`
	Schema              *createFieldSchemaDTO             `json:"schema"`
	HasDefaultValue     *bool                             `json:"hasDefaultValue"`
	AllowedValues       qualifiedCreateAllowedValuesDTO   `json:"allowedValues"`
	AutoCompleteURL     qualifiedCreateAutocompleteMarker `json:"autoCompleteUrl"`
	AutocompletePresent bool                              `json:"-"`
}

func (f *qualifiedCreateFieldDTO) UnmarshalJSON(data []byte) error {
	type wire qualifiedCreateFieldDTO
	var decoded wire
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*f = qualifiedCreateFieldDTO(decoded)
	_, f.AutocompletePresent = members["autoCompleteUrl"]
	return nil
}

// qualifiedCreateAllowedValuesDTO records only member presence and array
// cardinality. Backend-controlled option labels and values never enter the
// qualified metadata DTO or domain contract.
type qualifiedCreateAllowedValuesDTO struct {
	Present bool
	Count   int
}

func (a *qualifiedCreateAllowedValuesDTO) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return fmt.Errorf("jira allowed-values metadata is not an array")
	}
	var values []struct{}
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	a.Present = true
	a.Count = len(values)
	return nil
}

// qualifiedCreateAutocompleteMarker records only whether Jira advertised a
// non-null autocomplete member. The URL itself is never decoded or retained.
type qualifiedCreateAutocompleteMarker bool

func (m *qualifiedCreateAutocompleteMarker) UnmarshalJSON(data []byte) error {
	*m = qualifiedCreateAutocompleteMarker(string(data) != "null")
	return nil
}

func createMetaBase(project string) string {
	return "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"
}

func (j *Jira) readCreateIssueTypes(ctx context.Context, project string) ([]createIssueTypeDTO, error) {
	var out []createIssueTypeDTO
	cursor := jiraOffsetCursor{}
	for {
		var page struct {
			StartAt int                  `json:"startAt"`
			Total   int                  `json:"total"`
			IsLast  bool                 `json:"isLast"`
			Values  []createIssueTypeDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", createMetaBase(project), cursor.requested(), createMetaPageSize)
		if err := j.c.GetJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Total > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		if !cursor.matches(page.StartAt) {
			return nil, fmt.Errorf("%w: Jira create metadata returned offset %d while %d was requested", domain.ErrCheckFailed, page.StartAt, cursor.requested())
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		if page.IsLast || page.Total > 0 && len(out) >= page.Total || page.Total == 0 && len(page.Values) < createMetaPageSize {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		decision := cursor.advance(len(page.Values), nil)
		if decision.state != jiraOffsetMore {
			return nil, fmt.Errorf("%w: Jira create metadata pagination made no progress", domain.ErrCheckFailed)
		}
	}
}

func (j *Jira) collectQualifiedCreateIssueTypes(ctx context.Context, project string) ([]createIssueTypeDTO, error) {
	var out []createIssueTypeDTO
	cursor := jiraOffsetCursor{}
	var qualifiedTotal *int
	for {
		var page struct {
			StartAt *int                 `json:"startAt"`
			Total   *int                 `json:"total"`
			IsLast  *bool                `json:"isLast"`
			Values  []createIssueTypeDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", createMetaBase(project), cursor.requested(), createMetaPageSize)
		if err := j.getStrictJiraEvidenceJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Total != nil && (*page.Total < 0 || *page.Total > createMetaMaxItems) {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		if page.StartAt == nil || !cursor.matches(*page.StartAt) {
			return nil, fmt.Errorf("%w: Jira create metadata pagination is unqualified", domain.ErrCheckFailed)
		}
		if page.Total != nil {
			if qualifiedTotal != nil && *qualifiedTotal != *page.Total {
				return nil, fmt.Errorf("%w: Jira create metadata returned inconsistent totals", domain.ErrCheckFailed)
			}
			total := *page.Total
			qualifiedTotal = &total
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		terminal, err := qualifiedCreateMetaTerminal(page.IsLast, qualifiedTotal, len(out), len(page.Values))
		if err != nil {
			return nil, fmt.Errorf("%w: Jira create metadata %s", domain.ErrCheckFailed, err)
		}
		if terminal {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		decision := cursor.advance(len(page.Values), nil)
		if decision.state != jiraOffsetMore {
			return nil, fmt.Errorf("%w: Jira create metadata pagination made no progress", domain.ErrCheckFailed)
		}
	}
}

func (j *Jira) readCreateFields(ctx context.Context, project, typeID string) ([]createFieldDTO, error) {
	var out []createFieldDTO
	base := createMetaBase(project) + "/" + url.PathEscape(typeID)
	cursor := jiraOffsetCursor{}
	for {
		var page struct {
			StartAt int              `json:"startAt"`
			Total   int              `json:"total"`
			IsLast  bool             `json:"isLast"`
			Values  []createFieldDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", base, cursor.requested(), createMetaPageSize)
		if err := j.c.GetJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Total > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		if !cursor.matches(page.StartAt) {
			return nil, fmt.Errorf("%w: Jira create-screen metadata returned offset %d while %d was requested", domain.ErrCheckFailed, page.StartAt, cursor.requested())
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		if page.IsLast || page.Total > 0 && len(out) >= page.Total || page.Total == 0 && len(page.Values) < createMetaPageSize {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		decision := cursor.advance(len(page.Values), nil)
		if decision.state != jiraOffsetMore {
			return nil, fmt.Errorf("%w: Jira create-screen pagination made no progress", domain.ErrCheckFailed)
		}
	}
}

func (j *Jira) collectQualifiedCreateFields(ctx context.Context, project, typeID string) ([]qualifiedCreateFieldDTO, error) {
	var out []qualifiedCreateFieldDTO
	base := createMetaBase(project) + "/" + url.PathEscape(typeID)
	cursor := jiraOffsetCursor{}
	var qualifiedTotal *int
	for {
		var page struct {
			StartAt *int                      `json:"startAt"`
			Total   *int                      `json:"total"`
			IsLast  *bool                     `json:"isLast"`
			Values  []qualifiedCreateFieldDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", base, cursor.requested(), createMetaPageSize)
		if err := j.getStrictJiraEvidenceJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.StartAt == nil || !cursor.matches(*page.StartAt) {
			return nil, fmt.Errorf("%w: Jira create-screen pagination is unqualified", domain.ErrCheckFailed)
		}
		if page.Total != nil {
			if *page.Total < 0 || *page.Total > createMetaMaxItems {
				return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
			}
			if qualifiedTotal != nil && *qualifiedTotal != *page.Total {
				return nil, fmt.Errorf("%w: Jira create-screen metadata returned inconsistent totals", domain.ErrCheckFailed)
			}
			total := *page.Total
			qualifiedTotal = &total
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		terminal, err := qualifiedCreateMetaTerminal(page.IsLast, qualifiedTotal, len(out), len(page.Values))
		if err != nil {
			return nil, fmt.Errorf("%w: Jira create-screen metadata %s", domain.ErrCheckFailed, err)
		}
		if terminal {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		decision := cursor.advance(len(page.Values), nil)
		if decision.state != jiraOffsetMore {
			return nil, fmt.Errorf("%w: Jira create-screen pagination made no progress", domain.ErrCheckFailed)
		}
	}
}

func qualifiedCreateMetaTerminal(isLast *bool, total *int, collected, pageItems int) (bool, error) {
	if total != nil && collected > *total {
		return false, fmt.Errorf("returned more rows than its total")
	}
	if isLast != nil && *isLast {
		if total != nil && collected != *total {
			return false, fmt.Errorf("marked the page terminal before its total")
		}
		return true, nil
	}
	if total != nil && collected == *total {
		if isLast != nil && !*isLast {
			return false, fmt.Errorf("contradicted its terminal total")
		}
		return true, nil
	}
	if isLast == nil && total == nil && pageItems < createMetaPageSize {
		return false, fmt.Errorf("pagination is unqualified")
	}
	return false, nil
}

func (j *Jira) ReadCreateIssueTypes(ctx context.Context, project string) ([]domain.JiraIssueType, error) {
	raw, err := j.readCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]domain.JiraIssueType, 0, len(raw))
	for _, issueType := range raw {
		if issueType.ID == "" || issueType.Name == "" {
			return nil, fmt.Errorf("%w: Jira issue-type metadata contains an incomplete row", domain.ErrCheckFailed)
		}
		out = append(out, domain.JiraIssueType{ID: issueType.ID, Name: issueType.Name, Subtask: issueType.Subtask})
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Name == out[k].Name {
			return out[i].ID < out[k].ID
		}
		return out[i].Name < out[k].Name
	})
	return out, nil
}

func (j *Jira) ReadCreateMetadata(ctx context.Context, project, selector string) (*domain.JiraCreateMetadata, error) {
	types, err := j.ReadCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	issueType, err := domain.ResolveJiraIssueType(types, selector)
	if err != nil {
		return nil, err
	}
	return j.readCreateMetadataFields(ctx, project, issueType)
}

func (j *Jira) readCreateMetadataFields(ctx context.Context, project string, issueType domain.JiraIssueType) (*domain.JiraCreateMetadata, error) {
	rawFields, err := j.readCreateFields(ctx, project, issueType.ID)
	if err != nil {
		return nil, err
	}
	fields := make([]domain.JiraCreateField, 0, len(rawFields))
	for _, field := range rawFields {
		if field.FieldID == "" || field.Name == "" {
			return nil, fmt.Errorf("%w: Jira create-screen metadata contains an incomplete field", domain.ErrCheckFailed)
		}
		fields = append(fields, domain.JiraCreateField{
			FieldID: field.FieldID, Name: field.Name, Required: field.Required,
			HasAllowedValues: len(field.AllowedValues) > 0,
		})
	}
	sort.Slice(fields, func(i, k int) bool { return fields[i].FieldID < fields[k].FieldID })
	return &domain.JiraCreateMetadata{Project: project, IssueType: issueType, Fields: fields}, nil
}

func (j *Jira) ReadQualifiedCreateMetadata(ctx context.Context, project, selector string) (*domain.JiraQualifiedCreateMetadata, error) {
	rawTypes, err := j.collectQualifiedCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	types := make([]domain.JiraIssueType, 0, len(rawTypes))
	seenTypes := make(map[string]struct{}, len(rawTypes))
	for _, raw := range rawTypes {
		if raw.ID == "" || raw.Name == "" {
			return nil, fmt.Errorf("%w: Jira issue-type metadata contains an incomplete row", domain.ErrCheckFailed)
		}
		if _, duplicate := seenTypes[raw.ID]; duplicate {
			return nil, fmt.Errorf("%w: Jira issue-type metadata contains a duplicate id", domain.ErrCheckFailed)
		}
		seenTypes[raw.ID] = struct{}{}
		types = append(types, domain.JiraIssueType{ID: raw.ID, Name: raw.Name, Subtask: raw.Subtask})
	}
	issueType, err := domain.ResolveJiraIssueType(types, selector)
	if err != nil {
		return nil, err
	}
	rawFields, err := j.collectQualifiedCreateFields(ctx, project, issueType.ID)
	if err != nil {
		return nil, err
	}
	fields := make([]domain.JiraQualifiedCreateField, 0, len(rawFields))
	seenFields := make(map[string]struct{}, len(rawFields))
	for _, raw := range rawFields {
		if raw.FieldID == "" || raw.Name == "" {
			return nil, fmt.Errorf("%w: Jira create-screen metadata contains an incomplete field", domain.ErrCheckFailed)
		}
		if _, duplicate := seenFields[raw.FieldID]; duplicate {
			return nil, fmt.Errorf("%w: Jira create-screen metadata contains a duplicate field id", domain.ErrCheckFailed)
		}
		seenFields[raw.FieldID] = struct{}{}
		field := domain.JiraQualifiedCreateField{
			FieldID: raw.FieldID, Name: raw.Name, Required: raw.Required,
			HasDefaultValue:      raw.HasDefaultValue,
			AllowedValuesPresent: raw.AllowedValues.Present,
			AllowedValuesCount:   raw.AllowedValues.Count,
			AutocompletePresent:  raw.AutocompletePresent,
			HasAutocomplete:      bool(raw.AutoCompleteURL),
		}
		if raw.Schema != nil {
			if strings.TrimSpace(raw.Schema.Type) == "" || raw.Schema.CustomID != nil && *raw.Schema.CustomID < 1 {
				return nil, fmt.Errorf("%w: Jira create-screen metadata contains malformed schema", domain.ErrCheckFailed)
			}
			field.Schema = &domain.JiraCreateFieldSchema{
				Type: raw.Schema.Type, Items: raw.Schema.Items, System: raw.Schema.System,
				Custom: raw.Schema.Custom, CustomID: raw.Schema.CustomID,
			}
		}
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, k int) bool { return fields[i].FieldID < fields[k].FieldID })
	return &domain.JiraQualifiedCreateMetadata{Project: project, IssueType: issueType, Fields: fields}, nil
}

var _ domain.JiraQualifiedCreateMetadataReader = (*Jira)(nil)
