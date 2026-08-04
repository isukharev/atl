package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const jiraHistorySummaryWireMaxBytes = 1 << 20

// JiraHistorySummaryView is the evaluator-owned released schema-v1 summary
// projection for jira issue history. It intentionally omits raw changelog
// entries and models only public wire data.
type JiraHistorySummaryView struct {
	Key           string                  `json:"key"`
	Complete      bool                    `json:"complete"`
	Source        string                  `json:"source"`
	Total         int                     `json:"total"`
	Fetched       int                     `json:"fetched"`
	Count         int                     `json:"count"`
	PartialReason string                  `json:"partial_reason,omitempty"`
	Filters       JiraHistoryFiltersView  `json:"filters"`
	Summary       JiraHistorySummaryFacts `json:"summary"`
	LastChanges   []JiraHistoryLastChange `json:"last_changes,omitempty"`
}

type JiraHistoryFiltersView struct {
	Fields                 []JiraHistorySelectedField `json:"fields,omitempty"`
	Since                  string                     `json:"since,omitempty"`
	Until                  string                     `json:"until,omitempty"`
	BoundaryTimeZone       string                     `json:"boundary_time_zone,omitempty"`
	BoundaryTimeZoneSource string                     `json:"boundary_time_zone_source,omitempty"`
	SinceInstant           string                     `json:"since_instant,omitempty"`
	UntilExclusiveInstant  string                     `json:"until_exclusive_instant,omitempty"`
}

type JiraHistorySelectedField struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema string `json:"schema,omitempty"`
}

type JiraHistorySummaryFacts struct {
	HistoryCount             int                      `json:"history_count"`
	HistoryIDNonemptyCount   int                      `json:"history_id_nonempty_count"`
	HistoryIDMissingCount    int                      `json:"history_id_missing_count"`
	HistoryIDsUnique         bool                     `json:"history_ids_unique"`
	HistoryNonemptyIDsUnique bool                     `json:"history_nonempty_ids_unique"`
	AuthorNonemptyCount      int                      `json:"author_nonempty_count"`
	TimestampNonemptyCount   int                      `json:"timestamp_nonempty_count"`
	ChronologicalComparable  bool                     `json:"chronological_comparable"`
	ChronologicalAscending   *bool                    `json:"chronological_ascending"`
	EntriesWithItems         int                      `json:"entries_with_items"`
	MultiItemEntryCount      int                      `json:"multi_item_entry_count"`
	ItemCount                int                      `json:"item_count"`
	ItemFieldNonemptyCount   int                      `json:"item_field_nonempty_count"`
	DistinctItemFieldCount   int                      `json:"distinct_item_field_count"`
	ItemsWithFromCount       int                      `json:"items_with_from_count"`
	ItemsWithToCount         int                      `json:"items_with_to_count"`
	StatusItemCount          int                      `json:"status_item_count"`
	CountMatchesHistory      bool                     `json:"count_matches_history"`
	FetchedMatchesTotal      bool                     `json:"fetched_matches_total"`
	Fields                   []JiraHistoryFieldBucket `json:"fields"`
}

type JiraHistoryFieldBucket struct {
	FieldID  string `json:"field_id,omitempty"`
	Field    string `json:"field"`
	Count    int    `json:"count"`
	WithFrom int    `json:"with_from"`
	WithTo   int    `json:"with_to"`
}

