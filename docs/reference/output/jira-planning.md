# Jira planning and Structure output contracts

Board, sprint, Structure, and related planning result shapes.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [Boards and Structure](#boards-and-structure)
<!-- reference-navigation:end -->

## Boards and Structure

`atl jira board config <ID>` returns the workflow projection used to interpret
board issues:

```json
{
  "id": 5,
  "name": "Quarter plan",
  "type": "kanban",
  "filter_id": "42",
  "kanban_subquery": "fixVersion is EMPTY",
  "constraint_type": "issueCount",
  "columns": [
    {"name": "To Do", "status_ids": ["11", "12"], "max": 7},
    {"name": "Done", "status_ids": ["13"]}
  ],
  "rank_field_id": "10019"
}
```

`board issues` and `board backlog` return one explicit common IssueList page.
The backend request may include `status` when board column context needs its id,
without adding an unrequested value to `projection.fields`. The backlog issue
endpoint is Scrum-only; `board backlog` refuses a Kanban board after reading its
configuration and before calling the incompatible endpoint.

`atl jira board view <ID>` returns a normalized multi-page snapshot:

```json
{
  "schema_version": 1,
  "board": {"id": 5, "name": "Quarter plan", "type": "kanban", "columns": []},
  "scope": "all",
  "projection": {
    "kind": "jira-fields-v1",
    "columns": ["position", "key", "summary", "status", "board.column", "assignee"],
    "fields": ["summary", "status", "assignee"],
    "ordering": "backend-rank"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "board_position": 0,
    "in_board": true,
    "in_backlog": false,
    "status_id": "11",
    "status": "Open",
    "column": "To Do",
    "column_index": 0,
    "column_mapped": true,
    "values": {"summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "complete": true,
  "truncated": false,
  "backlog_fetched": false
}
```

When `board view` receives `--epic-field <exact-field>` and at least one
`--done-status`, it adds an optional deterministic aggregate:

```json
{
  "epic_rollup": {
    "epic_field": "customfield_10001",
    "done_statuses": ["Done"],
    "complete": true,
    "epics": [{
      "key": "PROJ-10",
      "parent_present": true,
      "child_count": 2,
      "done_child_count": 1,
      "status_counts": [
        {"status": "Done", "count": 1},
        {"status": "In Progress", "count": 1}
      ],
      "latest_child_updated": "2026-06-20T10:00:00.000+0000",
      "timestamped_children": 2,
      "missing_updated_children": 0,
      "timestamp_coverage_complete": true
    }]
  }
}
```

The exact epic field and `updated` must both occur in the selected projection.
Done statuses are matched case-insensitively, case-insensitive duplicates are
rejected, and accepted values are emitted in deterministic order. Epic keys and
status records are sorted lexically. The aggregate uses only rows in this
snapshot and does not fetch children separately. Its `complete` is false when
the snapshot is incomplete, a referenced parent is absent, or a child lacks
`updated`. A backend relation must be an exact non-empty string or an object
with an exact non-empty string `key`; arrays, scalar non-strings, and objects
without that key fail check validation. Malformed timestamps fail the same
way. With no rollup options the field is omitted, preserving the existing JSON
shape.

Rows from board scope retain backend rank order. For Scrum `scope:all`, backlog
membership and backlog position are joined by issue key; backlog-only issues
are appended in backlog order. For Kanban, `scope:all` reads board scope only,
sets `backlog_fetched:false`, and never calls backlog or sprint endpoints.
Unknown status ids use `column:"Unmapped"`, `column_index:-1`, and
`column_mapped:false` rather than disappearing.

`--limit 0` follows pagination to exhaustion. A positive limit applies per
requested scope; when more rows exist the output sets `complete:false` and
`truncated:true`. Negative aggregate limits are usage exit 2 before any request
or output-file creation. Repeated issues across pages, a non-advancing cursor, or the
pagination safety cap return check-failed (exit 8). There is no board snapshot
version in Jira's API, so `complete` means all reported pages were consumed,
not that concurrent board changes were transactionally excluded.

`board export --format json|jsonl|csv|md` writes the existing row projection
and does not accept or emit the optional view-only epic rollup. JSONL
repeats compact board identity, projection, row count, and completeness with each row. CSV contains rank,
scope membership, status/column mapping, and selected fields; formula-leading
cells are neutralized unless `--raw-csv` is explicitly approved. Markdown is a
compact review table rendered by the same primitive as other issue lists. None
of these read paths call rank, sprint, move, or issue
write endpoints.

`atl jira structure folders <ID>` is the fast stored-folder index. It fetches
metadata, one forest, and one batched folder-label value projection; it never
searches Jira issues:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Planning", "read_only": false},
  "forest_version": {"signature": 10, "version": 2},
  "folders": [{
    "folder_id": "100",
    "row_id": 500,
    "name": "Quarter",
    "path": ["Plans", "Quarter"],
    "depth": 1,
    "parent_folder_id": "99",
    "stats": {"descendant_rows": 86, "issue_rows": 72, "unique_issues": 70, "subfolders": 2, "max_relative_depth": 4}
  }],
  "complete": true,
  "warnings": []
}
```

`structure.read_only` is always present, including when it is `false`, so a
known mutable Structure is not confused with missing metadata. Folder `name`
and `parent_folder_id` are also always present strings: a missing label is
`name:""` while `path` uses the stable `folder:<id>` fallback, and a root folder
has `parent_folder_id:""`. Consumers must not substitute the fallback path into
the empty semantic name. `-o id` emits stable folder item ids, not row ids.
Missing/partial labels keep technical ids and statistics, set `complete:false`,
and add bounded warnings.

`atl jira structure rows <ID>` returns a parsed read-only view of a Tempo Structure forest:

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": false,
  "rows": [
    {
      "row_id": 100,
      "depth": 0,
      "item_type": "issue",
      "item_id": "10001",
      "position": 0
    }
  ]
}
```

