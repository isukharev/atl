---
name: jira
description: Search, pull, read, and edit Jira issues with the atl CLI — search by JQL, mirror issues locally, and create/update/transition/comment/link/delete issues and epics. USE WHEN the user wants to read, search, create, update, transition, comment on, link, delete, check fields of, or report on a Jira issue, ticket, bug, story, epic, or task; add/remove labels; view issue history or changelog; look up users; run a JQL query; find out who is logged in; check required fields before transitioning; download images from an issue; work with agile boards and sprints; or read Tempo Structure metadata, forest rows, and values.
---

# Jira issues with `atl`

Read Jira issues as local files; **change them only through commands** (there is no file→push path
for Jira). `atl` prints JSON by default. Issue keys are **positional** arguments
(`atl jira issue get PROJ-1`), except the meta command `atl jira transitions --key PROJ-1`.

**Output modes:** `-o id` prints just the primary identifiers (issue keys or comment/link IDs) one
per line, suitable for piping into `xargs` or scripts. `-o text` gives a human-readable view.
`--verbose` (or `ATL_VERBOSE=1`) traces every HTTP request/response to stderr; the bearer token is
never written to the trace.

**Preflight:** `atl` must be installed and configured (Jira URL + PAT). If `command -v atl` fails or
`atl config show` has an empty `jira_url` (or any command exits `7` = "not configured"), **run
`/atl:setup` and stop** instead of pushing on. The mirror root is `~/.atl/<workspace>/`; when the
workspace exported `ATL_MIRROR_ROOT`, `jira pull --into` already defaults to it.

## The canonical loop

### 1. Find issues
```bash
atl jira issue search --jql '<JQL>' --limit 50
```
→ `{ "issues": [ {key, summary, status, type, project, assignee, labels} ], "next_cursor": "<startAt>" }`
(`--jql` required; default `--limit` 50; paginate with `--cursor <next_cursor>`; slim output with
`--fields summary,status`.) See [jql.md](reference/jql.md).

### 2. Pull issues you'll work with
```bash
atl jira pull --jql '<JQL>' --into ~/.atl/<workspace>/ --limit 0 [--fields customfield_10001,customfield_10002]
```
(`--limit 0` = all; default 100.) → `{ "into": "...", "issues": [ {key, path} ] }`

On disk per issue (both are **read-only snapshots**, regenerated on pull):
```
<root>/<PROJECT>/<KEY>.md     # YAML frontmatter + wiki body + links + comments
<root>/<PROJECT>/<KEY>.json   # {key,id,fields:{...}}; raw Jira fields live under .fields
```

For compact analysis artifacts instead of a directory mirror:
```bash
atl jira export --jql '<JQL>' --format jsonl --out issues.jsonl [--fields customfield_10001]
atl jira export --jql '<JQL>' --format csv --out issues.csv
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 --out selected.jsonl
atl jira export diff old.jsonl new.jsonl
```
This writes the artifact plus `<out>.manifest.json`; the manifest includes query/fields/count and a
backend URL hash, never the backend hostname or PAT. Use `--ids`/`--keys` when the CLI should build
safe batched `id in (...)` / `key in (...)` JQL instead of hand-editing a long query.

### 3. Read for context
Read `<KEY>.md` (human view) and `<KEY>.json` (raw fields) to ground your work.

### 4. Edit — via commands only
**There is no version gate (last-writer-wins). Run `atl jira issue get <KEY>` immediately before an
update** to avoid overwriting someone else's change.

