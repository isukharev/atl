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
	Root       string
	RootFields []string
	Fields     []string
	BatchSize  int
	Limit      int
	Format     string
	Out        string
	RawCSV     bool
}

// StructureExportResult describes a written Structure export.
type StructureExportResult struct {
	Path        string `json:"path"`
	Format      string `json:"format"`
	StructureID int64  `json:"structure_id"`
	RowCount    int    `json:"row_count"`
	IssueCount  int    `json:"issue_count"`
}

// StructureExportDocument is the JSON Structure export payload.
type StructureExportDocument struct {
	StructureID int64                    `json:"structure_id"`
	Version     *domain.StructureVersion `json:"version,omitempty"`
	Rows        []StructureExportRow     `json:"rows"`
	IssueIDs    []string                 `json:"issue_ids"`
	Issues      []JiraIssueSnapshot      `json:"issues"`
}

// StructureExportRow joins a Structure row with its Jira issue snapshot.
type StructureExportRow struct {
	RowID       int64          `json:"row_id"`
	Depth       int            `json:"depth"`
	ParentRowID int64          `json:"parent_row_id,omitempty"`
	ItemType    string         `json:"item_type"`
	ItemID      string         `json:"item_id"`
	Semantic    string         `json:"semantic,omitempty"`
	Position    int            `json:"position"`
	IssueKey    string         `json:"issue_key,omitempty"`
	IssueID     string         `json:"issue_id,omitempty"`
	Fields      map[string]any `json:"fields,omitempty"`
}

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

// StructureExport writes a Structure tree plus issue field snapshots.
func (s *JiraService) StructureExport(ctx context.Context, id int64, opts StructureExportOpts) (*StructureExportResult, error) {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json", "csv", "md", "markdown":
		if format == "markdown" {
			format = "md"
		}
	default:
		return nil, fmt.Errorf("%w: --format must be json, csv, or md", domain.ErrUsage)
	}
	if strings.TrimSpace(opts.Out) == "" || opts.Out == "-" {
		return nil, fmt.Errorf("%w: --out is required and must be a file path", domain.ErrUsage)
	}
	if opts.RawCSV && format != "csv" {
		return nil, fmt.Errorf("%w: --raw-csv requires --format csv", domain.ErrUsage)
	}
	pulled, err := s.StructurePullIssues(ctx, id, StructureIssuePullOpts{
		Root:       opts.Root,
		RootFields: opts.RootFields,
		Fields:     opts.Fields,
		BatchSize:  opts.BatchSize,
		Limit:      opts.Limit,
	})
	if err != nil {
		return nil, err
	}
	doc := structureExportDocument(id, pulled.Version, pulled.Rows, pulled.IssueIDs, pulled.Issues)
	data, err := renderStructureExport(format, doc, opts.Fields, opts.RawCSV)
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
		RowCount:    len(pulled.Rows),
		IssueCount:  len(pulled.Issues),
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
	rowIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		rowIDs = append(rowIDs, row.RowID)
	}
	fields := opts.RootFields
	if len(fields) == 0 {
		fields = []string{"key", "summary"}
	}
	values, err := s.StructureValues(ctx, id, rowIDs, fields)
	if err != nil {
		return nil, err
	}
	filtered := FilterStructureRows(rows, root, structureValueTextByRow(values))
	if len(filtered) == 0 {
		return nil, fmt.Errorf("%w: structure root %q was not found", domain.ErrUsage, root)
	}
	return filtered, nil
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

func structureValueTextByRow(values *domain.StructureValues) map[int64]string {
	out := map[int64]string{}
	if values == nil {
		return out
	}
	for _, response := range values.Responses {
		mergeStructureValueText(out, response)
	}
	if values.Raw != nil {
		mergeStructureValueText(out, values.Raw)
	}
	return out
}

func mergeStructureValueText(out map[int64]string, m map[string]any) {
	rows := int64SliceFromAny(m["rows"])
	for _, key := range []string{"data", "values"} {
		appendStructureValueData(out, rows, m[key])
	}
}

func appendStructureValueData(out map[int64]string, rows []int64, data any) {
	switch v := data.(type) {
	case []any:
		for i, entry := range v {
			if i >= len(rows) {
				break
			}
			appendRowText(out, rows[i], entry)
		}
	case map[string]any:
		for key, entry := range v {
			if rowID, err := strconv.ParseInt(key, 10, 64); err == nil {
				appendRowText(out, rowID, entry)
			}
		}
	}
}

func appendRowText(out map[int64]string, rowID int64, v any) {
	if rowID == 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		out[rowID] += " " + fmt.Sprintf("%v", v)
		return
	}
	out[rowID] += " " + string(b)
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

func structureExportDocument(id int64, version *domain.StructureVersion, rows []domain.StructureRow, issueIDs []string, issues []JiraIssueSnapshot) StructureExportDocument {
	byID := map[string]JiraIssueSnapshot{}
	for _, issue := range issues {
		if issue.ID != "" {
			byID[issue.ID] = issue
		}
	}
	exportRows := make([]StructureExportRow, 0, len(rows))
	for _, row := range rows {
		exportRow := StructureExportRow{
			RowID:       row.RowID,
			Depth:       row.Depth,
			ParentRowID: row.ParentRowID,
			ItemType:    row.ItemType,
			ItemID:      row.ItemID,
			Semantic:    row.Semantic,
			Position:    row.Position,
		}
		if issue, ok := byID[row.ItemID]; ok {
			exportRow.IssueKey = issue.Key
			exportRow.IssueID = issue.ID
			exportRow.Fields = issue.Fields
		}
		exportRows = append(exportRows, exportRow)
	}
	return StructureExportDocument{StructureID: id, Version: version, Rows: exportRows, IssueIDs: issueIDs, Issues: issues}
}

func renderStructureExport(format string, doc StructureExportDocument, fields []string, rawCSV bool) ([]byte, error) {
	switch format {
	case "json":
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "csv":
		return renderStructureExportCSV(doc, exportFields(fields), rawCSV)
	case "md":
		return renderStructureExportMarkdown(doc, exportFields(fields)), nil
	default:
		return nil, fmt.Errorf("%w: unsupported structure export format %q", domain.ErrUsage, format)
	}
}

func renderStructureExportCSV(doc StructureExportDocument, fields []string, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := append([]string{"row_id", "depth", "parent_row_id", "item_type", "item_id", "issue_key", "issue_id"}, fields...)
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range doc.Rows {
		record := []string{
			strconv.FormatInt(row.RowID, 10),
			strconv.Itoa(row.Depth),
			emptyZero(row.ParentRowID),
			row.ItemType,
			row.ItemID,
			row.IssueKey,
			row.IssueID,
		}
		for _, field := range fields {
			record = append(record, csvFieldValue(row.Fields[field]))
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

func renderStructureExportMarkdown(doc StructureExportDocument, fields []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Structure %d\n\n", doc.StructureID)
	for _, row := range doc.Rows {
		indent := strings.Repeat("  ", row.Depth)
		label := row.ItemType + ":" + row.ItemID
		if row.IssueKey != "" {
			label = row.IssueKey
		}
		fmt.Fprintf(&b, "%s- %s", indent, label)
		if len(fields) > 0 && row.Fields != nil {
			var parts []string
			for _, field := range fields {
				if value := csvFieldValue(row.Fields[field]); value != "" {
					parts = append(parts, field+"="+value)
				}
			}
			if len(parts) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(parts, "; "))
			}
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func emptyZero(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
