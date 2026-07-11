<!-- Generated from skills-src/confluence/reference/tables-attachments.md — edit the source and run 'make gen-plugins'. -->
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
preserves ordinary links, and marks colors as
`⟦color:...⟧text⟦/color⟧`. Inspect native CSF before any structural edit.

## Table extraction

```bash
atl conf table extract --id <page-id> [--table N] --format json|csv|xlsx [--out <file>]
```

`--table` is 1-based; zero selects all tables. XLSX requires `--out`. CSV
neutralizes cells starting with spreadsheet formula characters. Keep the safe
default for files humans may open; `--raw-csv` is only for a trusted
non-spreadsheet consumer.

## Attachments

```bash
atl conf attachment list --id <page-id>
atl conf attachment get --id <page-id> --name <filename> [--version N] --into <dir>
atl conf attachment upload --id <page-id> --file <path> [--comment <text>]
atl conf attachment delete --id <attachment-id> --force
```

Attachment deletion is permanent and the explicit `--force` confirms it.
Downloads and uploads stream bytes. Treat upload as non-idempotent. Before the
first upload, list attachments and retain a private baseline of matching
filename, id, version, size, and comment. After an ambiguous response, list
again and compare against that baseline; only a new id/version with the expected
attributes can support a committed outcome. If either listing errors, appears
incomplete/capped, or cannot distinguish prior state, report `unknown` and do
not retry. Never blindly replay.

Use `conf pull --assets` when diagrams or images are needed for understanding a
page; exact-revision renders land in the page's `.assets/` directory. Attachment
and image markers inside Markdown are identity-bearing and readonly.
