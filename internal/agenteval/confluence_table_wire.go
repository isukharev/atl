package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// ConfluenceTableWireSchemaVersion and ConfluenceTableWireCellContract are
	// the released table MCP projection. They are intentionally evaluator-owned:
	// the corpus must notice a product wire change rather than inherit it through
	// an app import.
	ConfluenceTableWireSchemaVersion = 3
	ConfluenceTableWireCellContract  = "confluence-table-cells/compact-v3"

	confluenceTableSummaryWireMaxBytes = 1 << 20
	confluenceTableExtractWireMaxBytes = 1 << 20
	confluenceTableFailureWireMaxBytes = 4 << 10
)

var confluenceTableWirePageID = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)

// ConfluenceTableSummaryView is the evaluator-owned released content-free
// confluence_table_summary wire. It deliberately duplicates only public wire
// fields; parsing and table construction remain product-owned.
type ConfluenceTableSummaryView struct {
	SchemaVersion       int                                `json:"schema_version"`
	CellContract        string                             `json:"cell_contract"`
	PageID              string                             `json:"page_id"`
	Version             int                                `json:"version"`
	PageVersionGated    bool                               `json:"page_version_gated"`
	TableCount          int                                `json:"table_count"`
	Table               int                                `json:"selected_table,omitempty"`
	ReturnedTableCount  int                                `json:"returned_table_count"`
	SelectionReconciled bool                               `json:"selection_reconciled"`
	Tables              []ConfluenceTableSummaryRecordView `json:"tables"`
}

// ConfluenceTableSummaryRecordView is one released content-free expanded-grid
// ledger. The evaluator checks arithmetic and selection provenance but does
// not reproduce CSF parsing or table expansion.
type ConfluenceTableSummaryRecordView struct {
	Index                     int  `json:"index"`
	RowCount                  int  `json:"row_count"`
	ColumnCount               int  `json:"column_count"`
	Rectangular               bool `json:"rectangular"`
	HeaderRowCount            int  `json:"header_row_count"`
	HeaderCellCount           int  `json:"header_cell_count"`
	ExpandedCellCount         int  `json:"expanded_cell_count"`
	OriginCellCount           int  `json:"origin_cell_count"`
	RepeatedCellCount         int  `json:"repeated_cell_count"`
	SyntheticEmptyCellCount   int  `json:"synthetic_empty_cell_count"`
	CellCountReconciled       bool `json:"cell_count_reconciled"`
	NonemptyTextCellCount     int  `json:"nonempty_text_cell_count"`
	NonemptyMarkdownCellCount int  `json:"nonempty_markdown_cell_count"`
	NonemptyRawCellCount      int  `json:"nonempty_raw_cell_count"`
	StyledCellCount           int  `json:"styled_cell_count"`
	StyleEntryCount           int  `json:"style_entry_count"`
	DistinctStyleMarkerCount  int  `json:"distinct_style_marker_count"`
	LinkedCellCount           int  `json:"linked_cell_count"`
	RowspanMetadataCellCount  int  `json:"rowspan_metadata_cell_count"`
	RowspanSourceCellCount    int  `json:"rowspan_source_cell_count"`
	RowspanCoveredCellCount   int  `json:"rowspan_covered_cell_count"`
	ColspanMetadataCellCount  int  `json:"colspan_metadata_cell_count"`
	ColspanSourceCellCount    int  `json:"colspan_source_cell_count"`
	ColspanCoveredCellCount   int  `json:"colspan_covered_cell_count"`
	WarningCount              int  `json:"warning_count"`
}

// ConfluenceTableExtractView is the evaluator-owned released selected-table
// confluence_table_extract wire. It is intentionally a consumer projection,
// not a second implementation of table extraction.
type ConfluenceTableExtractView struct {
	SchemaVersion       int                   `json:"schema_version"`
	CellContract        string                `json:"cell_contract"`
	PageID              string                `json:"page_id"`
	Title               string                `json:"title,omitempty"`
	Version             int                   `json:"version"`
	PageVersionGated    bool                  `json:"page_version_gated"`
	TableCount          int                   `json:"table_count"`
	Table               int                   `json:"selected_table"`
	ReturnedTableCount  int                   `json:"returned_table_count"`
	SelectionReconciled bool                  `json:"selection_reconciled"`
	Tables              []ConfluenceTableView `json:"tables"`
}

