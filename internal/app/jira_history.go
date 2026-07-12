package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type JiraHistoryOpts struct {
	Fields []string
	Since  string
	Until  string
}

type JiraHistoryFilters struct {
	Fields []domain.FieldDef `json:"fields,omitempty"`
	Since  string            `json:"since,omitempty"`
	Until  string            `json:"until,omitempty"`
}

type JiraFieldLastChange struct {
	FieldID   string `json:"field_id,omitempty"`
	Field     string `json:"field"`
	Created   string `json:"created"`
	HistoryID string `json:"history_id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
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
	LastChanges   []JiraFieldLastChange   `json:"last_changes,omitempty"`
}

type jiraHistoryBoundary struct {
	time time.Time
}

// HistoryFiltered returns a provenance-qualified changelog. Jira does not
// support these filters on the compatible DC endpoints, so filtering happens
// locally after the adapter has exhausted pagination or labeled its fallback.
func (s *JiraService) HistoryFiltered(ctx context.Context, key string, opts JiraHistoryOpts) (*JiraHistoryResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	since, err := parseJiraHistoryBoundary(opts.Since, false)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid --since: %v", domain.ErrUsage, err)
	}
	until, err := parseJiraHistoryBoundary(opts.Until, true)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid --until: %v", domain.ErrUsage, err)
	}
	if since != nil && until != nil && !since.time.Before(until.time) {
		return nil, fmt.Errorf("%w: --since must be earlier than --until", domain.ErrUsage)
	}

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
		Filters: JiraHistoryFilters{Fields: defs, Since: strings.TrimSpace(opts.Since), Until: strings.TrimSpace(opts.Until)},
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
				identity := def.ID
				candidate := JiraFieldLastChange{FieldID: def.ID, Field: def.Name, Created: entry.Created, HistoryID: entry.ID, From: item.From, To: item.To}
				previous, exists := latestTime[identity]
				if !exists || parseErr != nil || created.After(previous) || created.Equal(previous) {
					latest[identity] = candidate
					if parseErr == nil {
						latestTime[identity] = created
					}
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
	return result, nil
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

func parseJiraHistoryBoundary(raw string, until bool) (*jiraHistoryBoundary, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		if until {
			return &jiraHistoryBoundary{time: parsed.AddDate(0, 0, 1)}, nil
		}
		return &jiraHistoryBoundary{time: parsed}, nil
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
