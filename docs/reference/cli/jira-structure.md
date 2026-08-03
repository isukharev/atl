# Jira Structure

Structure metadata, forests, rows, folders, values, issue pulls, and exports.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl jira structure {get,view,forest,rows,folders,values,pull-issues,export}`

Read-only Tempo Structure access via the Structure REST API
(`/rest/structure/2.0/`). Structures are addressed by numeric id. If the
Structure plugin is not installed, the endpoint is disabled, or the object is not
visible to the token, Jira returns an API error (commonly exit 4 or 6).

```bash
atl jira structure get 123
atl jira structure view 123                         # normalized JSON; -o text -> Markdown table
atl jira structure view 123 --fields key,summary,status,assignee
atl jira structure view 123 --view full
atl jira structure forest 123
atl jira structure rows 123                         # parsed forest rows; -o id -> row ids
atl jira structure rows 123 --root "release train"  # first matching subtree
atl jira structure folders 123                     # stable folder ids, paths, subtree statistics
atl jira structure view 123 --folder-id 100        # exact stable folder selector
atl jira structure view 123 --folder-path 'Plans / Quarter' -o text
atl jira structure view 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7 # bind a selector to one forest version
atl jira structure rows 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure pull-issues 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7 --out issues.json
atl jira structure export 123 --root "release train" --fields key,summary,status --format jsonl --out structure.jsonl
atl jira structure export 123 --folder-id 100 --format md --out structure.md \
  --expected-forest-signature 55 --expected-forest-version 7 # fails before the file is created if stale
