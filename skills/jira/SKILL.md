---
name: jira
description: Search, pull, read, and edit Jira issues with the atl CLI — search by JQL, mirror issues locally, and create/update/transition/comment/link issues and epics. Use when the user wants to read, search, create, update, transition, comment on, link, or report on a Jira issue, ticket, bug, or epic, or run a JQL query.
---

# Jira issues with `atl`

Read Jira issues as local files; **change them only through commands** (there is no file→push path
for Jira). `atl` prints JSON by default. Issue keys are **positional** arguments
(`atl jira issue get PROJ-1`), except the meta command `atl jira transitions --key PROJ-1`.

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
atl jira issue transition PROJ-1 --to 'In Progress' [--comment 'why']                  # list first ↓
atl jira issue comment PROJ-1 --from-file comment.wiki
atl jira issue link PROJ-1 --to PROJ-2 --type blocks                                    # types ↓
atl jira issue link-epic PROJ-1 --epic PROJ-100
```

### 5. Discover valid values before writing
```bash
atl jira fields                                              # {fields:[{id,name,custom}]}
atl jira field-options --project PROJ --type Bug --field priority   # {options:[...]}
atl jira transitions --key PROJ-1                            # {transitions:[{id,name,to}]}
atl jira link-types                                          # {link_types:[...]}
```
See [fields.md](reference/fields.md) for the discovery flow and the **large-description / epic edit
pattern** (edit a wiki body as a file, then `update --from-file`).

### 6. Images for vision
```bash
atl jira issue images PROJ-1 --into /tmp/proj1-images   # {key, images:[paths]}
```
Open the downloaded images when a screenshot/diagram matters.

## Hard rules
- **Never edit `<KEY>.md` / `<KEY>.json` to change an issue** — they are read-only snapshots;
  changes go through the commands above.
- No version gate → always `get` right before `update`.
- Before setting a status, field value, or link type, confirm it exists (`transitions`,
  `field-options`, `link-types`) — Jira rejects unknown names.