```bash
atl jira issue get PROJ-1 [--fields summary,description,status]
# → {key, summary, status, type, project, assignee, reporter, description, labels, links, comments}

atl jira issue create --project PROJ --type Bug --summary 'Title' --from-file desc.wiki [--field k=v]
atl jira issue update PROJ-1 [--summary 'New'] [--from-file desc.wiki] [--field k=v]   # see fields.md for big bodies
atl jira issue transition PROJ-1 --to 'In Progress' [--comment 'why'] [--field k=v]    # list first ↓
atl jira issue comment add PROJ-1 --from-file comment.wiki                              # BREAKING: was comment PROJ-1
atl jira issue comment list PROJ-1
atl jira issue comment delete PROJ-1 <COMMENT-ID>
atl jira issue link add PROJ-1 --to PROJ-2 --type blocks                                # BREAKING: was link PROJ-1
atl jira issue link list PROJ-1
atl jira issue link delete <LINK-ID>
atl jira issue link-epic PROJ-1 --epic PROJ-100
atl jira issue labels PROJ-1 --add bug,backend [--remove wontfix]
atl jira issue history PROJ-1                                                           # changelog
atl jira issue check PROJ-1 [--require assignee,fixVersions] [--warn priority]         # non-zero if required empty
atl jira issue delete PROJ-1 --force                                                    # PERMANENT on DC; no trash
```

### 5. Discover valid values before writing
```bash
atl jira fields                                              # {fields:[{id,name,custom}]}
atl jira fields --name-like Epic
atl jira fields --id customfield_10001
atl jira field-options --project PROJ --type Bug --field priority   # {options:[...]}
atl jira transitions --key PROJ-1                            # {transitions:[{id,name,to}]}
atl jira link-types                                          # {link_types:[...]}
atl jira me                                                  # authenticated user
atl jira user search 'alice'                                 # find users by name/username
atl jira user get alice                                      # get a user by DC username
```
See [fields.md](reference/fields.md) for the discovery flow and the **large-description / epic edit
pattern** (edit a wiki body as a file, then `update --from-file`).

### 6. Images for vision
```bash
atl jira issue images PROJ-1 --into /tmp/proj1-images   # {key, images:[paths]}
```
Open the downloaded images when a screenshot/diagram matters.

### 7. Boards & sprints (Jira Software only)
Backed by the Data Center Agile API; boards/sprints are addressed by **numeric id**.
Typical flow: find the board for a project, list its sprints, then read or move issues.
```bash
atl jira board list --project PROJ                 # {boards:[{id,name,type,project_key}]} — id feeds --board
atl jira board get 5
atl jira sprint list --board 5 [--state active]    # {sprints:[{id,name,state,...}]}; state: active|closed|future
atl jira sprint current --board 5                  # the active sprint (exit 4 if none)
atl jira sprint issues 7 [--fields summary,status] # issues in sprint 7; -o id → just the keys
atl jira sprint add 7 PROJ-1 PROJ-2                # move issues into sprint 7
atl jira sprint remove PROJ-1                       # move issue(s) back to the backlog
```
These need Jira **Software** (GreenHopper); on a Core/Service-Management-only instance the
Agile endpoints 404 (exit 4). Use `board list --project` to discover the id `--board` wants.

### 8. Tempo Structure (read-only)
Backed by the Structure plugin API (`/rest/structure/2.0/`). Structures and rows are numeric ids.
Use this to inspect a structure tree and fetch selected attributes; there are no writeback commands.
```bash
atl jira structure get 123
atl jira structure forest 123
atl jira structure rows 123                                      # parsed hierarchy; -o id → row ids
atl jira structure values 123 --rows 100,101 --fields key,summary,status
```
`rows` reports `{structure_id,version,rows:[{row_id,depth,parent_row_id,item_type,item_id}]}`.
`values` preserves the backend matrix in `responses`/`raw` and exposes `inaccessible_rows` when
the server reports permission gaps. If the plugin or object is unavailable, expect exit 4/6.

## Quick Reference — all `jira` commands

