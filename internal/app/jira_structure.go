package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// StructureRowsOpts controls optional Structure row filtering.
type StructureRowsOpts struct {
	Root       string
	RootFields []string
	StructureFolderSelector
}

// StructureRowsResult is a parsed Structure row snapshot.
type StructureRowsResult struct {
	StructureID int64                    `json:"structure_id"`
	Version     *domain.StructureVersion `json:"version,omitempty"`
	Rows        []domain.StructureRow    `json:"rows"`
	Selection   *StructureSelection      `json:"selection,omitempty"`
	Complete    bool                     `json:"complete"`
	Warnings    []string                 `json:"warnings"`
}

// StructureIssuePullOpts controls Structure issue collection.
type StructureIssuePullOpts struct {
	Root       string
	RootFields []string
	Fields     []string
	BatchSize  int
	Limit      int
	Out        string
	StructureFolderSelector
}

// StructureIssuePullResult contains issue snapshots referenced by a Structure.
type StructureIssuePullResult struct {
	StructureID      int64                    `json:"structure_id"`
	Version          *domain.StructureVersion `json:"version,omitempty"`
	Rows             []domain.StructureRow    `json:"rows"`
	IssueIDs         []string                 `json:"issue_ids"`
	Issues           []JiraIssueSnapshot      `json:"issues"`
	Count            int                      `json:"count"`
	InaccessibleRows []int64                  `json:"inaccessible_rows,omitempty"`
	Path             string                   `json:"path,omitempty"`
	Selection        *StructureSelection      `json:"selection,omitempty"`
	Complete         bool                     `json:"complete"`
	Warnings         []string                 `json:"warnings"`
}

// StructureExportOpts controls Structure offline exports.
type StructureExportOpts struct {
	Root      string
	Fields    []string
	BatchSize int
	Format    string
	Out       string
	RawCSV    bool
	StructureFolderSelector
}

// StructureExportResult describes a written Structure export.
type StructureExportResult struct {
	Path        string `json:"path"`
	Format      string `json:"format"`
	StructureID int64  `json:"structure_id"`
	RowCount    int    `json:"row_count"`
	IssueCount  int    `json:"issue_count"`
}

// StructureSnapshotOpts controls the normalized, agent-facing Structure view.
type StructureSnapshotOpts struct {
	Root       string
	Attributes []string
	BatchSize  int
	StructureFolderSelector
}

// StructureProjection makes it explicit that atl selected Jira fields; it is
// not an undiscoverable Structure browser saved-view configuration.
type StructureProjection struct {
	Kind                  string   `json:"kind"`
	Source                string   `json:"source"`
	Attributes            []string `json:"attributes"`
	BrowserViewReproduced bool     `json:"browser_view_reproduced"`
}

// StructureSnapshotMetadata is the compact identity needed to interpret a
// snapshot without carrying owner/permission transport objects into exports.
type StructureSnapshotMetadata struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// StructureSnapshot is a consistent normalized forest/value snapshot.
type StructureSnapshot struct {
	SchemaVersion    int                       `json:"schema_version"`
	Structure        StructureSnapshotMetadata `json:"structure"`
	ForestVersion    domain.StructureVersion   `json:"forest_version"`
	Projection       StructureProjection       `json:"projection"`
	Rows             []StructureSnapshotRow    `json:"rows"`
	RowCount         int                       `json:"row_count"`
	IssueCount       int                       `json:"issue_count"`
	Complete         bool                      `json:"complete"`
	InaccessibleRows []int64                   `json:"inaccessible_rows"`
	Selection        *StructureSelection       `json:"selection,omitempty"`
	Warnings         []string                  `json:"warnings"`
}

