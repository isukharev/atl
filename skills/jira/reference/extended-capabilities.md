<!-- Generated from skills-src/jira/reference/extended-capabilities.md — edit the source and run 'make gen-plugins'. -->
# Jira attachments, planning, Agile, and Structure

Load only the section needed for the current task.

## Compact exports

```bash
atl jira export --jql '<JQL>' --format jsonl --out issues.jsonl
atl jira export --jql '<JQL>' --format csv --out issues.csv
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 --out selected.jsonl
atl jira export diff old.jsonl new.jsonl
```

The manifest contains query/fields/count and a backend identity hash, so it is
credential-sanitized but may still be private. JSONL/CSV stream; aggregate JSON
is capped at 10,000 issues/64 MiB and row streams at 250,000 identities. CSV
neutralizes formulas unless the user explicitly approves `--raw-csv` for a
trusted non-spreadsheet consumer.

## Guarded bulk links and plans

Use `jira issue link suggest --csv ...` before bulk link work. Plan CSV requires
schema `version=1`, `expected_updated` on every row, narrow operation/field/link
allowlists, and one row per source issue. It is fail-fast and dry-run unless
both `--apply` and `--confirm APPLY` are present; `--continue-on-error` still
returns exit 8 for blocked/failed rows. Split dependent mutations so an earlier
write cannot invalidate a later freshness gate.

## Attachments and images

```bash
atl jira issue attachment list PROJ-1
atl jira issue attachment get PROJ-1 --id spec.xlsx --into ./attachments
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
atl jira issue images PROJ-1 --into /tmp/proj1-images
```

Use `attachment get` for any file type, `attachment upload` to add a file, and
`images` when visual inspection needs only image attachments.

## Planning quality reports

```bash
atl jira planning report --jql '<JQL>' --estimate-field customfield_10001 \
  --epic-field customfield_10002 --require fixVersions,components --csv planning.csv
atl jira quality-report --jql '<JQL>'
```

Reports deterministic `score`, `level`, `gaps`, artifact `refs`, and optional
epic `children`. Use `issue refs` for links only and `issue tree` for normalized
epic/child structure only.

## Boards and sprints

These commands require Jira Software (GreenHopper); Core-only instances return
404/exit 4. Discover numeric ids before acting:

```bash
atl jira board list --project PROJ
atl jira board get 5
atl jira board config 5
atl jira board view 5 -o text
atl jira board view 5 --jql 'statusCategory != Done' --limit 500
atl jira board export 5 --format jsonl --out board.jsonl
atl jira sprint list --board 5 --state active
atl jira sprint current --board 5
atl jira sprint issues 7 --fields summary,status
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
explicit. Use `--jql 'statusCategory != Done'` or another user-approved
refinement when an old board has a very large history. `--limit 0` reads all;
positive limits are explicit truncation per scope. For repeated filters, export
JSONL and use `jq -c`; CSV is formula-safe by default; Markdown is for review.
These reads never call rank/move/update endpoints. Sprint `add/remove` are
separate writes and still require explicit user intent.

## Tempo Structure (read-only)

Structure commands use numeric ids and never write Structure data:

```bash
atl jira structure get 123
atl jira structure view 123 -o text
atl jira structure view 123 --fields key,summary,status,assignee
atl jira structure forest 123
atl jira structure rows 123 --root "release train"
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure export 123 --format jsonl --out structure.jsonl
```

Use `view` first for agent analysis: JSON is compact and jq-friendly, `-o text`
is a readable Markdown table, and stored folders receive best-effort labels.
Calculated grouping rows intentionally keep technical identities because their
row ids can be regenerated. The default Jira-field projection is
`key,summary,status,assignee,priority,issuetype`; use `--fields` for the PM's
planning columns. Do not claim this matches the browser's selected saved view:
the supported integration API does not reliably expose saved/per-user columns,
and the output explicitly records `browser_view_reproduced:false`.
Generated `row_id` values can be ephemeral; atl resolves issues by stable
`item_id`. Filter and correlate primarily by `row.values.key`, `row.item_id`,
and hierarchy position within one snapshot.

For repeated filtering, export JSONL and use `jq -c` per record; use CSV for
spreadsheet/relational tools and Markdown for human review. Exports support
`json|jsonl|csv|md`; CSV neutralizes formulas unless the user explicitly
approves `--raw-csv` for a trusted non-spreadsheet consumer. `rows` and `values`
remain low-level diagnostics. `pull-issues` is the separate rich/raw Jira
snapshot path. Explicit per-row permission gaps remain visible through `complete`, `accessible`, and
`inaccessible_rows`; plugin/object failures normally surface as exit 4/6.
