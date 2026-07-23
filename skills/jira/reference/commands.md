<!-- Generated from skills-src/jira/reference/commands.md — edit the source and run 'make gen-plugins'. -->
# Jira command reference

For status/quarter analysis, choose commands through
[evidence-workflow.md](evidence-workflow.md) instead of mechanically calling
every read primitive.

Load this reference only when exact command or flag lookup is useful.

| Command | What it does | Key flags |
|---|---|---|
| `jira issue get <KEY>` | Get an issue | `--fields` |
| `jira issue fields <KEY>` | Compact non-empty named field inspection | repeat `--field`; opt in with `--include-empty` or private `--raw` |
| `jira issue field get <KEY>` | Qualified bounded expansion of one exact compact value | `--field` required; `--max-bytes` 256..131072, default 16384 |
| `jira issue field preview <KEY>` | GET-only file-backed custom-field proposal, safe under `ATL_READ_ONLY=1` | `--from-file FIELD=PATH`, `--from-md FIELD=PATH`, `--allow-fields` |
| `jira issue view <KEY>` | Render one configured Markdown view without writing files | `-o text`, `--render-root`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira issue search` | Search as a common IssueList / Markdown table | `--jql`, `--view`, `--columns`, `--limit`, `--cursor` |
| `jira issue search -o id` | Print matching issue keys one per line | `-o id` |
| `jira issue children <EPIC-KEY>` | Read direct epic children as a common IssueList without per-child reads | `--view`, `--columns`, `--limit`, `--cursor`, `--epic-field`, `-o text/id` |
| `jira epic digest <EPIC-KEY>` | Deterministic multi-source epic evidence with per-source completeness | `--projection compact|full`, period, includes, fields, caps, optional bounded Confluence heading expansion |
| `jira issue create` | Create an issue | `--project`, `--type`, `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue update <KEY>` | Update summary/description/fields (whole body) | `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue field set <KEY>` | Apply a reviewed file-backed custom-field proposal | `--from-file FIELD=PATH`, `--from-md FIELD=PATH`, `--allow-fields`, `--expected-updated`, `--expected-proposal-hash`, `--apply` |
| `jira issue edit <KEY>` | Targeted description replace in one command | `--old`, `--new`, `--old-file`, `--new-file`, `--all`, `--dry-run` |
| `jira issue assign <KEY>` | Set or clear the assignee | exactly one of `--to USER`, `--me`, `--none` |
| `jira issue transition <KEY>` | Transition to a status | `--to`, `--comment`, `--field k=v` |
| `jira issue check <KEY>` | Audit required/important fields; non-zero exit if required field empty | `--require fields`, `--warn fields` |
| `jira issue delete <KEY>` | Permanently delete (DC has no trash) | `--force`, `--delete-subtasks` |
| `jira issue labels <KEY>` | Add/remove labels | `--add labels`, `--remove labels` |
| `jira issue watchers list <KEY>` | Read watcher membership | inspect `complete` |
| `jira issue watchers add\|remove <KEY>` | Guarded watcher preview/apply | exactly one of `--username`, `--me`; `--apply`, `--expected-proposal-hash` |
| `jira issue worklog list <KEY>` | Read complete time entries | `-o text/id`; inspect `complete` |
| `jira issue worklog add <KEY>` | Baseline-bound one-entry time preview/apply | `--time`, optional `--started`, `--from-file`; review `baseline_sha256`; `--apply`, `--expected-proposal-hash` |
| `jira issue history <KEY>` | Qualified changelog with deterministic `summary`; repeat `--field`, filter with `--since`/`--until`; inspect `complete`, separate missing/non-empty-id identity facts, summary consistency, and `last_changes` | — |
| `jira issue refs [KEY]` | Extract provenance-qualified artifact references with reconciled per-issue/top-level aggregates; field ids or exact names; JQL adds one complete comment listing per issue | `--jql`, `--fields`, `--limit` |
| `jira issue tree` | Build read-only epic-to-child grouping | `--jql`, `--epic-field`, `--fields`, `--limit` |
| `jira issue comment add <KEY>` | Add a comment | `--from-md`, `--from-file` |
| `jira issue comment list <KEY>` | List comments | — |
| `jira issue comment delete <KEY> <ID>` | Delete a comment | — |
| `jira issue link add <KEY>` | Link an issue to another | `--to KEY2`, `--type blocks` |
| `jira issue link list <KEY>` | List links with ids | — |
| `jira issue link delete <LINK-ID>` | Delete a link by id | — |
| `jira issue link suggest` | Read-only missing-link candidates from CSV | `--csv` |
| `jira issue plan apply` | Dry-run/apply guarded CSV operation plan | `--csv`, `--allow-ops`, `--allow-fields`, `--allow-link-types`, `--continue-on-error`, `--apply`, `--confirm APPLY` |
| `jira issue link-epic <KEY>` | Set the Epic Link | `--epic EPIC-KEY` |
| `jira issue attachment list <KEY>` | List attachments | `-o id` |
| `jira issue attachment get <KEY>` | Download an attachment | `--id ID-or-filename`, `--into DIR` |
| `jira issue attachment upload <KEY>` | Upload an attachment | `--file PATH` |
| `jira issue images <KEY>` | Download image attachments | `--into DIR` |
| `jira pull` | Export `.wiki` + `.md` + `.json` per issue | `--jql`, `--into`, `--limit`, `--fields`, `--assets`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira render [DIR\|FILE]` | Regenerate `.md` views offline | `--render-profile`, `--render-include`, `--render-exclude`, `--into` |
| `jira apply <FILE.md>` | Merge/stage supported generated edits | `--dry-run`, `--allow-loss`, `--rebase-pending`, `--into`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira snapshot [DIR]` | Exact content-free mirror health cardinalities | `--remote` |
| `jira status [DIR]` | Show local edits and optional remote drift | `--remote` |
| `jira push <file.wiki\|DIR>` | Preview or apply guarded write-back | `--apply`, `--force`, `--into` |
| `jira export` | Write compact JSONL/JSON/CSV plus manifest, or artifact-only stdout with `--out -`; explicit ids/keys keep selector order and omit missing rows | `--jql`/`--ids`/`--keys`, `--out`, `--format`, `--limit`, `--fields` ids/names, `--batch-size`, `--raw-csv` |
| `jira export diff <OLD> <NEW>` | Compare compact exports | — |
| `jira planning report` | Deterministic planning quality report | `--jql`, `--require`, `--estimate-field`, `--epic-field`, `--limit`, `--csv`, `--raw-csv` |
| `jira quality-report` | Compatibility alias | same flags as `planning report` |
| `jira fields` | List a qualified value-free Jira field catalog | `--name-like`, `--id`, `--id-like`, `--schema`, `--custom true|false`, `-o text` |
| `jira field-options` | List allowed field values | `--project`, `--type`, `--field`, `-o text` |
| `jira transitions` | List available transitions | `--key`, `-o text` |
| `jira link-types` | List issue link types | `-o text` |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users | `--limit` |
| `jira user get <USERNAME>` | Get a user | — |
| `jira board list` | Discover Agile boards | `--project`, `--limit`, `--cursor`, `-o id` |
| `jira board get <ID>` | Get board identity | `-o id` |
| `jira board config <ID>` | Get filter, ordered columns/statuses, constraints, estimation, rank | `-o text/id` |
| `jira board issues <ID>` | Read one backend-ranked IssueList page | `--view`, `--columns`, `--jql`, `--limit`, `--cursor`, `-o text/id` |
| `jira board backlog <ID>` | Read one Scrum backlog IssueList page | `--view`, `--columns`, `--jql`, `--limit`, `--cursor`, `-o text/id` |
| `jira board view <ID>` | Read normalized config/issues/backlog snapshot | `--scope all/board/backlog`, `--view`, `--columns`, `--jql`, `--limit`, `-o text/id` |
| `jira board export <ID>` | Write normalized board artifact | `--scope`, `--view`, `--columns`, `--jql`, `--limit`, `--format json/jsonl/csv/md`, `--out`, `--raw-csv` |
| `jira sprint issues <ID>` | Read one sprint IssueList page | `--view`, `--columns`, `--limit`, `--cursor`, `-o text/id` |
| `jira structure get <ID>` | Get Structure metadata | `-o id` |
| `jira structure view <ID>` | Read normalized hierarchy + Jira fields | exact folder selector or fuzzy `--root`, `--view`, `--fields`, `--batch-size`, `-o text/id` |
| `jira structure forest <ID>` | Get raw latest Structure forest formula | — |
| `jira structure rows <ID>` | Parse Structure forest rows | exact folder selector or fuzzy `--root`, `--root-fields`, `-o id` |
| `jira structure folders <ID>` | Discover stable stored folders, paths, and subtree statistics without Jira issue reads | `-o text/id` |
| `jira structure values <ID>` | Get row values | `--rows`, `--fields` |
| `jira structure pull-issues <ID>` | Fetch snapshots from Structure rows | exact folder selector or fuzzy `--root`, `--view`, `--fields`, `--batch-size`, `--limit`, `--out`, `-o id` |
| `jira structure export <ID>` | Write a normalized offline Structure artifact | exact folder selector or fuzzy `--root`, `--view`, `--fields`, `--format json/jsonl/csv/md`, `--out`, `--raw-csv` |
