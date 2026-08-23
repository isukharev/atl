<!-- Generated from skills-src/jira/reference/extended-capabilities.md — edit the source and run 'make gen-plugins'. -->
# Jira attachments, planning, Agile, and Structure

Load only the section needed for the current task.

## Compact exports

```bash
atl jira export --jql '<JQL>' --format jsonl --out issues.jsonl
atl jira export --jql '<JQL>' --format csv --out issues.csv
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 \
  --fields status,customfield_10001 --format json --out selected.json
jq -c '
(["PROJ-1","PROJ-2"]) as $requested |
([.issues[].key]) as $returned |
{
  manifest:(.manifest|{query_mode,row_order,missing_identity_behavior,fields,limit,count}),
  selector_reconciliation:{requested:$requested,returned:$returned,missing:($requested-$returned)},
  issues:[.issues[]|{key,evidence:.fields.customfield_10001}]
}' selected.json
atl jira export diff old.jsonl new.jsonl
```

The manifest contains query/fields/count and a backend identity hash, so it is
credential-sanitized but may still be private. JSONL/CSV stream; aggregate JSON
is capped at 10,000 issues/64 MiB and row streams at 250,000 identities. CSV
neutralizes formulas unless the user explicitly approves `--raw-csv` for a
trusted non-spreadsheet consumer.

Prefer `--keys`/`--ids` when downstream position matters: all formats preserve
the de-duplicated first-occurrence selector order across Jira pages and generated
batches. Missing/inaccessible identities are omitted, so compare returned
`key`/`id` values with the retained request; a missing identity is not proof of
absence, and output position is not a placeholder. User-authored `--jql`
remains in Jira's returned order. File manifests record these policies as
`row_order` and `missing_identity_behavior`. Reordering buffers one batch up to
64 MiB; reduce `--batch-size` if atl refuses an unusually wide batch.

Use `--out -` for small transient analysis that is consumed directly: stdout
is only JSONL, a bare JSON array, or CSV, with no manifest/result envelope and
no created files. Choose it with `--format` and omit `-o text`, which is not an
artifact format. Display names in `--fields` resolve to ids before search.
Always honor the exit code and discard a streamed prefix after non-zero exit.
Do not pipe streamed stdout to `jq`: write through native `--out`, require the
small receipt/zero exit, then filter the artifact as above.

## Epic evidence digest

```bash
atl jira epic digest PROJ-1 --quarter 2026-Q2 \
  --status-field 'Delivery Notes' --dod-field 'Definition of Done' \
  --projection compact
```

Use this before assembling a quarterly result from separate calls. It composes
the common IssueList children contract, qualified history, bounded comments,
links/blockers, refs, and configured narrative fields. Check every source's
`complete` flag and the dated reasons under `staleness`; the CLI deliberately
does not write a subjective summary. Compact output names every omitted or
clipped path; use full only for one specifically required raw detail. Optional Confluence expansion requires an
exact heading and remains same-origin/bounded.

Quarter/date-only periods are calendar ranges in the observed Jira current-user
IANA timezone. Inspect the returned zone/source and canonical UTC instants;
digest resolves the zone once and reuses it for history. Explicit-offset
RFC3339 bounds are already absolute and add no timezone GET. Midnight gaps and
folds cover every real instant of the requested date; a fully skipped date is a
fail-closed boundary error. Never rewrite raw
JQL to imitate this high-level local filtering.

## Guarded bulk links and plans

Use `jira issue link suggest --csv ...` before bulk link work. Plan CSV requires
schema v2 and narrow operation/field/link allowlists. First run the dedicated
read-only `jira issue plan preview`, then repeat the exact file and allowlists
with execution-only `plan apply --confirm APPLY --expected-proposal-hash ...`.
No writer runs until the global qualification, policy, and hash barrier passes.
`--continue-on-error` applies only to conclusive failures after that barrier;
ambiguity always stops and must not be replayed.

## Attachments and images

```bash
atl jira issue attachment list PROJ-1
atl jira issue attachment get PROJ-1 --id spec.xlsx --into ./attachments
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
atl jira issue images PROJ-1 --into /tmp/proj1-images
```