type ConfluenceTableView struct {
	Index       int                              `json:"index"`
	RowCount    int                              `json:"row_count"`
	ColumnCount int                              `json:"column_count"`
	Summary     ConfluenceTableSummaryRecordView `json:"summary"`
	Headers     []string                         `json:"headers,omitempty"`
	Rows        []ConfluenceTableRowView         `json:"rows"`
	Warnings    []string                         `json:"warnings,omitempty"`
	Metadata    map[string]map[string]any        `json:"metadata,omitempty"`
}

type ConfluenceTableRowView struct {
	Index  int                       `json:"index"`
	Header bool                      `json:"header,omitempty"`
	Cells  []ConfluenceTableCellView `json:"cells"`
}

type ConfluenceTableCellView struct {
	Row          int                       `json:"row"`
	Column       int                       `json:"column"`
	Text         string                    `json:"text"`
	Markdown     string                    `json:"markdown,omitempty"`
	Links        []ConfluenceTableLinkView `json:"links,omitempty"`
	Styles       map[string]string         `json:"styles,omitempty"`
	Header       bool                      `json:"header,omitempty"`
	Rowspan      int                       `json:"rowspan,omitempty"`
	Colspan      int                       `json:"colspan,omitempty"`
	Repeated     bool                      `json:"repeated,omitempty"`
	Synthetic    bool                      `json:"synthetic,omitempty"`
	SourceRow    int                       `json:"source_row,omitempty"`
	SourceColumn int                       `json:"source_column,omitempty"`
	Raw          map[string]string         `json:"raw,omitempty"`
}

type ConfluenceTableLinkView struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// ConfluenceTableSelectionFailureView is the content-free recoverable error
// used by the table-selection corpus route. It exposes only released counts
// and the reviewed recovery transition.
type ConfluenceTableSelectionFailureView struct {
	Kind        string
	Remediation string
	Message     string
	Requested   int
	Available   int
}

type confluenceTableFailureWire struct {
	Kind        string          `json:"kind"`
	Remediation string          `json:"remediation"`
	Message     string          `json:"message"`
	Recovery    json.RawMessage `json:"recovery"`
}

// DecodeConfluenceTableSummaryView strictly decodes one bounded released
// summary projection and checks the content-free structural ledger.
func DecodeConfluenceTableSummaryView(r io.Reader) (ConfluenceTableSummaryView, error) {
	data, err := readConfluenceTableWire(r, confluenceTableSummaryWireMaxBytes, "summary")
	if err != nil {
		return ConfluenceTableSummaryView{}, err
	}
	if err := validateConfluenceTableSummaryMembers(data); err != nil {
		return ConfluenceTableSummaryView{}, fmt.Errorf("decode Confluence table summary wire: %w", err)
	}
	var view ConfluenceTableSummaryView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return ConfluenceTableSummaryView{}, fmt.Errorf("decode Confluence table summary wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return ConfluenceTableSummaryView{}, fmt.Errorf("validate Confluence table summary wire: %w", err)
	}
	return view, nil
}

// DecodeConfluenceTableExtractView strictly decodes one bounded released
// selected-table extraction and reconciles the consumer-visible nested grid.
func DecodeConfluenceTableExtractView(r io.Reader) (ConfluenceTableExtractView, error) {
	data, err := readConfluenceTableWire(r, confluenceTableExtractWireMaxBytes, "extract")
	if err != nil {
		return ConfluenceTableExtractView{}, err
	}
	if err := validateConfluenceTableExtractMembers(data); err != nil {
		return ConfluenceTableExtractView{}, fmt.Errorf("decode Confluence table extract wire: %w", err)
	}
	var view ConfluenceTableExtractView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return ConfluenceTableExtractView{}, fmt.Errorf("decode Confluence table extract wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return ConfluenceTableExtractView{}, fmt.Errorf("validate Confluence table extract wire: %w", err)
	}
	return view, nil
}

