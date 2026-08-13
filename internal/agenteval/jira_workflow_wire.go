package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	JiraWorkflowWireSchemaVersion = 1

	jiraPortfolioBoardListWireMaxBytes = 256 << 10
	jiraPortfolioFoldersWireMaxBytes   = 1 << 20
	jiraSprintCurrentWireMaxBytes      = 64 << 10
	jiraSprintMembershipWireMaxBytes   = 1 << 20
	jiraUserSearchWireMaxBytes         = 256 << 10
	jiraIssueCreateWireMaxBytes        = 128 << 10
	jiraEpicLinkWireMaxBytes           = 64 << 10
	jiraWorkflowMaximumItems           = 1000
	jiraWorkflowMaximumWarnings        = 256
	jiraWorkflowMaximumPathComponents  = 128
	jiraWorkflowMaximumStringBytes     = 16 << 10
)

// JiraUserSearch is the evaluator-owned released JSON result of
// `atl jira user search`. It intentionally carries only the closed public
// projection needed by synthetic identity qualification.
type JiraUserSearch struct {
	Users []JiraWorkflowUser `json:"users"`
}

type JiraWorkflowUser struct {
	Name        string `json:"name,omitempty"`
	Key         string `json:"key,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email,omitempty"`
	Active      bool   `json:"active"`
}

// JiraIssueCreate is the evaluator-owned released JSON result of an
// unregistered `atl jira issue create --from-md` invocation. The selected
// synthetic workflows use the command's complete returned issue projection;
// no product domain type crosses this boundary.
type JiraIssueCreate struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	Type        string `json:"type"`
	Project     string `json:"project"`
	Description string `json:"description"`
}

// JiraEpicLink is the evaluator-owned released JSON result of
// `atl jira issue link-epic`.
type JiraEpicLink struct {
	Issue  string `json:"issue"`
	Epic   string `json:"epic"`
	Status string `json:"status"`
}

// JiraPortfolioBoardList is the evaluator-owned released board-list envelope.
// It models only the public CLI JSON and intentionally has no product import.
type JiraPortfolioBoardList struct {
	Boards     []JiraPortfolioBoard `json:"boards"`
	NextCursor string               `json:"next_cursor"`
	Count      int                  `json:"count"`
	Complete   bool                 `json:"complete"`
	Truncated  bool                 `json:"truncated"`
}

type JiraPortfolioBoard struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	ProjectKey string `json:"project_key,omitempty"`
}

// JiraPortfolioStructureFolders is the released schema-v1 folders discovery
// result. No raw Structure transport objects cross this evaluator boundary.
type JiraPortfolioStructureFolders struct {
	SchemaVersion int                            `json:"schema_version"`
	Structure     JiraPortfolioStructureIdentity `json:"structure"`
	ForestVersion JiraPortfolioForestVersion     `json:"forest_version"`
	Folders       []JiraPortfolioFolder          `json:"folders"`
	Complete      bool                           `json:"complete"`
	Warnings      []string                       `json:"warnings"`
}

type JiraPortfolioStructureIdentity struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
}

type JiraPortfolioForestVersion struct {
	Signature int64 `json:"signature"`
	Version   int64 `json:"version"`
}

type JiraPortfolioFolder struct {
	FolderID       string                   `json:"folder_id"`
	RowID          int64                    `json:"row_id"`
	Name           string                   `json:"name"`
	Path           []string                 `json:"path"`
	Depth          int                      `json:"depth"`
	ParentFolderID string                   `json:"parent_folder_id"`
	Stats          JiraPortfolioFolderStats `json:"stats"`
}

type JiraPortfolioFolderStats struct {
	DescendantRows   int `json:"descendant_rows"`
	IssueRows        int `json:"issue_rows"`
	UniqueIssues     int `json:"unique_issues"`
	Subfolders       int `json:"subfolders"`
	MaxRelativeDepth int `json:"max_relative_depth"`
}

// JiraSprintCurrent is the evaluator-owned public sprint object returned by
// `atl jira sprint current`. Optional properties mirror the released CLI wire.
type JiraSprintCurrent struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	StartDate     string `json:"start_date,omitempty"`
	EndDate       string `json:"end_date,omitempty"`
	CompleteDate  string `json:"complete_date,omitempty"`
	Goal          string `json:"goal,omitempty"`
	OriginBoardID int    `json:"origin_board_id,omitempty"`
}

