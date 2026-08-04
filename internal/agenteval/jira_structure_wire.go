package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	JiraStructureWireSchemaVersion = 1

	jiraStructureMetadataWireMaxBytes = 32 << 10
	jiraStructureViewWireMaxBytes     = 1 << 20
	jiraStructureViewMaxRows          = 1000
	jiraStructureViewMaxFields        = 32
	jiraStructureFieldMaxBytes        = 256
)

// JiraStructureMetadataView is the evaluator-owned released
// jira_structure_get projection.
type JiraStructureMetadataView struct {
	SchemaVersion int    `json:"schema_version"`
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	ReadOnly      bool   `json:"read_only"`
}

// JiraStructureView is the evaluator-owned released jira_structure_view
// projection. Values remain open JSON because requested Jira fields are
// backend- and plugin-defined; every surrounding member remains closed.
type JiraStructureView struct {
	SchemaVersion      int                        `json:"schema_version"`
	Structure          JiraStructureIdentity      `json:"structure"`
	ForestVersion      JiraStructureForestVersion `json:"forest_version"`
	ForestVersionGated bool                       `json:"forest_version_gated"`
	Projection         JiraStructureProjection    `json:"projection"`
	Rows               []JiraStructureRow         `json:"rows"`
	RowCount           int                        `json:"row_count"`
	IssueCount         int                        `json:"issue_count"`
	Complete           bool                       `json:"complete"`
	InaccessibleRows   []int64                    `json:"inaccessible_rows"`
	Selection          *JiraStructureSelection    `json:"selection,omitempty"`
	Warnings           []string                   `json:"warnings"`
}

type JiraStructureIdentity struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
}

type JiraStructureForestVersion struct {
	Signature int64 `json:"signature"`
	Version   int64 `json:"version"`
}

type JiraStructureProjection struct {
	Kind                  string   `json:"kind"`
	Source                string   `json:"source"`
	Attributes            []string `json:"attributes"`
	BrowserViewReproduced bool     `json:"browser_view_reproduced"`
	View                  string   `json:"view,omitempty"`
}

type JiraStructureRow struct {
	RowID         int64          `json:"row_id"`
	Depth         int            `json:"depth"`
	RelativeDepth *int           `json:"relative_depth,omitempty"`
	ParentRowID   int64          `json:"parent_row_id,omitempty"`
	ItemType      string         `json:"item_type"`
	ItemID        string         `json:"item_id"`
	Semantic      string         `json:"semantic,omitempty"`
	Position      int            `json:"position"`
	Accessible    bool           `json:"accessible"`
	Values        map[string]any `json:"values"`
}

type JiraStructureSelection struct {
	Kind     string   `json:"kind"`
	FolderID string   `json:"folder_id"`
	RowID    int64    `json:"row_id"`
	Path     []string `json:"path"`
}

// DecodeJiraStructureMetadata strictly decodes the fixed-size released
// Structure metadata projection.
func DecodeJiraStructureMetadata(r io.Reader) (JiraStructureMetadataView, error) {
	data, err := readJiraStructureWire(r, jiraStructureMetadataWireMaxBytes, "metadata")
	if err != nil {
		return JiraStructureMetadataView{}, err
	}
	if err := validateJiraStructureMetadataMembers(data); err != nil {
		return JiraStructureMetadataView{}, fmt.Errorf("decode jira structure metadata wire: %w", err)
	}
	var view JiraStructureMetadataView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return JiraStructureMetadataView{}, fmt.Errorf("decode jira structure metadata wire: %w", err)
	}
	if view.SchemaVersion != JiraStructureWireSchemaVersion || view.ID <= 0 ||
		strings.TrimSpace(view.Name) == "" || !utf8.ValidString(view.Name) {
		return JiraStructureMetadataView{}, fmt.Errorf("validate jira structure metadata: identity is invalid")
	}
	return view, nil
}

// DecodeJiraStructureView strictly decodes and independently reconciles one
// bounded released Structure snapshot.
func DecodeJiraStructureView(r io.Reader) (JiraStructureView, error) {
	data, err := readJiraStructureWire(r, jiraStructureViewWireMaxBytes, "view")
	if err != nil {
		return JiraStructureView{}, err
	}
	if err := validateJiraStructureViewMembers(data); err != nil {
		return JiraStructureView{}, fmt.Errorf("decode jira structure view wire: %w", err)
	}
	var view JiraStructureView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return JiraStructureView{}, fmt.Errorf("decode jira structure view wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return JiraStructureView{}, fmt.Errorf("validate jira structure view: %w", err)
	}
	return view, nil
}

func readJiraStructureWire(r io.Reader, maxBytes int64, subject string) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read jira structure %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("jira structure %s wire exceeds %d bytes", subject, maxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("decode jira structure %s wire: %w", subject, err)
	}
	return data, nil
}