// DecodeConfluenceTableSelectionFailureView strictly decodes the bounded,
// content-free table-selection failure before a recovery route is admitted.
func DecodeConfluenceTableSelectionFailureView(r io.Reader) (ConfluenceTableSelectionFailureView, error) {
	data, err := readConfluenceTableWire(r, confluenceTableFailureWireMaxBytes, "selection failure")
	if err != nil {
		return ConfluenceTableSelectionFailureView{}, err
	}
	root, err := confluenceTableWireObject(data, "selection failure")
	if err != nil {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("decode Confluence table selection failure: %w", err)
	}
	if err := confluenceTableWireMembers(root, "selection failure",
		[]string{"kind", "remediation", "message", "recovery"}, nil); err != nil {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("decode Confluence table selection failure: %w", err)
	}
	var wire confluenceTableFailureWire
	if err := decodeStrict(bytes.NewReader(data), &wire); err != nil {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("decode Confluence table selection failure: %w", err)
	}
	if !validCLIErrorRecoveryJSON(wire.Recovery) {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("validate Confluence table selection failure: recovery is invalid")
	}
	var recovery cliErrorRecovery
	if err := json.Unmarshal(wire.Recovery, &recovery); err != nil {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("decode Confluence table selection failure recovery: %w", err)
	}
	view := ConfluenceTableSelectionFailureView{
		Kind: wire.Kind, Remediation: wire.Remediation, Message: wire.Message,
	}
	if recovery.Requested != nil {
		view.Requested = *recovery.Requested
	}
	if recovery.Available != nil {
		view.Available = *recovery.Available
	}
	if err := view.validate(recovery); err != nil {
		return ConfluenceTableSelectionFailureView{}, fmt.Errorf("validate Confluence table selection failure: %w", err)
	}
	return view, nil
}

func readConfluenceTableWire(r io.Reader, maxBytes int64, subject string) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Confluence table %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("confluence table %s wire exceeds %d bytes", subject, maxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("decode Confluence table %s wire: %w", subject, err)
	}
	return data, nil
}

func validateConfluenceTableSummaryMembers(data []byte) error {
	root, err := confluenceTableWireObject(data, "summary")
	if err != nil {
		return err
	}
	if err := confluenceTableWireMembers(root, "summary", confluenceTableSummaryRootMembers,
		[]string{"selected_table"}); err != nil {
		return err
	}
	if err := validateConfluenceTableOptionalPositiveInt(root, "selected_table", "summary"); err != nil {
		return err
	}
	return validateConfluenceTableSummaryRecords(root["tables"], "summary.tables")
}