| Command | What it does | Key flags |
|---|---|---|
| `jira issue get <KEY>` | Get an issue | `--fields` |
| `jira issue search` | Search issues by JQL | `--jql`, `--fields`, `--limit`, `--cursor` |
| `jira issue search -o id` | Print matching issue keys one per line | `-o id` |
| `jira issue create` | Create an issue | `--project`, `--type`, `--summary`, `--from-file`, `--field k=v` |
| `jira issue update <KEY>` | Update summary/description/fields | `--summary`, `--from-file`, `--field k=v` |
| `jira issue transition <KEY>` | Transition to a status | `--to`, `--comment`, `--field k=v` |
| `jira issue check <KEY>` | Audit required/important fields; non-zero exit if required field empty | `--require fields`, `--warn fields` |
| `jira issue delete <KEY>` | Permanently delete (DC has no trash) | `--force`, `--delete-subtasks` |
| `jira issue labels <KEY>` | Add/remove labels | `--add labels`, `--remove labels` |
| `jira issue history <KEY>` | Show issue changelog (who changed what, when) | — |
| `jira issue comment add <KEY>` | Add a comment | `--from-file` |
| `jira issue comment list <KEY>` | List comments | — |
| `jira issue comment delete <KEY> <ID>` | Delete a comment | — |
| `jira issue link add <KEY>` | Link an issue to another | `--to KEY2`, `--type blocks` |
| `jira issue link list <KEY>` | List links with ids | — |
| `jira issue link delete <LINK-ID>` | Delete a link by id | — |
| `jira issue link-epic <KEY>` | Set the Epic Link | `--epic EPIC-KEY` |
| `jira issue images <KEY>` | Download image attachments (agent vision) | `--into DIR` |
| `jira pull` | Export issues to disk (.md + .json) | `--jql`, `--into`, `--limit`, `--fields` |
| `jira export` | Write one compact JSONL/JSON/CSV artifact plus manifest | `--jql`/`--ids`/`--keys`, `--out`, `--format`, `--limit`, `--fields`, `--batch-size` |
| `jira export diff <OLD> <NEW>` | Compare compact exports | — |
| `jira fields` | List Jira fields | `--name-like`, `--id` |
| `jira field-options` | List allowed values for a field | `--project`, `--type`, `--field` |
| `jira transitions` | List available transitions for an issue | `--key` |
| `jira link-types` | List issue link types | — |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users by name/username | `--limit` |
| `jira user get <USERNAME>` | Get a user by DC username | — |
| `jira structure get <ID>` | Get Structure metadata | `-o id` |
| `jira structure forest <ID>` | Get raw latest Structure forest formula | — |
| `jira structure rows <ID>` | Parse Structure forest rows | `-o id` |
| `jira structure values <ID>` | Get row attribute values | `--rows`, `--fields` |

## Common Errors

| Symptom / Exit | Likely cause | Remedy |
|---|---|---|
| Exit 7 from any command | Backend URL or PAT not configured | Run `/atl:setup` (or `atl config set` + `atl auth login`) |
| Exit 3 | Token rejected (expired/revoked/wrong instance) | Re-run `atl auth login --service jira` with a valid PAT |
| Exit 4 | Issue key doesn't exist or isn't visible | Verify the key; the issue may have been deleted |
| Exit 6 | Token lacks permission | Surface to the user; they may need a broader-scoped PAT |
| `jira issue check` exits non-zero | A field in `--require` is empty | Populate the missing fields, then retry `check` before transitioning |
| `comment <KEY>` returns "unknown command" | Breaking change: subcommand restructured | Use `comment add <KEY>` or `comment list <KEY>` instead |
| `link <KEY>` returns "unknown command" | Breaking change: subcommand restructured | Use `link add <KEY>` or `link list <KEY>` instead |
| Transition rejected by Jira | Status name not available from current state | Run `jira transitions --key PROJ-1` first to see valid transitions |
| Field value rejected | Field option doesn't exist for this project/type | Run `jira field-options --project PROJ --field <field>` to list valid values |
| Structure command exits 4/6 | Structure plugin/object unavailable or token lacks permission | Verify the numeric id and permissions; commands are read-only |

## Hard rules
- **Never edit `<KEY>.md` / `<KEY>.json` to change an issue** — they are read-only snapshots;
  changes go through the commands above.
- Structure commands are read-only inspection tools; do not infer that `atl` can write Structure data.
- No version gate → always `get` right before `update`.
- Before setting a status, field value, or link type, confirm it exists (`transitions`,
  `field-options`, `link-types`) — Jira rejects unknown names.