Use `attachment get` for any file type, `attachment upload` to add a file, and
`images` when visual inspection needs only image attachments. A malformed or
empty successful backend upload response is exit `8`; because the mutation may
have committed, inspect the attachment list and do not retry blindly.
The current attachment list is useful for selecting an exact id, but it has no
explicit completeness member; do not treat an empty list as proof that no
attachment exists.

## Planning quality reports

```bash
atl jira planning report --jql '<JQL>' --estimate-field customfield_10001 \
  --epic-field customfield_10002 --require fixVersions,components --csv planning.csv
atl jira quality-report --jql '<JQL>'
```

Reports deterministic `score`, `level`, `gaps`, artifact `refs`, and optional
epic `children`. Use `issue refs` for links only and `issue tree` for normalized
epic/child structure only. A positive `--limit` is a caller-bounded sample and
the current result has no separate selection-completeness member; never use it
for a whole-scope absence claim. `--limit 0` removes the caller cap and asks the
legacy collector to exhaust pagination, but the result still cannot prove
backend completeness because it loses source qualification. Even with `--csv`,
stdout retains the full JSON report; the artifact is for reuse, not a
context-suppression mode.

## Boards and sprints

These commands require Jira Software (GreenHopper); Core-only instances return
404/exit 4. Discover numeric ids before acting:

```bash
atl jira board list --project PROJ
atl jira board get 5
atl jira board config 5
atl jira board view 5 -o text
atl jira board view 5 --jql 'statusCategory != Done' --limit 500
atl jira board view 5 --columns key,status,updated,customfield_10001 \
  --epic-field customfield_10001 --done-status Done
atl jira board export 5 --format jsonl --out board.jsonl
atl jira sprint list --board 5 --state active
atl jira sprint get 7
atl jira sprint current --board 5
atl jira sprint issues 7 --columns position,key,summary,status --limit 50
atl jira sprint add 7 PROJ-1 PROJ-2
atl jira sprint remove PROJ-1
```

Route by board type before asking for sprints:

- For Kanban, use `board config`, `board issues`, and `board view`. The Jira DC
  backlog issue endpoint is Scrum-only; `view --scope all` records
  `backlog_fetched:false` and does not call backlog or sprint endpoints. Use the
  configured ordered columns/status ids to understand workflow state.
- For Scrum, `view --scope all` additionally reads backlog membership, and only
  then use `sprint list/current/issues` when sprint context is relevant.

`board view` is the recommended compact agent path. It preserves backend rank
order, maps status ids to configured columns, and keeps unmapped statuses
explicit. When portfolio grouping is needed, select the exact epic relation
field plus `updated`, use `--epic-field` and repeatable `--done-status`, require
`epic_rollup.complete:true`, and consume its counts/latest child timestamp
without regrouping raw rows. Use `--jql 'statusCategory != Done'` or another user-approved
refinement when an old board has a very large history. `--limit 0` reads all;
positive limits are explicit truncation per scope and negatives fail before
requests/output. One-page board list/issues/backlog and sprint list/issues use
`1..50`; explicit zero is invalid there. For repeated filters, export
JSONL and use `jq -c`; CSV is formula-safe by default; Markdown is for review.
The epic rollup is view-only; board exports retain their existing row formats.
Markdown follows the requested field projection, while retaining explicit
status, column, and backlog context.
Use a confirmed `--view <name>` from `atl config show` for recurring team
projections; explicit `--columns` wins for one read.
These reads never call rank/move/update endpoints. Sprint `add/remove` are
separate writes and still require explicit user intent.

## Tempo Structure (read-only)

Structure commands use numeric ids and never write Structure data:

```bash
atl jira structure get 123
atl jira structure view 123 -o text
atl jira structure view 123 --fields key,summary,status,assignee
atl jira structure view 123 --view full
atl jira structure forest 123
atl jira structure rows 123 --root "release train"
atl jira structure folders 123
atl jira structure view 123 --folder-id 100 -o text
atl jira structure view 123 --folder-id 100 --expected-forest-signature 55 --expected-forest-version 7
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure export 123 --format jsonl --out structure.jsonl
atl jira structure export 123 --folder-id 100 --format jsonl --out structure.jsonl \
  --expected-forest-signature 55 --expected-forest-version 7
```