func validateConfluenceTableExtractMembers(data []byte) error {
	root, err := confluenceTableWireObject(data, "extract")
	if err != nil {
		return err
	}
	required := append(append([]string{}, confluenceTableSummaryRootMembers...), "selected_table")
	if err := confluenceTableWireMembers(root, "extract", required, []string{"title"}); err != nil {
		return err
	}
	if err := validateConfluenceTableOptionalNonemptyString(root, "title", "extract"); err != nil {
		return err
	}
	tables, err := confluenceTableWireArray(root["tables"], "extract.tables")
	if err != nil {
		return err
	}
	for index, raw := range tables {
		owner := fmt.Sprintf("extract.tables[%d]", index)
		table, err := confluenceTableWireObject(raw, owner)
		if err != nil {
			return err
		}
		if err := confluenceTableWireMembersNullable(table, owner,
			[]string{"index", "row_count", "column_count", "summary", "rows"},
			[]string{"headers", "warnings", "metadata"}, []string{"rows"}); err != nil {
			return err
		}
		if err := validateConfluenceTableSummaryRecord(table["summary"], owner+".summary"); err != nil {
			return err
		}
		if err := validateConfluenceTableOptionalStringArray(table, "headers", owner); err != nil {
			return err
		}
		if err := validateConfluenceTableOptionalStringArray(table, "warnings", owner); err != nil {
			return err
		}
		if err := validateConfluenceTableOptionalMetadata(table, owner); err != nil {
			return err
		}
		rows, err := confluenceTableWireNullableArray(table["rows"], owner+".rows")
		if err != nil {
			return err
		}
		for rowIndex, rowRaw := range rows {
			rowOwner := fmt.Sprintf("%s.rows[%d]", owner, rowIndex)
			row, err := confluenceTableWireObject(rowRaw, rowOwner)
			if err != nil {
				return err
			}
			if err := confluenceTableWireMembersNullable(
				row, rowOwner, []string{"index", "cells"}, []string{"header"}, []string{"cells"},
			); err != nil {
				return err
			}
			if err := validateConfluenceTableOptionalTrue(row, "header", rowOwner); err != nil {
				return err
			}
			cells, err := confluenceTableWireNullableArray(row["cells"], rowOwner+".cells")
			if err != nil {
				return err
			}
			for cellIndex, cellRaw := range cells {
				if err := validateConfluenceTableCellMembers(cellRaw, fmt.Sprintf("%s.cells[%d]", rowOwner, cellIndex)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var confluenceTableSummaryRootMembers = []string{
	"schema_version", "cell_contract", "page_id", "version", "page_version_gated", "table_count",
	"returned_table_count", "selection_reconciled", "tables",
}

var confluenceTableSummaryRecordMembers = []string{
	"index", "row_count", "column_count", "rectangular", "header_row_count", "header_cell_count",
	"expanded_cell_count", "origin_cell_count", "repeated_cell_count", "synthetic_empty_cell_count",
	"cell_count_reconciled", "nonempty_text_cell_count", "nonempty_markdown_cell_count",
	"nonempty_raw_cell_count", "styled_cell_count", "style_entry_count", "distinct_style_marker_count",
	"linked_cell_count", "rowspan_metadata_cell_count", "rowspan_source_cell_count",
	"rowspan_covered_cell_count", "colspan_metadata_cell_count", "colspan_source_cell_count",
	"colspan_covered_cell_count", "warning_count",
}

func validateConfluenceTableSummaryRecords(raw json.RawMessage, owner string) error {
	entries, err := confluenceTableWireArray(raw, owner)
	if err != nil {
		return err
	}
	for index, entry := range entries {
		if err := validateConfluenceTableSummaryRecord(entry, fmt.Sprintf("%s[%d]", owner, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateConfluenceTableSummaryRecord(raw json.RawMessage, owner string) error {
	record, err := confluenceTableWireObject(raw, owner)
	if err != nil {
		return err
	}
	return confluenceTableWireMembers(record, owner, confluenceTableSummaryRecordMembers, nil)
}

func validateConfluenceTableCellMembers(raw json.RawMessage, owner string) error {
	cell, err := confluenceTableWireObject(raw, owner)
	if err != nil {
		return err
	}
	if err := confluenceTableWireMembers(cell, owner, []string{"row", "column", "text"}, []string{
		"markdown", "links", "styles", "header", "rowspan", "colspan", "repeated", "synthetic",
		"source_row", "source_column", "raw",
	}); err != nil {
		return err
	}
	if err := validateConfluenceTableOptionalNonemptyString(cell, "markdown", owner); err != nil {
		return err
	}
	for _, name := range []string{"header", "repeated", "synthetic"} {
		if err := validateConfluenceTableOptionalTrue(cell, name, owner); err != nil {
			return err
		}
	}
	for _, name := range []string{"rowspan", "colspan", "source_row", "source_column"} {
		if err := validateConfluenceTableOptionalPositiveInt(cell, name, owner); err != nil {
			return err
		}
	}
	for _, name := range []string{"styles", "raw"} {
		if err := validateConfluenceTableOptionalStringMap(cell, name, owner); err != nil {
			return err
		}
	}
	linksRaw, ok := cell["links"]
	if !ok {
		return nil
	}
	links, err := confluenceTableWireArray(linksRaw, owner+".links")
	if err != nil || len(links) == 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s.links must be omitted when empty", owner)
	}
	for index, rawLink := range links {
		linkOwner := fmt.Sprintf("%s.links[%d]", owner, index)
		link, err := confluenceTableWireObject(rawLink, linkOwner)
		if err != nil {
			return err
		}
		if err := confluenceTableWireMembers(link, linkOwner, []string{"text", "url"}, nil); err != nil {
			return err
		}
	}
	return nil
}

func confluenceTableWireObject(raw []byte, owner string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s must not be null", owner)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func confluenceTableWireArray(raw json.RawMessage, owner string) ([]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s must not be null", owner)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%s must be an array", owner)
	}
	for index, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("%s[%d] must not be null", owner, index)
		}
	}
	return values, nil
}

func confluenceTableWireNullableArray(raw json.RawMessage, owner string) ([]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	return confluenceTableWireArray(raw, owner)
}

func confluenceTableWireMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	return confluenceTableWireMembersNullable(object, owner, required, optional, nil)
}

func confluenceTableWireMembersNullable(
	object map[string]json.RawMessage,
	owner string,
	required, optional, nullableRequired []string,
) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	nullable := make(map[string]struct{}, len(nullableRequired))
	for _, name := range nullableRequired {
		nullable[name] = struct{}{}
	}
	for _, name := range required {
		allowed[name] = struct{}{}
		raw, exists := object[name]
		if !exists {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
		_, mayBeNull := nullable[name]
		if !mayBeNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
		if raw, exists := object[name]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for name := range object {
		if _, allowed := allowed[name]; !allowed {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func validateConfluenceTableOptionalTrue(object map[string]json.RawMessage, name, owner string) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil || !value {
		return fmt.Errorf("%s.%s must be omitted when false", owner, name)
	}
	return nil
}

func validateConfluenceTableOptionalPositiveInt(object map[string]json.RawMessage, name, owner string) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 1 {
		return fmt.Errorf("%s.%s must be omitted when zero", owner, name)
	}
	return nil
}

func validateConfluenceTableOptionalNonemptyString(object map[string]json.RawMessage, name, owner string) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("%s.%s must be omitted when empty", owner, name)
	}
	return nil
}

func validateConfluenceTableOptionalStringArray(object map[string]json.RawMessage, name, owner string) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	values, err := confluenceTableWireArray(raw, owner+"."+name)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return fmt.Errorf("%s.%s must be omitted when empty", owner, name)
	}
	for index, value := range values {
		var text string
		if err := json.Unmarshal(value, &text); err != nil || !utf8.ValidString(text) {
			return fmt.Errorf("%s.%s[%d] must be a string", owner, name, index)
		}
	}
	return nil
}

func validateConfluenceTableOptionalStringMap(object map[string]json.RawMessage, name, owner string) error {
	raw, exists := object[name]
	if !exists {
		return nil
	}
	mapValue, err := confluenceTableWireObject(raw, owner+"."+name)
	if err != nil {
		return err
	}
	if len(mapValue) == 0 {
		return fmt.Errorf("%s.%s must be omitted when empty", owner, name)
	}
	for key, value := range mapValue {
		var text string
		if strings.TrimSpace(key) == "" || !utf8.ValidString(key) ||
			bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil || !utf8.ValidString(text) {
			return fmt.Errorf("%s.%s has an invalid entry", owner, name)
		}
	}
	return nil
}

func validateConfluenceTableOptionalMetadata(object map[string]json.RawMessage, owner string) error {
	raw, exists := object["metadata"]
	if !exists {
		return nil
	}
	metadata, err := confluenceTableWireObject(raw, owner+".metadata")
	if err != nil {
		return err
	}
	if len(metadata) == 0 {
		return fmt.Errorf("%s.metadata must be omitted when empty", owner)
	}
	for name, value := range metadata {
		if strings.TrimSpace(name) == "" || !utf8.ValidString(name) {
			return fmt.Errorf("%s.metadata has an invalid name", owner)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		if _, err := confluenceTableWireObject(value, owner+".metadata"); err != nil {
			return err
		}
	}
	return nil
}

func (view ConfluenceTableSummaryView) validate() error {
	return validateConfluenceTableEnvelope(
		view.SchemaVersion, view.CellContract, view.PageID, view.Version, view.TableCount,
		view.Table, view.ReturnedTableCount, view.SelectionReconciled, view.Tables, true,
	)
}

func (view ConfluenceTableExtractView) validate() error {
	if view.Title != "" && !utf8.ValidString(view.Title) {
		return fmt.Errorf("title is invalid")
	}
	if view.Table < 1 || len(view.Tables) != 1 {
		return fmt.Errorf("selected-table extract is not singular")
	}
	summaries := make([]ConfluenceTableSummaryRecordView, len(view.Tables))
	for index := range view.Tables {
		if err := view.Tables[index].validate(view.Table); err != nil {
			return err
		}
		summaries[index] = view.Tables[index].Summary
	}
	return validateConfluenceTableEnvelope(
		view.SchemaVersion, view.CellContract, view.PageID, view.Version, view.TableCount,
		view.Table, view.ReturnedTableCount, view.SelectionReconciled, summaries, false,
	)
}

func validateConfluenceTableEnvelope(
	schemaVersion int,
	cellContract, pageID string,
	version, tableCount, selectedTable, returnedTableCount int,
	selectionReconciled bool,
	tables []ConfluenceTableSummaryRecordView,
	requireReconciled bool,
) error {
	if schemaVersion != ConfluenceTableWireSchemaVersion || cellContract != ConfluenceTableWireCellContract ||
		!confluenceTableWirePageID.MatchString(pageID) || version < 1 || tableCount < 0 ||
		returnedTableCount != len(tables) || !selectionReconciled || tables == nil {
		return fmt.Errorf("table envelope is not reconciled")
	}
	if selectedTable == 0 {
		if len(tables) != tableCount {
			return fmt.Errorf("page-wide table selection is not reconciled")
		}
	} else if selectedTable < 1 || selectedTable > tableCount || len(tables) != 1 || tables[0].Index != selectedTable {
		return fmt.Errorf("selected table is not reconciled")
	}
	for index, record := range tables {
		expected := index + 1
		if selectedTable > 0 {
			expected = selectedTable
		}
		if err := record.validate(expected, requireReconciled); err != nil {
			return err
		}
	}
	return nil
}

func (record ConfluenceTableSummaryRecordView) validate(expectedIndex int, requireReconciled bool) error {
	if record.Index != expectedIndex || record.RowCount < 0 || record.ColumnCount < 0 || !record.Rectangular ||
		requireReconciled && (record.RowCount < 1 || record.ColumnCount < 1 || !record.CellCountReconciled) ||
		!requireReconciled && (record.RowCount > 0 && record.ColumnCount > 0) != record.CellCountReconciled {
		return fmt.Errorf("table summary identity is invalid")
	}
	expanded, ok := confluenceTableWireProduct(record.RowCount, record.ColumnCount)
	if !ok || record.ExpandedCellCount != expanded {
		return fmt.Errorf("table summary expanded-cell count is invalid")
	}
	counts := []int{
		record.HeaderRowCount, record.HeaderCellCount, record.OriginCellCount, record.RepeatedCellCount,
		record.SyntheticEmptyCellCount, record.NonemptyTextCellCount, record.NonemptyMarkdownCellCount,
		record.NonemptyRawCellCount, record.StyledCellCount, record.LinkedCellCount, record.RowspanMetadataCellCount,
		record.RowspanSourceCellCount, record.RowspanCoveredCellCount, record.ColspanMetadataCellCount,
		record.ColspanSourceCellCount, record.ColspanCoveredCellCount,
	}
	for _, count := range counts {
		if count < 0 || count > expanded {
			return fmt.Errorf("table summary count is outside the grid")
		}
	}
	remainingCells := expanded
	for _, count := range []int{record.OriginCellCount, record.RepeatedCellCount, record.SyntheticEmptyCellCount} {
		if count > remainingCells {
			return fmt.Errorf("table summary cell buckets are not reconciled")
		}
		remainingCells -= count
	}
	if record.StyleEntryCount < 0 || record.DistinctStyleMarkerCount < 0 || record.WarningCount < 0 ||
		record.HeaderRowCount > record.RowCount || remainingCells != 0 ||
		record.RowspanSourceCellCount > record.OriginCellCount ||
		record.RowspanCoveredCellCount > record.RepeatedCellCount ||
		record.ColspanSourceCellCount > record.OriginCellCount ||
		record.ColspanCoveredCellCount > record.RepeatedCellCount ||
		record.DistinctStyleMarkerCount > record.StyleEntryCount {
		return fmt.Errorf("table summary counters are not reconciled")
	}
	return nil
}

func (table ConfluenceTableView) validate(selected int) error {
	if table.Index != selected || table.RowCount < 0 || table.ColumnCount < 0 || len(table.Rows) != table.RowCount ||
		table.RowCount > 0 && table.Rows == nil {
		return fmt.Errorf("selected table dimensions are invalid")
	}
	if table.Headers != nil && len(table.Headers) != table.ColumnCount {
		return fmt.Errorf("selected table headers do not match its width")
	}
	for _, text := range table.Headers {
		if !utf8.ValidString(text) {
			return fmt.Errorf("selected table header is invalid")
		}
	}
	if table.Warnings != nil && len(table.Warnings) != table.Summary.WarningCount {
		return fmt.Errorf("selected table warning count is not reconciled")
	}
	for _, warning := range table.Warnings {
		if warning == "" || !utf8.ValidString(warning) {
			return fmt.Errorf("selected table warning is invalid")
		}
	}

	computed := ConfluenceTableSummaryRecordView{Index: table.Index, RowCount: table.RowCount, ColumnCount: table.ColumnCount, Rectangular: true}
	styleMarkers := map[[2]string]struct{}{}
	// Do not preallocate from wire-declared dimensions: the bounded payload may
	// still claim an enormous width before its short row is rejected below.
	cells := make(map[[2]int]ConfluenceTableCellView)
	for rowIndex, row := range table.Rows {
		if row.Index != rowIndex+1 || len(row.Cells) != table.ColumnCount ||
			table.ColumnCount > 0 && row.Cells == nil {
			return fmt.Errorf("selected table row is not rectangular")
		}
		if row.Header {
			computed.HeaderRowCount++
		}
		for columnIndex, cell := range row.Cells {
			if err := validateConfluenceTableCell(cell, rowIndex+1, columnIndex+1, cells); err != nil {
				return err
			}
			cells[[2]int{cell.Row, cell.Column}] = cell
			computed.ExpandedCellCount++
			if cell.Header {
				computed.HeaderCellCount++
			}
			if cell.Text != "" {
				computed.NonemptyTextCellCount++
			}
			if cell.Markdown != "" {
				computed.NonemptyMarkdownCellCount++
			}
			if len(cell.Raw) > 0 {
				computed.NonemptyRawCellCount++
			}
			if len(cell.Styles) > 0 {
				computed.StyledCellCount++
				computed.StyleEntryCount += len(cell.Styles)
				for name, value := range cell.Styles {
					styleMarkers[[2]string{name, value}] = struct{}{}
				}
			}
			if len(cell.Links) > 0 {
				computed.LinkedCellCount++
			}
			if cell.Rowspan > 1 {
				computed.RowspanMetadataCellCount++
			}
			if cell.Colspan > 1 {
				computed.ColspanMetadataCellCount++
			}
			switch {
			case cell.Repeated:
				computed.RepeatedCellCount++
				if cell.Row != cell.SourceRow {
					computed.RowspanCoveredCellCount++
				}
				if cell.Column != cell.SourceColumn {
					computed.ColspanCoveredCellCount++
				}
			case cell.Synthetic:
				computed.SyntheticEmptyCellCount++
			default:
				computed.OriginCellCount++
				if cell.Rowspan > 1 {
					computed.RowspanSourceCellCount++
				}
				if cell.Colspan > 1 {
					computed.ColspanSourceCellCount++
				}
			}
		}
	}
	if table.Headers != nil {
		if len(table.Rows) == 0 || !table.Rows[0].Header {
			return fmt.Errorf("selected table headers have no header-row projection")
		}
		for index, text := range table.Headers {
			if text != table.Rows[0].Cells[index].Text {
				return fmt.Errorf("selected table headers do not reconcile with its first row")
			}
		}
	}
	claimsReconciled := table.RowCount > 0 && table.ColumnCount > 0
	if claimsReconciled {
		if err := validateConfluenceTableClaims(table, cells); err != nil {
			return err
		}
	}
	computed.DistinctStyleMarkerCount = len(styleMarkers)
	computed.WarningCount = len(table.Warnings)
	computed.CellCountReconciled = claimsReconciled &&
		computed.OriginCellCount+computed.RepeatedCellCount+computed.SyntheticEmptyCellCount == computed.ExpandedCellCount
	if computed != table.Summary {
		return fmt.Errorf("selected table summary does not reconcile with its grid")
	}
	return nil
}

func validateConfluenceTableCell(
	cell ConfluenceTableCellView,
	row, column int,
	seen map[[2]int]ConfluenceTableCellView,
) error {
	if cell.Row != row || cell.Column != column || !utf8.ValidString(cell.Text) ||
		!utf8.ValidString(cell.Markdown) || cell.Rowspan < 0 || cell.Colspan < 0 ||
		cell.SourceRow < 0 || cell.SourceColumn < 0 || cell.Repeated && cell.Synthetic {
		return fmt.Errorf("selected table cell is invalid")
	}
	for _, link := range cell.Links {
		if strings.TrimSpace(link.URL) == "" || !utf8.ValidString(link.Text) || !utf8.ValidString(link.URL) {
			return fmt.Errorf("selected table link is invalid")
		}
	}
	for _, values := range []map[string]string{cell.Styles, cell.Raw} {
		for name, value := range values {
			if strings.TrimSpace(name) == "" || !utf8.ValidString(name) || !utf8.ValidString(value) {
				return fmt.Errorf("selected table cell map is invalid")
			}
		}
	}
	switch {
	case cell.Repeated:
		if cell.SourceRow < 1 || cell.SourceColumn < 1 || cell.SourceRow > row || cell.SourceColumn > column ||
			cell.SourceRow == row && cell.SourceColumn == column {
			return fmt.Errorf("repeated selected-table cell has an invalid source")
		}
		source, exists := seen[[2]int{cell.SourceRow, cell.SourceColumn}]
		if !exists || source.Repeated || source.Synthetic {
			return fmt.Errorf("repeated selected-table cell does not name an origin")
		}
	case cell.Synthetic:
		if cell.Text != "" || cell.Markdown != "" || len(cell.Links) != 0 || len(cell.Styles) != 0 || len(cell.Raw) != 0 ||
			cell.Header || cell.Rowspan != 0 || cell.Colspan != 0 || cell.SourceRow != 0 || cell.SourceColumn != 0 {
			return fmt.Errorf("synthetic selected-table cell carries content")
		}
	default:
		if cell.SourceRow != 0 || cell.SourceColumn != 0 {
			return fmt.Errorf("origin selected-table cell carries source coordinates")
		}
	}
	return nil
}

// validateConfluenceTableClaims reconstructs only the public emitted-grid
// span ledger. It does not inspect CSF or reproduce table extraction: it
// protects consumers from accepting a selected-table payload whose repeated
// and synthetic coordinates cannot be explained by the durable cell contract.
func validateConfluenceTableClaims(table ConfluenceTableView, cells map[[2]int]ConfluenceTableCellView) error {
	type coordinate = [2]int
	claims := make(map[coordinate]coordinate)
	origins := make(map[coordinate]ConfluenceTableCellView)
	for _, row := range table.Rows {
		for _, cell := range row.Cells {
			if cell.Repeated || cell.Synthetic {
				continue
			}
			origin := coordinate{cell.Row, cell.Column}
			origins[origin] = cell
			rowspan, colspan := 1, 1
			if cell.Rowspan > 1 {
				rowspan = cell.Rowspan
			}
			if cell.Colspan > 1 {
				colspan = cell.Colspan
			}
			if rowspan > table.RowCount-cell.Row+1 || colspan > table.ColumnCount-cell.Column+1 {
				return fmt.Errorf("selected table origin span exceeds its grid")
			}
			for rowOffset := 0; rowOffset < rowspan; rowOffset++ {
				for columnOffset := 0; columnOffset < colspan; columnOffset++ {
					at := coordinate{cell.Row + rowOffset, cell.Column + columnOffset}
					if _, exists := claims[at]; exists {
						return fmt.Errorf("selected table spans overlap")
					}
					claims[at] = origin
				}
			}
		}
	}
	for _, row := range table.Rows {
		for _, cell := range row.Cells {
			at := coordinate{cell.Row, cell.Column}
			owner, claimed := claims[at]
			switch {
			case cell.Repeated:
				source := coordinate{cell.SourceRow, cell.SourceColumn}
				origin, exists := origins[source]
				if !claimed || !exists || owner != source || cell.Rowspan != origin.Rowspan || cell.Colspan != origin.Colspan {
					return fmt.Errorf("repeated selected-table cell does not reconcile with its origin span")
				}
			case cell.Synthetic:
				if claimed {
					return fmt.Errorf("synthetic selected-table cell occupies an origin span")
				}
			default:
				if !claimed || owner != at {
					return fmt.Errorf("origin selected-table cell does not own its coordinate")
				}
			}
		}
	}
	if len(claims) > len(cells) {
		return fmt.Errorf("selected table span ledger exceeds its grid")
	}
	return nil
}

func confluenceTableWireProduct(left, right int) (int, bool) {
	if left < 0 || right < 0 {
		return 0, false
	}
	max := int(^uint(0) >> 1)
	if left != 0 && right > max/left {
		return 0, false
	}
	return left * right, true
}

func (view ConfluenceTableSelectionFailureView) validate(recovery cliErrorRecovery) error {
	if view.Kind != "not_found" || view.Remediation != "summarize_then_select_table" ||
		view.Message == "" || !utf8.ValidString(view.Message) ||
		recovery.Action != cliErrorRecoveryRereadThenReselect || recovery.RetrySafe == nil || *recovery.RetrySafe ||
		recovery.NextCapability != cliErrorCapabilityConfluenceTableSummary || recovery.Requested == nil ||
		recovery.Available == nil || recovery.Matches != nil || recovery.ExpectedVersion != nil ||
		recovery.ObservedVersion != nil || recovery.ExpectedForest != nil || recovery.ObservedForest != nil ||
		view.Requested < 1 || view.Available < 0 || view.Requested <= view.Available {
		return fmt.Errorf("selection recovery facts are invalid")
	}
	want := fmt.Sprintf("selected Confluence table index %d is out of range; available table count is %d", view.Requested, view.Available)
	if view.Message != want {
		return fmt.Errorf("selection failure message is not the released count-only shape")
	}
	return nil
}
