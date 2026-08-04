package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfluenceTableWiresAcceptReleasedShapes(t *testing.T) {
	summary, err := DecodeConfluenceTableSummaryView(bytes.NewReader(validConfluenceTableSummaryWire(t)))
	if err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != ConfluenceTableWireSchemaVersion || summary.PageID != "7001" ||
		summary.TableCount != 1 || summary.ReturnedTableCount != 1 || summary.Table != 0 ||
		len(summary.Tables) != 1 || summary.Tables[0].NonemptyTextCellCount != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	selectedSummary, err := DecodeConfluenceTableSummaryView(bytes.NewReader(mutateConfluenceTableWire(t, validConfluenceTableSummaryWire(t), func(root map[string]any) {
		root["selected_table"] = 1
	})))
	if err != nil || selectedSummary.Table != 1 {
		t.Fatalf("selected summary=%+v err=%v", selectedSummary, err)
	}

	extract, err := DecodeConfluenceTableExtractView(bytes.NewReader(validConfluenceTableExtractWire(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	if extract.Title != "Synthetic" || extract.Table != 1 || !extract.SelectionReconciled ||
		len(extract.Tables) != 1 || len(extract.Tables[0].Rows) != 1 ||
		extract.Tables[0].Rows[0].Cells[0].Text != "Synthetic" {
		t.Fatalf("extract=%+v", extract)
	}
	if _, err := DecodeConfluenceTableExtractView(bytes.NewReader(validConfluenceTableExtractWire(t, false))); err != nil {
		t.Fatalf("extract without optional title: %v", err)
	}
	empty, err := DecodeConfluenceTableExtractView(bytes.NewReader(validConfluenceTableEmptyExtractWire(t)))
	if err != nil || empty.Tables[0].RowCount != 0 || empty.Tables[0].Rows != nil || empty.Tables[0].Summary.CellCountReconciled {
		t.Fatalf("released empty selected table=%+v err=%v", empty, err)
	}
	oneByZero := mutateConfluenceTableWire(t, validConfluenceTableEmptyExtractWire(t), func(root map[string]any) {
		table := root["tables"].([]any)[0].(map[string]any)
		table["row_count"] = 1
		table["rows"] = []any{map[string]any{"index": 1, "cells": nil}}
		table["summary"].(map[string]any)["row_count"] = 1
	})
	zeroWidth, err := DecodeConfluenceTableExtractView(bytes.NewReader(oneByZero))
	if err != nil || zeroWidth.Tables[0].RowCount != 1 || zeroWidth.Tables[0].ColumnCount != 0 ||
		len(zeroWidth.Tables[0].Rows) != 1 || zeroWidth.Tables[0].Rows[0].Cells != nil {
		t.Fatalf("released zero-width selected table=%+v err=%v", zeroWidth, err)
	}
	multiStyle := mutateConfluenceTableWire(t, validConfluenceTableExtractWire(t, true), func(root map[string]any) {
		table := root["tables"].([]any)[0].(map[string]any)
		cell := table["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
		cell["styles"] = map[string]any{"color": "red", "background-color": "white"}
		table["warnings"] = []any{"first", "second"}
		summary := table["summary"].(map[string]any)
		summary["styled_cell_count"] = 1
		summary["style_entry_count"] = 2
		summary["distinct_style_marker_count"] = 2
		summary["warning_count"] = 2
		table["metadata"] = map[string]any{"optional": nil}
	})
	if _, err := DecodeConfluenceTableExtractView(bytes.NewReader(multiStyle)); err != nil {
		t.Fatalf("released multi-style/warning/null-metadata extract: %v", err)
	}
	repeated, err := DecodeConfluenceTableExtractView(bytes.NewReader(validConfluenceTableRepeatedExtractWire(t)))
	if err != nil || !repeated.Tables[0].Rows[0].Cells[1].Repeated {
		t.Fatalf("repeated selected table=%+v err=%v", repeated, err)
	}

	failure, err := DecodeConfluenceTableSelectionFailureView(bytes.NewReader(validConfluenceTableSelectionFailureWire(t)))
	if err != nil {
		t.Fatal(err)
	}
	if failure.Kind != "not_found" || failure.Requested != 3 || failure.Available != 2 {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestDecodeConfluenceTableWiresRejectWireDrift(t *testing.T) {
	validSummary := validConfluenceTableSummaryWire(t)
	validExtract := validConfluenceTableExtractWire(t, true)
	validFailure := validConfluenceTableSelectionFailureWire(t)

	tests := []struct {
		name   string
		data   []byte
		decode func([]byte) error
	}{
		{
			name: "summary unknown root member",
			data: mutateConfluenceTableWire(t, validSummary, func(root map[string]any) { root["private"] = true }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "summary missing nested member",
			data: mutateConfluenceTableWire(t, validSummary, func(root map[string]any) {
				delete(root["tables"].([]any)[0].(map[string]any), "warning_count")
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "summary null optional member",
			data: mutateConfluenceTableWire(t, validSummary, func(root map[string]any) { root["selected_table"] = nil }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "summary explicit zero optional member",
			data: mutateConfluenceTableWire(t, validSummary, func(root map[string]any) { root["selected_table"] = 0 }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract unknown cell member",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
				cell["unreviewed"] = "value"
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract null optional title",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) { root["title"] = nil }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract empty optional title",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) { root["title"] = "" }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract explicit false optional marker",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
				cell["header"] = false
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract explicit zero optional span",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
				cell["rowspan"] = 0
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract null style value",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
				cell["styles"] = map[string]any{"color": nil}
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract null raw value",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
				cell["raw"] = map[string]any{"data-source": nil}
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract declared enormous width",
			data: mutateConfluenceTableWire(t, validExtract, func(root map[string]any) {
				table := root["tables"].([]any)[0].(map[string]any)
				delete(table, "headers")
				table["column_count"] = int(^uint(0) >> 1)
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "selection failure unknown recovery member",
			data: mutateConfluenceTableWire(t, validFailure, func(root map[string]any) {
				root["recovery"].(map[string]any)["private"] = true
			}),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSelectionFailureView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "selection failure required null",
			data: mutateConfluenceTableWire(t, validFailure, func(root map[string]any) { root["recovery"] = nil }),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceTableSelectionFailureView(bytes.NewReader(data))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode(test.data); err == nil {
				t.Fatal("wire drift was accepted")
			}
		})
	}

	duplicate := bytes.Replace(validExtract, []byte(`"text":"Synthetic"`), []byte(`"text":"Synthetic","text":"Synthetic"`), 1)
	if bytes.Equal(duplicate, validExtract) {
		t.Fatal("extract duplicate-key mutation did not apply")
	}
	if _, err := DecodeConfluenceTableExtractView(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("nested duplicate key was accepted")
	}
	if _, err := DecodeConfluenceTableSummaryView(bytes.NewReader(append(bytes.Clone(validSummary), []byte("\n{}")...))); err == nil {
		t.Fatal("trailing summary value was accepted")
	}
	for _, test := range []struct {
		name string
		data []byte
		call func([]byte) error
	}{
		{
			name: "summary", data: append(bytes.Clone(validSummary), bytes.Repeat([]byte(" "), confluenceTableSummaryWireMaxBytes-len(validSummary)+1)...),
			call: func(data []byte) error {
				_, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "extract", data: append(bytes.Clone(validExtract), bytes.Repeat([]byte(" "), confluenceTableExtractWireMaxBytes-len(validExtract)+1)...),
			call: func(data []byte) error {
				_, err := DecodeConfluenceTableExtractView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "failure", data: append(bytes.Clone(validFailure), bytes.Repeat([]byte(" "), confluenceTableFailureWireMaxBytes-len(validFailure)+1)...),
			call: func(data []byte) error {
				_, err := DecodeConfluenceTableSelectionFailureView(bytes.NewReader(data))
				return err
			},
		},
	} {
		t.Run("oversized "+test.name, func(t *testing.T) {
			if err := test.call(test.data); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversized wire error=%v", err)
			}
		})
	}
}

func TestDecodeConfluenceTableWiresRejectUnreconciledConsumerViews(t *testing.T) {
	validSummary := validConfluenceTableSummaryWire(t)
	validExtract := validConfluenceTableExtractWire(t, true)
	for name, mutate := range map[string]func(map[string]any){
		"summary returned count": func(root map[string]any) { root["returned_table_count"] = 2 },
		"summary cell arithmetic": func(root map[string]any) {
			root["tables"].([]any)[0].(map[string]any)["expanded_cell_count"] = 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceTableWire(t, validSummary, mutate)
			if _, err := DecodeConfluenceTableSummaryView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled summary was accepted")
			}
		})
	}
	for name, mutate := range map[string]func(map[string]any){
		"attached summary drift": func(root map[string]any) {
			root["tables"].([]any)[0].(map[string]any)["summary"].(map[string]any)["nonempty_text_cell_count"] = 0
		},
		"header projection drift": func(root map[string]any) {
			root["tables"].([]any)[0].(map[string]any)["headers"].([]any)[0] = "Different"
		},
		"origin carries source coordinates": func(root map[string]any) {
			cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
			cell["source_row"] = 1
			cell["source_column"] = 1
		},
		"synthetic carries text": func(root map[string]any) {
			cell := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
			delete(cell, "header")
			cell["synthetic"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceTableWire(t, validExtract, mutate)
			if _, err := DecodeConfluenceTableExtractView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled selected table was accepted")
			}
		})
	}
	malformedRepeated := mutateConfluenceTableWire(t, validConfluenceTableRepeatedExtractWire(t), func(root map[string]any) {
		origin := root["tables"].([]any)[0].(map[string]any)["rows"].([]any)[0].(map[string]any)["cells"].([]any)[0].(map[string]any)
		origin["colspan"] = 0
	})
	if _, err := DecodeConfluenceTableExtractView(bytes.NewReader(malformedRepeated)); err == nil {
		t.Fatal("repeated cell with an unclaimed source span was accepted")
	}
}

func validConfluenceTableSummaryWire(t *testing.T) []byte {
	t.Helper()
	return marshalConfluenceTableWire(t, map[string]any{
		"schema_version": 3, "cell_contract": ConfluenceTableWireCellContract,
		"page_id": "7001", "version": 2, "page_version_gated": false,
		"table_count": 1, "returned_table_count": 1, "selection_reconciled": true,
		"tables": []any{validConfluenceTableSummaryRecord()},
	})
}

func validConfluenceTableExtractWire(t *testing.T, withTitle bool) []byte {
	t.Helper()
	root := map[string]any{
		"schema_version": 3, "cell_contract": ConfluenceTableWireCellContract,
		"page_id": "7001", "version": 2, "page_version_gated": false,
		"table_count": 1, "selected_table": 1, "returned_table_count": 1, "selection_reconciled": true,
		"tables": []any{map[string]any{
			"index": 1, "row_count": 1, "column_count": 1,
			"summary": validConfluenceTableSummaryRecord(),
			"headers": []any{"Synthetic"},
			"rows": []any{map[string]any{
				"index": 1, "header": true,
				"cells": []any{map[string]any{"row": 1, "column": 1, "text": "Synthetic", "header": true}},
			}},
		}},
	}
	if withTitle {
		root["title"] = "Synthetic"
	}
	return marshalConfluenceTableWire(t, root)
}

func validConfluenceTableSummaryRecord() map[string]any {
	return map[string]any{
		"index": 1, "row_count": 1, "column_count": 1, "rectangular": true,
		"header_row_count": 1, "header_cell_count": 1,
		"expanded_cell_count": 1, "origin_cell_count": 1, "repeated_cell_count": 0,
		"synthetic_empty_cell_count": 0, "cell_count_reconciled": true,
		"nonempty_text_cell_count": 1, "nonempty_markdown_cell_count": 0, "nonempty_raw_cell_count": 0,
		"styled_cell_count": 0, "style_entry_count": 0, "distinct_style_marker_count": 0,
		"linked_cell_count": 0, "rowspan_metadata_cell_count": 0, "rowspan_source_cell_count": 0,
		"rowspan_covered_cell_count": 0, "colspan_metadata_cell_count": 0, "colspan_source_cell_count": 0,
		"colspan_covered_cell_count": 0, "warning_count": 0,
	}
}

func validConfluenceTableRepeatedExtractWire(t *testing.T) []byte {
	t.Helper()
	summary := map[string]any{
		"index": 1, "row_count": 1, "column_count": 2, "rectangular": true,
		"header_row_count": 1, "header_cell_count": 2,
		"expanded_cell_count": 2, "origin_cell_count": 1, "repeated_cell_count": 1,
		"synthetic_empty_cell_count": 0, "cell_count_reconciled": true,
		"nonempty_text_cell_count": 2, "nonempty_markdown_cell_count": 0, "nonempty_raw_cell_count": 0,
		"styled_cell_count": 0, "style_entry_count": 0, "distinct_style_marker_count": 0,
		"linked_cell_count": 0, "rowspan_metadata_cell_count": 0, "rowspan_source_cell_count": 0,
		"rowspan_covered_cell_count": 0, "colspan_metadata_cell_count": 2, "colspan_source_cell_count": 1,
		"colspan_covered_cell_count": 1, "warning_count": 0,
	}
	return marshalConfluenceTableWire(t, map[string]any{
		"schema_version": 3, "cell_contract": ConfluenceTableWireCellContract,
		"page_id": "7001", "version": 2, "page_version_gated": false,
		"table_count": 1, "selected_table": 1, "returned_table_count": 1, "selection_reconciled": true,
		"tables": []any{map[string]any{
			"index": 1, "row_count": 1, "column_count": 2, "summary": summary,
			"rows": []any{map[string]any{
				"index": 1, "header": true,
				"cells": []any{
					map[string]any{"row": 1, "column": 1, "text": "Wide", "header": true, "colspan": 2},
					map[string]any{"row": 1, "column": 2, "text": "Wide", "header": true, "colspan": 2, "repeated": true, "source_row": 1, "source_column": 1},
				},
			}},
		}},
	})
}

func validConfluenceTableEmptyExtractWire(t *testing.T) []byte {
	t.Helper()
	summary := map[string]any{
		"index": 1, "row_count": 0, "column_count": 0, "rectangular": true,
		"header_row_count": 0, "header_cell_count": 0,
		"expanded_cell_count": 0, "origin_cell_count": 0, "repeated_cell_count": 0,
		"synthetic_empty_cell_count": 0, "cell_count_reconciled": false,
		"nonempty_text_cell_count": 0, "nonempty_markdown_cell_count": 0, "nonempty_raw_cell_count": 0,
		"styled_cell_count": 0, "style_entry_count": 0, "distinct_style_marker_count": 0,
		"linked_cell_count": 0, "rowspan_metadata_cell_count": 0, "rowspan_source_cell_count": 0,
		"rowspan_covered_cell_count": 0, "colspan_metadata_cell_count": 0, "colspan_source_cell_count": 0,
		"colspan_covered_cell_count": 0, "warning_count": 0,
	}
	return marshalConfluenceTableWire(t, map[string]any{
		"schema_version": 3, "cell_contract": ConfluenceTableWireCellContract,
		"page_id": "7001", "version": 2, "page_version_gated": false,
		"table_count": 1, "selected_table": 1, "returned_table_count": 1, "selection_reconciled": true,
		"tables": []any{map[string]any{
			"index": 1, "row_count": 0, "column_count": 0, "summary": summary, "rows": nil,
		}},
	})
}

func validConfluenceTableSelectionFailureWire(t *testing.T) []byte {
	t.Helper()
	return marshalConfluenceTableWire(t, map[string]any{
		"kind": "not_found", "remediation": "summarize_then_select_table",
		"message": "selected Confluence table index 3 is out of range; available table count is 2",
		"recovery": map[string]any{
			"schema_version": 1, "action": "reread_then_reselect", "retry_safe": false,
			"next_capability": "confluence.table.summary", "requested": 3, "available": 2,
		},
	})
}

func mutateConfluenceTableWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return marshalConfluenceTableWire(t, root)
}

func marshalConfluenceTableWire(t *testing.T, root map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
