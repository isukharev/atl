package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	jiraQuarterFieldCatalogWireMaxBytes = 1 << 20
	jiraQuarterEpicDigestWireMaxBytes   = 1 << 20
)

// JiraQuarterFieldCatalog is the strict evaluator-owned projection consumed by
// the retained quarter-portfolio MCP workflow. It deliberately models the
// released wire rather than importing app or configuration owners.
type JiraQuarterFieldCatalog struct {
	SchemaVersion int                          `json:"schema_version"`
	Projection    string                       `json:"projection"`
	Source        string                       `json:"source"`
	Complete      bool                         `json:"complete"`
	PartialReason string                       `json:"partial_reason,omitempty"`
	Total         int                          `json:"total"`
	Count         int                          `json:"count"`
	CustomCount   int                          `json:"custom_count"`
	SystemCount   int                          `json:"system_count"`
	Fields        []JiraQuarterFieldDefinition `json:"fields"`
}

type JiraQuarterFieldDefinition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema string `json:"schema,omitempty"`
}

// JiraQuarterCompactEpicDigest is the exact compact evidence shape selected by
// the quarter-portfolio workflow. Other digest projections intentionally do
// not pass this decoder.
type JiraQuarterCompactEpicDigest struct {
	SchemaVersion  int                                `json:"schema_version"`
	Period         JiraQuarterDigestPeriod            `json:"period"`
	Includes       []string                           `json:"includes"`
	Sources        map[string]JiraQuarterDigestSource `json:"sources"`
	Epic           JiraQuarterDigestIdentity          `json:"epic"`
	StatusField    JiraQuarterDigestField             `json:"status_field"`
	Staleness      JiraQuarterDigestStaleness         `json:"staleness"`
	Warnings       []string                           `json:"warnings,omitempty"`
	Projection     JiraQuarterDigestProjection        `json:"projection"`
	HistorySummary JiraQuarterDigestHistorySummary    `json:"history_summary"`
}

type JiraQuarterDigestPeriod struct {
	Quarter                string `json:"quarter"`
	Since                  string `json:"since"`
	Until                  string `json:"until"`
	BoundaryTimeZone       string `json:"boundary_time_zone"`
	BoundaryTimeZoneSource string `json:"boundary_time_zone_source"`
	SinceInstant           string `json:"since_instant"`
	UntilExclusiveInstant  string `json:"until_exclusive_instant"`
}

type JiraQuarterDigestSource struct {
	Complete       bool   `json:"complete"`
	Count          int    `json:"count"`
	CountTruncated bool   `json:"count_truncated,omitempty"`
	TextTruncated  bool   `json:"text_truncated,omitempty"`
	Warning        string `json:"warning,omitempty"`
}

type JiraQuarterDigestIdentity struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	Resolution  string `json:"resolution,omitempty"`
	Type        string `json:"type,omitempty"`
	Updated     string `json:"updated,omitempty"`
	Description string `json:"description,omitempty"`
}

type JiraQuarterDigestField struct {
	ID         string                      `json:"id"`
	Name       string                      `json:"name"`
	Value      string                      `json:"value,omitempty"`
	LastChange JiraQuarterDigestLastChange `json:"last_change"`
	Truncated  bool                        `json:"truncated,omitempty"`
}