type JiraHistoryLastChange struct {
	FieldID   string `json:"field_id,omitempty"`
	Field     string `json:"field"`
	Created   string `json:"created"`
	HistoryID string `json:"history_id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

// DecodeJiraHistorySummary strictly decodes and reconciles one bounded
// released Jira history summary projection.
func DecodeJiraHistorySummary(r io.Reader) (JiraHistorySummaryView, error) {
	limited := &io.LimitedReader{R: r, N: jiraHistorySummaryWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return JiraHistorySummaryView{}, fmt.Errorf("read jira history summary wire: %w", err)
	}
	if limited.N <= 0 {
		return JiraHistorySummaryView{}, fmt.Errorf("jira history summary wire exceeds %d bytes", jiraHistorySummaryWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return JiraHistorySummaryView{}, fmt.Errorf("decode jira history summary wire: %w", err)
	}
	presence, err := validateJiraHistoryMembers(data)
	if err != nil {
		return JiraHistorySummaryView{}, fmt.Errorf("decode jira history summary wire: %w", err)
	}
	var view JiraHistorySummaryView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return JiraHistorySummaryView{}, fmt.Errorf("decode jira history summary wire: %w", err)
	}
	if err := view.validate(presence); err != nil {
		return JiraHistorySummaryView{}, fmt.Errorf("validate jira history summary: %w", err)
	}
	return view, nil
}

type jiraHistoryPresence struct {
	partialReason bool
	filterFields  bool
	filterStrings map[string]bool
	lastChanges   bool
}

func validateJiraHistoryMembers(data []byte) (jiraHistoryPresence, error) {
	root, err := jiraHistoryObject(data, "history")
	if err != nil {
		return jiraHistoryPresence{}, err
	}
	if err := jiraHistoryMembers(root, "history",
		[]string{"key", "complete", "source", "total", "fetched", "count", "filters", "summary"},
		[]string{"partial_reason", "last_changes"}); err != nil {
		return jiraHistoryPresence{}, err
	}
	presence := jiraHistoryPresence{
		partialReason: root["partial_reason"] != nil,
		lastChanges:   root["last_changes"] != nil,
		filterStrings: map[string]bool{},
	}

	filters, err := jiraHistoryNestedObject(root["filters"], "history.filters")
	if err != nil {
		return jiraHistoryPresence{}, err
	}
	filterNames := []string{
		"fields", "since", "until", "boundary_time_zone", "boundary_time_zone_source",
		"since_instant", "until_exclusive_instant",
	}
	if err := jiraHistoryMembers(filters, "history.filters", nil, filterNames); err != nil {
		return jiraHistoryPresence{}, err
	}
	for _, name := range filterNames[1:] {
		presence.filterStrings[name] = filters[name] != nil
	}
	if raw, ok := filters["fields"]; ok {
		presence.filterFields = true
		if err := jiraHistoryArray(raw, "history.filters.fields", func(item map[string]json.RawMessage, owner string) error {
			if err := jiraHistoryMembers(item, owner, []string{"id", "name", "custom"}, []string{"schema"}); err != nil {
				return err
			}
			return jiraHistoryNonemptyOptionalString(item, "schema", owner)
		}); err != nil {
			return jiraHistoryPresence{}, err
		}
	}

	summary, err := jiraHistoryNestedObject(root["summary"], "history.summary")
	if err != nil {
		return jiraHistoryPresence{}, err
	}
	requiredSummary := []string{
		"history_count", "history_id_nonempty_count", "history_id_missing_count", "history_ids_unique",
		"history_nonempty_ids_unique", "author_nonempty_count", "timestamp_nonempty_count",
		"chronological_comparable", "chronological_ascending", "entries_with_items", "multi_item_entry_count",
		"item_count", "item_field_nonempty_count", "distinct_item_field_count", "items_with_from_count",
		"items_with_to_count", "status_item_count", "count_matches_history", "fetched_matches_total", "fields",
	}
	if err := jiraHistoryMembersAllowNull(summary, "history.summary", requiredSummary, "chronological_ascending"); err != nil {
		return jiraHistoryPresence{}, err
	}
	if err := jiraHistoryArray(summary["fields"], "history.summary.fields", func(item map[string]json.RawMessage, owner string) error {
		if err := jiraHistoryMembers(item, owner, []string{"field", "count", "with_from", "with_to"}, []string{"field_id"}); err != nil {
			return err
		}
		return jiraHistoryNonemptyOptionalString(item, "field_id", owner)
	}); err != nil {
		return jiraHistoryPresence{}, err
	}
	if raw, ok := root["last_changes"]; ok {
		if err := jiraHistoryArray(raw, "history.last_changes", func(item map[string]json.RawMessage, owner string) error {
			return jiraHistoryMembers(item, owner, []string{"field", "created", "history_id"}, []string{"field_id", "from", "to"})
		}); err != nil {
			return jiraHistoryPresence{}, err
		}
	}
	return presence, nil
}

func jiraHistoryObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraHistoryNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	return jiraHistoryObject(raw, owner)
}

func jiraHistoryMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	return jiraHistoryMembersAllowNull(object, owner, append(required, optional...), "", required...)
}

func jiraHistoryMembersAllowNull(
	object map[string]json.RawMessage,
	owner string,
	allowed []string,
	nullable string,
	required ...string,
) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	for _, name := range required {
		raw, ok := object[name]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
		if name != nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for name, raw := range object {
		if !allowedSet[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
		if name != nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	return nil
}

func jiraHistoryArray(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	for index, raw := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		item, err := jiraHistoryNestedObject(raw, itemOwner)
		if err != nil {
			return err
		}
		if err := validate(item, itemOwner); err != nil {
			return err
		}
	}
	return nil
}

func jiraHistoryNonemptyOptionalString(object map[string]json.RawMessage, name, owner string) error {
	raw, ok := object[name]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !jiraHistoryNormalized(value) {
		return fmt.Errorf("%s.%s must be a non-empty normalized string", owner, name)
	}
	return nil
}

func (view JiraHistorySummaryView) validate(p jiraHistoryPresence) error {
	if !jiraHistoryNormalized(view.Key) || !jiraHistoryNormalized(view.Source) {
		return fmt.Errorf("key or source is invalid")
	}
	if view.Total < 0 || view.Fetched < 0 || view.Count < 0 || view.Count > view.Fetched {
		return fmt.Errorf("root counts are invalid")
	}
	if view.Complete == p.partialReason || p.partialReason && !jiraHistoryNormalized(view.PartialReason) {
		return fmt.Errorf("complete and partial_reason are not reconciled")
	}
	if err := view.Filters.validate(p); err != nil {
		return err
	}
	if err := view.Summary.validate(view.Count, view.Fetched, view.Total, view.Filters.Fields); err != nil {
		return err
	}
	if view.Complete && !view.Summary.FetchedMatchesTotal {
		return fmt.Errorf("complete history must reconcile fetched and total")
	}
	if p.lastChanges && len(view.LastChanges) == 0 {
		return fmt.Errorf("present last_changes must not be empty")
	}
	if err := validateJiraHistoryLastChanges(view.LastChanges, view.Filters.Fields); err != nil {
		return err
	}
	return nil
}

func (filters JiraHistoryFiltersView) validate(p jiraHistoryPresence) error {
	if p.filterFields && len(filters.Fields) == 0 {
		return fmt.Errorf("present selected fields must not be empty")
	}
	seen := map[string]bool{}
	for _, field := range filters.Fields {
		if !jiraHistoryNormalized(field.ID) || !jiraHistoryNormalized(field.Name) ||
			field.Schema != "" && !jiraHistoryNormalized(field.Schema) {
			return fmt.Errorf("selected field definition is invalid")
		}
		identity := strings.ToLower(field.ID)
		if seen[identity] {
			return fmt.Errorf("selected fields are not distinct")
		}
		seen[identity] = true
	}
	stringsByName := map[string]string{
		"since": filters.Since, "until": filters.Until,
		"boundary_time_zone":        filters.BoundaryTimeZone,
		"boundary_time_zone_source": filters.BoundaryTimeZoneSource,
		"since_instant":             filters.SinceInstant, "until_exclusive_instant": filters.UntilExclusiveInstant,
	}
	for name, value := range stringsByName {
		if p.filterStrings[name] && !jiraHistoryNormalized(value) {
			return fmt.Errorf("filter %s is invalid", name)
		}
	}
	if p.filterStrings["since"] != p.filterStrings["since_instant"] ||
		p.filterStrings["until"] != p.filterStrings["until_exclusive_instant"] {
		return fmt.Errorf("filter boundaries and instants are not reconciled")
	}
	if p.filterStrings["boundary_time_zone"] != p.filterStrings["boundary_time_zone_source"] ||
		filters.BoundaryTimeZoneSource != "" && filters.BoundaryTimeZoneSource != "jira_current_user" {
		return fmt.Errorf("boundary timezone provenance is invalid")
	}
	return nil
}

func (summary JiraHistorySummaryFacts) validate(count, fetched, total int, selected []JiraHistorySelectedField) error {
	counts := []int{
		summary.HistoryCount, summary.HistoryIDNonemptyCount, summary.HistoryIDMissingCount,
		summary.AuthorNonemptyCount, summary.TimestampNonemptyCount, summary.EntriesWithItems,
		summary.MultiItemEntryCount, summary.ItemCount, summary.ItemFieldNonemptyCount,
		summary.DistinctItemFieldCount, summary.ItemsWithFromCount, summary.ItemsWithToCount, summary.StatusItemCount,
	}
	for _, value := range counts {
		if value < 0 {
			return fmt.Errorf("summary counts must be nonnegative")
		}
	}
	if count != summary.HistoryCount || !summary.CountMatchesHistory ||
		summary.FetchedMatchesTotal != (fetched == total) {
		return fmt.Errorf("summary root reconciliation is invalid")
	}
	if summary.HistoryIDNonemptyCount+summary.HistoryIDMissingCount != summary.HistoryCount ||
		summary.AuthorNonemptyCount > summary.HistoryCount || summary.TimestampNonemptyCount > summary.HistoryCount ||
		summary.EntriesWithItems > summary.HistoryCount || summary.MultiItemEntryCount > summary.EntriesWithItems ||
		summary.ItemFieldNonemptyCount > summary.ItemCount || summary.ItemsWithFromCount > summary.ItemCount ||
		summary.ItemsWithToCount > summary.ItemCount || summary.StatusItemCount > summary.ItemCount ||
		summary.EntriesWithItems > summary.ItemCount {
		return fmt.Errorf("summary counters are internally inconsistent")
	}
	if summary.ChronologicalComparable != (summary.ChronologicalAscending != nil) {
		return fmt.Errorf("chronological tri-state is invalid")
	}
	if summary.HistoryIDsUnique != (summary.HistoryNonemptyIDsUnique && summary.HistoryIDMissingCount <= 1) {
		return fmt.Errorf("history id uniqueness facts are inconsistent")
	}
	if summary.DistinctItemFieldCount != len(summary.Fields) {
		return fmt.Errorf("distinct field count does not match field buckets")
	}
	var bucketItems int
	seen := map[string]bool{}
	for index, field := range summary.Fields {
		if !jiraHistoryNormalized(field.Field) || field.FieldID != "" && !jiraHistoryNormalized(field.FieldID) ||
			field.Count <= 0 || field.WithFrom < 0 || field.WithFrom > field.Count ||
			field.WithTo < 0 || field.WithTo > field.Count {
			return fmt.Errorf("field bucket %d is invalid", index)
		}
		identity := jiraHistoryFieldIdentity(field.FieldID, field.Field)
		if seen[identity] || index > 0 && !jiraHistoryFieldLess(summary.Fields[index-1], field) {
			return fmt.Errorf("field buckets are not ordered and distinct")
		}
		seen[identity] = true
		bucketItems += field.Count
		if len(selected) > 0 && jiraHistorySelectedIndex(selected, field.FieldID, field.Field) < 0 {
			return fmt.Errorf("field bucket is outside selected fields")
		}
	}
	if bucketItems > summary.ItemCount {
		return fmt.Errorf("field bucket counts exceed item count")
	}
	return nil
}

func validateJiraHistoryLastChanges(changes []JiraHistoryLastChange, selected []JiraHistorySelectedField) error {
	if len(changes) > 0 && len(selected) == 0 {
		return fmt.Errorf("last_changes require selected fields")
	}
	previous := -1
	for index, change := range changes {
		if !jiraHistoryNormalized(change.FieldID) || !jiraHistoryNormalized(change.Field) ||
			!jiraHistoryNormalized(change.Created) || !jiraHistoryEmptyOrNormalized(change.HistoryID) {
			return fmt.Errorf("last change %d identity is invalid", index)
		}
		selectedIndex := jiraHistorySelectedIndex(selected, change.FieldID, change.Field)
		if selectedIndex <= previous {
			return fmt.Errorf("last_changes are not an ordered selected-field subset")
		}
		previous = selectedIndex
	}
	return nil
}

func jiraHistorySelectedIndex(fields []JiraHistorySelectedField, id, name string) int {
	for index, field := range fields {
		if id != "" && strings.EqualFold(field.ID, id) {
			return index
		}
		if id == "" && (strings.EqualFold(field.Name, name) || strings.EqualFold(field.ID, name)) {
			return index
		}
	}
	return -1
}

func jiraHistoryFieldIdentity(id, name string) string {
	if id != "" {
		return "id:" + strings.ToLower(id)
	}
	return "name:" + strings.ToLower(name)
}

func jiraHistoryFieldLess(left, right JiraHistoryFieldBucket) bool {
	leftID, rightID := strings.ToLower(left.FieldID), strings.ToLower(right.FieldID)
	if leftID != rightID {
		if leftID == "" {
			return false
		}
		if rightID == "" {
			return true
		}
		return leftID < rightID
	}
	leftName, rightName := strings.ToLower(left.Field), strings.ToLower(right.Field)
	if leftName != rightName {
		return leftName < rightName
	}
	if left.Field != right.Field {
		return left.Field < right.Field
	}
	return left.FieldID < right.FieldID
}

func jiraHistoryNormalized(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func jiraHistoryEmptyOrNormalized(value string) bool {
	return value == "" || value == strings.TrimSpace(value)
}