// JiraSprintMembershipIssueList is the released IssueList variant for a
// sprint-membership read. It deliberately differs from JiraSnapshotIssueList:
// its source identity and row context are sprint-specific, while Values remains
// open because Jira field values are intentionally backend-defined.
type JiraSprintMembershipIssueList struct {
	SchemaVersion int                                `json:"schema_version"`
	Source        JiraSprintMembershipSource         `json:"source"`
	Selection     JiraSprintMembershipSelection      `json:"selection"`
	Projection    JiraSprintMembershipProjection     `json:"projection"`
	Rows          []JiraSprintMembershipIssueListRow `json:"rows"`
	Page          JiraSprintMembershipIssueListPage  `json:"page"`
}

type JiraSprintMembershipSource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// JiraSprintMembershipSelection is intentionally closed and empty. Sprint
// identity belongs to source.id; a JQL selection object would be a wire drift.
type JiraSprintMembershipSelection struct{}

type JiraSprintMembershipProjection struct {
	Columns  []string `json:"columns"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
	View     string   `json:"view,omitempty"`
}

type JiraSprintMembershipIssueListRow struct {
	Key      string                           `json:"key"`
	ID       string                           `json:"id,omitempty"`
	Position int                              `json:"position"`
	Values   map[string]any                   `json:"values"`
	Context  JiraSprintMembershipIssueContext `json:"context"`
}

type JiraSprintMembershipIssueContext struct {
	Sprint JiraSprintMembershipContextSprint `json:"sprint"`
}

type JiraSprintMembershipContextSprint struct {
	ID int `json:"id"`
}

type JiraSprintMembershipIssueListPage struct {
	Count         int     `json:"count"`
	Complete      bool    `json:"complete"`
	Truncated     bool    `json:"truncated"`
	PartialReason string  `json:"partial_reason,omitempty"`
	NextCursor    *string `json:"next_cursor"`
}

func DecodeJiraUserSearch(r io.Reader) (JiraUserSearch, error) {
	var users JiraUserSearch
	if err := decodeJiraWorkflowWire(r, jiraUserSearchWireMaxBytes, "Jira user search", &users, validateJiraUserSearchMembers); err != nil {
		return JiraUserSearch{}, err
	}
	if err := users.validate(); err != nil {
		return JiraUserSearch{}, fmt.Errorf("validate Jira user search: %w", err)
	}
	return users, nil
}

func DecodeJiraIssueCreate(r io.Reader) (JiraIssueCreate, error) {
	var issue JiraIssueCreate
	if err := decodeJiraWorkflowWire(r, jiraIssueCreateWireMaxBytes, "Jira issue create", &issue, validateJiraIssueCreateMembers); err != nil {
		return JiraIssueCreate{}, err
	}
	if err := issue.validate(); err != nil {
		return JiraIssueCreate{}, fmt.Errorf("validate Jira issue create: %w", err)
	}
	return issue, nil
}

func DecodeJiraEpicLink(r io.Reader) (JiraEpicLink, error) {
	var link JiraEpicLink
	if err := decodeJiraWorkflowWire(r, jiraEpicLinkWireMaxBytes, "Jira epic link", &link, validateJiraEpicLinkMembers); err != nil {
		return JiraEpicLink{}, err
	}
	if err := link.validate(); err != nil {
		return JiraEpicLink{}, fmt.Errorf("validate Jira epic link: %w", err)
	}
	return link, nil
}

func DecodeJiraPortfolioBoardList(r io.Reader) (JiraPortfolioBoardList, error) {
	var list JiraPortfolioBoardList
	if err := decodeJiraWorkflowWire(r, jiraPortfolioBoardListWireMaxBytes, "portfolio board list", &list, validateJiraPortfolioBoardListMembers); err != nil {
		return JiraPortfolioBoardList{}, err
	}
	if err := list.validate(); err != nil {
		return JiraPortfolioBoardList{}, fmt.Errorf("validate portfolio board list: %w", err)
	}
	return list, nil
}

func DecodeJiraPortfolioStructureFolders(r io.Reader) (JiraPortfolioStructureFolders, error) {
	var folders JiraPortfolioStructureFolders
	if err := decodeJiraWorkflowWire(r, jiraPortfolioFoldersWireMaxBytes, "portfolio structure folders", &folders, validateJiraPortfolioStructureFoldersMembers); err != nil {
		return JiraPortfolioStructureFolders{}, err
	}
	if err := folders.validate(); err != nil {
		return JiraPortfolioStructureFolders{}, fmt.Errorf("validate portfolio structure folders: %w", err)
	}
	return folders, nil
}

func DecodeJiraSprintCurrent(r io.Reader) (JiraSprintCurrent, error) {
	var sprint JiraSprintCurrent
	if err := decodeJiraWorkflowWire(r, jiraSprintCurrentWireMaxBytes, "sprint current", &sprint, validateJiraSprintCurrentMembers); err != nil {
		return JiraSprintCurrent{}, err
	}
	if err := sprint.validate(); err != nil {
		return JiraSprintCurrent{}, fmt.Errorf("validate sprint current: %w", err)
	}
	return sprint, nil
}

func DecodeJiraSprintMembershipIssueList(r io.Reader) (JiraSprintMembershipIssueList, error) {
	var list JiraSprintMembershipIssueList
	if err := decodeJiraWorkflowWire(r, jiraSprintMembershipWireMaxBytes, "sprint membership issue list", &list, validateJiraSprintMembershipMembers); err != nil {
		return JiraSprintMembershipIssueList{}, err
	}
	if err := list.validate(); err != nil {
		return JiraSprintMembershipIssueList{}, fmt.Errorf("validate sprint membership issue list: %w", err)
	}
	return list, nil
}

func decodeJiraWorkflowWire(r io.Reader, maximum int64, subject string, dst any, validateMembers func([]byte) error) error {
	limited := &io.LimitedReader{R: r, N: maximum + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("%s wire exceeds %d bytes", subject, maximum)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("decode %s wire: wire is not valid UTF-8", subject)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode %s wire: %w", subject, err)
	}
	if err := validateMembers(data); err != nil {
		return fmt.Errorf("decode %s wire: %w", subject, err)
	}
	if err := decodeStrict(bytes.NewReader(data), dst); err != nil {
		return fmt.Errorf("decode %s wire: %w", subject, err)
	}
	return nil
}

func validateJiraUserSearchMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira user search")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "Jira user search", []string{"users"}, nil); err != nil {
		return err
	}
	return jiraWorkflowArray(root["users"], "Jira user search.users", func(user map[string]json.RawMessage, owner string) error {
		return jiraWorkflowMembers(user, owner, []string{"displayName", "active"}, []string{"name", "key", "accountId", "email"})
	})
}

func validateJiraIssueCreateMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira issue create")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(root, "Jira issue create", []string{
		"key", "summary", "status", "type", "project", "description",
	}, nil)
}

func validateJiraEpicLinkMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira epic link")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(root, "Jira epic link", []string{"issue", "epic", "status"}, nil)
}

func validateJiraPortfolioBoardListMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "board list")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "board list", []string{"boards", "next_cursor", "count", "complete", "truncated"}, nil); err != nil {
		return err
	}
	return jiraWorkflowArray(root["boards"], "board list.boards", func(item map[string]json.RawMessage, owner string) error {
		return jiraWorkflowMembers(item, owner, []string{"id", "name", "type"}, []string{"project_key"})
	})
}

func validateJiraPortfolioStructureFoldersMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "structure folders")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "structure folders", []string{
		"schema_version", "structure", "forest_version", "folders", "complete", "warnings",
	}, nil); err != nil {
		return err
	}
	structure, err := jiraWorkflowNestedObject(root["structure"], "structure folders.structure")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(structure, "structure folders.structure", []string{"id", "name", "read_only"}, nil); err != nil {
		return err
	}
	version, err := jiraWorkflowNestedObject(root["forest_version"], "structure folders.forest_version")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(version, "structure folders.forest_version", []string{"signature", "version"}, nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(root["folders"], "structure folders.folders", validateJiraPortfolioFolderMembers); err != nil {
		return err
	}
	return jiraWorkflowArray(root["warnings"], "structure folders.warnings", nil)
}

func validateJiraPortfolioFolderMembers(folder map[string]json.RawMessage, owner string) error {
	if err := jiraWorkflowMembers(folder, owner, []string{
		"folder_id", "row_id", "name", "path", "depth", "parent_folder_id", "stats",
	}, nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(folder["path"], owner+".path", nil); err != nil {
		return err
	}
	stats, err := jiraWorkflowNestedObject(folder["stats"], owner+".stats")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(stats, owner+".stats", []string{
		"descendant_rows", "issue_rows", "unique_issues", "subfolders", "max_relative_depth",
	}, nil)
}

func validateJiraSprintCurrentMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "sprint current")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(root, "sprint current", []string{"id", "name", "state"}, []string{
		"start_date", "end_date", "complete_date", "goal", "origin_board_id",
	})
}

func validateJiraSprintMembershipMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "sprint membership issue list")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(root, "sprint membership issue list", []string{
		"schema_version", "source", "selection", "projection", "rows", "page",
	}, nil); err != nil {
		return err
	}
	source, err := jiraWorkflowNestedObject(root["source"], "sprint membership issue list.source")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(source, "sprint membership issue list.source", []string{"kind", "id"}, []string{"name"}); err != nil {
		return err
	}
	selection, err := jiraWorkflowNestedObject(root["selection"], "sprint membership issue list.selection")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(selection, "sprint membership issue list.selection", nil, nil); err != nil {
		return err
	}
	projection, err := jiraWorkflowNestedObject(root["projection"], "sprint membership issue list.projection")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(projection, "sprint membership issue list.projection", []string{"columns", "fields", "ordering"}, []string{"view"}); err != nil {
		return err
	}
	if err := jiraWorkflowArray(projection["columns"], "sprint membership issue list.projection.columns", nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(projection["fields"], "sprint membership issue list.projection.fields", nil); err != nil {
		return err
	}
	if err := jiraWorkflowArray(root["rows"], "sprint membership issue list.rows", validateJiraSprintMembershipRowMembers); err != nil {
		return err
	}
	page, err := jiraWorkflowNestedObject(root["page"], "sprint membership issue list.page")
	if err != nil {
		return err
	}
	return jiraWorkflowMembersAllowNull(page, "sprint membership issue list.page", []string{"count", "complete", "truncated", "next_cursor"}, []string{"partial_reason"}, []string{"next_cursor"})
}

func validateJiraSprintMembershipRowMembers(row map[string]json.RawMessage, owner string) error {
	if err := jiraWorkflowMembers(row, owner, []string{"key", "position", "values", "context"}, []string{"id"}); err != nil {
		return err
	}
	if _, err := jiraWorkflowNestedObject(row["values"], owner+".values"); err != nil {
		return err
	}
	context, err := jiraWorkflowNestedObject(row["context"], owner+".context")
	if err != nil {
		return err
	}
	if err := jiraWorkflowMembers(context, owner+".context", []string{"sprint"}, nil); err != nil {
		return err
	}
	sprint, err := jiraWorkflowNestedObject(context["sprint"], owner+".context.sprint")
	if err != nil {
		return err
	}
	return jiraWorkflowMembers(sprint, owner+".context.sprint", []string{"id"}, nil)
}

func jiraWorkflowObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraWorkflowNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	if jiraWorkflowNull(raw) {
		return nil, fmt.Errorf("%s must not be null", owner)
	}
	return jiraWorkflowObject(raw, owner)
}

func jiraWorkflowMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	return jiraWorkflowMembersAllowNull(object, owner, required, optional, nil)
}

func jiraWorkflowMembersAllowNull(object map[string]json.RawMessage, owner string, required, optional, nullable []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	allowNull := make(map[string]bool, len(nullable))
	for _, name := range nullable {
		allowNull[name] = true
	}
	for _, name := range required {
		allowed[name] = true
		raw, ok := object[name]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
		if jiraWorkflowNull(raw) && !allowNull[name] {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
		if raw, ok := object[name]; ok && jiraWorkflowNull(raw) && !allowNull[name] {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func jiraWorkflowArray(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	if len(values) > jiraWorkflowMaximumItems {
		return fmt.Errorf("%s exceeds %d items", owner, jiraWorkflowMaximumItems)
	}
	for index, value := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		if jiraWorkflowNull(value) {
			return fmt.Errorf("%s must not be null", itemOwner)
		}
		if validate != nil {
			item, err := jiraWorkflowNestedObject(value, itemOwner)
			if err != nil {
				return err
			}
			if err := validate(item, itemOwner); err != nil {
				return err
			}
		}
	}
	return nil
}

func jiraWorkflowNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (users JiraUserSearch) validate() error {
	if users.Users == nil || len(users.Users) > jiraWorkflowMaximumItems {
		return fmt.Errorf("users must contain at most %d entries", jiraWorkflowMaximumItems)
	}
	seen := make(map[string]struct{}, len(users.Users))
	for index, user := range users.Users {
		if !jiraWorkflowNormalized(user.DisplayName) ||
			user.Name != "" && !jiraWorkflowNormalized(user.Name) ||
			user.Key != "" && !jiraWorkflowNormalized(user.Key) ||
			user.AccountID != "" && !jiraWorkflowNormalized(user.AccountID) ||
			user.Email != "" && !jiraWorkflowNormalized(user.Email) {
			return fmt.Errorf("users[%d] is invalid", index)
		}
		identity := user.Name
		if identity == "" {
			identity = user.Key
		}
		if identity == "" {
			identity = user.AccountID
		}
		if identity == "" {
			return fmt.Errorf("users[%d] has no identity", index)
		}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("users[%d] duplicates an earlier identity", index)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func (issue JiraIssueCreate) validate() error {
	if !jiraWorkflowNormalized(issue.Key) || !jiraWorkflowNormalized(issue.Summary) ||
		!jiraWorkflowNormalizedOrEmpty(issue.Status) || !jiraWorkflowNormalized(issue.Type) ||
		!jiraWorkflowNormalized(issue.Project) || issue.Description == "" ||
		len(issue.Description) > jiraWorkflowMaximumStringBytes || !utf8.ValidString(issue.Description) {
		return fmt.Errorf("created issue is invalid")
	}
	return nil
}

func (link JiraEpicLink) validate() error {
	if !jiraWorkflowNormalized(link.Issue) || !jiraWorkflowNormalized(link.Epic) || link.Status != "linked" {
		return fmt.Errorf("epic link is invalid")
	}
	return nil
}

func (list JiraPortfolioBoardList) validate() error {
	if list.Boards == nil || len(list.Boards) > jiraWorkflowMaximumItems {
		return fmt.Errorf("boards must contain at most %d entries", jiraWorkflowMaximumItems)
	}
	if list.Count != len(list.Boards) {
		return fmt.Errorf("count must equal the number of boards")
	}
	if !jiraWorkflowNormalizedOrEmpty(list.NextCursor) {
		return fmt.Errorf("next_cursor is not whitespace-normalized")
	}
	if list.Complete != (list.NextCursor == "") || list.Truncated != !list.Complete {
		return fmt.Errorf("complete, truncated, and next_cursor contradict")
	}
	seen := make(map[int]struct{}, len(list.Boards))
	for index, board := range list.Boards {
		if board.ID <= 0 {
			return fmt.Errorf("boards[%d].id must be positive", index)
		}
		if _, duplicate := seen[board.ID]; duplicate {
			return fmt.Errorf("boards[%d].id duplicates an earlier board", index)
		}
		seen[board.ID] = struct{}{}
		if !jiraWorkflowNormalized(board.Name) {
			return fmt.Errorf("boards[%d].name is invalid", index)
		}
		if board.Type != "scrum" && board.Type != "kanban" {
			return fmt.Errorf("boards[%d].type %q is unsupported", index, board.Type)
		}
		if board.ProjectKey != "" && !jiraWorkflowNormalized(board.ProjectKey) {
			return fmt.Errorf("boards[%d].project_key is invalid", index)
		}
	}
	return nil
}

func (folders JiraPortfolioStructureFolders) validate() error {
	if folders.SchemaVersion != JiraWorkflowWireSchemaVersion {
		return fmt.Errorf("schema_version must be %d", JiraWorkflowWireSchemaVersion)
	}
	if folders.Structure.ID <= 0 || !jiraWorkflowNormalized(folders.Structure.Name) {
		return fmt.Errorf("structure identity is invalid")
	}
	if folders.ForestVersion.Version < 0 || folders.ForestVersion.Signature < 0 {
		return fmt.Errorf("forest_version is invalid")
	}
	if folders.Folders == nil || len(folders.Folders) > jiraWorkflowMaximumItems {
		return fmt.Errorf("folders must contain at most %d entries", jiraWorkflowMaximumItems)
	}
	if folders.Warnings == nil || len(folders.Warnings) > jiraWorkflowMaximumWarnings {
		return fmt.Errorf("warnings must contain at most %d entries", jiraWorkflowMaximumWarnings)
	}
	if folders.Complete != (len(folders.Warnings) == 0) {
		return fmt.Errorf("complete is not reconciled with warnings")
	}
	seenIDs := make(map[string]struct{}, len(folders.Folders))
	seenRows := make(map[int64]struct{}, len(folders.Folders))
	for index, folder := range folders.Folders {
		if !jiraWorkflowNormalized(folder.FolderID) || folder.RowID <= 0 || folder.Depth < 0 {
			return fmt.Errorf("folders[%d] identity or depth is invalid", index)
		}
		if _, duplicate := seenRows[folder.RowID]; duplicate {
			return fmt.Errorf("folders[%d].row_id duplicates an earlier folder", index)
		}
		if folder.ParentFolderID != "" {
			if !jiraWorkflowNormalized(folder.ParentFolderID) {
				return fmt.Errorf("folders[%d].parent_folder_id is invalid", index)
			}
			if _, present := seenIDs[folder.ParentFolderID]; !present {
				return fmt.Errorf("folders[%d].parent_folder_id does not name an earlier folder", index)
			}
		}
		if len(folder.Path) == 0 || len(folder.Path) > jiraWorkflowMaximumPathComponents || len(folder.Path) != folder.Depth+1 {
			return fmt.Errorf("folders[%d].path is not reconciled with depth", index)
		}
		for pathIndex, component := range folder.Path {
			if !jiraWorkflowNormalized(component) {
				return fmt.Errorf("folders[%d].path[%d] is invalid", index, pathIndex)
			}
		}
		if !utf8.ValidString(folder.Name) || len(folder.Name) > jiraWorkflowMaximumStringBytes {
			return fmt.Errorf("folders[%d].name is invalid", index)
		}
		if folder.Stats.DescendantRows < 0 || folder.Stats.IssueRows < 0 || folder.Stats.UniqueIssues < 0 ||
			folder.Stats.Subfolders < 0 || folder.Stats.MaxRelativeDepth < 0 || folder.Stats.IssueRows > folder.Stats.DescendantRows ||
			folder.Stats.UniqueIssues > folder.Stats.IssueRows || folder.Stats.MaxRelativeDepth > folder.Stats.DescendantRows {
			return fmt.Errorf("folders[%d].stats are contradictory", index)
		}
		seenIDs[folder.FolderID] = struct{}{}
		seenRows[folder.RowID] = struct{}{}
	}
	for index, warning := range folders.Warnings {
		if !jiraWorkflowNormalized(warning) {
			return fmt.Errorf("warnings[%d] is invalid", index)
		}
	}
	return nil
}

func (sprint JiraSprintCurrent) validate() error {
	if sprint.ID <= 0 || !jiraWorkflowNormalized(sprint.Name) {
		return fmt.Errorf("sprint identity is invalid")
	}
	if sprint.State != "active" && sprint.State != "closed" && sprint.State != "future" {
		return fmt.Errorf("sprint state %q is unsupported", sprint.State)
	}
	for name, value := range map[string]string{
		"start_date": sprint.StartDate, "end_date": sprint.EndDate, "complete_date": sprint.CompleteDate, "goal": sprint.Goal,
	} {
		if value != "" && !jiraWorkflowNormalized(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if sprint.OriginBoardID < 0 {
		return fmt.Errorf("origin_board_id is invalid")
	}
	return nil
}

func (list JiraSprintMembershipIssueList) validate() error {
	if list.SchemaVersion != JiraWorkflowWireSchemaVersion {
		return fmt.Errorf("schema_version must be %d", JiraWorkflowWireSchemaVersion)
	}
	if list.Source.Kind != "sprint" || !jiraWorkflowNormalized(list.Source.ID) {
		return fmt.Errorf("source is invalid")
	}
	sprintID, err := strconv.Atoi(list.Source.ID)
	if err != nil || sprintID <= 0 {
		return fmt.Errorf("source.id is not a positive sprint identity")
	}
	if list.Source.Name != "" && !jiraWorkflowNormalized(list.Source.Name) {
		return fmt.Errorf("source.name is invalid")
	}
	fields, err := list.Projection.validate()
	if err != nil {
		return err
	}
	if list.Rows == nil || len(list.Rows) > jiraWorkflowMaximumItems {
		return fmt.Errorf("rows must contain at most %d entries", jiraWorkflowMaximumItems)
	}
	seenKeys, seenIDs := map[string]struct{}{}, map[string]struct{}{}
	for index, row := range list.Rows {
		if !jiraWorkflowNormalized(row.Key) || row.Position != index {
			return fmt.Errorf("rows[%d] identity or position is invalid", index)
		}
		if _, duplicate := seenKeys[row.Key]; duplicate {
			return fmt.Errorf("rows[%d].key duplicates an earlier row", index)
		}
		seenKeys[row.Key] = struct{}{}
		if row.ID != "" {
			if !jiraWorkflowNormalized(row.ID) {
				return fmt.Errorf("rows[%d].id is invalid", index)
			}
			if _, duplicate := seenIDs[row.ID]; duplicate {
				return fmt.Errorf("rows[%d].id duplicates an earlier row", index)
			}
			seenIDs[row.ID] = struct{}{}
		}
		if row.Values == nil || len(row.Values) != len(fields) {
			return fmt.Errorf("rows[%d].values are not reconciled with projection.fields", index)
		}
		for _, field := range fields {
			if _, present := row.Values[field]; !present {
				return fmt.Errorf("rows[%d].values omit projected field %q", index, field)
			}
		}
		if row.Context.Sprint.ID != sprintID {
			return fmt.Errorf("rows[%d].context.sprint.id is not reconciled with source.id", index)
		}
	}
	page := list.Page
	if page.Count != len(list.Rows) || page.Complete == page.Truncated {
		return fmt.Errorf("page count or flags are contradictory")
	}
	if page.Complete {
		if page.NextCursor != nil || page.PartialReason != "" {
			return fmt.Errorf("complete page carries continuation or partial reason")
		}
		return nil
	}
	if page.NextCursor != nil {
		if !jiraWorkflowNormalized(*page.NextCursor) || page.PartialReason != "" || len(list.Rows) == 0 {
			return fmt.Errorf("resumable page carries contradictory qualification")
		}
		return nil
	}
	if !jiraWorkflowValidPartialReason(page.PartialReason) {
		return fmt.Errorf("terminal incomplete page has invalid partial_reason %q", page.PartialReason)
	}
	return nil
}

func (projection JiraSprintMembershipProjection) validate() ([]string, error) {
	if projection.Ordering != "backend-order" {
		return nil, fmt.Errorf("projection.ordering %q is unsupported", projection.Ordering)
	}
	if projection.View != "" && projection.View != "explicit" && !jiraSnapshotViewName.MatchString(projection.View) {
		return nil, fmt.Errorf("projection.view %q is invalid", projection.View)
	}
	if len(projection.Columns) == 0 || len(projection.Columns) > jiraWorkflowMaximumItems || projection.Fields == nil || len(projection.Fields) > jiraWorkflowMaximumItems {
		return nil, fmt.Errorf("projection arrays are invalid")
	}
	seenColumns, seenFields := map[string]struct{}{}, map[string]struct{}{}
	fields := make([]string, 0, len(projection.Columns))
	for index, column := range projection.Columns {
		if !jiraWorkflowNormalized(column) {
			return nil, fmt.Errorf("projection.columns[%d] is invalid", index)
		}
		if _, duplicate := seenColumns[column]; duplicate {
			return nil, fmt.Errorf("projection.columns[%d] duplicates an earlier column", index)
		}
		seenColumns[column] = struct{}{}
		switch column {
		case "position", "key", "id", "sprint.id":
			continue
		}
		if strings.Contains(column, ".") {
			return nil, fmt.Errorf("projection.columns[%d] is not a sprint column", index)
		}
		if _, duplicate := seenFields[column]; duplicate {
			return nil, fmt.Errorf("projection field %q is duplicated", column)
		}
		seenFields[column] = struct{}{}
		fields = append(fields, column)
	}
	if len(projection.Fields) != len(fields) {
		return nil, fmt.Errorf("projection.fields are not reconciled with columns")
	}
	for index, field := range projection.Fields {
		if field != fields[index] {
			return nil, fmt.Errorf("projection.fields are not reconciled with columns")
		}
	}
	return fields, nil
}

func jiraWorkflowValidPartialReason(reason string) bool {
	switch reason {
	case "legacy_unqualified", "pagination_unqualified", "pagination_stalled":
		return true
	default:
		return false
	}
}

func jiraWorkflowNormalized(value string) bool {
	return value != "" && len(value) <= jiraWorkflowMaximumStringBytes && utf8.ValidString(value) && strings.TrimSpace(value) == value
}

func jiraWorkflowNormalizedOrEmpty(value string) bool {
	return value == "" || jiraWorkflowNormalized(value)
}