func validateJiraStructureMetadataMembers(data []byte) error {
	root, err := jiraStructureObject(data, "metadata")
	if err != nil {
		return err
	}
	return jiraStructureMembers(root, "metadata", []string{"schema_version", "id", "name", "read_only"}, nil)
}

func validateJiraStructureViewMembers(data []byte) error {
	root, err := jiraStructureObject(data, "view")
	if err != nil {
		return err
	}
	if err := jiraStructureMembers(root, "view", []string{
		"schema_version", "structure", "forest_version", "forest_version_gated", "projection",
		"rows", "row_count", "issue_count", "complete", "inaccessible_rows", "warnings",
	}, []string{"selection"}); err != nil {
		return err
	}
	structure, err := jiraStructureNestedObject(root["structure"], "view.structure")
	if err != nil {
		return err
	}
	if err := jiraStructureMembers(structure, "view.structure", []string{"id", "name", "read_only"}, nil); err != nil {
		return err
	}
	version, err := jiraStructureNestedObject(root["forest_version"], "view.forest_version")
	if err != nil {
		return err
	}
	if err := jiraStructureMembers(version, "view.forest_version", []string{"signature", "version"}, nil); err != nil {
		return err
	}
	projection, err := jiraStructureNestedObject(root["projection"], "view.projection")
	if err != nil {
		return err
	}
	if err := jiraStructureMembers(projection, "view.projection",
		[]string{"kind", "source", "attributes", "browser_view_reproduced"}, []string{"view"}); err != nil {
		return err
	}
	if err := jiraStructureArray(projection["attributes"], "view.projection.attributes", nil); err != nil {
		return err
	}
	if err := jiraStructureArray(root["rows"], "view.rows", validateJiraStructureRowMembers); err != nil {
		return err
	}
	if err := jiraStructureArray(root["inaccessible_rows"], "view.inaccessible_rows", nil); err != nil {
		return err
	}
	if err := jiraStructureArray(root["warnings"], "view.warnings", nil); err != nil {
		return err
	}
	if raw, ok := root["selection"]; ok {
		selection, err := jiraStructureNestedObject(raw, "view.selection")
		if err != nil {
			return err
		}
		if err := jiraStructureMembers(selection, "view.selection", []string{"kind", "folder_id", "row_id", "path"}, nil); err != nil {
			return err
		}
		if err := jiraStructureArray(selection["path"], "view.selection.path", nil); err != nil {
			return err
		}
	}
	return nil
}

func validateJiraStructureRowMembers(row map[string]json.RawMessage, owner string) error {
	if err := jiraStructureMembers(row, owner,
		[]string{"row_id", "depth", "item_type", "item_id", "position", "accessible", "values"},
		[]string{"relative_depth", "parent_row_id", "semantic"}); err != nil {
		return err
	}
	_, err := jiraStructureNestedObject(row["values"], owner+".values")
	return err
}

func jiraStructureObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraStructureNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	return jiraStructureObject(raw, owner)
}

func jiraStructureMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		raw, ok := object[name]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
		if jiraStructureNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
		if raw, ok := object[name]; ok && jiraStructureNull(raw) {
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

func jiraStructureArray(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	for index, value := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		if jiraStructureNull(value) {
			return fmt.Errorf("%s must not be null", itemOwner)
		}
		if validate != nil {
			item, err := jiraStructureNestedObject(value, itemOwner)
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

func jiraStructureNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (view JiraStructureView) validate() error {
	if view.SchemaVersion != JiraStructureWireSchemaVersion || view.Structure.ID <= 0 ||
		strings.TrimSpace(view.Structure.Name) == "" ||
		!utf8.ValidString(view.Structure.Name) {
		return fmt.Errorf("structure identity is invalid")
	}
	if view.ForestVersion.Version < 0 || view.ForestVersionGated &&
		(view.ForestVersion.Signature == 0 || view.ForestVersion.Version < 1) {
		return fmt.Errorf("forest version gate is invalid")
	}
	if err := validateJiraStructureProjection(view.Projection); err != nil {
		return err
	}
	if view.RowCount != len(view.Rows) || view.RowCount > jiraStructureViewMaxRows || view.IssueCount < 0 {
		return fmt.Errorf("row or issue count is invalid")
	}

	rows := make(map[int64]JiraStructureRow, len(view.Rows))
	issueIDs := map[string]bool{}
	previousPosition := -1
	for index, row := range view.Rows {
		if row.RowID <= 0 || row.Depth < 0 || row.Position < 0 || row.Position <= previousPosition ||
			strings.TrimSpace(row.ItemType) == "" || strings.TrimSpace(row.ItemID) == "" ||
			!utf8.ValidString(row.ItemType) || !utf8.ValidString(row.ItemID) {
			return fmt.Errorf("row %d identity or ordering is invalid", index)
		}
		if _, duplicate := rows[row.RowID]; duplicate {
			return fmt.Errorf("row ids are not unique")
		}
		if row.ParentRowID < 0 || row.ParentRowID == row.RowID || !utf8.ValidString(row.Semantic) {
			return fmt.Errorf("row %d hierarchy is invalid", index)
		}
		if len(row.Values) != len(view.Projection.Attributes) {
			return fmt.Errorf("row %d value projection count is invalid", index)
		}
		for _, field := range view.Projection.Attributes {
			if _, exists := row.Values[field]; !exists {
				return fmt.Errorf("row %d value projection is incomplete", index)
			}
		}
		if strings.EqualFold(row.ItemType, "issue") {
			issueIDs[row.ItemID] = true
		}
		rows[row.RowID] = row
		previousPosition = row.Position
	}
	if view.IssueCount != len(issueIDs) {
		return fmt.Errorf("issue count does not match unique issue rows")
	}

	if err := validateJiraStructureSelection(view.Selection, view.Rows); err != nil {
		return err
	}
	inaccessible := make(map[int64]bool, len(view.InaccessibleRows))
	lastInaccessiblePosition := -1
	for _, rowID := range view.InaccessibleRows {
		row, exists := rows[rowID]
		if !exists || row.Accessible || inaccessible[rowID] || row.Position <= lastInaccessiblePosition {
			return fmt.Errorf("inaccessible row inventory is invalid or unordered")
		}
		inaccessible[rowID] = true
		lastInaccessiblePosition = row.Position
	}
	for _, row := range view.Rows {
		if !row.Accessible != inaccessible[row.RowID] {
			return fmt.Errorf("row accessibility is not reconciled")
		}
	}
	warnings := map[string]bool{}
	for _, warning := range view.Warnings {
		if warning == "" || warning != strings.TrimSpace(warning) || !utf8.ValidString(warning) || warnings[warning] {
			return fmt.Errorf("warnings are invalid")
		}
		warnings[warning] = true
	}
	if view.Complete && (len(inaccessible) != 0 || len(view.Warnings) != 0) ||
		!view.Complete && len(inaccessible) == 0 && len(view.Warnings) == 0 {
		return fmt.Errorf("completeness is not reconciled")
	}
	return nil
}

func validateJiraStructureProjection(projection JiraStructureProjection) error {
	if projection.Kind != "jira-fields-v1" || projection.Source != "explicit" ||
		projection.BrowserViewReproduced || projection.View != "explicit" ||
		len(projection.Attributes) == 0 || len(projection.Attributes) > jiraStructureViewMaxFields {
		return fmt.Errorf("projection identity or bounds are invalid")
	}
	seen := map[string]bool{}
	for _, field := range projection.Attributes {
		if field == "" || field != strings.TrimSpace(field) || len(field) > jiraStructureFieldMaxBytes ||
			field == "position" || field == "id" || strings.Contains(field, ".") || seen[field] || !utf8.ValidString(field) {
			return fmt.Errorf("projection attributes are invalid")
		}
		seen[field] = true
	}
	return nil
}

func validateJiraStructureSelection(selection *JiraStructureSelection, rows []JiraStructureRow) error {
	if selection == nil {
		for _, row := range rows {
			if row.RelativeDepth != nil {
				return fmt.Errorf("selector-free rows contain relative depth")
			}
		}
		return nil
	}
	if !jiraStructureOneOf(selection.Kind, "folder-id", "folder-row", "folder-path") ||
		strings.TrimSpace(selection.FolderID) == "" || selection.RowID <= 0 ||
		len(selection.Path) == 0 {
		return fmt.Errorf("selection identity is invalid")
	}
	for _, segment := range selection.Path {
		if strings.Join(strings.Fields(segment), " ") == "" || !utf8.ValidString(segment) {
			return fmt.Errorf("selection path is invalid")
		}
	}
	if len(rows) == 0 {
		return fmt.Errorf("selection path or rows are invalid")
	}
	root := rows[0]
	if root.RowID != selection.RowID || root.ItemID != selection.FolderID ||
		!strings.EqualFold(root.ItemType, "folder") || root.RelativeDepth == nil || *root.RelativeDepth != 0 {
		return fmt.Errorf("selected subtree root is not reconciled")
	}
	for index, row := range rows {
		if row.RelativeDepth == nil || index == 0 && *row.RelativeDepth != 0 || index > 0 && *row.RelativeDepth <= 0 {
			return fmt.Errorf("selected subtree depths are not reconciled")
		}
	}
	return nil
}

func jiraStructureOneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
