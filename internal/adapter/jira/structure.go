package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
)

var _ domain.StructureReader = (*Jira)(nil)

// GetStructure fetches read-only metadata for one Tempo Structure.
func (j *Jira) GetStructure(ctx context.Context, id int64) (*domain.Structure, error) {
	q := url.Values{}
	q.Set("withPermissions", "true")
	q.Set("withOwner", "true")
	var d structureDTO
	path := "/rest/structure/2.0/structure/" + strconv.FormatInt(id, 10) + "?" + q.Encode()
	if err := j.c.GetJSON(ctx, path, &d); err != nil {
		return nil, err
	}
	s := d.toDomain()
	return &s, nil
}

// StructureForest returns the latest forest formula for one Structure.
func (j *Jira) StructureForest(ctx context.Context, id int64) (*domain.StructureForest, error) {
	spec := url.QueryEscape(fmt.Sprintf(`{"structureId":%d}`, id))
	var d structureForestDTO
	if err := j.c.GetJSON(ctx, "/rest/structure/2.0/forest/latest?s="+spec, &d); err != nil {
		return nil, err
	}
	f := d.toDomain()
	return &f, nil
}

// StructureValues fetches attribute values for selected Structure rows.
func (j *Jira) StructureValues(ctx context.Context, id int64, rows []int64, fields []string) (*domain.StructureValues, error) {
	attrs := make([]map[string]string, 0, len(fields))
	for _, field := range fields {
		attrs = append(attrs, map[string]string{"id": field, "format": "text"})
	}
	payload := map[string]any{
		"requests": []any{map[string]any{
			"forestSpec": map[string]any{"structureId": id},
			"rows":       rows,
			"attributes": attrs,
		}},
	}
	var raw map[string]any
	if err := j.c.SendJSON(ctx, http.MethodPost, "/rest/structure/2.0/value", payload, &raw); err != nil {
		return nil, err
	}
	return mapStructureValues(raw), nil
}

func mapStructureValues(raw map[string]any) *domain.StructureValues {
	v := &domain.StructureValues{Raw: raw, InaccessibleRows: []int64{}}
	if responses, ok := raw["responses"].([]any); ok {
		v.Responses = make([]map[string]any, 0, len(responses))
		for _, entry := range responses {
			if m, ok := entry.(map[string]any); ok {
				v.Responses = append(v.Responses, m)
				v.InaccessibleRows = append(v.InaccessibleRows, extractInt64s(m["inaccessibleRows"])...)
				if len(v.ItemTypes) == 0 {
					v.ItemTypes = stringMap(m["itemTypes"])
				}
				if v.ItemsVersion == (domain.StructureVersion{}) {
					v.ItemsVersion = structureVersionFromAny(m["itemsVersion"])
				}
				if v.ForestVersion == (domain.StructureVersion{}) {
					v.ForestVersion = structureVersionFromAny(m["forestVersion"])
				}
			}
		}
	}
	v.InaccessibleRows = append(v.InaccessibleRows, extractInt64s(raw["inaccessibleRows"])...)
	if len(v.ItemTypes) == 0 {
		v.ItemTypes = stringMap(raw["itemTypes"])
	}
	if v.ItemsVersion == (domain.StructureVersion{}) {
		v.ItemsVersion = structureVersionFromAny(raw["itemsVersion"])
	}
	if v.ForestVersion == (domain.StructureVersion{}) {
		v.ForestVersion = structureVersionFromAny(raw["forestVersion"])
	}
	return v
}

func extractInt64s(v any) []int64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, raw := range arr {
		switch n := raw.(type) {
		case float64:
			out = append(out, int64(n))
		case int64:
			out = append(out, n)
		case int:
			out = append(out, int64(n))
		case string:
			if parsed, err := strconv.ParseInt(n, 10, 64); err == nil {
				out = append(out, parsed)
			}
		}
	}
	return out
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, raw := range m {
		out[k] = str(raw)
	}
	return out
}

func structureVersionFromAny(v any) domain.StructureVersion {
	m, ok := v.(map[string]any)
	if !ok {
		return domain.StructureVersion{}
	}
	return domain.StructureVersion{
		Signature: int64FromAny(m["signature"]),
		Version:   int64FromAny(m["version"]),
	}
}

func int64FromAny(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(n, 10, 64)
		return parsed
	default:
		return 0
	}
}

type structureDTO struct {
	ID                                int64            `json:"id"`
	Name                              string           `json:"name"`
	Description                       string           `json:"description"`
	ReadOnly                          bool             `json:"readOnly"`
	EditRequiresParentIssuePermission bool             `json:"editRequiresParentIssuePermission"`
	Owner                             any              `json:"owner"`
	Permissions                       []map[string]any `json:"permissions"`
	Views                             []map[string]any `json:"views"`
}

func (d structureDTO) toDomain() domain.Structure {
	return domain.Structure{
		ID:                                d.ID,
		Name:                              d.Name,
		Description:                       d.Description,
		ReadOnly:                          d.ReadOnly,
		EditRequiresParentIssuePermission: d.EditRequiresParentIssuePermission,
		Owner:                             d.Owner,
		Permissions:                       d.Permissions,
		Views:                             d.Views,
	}
}

type structureForestDTO struct {
	Spec      map[string]any          `json:"spec"`
	Formula   string                  `json:"formula"`
	ItemTypes map[string]string       `json:"itemTypes"`
	Version   domain.StructureVersion `json:"version"`
}

func (d structureForestDTO) toDomain() domain.StructureForest {
	return domain.StructureForest{
		Spec:      d.Spec,
		Formula:   d.Formula,
		ItemTypes: d.ItemTypes,
		Version:   d.Version,
	}
}
