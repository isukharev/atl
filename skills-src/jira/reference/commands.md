# Jira command reference

Load this reference only when exact command or flag lookup is useful.

| Command | What it does | Key flags |
|---|---|---|
| `jira issue get <KEY>` | Get an issue | `--fields` |
| `jira issue view <KEY>` | Render one configured Markdown view without writing files | `-o text`, `--render-root`, `--render-profile`, `--render-include`, `--render-exclude` |
| `jira issue search` | Search issues by JQL | `--jql`, `--fields`, `--limit`, `--cursor` |
| `jira issue search -o id` | Print matching issue keys one per line | `-o id` |
| `jira issue create` | Create an issue | `--project`, `--type`, `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue update <KEY>` | Update summary/description/fields (whole body) | `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue field set <KEY>` | Guarded file-backed custom-field preview/apply | `--from-file FIELD=PATH`, `--from-md FIELD=PATH`, `--allow-fields`, `--expected-updated`, `--expected-proposal-hash`, `--apply` |
| `jira issue edit <KEY>` | Targeted description replace in one command | `--old`, `--new`, `--old-file`, `--new-file`, `--all`, `--dry-run` |
| `jira issue assign <KEY>` | Set or clear the assignee | exactly one of `--to USER`, `--me`, `--none` |
| `jira issue transition <KEY>` | Transition to a status | `--to`, `--comment`, `--field k=v` |
| `jira issue check <KEY>` | Audit required/important fields; non-zero exit if required field empty | `--require fields`, `--warn fields` |
| `jira issue delete <KEY>` | Permanently delete (DC has no trash) | `--force`, `--delete-subtasks` |
| `jira issue labels <KEY>` | Add/remove labels | `--add labels`, `--remove labels` |
| `jira issue history <KEY>` | Show issue changelog | — |
| `jira issue refs [KEY]` | Extract artifact references | `--jql`, `--fields`, `--limit` |
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
| `jira status [DIR]` | Show local edits and optional remote drift | `--remote` |
| `jira push <file.wiki\|DIR>` | Preview or apply guarded write-back | `--apply`, `--force`, `--into` |
| `jira export` | Write compact JSONL/JSON/CSV plus manifest | `--jql`/`--ids`/`--keys`, `--out`, `--format`, `--limit`, `--fields`, `--batch-size`, `--raw-csv` |
| `jira export diff <OLD> <NEW>` | Compare compact exports | — |
| `jira planning report` | Deterministic planning quality report | `--jql`, `--require`, `--estimate-field`, `--epic-field`, `--limit`, `--csv`, `--raw-csv` |
| `jira quality-report` | Compatibility alias | same flags as `planning report` |
| `jira fields` | List Jira fields | `--name-like`, `--id`, `--id-like`, `--schema`, `--custom true|false` |
| `jira field-options` | List allowed field values | `--project`, `--type`, `--field` |
| `jira transitions` | List available transitions | `--key` |
| `jira link-types` | List issue link types | — |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users | `--limit` |
| `jira user get <USERNAME>` | Get a user | — |
| `jira structure get <ID>` | Get Structure metadata | `-o id` |
| `jira structure view <ID>` | Read normalized hierarchy + Jira fields | `--root`, `--fields`, `--batch-size`, `-o text/id` |
| `jira structure forest <ID>` | Get raw latest Structure forest formula | — |
| `jira structure rows <ID>` | Parse Structure forest rows | `--root`, `--root-fields`, `-o id` |
| `jira structure values <ID>` | Get row values | `--rows`, `--fields` |
| `jira structure pull-issues <ID>` | Fetch snapshots from Structure rows | `--root`, `--fields`, `--batch-size`, `--limit`, `--out`, `-o id` |
| `jira structure export <ID>` | Write a normalized offline Structure artifact | `--root`, `--fields`, `--format json/jsonl/csv/md`, `--out`, `--raw-csv` |
