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
}

// StructureRowsResult is a parsed Structure row snapshot.
type StructureRowsResult struct {
	StructureID int64                    `json:"structure_id"`
	Version     *domain.StructureVersion `json:"version,omitempty"`
	Rows        []domain.StructureRow    `json:"rows"`
}

// StructureIssuePullOpts controls Structure issue collection.
type StructureIssuePullOpts struct {
	Root       string
	RootFields []string
	Fields     []string
	BatchSize  int
	Limit      int
	Out        string
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
}

// StructureExportOpts controls Structure offline exports.
type StructureExportOpts struct {
	Root      string
	Fields    []string
	BatchSize int
	Format    string
	Out       string
	RawCSV    bool
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
}

// StructureSnapshotRow is one jq/JSONL-friendly hierarchy row with selected
// Jira fields for issues and a best-effort summary for stored folders.
type StructureSnapshotRow struct {
	RowID       int64          `json:"row_id"`
	Depth       int            `json:"depth"`
	ParentRowID int64          `json:"parent_row_id,omitempty"`
	ItemType    string         `json:"item_type"`
	ItemID      string         `json:"item_id"`
	Semantic    string         `json:"semantic,omitempty"`
	Position    int            `json:"position"`
	Accessible  bool           `json:"accessible"`
	Values      map[string]any `json:"values"`
}

var defaultStructureAttributes = []string{"key", "summary", "status", "assignee", "priority", "issuetype"}

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
	rows, version, err := s.StructureRows(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.Root) != "" {
		rows, err = s.filterStructureRows(ctx, id, rows, opts)
		if err != nil {
			return nil, err
		}
	}
	return &StructureRowsResult{StructureID: id, Version: version, Rows: rows}, nil
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
	root := strings.TrimSpace(opts.Root)
	rootResolved := root == ""
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
		issues, err = s.collectExportIssues(ctx, batchedJQL("id", issueIDs, normalizeBatchSize(opts.BatchSize), false), issueFields, 0)
		if err != nil {
			return nil, err
		}
	}
	issuesByID := make(map[string]JiraIssueSnapshot, len(issues))
	for _, issue := range issues {
		issuesByID[issue.ID] = issue
	}
	folderLabels := s.structureFolderLabels(ctx, id, rows)

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
		IssueCount:       len(issueIDs),
		Complete:         true,
		InaccessibleRows: []int64{},
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
	for _, row := range rows {
		selected := structureSnapshotValues(row, attributes, issuesByID, folderLabels)
		accessible := row.ItemType != "issue" || issuesByID[row.ItemID].ID != ""
		if !accessible {
			result.Complete = false
			result.InaccessibleRows = append(result.InaccessibleRows, row.RowID)
		}
		result.Rows = append(result.Rows, StructureSnapshotRow{
			RowID: row.RowID, Depth: row.Depth, ParentRowID: row.ParentRowID,
			ItemType: row.ItemType, ItemID: row.ItemID, Semantic: row.Semantic,
			Position: row.Position, Accessible: accessible, Values: selected,
		})
	}
	result.RowCount = len(result.Rows)

	return result, nil
}

