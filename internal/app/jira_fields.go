package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraCompactFieldStringCap = 32 << 10
	jiraCompactFieldArrayCap  = 100
	jiraCompactFieldDepthCap  = 6
)

type JiraIssueFieldsOpts struct {
	Selectors    []string
	IncludeEmpty bool
	Raw          bool
	MetadataOnly bool
}

type JiraIssueFieldRecord struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Custom        bool   `json:"custom"`
	Schema        string `json:"schema,omitempty"`
	Empty         bool   `json:"empty,omitempty"`
	ValueType     string `json:"value_type,omitempty"`
	Value         any    `json:"-"`
	Truncated     bool   `json:"truncated,omitempty"`
	OriginalBytes int    `json:"original_bytes,omitempty"`
	MetadataOnly  bool   `json:"-"`
}

// MarshalJSON omits value entirely in metadata mode while preserving the
// existing explicit `"value":null` contract for compact/raw empty fields.
func (r JiraIssueFieldRecord) MarshalJSON() ([]byte, error) {
	type wireRecord struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Custom        bool   `json:"custom"`
		Schema        string `json:"schema,omitempty"`
		Empty         bool   `json:"empty,omitempty"`
		ValueType     string `json:"value_type,omitempty"`
		Value         *any   `json:"value,omitempty"`
		Truncated     bool   `json:"truncated,omitempty"`
		OriginalBytes int    `json:"original_bytes,omitempty"`
	}
	var value *any
	if !r.MetadataOnly {
		value = &r.Value
	}
	return json.Marshal(wireRecord{
		ID: r.ID, Name: r.Name, Custom: r.Custom, Schema: r.Schema, Empty: r.Empty,
		ValueType: r.ValueType, Value: value, Truncated: r.Truncated, OriginalBytes: r.OriginalBytes,
	})
}

type JiraIssueFieldsResult struct {
	Key          string                 `json:"key"`
	Mode         string                 `json:"mode"`
	NonEmptyOnly bool                   `json:"non_empty_only"`
	Count        int                    `json:"count"`
	OmittedEmpty int                    `json:"omitted_empty,omitempty"`
	Fields       []JiraIssueFieldRecord `json:"fields"`
}

// ResolveJiraFieldSelectors resolves exact field ids or exact
// case-insensitive display names. An ambiguous name is never chosen by order.
func ResolveJiraFieldSelectors(defs []domain.FieldDef, selectors []string) ([]domain.FieldDef, error) {
	byID := make(map[string]domain.FieldDef, len(defs))
	byFoldedID := make(map[string][]domain.FieldDef, len(defs))
	byName := make(map[string][]domain.FieldDef, len(defs))
	for _, def := range defs {
		byID[def.ID] = def
		byFoldedID[strings.ToLower(def.ID)] = append(byFoldedID[strings.ToLower(def.ID)], def)
		byName[strings.ToLower(strings.TrimSpace(def.Name))] = append(byName[strings.ToLower(strings.TrimSpace(def.Name))], def)
	}
	seen := map[string]bool{}
	resolved := make([]domain.FieldDef, 0, len(selectors))
	for _, raw := range selectors {
		selector := strings.TrimSpace(raw)
		if selector == "" {
			return nil, fmt.Errorf("%w: Jira field selector must not be empty", domain.ErrUsage)
		}
		explicitID := false
		if strings.HasPrefix(selector, "id:") {
			selector = strings.TrimSpace(strings.TrimPrefix(selector, "id:"))
			explicitID = true
		}
		var candidates []domain.FieldDef
		if def, ok := byID[selector]; ok {
			candidates = []domain.FieldDef{def}
		} else if folded := byFoldedID[strings.ToLower(selector)]; len(folded) > 0 {
			candidates = folded
		} else if !explicitID {
			candidates = byName[strings.ToLower(selector)]
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("%w: Jira field %q was not found", domain.ErrNotFound, selector)
		}
		if len(candidates) > 1 {
			ids := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				ids = append(ids, candidate.ID)
			}
			sort.Strings(ids)
			return nil, fmt.Errorf("%w: Jira field selector %q is ambiguous; use one of ids: %s", domain.ErrCheckFailed, selector, strings.Join(ids, ", "))
		}
		if !seen[candidates[0].ID] {
			seen[candidates[0].ID] = true
			resolved = append(resolved, candidates[0])
		}
	}
	return resolved, nil
}

