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
}

type JiraFieldCatalogResult struct {
	Fields []domain.FieldDef `json:"fields"`
}

// FieldCatalog lists and filters field definitions in the application layer so
// CLI and MCP transports share one exact selection contract.
func (s *JiraService) FieldCatalog(ctx context.Context, opts JiraFieldCatalogOpts) (*JiraFieldCatalogResult, error) {
	fields, err := s.Fields(ctx)
	if err != nil {
		return nil, err
	}
	fields, err = FilterFieldDefs(fields, opts)
	if err != nil {
		return nil, err
	}
	return &JiraFieldCatalogResult{Fields: fields}, nil
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
