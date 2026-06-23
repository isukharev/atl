---
name: jira
description: Search, pull, read, and edit Jira issues with the atl CLI — search by JQL, mirror issues locally, and create/update/transition/comment/link/delete issues and epics. USE WHEN the user wants to read, search, create, update, transition, comment on, link, delete, check fields of, or report on a Jira issue, ticket, bug, story, epic, or task; add/remove labels; view issue history or changelog; look up users; run a JQL query; find out who is logged in; check required fields before transitioning; download images from an issue.
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
atl jira pull --jql '<JQL>' --into ~/.atl/<workspace>/ --limit 0
```
(`--limit 0` = all; default 100.) → `{ "into": "...", "issues": [ {key, path} ] }`

On disk per issue (both are **read-only snapshots**, regenerated on pull):
```
<root>/<PROJECT>/<KEY>.md     # YAML frontmatter + wiki body + links + comments
<root>/<PROJECT>/<KEY>.json   # the raw Jira `fields` map (e.g. .description, .status, …)
```

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
| `jira pull` | Export issues to disk (.md + .json) | `--jql`, `--into`, `--limit` |
| `jira fields` | List Jira fields | — |
| `jira field-options` | List allowed values for a field | `--project`, `--type`, `--field` |
| `jira transitions` | List available transitions for an issue | `--key` |
| `jira link-types` | List issue link types | — |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users by name/username | `--limit` |
| `jira user get <USERNAME>` | Get a user by DC username | — |

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

## Hard rules
- **Never edit `<KEY>.md` / `<KEY>.json` to change an issue** — they are read-only snapshots;
  changes go through the commands above.
- No version gate → always `get` right before `update`.
- Before setting a status, field value, or link type, confirm it exists (`transitions`,
  `field-options`, `link-types`) — Jira rejects unknown names.