func structureSnapshotValues(row domain.StructureRow, attributes []string, issues map[string]JiraIssueSnapshot, folderLabels map[int64]string) map[string]any {
	selected := make(map[string]any, len(attributes))
	issue, isIssue := issues[row.ItemID]
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
	labels := map[int64]string{}
	var folderRows []int64
	for _, row := range rows {
		if structureItemTypeLabel(row.ItemType) == "folder" {
			folderRows = append(folderRows, row.RowID)
		}
	}
	if len(folderRows) == 0 {
		return labels
	}
	values, err := s.StructureValues(ctx, id, folderRows, []string{"key", "summary"})
	if err != nil {
		return labels
	}
	normalized, _, err := normalizeStructureValueRows(values)
	if err != nil {
		return labels
	}
	for _, rowID := range folderRows {
		if snapshotText(normalized[rowID]["key"]) == "" {
			labels[rowID] = snapshotText(normalized[rowID]["summary"])
		}
	}
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
	rowResult, err := s.StructureRowsWithOptions(ctx, id, StructureRowsOpts{Root: opts.Root, RootFields: opts.RootFields})
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
	}
	if len(ids) > 0 {
		queries := batchedJQL("id", ids, normalizeBatchSize(opts.BatchSize), false)
		issues, err := s.collectExportIssues(ctx, queries, opts.Fields, opts.Limit)
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
		Root:       opts.Root,
		Attributes: opts.Fields,
		BatchSize:  opts.BatchSize,
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
	issues, err := s.collectExportIssues(ctx, batchedJQL("id", issueIDs, normalizeBatchSize(0), false), issueFields, 0)
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
				Row           StructureSnapshotRow    `json:"row"`
			}{snapshot.SchemaVersion, snapshot.Structure.ID, snapshot.ForestVersion, snapshot.Projection, snapshot.Complete, snapshot.InaccessibleRows, row}
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
	header := append([]string{"row_id", "depth", "parent_row_id", "position", "item_type", "item_id", "accessible"}, snapshot.Projection.Attributes...)
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range snapshot.Rows {
		record := []string{
			strconv.FormatInt(row.RowID, 10),
			strconv.Itoa(row.Depth),
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
	if !snapshot.Complete {
		fmt.Fprintf(&b, "> **Partial visibility:** %d issue rows were unavailable; they remain in the hierarchy with empty values and `Accessible = false`.\n\n", len(snapshot.InaccessibleRows))
	}
	header := []string{"Row", "Tree", "Type", "Accessible"}
	for _, attribute := range snapshot.Projection.Attributes {
		header = append(header, markdownTableCell(attribute))
	}
	b.WriteString("| ")
	b.WriteString(strings.Join(header, " | "))
	b.WriteString(" |\n|")
	for range header {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, row := range snapshot.Rows {
		cells := []string{strconv.FormatInt(row.RowID, 10), structureTreeLabel(row), markdownTableCell(structureItemTypeLabel(row.ItemType)), strconv.FormatBool(row.Accessible)}
		for _, attribute := range snapshot.Projection.Attributes {
			cells = append(cells, markdownTableCell(snapshotText(row.Values[attribute])))
		}
		b.WriteString("| ")
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	return []byte(b.String())
}

// StructureSnapshotMarkdown renders the exact human-facing table used by
// `jira structure view -o text` and Markdown exports.
func StructureSnapshotMarkdown(snapshot *StructureSnapshot) string {
	return string(renderStructureSnapshotMarkdown(snapshot))
}

func structureTreeLabel(row StructureSnapshotRow) string {
	label := snapshotText(row.Values["summary"])
	key := snapshotText(row.Values["key"])
	if label == "" {
		label = key
	} else if key != "" && key != label {
		label = key + " — " + label
	}
	if label == "" {
		label = structureItemTypeLabel(row.ItemType) + ":" + row.ItemID
	}
	return strings.Repeat("↳ ", row.Depth) + markdownTableCell(label)
}

func structureItemTypeLabel(itemType string) string {
	if _, suffix, ok := strings.Cut(itemType, ":type-"); ok && suffix != "" {
		return suffix
	}
	return itemType
}

func markdownTableCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}

// snapshotText converts Structure's text-format values into compact cells. A
// backend may still wrap text in a small object; prefer its human label instead
// of leaking transport URLs and object internals into Markdown or CSV.
func snapshotText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, entry := range v {
			if text := snapshotText(entry); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		for _, key := range []string{"displayName", "name", "value", "key", "label", "text", "id"} {
			if text := snapshotText(v[key]); text != "" {
				return text
			}
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func normalizedSnapshotValue(value any) any {
	switch value.(type) {
	case map[string]any, []any:
		return snapshotText(value)
	default:
		return value
	}
}

func emptyZero(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
