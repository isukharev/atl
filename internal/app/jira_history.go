package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type JiraHistoryOpts struct {
	Fields           []string
	Since            string
	Until            string
	boundaryTimeZone string // reuse one already-observed Jira user zone inside a digest
}

type JiraHistoryFilters struct {
	Fields                 []domain.FieldDef `json:"fields,omitempty"`
	Since                  string            `json:"since,omitempty"`
	Until                  string            `json:"until,omitempty"`
	BoundaryTimeZone       string            `json:"boundary_time_zone,omitempty"`
	BoundaryTimeZoneSource string            `json:"boundary_time_zone_source,omitempty"`
	SinceInstant           string            `json:"since_instant,omitempty"`
	UntilExclusiveInstant  string            `json:"until_exclusive_instant,omitempty"`
}

type JiraFieldLastChange struct {
	FieldID   string `json:"field_id,omitempty"`
	Field     string `json:"field"`
	Created   string `json:"created"`
	HistoryID string `json:"history_id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

type JiraHistoryFieldSummary struct {
	FieldID  string `json:"field_id,omitempty"`
	Field    string `json:"field"`
	Count    int    `json:"count"`
	WithFrom int    `json:"with_from"`
	WithTo   int    `json:"with_to"`
}

// JiraHistorySummary carries deterministic cardinality and consistency facts
// for the filtered History array. ChronologicalAscending is nil when one or
// more timestamps cannot be compared, so unknown ordering is never reported as
// false ordering.
type JiraHistorySummary struct {
	HistoryCount             int                       `json:"history_count"`
	HistoryIDNonemptyCount   int                       `json:"history_id_nonempty_count"`
	HistoryIDMissingCount    int                       `json:"history_id_missing_count"`
	HistoryIDsUnique         bool                      `json:"history_ids_unique"`
	HistoryNonemptyIDsUnique bool                      `json:"history_nonempty_ids_unique"`
	AuthorNonemptyCount      int                       `json:"author_nonempty_count"`
	TimestampNonemptyCount   int                       `json:"timestamp_nonempty_count"`
	ChronologicalComparable  bool                      `json:"chronological_comparable"`
	ChronologicalAscending   *bool                     `json:"chronological_ascending"`
	EntriesWithItems         int                       `json:"entries_with_items"`
	MultiItemEntryCount      int                       `json:"multi_item_entry_count"`
	ItemCount                int                       `json:"item_count"`
	ItemFieldNonemptyCount   int                       `json:"item_field_nonempty_count"`
	DistinctItemFieldCount   int                       `json:"distinct_item_field_count"`
	ItemsWithFromCount       int                       `json:"items_with_from_count"`
	ItemsWithToCount         int                       `json:"items_with_to_count"`
	StatusItemCount          int                       `json:"status_item_count"`
	CountMatchesHistory      bool                      `json:"count_matches_history"`
	FetchedMatchesTotal      bool                      `json:"fetched_matches_total"`
	Fields                   []JiraHistoryFieldSummary `json:"fields"`
}

type JiraHistoryResult struct {
	Key           string                  `json:"key"`
	Complete      bool                    `json:"complete"`
	Source        string                  `json:"source"`
	Total         int                     `json:"total"`
	Fetched       int                     `json:"fetched"`
	Count         int                     `json:"count"`
	PartialReason string                  `json:"partial_reason,omitempty"`
	Filters       JiraHistoryFilters      `json:"filters"`
	History       []domain.ChangelogEntry `json:"history"`
	Summary       JiraHistorySummary      `json:"summary"`
	LastChanges   []JiraFieldLastChange   `json:"last_changes,omitempty"`
}

// JiraHistorySummaryResult is the bounded projection for consumers that need
// deterministic changelog facts without the raw History array. LastChanges is
// present only for explicitly selected fields, just as it is on the full
// result.
type JiraHistorySummaryResult struct {
	Key           string                `json:"key"`
	Complete      bool                  `json:"complete"`
	Source        string                `json:"source"`
	Total         int                   `json:"total"`
	Fetched       int                   `json:"fetched"`
	Count         int                   `json:"count"`
	PartialReason string                `json:"partial_reason,omitempty"`
	Filters       JiraHistoryFilters    `json:"filters"`
	Summary       JiraHistorySummary    `json:"summary"`
	LastChanges   []JiraFieldLastChange `json:"last_changes,omitempty"`
}

// JiraHistorySummaryProjection removes the raw changelog while preserving its
// provenance, filters, deterministic summary, and selected-field recency.
func JiraHistorySummaryProjection(result *JiraHistoryResult) *JiraHistorySummaryResult {
	if result == nil {
		return nil
	}
	return &JiraHistorySummaryResult{
		Key:           result.Key,
		Complete:      result.Complete,
		Source:        result.Source,
		Total:         result.Total,
		Fetched:       result.Fetched,
		Count:         result.Count,
		PartialReason: result.PartialReason,
		Filters:       result.Filters,
		Summary:       result.Summary,
		LastChanges:   result.LastChanges,
	}
}

type jiraHistoryBoundary struct {
	time time.Time
}

type jiraHistoryBoundaries struct {
	since    *jiraHistoryBoundary
	until    *jiraHistoryBoundary
	timeZone string
}

var errJiraCivilDateUnavailable = errors.New("jira civil date has no real instant")

// HistoryFiltered returns a provenance-qualified changelog. Jira does not
// support these filters on the compatible DC endpoints, so filtering happens
// locally after the adapter has exhausted pagination or labeled its fallback.
func (s *JiraService) HistoryFiltered(ctx context.Context, key string, opts JiraHistoryOpts) (*JiraHistoryResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	boundaries, err := s.resolveJiraHistoryBoundaries(ctx, opts)
	if err != nil {
		return nil, err
	}
	since, until := boundaries.since, boundaries.until

	var defs []domain.FieldDef
	if len(opts.Fields) > 0 {
		defs, err = s.resolveJiraFieldSelectors(ctx, opts.Fields)
		if err != nil {
			return nil, err
		}
	}

	snapshot := &domain.ChangelogSnapshot{Source: "legacy", PartialReason: "tracker does not expose changelog completeness"}
	if reader, ok := s.tr.(domain.CompleteChangelogReader); ok {
		snapshot, err = reader.CompleteChangelog(ctx, key)
	} else {
		snapshot.Entries, err = s.tr.Changelog(ctx, key)
		snapshot.Total = len(snapshot.Entries)
	}
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, fmt.Errorf("%w: Jira returned no changelog snapshot for %s", domain.ErrCheckFailed, key)
	}

	result := &JiraHistoryResult{
		Key: key, Complete: snapshot.Complete, Source: snapshot.Source, Total: snapshot.Total,
		Fetched: len(snapshot.Entries), PartialReason: snapshot.PartialReason,
		Filters: JiraHistoryFilters{
			Fields: defs, Since: strings.TrimSpace(opts.Since), Until: strings.TrimSpace(opts.Until),
			BoundaryTimeZone: boundaries.timeZone, BoundaryTimeZoneSource: boundaryTimeZoneSource(boundaries.timeZone),
			SinceInstant: instantString(since), UntilExclusiveInstant: instantString(until),
		},
		History: []domain.ChangelogEntry{},
	}
	latest := map[string]JiraFieldLastChange{}
	latestTime := map[string]time.Time{}
	for _, entry := range snapshot.Entries {
		created, parseErr := parseJiraHistoryTime(entry.Created)
		if parseErr != nil && (since != nil || until != nil) {
			return nil, fmt.Errorf("%w: Jira changelog entry %s has unsupported timestamp %q", domain.ErrCheckFailed, entry.ID, entry.Created)
		}
		if since != nil && created.Before(since.time) {
			continue
		}
		if until != nil && !created.Before(until.time) {
			continue
		}
		filtered := domain.ChangelogEntry{ID: entry.ID, Author: entry.Author, Created: entry.Created, Items: []domain.ChangelogItem{}}
		for _, item := range entry.Items {
			def, selected := selectedHistoryField(defs, item)
			if len(defs) > 0 && !selected {
				continue
			}
			filtered.Items = append(filtered.Items, item)
			if len(defs) > 0 {
				if parseErr != nil {
					return nil, fmt.Errorf("%w: Jira changelog entry %s has unsupported timestamp %q; cannot determine latest change for field %s", domain.ErrCheckFailed, entry.ID, entry.Created, def.ID)
				}
				identity := def.ID
				candidate := JiraFieldLastChange{FieldID: def.ID, Field: def.Name, Created: entry.Created, HistoryID: entry.ID, From: item.From, To: item.To}
				previous, exists := latestTime[identity]
				if !exists || created.After(previous) || created.Equal(previous) {
					latest[identity] = candidate
					latestTime[identity] = created
				}
			}
		}
		if len(filtered.Items) > 0 {
			result.History = append(result.History, filtered)
		}
	}
	result.Count = len(result.History)
	for _, def := range defs {
		if change, ok := latest[def.ID]; ok {
			result.LastChanges = append(result.LastChanges, change)
		}
	}
	result.Summary = summarizeJiraHistory(result)
	return result, nil
}

func summarizeJiraHistory(result *JiraHistoryResult) JiraHistorySummary {
	summary := JiraHistorySummary{
		HistoryIDsUnique:         true,
		HistoryNonemptyIDsUnique: true,
		ChronologicalComparable:  true,
		Fields:                   []JiraHistoryFieldSummary{},
	}
	if result == nil {
		ascending := true
		summary.ChronologicalAscending = &ascending
		return summary
	}

	summary.HistoryCount = len(result.History)
	summary.CountMatchesHistory = result.Count == summary.HistoryCount
	summary.FetchedMatchesTotal = result.Fetched == result.Total

	ids := make(map[string]struct{}, len(result.History))
	nonemptyIDs := make(map[string]struct{}, len(result.History))
	fields := make(map[string]*JiraHistoryFieldSummary)
	ascending := true
	var previous time.Time
	havePrevious := false
	for _, entry := range result.History {
		if entry.ID != "" {
			summary.HistoryIDNonemptyCount++
			if _, exists := nonemptyIDs[entry.ID]; exists {
				summary.HistoryNonemptyIDsUnique = false
			} else {
				nonemptyIDs[entry.ID] = struct{}{}
			}
		} else {
			summary.HistoryIDMissingCount++
		}
		if _, exists := ids[entry.ID]; exists {
			summary.HistoryIDsUnique = false
		} else {
			ids[entry.ID] = struct{}{}
		}
		if entry.Author != "" {
			summary.AuthorNonemptyCount++
		}
		if entry.Created != "" {
			summary.TimestampNonemptyCount++
		}
		created, err := parseJiraHistoryTime(entry.Created)
		if err != nil {
			summary.ChronologicalComparable = false
		} else {
			if havePrevious && created.Before(previous) {
				ascending = false
			}
			previous = created
			havePrevious = true
		}

		if len(entry.Items) > 0 {
			summary.EntriesWithItems++
		}
		if len(entry.Items) > 1 {
			summary.MultiItemEntryCount++
		}
		for _, item := range entry.Items {
			summary.ItemCount++
			if item.Field != "" {
				summary.ItemFieldNonemptyCount++
			}
			if item.From != "" {
				summary.ItemsWithFromCount++
			}
			if item.To != "" {
				summary.ItemsWithToCount++
			}
			if strings.EqualFold(strings.TrimSpace(item.FieldID), "status") || strings.EqualFold(strings.TrimSpace(item.Field), "status") {
				summary.StatusItemCount++
			}

			field, fieldID := strings.TrimSpace(item.Field), strings.TrimSpace(item.FieldID)
			identity := "name:" + strings.ToLower(field)
			if fieldID != "" {
				identity = "id:" + strings.ToLower(fieldID)
			} else if field == "" {
				continue
			}
			bucket, ok := fields[identity]
			if !ok {
				bucket = &JiraHistoryFieldSummary{FieldID: fieldID, Field: field}
				fields[identity] = bucket
			} else if bucket.Field == "" && field != "" {
				bucket.Field = field
			}
			bucket.Count++
			if item.From != "" {
				bucket.WithFrom++
			}
			if item.To != "" {
				bucket.WithTo++
			}
		}
	}
	if summary.ChronologicalComparable {
		summary.ChronologicalAscending = &ascending
	}

	summary.Fields = make([]JiraHistoryFieldSummary, 0, len(fields))
	for _, field := range fields {
		summary.Fields = append(summary.Fields, *field)
	}
	sort.Slice(summary.Fields, func(i, j int) bool {
		leftID, rightID := strings.ToLower(summary.Fields[i].FieldID), strings.ToLower(summary.Fields[j].FieldID)
		if leftID != rightID {
			if leftID == "" {
				return false
			}
			if rightID == "" {
				return true
			}
			return leftID < rightID
		}
		left, right := strings.ToLower(summary.Fields[i].Field), strings.ToLower(summary.Fields[j].Field)
		if left != right {
			return left < right
		}
		if summary.Fields[i].Field != summary.Fields[j].Field {
			return summary.Fields[i].Field < summary.Fields[j].Field
		}
		return summary.Fields[i].FieldID < summary.Fields[j].FieldID
	})
	summary.DistinctItemFieldCount = len(summary.Fields)
	return summary
}

func selectedHistoryField(defs []domain.FieldDef, item domain.ChangelogItem) (domain.FieldDef, bool) {
	for _, def := range defs {
		if item.FieldID != "" && strings.EqualFold(item.FieldID, def.ID) {
			return def, true
		}
		if strings.EqualFold(strings.TrimSpace(item.Field), strings.TrimSpace(def.Name)) || strings.EqualFold(item.Field, def.ID) {
			return def, true
		}
	}
	return domain.FieldDef{}, false
}

func (s *JiraService) resolveJiraHistoryBoundaries(ctx context.Context, opts JiraHistoryOpts) (*jiraHistoryBoundaries, error) {
	sinceRaw, untilRaw := strings.TrimSpace(opts.Since), strings.TrimSpace(opts.Until)
	sinceDate, err := jiraBoundaryIsDate(sinceRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid --since: %v", domain.ErrUsage, err)
	}
	untilDate, err := jiraBoundaryIsDate(untilRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid --until: %v", domain.ErrUsage, err)
	}
	// Two calendar dates can be ordered before the metadata lookup. Equal dates
	// are a valid one-day inclusive interval; only a reversed range is invalid.
	if sinceDate && untilDate && sinceRaw > untilRaw {
		return nil, fmt.Errorf("%w: --since must be earlier than or equal to --until", domain.ErrUsage)
	}

	location := time.UTC
	timeZone := ""
	if sinceDate || untilDate {
		timeZone = strings.TrimSpace(opts.boundaryTimeZone)
		if timeZone == "" {
			reader, ok := s.tr.(domain.JiraUserTimeZoneReader)
			if !ok {
				return nil, fmt.Errorf("%w: Jira current-user timezone is required for date-only period boundaries", domain.ErrCheckFailed)
			}
			timeZone, err = reader.CurrentUserTimeZone(ctx)
			if err != nil {
				return nil, fmt.Errorf("resolve Jira current-user timezone: %w", err)
			}
			timeZone = strings.TrimSpace(timeZone)
		}
		if timeZone == "" {
			return nil, fmt.Errorf("%w: Jira current user did not expose a timezone required for date-only period boundaries", domain.ErrCheckFailed)
		}
		location, err = time.LoadLocation(timeZone)
		if err != nil {
			return nil, fmt.Errorf("%w: Jira current user returned invalid IANA timezone %q", domain.ErrCheckFailed, timeZone)
		}
		timeZone = location.String()
	}

	since, err := parseJiraHistoryBoundaryIn(sinceRaw, false, location)
	if err != nil {
		if errors.Is(err, errJiraCivilDateUnavailable) {
			return nil, fmt.Errorf("%w: cannot resolve --since: %v", domain.ErrCheckFailed, err)
		}
		return nil, fmt.Errorf("%w: invalid --since: %v", domain.ErrUsage, err)
	}
	until, err := parseJiraHistoryBoundaryIn(untilRaw, true, location)
	if err != nil {
		if errors.Is(err, errJiraCivilDateUnavailable) {
			return nil, fmt.Errorf("%w: cannot resolve --until: %v", domain.ErrCheckFailed, err)
		}
		return nil, fmt.Errorf("%w: invalid --until: %v", domain.ErrUsage, err)
	}
	if since != nil && until != nil && !since.time.Before(until.time) {
		return nil, fmt.Errorf("%w: --since must be earlier than --until", domain.ErrUsage)
	}
	return &jiraHistoryBoundaries{since: since, until: until, timeZone: timeZone}, nil
}

// jiraBoundaryIsDate validates one boundary without observing backend state and
// reports whether it needs the Jira current-user calendar timezone.
func jiraBoundaryIsDate(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	if _, err := time.Parse("2006-01-02", raw); err == nil {
		return true, nil
	}
	if _, err := parseJiraHistoryTime(raw); err != nil {
		return false, fmt.Errorf("want YYYY-MM-DD, RFC3339, or Jira datetime")
	}
	return false, nil
}

func instantString(boundary *jiraHistoryBoundary) string {
	if boundary == nil {
		return ""
	}
	return boundary.time.UTC().Format(time.RFC3339Nano)
}

func boundaryTimeZoneSource(timeZone string) string {
	if timeZone == "" {
		return ""
	}
	return "jira_current_user"
}

func parseJiraHistoryBoundaryIn(raw string, until bool, location *time.Location) (*jiraHistoryBoundary, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		start, end, boundsErr := jiraCivilDateBounds(parsed, location)
		if boundsErr != nil {
			return nil, boundsErr
		}
		if until {
			return &jiraHistoryBoundary{time: end}, nil
		}
		return &jiraHistoryBoundary{time: start}, nil
	}
	parsed, err := parseJiraHistoryTime(raw)
	if err != nil {
		return nil, fmt.Errorf("want YYYY-MM-DD, RFC3339, or Jira datetime")
	}
	if until {
		parsed = parsed.Add(time.Nanosecond)
	}
	return &jiraHistoryBoundary{time: parsed}, nil
}

// jiraCivilDateBounds returns the smallest continuous UTC interval containing
// every real instant whose localized calendar date equals date. IANA offsets
// and transitions have whole-second precision in Go's time package, so a
// second-granularity scan finds exact boundaries even when midnight is skipped
// or repeated. A wide fixed window covers the IANA offset range and historical
// date-line jumps without consulting the backend or the host timezone.
func jiraCivilDateBounds(date time.Time, location *time.Location) (time.Time, time.Time, error) {
	const radius = 48 * time.Hour
	year, month, day := date.Date()
	center := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	var first, last time.Time
	for candidate, limit := center.Add(-radius), center.Add(radius); !candidate.After(limit); candidate = candidate.Add(time.Second) {
		local := candidate.In(location)
		localYear, localMonth, localDay := local.Date()
		if localYear != year || localMonth != month || localDay != day {
			continue
		}
		if first.IsZero() {
			first = candidate
		}
		last = candidate
	}
	if first.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %s in %s", errJiraCivilDateUnavailable, date.Format("2006-01-02"), location)
	}
	return first, last.Add(time.Second), nil
}

func parseJiraHistoryTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700", "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Jira datetime")
}

func JiraHistoryMarkdown(result *JiraHistoryResult) string {
	if result == nil {
		return ""
	}
	rows := make([][]string, 0)
	for _, entry := range result.History {
		for _, item := range entry.Items {
			field := item.Field
			if item.FieldID != "" {
				field += " (" + item.FieldID + ")"
			}
			rows = append(rows, []string{entry.Created, entry.Author, field, item.From, item.To})
		}
	}
	status := fmt.Sprintf("Complete: %t · source: %s · fetched: %d/%d · matched entries: %d", result.Complete, result.Source, result.Fetched, result.Total, result.Count)
	if result.PartialReason != "" {
		status += " · partial: " + result.PartialReason
	}
	return status + "\n\n" + MarkdownTable([]string{"Created", "Author", "Field", "From", "To"}, rows)
}

func JiraHistorySummaryMarkdown(result *JiraHistorySummaryResult) string {
	if result == nil {
		return ""
	}
	status := fmt.Sprintf("Complete: %t · source: %s · fetched: %d/%d · matched entries: %d", result.Complete, result.Source, result.Fetched, result.Total, result.Count)
	if result.PartialReason != "" {
		status += " · partial: " + result.PartialReason
	}

	ascending := "unknown"
	if result.Summary.ChronologicalAscending != nil {
		ascending = fmt.Sprintf("%t", *result.Summary.ChronologicalAscending)
	}
	facts := [][]string{
		{"History entries", fmt.Sprintf("%d", result.Summary.HistoryCount)},
		{"Items", fmt.Sprintf("%d", result.Summary.ItemCount)},
		{"Distinct fields", fmt.Sprintf("%d", result.Summary.DistinctItemFieldCount)},
		{"Missing history ids", fmt.Sprintf("%d", result.Summary.HistoryIDMissingCount)},
		{"Non-empty ids unique", fmt.Sprintf("%t", result.Summary.HistoryNonemptyIDsUnique)},
		{"Chronologically comparable", fmt.Sprintf("%t", result.Summary.ChronologicalComparable)},
		{"Chronologically ascending", ascending},
		{"Count matches history", fmt.Sprintf("%t", result.Summary.CountMatchesHistory)},
		{"Fetched matches total", fmt.Sprintf("%t", result.Summary.FetchedMatchesTotal)},
	}
	sections := []string{status, MarkdownTable([]string{"Fact", "Value"}, facts)}

	if len(result.Summary.Fields) > 0 {
		rows := make([][]string, 0, len(result.Summary.Fields))
		for _, field := range result.Summary.Fields {
			rows = append(rows, []string{field.FieldID, field.Field, fmt.Sprintf("%d", field.Count), fmt.Sprintf("%d", field.WithFrom), fmt.Sprintf("%d", field.WithTo)})
		}
		sections = append(sections, MarkdownTable([]string{"Field ID", "Field", "Count", "With from", "With to"}, rows))
	}
	if len(result.LastChanges) > 0 {
		rows := make([][]string, 0, len(result.LastChanges))
		for _, change := range result.LastChanges {
			rows = append(rows, []string{change.FieldID, change.Field, change.Created, change.HistoryID, change.From, change.To})
		}
		sections = append(sections, MarkdownTable([]string{"Field ID", "Field", "Created", "History ID", "From", "To"}, rows))
	}
	return strings.Join(sections, "\n\n")
}
