# Confluence tables and attachments

## Table decision

Use the Markdown/apply path for cell text and whole-row additions/deletions in
ordinary or styled tables. Apply merges row/cell-wise and preserves untouched
row/cell bytes and styling.

Require `conf apply <page.md> --dry-run` before every table merge. Stop on:

- a rowspan/colspan boundary-crossing edit;
- column add/remove;
- nested table or copied macro cell;
- an ambiguous/unconvertible wrapper.

For those cases, either finish/push supported Markdown edits and fresh-pull
before a separate CSF table cycle, or perform the whole change as one direct-CSF
cycle. Do not mix surfaces before a push.

Rendered Markdown pads `colspan`, repeats `rowspan` values across covered rows,
preserves ordinary links, and keeps colors in protected
`<span style="color: ...">` or `<span data-atl-color="...">` wrappers. Inspect
native CSF before any structural edit.

## Table extraction

```bash
atl conf table summary --id <page-id-or-same-origin-url> [--table N]
atl conf table extract --id <page-id-or-same-origin-url> [--table N] --format json|csv|xlsx [--out <file>]
```

Start with `table summary` when deciding which table to read or when only
structural counts are required. Its bounded JSON contains shapes and separate
origin/repeated/padding counts, direct rowspan/colspan metadata plus source and
covered-cell counts, non-empty text/Markdown/raw counts, style-entry and
distinct-marker counts, and selection/cell reconciliation. Use these fields
instead of recounting raw extraction cells. It exposes no page title, cell text,
URLs, style keys/values, raw attributes, or warning text.

Table schema v3 requires
`cell_contract:"confluence-table-cells/compact-v3"`. A native origin is the
unmarked default with no source coordinates, a repeated cell has
`repeated:true` and names its covering origin, and synthetic padding has
`synthetic:true` with neither coordinate. `cell_count_reconciled:true` means an
independent source-placement/span ledger also agrees with that durable grid;
false is a hard evidence failure, not a count to repair client-side.

`--table` is 1-based; zero selects all tables. XLSX requires `--out`. CSV
neutralizes cells starting with spreadsheet formula characters. Keep the safe
default for files humans may open; `--raw-csv` is only for a trusted
non-spreadsheet consumer.

Every `--out` format (JSON, CSV, XLSX) is written atomically through one
temp-file-then-rename path, so a partial file never lands; missing parent
directories are created as needed. A persistence failure exits `8` and writes
nothing to stdout; a missing XLSX `--out` is still a usage error (exit `2`). On
success the acknowledgement JSON/text shape is unchanged; without `--out`, JSON
and CSV still stream to stdout.

## Attachments

```bash
atl conf attachment list --id <page-id>
atl conf attachment list --id <page-id> --expected-version <N>
atl conf attachment get --id <page-id> --name <filename> [--version N] --into <dir>
atl conf attachment upload --id <page-id> --file <path> [--comment <text>]
atl conf attachment delete --id <attachment-id> --force
```

`list` returns the qualified inventory `{schema_version, page_id, page_version,
count, complete, partial_reason?, attachments:[...]}`. Successful JSON listings
always return an `attachments` array; `null` is not a successful current output
shape. Treat `[]` as a complete empty result only when `complete:true`. A
`complete:false` inventory names its limiter with a static `partial_reason`
(`page_limit`, `item_limit`, `pagination_stalled`, `legacy_unqualified`) and is
a prefix, never evidence that an attachment is absent. `-o id` and `-o text`
output are unchanged.

`--expected-version N` binds the listing to a page version you already
observed: a positive value refuses the read with exit `8` when the page has
moved, before any attachment request, and reports only the expected and current
version integers. Use it whenever the inventory must correspond to a specific
page read.

Attachment deletion is permanent and the explicit `--force` confirms it.
Downloads and uploads stream bytes. Treat upload as non-idempotent. Before the
first upload, list attachments and retain a private baseline of matching
filename, id, version, size, and comment. After an ambiguous response, list
again and compare against that baseline; only a new id/version with the expected
attributes can support a committed outcome. If either listing errors, reports
`complete:false`, or cannot distinguish prior state, report `unknown` and do
not retry. Never blindly replay. Caller size faults are exit `2`; malformed or
empty successful backend upload responses are exit `8` and must not be treated
as proof that the upload was not committed.

Use `conf pull --assets` when diagrams or images are needed for understanding a
page; exact-revision renders land in the page's `.assets/` directory. Attachment
and image markers inside Markdown are identity-bearing and readonly.