// StructureSnapshotRow is one jq/JSONL-friendly hierarchy row with selected
// Jira fields for issues and a best-effort summary for stored folders.
type StructureSnapshotRow struct {
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

var defaultStructureAttributes = []string{"key", "summary", "status", "assignee"}

// Structure fetches metadata for a Tempo Structure.
func (s *JiraService) Structure(ctx context.Context, id int64) (*domain.Structure, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	return s.structure.GetStructure(ctx, id)
}

// StructureForest returns the latest raw forest formula for a Tempo Structure.
func (s *JiraService) StructureForest(ctx context.Context, id int64) (*domain.StructureForest, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	return s.structure.StructureForest(ctx, id)
}

// StructureRows parses the latest forest formula into row records.
func (s *JiraService) StructureRows(ctx context.Context, id int64) ([]domain.StructureRow, *domain.StructureVersion, error) {
	forest, err := s.StructureForest(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rows, err := ParseStructureRows(forest)
	if err != nil {
		return nil, nil, err
	}
	return rows, &forest.Version, nil
}

// StructureRowsWithOptions parses Structure rows and optionally keeps one subtree.
func (s *JiraService) StructureRowsWithOptions(ctx context.Context, id int64, opts StructureRowsOpts) (*StructureRowsResult, error) {
	if err := validateStructureSelector(opts.Root, opts.StructureFolderSelector); err != nil {
		return nil, err
	}
	rows, version, err := s.StructureRows(ctx, id)
	if err != nil {
		return nil, err
	}
	result := &StructureRowsResult{StructureID: id, Version: version, Rows: rows, Complete: true, Warnings: []string{}}
	if selectorCount(opts.StructureFolderSelector) > 0 {
		labels, complete, warnings := s.structureFolderLabelsChecked(ctx, id, rows)
		selected, selection, selectErr := selectStructureFolder(rows, buildStructureFolders(rows, labels), complete, opts.StructureFolderSelector)
		if selectErr != nil {
			return nil, selectErr
		}
		result.Rows, result.Selection, result.Complete, result.Warnings = selected, selection, complete, warnings
	} else if strings.TrimSpace(opts.Root) != "" {
		rows, err = s.filterStructureRows(ctx, id, rows, opts)
		if err != nil {
			return nil, err
		}
		result.Rows = rows
	}
	return result, nil
}

// StructureValues fetches attribute values for selected Structure rows.
func (s *JiraService) StructureValues(ctx context.Context, id int64, rows []int64, fields []string) (*domain.StructureValues, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: pass at least one row id", domain.ErrUsage)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: pass at least one field", domain.ErrUsage)
	}
	return s.structure.StructureValues(ctx, id, rows, fields)
}