atl jira structure export 123 --fields summary --format csv --out raw.csv --raw-csv # unsafe in spreadsheets
```

`view` is the recommended agent read path. It joins the hierarchy's stable item
identities with compact Jira issue fields; stored folders receive best-effort
labels, while calculated grouping/generator rows keep honest technical labels.
The nested Structure identity always includes `read_only:true|false`; `false`
is explicit rather than omitted, so agents can distinguish known mutability
from missing metadata.
Folder discovery likewise always emits string `name` and `parent_folder_id`
fields. An unavailable label remains `name:""` while `path` uses
`folder:<id>` as a stable technical fallback; root folders use
`parent_folder_id:""`.
JSON is the default, `-o text` is a Markdown table, and `-o id` emits row ids.
The default projection is `key,summary,status,assignee`;
`--fields` accepts Jira field ids and replaces that list. `--view NAME` selects
the preset's `structure` fields; explicit `--fields` wins. Structure presets
accept Jira field ids only because hierarchy columns remain fixed and honest.
The JSON projection records `source:"list-view"` for named/default presets and
`source:"explicit"` only when `--fields` supplied the projection.

Tempo's browser saved views and per-user column adjustments are a separate UI
configuration surface and are not reproduced by the documented integration
API. Every snapshot therefore includes an explicit `projection` object with
`browser_view_reproduced:false`. Ask which planning columns matter and pass
them through `--fields` instead of assuming the browser's currently selected
view.

Generated Structure rows may receive new ephemeral `row_id` values on a later
expansion even when the plan is unchanged. Atl therefore resolves issue data by
stable issue `item_id` only when `item_type` is `issue`, never by calculated row
id. Jira's advisory strict-query validation is disabled only for these generated
Structure identity joins so one deleted or permission-hidden id does not reject
the whole batch; user-authored JQL remains strict and inaccessible rows remain
explicit. Use `values.key`, `item_id`,
and the ordered hierarchy for durable analysis; use `row_id` only within one
snapshot.

`folders` is the fast discovery path: it reads metadata, one forest, and one
batched Structure Value projection for stored-folder labels, but never searches
Jira issues. JSON reports stable `folder_id`, snapshot-local `row_id`, exact
folder path, parent folder, depth, and subtree statistics for descendant rows,
issue occurrences, unique issues, descendant folders, and maximum relative
depth. `-o id` prints stable folder item ids; `-o text` is a compact Markdown
table. Label failures retain ids/statistics with `complete:false` and bounded
warnings.

`rows`, `view`, `pull-issues`, and `export` accept exactly one selector:
`--folder-id` (preferred stable id), `--folder-row` (one current-forest
occurrence), `--folder-path` (exact normalized slash-separated path), or legacy
fuzzy `--root`. Exact selectors verify a stored folder, never fall back to the
first substring match, and fail closed on absence/ambiguity. JSON preserves
absolute `depth`/`parent_row_id`, adds `relative_depth`, and returns a
`selection` object. Selected Markdown starts at depth zero. Path matching is
case-insensitive and collapses whitespace per segment; use folder id/row when a
folder name contains `/`. Completeness is scoped to emitted rows, so missing
labels in an unrelated branch do not mark a selected subtree partial.

`view`, `rows`, `pull-issues`, and `export` additionally accept the same paired
optional flags `--expected-forest-signature` and `--expected-forest-version`,
which bind one read to exactly one forest version. Omitting both is an
explicitly ungated read. If either is supplied both are required, the signature
must be non-zero, and the version must be positive; an unpaired, zero, or
non-positive pair is a usage error (exit `2`) decided before any backend
request. When a `--folder-id`, `--folder-row`, or `--folder-path` selector came
from an earlier `view`, `folders`, or `rows` result, copy both members of that
result's forest version into the later call. A selector fixed outside any
earlier read may omit both and must then be read as ungated evidence. A stale
pair fails with exit `8` after the initial forest read and before any
stored-folder label lookup, Structure Value request, Jira issue expansion,
export rendering, or local output file, so a caller that already lost the race
does no projection work and leaves no partial artifact; the diagnostic carries
only the expected and current signature/version integers. `get`, `forest`,
`folders`, and `values` accept no expected-version flags.

The gate is a single comparison against the forest the read is built from; no
additional forest request is made, and there is no final re-read after
projection. Every successful `view` snapshot therefore reports the
`forest_version` it was assembled from plus `forest_version_gated` — true only
when the caller supplied the exact pair. `rows` and `pull-issues` keep their
existing `version` member and add the same always-present
`forest_version_gated`; the `export` command result adds always-present
`forest_version` and `forest_version_gated`. A returned pair with either member
zero is non-bindable: do not pass it back through the expected-version flags,
and keep the later selection explicitly ungated. The forest version qualifies
the hierarchy and the selection only: Jira issue fields and stored-folder labels
are read at separate times and are not covered by it, so a gated result is not
one atomic versioned value snapshot.

`rows` parses Structure's forest formula into a stable row list. `--root`
matches the first row by row id, item id/type/semantic, or by selected Structure
attribute values (`--root-fields`, default `key,summary`) and emits only that
row plus its descendants:

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

`values` posts selected row ids and attribute ids to the Structure value
resource. The output preserves the raw response under `raw`, exposes
`responses`, and lifts any reported inaccessible row ids to
`inaccessible_rows` so scripts can detect permission gaps.

`pull-issues` collects numeric Jira issue ids from Structure issue rows and reads
the matching Jira issues via generated `id in (...)` JQL batches. Its default
field set comes from `jira.list_views.default.structure`; use `--view` for a
named projection or explicit `--fields` to override it. It emits:

Its aggregate `--limit` is non-negative: `0` means no configured issue cap,
positive values cap collected issues, and negatives fail before hierarchy
reads or output-file creation.

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": false,
  "issue_ids": ["10001"],
  "issues": [
    {
      "key": "PROJ-1",
      "id": "10001",
      "fields": {
        "summary": "Example"
      }
    }
  ],
  "count": 1
}
```

Full Structure Markdown uses separate emitted `#`, numeric `Depth`, technical
`Type` and `Item`, Jira value columns, and `Access`; it no longer combines
indentation, key, and summary into a duplicated `Tree` cell.

`export` writes the same normalized projection as `view`. Supported formats are
`json`, `jsonl`, `csv`, and `md`; `--out` is required. JSONL emits one
self-contained hierarchy row for line-oriented tools, CSV includes row metadata
plus requested issue fields, and Markdown renders a compact hierarchy table.
The command result reports the exported artifact plus the forest it was built
from:

```json
{
  "path": "structure.jsonl",
  "format": "jsonl",
  "structure_id": 123,
  "forest_version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": true,
  "row_count": 1,
  "issue_count": 1
}
```

The JSON and Markdown artifacts carry the same normalized snapshot as `view`,
JSONL repeats `forest_version` and `forest_version_gated` on every record, and
the Markdown header note states the signature, version, and gate status. CSV
headers and cells are unchanged, so a CSV export carries that provenance only in
this command result — record it alongside the file if the CSV must stay
auditable. With `-o text`, the result line includes `forest_signature`,
`forest_version`, and `gated` alongside the format and counts.
The reported unique-issue count follows the emitted root/subtree. CSV formula-leading
cells are apostrophe-prefixed by default. `--raw-csv`
preserves them verbatim only with `--format csv` and produces an artifact that is
unsafe to open in a spreadsheet. These commands are read-only and do not write
Structure data back to Jira.