type JiraQuarterDigestLastChange struct {
	FieldID   string `json:"field_id"`
	Field     string `json:"field"`
	Created   string `json:"created"`
	HistoryID string `json:"history_id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

type JiraQuarterDigestStaleness struct {
	Evaluated          bool     `json:"evaluated"`
	Stale              bool     `json:"stale"`
	StatusFieldUpdated string   `json:"status_field_updated,omitempty"`
	LatestEvidenceAt   string   `json:"latest_evidence_at,omitempty"`
	NewerChildUpdates  int      `json:"newer_child_updates"`
	NewerComments      int      `json:"newer_comments"`
	Reasons            []string `json:"reasons"`
}

type JiraQuarterDigestProjection struct {
	Name    string   `json:"name"`
	Omitted []string `json:"omitted"`
	Clipped []string `json:"clipped"`
}

type JiraQuarterDigestHistorySummary struct {
	Count  int                                    `json:"count"`
	Recent []JiraQuarterDigestCompactHistoryEntry `json:"recent"`
}

type JiraQuarterDigestCompactHistoryEntry struct {
	ID      string                         `json:"id"`
	Created string                         `json:"created"`
	Items   []JiraQuarterDigestHistoryItem `json:"items"`
}

type JiraQuarterDigestHistoryItem struct {
	Field   string `json:"field"`
	FieldID string `json:"field_id,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

func DecodeJiraQuarterFieldCatalog(r io.Reader) (JiraQuarterFieldCatalog, error) {
	var catalog JiraQuarterFieldCatalog
	if err := decodeJiraWorkflowWire(r, jiraQuarterFieldCatalogWireMaxBytes, "Jira quarter field catalog", &catalog, validateJiraQuarterFieldCatalogMembers); err != nil {
		return JiraQuarterFieldCatalog{}, err
	}
	if err := catalog.validate(); err != nil {
		return JiraQuarterFieldCatalog{}, fmt.Errorf("validate Jira quarter field catalog: %w", err)
	}
	return catalog, nil
}

func DecodeJiraQuarterCompactEpicDigest(r io.Reader) (JiraQuarterCompactEpicDigest, error) {
	var digest JiraQuarterCompactEpicDigest
	if err := decodeJiraWorkflowWire(r, jiraQuarterEpicDigestWireMaxBytes, "Jira quarter compact epic digest", &digest, validateJiraQuarterCompactEpicDigestMembers); err != nil {
		return JiraQuarterCompactEpicDigest{}, err
	}
	if err := digest.validate(); err != nil {
		return JiraQuarterCompactEpicDigest{}, fmt.Errorf("validate Jira quarter compact epic digest: %w", err)
	}
	return digest, nil
}

func validateJiraQuarterFieldCatalogMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "quarter field catalog")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "quarter field catalog", []string{
		"schema_version", "projection", "source", "complete", "total", "count", "custom_count", "system_count", "fields",
	}, []string{"partial_reason"}); err != nil {
		return err
	}
	return jiraWorkflowArray(root["fields"], "quarter field catalog.fields", func(field map[string]json.RawMessage, owner string) error {
		return jiraWorkflowMembers(field, owner, []string{"id", "name", "custom"}, []string{"schema"})
	})
}

func validateJiraQuarterCompactEpicDigestMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "quarter compact epic digest")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "quarter compact epic digest", []string{
		"schema_version", "period", "includes", "sources", "epic", "status_field", "staleness", "projection", "history_summary",
	}, []string{"warnings"}); err != nil {
		return err
	}
	period, err := jiraWorkflowNestedObject(root["period"], "quarter compact epic digest.period")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(period, "quarter compact epic digest.period", []string{
		"quarter", "since", "until", "boundary_time_zone", "boundary_time_zone_source", "since_instant", "until_exclusive_instant",
	}, nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(root["includes"], "quarter compact epic digest.includes", nil); err != nil {
		return err
	}
	sources, err := jiraWorkflowNestedObject(root["sources"], "quarter compact epic digest.sources")
	if err != nil {
		return err
	}
	for _, name := range []string{"history", "identity", "status-field"} {
		source, err := jiraWorkflowNestedObject(sources[name], "quarter compact epic digest.sources."+name)
		if err != nil {
			return err
		}
		if err := jiraWorkflowMembers(source, "quarter compact epic digest.sources."+name, []string{"complete", "count"}, []string{"count_truncated", "text_truncated", "warning"}); err != nil {
			return err
		}
	}
	if len(sources) != 3 {
		return fmt.Errorf("quarter compact epic digest.sources must contain exactly selected sources")
	}
	epic, err := jiraWorkflowNestedObject(root["epic"], "quarter compact epic digest.epic")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(epic, "quarter compact epic digest.epic", []string{"key", "summary", "status"}, []string{"resolution", "type", "updated", "description"}); err != nil {
		return err
	}
	statusField, err := jiraWorkflowNestedObject(root["status_field"], "quarter compact epic digest.status_field")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(statusField, "quarter compact epic digest.status_field", []string{"id", "name", "last_change"}, []string{"value", "truncated"}); err != nil {
		return err
	}
	lastChange, err := jiraWorkflowNestedObject(statusField["last_change"], "quarter compact epic digest.status_field.last_change")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(lastChange, "quarter compact epic digest.status_field.last_change", []string{"field_id", "field", "created", "history_id"}, []string{"from", "to"}); err != nil {
		return err
	}
	staleness, err := jiraWorkflowNestedObject(root["staleness"], "quarter compact epic digest.staleness")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(staleness, "quarter compact epic digest.staleness", []string{
		"evaluated", "stale", "newer_child_updates", "newer_comments", "reasons",
	}, []string{"status_field_updated", "latest_evidence_at"}); err != nil {
		return err
	}
	if err := jiraWorkflowArray(staleness["reasons"], "quarter compact epic digest.staleness.reasons", nil); err != nil {
		return err
	}
	projection, err := jiraWorkflowNestedObject(root["projection"], "quarter compact epic digest.projection")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(projection, "quarter compact epic digest.projection", []string{"name", "omitted", "clipped"}, nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(projection["omitted"], "quarter compact epic digest.projection.omitted", nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(projection["clipped"], "quarter compact epic digest.projection.clipped", nil); err != nil {
		return err
	}
	history, err := jiraWorkflowNestedObject(root["history_summary"], "quarter compact epic digest.history_summary")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(history, "quarter compact epic digest.history_summary", []string{"count", "recent"}, nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(history["recent"], "quarter compact epic digest.history_summary.recent", validateJiraQuarterDigestHistoryEntryMembers); err != nil {
		return err
	}
	if warnings, ok := root["warnings"]; ok {
		return jiraWorkflowArray(warnings, "quarter compact epic digest.warnings", nil)
	}
	return nil
}

func validateJiraQuarterDigestHistoryEntryMembers(entry map[string]json.RawMessage, owner string) error {
	if err := jiraWorkflowMembers(entry, owner, []string{"id", "created", "items"}, nil); err != nil {
		return err
	}
	return jiraWorkflowArray(entry["items"], owner+".items", func(item map[string]json.RawMessage, itemOwner string) error {
		return jiraWorkflowMembers(item, itemOwner, []string{"field"}, []string{"field_id", "from", "to"})
	})
}

func (c JiraQuarterFieldCatalog) validate() error {
	if c.SchemaVersion != 1 || c.Projection != "full" || c.Source != "jira-field-catalog" || !c.Complete || c.PartialReason != "" {
		return fmt.Errorf("catalog projection, source, or completeness is unsupported")
	}
	if c.Fields == nil || c.Total < 1 || c.Count != len(c.Fields) || c.Total != c.Count ||
		c.CustomCount < 0 || c.SystemCount < 0 || c.CustomCount+c.SystemCount != c.Count {
		return fmt.Errorf("catalog counts are not reconciled with fields")
	}
	custom := 0
	seen := make(map[string]bool, len(c.Fields))
	for index, field := range c.Fields {
		if !jiraWorkflowNormalized(field.ID) || !jiraWorkflowNormalized(field.Name) ||
			field.Schema != "" && !jiraWorkflowNormalized(field.Schema) || seen[field.ID] {
			return fmt.Errorf("fields[%d] is invalid or duplicates an earlier field", index)
		}
		seen[field.ID] = true
		if field.Custom {
			custom++
		}
	}
	if custom != c.CustomCount {
		return fmt.Errorf("custom_count is not reconciled with fields")
	}
	return nil
}

func (d JiraQuarterCompactEpicDigest) validate() error {
	if d.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if err := d.Period.validate(); err != nil {
		return err
	}
	if want := []string{"history", "identity", "status-field"}; !equalQuarterStrings(d.Includes, want) {
		return fmt.Errorf("includes are not the selected compact evidence set")
	}
	if err := d.validateSources(); err != nil {
		return err
	}
	if !jiraWorkflowNormalized(d.Epic.Key) || !jiraWorkflowNormalized(d.Epic.Summary) || !jiraWorkflowNormalized(d.Epic.Status) ||
		d.Epic.Resolution != "" && !jiraWorkflowNormalized(d.Epic.Resolution) ||
		d.Epic.Type != "" && !jiraWorkflowNormalized(d.Epic.Type) ||
		d.Epic.Updated != "" && !jiraQuarterDigestTimestamp(d.Epic.Updated) ||
		d.Epic.Description != "" && !jiraWorkflowNormalized(d.Epic.Description) {
		return fmt.Errorf("epic identity is invalid")
	}
	if err := d.StatusField.validate(); err != nil {
		return err
	}
	if err := d.Projection.validate(); err != nil {
		return err
	}
	if err := d.HistorySummary.validate(d.Sources["history"].Count, d.StatusField, d.Projection.historyItemsOmitted()); err != nil {
		return err
	}
	if err := d.Staleness.validate(d.StatusField); err != nil {
		return err
	}
	if len(d.Warnings) > jiraWorkflowMaximumWarnings {
		return fmt.Errorf("warnings exceed %d entries", jiraWorkflowMaximumWarnings)
	}
	for index, warning := range d.Warnings {
		if !jiraWorkflowNormalized(warning) {
			return fmt.Errorf("warnings[%d] is invalid", index)
		}
	}
	return nil
}

func (p JiraQuarterDigestPeriod) validate() error {
	year, quarter, ok := parseJiraQuarterDigestQuarter(p.Quarter)
	if !ok || p.BoundaryTimeZoneSource != "jira_current_user" {
		return fmt.Errorf("period is invalid")
	}
	location, err := time.LoadLocation(p.BoundaryTimeZone)
	if err != nil || location.String() != p.BoundaryTimeZone {
		return fmt.Errorf("period timezone is invalid")
	}
	startMonth := time.Month((quarter-1)*3 + 1)
	dateStart := time.Date(year, startMonth, 1, 0, 0, 0, 0, time.UTC)
	if p.Since != dateStart.Format("2006-01-02") || p.Until != dateStart.AddDate(0, 3, -1).Format("2006-01-02") {
		return fmt.Errorf("period dates are not reconciled with quarter")
	}
	sinceInstant, err := time.Parse(time.RFC3339Nano, p.SinceInstant)
	if err != nil || p.SinceInstant != sinceInstant.UTC().Format(time.RFC3339Nano) {
		return fmt.Errorf("period since instant is invalid")
	}
	untilInstant, err := time.Parse(time.RFC3339Nano, p.UntilExclusiveInstant)
	if err != nil || p.UntilExclusiveInstant != untilInstant.UTC().Format(time.RFC3339Nano) || !untilInstant.After(sinceInstant) {
		return fmt.Errorf("period until instant is invalid")
	}
	wantUntilDate := dateStart.AddDate(0, 3, 0).Format("2006-01-02")
	if sinceInstant.In(location).Format("2006-01-02") != p.Since ||
		sinceInstant.Add(-time.Nanosecond).In(location).Format("2006-01-02") == p.Since ||
		untilInstant.In(location).Format("2006-01-02") != wantUntilDate ||
		untilInstant.Add(-time.Nanosecond).In(location).Format("2006-01-02") != p.Until {
		return fmt.Errorf("period instants are not reconciled with quarter timezone")
	}
	return nil
}

func (d JiraQuarterCompactEpicDigest) validateSources() error {
	if len(d.Sources) != 3 {
		return fmt.Errorf("sources are not the selected compact evidence set")
	}
	for _, name := range []string{"history", "identity", "status-field"} {
		source, ok := d.Sources[name]
		if !ok || !source.Complete || source.Count < 0 || source.CountTruncated || source.TextTruncated || source.Warning != "" {
			return fmt.Errorf("source %q is incomplete or invalid", name)
		}
	}
	if d.Sources["identity"].Count != 1 || d.Sources["status-field"].Count != 1 {
		return fmt.Errorf("identity or status-field source count is not reconciled")
	}
	return nil
}

func (f JiraQuarterDigestField) validate() error {
	if !jiraWorkflowNormalized(f.ID) || !jiraWorkflowNormalized(f.Name) || !jiraWorkflowNormalized(f.Value) || f.Truncated ||
		f.LastChange.FieldID != f.ID || f.LastChange.Field != f.Name ||
		!jiraWorkflowNormalized(f.LastChange.HistoryID) || !jiraQuarterDigestTimestamp(f.LastChange.Created) ||
		f.LastChange.From != "" && !jiraWorkflowNormalized(f.LastChange.From) ||
		f.LastChange.To != "" && !jiraWorkflowNormalized(f.LastChange.To) {
		return fmt.Errorf("status_field is invalid or unreconciled")
	}
	return nil
}

func (h JiraQuarterDigestHistorySummary) validate(sourceCount int, status JiraQuarterDigestField, itemsOmitted bool) error {
	if h.Count != sourceCount || h.Count < 1 || h.Recent == nil || len(h.Recent) == 0 || len(h.Recent) > h.Count || len(h.Recent) > 5 {
		return fmt.Errorf("history_summary count is not reconciled")
	}
	seenIDs := make(map[string]bool, len(h.Recent))
	for index, entry := range h.Recent {
		if !jiraWorkflowNormalized(entry.ID) || seenIDs[entry.ID] || !jiraQuarterDigestTimestamp(entry.Created) ||
			entry.Items == nil || len(entry.Items) == 0 || len(entry.Items) > 3 {
			return fmt.Errorf("history_summary.recent[%d] is invalid", index)
		}
		seenIDs[entry.ID] = true
		selectedChangeFound := false
		for itemIndex, item := range entry.Items {
			if !jiraWorkflowNormalized(item.Field) || item.FieldID != "" && !jiraWorkflowNormalized(item.FieldID) ||
				item.From != "" && !jiraWorkflowNormalized(item.From) || item.To != "" && !jiraWorkflowNormalized(item.To) {
				return fmt.Errorf("history_summary.recent[%d].items[%d] is invalid", index, itemIndex)
			}
			selectedField := item.FieldID != "" && strings.EqualFold(item.FieldID, status.ID) ||
				strings.EqualFold(strings.TrimSpace(item.Field), strings.TrimSpace(status.Name)) ||
				strings.EqualFold(item.Field, status.ID)
			if entry.ID == status.LastChange.HistoryID && selectedField {
				if item.From != status.LastChange.From || item.To != status.LastChange.To {
					return fmt.Errorf("history_summary does not reconcile present status_field.last_change values")
				}
				selectedChangeFound = true
			}
		}
		if entry.ID == status.LastChange.HistoryID &&
			(entry.Created != status.LastChange.Created || !selectedChangeFound && !itemsOmitted) {
			return fmt.Errorf("history_summary does not reconcile present status_field.last_change")
		}
	}
	return nil
}

func (s JiraQuarterDigestStaleness) validate(status JiraQuarterDigestField) error {
	if !s.Evaluated || s.Stale || s.NewerChildUpdates != 0 || s.NewerComments != 0 ||
		s.StatusFieldUpdated != status.LastChange.Created || s.LatestEvidenceAt != "" || s.Reasons == nil || len(s.Reasons) == 0 {
		return fmt.Errorf("staleness is not reconciled with selected compact evidence")
	}
	for index, reason := range s.Reasons {
		if !jiraWorkflowNormalized(reason) {
			return fmt.Errorf("staleness.reasons[%d] is invalid", index)
		}
	}
	return nil
}

func (p JiraQuarterDigestProjection) validate() error {
	if p.Name != "compact" || p.Omitted == nil || p.Clipped == nil || len(p.Omitted) < 1 || len(p.Omitted) > 6 ||
		p.Omitted[len(p.Omitted)-1] != "history" {
		return fmt.Errorf("compact projection is not reconciled with history summary")
	}
	for _, omitted := range p.Omitted[:len(p.Omitted)-1] {
		if omitted != "history_summary.recent.items[remaining]" {
			return fmt.Errorf("compact projection contains unsupported omission")
		}
	}
	for index, clipped := range p.Clipped {
		if !jiraWorkflowNormalized(clipped) || index > 0 && p.Clipped[index-1] >= clipped {
			return fmt.Errorf("projection.clipped[%d] is invalid, duplicated, or unordered", index)
		}
	}
	return nil
}

func (p JiraQuarterDigestProjection) historyItemsOmitted() bool {
	return len(p.Omitted) > 1
}

func equalQuarterStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func parseJiraQuarterDigestQuarter(value string) (int, int, bool) {
	if len(value) != len("2026-Q2") || value[4:6] != "-Q" {
		return 0, 0, false
	}
	parsed, err := time.Parse("2006", value[:4])
	quarter := int(value[6] - '0')
	if err != nil || parsed.Year() < 1970 || quarter < 1 || quarter > 4 {
		return 0, 0, false
	}
	return parsed.Year(), quarter, true
}

func jiraQuarterDigestTimestamp(value string) bool {
	if !jiraWorkflowNormalized(value) {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}
