# Confluence tables

Bounded table inventory and extraction in JSON, CSV, and XLSX forms.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl conf table extract`

Extract tables from a page's native CSF body into structured data. This is useful
when the page has multiple tables or merged cells and a script needs something
more explicit than the Markdown staging view.

```bash
# all tables as JSON, preserving per-cell metadata
atl conf table extract --id 12345678

# one table as rectangular CSV
atl conf table extract --id 12345678 --table 2 --format csv
atl conf table extract --id 12345678 --table 2 --expected-version 7 --format csv
atl conf table extract --id 12345678 --table 2 --format csv --raw-csv # unsafe spreadsheet interoperability

# all tables as an XLSX workbook, one sheet per table
atl conf table extract --id 12345678 --format xlsx --out tables.xlsx
```

Flags:

| flag | description |
|---|---|
| `--id` | page id |
| `--table` | 1-based table index to extract (0 = all tables) |
| `--expected-version` | optional positive page version already observed for this selection |
| `--format` | `json`, `csv`, or `xlsx` |
| `--out` | optional output file; required for `xlsx` |
| `--raw-csv` | preserve formula-leading cells verbatim; CSV only and unsafe in spreadsheets |

JSON preserves the expanded cells, durable compact provenance, ordinary links,
and visible inline color markers. In schema v3 a native cell is the unmarked
default with no source coordinates, a span repeat has `repeated:true` and names
its covering origin, and rectangular padding has `synthetic:true` with no
source coordinates. Every JSON table
also includes a required `summary` record with the same reconciled structural
metrics as `atl conf table summary`, so scripts can consume exact counts without
recounting cells. Top-level `returned_table_count` is the actual length of
`tables`, while `selection_reconciled` confirms that a selected index returned
exactly that table or that an unselected read returned the full page-wide
`table_count`. CSV without `--table` emits a cell-level stream so pages with
different table shapes can share one file; CSV with `--table` emits a
rectangular table.
JSON results also include `schema_version:3`, the exact
`cell_contract:"confluence-table-cells/compact-v3"` marker, the positive page
`version`, and `page_version_gated`. When a table index came from `conf table summary`, pass
that summary's exact version with `--expected-version`; omission is explicit
ungated evidence for a directly fixed index. A stale positive version exits
`8` before emitting table evidence; a negative value is a usage error and
exits `2`.
CSV prefixes cells beginning with `=`, `+`, `-`, `@`, tab, CR, or LF with an
apostrophe by default so opening untrusted page data in a spreadsheet does not
execute it as a formula. `--raw-csv` is an explicit unsafe escape hatch.

With `--out`, all three formats persist through one atomic writer (temp file
then rename), so a partial or truncated artifact never lands; missing parent
directories are created as needed. A persistence failure exits `8` and writes
nothing to stdout, leaving any prior file in place; a missing `--out` for
`xlsx` is still a usage error (exit `2`). The success acknowledgement JSON/text
is unchanged, and JSON and CSV without `--out` still stream to stdout.

## `atl conf table summary`

Inventory table structure without emitting page titles, cell text, URLs, style
values, raw attributes, or warning text. Use this bounded read before choosing
a table to extract, or when only shapes and structural features are needed.

```bash
atl conf table summary --id 12345678
atl conf table summary --id 12345678 --table 2
atl conf table summary --id 12345678 --expected-version 7
```

`--table` is 1-based; zero summarizes every table while preserving the page's
total `table_count`. `returned_table_count` and `selection_reconciled` qualify
that selection. Counts use the expanded rectangular representation and expose
native origins, span repeats, synthetic padding, direct rowspan/colspan
metadata, non-empty text/Markdown/raw cells, style entries, and distinct style
key/value markers without revealing their values. `rectangular` reports grid
shape. `cell_count_reconciled` additionally requires an independent
source-placement ledger: declared span rectangles may not overlap or leave the
source row domain, and every claim must agree with the expanded grid. Rowspan and
colspan source cells remain separate from coordinate-covered positions, avoiding
an ambiguous combined span count. Native `rowspan` and `colspan` values above
100 are refused with exit `8` before the rectangular grid is expanded; the
command never reports a silently clamped shape as reconciled.
The result reports `schema_version:3`, the exact
`cell_contract:"confluence-table-cells/compact-v3"` marker, the positive page
`version`, and `page_version_gated`. `--expected-version` optionally binds this read to an
already-observed revision without another request. A stale version fails before
any table evidence is returned with exit `8`; a negative value is a usage error
and exits `2`.
