package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// JiraFieldCatalogOpts narrows Jira's field catalog without reading any issue
// values. Custom accepts the same empty/true/false forms as the CLI contract.
type JiraFieldCatalogOpts struct {
	ID       string
	NameLike string
	IDLike   string
	Schema   string
	Custom   string
	// SummaryOnly preserves qualification and reconciled counts while omitting
	// field definitions from the result.
	SummaryOnly bool
}

type JiraFieldCatalogResult struct {
	SchemaVersion int               `json:"schema_version"`
	Projection    string            `json:"projection"`
	Source        string            `json:"source"`
	Complete      bool              `json:"complete"`
	PartialReason string            `json:"partial_reason,omitempty"`
	Total         int               `json:"total"`
	Count         int               `json:"count"`
	CustomCount   int               `json:"custom_count"`
	SystemCount   int               `json:"system_count"`
	Fields        []domain.FieldDef `json:"fields"`
}

// FieldCatalog lists and filters field definitions in the application layer so
// CLI and MCP transports share one exact selection contract.
func (s *JiraService) FieldCatalog(ctx context.Context, opts JiraFieldCatalogOpts) (*JiraFieldCatalogResult, error) {
	snapshot := domain.FieldCatalogSnapshot{Complete: false, PartialReason: "tracker does not expose field-catalog completeness"}
	source := "legacy"
	var err error
	if qualified, ok := s.tr.(domain.QualifiedFieldCatalogReader); ok {
		snapshot, err = qualified.ReadFieldCatalog(ctx)
		source = "jira-field-catalog"
	} else {
		snapshot.Fields, err = s.Fields(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err := validateFieldCatalogSnapshot(snapshot); err != nil {
		return nil, err
	}
	total := len(snapshot.Fields)
	fields, err := FilterFieldDefs(snapshot.Fields, opts)
	if err != nil {
		return nil, err
	}
	customCount := 0
	for _, field := range fields {
		if field.Custom {
			customCount++
		}
	}
	projection := "full"
	resultFields := fields
	if opts.SummaryOnly {
		projection = "summary"
		resultFields = []domain.FieldDef{}
	}
	return &JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: projection, Source: source, Complete: snapshot.Complete,
		PartialReason: snapshot.PartialReason, Total: total, Count: len(fields),
		CustomCount: customCount, SystemCount: len(fields) - customCount, Fields: resultFields,
	}, nil
}

func validateFieldCatalogSnapshot(snapshot domain.FieldCatalogSnapshot) error {
	if snapshot.Complete && strings.TrimSpace(snapshot.PartialReason) != "" {
		return fmt.Errorf("%w: complete Jira field catalog has a partial reason", domain.ErrCheckFailed)
	}
	if snapshot.Complete && len(snapshot.Fields) == 0 {
		return fmt.Errorf("%w: complete Jira field catalog is empty", domain.ErrCheckFailed)
	}
	if !snapshot.Complete && strings.TrimSpace(snapshot.PartialReason) == "" {
		return fmt.Errorf("%w: partial Jira field catalog has no reason", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(snapshot.Fields))
	for _, field := range snapshot.Fields {
		if strings.TrimSpace(field.ID) == "" {
			return fmt.Errorf("%w: Jira field catalog contains an empty field id", domain.ErrCheckFailed)
		}
		if _, exists := seen[field.ID]; exists {
			return fmt.Errorf("%w: Jira field catalog contains duplicate field id %q", domain.ErrCheckFailed, field.ID)
		}
		seen[field.ID] = struct{}{}
	}
	return nil
}

func FilterFieldDefs(fields []domain.FieldDef, opts JiraFieldCatalogOpts) ([]domain.FieldDef, error) {
	id := strings.TrimSpace(opts.ID)
	nameLike := strings.ToLower(strings.TrimSpace(opts.NameLike))
	idLike := strings.ToLower(strings.TrimSpace(opts.IDLike))
	schema := strings.TrimSpace(opts.Schema)
	custom := strings.ToLower(strings.TrimSpace(opts.Custom))
	var wantCustom *bool
	if custom != "" {
		switch custom {
		case "true", "1", "yes":
			value := true
			wantCustom = &value
		case "false", "0", "no":
			value := false
			wantCustom = &value
		default:
			return nil, fmt.Errorf("%w: --custom must be true or false", domain.ErrUsage)
		}
	}
	if id == "" && nameLike == "" && idLike == "" && schema == "" && wantCustom == nil {
		return fields, nil
	}
	out := make([]domain.FieldDef, 0, len(fields))
	for _, field := range fields {
		if id != "" && field.ID != id {
			continue
		}
		if idLike != "" && !strings.Contains(strings.ToLower(field.ID), idLike) {
			continue
		}
		if nameLike != "" && !strings.Contains(strings.ToLower(field.Name), nameLike) {
			continue
		}
		if schema != "" && field.Schema != schema {
			continue
		}
		if wantCustom != nil && field.Custom != *wantCustom {
			continue
		}
		out = append(out, field)
	}
	return out, nil
}
