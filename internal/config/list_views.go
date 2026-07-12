package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	JiraListSourceSearch          = "search"
	JiraListSourceEpicChildren    = "epic_children"
	JiraListSourceBoard           = "board"
	JiraListSourceBoardSnapshot   = "board_snapshot"
	JiraListSourceSprint          = "sprint"
	JiraListSourceStructure       = "structure"
	JiraListSourceConfluenceMacro = "confluence_macro"
)

// JiraListView is one reusable, source-aware Jira list projection. Missing
// source entries inherit the built-in default, so a custom view can override
// only the workflows it actually specializes.
type JiraListView struct {
	Description     string   `json:"description,omitempty"`
	Search          []string `json:"search"`
	EpicChildren    []string `json:"epic_children"`
	Board           []string `json:"board"`
	BoardSnapshot   []string `json:"board_snapshot"`
	Sprint          []string `json:"sprint"`
	Structure       []string `json:"structure"`
	ConfluenceMacro []string `json:"confluence_macro"`
}

var jiraListViewName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// DefaultJiraListViews returns independent built-in presets. They are part of
// effective config (and therefore config show/save), not hidden CLI constants.
func DefaultJiraListViews() map[string]JiraListView {
	return map[string]JiraListView{
		"default": {
			Description:     "Compact everyday agent view",
			Search:          []string{"key", "summary", "status", "assignee"},
			EpicChildren:    []string{"key", "summary", "status", "issuetype", "assignee"},
			Board:           []string{"position", "key", "summary", "status", "assignee"},
			BoardSnapshot:   []string{"position", "key", "summary", "status", "board.column", "assignee"},
			Sprint:          []string{"position", "key", "summary", "status", "assignee"},
			Structure:       []string{"key", "summary", "status", "assignee"},
			ConfluenceMacro: []string{"key", "summary", "status", "assignee"},
		},
		"full": {
			Description:     "Broader planning and review context",
			Search:          []string{"position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"},
			EpicChildren:    []string{"position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels", "epic.parent"},
			Board:           []string{"position", "key", "summary", "status", "board.column", "issuetype", "priority", "assignee", "labels"},
			BoardSnapshot:   []string{"position", "key", "summary", "status", "board.column", "board.in_backlog", "issuetype", "priority", "assignee", "labels"},
			Sprint:          []string{"position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"},
			Structure:       []string{"key", "summary", "status", "issuetype", "priority", "assignee", "labels"},
			ConfluenceMacro: []string{"position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"},
		},
	}
}