func (s *JiraService) resolveJiraFieldSelectors(ctx context.Context, selectors []string) ([]domain.FieldDef, error) {
	if len(selectors) == 0 {
		return nil, nil
	}
	if defs, ok := jiraTechnicalFieldDefs(selectors); ok {
		return defs, nil
	}
	defs, err := s.tr.Fields(ctx)
	if err != nil {
		return nil, err
	}
	return ResolveJiraFieldSelectors(defs, selectors)
}

var jiraKnownSystemFieldIDs = map[string]bool{
	"aggregateprogress": true, "aggregatetimeestimate": true, "aggregatetimeoriginalestimate": true,
	"aggregatetimespent": true, "assignee": true, "attachment": true, "comment": true,
	"components": true, "created": true, "creator": true, "description": true, "duedate": true,
	"environment": true, "fixVersions": true, "issuelinks": true, "issuetype": true,
	"labels": true, "lastViewed": true, "parent": true, "priority": true, "progress": true,
	"project": true, "reporter": true, "resolution": true, "resolutiondate": true,
	"security": true, "status": true, "subtasks": true, "summary": true,
	"timeestimate": true, "timeoriginalestimate": true, "timespent": true, "timetracking": true,
	"updated": true, "versions": true, "votes": true, "watches": true, "worklog": true,
}

func jiraTechnicalFieldDefs(selectors []string) ([]domain.FieldDef, bool) {
	defs := make([]domain.FieldDef, 0, len(selectors))
	seen := map[string]bool{}
	for _, raw := range selectors {
		selector := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "id:"))
		custom := false
		if strings.HasPrefix(selector, "customfield_") {
			digits := strings.TrimPrefix(selector, "customfield_")
			custom = digits != ""
			for _, char := range digits {
				if char < '0' || char > '9' {
					custom = false
					break
				}
			}
		}
		if !custom && !jiraKnownSystemFieldIDs[selector] {
			return nil, false
		}
		if !seen[selector] {
			seen[selector] = true
			defs = append(defs, domain.FieldDef{ID: selector, Name: selector, Custom: custom})
		}
	}
	return defs, true
}

func (s *JiraService) IssueFields(ctx context.Context, key string, opts JiraIssueFieldsOpts) (*JiraIssueFieldsResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	if opts.Raw && opts.MetadataOnly {
		return nil, fmt.Errorf("%w: --metadata-only conflicts with --raw", domain.ErrUsage)
	}
	defs, err := s.tr.Fields(ctx)
	if err != nil {
		return nil, err
	}
	selected := defs
	requestFields := []string{"*all"}
	if len(opts.Selectors) > 0 {
		selected, err = ResolveJiraFieldSelectors(defs, opts.Selectors)
		if err != nil {
			return nil, err
		}
		requestFields = fieldDefIDs(selected)
	}
	issue, err := s.tr.GetIssue(ctx, key, requestFields)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("%w: issue %s returned no field snapshot", domain.ErrCheckFailed, key)
	}
	defsByID := make(map[string]domain.FieldDef, len(defs))
	for _, def := range defs {
		defsByID[def.ID] = def
	}
	if len(opts.Selectors) == 0 {
		selected = make([]domain.FieldDef, 0, len(issue.Fields))
		for id := range issue.Fields {
			if def, ok := defsByID[id]; ok {
				selected = append(selected, def)
			} else {
				selected = append(selected, domain.FieldDef{ID: id, Name: id})
			}
		}
		if opts.IncludeEmpty {
			// IncludeEmpty is monotonic: it adds catalog-declared empty fields to
			// every field actually returned by the issue. A plugin/private field
			// can be populated yet absent from /field; asking for more must not
			// make that observed evidence disappear.
			selected = append(selected, defs...)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		left, right := strings.ToLower(selected[i].Name), strings.ToLower(selected[j].Name)
		if left != right {
			return left < right
		}
		return selected[i].ID < selected[j].ID
	})
	mode := "compact"
	if opts.Raw {
		mode = "raw"
	} else if opts.MetadataOnly {
		mode = "metadata"
	}
	result := &JiraIssueFieldsResult{Key: issue.Key, Mode: mode, NonEmptyOnly: !opts.IncludeEmpty, Fields: []JiraIssueFieldRecord{}}
	seen := map[string]bool{}
	for _, def := range selected {
		if seen[def.ID] {
			continue
		}
		seen[def.ID] = true
		value, present := issue.Fields[def.ID]
		empty := !present || jiraFieldValueEmpty(value)
		if empty && !opts.IncludeEmpty {
			result.OmittedEmpty++
			continue
		}
		record := JiraIssueFieldRecord{
			ID: def.ID, Name: def.Name, Custom: def.Custom, Schema: def.Schema,
			Empty: empty, Value: value, MetadataOnly: opts.MetadataOnly,
		}
		if opts.MetadataOnly {
			record.ValueType = jiraFieldValueType(value)
		} else if !opts.Raw {
			compact, truncated := compactJiraFieldValue(value, 0)
			record.Value = compact
			record.Truncated = truncated
			if truncated {
				if encoded, encodeErr := json.Marshal(value); encodeErr == nil {
					record.OriginalBytes = len(encoded)
				}
			}
		}
		result.Fields = append(result.Fields, record)
	}
	result.Count = len(result.Fields)
	return result, nil
}

func jiraFieldValueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int32, int64, uint, uint32, uint64:
		return "number"
	case []any, []string:
		return "list"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func fieldDefIDs(defs []domain.FieldDef) []string {
	ids := make([]string, 0, len(defs))
	for _, def := range defs {
		ids = append(ids, def.ID)
	}
	return ids
}

func jiraFieldValueEmpty(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(value) == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		if len(value) == 0 {
			return true
		}
		for _, child := range value {
			if !jiraFieldValueEmpty(child) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func compactJiraFieldValue(value any, depth int) (any, bool) {
	return compactJiraFieldValueWithLimits(value, depth, jiraCompactFieldStringCap, jiraCompactFieldArrayCap)
}

func compactJiraFieldValueWithLimits(value any, depth, stringCap, arrayCap int) (any, bool) {
	if depth >= jiraCompactFieldDepthCap {
		return map[string]any{"kind": "nested", "present": true}, true
	}
	switch value := value.(type) {
	case nil, bool, json.Number, float64:
		return value, false
	case string:
		if len(value) <= stringCap {
			return value, false
		}
		end := stringCap
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		return value[:end], true
	case []any:
		limit := len(value)
		truncated := false
		if limit > arrayCap {
			limit = arrayCap
			truncated = true
		}
		out := make([]any, 0, limit)
		for _, item := range value[:limit] {
			compact, childTruncated := compactJiraFieldValueWithLimits(item, depth+1, stringCap, arrayCap)
			out = append(out, compact)
			truncated = truncated || childTruncated
		}
		return out, truncated
	case map[string]any:
		if _, userLike := value["displayName"]; userLike || value["emailAddress"] != nil || value["avatarUrls"] != nil {
			out := map[string]any{"kind": "user"}
			copyCompactScalarBounded(out, "name", value["name"], stringCap)
			copyCompactScalarBounded(out, "key", value["key"], stringCap)
			copyCompactScalarBounded(out, "display_name", value["displayName"], stringCap)
			copyCompactScalar(out, "active", value["active"])
			return out, false
		}
		if option, ok := value["value"]; ok {
			out := map[string]any{"kind": "option"}
			compact, truncated := compactJiraFieldValueWithLimits(option, depth+1, stringCap, arrayCap)
			out["value"] = compact
			copyCompactScalarBounded(out, "id", value["id"], stringCap)
			return out, truncated
		}
		if name, ok := value["name"]; ok {
			out := map[string]any{"kind": "named"}
			compact, truncated := compactJiraFieldValueWithLimits(name, depth+1, stringCap, arrayCap)
			out["name"] = compact
			copyCompactScalarBounded(out, "key", value["key"], stringCap)
			copyCompactScalarBounded(out, "id", value["id"], stringCap)
			copyCompactScalar(out, "released", value["released"])
			copyCompactScalar(out, "archived", value["archived"])
			return out, truncated
		}
		keys := make([]string, 0, len(value))
		for key, child := range value {
			if key == "self" || key == "emailAddress" || key == "avatarUrls" || jiraFieldValueEmpty(child) {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		truncated := false
		if len(keys) > 20 {
			keys = keys[:20]
			truncated = true
		}
		return map[string]any{"kind": "object", "fields": keys}, truncated
	default:
		return fmt.Sprint(value), false
	}
}

func copyCompactScalarBounded(dst map[string]any, key string, value any, stringCap int) {
	if text, ok := value.(string); ok && len(text) > stringCap {
		end := stringCap
		for end > 0 && !utf8.ValidString(text[:end]) {
			end--
		}
		value = text[:end]
	}
	copyCompactScalar(dst, key, value)
}

func copyCompactScalar(dst map[string]any, key string, value any) {
	switch value := value.(type) {
	case string:
		if value != "" {
			dst[key] = value
		}
	case bool:
		dst[key] = value
	case json.Number, float64:
		dst[key] = value
	}
}

func (s *JiraService) resolveRenderFieldSelectors(ctx context.Context, rs RenderSettings) (RenderSettings, error) {
	selectors := append([]string(nil), rs.CustomFields...)
	for _, view := range rs.FieldViews {
		selectors = append(selectors, view.ID)
	}
	if len(selectors) == 0 {
		return rs, nil
	}
	if _, ok := jiraTechnicalFieldDefs(selectors); ok {
		return rs, nil
	}
	defs, err := s.tr.Fields(ctx)
	if err != nil {
		return rs, err
	}
	bySelector := map[string]domain.FieldDef{}
	for _, selector := range selectors {
		resolved, resolveErr := ResolveJiraFieldSelectors(defs, []string{selector})
		if resolveErr != nil {
			return rs, resolveErr
		}
		bySelector[selector] = resolved[0]
	}
	views := make([]config.JiraFieldView, 0, len(rs.FieldViews)+len(rs.CustomFields))
	seen := map[string]bool{}
	for _, view := range rs.FieldViews {
		def := bySelector[view.ID]
		if view.Label == view.ID && view.ID != def.ID {
			view.Label = def.Name
		}
		view.ID = def.ID
		if seen[view.ID] {
			return rs, fmt.Errorf("%w: Jira render fields resolve to duplicate id %q", domain.ErrCheckFailed, view.ID)
		}
		seen[view.ID] = true
		views = append(views, view)
	}
	custom := make([]string, 0, len(rs.CustomFields))
	for _, selector := range rs.CustomFields {
		def := bySelector[selector]
		if seen[def.ID] {
			continue
		}
		seen[def.ID] = true
		if selector != def.ID {
			views = append(views, config.JiraFieldView{ID: def.ID, Label: def.Name, Placement: "metadata", Format: "auto"})
		} else {
			custom = append(custom, def.ID)
		}
	}
	rs.CustomFields = custom
	rs.FieldViews = views
	return rs, nil
}

func JiraIssueFieldsMarkdown(result *JiraIssueFieldsResult) string {
	if result == nil {
		return ""
	}
	rows := make([][]string, 0, len(result.Fields))
	if result.Mode == "metadata" {
		for _, field := range result.Fields {
			rows = append(rows, []string{field.Name, field.ID, field.Schema, field.ValueType, fmt.Sprint(field.Empty)})
		}
		return MarkdownTable([]string{"Field", "ID", "Schema", "Value type", "Empty"}, rows)
	}
	for _, field := range result.Fields {
		value := ""
		if field.Empty {
			value = "—"
		} else if text, ok := field.Value.(string); ok {
			value = text
		} else if encoded, err := json.Marshal(field.Value); err == nil {
			value = string(encoded)
		} else {
			value = fmt.Sprint(field.Value)
		}
		if field.Truncated {
			value += fmt.Sprintf(" [truncated; original %d bytes]", field.OriginalBytes)
		}
		rows = append(rows, []string{field.Name, field.ID, field.Schema, value})
	}
	return MarkdownTable([]string{"Field", "ID", "Type", "Value"}, rows)
}