// StructureSnapshot joins the latest forest's stable item identities with Jira
// issue fields. It avoids joining calculated Structure rows through ephemeral
// row ids; only stored folder rows use Value API for best-effort labels.
func (s *JiraService) StructureSnapshot(ctx context.Context, id int64, opts StructureSnapshotOpts) (*StructureSnapshot, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	if err := validateStructureSelector(opts.Root, opts.StructureFolderSelector); err != nil {
		return nil, err
	}
	attributes, source := normalizedStructureAttributes(opts.Attributes)

	metadata, err := s.Structure(ctx, id)
	if err != nil {
		return nil, err
	}
	forest, err := s.StructureForest(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := ParseStructureRows(forest)
	if err != nil {
		return nil, err
	}
	forestRowCount := len(rows)
	folderLabels, labelsComplete, warnings := s.structureFolderLabelsChecked(ctx, id, rows)
	var selection *StructureSelection
	if selectorCount(opts.StructureFolderSelector) > 0 {
		rows, selection, err = selectStructureFolder(rows, buildStructureFolders(rows, folderLabels), labelsComplete, opts.StructureFolderSelector)
		if err != nil {
			return nil, err
		}
	}
	root := strings.TrimSpace(opts.Root)
	rootResolved := root == "" || selection != nil
	if root != "" {
		if filtered := FilterStructureRows(rows, root, nil); len(filtered) > 0 {
			rows = filtered
			rootResolved = true
		}
	}
	issueIDs := structureIssueIDs(rows)
	issueFields := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute != "key" {
			issueFields = append(issueFields, attribute)
		}
	}
	issues := []JiraIssueSnapshot{}
	if len(issueIDs) > 0 {
		issues, err = s.collectStructureIssues(ctx, batchedJQL("id", issueIDs, normalizeBatchSize(opts.BatchSize), false), issueFields, 0)
		if err != nil {
			return nil, err
		}
	}
	issuesByID := make(map[string]JiraIssueSnapshot, len(issues))
	for _, issue := range issues {
		issuesByID[issue.ID] = issue
	}
	if !rootResolved {
		valueText := map[int64]string{}
		for _, row := range rows {
			rowValues := structureSnapshotValues(row, attributes, issuesByID, folderLabels)
			b, _ := json.Marshal(rowValues)
			valueText[row.RowID] = string(b)
		}
		rows = FilterStructureRows(rows, root, valueText)
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: structure root %q was not found", domain.ErrUsage, root)
		}
	}
	// Completeness describes the emitted selection. Folder labels outside a
	// selected subtree must not make that subtree look partial. Exact path
	// selection already required a complete full-forest label projection.
	if len(rows) < forestRowCount && strings.TrimSpace(opts.FolderPath) == "" {
		folderLabels, labelsComplete, warnings = s.structureFolderLabelsChecked(ctx, id, rows)
	}
	result := &StructureSnapshot{
		SchemaVersion: 1,
		Structure: StructureSnapshotMetadata{
			ID: metadata.ID, Name: metadata.Name, ReadOnly: metadata.ReadOnly,
		},
		ForestVersion: forest.Version,
		Projection: StructureProjection{
			Kind: "jira-fields-v1", Source: source, Attributes: attributes,
			BrowserViewReproduced: false,
		},
		Rows:             []StructureSnapshotRow{},
		IssueCount:       0,
		Complete:         labelsComplete,
		InaccessibleRows: []int64{},
		Selection:        selection,
		Warnings:         warnings,
	}
	for _, row := range rows {
		selected := structureSnapshotValues(row, attributes, issuesByID, folderLabels)
		accessible := row.ItemType != "issue" || issuesByID[row.ItemID].ID != ""
		if !accessible {
			result.Complete = false
			result.InaccessibleRows = append(result.InaccessibleRows, row.RowID)
		}
		result.Rows = append(result.Rows, StructureSnapshotRow{
			RowID: row.RowID, Depth: row.Depth, RelativeDepth: row.RelativeDepth, ParentRowID: row.ParentRowID,
			ItemType: row.ItemType, ItemID: row.ItemID, Semantic: row.Semantic,
			Position: row.Position, Accessible: accessible, Values: selected,
		})
	}
	result.RowCount = len(result.Rows)
	result.IssueCount = len(structureIssueIDs(rows))

	return result, nil
}

func structureSnapshotValues(row domain.StructureRow, attributes []string, issues map[string]JiraIssueSnapshot, folderLabels map[int64]string) map[string]any {
	selected := make(map[string]any, len(attributes))
	issue, foundIssue := issues[row.ItemID]
	isIssue := row.ItemType == "issue" && foundIssue
	for _, attribute := range attributes {
		var value any
		if isIssue {
			if attribute == "key" {
				value = issue.Key
			} else {
				value = issue.Fields[attribute]
			}
		} else if attribute == "summary" {
			value = folderLabels[row.RowID]
		}
		selected[attribute] = normalizedSnapshotValue(value)
	}
	return selected
}

// Folder rows are stored items whose ids remain stable across generator
// expansion. Their summaries are useful hierarchy labels. Other calculated
// non-issue rows deliberately keep technical identities because Value API row
// ids can be regenerated before the value request is evaluated.
func (s *JiraService) structureFolderLabels(ctx context.Context, id int64, rows []domain.StructureRow) map[int64]string {
	labels, _, _ := s.structureFolderLabelsChecked(ctx, id, rows)
	return labels
}

func normalizedStructureAttributes(attributes []string) ([]string, string) {
	source := "explicit"
	if len(attributes) == 0 {
		attributes = defaultStructureAttributes
		source = "default"
	}
	out := normalizedExplicitStructureAttributes(attributes)
	if len(out) == 0 {
		out = append([]string(nil), defaultStructureAttributes...)
		source = "default"
	}
	return out, source
}

