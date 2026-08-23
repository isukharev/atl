<!-- Generated from skills-src/jira/reference/commands.md — edit the source and run 'make gen-plugins'. -->
# Jira command reference

For status/quarter analysis, choose commands through
[evidence-workflow.md](evidence-workflow.md) instead of mechanically calling
every read primitive.

Load this reference only when exact command or flag lookup is useful.

The typed `jira_issue_graph` counterpart is Jira-only, caps depth at 2, and
takes no Confluence-resolution or strictness input. Its reported
`bounds.max_response_bytes` is a fixed aggregate Jira response bound, while the
`max_bytes` input separately bounds the encoded MCP result. Omitted or false
`include_development` preserves the stable projection without treating absent
Development as zero; true returns closed experimental SCM coordinates and omits
Development-node URLs. Omitted or explicit full projection keeps schema-v2
bytes and requests unchanged. Compact schema v1 accepts the same
`urls|scm|none` selection as CLI, after full collection; it defaults to URLs
and adds SCM by default only with Development enabled. ATL makes no GitLab
request. A downstream read requires
exact owner-approved lowercase host equality and a separately authenticated
read-only client, never Jira credentials. Use the CLI graph route for optional
id/title-only Confluence resolution.

## Inverse-reference search

The `jira/inverse-reference` capability starts from one exact GitLab project or
Confluence page and returns referring issues inside an explicit Jira scope. It
is CLI-only and has no typed MCP counterpart. Run one bounded JSON command:

```bash
export ATL_READ_ONLY=1
atl jira issue reference search \
  --target 'https://gitlab.example.test/platform/widget' \
  --target-kind gitlab-project \
  --scope-jql 'project = DEMO' \
  --mode exhaustive \
  --sources description,comments,remote-links,development \
  --max-issues 100 \
  --max-requests 1000 \
  --max-response-bytes 16777216 \
  --strict
```

Every invocation must supply `--target`, `--target-kind
gitlab-project|confluence-page`, a caller-qualified `--scope-jql` without
`ORDER BY`, `--mode exhaustive|fast`, repeat/comma `--sources`, and all three
positive bounds. Sources are `description`, `fields`, `comments`,
`remote-links`, `worklogs`, `development`, and `properties`. Selecting
`fields` requires one or more exact technical ids in `--fields`; supplying
field ids without that source is invalid. Hard maxima are 2048 target bytes,
16384 JQL bytes, 128 field ids, 5000 issues, 25000 requests, and 268435456
response bytes.

Use `exhaustive` for an absence question. Its two terminal key-ordered scope
passes must reconcile to the same issue set, after which every requested source
is verified for every issue. This detects selection drift but does not create
an atomic Jira snapshot. Only `complete:true` and `absence_proven:true` prove
zero matches. Fast mode is target-derived discovery and always returns
`selection.complete:false`. An otherwise normally terminal narrowed pass uses
`reason:"mode_fast"`; any concrete selection failure retains its own closed
reason.

Use the default JSON for reasoning. It emits only an opaque target id,
normalized selectors, phase/source qualification, content-free match facts,
frontier, reconciliation, and usage. It omits the target, JQL, URLs, titles,
source values, property keys, identities, and backend errors. `--strict` emits
that same result before exit 8 when incomplete; retain it. Text is only a match
table and cannot prove absence; id output is unsupported.

Matching happens locally after bounded Jira reads. ATL never contacts GitLab
or dereferences any discovered URL. A caller-supplied Confluence display or
short target may use the configured Confluence resolver under the shared
single-attempt budget; ids and direct id-bearing URLs resolve offline.
Confluence values found in Jira match only direct same-origin id-bearing links,
not display/short URLs, and no page body or backlink query is made.