// NormalizeJiraListViews overlays user presets onto built-ins and fills omitted
// source projections from the built-in default. It rejects ambiguous names and
// empty/duplicate column identifiers before any Jira request can be made.
func NormalizeJiraListViews(user map[string]JiraListView) (map[string]JiraListView, error) {
	builtins := DefaultJiraListViews()
	out := map[string]JiraListView{}
	for name, view := range builtins {
		out[name] = cloneJiraListView(view)
	}
	for rawName, candidate := range user {
		name := strings.TrimSpace(rawName)
		if !jiraListViewName.MatchString(name) {
			return nil, fmt.Errorf("jira list view name %q must match %s", rawName, jiraListViewName.String())
		}
		base, ok := out[name]
		if !ok {
			base = cloneJiraListView(builtins["default"])
			base.Description = ""
		}
		out[name] = overlayJiraListView(base, candidate)
	}
	for name, view := range out {
		if err := validateJiraListView(name, view); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ResolveJiraListView(views map[string]JiraListView, name, source string) ([]string, string, error) {
	normalized, err := NormalizeJiraListViews(views)
	if err != nil {
		return nil, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	view, ok := normalized[name]
	if !ok {
		names := make([]string, 0, len(normalized))
		for candidate := range normalized {
			names = append(names, candidate)
		}
		sort.Strings(names)
		return nil, "", fmt.Errorf("unknown Jira list view %q (available: %s)", name, strings.Join(names, ", "))
	}
	columns, ok := jiraListViewSource(view, source)
	if !ok {
		return nil, "", fmt.Errorf("unknown Jira list source %q", source)
	}
	return append([]string(nil), columns...), name, nil
}

// SetJiraListViewsJSON applies either the whole jira.list_views map or one
// jira.list_views.<name> object. Unknown JSON fields are refused.
func SetJiraListViewsJSON(current map[string]JiraListView, key, value string) (map[string]JiraListView, error) {
	next := make(map[string]JiraListView, len(current))
	for name, view := range current {
		next[name] = cloneJiraListView(view)
	}
	decode := func(dst any) error {
		dec := json.NewDecoder(bytes.NewBufferString(value))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			return err
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			if err == nil {
				return fmt.Errorf("multiple JSON values")
			}
			return err
		}
		return nil
	}
	if key == "jira.list_views" {
		var replacement map[string]JiraListView
		if err := decode(&replacement); err != nil {
			return nil, fmt.Errorf("jira.list_views must be a JSON object: %v", err)
		}
		return NormalizeJiraListViews(replacement)
	}
	const prefix = "jira.list_views."
	if !strings.HasPrefix(key, prefix) {
		return nil, fmt.Errorf("unknown config key %q", key)
	}
	name := strings.TrimPrefix(key, prefix)
	if strings.TrimSpace(value) == "null" {
		if !jiraListViewName.MatchString(name) {
			return nil, fmt.Errorf("jira list view name %q must match %s", name, jiraListViewName.String())
		}
		delete(next, name)
		if normalized, err := NormalizeJiraListViews(next); err == nil {
			return normalized, nil
		}
		// A narrow recovery delete must be persistable even when another entry
		// remains invalid. Runtime Load still validates the whole catalog and
		// stays fail-closed until every bad entry has been removed/replaced.
		return next, nil
	}
	var view JiraListView
	if err := decode(&view); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %v", key, err)
	}
	next[name] = view
	return NormalizeJiraListViews(next)
}

func jiraListViewSource(view JiraListView, source string) ([]string, bool) {
	switch source {
	case JiraListSourceSearch:
		return view.Search, true
	case JiraListSourceEpicChildren:
		return view.EpicChildren, true
	case JiraListSourceBoard:
		return view.Board, true
	case JiraListSourceBoardSnapshot:
		return view.BoardSnapshot, true
	case JiraListSourceSprint:
		return view.Sprint, true
	case JiraListSourceStructure:
		return view.Structure, true
	case JiraListSourceConfluenceMacro:
		return view.ConfluenceMacro, true
	default:
		return nil, false
	}
}

func overlayJiraListView(base, candidate JiraListView) JiraListView {
	if candidate.Description != "" {
		base.Description = strings.TrimSpace(candidate.Description)
	}
	for target, source := range map[*[]string][]string{
		&base.Search: candidate.Search, &base.EpicChildren: candidate.EpicChildren,
		&base.Board: candidate.Board, &base.BoardSnapshot: candidate.BoardSnapshot,
		&base.Sprint: candidate.Sprint, &base.Structure: candidate.Structure,
		&base.ConfluenceMacro: candidate.ConfluenceMacro,
	} {
		if len(source) > 0 {
			*target = append([]string(nil), source...)
		}
	}
	return base
}

func cloneJiraListView(view JiraListView) JiraListView {
	return overlayJiraListView(JiraListView{Description: view.Description}, view)
}

func validateJiraListView(name string, view JiraListView) error {
	for source, columns := range map[string][]string{
		JiraListSourceSearch: view.Search, JiraListSourceEpicChildren: view.EpicChildren,
		JiraListSourceBoard: view.Board, JiraListSourceBoardSnapshot: view.BoardSnapshot,
		JiraListSourceSprint: view.Sprint, JiraListSourceStructure: view.Structure,
		JiraListSourceConfluenceMacro: view.ConfluenceMacro,
	} {
		if len(columns) == 0 {
			return fmt.Errorf("jira list view %q source %q must contain at least one column", name, source)
		}
		seen := map[string]bool{}
		for _, raw := range columns {
			column := strings.TrimSpace(raw)
			if column == "" {
				return fmt.Errorf("jira list view %q source %q contains an empty column", name, source)
			}
			if seen[column] {
				return fmt.Errorf("jira list view %q source %q repeats column %q", name, source, column)
			}
			seen[column] = true
			if err := validateJiraListViewColumn(source, column); err != nil {
				return fmt.Errorf("jira list view %q: %w", name, err)
			}
		}
	}
	return nil
}

func validateJiraListViewColumn(source, column string) error {
	if source == JiraListSourceStructure {
		if column == "position" || column == "id" || strings.Contains(column, ".") {
			return fmt.Errorf("source %q accepts Jira field ids only; invalid column %q", source, column)
		}
		return nil
	}
	if !strings.Contains(column, ".") {
		return nil
	}
	allowed := map[string]map[string]bool{
		JiraListSourceEpicChildren: {"epic.parent": true, "epic.relation": true},
		JiraListSourceBoard: {
			"board.column": true, "board.column_index": true, "board.column_mapped": true,
			"board.in_board": true, "board.in_backlog": true,
		},
		JiraListSourceBoardSnapshot: {
			"board.column": true, "board.column_index": true, "board.column_mapped": true,
			"board.in_board": true, "board.in_backlog": true,
		},
		JiraListSourceSprint: {"sprint.id": true},
	}
	if !allowed[source][column] {
		return fmt.Errorf("column %q is not available for source %q", column, source)
	}
	return nil
}