Use `view` first for agent analysis: JSON is compact and jq-friendly, `-o text`
is a readable Markdown table, and stored folders receive best-effort labels.
Calculated grouping rows intentionally keep technical identities because their
row ids can be regenerated. The default Jira-field projection is
`key,summary,status,assignee`; use `--fields` for the PM's
planning columns or a confirmed named `--view` for repeated projections.
Structure `pull-issues --limit` is aggregate: zero means no configured issue
cap and negative values fail before hierarchy reads or output creation.
The snapshot reports `projection.source:list-view` for built-in/custom presets
and `projection.source:explicit` only when `--fields` wins.
Do not claim this matches the browser's selected saved view:
the supported integration API does not reliably expose saved/per-user columns,
and the output explicitly records `browser_view_reproduced:false`.
Compact projections use human labels for known Jira objects and `[object]` for
an unknown non-empty object; inspect a raw issue snapshot when its internal
shape is required.
Generated `row_id` values can be ephemeral; atl resolves issues by stable
`item_id` only on `item_type:issue` rows. A deleted or permission-hidden id does
not reject the rest of a generated Structure identity-join batch; its row
remains explicit with `accessible:false`. Ordinary user-authored JQL remains
strict. Filter and correlate primarily by `row.values.key`, `row.item_id`,
and hierarchy position within one snapshot.

When the folder is unknown, call `structure folders` once, then reuse its stable
`folder_id` in `view --folder-id`. Use `--folder-path` only when an exact human
path is known and labels are complete; it fails on missing or duplicate paths.
Path matching ignores case and repeated whitespace; names containing `/` must
use folder id/row selection. Completeness is scoped to the emitted subtree.
`--folder-row` is snapshot-local. Keep `--root` only for explicitly fuzzy work.
Selected Markdown uses relative numeric Depth with separate Key/Summary cells;
JSON keeps absolute depth and adds `relative_depth`.

Bind that second call to the forest the selector came from: copy both
`signature` and `version` from the earlier `view`, `folders`, or `rows` result
into `--expected-forest-signature` and
`--expected-forest-version`. `view`, `rows`, `pull-issues`, and `export` all
accept that pair; `get`, `forest`, `folders`, and `values` accept none. The
flags are optional but paired — omitting both is
an explicitly ungated read, and an unpaired, zero, or non-positive pair is a
usage error (exit 2) before any backend request. A pair that does not match the
current forest exits 8 on the initial forest read — before folder labels,
Structure Value or Jira issue expansion, export rendering, and before any
`--out` file exists, so no partial artifact is left behind. It reports only the
expected and current integers; re-read the view,
re-select the subtree there, and retry once with the new pair. Omit both only
for a selector fixed outside any earlier read. If either returned version
member is zero, the pair is non-bindable: omit both flags, keep the selection
explicitly ungated, and report that limitation. Every result reports
`forest_version_gated`, alongside `forest_version` on `view`/`export` and the
existing `version` on `rows`/`pull-issues`; there is no
second forest request. That version covers the hierarchy and the selection only
— Jira issue fields and stored folder labels are separately timed, so do not
present them as one atomic versioned value snapshot. A gated CSV export reports
that provenance only in the command result, because CSV headers and cells are
unchanged; keep it with the file if the export must stay auditable. The default
JSON result and `-o text` both report the pair and gate state.

For repeated filtering, export JSONL and use `jq -c` per record; use CSV for
spreadsheet/relational tools and Markdown for human review. Exports support
`json|jsonl|csv|md`; CSV neutralizes formulas unless the user explicitly
approves `--raw-csv` for a trusted non-spreadsheet consumer. `rows` and `values`
remain low-level diagnostics. `pull-issues` is the separate rich/raw Jira
snapshot path. Explicit per-row permission gaps remain visible through `complete`, `accessible`, and
`inaccessible_rows`; plugin/object failures normally surface as exit 4/6.
The reported unique-issue count always describes the emitted root/subtree.
