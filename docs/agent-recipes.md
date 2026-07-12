# Agent recipes for Jira and Confluence

These recipes are compact retrieval targets for coding agents. They complement
the complete [usage reference](usage.md) and machine-readable [output
contract](OUTPUT_CONTRACT.md); those documents remain authoritative for every
flag and JSON field.

`atl` is non-interactive and emits JSON by default. Use `-o text` when Markdown
is easier to read and `-o id` only on commands that document an identity
projection.

## Configure Server or Data Center backends

Configure public base URLs separately from credentials. Tokens are read from
the environment or the owner-only credential store, never from a mirror.

```sh
atl config set --jira-url https://jira.example.com
atl config set --confluence-url https://confluence.example.com
atl auth login --service jira
atl auth login --service confluence
atl auth status
```

Atlassian Cloud email/API-token authentication is not supported. For scripts,
inspect configuration without exposing credentials:

```sh
atl config show | jq '{jira_url, confluence_url, jira_list_views}'
```

## Find and read Jira issues without creating a mirror

Use the common IssueList contract for search and `issue view` for one complete
configured Markdown projection.

```sh
atl jira issue search \
  --jql 'project = PROJ AND statusCategory != Done ORDER BY updated DESC' \
  --view default --limit 50

atl jira issue search \
  --jql 'project = PROJ ORDER BY updated DESC' \
  --columns key,summary,status,assignee,priority \
  -o text

atl jira issue view PROJ-42 --render-profile full -o text
```

The stable list shape keeps selected Jira fields under `values`, source-specific
facts under `context`, and completeness under `page`:

```json
{
  "schema_version": 1,
  "source": {"kind": "jql"},
  "selection": {"jql": "project = PROJ ORDER BY key"},
  "projection": {
    "columns": ["key", "summary", "status"],
    "fields": ["summary", "status"],
    "ordering": "jql-order",
    "view": "explicit"
  },
  "rows": [{
    "key": "PROJ-42",
    "id": "10001",
    "position": 0,
    "values": {"summary": "Example issue", "status": "Open"}
  }],
  "page": {"count": 1, "complete": true, "truncated": false, "next_cursor": null}
}
```

Read JSON rows and pagination explicitly:

```sh
atl jira issue search --jql 'project = PROJ ORDER BY key' --limit 50 |
  jq '{issues: [.rows[] | {key, summary: .values.summary}], next: .page.next_cursor}'
```

Transient views are read-only. Do not save their Markdown into a mirror or feed
it to apply.

## Analyze Jira evidence without manual joins

Choose the narrowest path instead of calling every read command:

| Question shape | Efficient path |
|---|---|
| unfamiliar single issue | compact non-empty fields → selected history/refs |
| epic/quarter with known field names | one epic digest |
| epic/quarter with unknown field names | compact fields → one epic digest |
| several known keys | transient batch export → per-key exceptions only |
| linked long Confluence page | resolve → outline → one exact section |

For a first analysis of an unfamiliar epic:

```sh
atl --read-only jira issue fields PROJ-42

atl --read-only jira epic digest PROJ-42 \
  --quarter 2026-Q2 \
  --status-field 'Delivery Notes' \
  --dod-field 'Definition of Done'
```

The first command omits empty fields and compacts users/options/unknown objects.
Choose an exact field name or stable id; do not begin with `*all` or raw values.
The digest joins the current epic, dated field changes, paginated children,
comments, blockers, refs, and explainable staleness. It deliberately does not
write management prose. Inspect every `sources.<name>.complete`; an incomplete
source means an absent fact is unproven.

For a non-epic issue, qualify only the evidence field and period you need:

```sh
atl --read-only jira issue history PROJ-43 \
  --field 'Delivery Notes' --since 2026-04-01 --until 2026-06-30
atl --read-only jira issue refs PROJ-43 --fields 'Delivery Notes'
```

Use `last_changes` rather than list position. For several keys, avoid shell
loops and durable manifests:

```sh
atl --read-only jira export \
  --keys PROJ-42,PROJ-43,PROJ-44 \
  --fields 'Delivery Notes,Impact' \
  --format json --out - |
  jq 'map({key, status: .fields.status.name, evidence: .fields.customfield_10001})'
```

Accept streamed stdout only on exit zero. Keep fields narrow; JSONL is the
bounded choice for larger selections.

Expand Confluence only after a reference is known, and only to the requested
section:

```sh
atl --read-only conf page resolve '<same-origin-page-or-short-url>'
atl --read-only conf page outline '<same-origin-page-or-short-url>'
atl --read-only conf page section '<same-origin-page-or-short-url>' \
  --heading 'Metrics' --max-bytes 65536 -o text
```

A digest can expand up to a requested small count with
`--expand-confluence 1 --confluence-heading 'Metrics'`. Honor both digest-source
and section completeness. Do not download a whole page to regex-slice Markdown.

Private output belongs in an owner-only ignored directory, never a public
repository or transcript. Avoid verbose tracing unless diagnosing a failure;
do not publish queries, page URLs, bodies, or user records. Read-only analysis
does not authorize comments, transitions, edits, or mirror replacement.

