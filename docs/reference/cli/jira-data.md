# Jira data and reports

Exports, planning reports, catalogs, transitions, link types, and user lookup.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl jira export`

Write one compact issue export as a file plus backend-identity-hashed provenance
manifest, or as a transient stdout artifact. This is for scripts and analysis
that need JSONL/JSON/CSV instead of a directory mirror. For file destinations,
the manifest is written to `<out>.manifest.json` and stores query, fields,
format, count, CLI version, and a backend URL hash; it does not store the backend
hostname or token.

```bash
atl jira export --jql "project=PROJ" --format jsonl --out issues.jsonl
atl jira export --jql "project=PROJ" --format csv --fields customfield_10001 --out issues.csv
atl jira export --jql "project=PROJ" --format csv --out raw.csv --raw-csv # unsafe in spreadsheets
atl jira export --jql "project=PROJ" --format json --out issues.json --limit 10000
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 --out selected.jsonl
atl jira export --keys PROJ-1,PROJ-2 --fields "Delivery Notes" --out - | jq -s '.'
atl jira export --ids 10001,10002 --format json --out - | jq 'map(.key)'
atl jira export diff old.jsonl new.jsonl
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query; pass exactly one of `--jql`, `--ids`, or `--keys` |
| `--ids` | comma-separated numeric issue ids; generated batches emit found rows in de-duplicated first-occurrence order |
| `--keys` | comma-separated issue keys; generated batches emit found rows in case-insensitive first-occurrence order |
| `--batch-size` | max ids/keys per generated JQL batch (default 100) |
| `--out` | artifact path (writes `<out>.manifest.json`), or `-` for artifact-only stdout with no files/manifest |
| `--format` | `jsonl`, `json`, or `csv` (default `jsonl`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated exact field ids or unambiguous display names |
| `--raw-csv` | preserve formula-leading CSV cells verbatim (unsafe in spreadsheets) |

JSONL and CSV are written incrementally through an fsynced atomic temporary file, so
`--limit 0` does not accumulate issue payloads or the artifact in memory. Aggregate
JSON intentionally caps at 10,000 issues and 64 MiB of serialized issue data;
use JSONL/CSV or a smaller limit for larger selections. CSV neutralizes formula-leading cells by default using an
apostrophe prefix. The manifest records `csv_raw: true` when the unsafe raw mode
is explicitly selected. Exact cross-page deduplication uses a bounded identity
index and refuses a single export above 250,000 unique issues; split larger
selections into multiple exports.

For explicit `--keys` and `--ids`, every format and destination preserves the
caller's de-duplicated first-occurrence order across generated batches, even
when Jira returns pages in another order. Missing or inaccessible identities
produce no placeholder row; the surrounding found rows keep their requested
relative order, and `--limit` counts only emitted rows. File manifests declare
`row_order: selector` and `missing_identity_behavior: omit`. A user-authored
`--jql` is never reordered (`row_order: backend`). Explicit selections are
buffered only one configured batch at a time, with a 64 MiB reorder-buffer cap
that asks the caller to reduce `--batch-size`; JQL JSONL/CSV stays streaming.

With `--out -`, stdout contains **only** the artifact: one object per line for
JSONL, a JSON array for `--format json`, or CSV bytes. No manifest, command
result object, or trailing status line is emitted, and no filesystem artifact is
created. The artifact type is selected by `--format`; do not pass `-o text`
with `--out -` (that combination returns exit 2). Warnings/errors remain on
stderr. JSON keeps the same 10,000-issue and
64 MiB aggregate caps; JSONL/CSV retain the 250,000-identity safety cap and
formula neutralization. A streaming request can have written a prefix before a
later backend failure, so pipelines must check atl's exit status and discard
stdout on non-zero exit. Display-name fields resolve fail-closed before the
first search and the artifact uses their stable ids.

`jira export diff` compares compact JSONL/JSON/CSV exports by issue key (or id
when key is absent) and reports deterministic `added`, `removed`, and `changed`
identifier lists.

## `atl jira planning report`

Build a deterministic read-only planning quality report over a JQL query. The
rubric checks summary, description, assignee, optional estimate field, optional
required fields, artifact references, and optional epic decomposition.

```bash
atl jira planning report --jql "project=PROJ" \
  --estimate-field customfield_10001 \
  --epic-field customfield_10002 \
  --require fixVersions,components \
  --csv planning.csv
atl jira planning report --jql "project=PROJ" --csv raw.csv --raw-csv # unsafe in spreadsheets
atl jira quality-report --jql "project=PROJ"     # compatibility alias
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--require` | comma-separated fields that must be populated |
| `--estimate-field` | field id/name used as the estimate check |
| `--epic-field` | field id/name containing parent epic key; enables child lists and missing-epic gaps |
| `--limit` | max issues (0 = all; default 100) |
| `--csv` | optional CSV report path |
| `--raw-csv` | preserve formula-leading cells verbatim; requires `--csv` and is unsafe in spreadsheets |

Output includes per-issue `score`, `max_score`, `level` (`good|warn|poor`),
`gaps`, extracted `refs`, and `children` for epic rows when `--epic-field` is
set. Reference kinds are deterministic categories such as `doc`, `design`,
`jira`, `chat`, and `link`.

## `atl jira fields`

List all Jira fields (system and custom) with their IDs and schema types.

```
atl jira fields
atl jira fields --name-like Epic
atl jira fields --id customfield_10001
atl jira fields --custom true --schema string --id-like customfield
atl jira fields --summary-only
```

Filters are applied client-side to Jira's field list. Available filters are
`--id`, `--id-like`, `--name-like`, `--schema`, and `--custom true|false`.
`--summary-only` keeps the same filters and qualification but emits no field
definitions. It sets `projection:"summary"` and returns the filtered
`custom_count` and `system_count`; their sum equals `count`. The default full
projection keeps `fields` and reports the same reconciled counts. Use
`field-options` when you need values allowed for a specific project and issue
type. The JSON envelope is versioned and reports `source`, `complete`, optional
`partial_reason`, `projection`, the unfiltered `total`, and filtered `count`;
filters and projections never change source completeness. A successful
non-empty atomic Jira catalog is complete. An empty or unqualified source is
explicitly incomplete, so agents must not treat a non-empty match or successful
call alone as proof. The full `-o text` projection preserves its
`complete, source, count, total` first line and emits one tab-separated
`id, name, custom, schema` record per field. The summary text projection adds
one `projection=summary, custom, system` line and no field records. Field
options and link types emit one value per line, and transitions emit
`id, destination, name`.

## `atl jira field-options`

List allowed values for a field on a specific project/issue-type combination
(uses the `createmeta` API).

```
atl jira field-options --project PROJ --type Bug --field priority
atl jira field-options --project PROJ --field customfield_10020
```

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (optional) |
| `--field` | field id or display name (required) |

## `atl jira transitions`

List the workflow transitions currently available for an issue.

```
atl jira transitions --key PROJ-1
```

## `atl jira link-types`

List the configured issue link type names.

```
atl jira link-types
```

## `atl jira me` / `atl jira user {search,get}`

Identity lookups using the Data Center username/userkey model (not Cloud
accountId). `-o id` prints the username for piping.

```
atl jira me                      # the authenticated user
atl jira user search 'alice'     # {users:[{name,key,displayName,email,active}]}
atl jira user get alice          # one user by DC username
```
