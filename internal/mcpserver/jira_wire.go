package mcpserver

import "encoding/json"

type JiraFieldsInput struct {
	ID          string `json:"id,omitempty" jsonschema:"exact technical field id"`
	NameLike    string `json:"name_like,omitempty" jsonschema:"case-insensitive substring of the display name"`
	IDLike      string `json:"id_like,omitempty" jsonschema:"case-insensitive substring of the technical id"`
	Schema      string `json:"schema,omitempty" jsonschema:"exact Jira schema type"`
	Custom      *bool  `json:"custom,omitempty" jsonschema:"when set, select only custom or system fields"`
	SummaryOnly bool   `json:"summary_only,omitempty" jsonschema:"omit field definitions and return only qualification and reconciled counts"`
	MaxBytes    int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraIssueSearchInput struct {
	JQL        string   `json:"jql" jsonschema:"bounded JQL selection; required"`
	Columns    []string `json:"columns,omitempty" jsonschema:"preferred ordered field ids or supported columns; supply at most one non-empty columns, fields, or projection selector"`
	Fields     []string `json:"fields,omitempty" jsonschema:"compatibility alias for columns; supply at most one non-empty columns, fields, or projection selector"`
	Projection []string `json:"projection,omitempty" jsonschema:"compatibility alias for columns; ordered field ids or supported columns; supply at most one non-empty selector alias"`
	View       string   `json:"view,omitempty" jsonschema:"named Jira list view; explicit columns, fields, or projection win"`
	Limit      int      `json:"limit,omitempty" jsonschema:"page size from 1 to 1000; default 50"`
	Cursor     string   `json:"cursor,omitempty" jsonschema:"opaque pagination cursor from a previous result"`
	MaxBytes   int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraIssueFieldGetInput struct {
	Key      string `json:"key" jsonschema:"Jira issue key"`
	Field    string `json:"field" jsonschema:"exact technical field id or unambiguous display name"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded compact value bytes from 256 to 131072; default 16384"`
}

// JiraIssueHistoryInput has no raw-changelog selector and no projection mode:
// the MCP tool always returns the bounded summary projection.
type JiraIssueHistoryInput struct {
	Key      string   `json:"key" jsonschema:"Jira issue key"`
	Fields   []string `json:"fields,omitempty" jsonschema:"exact technical field ids or unambiguous display names; a selection also reports per-field last_changes"`
	Since    string   `json:"since,omitempty" jsonschema:"inclusive date in the Jira user calendar or an explicit timestamp"`
	Until    string   `json:"until,omitempty" jsonschema:"inclusive date in the Jira user calendar or an explicit timestamp"`
	MaxBytes int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

// JiraIssueRefsInput has no raw-reference selector: the MCP tool always
// projects URLs and issue narrative away before validating or bounding output.
type JiraIssueRefsInput struct {
	Key      string   `json:"key,omitempty" jsonschema:"exact Jira issue key; supply exactly one of key or jql"`
	JQL      string   `json:"jql,omitempty" jsonschema:"bounded Jira query; supply exactly one of key or jql and set limit for JQL mode"`
	Fields   []string `json:"fields,omitempty" jsonschema:"up to 8 exact technical field ids whose values may contain references"`
	Limit    int      `json:"limit,omitempty" jsonschema:"JQL issue bound from 1 to 25; required for JQL mode and invalid for key mode"`
	MaxBytes int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraEpicDigestInput struct {
	Key          string   `json:"key" jsonschema:"epic issue key"`
	Quarter      string   `json:"quarter,omitempty" jsonschema:"Jira-user calendar quarter such as 2026-Q2"`
	Since        string   `json:"since,omitempty" jsonschema:"inclusive date or timestamp; requires until"`
	Until        string   `json:"until,omitempty" jsonschema:"inclusive date or timestamp; requires since"`
	Include      []string `json:"include" jsonschema:"one or more evidence sources: identity,status-field,children,comments,links,history,refs"`
	StatusField  string   `json:"status_field,omitempty" jsonschema:"narrative status field id or exact display name"`
	DoDField     string   `json:"dod_field,omitempty" jsonschema:"additional definition-of-done field id or exact display name"`
	EpicField    string   `json:"epic_field,omitempty" jsonschema:"epic link or parent field id or exact display name"`
	ChildLimit   int      `json:"child_limit,omitempty" jsonschema:"maximum child rows; default and maximum 1000"`
	CommentLimit int      `json:"comment_limit,omitempty" jsonschema:"maximum newest comments; default and maximum 50"`
	HistoryLimit int      `json:"history_limit,omitempty" jsonschema:"maximum newest matching history entries; default and maximum 500"`
	Projection   string   `json:"projection,omitempty" jsonschema:"output projection: full or compact; compact is recommended for synthesis"`
	MaxBytes     int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraBoardViewInput struct {
	BoardID      int      `json:"board_id" jsonschema:"positive Jira Agile board id"`
	Scope        string   `json:"scope,omitempty" jsonschema:"all, board, or backlog; default all"`
	Columns      []string `json:"columns,omitempty" jsonschema:"ordered field ids or supported board columns"`
	View         string   `json:"view,omitempty" jsonschema:"named board list view; explicit columns win"`
	JQL          string   `json:"jql,omitempty" jsonschema:"optional bounded board refinement"`
	Limit        int      `json:"limit,omitempty" jsonschema:"maximum issues per scope from 1 to 1000; default 200"`
	EpicField    string   `json:"epic_field,omitempty" jsonschema:"exact epic relation field selected in columns; enables deterministic rollup"`
	DoneStatuses []string `json:"done_statuses,omitempty" jsonschema:"one or more statuses counted as done; requires epic_field"`
	MaxBytes     int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type JiraStructureGetInput struct {
	StructureID json.RawMessage `json:"structure_id"`
}

type JiraStructureViewInput struct {
	StructureID             int64    `json:"structure_id" jsonschema:"positive Jira Structure id"`
	Fields                  []string `json:"fields,omitempty" jsonschema:"ordered Jira field ids; default key,summary,status,assignee; maximum 32"`
	FolderID                string   `json:"folder_id,omitempty" jsonschema:"exact stable stored-folder item id; mutually exclusive with folder_row and folder_path"`
	FolderRow               int64    `json:"folder_row,omitempty" jsonschema:"exact positive stored-folder row id in the current forest; mutually exclusive with folder_id and folder_path"`
	FolderPath              string   `json:"folder_path,omitempty" jsonschema:"exact slash-separated stored-folder path; mutually exclusive with folder_id and folder_row"`
	ExpectedForestSignature *int64   `json:"expected_forest_signature,omitempty" jsonschema:"exact nonzero signature from forest_version.signature in the earlier jira_structure_view that supplied this selector; requires expected_forest_version"`
	ExpectedForestVersion   *int64   `json:"expected_forest_version,omitempty" jsonschema:"exact positive version from forest_version.version in the earlier jira_structure_view that supplied this selector; requires expected_forest_signature"`
	MaxRows                 int      `json:"max_rows,omitempty" jsonschema:"maximum selected rows from 1 to 1000; default 200"`
	MaxBytes                int      `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}