| Command | What it does | Key flags |
|---|---|---|
| `jira project list` | List visible projects with explicit local completeness | `--include-archived`, `--limit` 1..1000, `-o text/id` |
| `jira issue get <KEY>` | Get an issue | `--fields` |
| `jira issue fields <KEY>` | Compact non-empty named field inspection | repeat `--field`; opt in with `--include-empty` or private `--raw` |
| `jira issue field get <KEY>` | Qualified bounded expansion of one exact compact value | `--field` required; `--max-bytes` 256..131072, default 16384 |
| `jira issue field preview <KEY>` | GET-only file-backed custom-field proposal, safe under `ATL_READ_ONLY=1` | `--from-file FIELD=PATH`, `--from-md FIELD=PATH`, `--allow-fields` |
| `jira issue view <KEY>` | Render one configured Markdown view without writing files | `-o text`, `--render-root`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira issue search` | Search as a paginated common IssueList / Markdown table | `--jql`, `--view`, `--columns`, `--limit` 1..1000, `--cursor` |
| `jira issue search -o id` | Print matching issue keys one per line | `-o id` |
| `jira issue types` | Discover create-eligible issue types for one project | `--project`, `-o text/id` |
| `jira issue create-check` | Inspect content-free create-screen field requirements | `--project`, exact `--type`, `-o text` |
| `jira issue create-metadata` | Inspect bounded, qualified create schema and omittability without raw defaults/options | `--project`, exact `--type`, `-o text` |
| `jira issue children <EPIC-KEY>` | Read direct epic children as a common IssueList without per-child reads | `--view`, `--columns`, `--limit 1..1000` (0 invalid), `--cursor`, `--epic-field`, `-o text/id` |
| `jira epic digest <EPIC-KEY>` | Deterministic multi-source epic evidence with per-source completeness | `--projection compact|full`, period, includes, fields, caps, optional bounded Confluence heading expansion |
| `jira issue create [preview]` | Preview by default; apply one exact hash-guarded create and optionally register its proved readback | candidate flags plus apply-only `--apply --expected-proposal-hash`; `--register --into <ROOT>` |
| `jira issue update <KEY>` | Update summary/description/fields (whole body) | `--summary`, `--from-md`, `--from-file`, `--field k=v`, `--field-json k=JSON` |
| `jira issue field set <KEY>` | Apply a reviewed file-backed custom-field proposal | `--from-file FIELD=PATH`, `--from-md FIELD=PATH`, `--allow-fields`, `--expected-updated`, `--expected-proposal-hash`, `--apply` |
| `jira issue edit preview <KEY>` | GET-only content-free targeted-description proposal | `--old`, `--new`, `--old-file`, `--new-file`, `--all`; safe under `ATL_READ_ONLY=1` |
| `jira issue edit <KEY>` | Preview or apply one hash-bound targeted description edit | same matcher inputs; apply with `--apply --expected-proposal-hash`; `--dry-run` is a preview alias |
| `jira issue assign <KEY>` | Set or clear the assignee | exactly one of `--to USER`, `--me`, `--none` |
| `jira issue transition preview <KEY>` | GET-only state-bound transition proposal | `--to`, optional `--comment`, `--field k=v`, `--field-json k=JSON`; inspect selected transition/current state/hash |
| `jira issue transition <KEY>` | Preview or apply one reviewed transition | `--to`, optional `--comment`, `--field k=v`, `--field-json k=JSON`, `--apply`, `--expected-proposal-hash` |
| `jira issue check <KEY>` | Audit required/important fields; non-zero exit if required field empty | `--require fields`, `--warn fields` |
| `jira issue delete <KEY>` | Preview/apply one immutable-id-bound permanent deletion; the whole leaf is mutation-classified | after explicit approval remove inherited read-only policy for preview; apply: `--apply --confirm DELETE --expected-updated --expected-proposal-hash`; optional reviewed `--delete-subtasks`; restore policy afterward |
| `jira issue labels preview <KEY>` | GET-only reviewed label-delta proposal | combined `--add labels`, `--remove labels`; safe under `ATL_READ_ONLY=1` |
| `jira issue labels <KEY>` | Preview/apply one guarded add/remove delta | combined `--add labels`, `--remove labels`; apply with `--apply --expected-proposal-hash` |
| `jira issue watchers list <KEY>` | Read watcher membership | inspect `complete` |
| `jira issue watchers add\|remove <KEY>` | Guarded watcher preview/apply | exactly one of `--username`, `--me`; `--apply`, `--expected-proposal-hash` |
| `jira issue worklog list <KEY>` | Read complete time entries | `-o text/id`; inspect `complete` |
| `jira issue worklog add <KEY>` | Baseline-bound one-entry time preview/apply | `--time`, optional `--started`, `--from-file`; review `baseline_sha256`; `--apply`, `--expected-proposal-hash` |
| `jira issue history <KEY>` | Qualified changelog with deterministic `summary`; inspect `complete`, separate missing/non-empty-id identity facts, summary consistency, and `last_changes` | repeat `--field`; `--since`, `--until`; `--summary-only` omits raw history and rejects explicit false |
| `jira issue graph <KEY>` | Full schema-v2 or compact schema-v1 bounded graph with metadata-reconciled fields; depth zero is direct, while greater depths follow only exact structured Jira relations; optional phases resolve discovered Confluence id/title metadata or collect fail-closed Jira Development coordinates | `--projection full|compact`; repeat/comma `--select urls|scm|none` for compact JSON; `scm` requires `--include-development`; `--depth` 0..3, `--resolve none|confluence`, node/edge/evidence/request/byte limits, `--strict`; Development is experimental and never fetched from GitLab |
| `jira issue reference search` | CLI-only content-free search from one exact GitLab project or Confluence page into a caller-qualified Jira scope; source-qualified fast discovery or exhaustive absence proof | required `--target`, `--target-kind`, `--scope-jql`, `--mode`, `--sources`, `--max-issues`, `--max-requests`, `--max-response-bytes`; exact `--fields` iff fields source; optional `--strict` |
| `jira issue refs [KEY]` | Extract provenance-qualified artifact references with reconciled per-issue/top-level aggregates; field ids or exact names; JQL adds one complete comment listing per issue | `--jql`, `--fields`, aggregate `--limit` (0 all, negative invalid) |
| `jira issue tree` | Build read-only epic-to-child grouping | `--jql`, `--epic-field`, `--fields`, aggregate `--limit` (0 all, negative invalid) |
| `jira issue comment preview <KEY>` | GET-only full-record/body/actor/issue-bound append proposal with content-minimized output | `--from-md`, `--from-file`; inspect identity, hashes, counts, bounds, and usage |
| `jira issue comment add <KEY>` | Preview or apply one reviewed append; one numeric-id POST and exact no-replay readback | `--from-md`, `--from-file`, `--apply`, `--expected-proposal-hash` |
| `jira issue comment list <KEY>` | List comments | — |
| `jira issue comment delete <KEY> <ID>` | Delete a comment | — |
| `jira issue link add preview <KEY>` | GET-only guarded-link proposal | `--to KEY2`, `--type blocks`; safe under `ATL_READ_ONLY=1` |
| `jira issue link add <KEY>` | Preview/apply an exact reviewed link | `--to KEY2`, `--type blocks`; apply with `--apply --expected-proposal-hash` |
| `jira issue link list <KEY>` | List links with ids | — |
| `jira issue link delete preview <LINK-ID>` | GET-only exact-link deletion proposal | required `--from`, `--to`, `--type` |
| `jira issue link delete <LINK-ID>` | Preview/apply exact-link deletion | required `--from`, `--to`, `--type`; apply with reviewed hash |
| `jira issue link suggest` | Read-only missing-link candidates from CSV | `--csv` |
| `jira issue plan apply` | Dry-run/apply guarded CSV operation plan | `--csv`, `--allow-ops`, `--allow-fields`, `--allow-link-types`, `--continue-on-error`, `--apply`, `--confirm APPLY` |
| `jira issue link-epic <KEY>` | Set the configured or auto-resolved Epic Link | `--epic EPIC-KEY`; optional global `render.jira.epic_field` selector |
| `jira issue attachment list <KEY>` | List attachments | `-o id` |
| `jira issue attachment get <KEY>` | Download an attachment | `--id ID-or-filename`, `--into DIR` |
| `jira issue attachment upload <KEY>` | Upload an attachment | `--file PATH` |
| `jira issue images <KEY>` | Download image attachments | `--into DIR` |
| `jira pull` | Export `.wiki` + `.md` + `.json` per issue | `--jql`, `--into`, aggregate `--limit` (0 all, negative invalid), `--fields`, `--assets`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira attachment-bodies` | Resume one-body private materialization from completed qualified attachment inventories | `--into`, repeat exact `--attachment-media-type`, explicit `--max-attachment-bytes` (<=128 MiB), explicit `--max-transactions` (1..4096) |
| `jira render [DIR\|FILE]` | Regenerate `.md` views offline | `--render-profile`, `--render-include`, `--render-exclude`, `--into` |
| `jira apply <FILE.md>` | Merge/stage supported generated edits | `--dry-run`, `--allow-loss`, `--rebase-pending`, `--into`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira snapshot [DIR]` | Exact content-free mirror health cardinalities | `[DIR]` or `--into`, `--remote` |
| `jira status [DIR]` | Show local edits and optional remote drift | `[DIR]` or `--into`, `--remote` |
| `jira push <file.wiki\|DIR>` | Preview or apply guarded write-back | `--apply`, `--force`, `--into` |
| `jira export` | Write compact JSONL/JSON/CSV plus manifest, or artifact-only stdout with `--out -`; explicit ids/keys keep selector order and omit missing rows | `--jql`/`--ids`/`--keys`, `--out`, `--format`, aggregate `--limit` (0 all, negative invalid), `--fields` ids/names, `--batch-size`, `--raw-csv` |
| `jira export diff <OLD> <NEW>` | Compare compact exports | — |
| `jira planning report` | Deterministic planning quality report | `--jql`, `--require`, `--estimate-field`, `--epic-field`, aggregate `--limit` (0 all, negative invalid), `--csv`, `--raw-csv` |
| `jira quality-report` | Compatibility alias | same flags as `planning report` |
| `jira fields` | List or compactly summarize a qualified value-free Jira field catalog | `--name-like`, `--id`, `--id-like`, `--schema`, `--custom true|false`, `--summary-only`, `-o text` |
| `jira field-options` | List allowed field values | `--project`, `--type`, `--field`, `-o text` |
| `jira transitions` | List available transitions | `--key`, `-o text` |
| `jira link-types` | List issue link types | `-o text` |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users | `--limit 1..1000` (0 invalid) |
| `jira user get <USERNAME>` | Get a user | — |
| `jira board list` | Discover Agile boards | `--project`, `--limit 1..50` (0 invalid), `--cursor`, `-o id` |
| `jira board get <ID>` | Get board identity | `-o id` |
| `jira board config <ID>` | Get filter, ordered columns/statuses, constraints, estimation, rank | `-o text/id` |
| `jira board issues <ID>` | Read one backend-ranked IssueList page | `--view`, `--columns`, `--jql`, `--limit 1..50` (0 invalid), `--cursor`, `-o text/id` |
| `jira board backlog <ID>` | Read one Scrum backlog IssueList page | `--view`, `--columns`, `--jql`, `--limit 1..50` (0 invalid), `--cursor`, `-o text/id` |
| `jira board view <ID>` | Read normalized config/issues/backlog snapshot with optional deterministic epic rollup | `--scope all/board/backlog`, `--view`, `--columns`, `--jql`, aggregate `--limit` (0 all, negative invalid), `--epic-field`, repeatable `--done-status`, `-o text/id` |
| `jira board export <ID>` | Write normalized board artifact | `--scope`, `--view`, `--columns`, `--jql`, aggregate `--limit` (0 all, negative invalid), `--format json/jsonl/csv/md`, `--out`, `--raw-csv` |
| `jira sprint get <ID>` | Get one sprint by numeric id | `-o text/id` |
| `jira sprint issues <ID>` | Read one sprint IssueList page | `--view`, `--columns`, `--limit 1..50` (0 invalid), `--cursor`, `-o text/id` |
| `jira structure get <ID>` | Get Structure metadata | `-o id` |
| `jira structure view <ID>` | Read normalized hierarchy + Jira fields, optionally bound to one forest version | exact folder selector or fuzzy `--root`, paired `--expected-forest-signature`/`--expected-forest-version`, `--view`, `--fields`, `--batch-size`, `-o text/id` |
| `jira structure forest <ID>` | Get raw latest Structure forest formula | — |
| `jira structure rows <ID>` | Parse Structure forest rows, optionally bound to one forest version | exact folder selector or fuzzy `--root`, `--root-fields`, paired `--expected-forest-signature`/`--expected-forest-version`, `-o id` |
| `jira structure folders <ID>` | Discover stable stored folders, paths, and subtree statistics without Jira issue reads | `-o text/id` |
| `jira structure values <ID>` | Get row values | `--rows`, `--fields` |
| `jira structure pull-issues <ID>` | Fetch snapshots from Structure rows, optionally bound to one forest version | exact folder selector or fuzzy `--root`, paired `--expected-forest-signature`/`--expected-forest-version`, `--view`, `--fields`, `--batch-size`, aggregate `--limit` (0 all, negative invalid), `--out`, `-o id` |
| `jira structure export <ID>` | Write a normalized offline Structure artifact, optionally bound to one forest version | exact folder selector or fuzzy `--root`, paired `--expected-forest-signature`/`--expected-forest-version`, `--view`, `--fields`, `--format json/jsonl/csv/md`, `--out`, `--raw-csv`, `-o text` |