For non-root rows, `parent_row_id` is present. `-o id` prints Structure row ids
one per line. `--root` emits the first matching row plus descendants; matching is
by row metadata first and then by Structure values fetched through
`--root-fields` (default `key,summary`).

`forest_version_gated` is always present and is `true` only when the caller
supplied the exact expected forest pair described under `structure view`; the
existing `version` member still reports the forest the rows were parsed from.

Rows/view/pull-issues/export also accept one mutually exclusive exact selector:
`--folder-id`, `--folder-row`, or `--folder-path`. Exact selectors verify a
stored folder in the same forest snapshot, never fall back to fuzzy matching,
and return not-found or check-failed on absence/ambiguity. Results include
`selection`; selected rows retain absolute `depth` and `parent_row_id` and add
`relative_depth` beginning at zero. `--folder-id` is the durable agent path;
`--folder-row` is snapshot-local and path selection requires complete labels.
Path comparison is case-insensitive and collapses whitespace in every segment;
folder names containing a literal `/` require id/row selection. `complete`
describes the emitted subtree: unrelated missing labels elsewhere in the forest
do not make an id/row/root-selected view partial.

`atl jira structure values <ID> --rows ... --fields ...` preserves the backend
value matrix under `responses` and `raw`; if the backend reports permission
gaps, normalized row ids are also exposed as `inaccessible_rows`. The field is
always present; when there are no reported gaps it is `[]`.