func normalizedExplicitStructureAttributes(attributes []string) []string {
	out := make([]string, 0, len(attributes))
	seen := map[string]bool{}
	for _, attribute := range attributes {
		attribute = strings.TrimSpace(attribute)
		if attribute == "" || seen[attribute] {
			continue
		}
		seen[attribute] = true
		out = append(out, attribute)
	}
	return out
}

func normalizeStructureValueRows(values *domain.StructureValues) (map[int64]map[string]any, map[int64]bool, error) {
	out := map[int64]map[string]any{}
	seen := map[int64]bool{}
	if values == nil {
		return out, seen, nil
	}
	responses := values.Responses
	if len(responses) == 0 && values.Raw != nil {
		responses = mapSlice(values.Raw["responses"])
	}
	for _, response := range responses {
		if structureValueErrorPresent(response["error"]) || structureValueErrorPresent(response["attributeErrors"]) {
			return nil, nil, fmt.Errorf("%w: Structure value response reported attribute errors", domain.ErrCheckFailed)
		}
		rows := int64SliceFromAny(response["rows"])
		for _, rowID := range rows {
			seen[rowID] = true
			if out[rowID] == nil {
				out[rowID] = map[string]any{}
			}
		}
		data, _ := response["data"].([]any)
		for _, rawBlock := range data {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			attribute, _ := block["attribute"].(map[string]any)
			attributeID, _ := attribute["id"].(string)
			rowValues, hasValues := block["values"].([]any)
			if attributeID != "" && hasValues {
				if len(rowValues) != len(rows) {
					return nil, nil, fmt.Errorf("%w: Structure returned %d values for %d rows in attribute %q", domain.ErrCheckFailed, len(rowValues), len(rows), attributeID)
				}
				for i, rowID := range rows {
					if i < len(rowValues) {
						out[rowID][attributeID] = rowValues[i]
					}
				}
				continue
			}
		}
		if len(data) == len(rows) {
			for i, rawRow := range data {
				if rowMap, ok := rawRow.(map[string]any); ok && rowMap["attribute"] == nil {
					for key, value := range rowMap {
						out[rows[i]][key] = value
					}
				}
			}
		}
	}
	return out, seen, nil
}

func structureValueErrorPresent(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}

func mapSlice(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, value := range arr {
		if m, ok := value.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// StructurePullIssues fetches Jira issue snapshots referenced by Structure issue rows.
func (s *JiraService) StructurePullIssues(ctx context.Context, id int64, opts StructureIssuePullOpts) (*StructureIssuePullResult, error) {
	rowResult, err := s.StructureRowsWithOptions(ctx, id, StructureRowsOpts{Root: opts.Root, RootFields: opts.RootFields, StructureFolderSelector: opts.StructureFolderSelector})
	if err != nil {
		return nil, err
	}
	ids := structureIssueIDs(rowResult.Rows)
	result := &StructureIssuePullResult{
		StructureID: id,
		Version:     rowResult.Version,
		Rows:        rowResult.Rows,
		IssueIDs:    ids,
		Issues:      []JiraIssueSnapshot{},
		Selection:   rowResult.Selection,
		Complete:    rowResult.Complete,
		Warnings:    rowResult.Warnings,
	}
	if len(ids) > 0 {
		queries := batchedJQL("id", ids, normalizeBatchSize(opts.BatchSize), false)
		issues, err := s.collectStructureIssues(ctx, queries, opts.Fields, opts.Limit)
		if err != nil {
			return nil, err
		}
		result.Issues = issues
	}
	result.Count = len(result.Issues)
	if strings.TrimSpace(opts.Out) != "" && opts.Out != "-" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := writeUserFile(opts.Out, append(data, '\n')); err != nil {
			return nil, err
		}
		result.Path = opts.Out
	}
	return result, nil
}

// StructureExport writes the same normalized snapshot exposed by structure
// view. Unlike pull-issues, it does not inflate rows with raw Jira objects.
func (s *JiraService) StructureExport(ctx context.Context, id int64, opts StructureExportOpts) (*StructureExportResult, error) {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json", "jsonl", "csv", "md", "markdown":
		if format == "markdown" {
			format = "md"
		}
	default:
		return nil, fmt.Errorf("%w: --format must be json, jsonl, csv, or md", domain.ErrUsage)
	}
	if strings.TrimSpace(opts.Out) == "" || opts.Out == "-" {
		return nil, fmt.Errorf("%w: --out is required and must be a file path", domain.ErrUsage)
	}
	if opts.RawCSV && format != "csv" {
		return nil, fmt.Errorf("%w: --raw-csv requires --format csv", domain.ErrUsage)
	}
	snapshot, err := s.StructureSnapshot(ctx, id, StructureSnapshotOpts{
		Root:                    opts.Root,
		Attributes:              opts.Fields,
		BatchSize:               opts.BatchSize,
		StructureFolderSelector: opts.StructureFolderSelector,
	})
	if err != nil {
		return nil, err
	}
	data, err := renderStructureSnapshot(format, snapshot, opts.RawCSV)
	if err != nil {
		return nil, err
	}
	if err := writeUserFile(opts.Out, data); err != nil {
		return nil, err
	}
	return &StructureExportResult{
		Path:        opts.Out,
		Format:      format,
		StructureID: id,
		RowCount:    snapshot.RowCount,
		IssueCount:  snapshot.IssueCount,
	}, nil
}

