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
atl jira sprint list --board 5 --state active
atl jira sprint current --board 5
atl jira sprint issues 7 --fields summary,status
atl jira sprint add 7 PROJ-1 PROJ-2
atl jira sprint remove PROJ-1
```

## Tempo Structure (read-only)

Structure commands use numeric ids and never write Structure data:

```bash
atl jira structure get 123
atl jira structure forest 123
atl jira structure rows 123 --root "release train"
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure export 123 --format json --out structure.json
```

`rows` reports parsed hierarchy; `--root` returns the first matching row and its
descendants. `values` preserves backend responses and permission gaps.
`pull-issues` fetches snapshots referenced by issue rows. Exports support
`json|csv|md`; CSV neutralizes formulas unless the user explicitly approves
`--raw-csv` for a trusted non-spreadsheet consumer. Plugin/object/permission
problems normally surface as exit 4/6.