`atl jira structure view <ID>` returns a normalized snapshot:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Quarter plan", "read_only": true},
  "forest_version": {"signature": 55, "version": 7},
  "forest_version_gated": false,
  "projection": {
    "kind": "jira-fields-v1",
    "source": "list-view",
    "attributes": ["key", "summary", "status", "assignee"],
    "browser_view_reproduced": false
  },
  "rows": [{
    "row_id": 100,
    "depth": 0,
    "item_type": "issue",
    "item_id": "10001",
    "position": 0,
    "accessible": true,
    "values": {"key": "PROJ-1", "summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "issue_count": 1,
  "complete": true,
  "inaccessible_rows": [],
  "warnings": []
}
```

`projection.source` is `list-view` for the built-in default, `full`, and custom
named views; it is `explicit` when `--fields` wins. The selected preset name is
reported separately as `projection.view`.

Every successful snapshot keeps `schema_version:1` and always carries
`forest_version` — the forest the snapshot was assembled from — and
`forest_version_gated`. `forest_version_gated:true` means the caller supplied
that exact pair through the paired `--expected-forest-signature` and
`--expected-forest-version` flags, and the snapshot's `forest_version` then
equals it. `view`, `rows`, `pull-issues`, and `export` accept the same paired
flags; `get`, `forest`, `folders`, and `values` accept none. Omitting both flags
is an explicitly ungated read. If either is supplied both are required, the
signature must be non-zero, and the version must be positive; an unpaired,
zero, or non-positive pair is a usage error and exits `2` before any backend
request. A supplied pair that does not match exits `8`: the comparison runs on
the initial forest read, before stored-folder labels, Structure Value or Jira
issue expansion, export rendering, and before any `--out` file is created, so a
stale gate leaves no partial local artifact. The diagnostic carries only the
expected and current signature/version integers. There is no second forest
request and no final re-read. A returned pair with either member zero is
non-bindable: do not pass it as an expected pair, and treat a later selection as
explicitly ungated. Copy
both non-zero members of a returned `forest_version` (`version` on `rows` and
`pull-issues`) into a later call
whenever its `--folder-id`, `--folder-row`, or `--folder-path` selector came
from an earlier `view`, `folders`, or `rows` result; a selector fixed outside
any earlier read may omit them and is then explicitly ungated evidence. The
forest version qualifies the hierarchy and the selection only — Jira issue
fields and stored folder labels are separately timed and are not covered by it,
so a gated result is not one atomic versioned value snapshot. The `-o text`
header states the signature, version, and gated facts alongside the projection
and row count.

`-o text` renders emitted `#`, numeric Depth (relative when selected), technical
Type/Item, separate Jira value columns, and Access. It does not duplicate key
and summary in a combined Tree cell or dump raw Jira objects/transport URLs.
Known Jira objects use their human label/name; an unknown non-empty object is
shown as `[object]` so it cannot be mistaken for a missing value without leaking
transport internals.
`-o id` emits row ids. The default attributes are shown above; explicit
`--fields` selects Jira fields and changes both `projection.attributes` and row values. Browser saved
views are deliberately not claimed as the source because Structure's supported
integration API does not expose a stable saved-view column projection.

Issue values are joined only for rows whose type is `issue`, using the forest's
stable numeric issue `item_id` through Jira search, not by Structure row id.
Structure's generated identity join disables Jira's advisory strict-query
validation so one deleted or hidden id cannot reject an otherwise readable
batch; ordinary user-authored JQL remains strict, and Jira parsing and
permission filtering still apply. Issues unavailable to the current
token/read remain usable but visible as gaps: `complete` is false, affected rows have
`accessible:false`, and their ids are listed in `inaccessible_rows`. Stored
folder summaries are best effort; calculated grouping/generator rows retain
their technical identity instead of risking a misleading label.

`issue_count` describes unique issue identities in the final emitted
root/subtree scope rather than the unfiltered forest. Structure may regenerate
row ids for calculated rows without changing the
expanded plan. Treat `row_id` and `parent_row_id` as snapshot-local identities;
issue keys and item ids remain the durable correlation keys.

`atl jira structure pull-issues <ID>` returns:

```json
{
  "structure_id": 123,
  "version": {"signature": 55, "version": 7},
  "forest_version_gated": false,
  "rows": [],
  "issue_ids": ["10001"],
  "issues": [{"key": "PROJ-1", "id": "10001", "fields": {}}],
  "count": 1
}
```

`forest_version_gated` is always present. When it is `true`, the stale-pair
check already ran on the initial forest read, so the Jira issue search and any
`--out` snapshot file happen only after the hierarchy matched the expected
forest. The pair still says nothing about the separately timed Jira fields
collected afterwards.

`atl jira structure export <ID> --out FILE --format json|jsonl|csv|md` writes the
artifact and returns a small result object:

```json
{
  "path": "structure.json",
  "format": "json",
  "structure_id": 123,
  "forest_version": {"signature": 55, "version": 7},
  "forest_version_gated": true,
  "row_count": 1,
  "issue_count": 1
}
```

`forest_version` and `forest_version_gated` are always present in the command
result, so an export is auditable without reopening the artifact. JSON and
Markdown contain the same normalized snapshot as `structure view`; Markdown
states that signature, version, and gated value in its header note. JSONL has
one self-contained record per row, including schema, structure id,
`forest_version`, `forest_version_gated`, projection, and
row, which makes line-oriented filtering safe. CSV
contains row metadata (`row_id`, `depth`, `relative_depth`, `parent_row_id`, `position`,
`item_type`, `item_id`, `accessible`) plus selected Structure attributes. CSV headers and
cells are unchanged by the gate, so a CSV export carries this provenance only in
the command result above. CSV cells use the
same default formula neutralization as `jira export`; `--raw-csv` disables it
only for CSV and is unsafe for spreadsheet use. Use `pull-issues` separately
when raw Jira issue snapshots are required.

With `-o text`, the command-result line reports `format`, `forest_signature`,
`forest_version`, `gated`, `rows`, and `issues` after the output path, so CSV
provenance remains visible in either output mode.