// ParseStructureRows parses Structure's forest formula. A component has the
// documented shape rowID:depth:item[:semantic]. Issue rows use a numeric item id;
// non-issue rows use itemType/itemID or itemType//stringItemID.
func ParseStructureRows(forest *domain.StructureForest) ([]domain.StructureRow, error) {
	if forest == nil || strings.TrimSpace(forest.Formula) == "" {
		return nil, nil
	}
	parts := strings.Split(forest.Formula, ",")
	rows := make([]domain.StructureRow, 0, len(parts))
	depthStack := map[int]int64{}
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		fields := strings.Split(raw, ":")
		if len(fields) < 3 {
			return nil, fmt.Errorf("%w: invalid structure formula component %q", domain.ErrUsage, raw)
		}
		rowID, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid structure row id %q", domain.ErrUsage, fields[0])
		}
		depth, err := strconv.Atoi(fields[1])
		if err != nil || depth < 0 {
			return nil, fmt.Errorf("%w: invalid structure row depth %q", domain.ErrUsage, fields[1])
		}
		itemType, itemID := parseStructureItem(fields[2], forest.ItemTypes)
		row := domain.StructureRow{
			RowID:    rowID,
			Depth:    depth,
			ItemType: itemType,
			ItemID:   itemID,
			Position: len(rows),
		}
		if len(fields) > 3 {
			row.Semantic = strings.Join(fields[3:], ":")
		}
		if depth > 0 {
			row.ParentRowID = depthStack[depth-1]
		}
		rows = append(rows, row)
		depthStack[depth] = rowID
		for staleDepth := range depthStack {
			if staleDepth > depth {
				delete(depthStack, staleDepth)
			}
		}
	}
	return rows, nil
}

func parseStructureItem(item string, itemTypes map[string]string) (string, string) {
	if _, err := strconv.ParseInt(item, 10, 64); err == nil {
		return "issue", item
	}
	typeID, itemID, ok := strings.Cut(item, "//")
	if !ok {
		typeID, itemID, ok = strings.Cut(item, "/")
	}
	if !ok {
		return "", item
	}
	itemType := typeID
	if itemTypes != nil && itemTypes[typeID] != "" {
		itemType = itemTypes[typeID]
	}
	return itemType, itemID
}