## Reuse named list projections

Built-in `default` and `full` projections are present in effective config.
Create a custom source-aware projection only for a repeated workflow; explicit
`--columns` or Structure `--fields` still wins for one call.

```sh
atl config show | jq '.jira_list_views'

atl config set jira.list_views.planning '{
  "description":"Planning review",
  "search":["key","summary","status","assignee","priority"],
  "board":["position","key","summary","status","board.column","assignee"],
  "structure":["key","summary","status","assignee","priority"],
  "confluence_macro":["key","summary","status","assignee"]
}'

atl jira issue search --jql 'project = PROJ' --view planning -o text
```

Unknown view names and source-incompatible columns fail before a Jira request.

## Edit a Jira description through a guarded mirror

Pull creates native `.wiki`, derived `.md`, and raw `.json` files. Edit only
supported sections in the versioned Markdown view, merge locally, preview the
server write, and apply it explicitly.

```sh
atl jira pull --jql 'key = PROJ-42' --into mirror-jira

# Edit mirror-jira/PROJ/PROJ-42.md, then inspect the local merge.
atl jira apply mirror-jira/PROJ/PROJ-42.md --dry-run -o text
atl jira apply mirror-jira/PROJ/PROJ-42.md

# jira push is a preview unless --apply is present.
atl jira push mirror-jira/PROJ/PROJ-42.wiki
atl jira push --apply mirror-jira/PROJ/PROJ-42.wiki
```

If apply reports a wiki-only construct loss, restore it or make the deliberate
native `.wiki` edit. Use `--allow-loss` only after reviewing the reported
constructs. Remote drift is never an instruction to replay a write blindly.

## Set a large Jira custom field

`field set` keeps large values out of argv, previews by default, and binds the
reviewed files, issue key, and remote `updated` value to a proposal hash.

```sh
umask 077
atl jira issue field set PROJ-42 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001 > proposal.json

jq '{expected_updated, proposal_hash, fields}' proposal.json

atl jira issue field set PROJ-42 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001 \
  --expected-updated "$(jq -r '.expected_updated' proposal.json)" \
  --expected-proposal-hash "$(jq -r '.proposal_hash' proposal.json)" \
  --apply

rm -f proposal.json
```

Changing the file, issue key, or remote timestamp after preview blocks the
write. Use dedicated commands for summary, links, comments, transitions, and
attachments.

## Inspect a board or Structure plan

Board and Structure reads are read-only planning projections. JSON is best for
analysis; `-o text` produces Markdown tables; exports support JSONL for stream
processing.

```sh
atl jira board list --project PROJ
atl jira board view 5 --view full -o text
atl jira board export 5 --format jsonl --out board.jsonl
jq -s '[.[] | select(.row.values.status != "Done")]' board.jsonl

atl jira structure folders 123 -o text
atl jira structure view 123 --folder-path 'Plans / Quarter' --view full -o text
atl jira structure export 123 \
  --folder-id 100 --fields key,summary,status,assignee \
  --format jsonl --out structure.jsonl
```

Structure browser saved-view columns are not reproduced. Select fields or a
named view explicitly. Treat `complete:false`, inaccessible rows, truncation,
and warnings as partial results rather than an empty plan.

## Read and edit a Confluence page

Use transient `page view` for one-off reading. Pull first when an edit or an
offline baseline is required.

```sh
atl conf page view 123456 --render-profile full -o text

atl conf pull --id 123456 --comments --into mirror
# Edit the generated page.md body, preserving atl document/section markers.
atl conf apply mirror/SPACE/page/page.md --dry-run -o text
atl conf apply mirror/SPACE/page/page.md

atl conf push mirror/SPACE/page/page.csf --dry-run
atl conf push mirror/SPACE/page/page.csf
```

The `.csf` file is the native write substrate. Generated Metadata, Comments,
and Jira Queries sections are read-only. `conf apply` merges only supported body
edits and keeps untouched native CSF bytes.

JQL-bearing Jira macros use the shared IssueList table in a generated suffix:

```sh
atl conf page view 123456 --jira-view planning -o text
atl conf pull --id 123456 --jira-view planning --into mirror
```

Columns configured inside a macro take precedence over the named
`confluence_macro` projection. Single-key Jira macros remain links.

## Handle exit codes without matching messages

Error text is for humans; scripts branch on the stable exit class.

```sh
set +e
result=$(atl jira issue view PROJ-42 2>atl-error.log)
code=$?
set -e

case "$code" in
  0) printf '%s\n' "$result" | jq . ;;
  2) echo 'usage or invalid input' >&2 ;;
  3) echo 'authentication rejected' >&2 ;;
  4) echo 'resource not found' >&2 ;;
  5) echo 'server-side version conflict' >&2 ;;
  6) echo 'forbidden' >&2 ;;
  7) echo 'backend or credential not configured' >&2 ;;
  8) echo 'safety/check gate refused the operation, including Jira drift' >&2 ;;
  *) echo 'unexpected failure' >&2 ;;
esac
```

Never turn an ambiguous or safety-gated write into an automatic retry. Re-read
the current state, reconcile it, and produce a new preview.
