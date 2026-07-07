---
name: jira
description: Search, pull, read, and edit Jira issues with the atl CLI — search by JQL, mirror issues locally, and create/update/edit/transition/comment/link/delete issues and epics. USE WHEN the user wants to read, search, create, update, assign, transition, comment on, link, delete, check fields of, or report on a Jira issue, ticket, bug, story, epic, or task; extract artifact references; build an epic tree; add/remove labels; view issue history or changelog; look up users; run a JQL query; find out who is logged in; check required fields before transitioning; list or download issue attachments/images; work with agile boards and sprints; or read Tempo Structure metadata, forest rows, values, and issue exports.
---
<!-- Generated from skills-src/jira/SKILL.md — edit the source and run 'make gen-plugins'. -->

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
`$setup` and stop** instead of pushing on. The mirror root is `~/.atl/<workspace>/`; when the
workspace exported `ATL_MIRROR_ROOT`, `jira pull --into` already defaults to it.

Driving a ticket end-to-end while developing (assign → in progress → progress comments → check →
done → update the linked Confluence page)? Follow the `atl` skill's dev-loop reference
(`skills/atl/reference/dev-loop.md`).

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
atl jira pull --jql '<JQL>' --assets   # also mirror image attachments (opt-in)
```
(`--limit 0` = all; default 100.) → `{ "into": "...", "issues": [ {key, path, wiki_path, assets} ] }`
(`assets` and a top-level `assets_skipped` appear only with `--assets` and are omitted at zero.)

On disk per issue:
```
<root>/<PROJECT>/<KEY>.wiki    # native Jira wiki body, VERBATIM — the editable source of truth
<root>/<PROJECT>/<KEY>.md      # read-only rendered Markdown view (regenerated on pull; may be stale)
<root>/<PROJECT>/<KEY>.json    # {key,id,fields:{...}}; raw Jira fields live under .fields
<root>/<PROJECT>/<KEY>.assets/ # only with --assets: image attachments, linked from the .md
```
The `.wiki` holds the byte-for-byte native body (like a Confluence `.csf`); the `.md` is a
best-effort, lossy read view rendered from it and is regenerated on every pull. To change a body,
edit `<KEY>.wiki` (or a copy) and apply it with `jira issue update --from-file` — never the `.md`.

`--assets` streams each issue's `image/*` attachments into
`<KEY>.assets/<attachment-id>-<filename>` and adds a `## Image Attachments`
section to the `.md` (between description and links). It is best-effort: a failed
image is skipped, counted in `assets_skipped`, and warned about on stderr; the
issue is still written. Attachments with an empty or `application/octet-stream`
media type are skipped (same as `jira issue images`). The `.json` snapshot is
unchanged. For a single issue's images use `jira issue images <KEY>` instead.

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

**Keep live reads slim.** A bare `issue get` returns the full comment thread, attachments, and
links — expensive in context. For a first look use
`atl jira issue get <KEY> --fields summary,status,issuetype,project,labels,description,attachment`;
this keeps type/project/labels and attachment metadata visible without downloading attachment bytes
or pulling comments/links. Fetch the discussion deliberately with `comment list <KEY>` only when it
matters; and when you need just a few values, project them in the pipe (`… | jq -r '.status'`)
instead of reading the whole payload.

### 4. Edit — via commands only
**There is no version gate (last-writer-wins). Run `atl jira issue get <KEY>` immediately before an
update** to avoid overwriting someone else's change — a narrow
`--fields summary,description` is enough for that drift check; don't re-pull the comment thread.

```bash
atl jira issue get PROJ-1 [--fields summary,status,issuetype,project,labels,description,attachment]
# → {key, summary, status, type, project, description, labels, fields.attachment}

atl jira issue create --project PROJ --type Bug --summary 'Title' --from-md desc.md [--field k=v]
atl jira issue update PROJ-1 [--summary 'New'] [--from-md desc.md] [--field k=v]       # see fields.md for big bodies
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'                  # targeted description edit
atl jira issue assign PROJ-1 --me                                                       # or --to <username> / --none
atl jira issue transition PROJ-1 --to 'In Progress' [--comment 'why'] [--field k=v]    # list first ↓
atl jira issue comment add PROJ-1 --from-md comment.md                                  # BREAKING: was comment PROJ-1
atl jira issue comment list PROJ-1
atl jira issue comment delete PROJ-1 <COMMENT-ID>
atl jira issue link add PROJ-1 --to PROJ-2 --type blocks                                # BREAKING: was link PROJ-1
atl jira issue link list PROJ-1
atl jira issue link delete <LINK-ID>
atl jira issue link suggest --csv links.csv                                             # dry-run missing links; no writes
atl jira issue plan apply --csv plan.csv                                                # dry-run guarded plan
atl jira issue plan apply --csv plan.csv --apply --confirm APPLY --allow-ops link       # explicit write mode
atl jira issue link-epic PROJ-1 --epic PROJ-100
atl jira issue labels PROJ-1 --add bug,backend [--remove wontfix]
atl jira issue history PROJ-1                                                           # changelog
atl jira issue attachment list PROJ-1                                                   # all attachments; -o id → ids
atl jira issue attachment get PROJ-1 --id 42 --into ./attachments                       # any file type
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx                              # upload a local file
atl jira issue check PROJ-1 [--require assignee,fixVersions] [--warn priority]         # non-zero if required empty
atl jira issue refs PROJ-1                                                             # artifact refs from one issue
atl jira issue refs --jql 'project=PROJ' --limit 100                                   # artifact refs from a selection
atl jira issue tree --jql 'project=PROJ' --epic-field customfield_10001                # epic-to-child grouping
atl jira issue delete PROJ-1 --force                                                    # PERMANENT on DC; no trash
```

**Changing a description: prefer `issue edit` (one command).** It fetches, replaces
`--old` → `--new`, and writes back — no `get` before (the `--old` match doubles as the
drift check), no temp files (pass multiline `--new` directly with bash `$'...'`), and no
verify `get` after (the output prints the before/after region). The match must be unique —
ambiguous → exit 2 (add surrounding context or pass `--all`); no match → exit 4 with the
closest region quoted. Insert a new section by anchoring on the heading that should follow
it: `--old 'h2. Verify' --new $'h2. Rollback\n\nRestore the snapshot.\n\nh2. Verify'`.
Several independent edits = several `edit` commands. Delete text with `--new ''`; preview
with `--dry-run`. `--new` is **wiki markup**, spliced verbatim (matching tolerates
NBSP/invisible bytes). Reach for `update --from-md` only when most of the description
changes.

### 5. Discover valid values before writing
```bash
atl jira fields                                              # {fields:[{id,name,custom}]}
atl jira fields --name-like Epic
atl jira fields --id customfield_10001
atl jira fields --custom true --schema string --id-like customfield
atl jira field-options --project PROJ --type Bug --field priority   # {options:[...]}
atl jira transitions --key PROJ-1                            # {transitions:[{id,name,to}]}
atl jira link-types                                          # {link_types:[...]}
atl jira me                                                  # authenticated user
atl jira user search 'alice'                                 # find users by name/username
atl jira user get alice                                      # get a user by DC username
```
See [fields.md](reference/fields.md) for the discovery flow, the **field value shapes** `--field`
needs (object-typed fields take JSON, e.g. `priority={"name":"High"}`), and the
**large-description / epic edit pattern** (edit a body as a file, then `update --from-md` /
`--from-file`).

**Authoring bodies: write markdown, pass `--from-md` (preferred).** `create`, `update`, and
`comment add` all take `--from-md <file|->`: compose the body in ordinary markdown (headings,
lists, GFM tables, fenced code, `[KEY](jira:KEY)` issue links, `[~username]` mentions) and atl
converts it to wiki markup, escaping wiki-active characters in your prose automatically.
Short bodies (a comment, a couple of paragraphs) go through stdin in **one command** —
`printf '…' | atl jira issue comment add PROJ-1 --from-md -` — don't create a file for them;
use a `body.md` file only for long or multiline-heavy descriptions.
**Exit 8** means a block can't be converted (task lists, images, mid-word emphasis, pipes in
table cells) — the error names it; simplify that block, or write the body as raw wiki markup
via `--from-file` per [wiki-markup.md](reference/wiki-markup.md). Raw bodies are **Jira wiki
markup, not Markdown** — `**bold**` and ``` fences publish as literal characters there.

### 6. Attachments and images
```bash
atl jira issue attachment list PROJ-1                    # {key, attachments:[...]}; -o id → ids
atl jira issue attachment get PROJ-1 --id spec.xlsx --into ./attachments
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
atl jira issue images PROJ-1 --into /tmp/proj1-images   # {key, images:[paths]}
```
Use `attachment get` for any file type and `attachment upload` to add a local file to an issue.
Use `images` when you specifically want only image attachments for visual inspection; open the
downloaded images when a screenshot/diagram matters.

### 7. Planning quality reports
```bash
atl jira planning report --jql '<JQL>' \
  --estimate-field customfield_10001 \
  --epic-field customfield_10002 \
  --require fixVersions,components \
  --csv planning.csv
atl jira quality-report --jql '<JQL>'                       # compatibility alias
```
Reports deterministic `score`, `level`, `gaps`, extracted artifact `refs`, and epic `children`
when `--epic-field` is set. The rubric is rules-based only; no LLM scoring and no Jira writes.
Use `jira issue refs` when you only need extracted artifact links, and `jira issue tree` when
you only need normalized epic/child structure without scoring.

### 8. Boards & sprints (Jira Software only)
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

### 9. Tempo Structure (read-only)
Backed by the Structure plugin API (`/rest/structure/2.0/`). Structures and rows are numeric ids.
Use this to inspect a structure tree, fetch selected attributes, pull referenced issue snapshots, or write offline tree exports; there are no writeback commands.
```bash
atl jira structure get 123
atl jira structure forest 123
atl jira structure rows 123                                      # parsed hierarchy; -o id → row ids
atl jira structure rows 123 --root "release train"               # first matching subtree
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status        # issue snapshots by generated id in (...) JQL
atl jira structure export 123 --format json --out structure.json  # json|csv|md offline artifact
```
`rows` reports `{structure_id,version,rows:[{row_id,depth,parent_row_id,item_type,item_id}]}`.
`--root` matches row metadata first, then selected Structure values (`--root-fields`, default
`key,summary`), and returns the first matching row plus descendants.
`values` preserves the backend matrix in `responses`/`raw` and always exposes `inaccessible_rows`
(`[]` when the server reports no permission gaps). `pull-issues` emits `{structure_id,rows,issue_ids,issues,count}`;
`export` writes `json`, `csv`, or `md` and returns `{path,format,structure_id,row_count,issue_count}`.
If the plugin or object is unavailable, expect exit 4/6.

## Quick Reference — all `jira` commands

| Command | What it does | Key flags |
|---|---|---|
| `jira issue get <KEY>` | Get an issue | `--fields` |
| `jira issue search` | Search issues by JQL | `--jql`, `--fields`, `--limit`, `--cursor` |
| `jira issue search -o id` | Print matching issue keys one per line | `-o id` |
| `jira issue create` | Create an issue | `--project`, `--type`, `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue update <KEY>` | Update summary/description/fields (whole body) | `--summary`, `--from-md`, `--from-file`, `--field k=v` |
| `jira issue edit <KEY>` | Targeted description replace in one command | `--old`, `--new`, `--old-file`, `--new-file`, `--all`, `--dry-run` |
| `jira issue assign <KEY>` | Set or clear the assignee | exactly one of `--to USER`, `--me`, `--none` |
| `jira issue transition <KEY>` | Transition to a status | `--to`, `--comment`, `--field k=v` |
| `jira issue check <KEY>` | Audit required/important fields; non-zero exit if required field empty | `--require fields`, `--warn fields` |
| `jira issue delete <KEY>` | Permanently delete (DC has no trash) | `--force`, `--delete-subtasks` |
| `jira issue labels <KEY>` | Add/remove labels | `--add labels`, `--remove labels` |
| `jira issue history <KEY>` | Show issue changelog (who changed what, when) | — |
| `jira issue refs [KEY]` | Extract artifact references from one issue or JQL | `--jql`, `--fields`, `--limit` |
| `jira issue tree` | Build read-only epic-to-child grouping | `--jql`, `--epic-field`, `--fields`, `--limit` |
| `jira issue comment add <KEY>` | Add a comment | `--from-md`, `--from-file` |
| `jira issue comment list <KEY>` | List comments | — |
| `jira issue comment delete <KEY> <ID>` | Delete a comment | — |
| `jira issue link add <KEY>` | Link an issue to another | `--to KEY2`, `--type blocks` |
| `jira issue link list <KEY>` | List links with ids | — |
| `jira issue link delete <LINK-ID>` | Delete a link by id | — |
| `jira issue link suggest` | Read-only missing-link candidates from CSV | `--csv` |
| `jira issue plan apply` | Dry-run/apply guarded CSV operation plan | `--csv`, `--allow-ops`, `--allow-fields`, `--apply`, `--confirm APPLY` |
| `jira issue link-epic <KEY>` | Set the Epic Link | `--epic EPIC-KEY` |
| `jira issue attachment list <KEY>` | List issue attachments | `-o id` |
| `jira issue attachment get <KEY>` | Download an issue attachment | `--id ID-or-filename`, `--into DIR` |
| `jira issue attachment upload <KEY>` | Upload a local file as an issue attachment | `--file PATH` |
| `jira issue images <KEY>` | Download image attachments (agent vision) | `--into DIR` |
| `jira pull` | Export issues to disk (.md + .json) | `--jql`, `--into`, `--limit`, `--fields`, `--assets` |
| `jira export` | Write one compact JSONL/JSON/CSV artifact plus manifest | `--jql`/`--ids`/`--keys`, `--out`, `--format`, `--limit`, `--fields`, `--batch-size` |
| `jira export diff <OLD> <NEW>` | Compare compact exports | — |
| `jira planning report` | Deterministic planning quality report | `--jql`, `--require`, `--estimate-field`, `--epic-field`, `--limit`, `--csv` |
| `jira quality-report` | Compatibility alias for planning report | same flags |
| `jira fields` | List Jira fields | `--name-like`, `--id`, `--id-like`, `--schema`, `--custom true|false` |
| `jira field-options` | List allowed values for a field | `--project`, `--type`, `--field` |
| `jira transitions` | List available transitions for an issue | `--key` |
| `jira link-types` | List issue link types | — |
| `jira me` | Show the authenticated Jira user | — |
| `jira user search <Q>` | Search users by name/username | `--limit` |
| `jira user get <USERNAME>` | Get a user by DC username | — |
| `jira structure get <ID>` | Get Structure metadata | `-o id` |
| `jira structure forest <ID>` | Get raw latest Structure forest formula | — |
| `jira structure rows <ID>` | Parse Structure forest rows | `--root`, `--root-fields`, `-o id` |
| `jira structure values <ID>` | Get row attribute values | `--rows`, `--fields` |
| `jira structure pull-issues <ID>` | Fetch issue snapshots from Structure issue rows | `--root`, `--fields`, `--batch-size`, `--limit`, `--out`, `-o id` |
| `jira structure export <ID>` | Write an offline Structure tree artifact | `--root`, `--fields`, `--format json|csv|md`, `--out` |

## Common Errors

| Symptom / Exit | Likely cause | Remedy |
|---|---|---|
| Exit 7 from any command | Backend URL or PAT not configured | Run `$setup` (or `atl config set` + `atl auth login`) |
| Exit 3 | Token rejected (expired/revoked/wrong instance) | Re-run `atl auth login --service jira` with a valid PAT |
| Exit 4 | Issue key doesn't exist or isn't visible | Verify the key; the issue may have been deleted |
| Exit 6 | Token lacks permission | Surface to the user; they may need a broader-scoped PAT |
| `jira issue check` exits non-zero | A field in `--require` is empty | Populate the missing fields, then retry `check` before transitioning |
| `comment <KEY>` returns "unknown command" | Breaking change: subcommand restructured | Use `comment add <KEY>` or `comment list <KEY>` instead |
| `link <KEY>` returns "unknown command" | Breaking change: subcommand restructured | Use `link add <KEY>` or `link list <KEY>` instead |
| Transition rejected by Jira | Status name not available from current state | Run `jira transitions --key PROJ-1` first to see valid transitions |
| Field value rejected | Field option doesn't exist for this project/type | Run `jira field-options --project PROJ --field <field>` to list valid values |
| Structure command exits 4/6 | Structure plugin/object unavailable or token lacks permission | Verify the numeric id and permissions; commands are read-only |
| Exit 4 from `issue edit` | `--old` not found (text changed or hidden bytes) | Read the quoted closest-region in the error; re-check with `issue get --fields description` |
| Exit 2 from `issue edit` | `--old` matches more than once | Add surrounding context to make it unique, or pass `--all` |
| Exit 8 from `issue edit` | Match would cross a line break `--old` doesn't have | Copy `--old` exactly from the description, newlines included |

Tool friction that cost you real turns (repeated failures, misleading errors, unexpected
refusals)? Offer the user a report — see the `atl` skill's feedback flow (consent-gated
sanitized issue + private case file).

## Hard rules
- **Never edit `<KEY>.md` / `<KEY>.json` to change an issue** — the `.md` is a regenerated read
  view and `.json` is a raw snapshot. The native wiki body lives in `<KEY>.wiki` (the editable
  substrate); to change a body, edit `<KEY>.wiki` and apply it via `jira issue update --from-file`.
- **Author bodies in markdown via `--from-md`** (fail-closed conversion, exit 8 names any
  unconvertible block). Raw `--from-file` bodies are **Jira wiki markup, not Markdown**
  (`*bold*`, `h2.`, `{code}` — see [wiki-markup.md](reference/wiki-markup.md)); Markdown
  syntax pasted there publishes as literal characters.
- Structure commands are read-only inspection tools; do not infer that `atl` can write Structure data.
- No version gate → always `get` right before `update`. (`issue edit` checks implicitly:
  the `--old` match fails closed if the text moved.)
- Before setting a status, field value, or link type, confirm it exists (`transitions`,
  `field-options`, `link-types`) — Jira rejects unknown names.
- Use `jira issue link suggest --csv ...` before bulk link work; it is read-only and emits
  only missing candidates from `source,target,type[,rationale]` CSV rows.
- `jira issue plan apply` is dry-run unless both `--apply` and `--confirm APPLY` are passed.
  Keep `--allow-ops` and `--allow-fields` narrow; prefer checking the JSON report before writing.