func (s *JiraService) filterStructureRows(ctx context.Context, id int64, rows []domain.StructureRow, opts StructureRowsOpts) ([]domain.StructureRow, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" || len(rows) == 0 {
		return rows, nil
	}
	if filtered := FilterStructureRows(rows, root, nil); len(filtered) > 0 {
		return filtered, nil
	}
	fields := opts.RootFields
	if len(fields) == 0 {
		fields = []string{"key", "summary"}
	}
	folderValues := map[int64]string{}
	if slicesContain(fields, "summary") {
		for rowID, summary := range s.structureFolderLabels(ctx, id, rows) {
			folderValues[rowID] = summary
		}
	}
	if filtered := FilterStructureRows(rows, root, folderValues); len(filtered) > 0 {
		return filtered, nil
	}
	issueIDs := structureIssueIDs(rows)
	issueFields := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "key" {
			issueFields = append(issueFields, field)
		}
	}
	issues, err := s.collectStructureIssues(ctx, batchedJQL("id", issueIDs, normalizeBatchSize(0), false), issueFields, 0)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]JiraIssueSnapshot, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	valueText := map[int64]string{}
	for _, row := range rows {
		values := structureSnapshotValues(row, fields, byID, nil)
		b, _ := json.Marshal(values)
		valueText[row.RowID] = string(b)
	}
	filtered := FilterStructureRows(rows, root, valueText)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("%w: structure root %q was not found", domain.ErrUsage, root)
	}
	return filtered, nil
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// FilterStructureRows returns the first row matching root and its descendants.
func FilterStructureRows(rows []domain.StructureRow, root string, valueText map[int64]string) []domain.StructureRow {
	root = strings.ToLower(strings.TrimSpace(root))
	if root == "" {
		return rows
	}
	rootIdx := -1
	for i, row := range rows {
		if structureRowMatches(row, valueText[row.RowID], root) {
			rootIdx = i
			break
		}
	}
	if rootIdx < 0 {
		return nil
	}
	rootDepth := rows[rootIdx].Depth
	out := []domain.StructureRow{rows[rootIdx]}
	for i := rootIdx + 1; i < len(rows); i++ {
		if rows[i].Depth <= rootDepth {
			break
		}
		out = append(out, rows[i])
	}
	return out
}

func structureRowMatches(row domain.StructureRow, valueText, root string) bool {
	parts := []string{
		strconv.FormatInt(row.RowID, 10),
		strconv.Itoa(row.Depth),
		row.ItemType,
		row.ItemID,
		row.Semantic,
		valueText,
	}
	return strings.Contains(strings.ToLower(strings.Join(parts, " ")), root)
}

func structureIssueIDs(rows []domain.StructureRow) []string {
	seen := map[string]bool{}
	var ids []string
	for _, row := range rows {
		if row.ItemType != "issue" {
			continue
		}
		if _, err := strconv.ParseInt(row.ItemID, 10, 64); err != nil {
			continue
		}
		if seen[row.ItemID] {
			continue
		}
		seen[row.ItemID] = true
		ids = append(ids, row.ItemID)
	}
	return ids
}

func normalizeBatchSize(n int) int {
	if n <= 0 {
		return 100
	}
	return n
}

func int64SliceFromAny(v any) []int64 {
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

func renderStructureSnapshot(format string, snapshot *StructureSnapshot, rawCSV bool) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("%w: Structure snapshot is unavailable", domain.ErrCheckFailed)
	}
	switch format {
	case "json":
		b, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "jsonl":
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		for _, row := range snapshot.Rows {
			record := struct {
				SchemaVersion int                     `json:"schema_version"`
				StructureID   int64                   `json:"structure_id"`
				ForestVersion domain.StructureVersion `json:"forest_version"`
				Projection    StructureProjection     `json:"projection"`
				Complete      bool                    `json:"complete"`
				Inaccessible  []int64                 `json:"inaccessible_rows"`
				Selection     *StructureSelection     `json:"selection,omitempty"`
				Warnings      []string                `json:"warnings"`
				Row           StructureSnapshotRow    `json:"row"`
			}{
				SchemaVersion: snapshot.SchemaVersion, StructureID: snapshot.Structure.ID,
				ForestVersion: snapshot.ForestVersion, Projection: snapshot.Projection,
				Complete: snapshot.Complete, Inaccessible: snapshot.InaccessibleRows,
				Selection: snapshot.Selection, Warnings: snapshot.Warnings, Row: row,
			}
			if err := enc.Encode(record); err != nil {
				return nil, err
			}
		}
		return b.Bytes(), nil
	case "csv":
		return renderStructureSnapshotCSV(snapshot, rawCSV)
	case "md":
		return renderStructureSnapshotMarkdown(snapshot), nil
	default:
		return nil, fmt.Errorf("%w: unsupported structure export format %q", domain.ErrUsage, format)
	}
}

func renderStructureSnapshotCSV(snapshot *StructureSnapshot, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := append([]string{"row_id", "depth", "relative_depth", "parent_row_id", "position", "item_type", "item_id", "accessible"}, snapshot.Projection.Attributes...)
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range snapshot.Rows {
		record := []string{
			strconv.FormatInt(row.RowID, 10),
			strconv.Itoa(row.Depth),
			optionalInt(row.RelativeDepth),
			emptyZero(row.ParentRowID),
			strconv.Itoa(row.Position),
			row.ItemType,
			row.ItemID,
			strconv.FormatBool(row.Accessible),
		}
		for _, attribute := range snapshot.Projection.Attributes {
			record = append(record, snapshotText(row.Values[attribute]))
		}
		if err := w.Write(spreadsheetRecord(record, rawCSV)); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func renderStructureSnapshotMarkdown(snapshot *StructureSnapshot) []byte {
	var b strings.Builder
	name := snapshot.Structure.Name
	b.WriteString("# Structure")
	if name != "" {
		b.WriteString(": ")
		b.WriteString(markdownTableCell(name))
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "> ATL Jira-field projection (`%s`, source: `%s`); browser saved-view columns are not reproduced. Forest version: `%d`; rows: %d.\n\n",
		snapshot.Projection.Kind, snapshot.Projection.Source, snapshot.ForestVersion.Version, snapshot.RowCount)
	if snapshot.Selection != nil {
		fmt.Fprintf(&b, "> Selected folder: `%s`; path: %s; depth is relative in this table.\n\n", snapshot.Selection.FolderID, markdownTableCell(strings.Join(snapshot.Selection.Path, " / ")))
	}
	if len(snapshot.InaccessibleRows) > 0 {
		fmt.Fprintf(&b, "> **Partial visibility:** %d issue rows were unavailable; they remain in the hierarchy with empty values and `Accessible = false`.\n\n", len(snapshot.InaccessibleRows))
	}
	for _, warning := range snapshot.Warnings {
		fmt.Fprintf(&b, "> **Warning:** %s.\n\n", warning)
	}
	header := []string{"#", "Depth", "Type", "Item"}
	for _, attribute := range snapshot.Projection.Attributes {
		header = append(header, issueListColumnLabel(attribute))
	}
	header = append(header, "Access")
	rows := make([][]string, 0, len(snapshot.Rows))
	for index, row := range snapshot.Rows {
		depth := row.Depth
		if row.RelativeDepth != nil {
			depth = *row.RelativeDepth
		}
		cells := []string{strconv.Itoa(index), strconv.Itoa(depth), structureItemTypeLabel(row.ItemType), row.ItemID}
		for _, attribute := range snapshot.Projection.Attributes {
			cells = append(cells, snapshotText(row.Values[attribute]))
		}
		cells = append(cells, strconv.FormatBool(row.Accessible))
		rows = append(rows, cells)
	}
	b.WriteString(MarkdownTable(header, rows))
	return []byte(b.String())
}

// StructureSnapshotMarkdown renders the exact human-facing table used by
// `jira structure view -o text` and Markdown exports.
func StructureSnapshotMarkdown(snapshot *StructureSnapshot) string {
	return string(renderStructureSnapshotMarkdown(snapshot))
}

func structureItemTypeLabel(itemType string) string {
	if _, suffix, ok := strings.Cut(itemType, ":type-"); ok && suffix != "" {
		return suffix
	}
	return itemType
}

func emptyZero(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
