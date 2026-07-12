---
name: jira
description: Search, pull, read, and edit Jira issues with the atl CLI — search by JQL, mirror issues locally, and create/update/edit/transition/comment/link/delete issues and epics. USE WHEN the user wants to read, search, create, update, assign, transition, comment on, link, delete, check fields of, or report on a Jira issue, ticket, bug, story, epic, or task; extract artifact references; build an epic tree; add/remove labels; view issue history or changelog; look up users; run a JQL query; find out who is logged in; check required fields before transitioning; list or download issue attachments/images; work with agile boards and sprints; or read Tempo Structure metadata, forest rows, values, and issue exports.
---
<!-- Generated from skills-src/jira/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Jira issues with `atl`

Read Jira issues as local files. Two write paths exist: **one-shot field edits go through commands**
(`jira issue update`/`edit`/`transition`/…), and a description edited through the mirror pushes back
with **`jira push`** — edit the **`.md`** view and merge it with **`jira apply`** (recommended), or
edit the native **`.wiki`** for what md can't express (dry-run by default; see the write-back loop below).
`atl` prints JSON by default. Issue keys are **positional** arguments
(`atl jira issue get PROJ-1`), except the meta command `atl jira transitions --key PROJ-1`.

**Output modes:** `-o id` prints just the primary identifiers (issue keys or comment/link IDs) one
per line, suitable for piping into `xargs` or scripts. `-o text` gives a human-readable view.
`--verbose` (or `ATL_VERBOSE=1`) traces every HTTP request/response to stderr; the bearer token is
never written to the trace.

**Preflight:** `atl` must be installed and configured (Jira URL + PAT). If `command -v atl` fails or
`atl config show` has an empty `jira_url` (or any command exits `7` = "not configured"), **run
`/atl:setup` and stop** instead of pushing on. The recommended mirror
root is `~/.atl/<workspace>/`; export it as `ATL_MIRROR_ROOT` or pass `--into`
explicitly. Without either, Jira's built-in fallback is `mirror-jira`.

If a workflow profile exists, load only `preferences` and `render_defaults` before the first
render or mirror operation. Compare the relevant Jira render defaults with `atl config show`; run
that check from the target mirror root when local render config is intended. A saved `mirror_root`
is memory, not an environment change. Compare it with the active root from `atl config show`: if
they differ, present the conflict and ask which wins; if no root is active, preview explicit
`--into <absolute-root>` and obtain separate approval before using the saved value. Expand `~`
without `eval`, then pass the absolute path as one shell-quoted argument. A previously declined sync
stays declined until the user approves it. A cleared memory preference does not reset runtime.
Never silently edit shell/workspace configuration or assume that saved render defaults are active.
When the user supplies an existing mirror file, its nearest `.atl` root is
authoritative for that edit, apply, and push. Do not redirect it to a saved or
newly selected root; pull a fresh copy into the new root before editing there.

Driving a ticket end-to-end while developing (assign → in progress → progress comments → check →
done → update the linked Confluence page)? Follow the `atl` skill's dev-loop reference
(`skills/atl/reference/dev-loop.md`).

## The canonical loop

### 1. Find issues
```bash
atl jira issue search --jql '<JQL>' --limit 50
```
→ common `{schema_version,source,selection,projection,rows,page}` issue-list contract.
`--jql` is required; default `--limit` is 50; resume with
`--cursor <page.next_cursor>`. Select a narrow ordered projection with
`--columns key,summary,status`; use `-o text` for a Markdown table and `-o id`
for keys. See [jql.md](reference/jql.md).

For repeated projections, inspect `atl config show | jq '.jira_list_views'` and
use `--view default|full|<custom>`. Explicit `--columns`/Structure `--fields`
wins for one call. Never guess a custom name: an unknown/source-invalid view
fails before network. Add one only after user approval with `atl config set
jira.list_views.<name> '<JSON object>'`. The same catalog controls Confluence
JQL-macro tables through each preset's `confluence_macro` entry and `conf
pull|page view --jira-view <name>`; explicit columns stored in the macro still
win.

If a runtime command reports config exit 7 for `jira_list_views`, do not bypass
the catalog or guess a projection. Run `atl config show`, inspect
`jira_list_views_error` and the raw offending entry, then propose a repair. With
approval, replace it or remove a custom entry via `atl config set
jira.list_views.<name> null`; rerun `config show` before the Jira read.

For one epic, prefer the direct list over project-wide JQL:

```bash
atl jira issue children <EPIC-KEY> --columns key,summary,status,assignee
```

It resolves the epic field, pages without per-child reads, and returns the same
IssueList contract. Follow `page.next_cursor` when non-null.

### 2. Pull issues you'll work with

For a one-off read that will not be edited or cached, skip the mirror and use:

```bash
atl jira issue view <KEY> -o text [--render-profile full]
```

This fetches only the fields required by the configured Markdown view and
writes no files. Default JSON is `{key,markdown}`; `-o text` is raw Markdown.
It creates no baseline, so never edit it as a write surface — pull fresh first
if the task turns into an edit. `--render-root <root>` selects that root's
presentation-only local config without writing there. It does not download
image files; use `pull --assets` or `issue images` when vision is needed.

For work that needs editing, repeatable offline reads, or attachment files:

```bash
atl jira pull --jql '<JQL>' --into ~/.atl/<workspace>/ --limit 0 [--fields customfield_10001,customfield_10002]
atl jira pull --jql '<JQL>' --assets   # also mirror image attachments (opt-in)
atl jira pull --jql '<JQL>' --render-profile full   # richer .md view (see Render profiles)
```
(`--limit 0` = all; default 100.) → `{ "into": "...", "issues": [ {key, path, wiki_path, assets} ] }`
(`assets` and a top-level `assets_skipped` appear only with `--assets` and are omitted at zero.)
With the opt-in `epic_children` render section, epic rows also report a child
count and get a `<KEY>.epic-children.json` offline sidecar; capped results warn
and carry explicit truncation fields.

On disk per issue:
```
<root>/<PROJECT>/<KEY>.wiki    # native Jira wiki body, VERBATIM — the substrate; edit directly only as fallback
<root>/<PROJECT>/<KEY>.md      # rendered Markdown view — edit supported sections, then `jira apply` (regenerated on pull)
<root>/<PROJECT>/<KEY>.json    # {key,id,fields:{...}}; raw Jira fields live under .fields
<root>/<PROJECT>/<KEY>.assets/ # only with --assets: image attachments, linked from the .md
<root>/<PROJECT>/<KEY>.epic-children.json # only when epic_children is enabled for an epic
```
The `.wiki` holds the byte-for-byte native body (like a Confluence `.csf`); the `.md` is a
best-effort, lossy staging view rendered from it and is regenerated on every pull. To change supported content,
edit generated `# Description` and/or an opt-in editable field section, then run `jira apply` (the
recommended loop, see 4b), or edit `<KEY>.wiki` directly for what the md view can't express (see 4c);
then push with `jira push` (the write-back loop below). The pull also records a
sidecar + base copy so `jira status`/`jira push` can detect edits and drift; mirrors pulled by an
older `atl` have no sidecar and read as never-synced until re-pulled.

`--assets` streams each issue's `image/*` attachments into
`<KEY>.assets/<attachment-id>-<filename>` and adds a `# Image Attachments`
section to the `.md` (between description and links). It is best-effort: a failed
image is skipped, counted in `assets_skipped`, and warned about on stderr; the
issue is still written. Attachments with an empty or `application/octet-stream`
media type are skipped (same as `jira issue images`). The `.json` snapshot is
unchanged. For a single issue's images use `jira issue images <KEY>` instead.

**Render profiles** control what the `.md` view contains (the `.wiki`/`.json`
substrate is never affected): `minimal` (key/summary + description), `default`
(adds status/type/project/assignee/labels/priority/parent + attachments/links/
comments), `full` (everything: reporter, dates, resolution, due date, components,
fix versions, subtasks, non-image attachments, sprint, configured custom fields).
Set per run with `--render-profile` / `--render-include <sections>` /
`--render-exclude <sections>`, or persist with `atl config set render.jira.profile
full` (see the setup skill). `full` widens the pull's `fields=` so no extra fetch
is needed; the pull result JSON is unchanged by the profile. Re-render an existing
mirror offline (no network) after a profile change:
```bash
atl jira render                                  # whole mirror (default root)
atl jira render <root>/PROJECT/KEY.md --render-profile full
```

If repeated work reveals a useful Jira field id, selector, or render preference, do not edit agent
memory silently. Offer the `onboarding` skill's consent-gated learning flow. Load only
`atl profile show --section schema --service jira` or the corresponding `selectors` slice;
verified field metadata and proposed preferences go through
`profile suggest → suggestion review → apply|reject`. Use explicit
`profile revalidation status` before relying on stale or previously failed field knowledge. After
an applied render or mirror preference, separately compare it with `atl config show` and obtain
approval before synchronizing runtime; for local render config, compare from the target mirror
root, and verify explicit `--into` from the next approved command result rather than running a
side-effecting command only for verification.
→ `{ "root": "...", "rendered": [ {key, path} ] }`. Only `.md` files are rewritten,
so `jira status` stays clean.

For readable non-core fields, prefer typed `render.jira.field_views` over the
legacy id-only `custom_fields` list. A descriptor selects the Jira field `id`, a
human-readable `label`, `metadata|section` placement,
`auto|scalar|list|jira_wiki|date|datetime` format, optional `show_empty`, and
optional `editable`. Editing is valid only for `section` + `jira_wiki` and is
off by default.
Enable the `custom_fields` section (`full` already does). Example:

```bash
atl config set --local --into <root> render.jira.field_views '[{"id":"customfield_10003","label":"Risk Notes","placement":"section","format":"jira_wiki","editable":true}]'
atl config set --local --into <root> render.jira.include custom_fields,epic_children
atl config set --local --into <root> render.jira.epic_field customfield_10004
atl jira pull --jql '<JQL>' --into <root> --render-profile full
```

`epic_children` is not in any profile by default because it performs one extra
bounded related query per main-search page that actually needs it. It lazily
auto-detects `Epic Link` unless `epic_field` is configured; explicit field ids
also let returned children identify localized epic types. Date/datetime formats
normalize valid values, and metadata renders as a readable Markdown table.
Typed fields and epic children are recorded with the view. Typed fields remain
read-only unless explicitly editable; transient `jira issue view` is always
read-only. Editable values are staged under `.atl/pending/jira/`, while the raw
snapshot remains untouched until push succeeds. Offline `jira render` overlays
pending values; epic children use an identity-checked sidecar.
Generated epic-children and subtasks sections are safe embedded Markdown
tables, so scan columns directly instead of parsing legacy bullet prose.

For compact exports instead of a directory mirror, load the export section of
[reference/extended-capabilities.md](reference/extended-capabilities.md).

### 3. Read for context
Read `<KEY>.md` (human view) and `<KEY>.json` (raw fields) to ground your work.

If the issue was not pulled because this is a one-off read, use `jira issue
view <KEY> -o text`; use `issue get --fields ...` when you specifically need a
structured raw-field response rather than the configured human view.

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
atl jira issue create --project PROJ --type Bug --summary 'Title' --from-md desc.md [--field k=v]
atl jira issue update PROJ-1 [--summary 'New'] [--from-md desc.md] [--field k=v]       # see fields.md for big bodies
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'                  # targeted description edit
atl jira issue field set PROJ-1 --from-md customfield_10001=notes.md --allow-fields customfield_10001  # guarded dry-run
```
Load [reference/commands.md](reference/commands.md) only when another exact
command/flag lookup is needed.

Comment listing fails closed whenever a complete, stable listing cannot be
proven (including a page-guard hit or inconsistent pagination metadata). Treat
that error as an incomplete preflight; do not assume a matching comment is
absent or retry a write from a partial listing.

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

### 4b. Mirror write-back — edit the `.md` view, `jira apply`, then `jira push` (recommended)
For a structural description rewrite or an opted-in rich-text field update, edit the generated supported section of the pulled `<KEY>.md` view
with normal text editing (it's markdown — no wiki-markup or invisible-byte traps), fold it back into
the `.wiki` block-by-block with `jira apply` (the Jira analog of `conf apply`), then push:
```bash
atl jira apply PROJ-1.md --dry-run                   # report the merge, write nothing
atl jira apply <root>/PROJECT/PROJ-1.md              # merge/stage supported .md sections
atl jira apply PROJ-1.md --allow-loss                # intentional {panel}/{color}/mention/embed removal
atl jira apply PROJ-1.md --rebase-pending            # after fresh pull + explicit remote/local field review
atl jira status [<root>] [--remote]                  # what's locally edited / drifted; --remote = 1 GET/issue
atl jira push <root>/PROJECT/PROJ-1.wiki             # DRY-RUN by default: prints the diff, writes nothing
atl jira push --apply <root>/PROJECT/PROJ-1.wiki     # actually write it back
atl jira push --apply --force <file.wiki>            # write over a drifted remote (re-base + write)
atl jira push <root>/                                # a dir includes dirty .wiki and field-only pending issues
```
Before editing, inspect the first line. It must be exactly
`<!-- atl:document jira-issue v2 -->`. If it is v1, missing, or older and the view is
still pristine, run `jira render` against that exact file/root first. If edits
already exist, save a reviewed patch outside the derived `.md`, render, then
reapply the patch; render rewrites `.md`. Use the existing file's nearest
`.atl` root throughout, or fresh-pull into a newly approved root before editing.
If the marker has a future/unknown version, update `atl`; never render or
downgrade that view with the older binary. A directory render preflights all
selected view versions before rewriting any sibling, repeats the target check
under the mutation lock, and warns on stderr for every unreadable snapshot it
skips. Pull applies the same future-marker guard before changing that issue's
artifacts. A CRLF marker line is recognized, but other bytes remain significant.

This is the measured-cheaper edit surface (issue #88: fewer turns and a higher success rate than
hand-writing wiki markup). Untouched blocks keep their **exact base bytes**; changed/new blocks
convert through the same markdown subset as `--from-md`. Generated `# Description` and typed field
sections configured with `editable:true` + `jira_wiki` are editable. An edit to generated metadata,
title, or the Comments/Links/Image Attachments/read-only field sections is refused (exit 8)
with a pointer to the dedicated command (`issue update` / `comment add` / `link add` /
`attachment upload`). A wiki-only construct in the base (`{panel}`, `{color}`, `[~mention]`, `!embed!`,
a macro) dropped by the edit is listed in `removed_constructs` and refused (exit 8) unless
`--allow-loss`. A block it cannot convert, or a `.wiki` that diverged from the pulled base, also
refuses (exit 8) — edit the `.wiki` directly (4c), or push/re-pull first. Local only: `jira apply`
writes Description to `.wiki`, stages fields under `.atl/pending/jira/`, and refreshes `.md`;
the raw `<KEY>.json` stays unchanged. `jira push` still sends the reviewed set to the server. `apply`
reproduces the pristine view from the render settings the `.md` was written with (recorded on
pull/render), so no `--render-*` flags are needed — pass them only to override that recorded view.
→ `{ path, wiki_path, pending_path?, dry_run, report:{...}, fields?:[{id,pending,report}], wrote, warning? }`.

Jira has **no server-side version gate**, so `jira push` guards staleness with an app-layer
compare against the bases recorded at pull/apply. Description drift is refused with exit 8 unless
`--force`; pending-field drift is always refused, including with `--force`, and must be reconciled.
When Jira shares a mirror root with Confluence, both services merge sidecar
patches under the same short-lived state lock; wait on an exit-8 contention and
never remove or bypass that lock.
Description plus fields are one typed write. An ambiguous response is reconciled by a fresh end-state
read and never replayed. If remote already equals the proposal after refresh failure, retry repairs
local state without a write. A transport/local refresh problem is a warning;
a successful verification read that differs from the reviewed proposal retains
pending and exits 8. For genuine field drift: pull fresh, compare raw `<KEY>.json` remote value
with the local proposal still visible in `.md` (and any combined local Description preserved in
`.wiki`), edit if needed, then run `jira apply --rebase-pending`;
push fresh-checks the adopted base again. This remains an app-layer TOCTOU guard, never exit 5. Always run the dry-run first and read every diff;
`--apply` is required to write. Prefer `issue edit` for a small, surgical text change (§4).

### 4c. Fallback — edit `<KEY>.wiki` directly
Reach for the substrate when the md view can't carry the change: an **unconvertible block** (apply
exits 8 naming it), a **wiki-only construct you must author** (`{panel}`, `{color}`, a complex macro —
outside the md subset), an **intentional `--allow-loss`** removal, or **bulk restructuring** where
you'd rather work the raw bytes. Edit the pulled `<KEY>.wiki` in place and push the whole file back
with the same `jira status` / `jira push` commands as 4b. With no pending field
state this writes only Description. If fields are already pending, a direct
`.wiki` edit deliberately breaks their reviewed hash binding; inspect both edits
and run `jira apply --rebase-pending` to explicitly bind the pending fields to
that exact wiki before push:
```bash
atl jira status [<root>] [--remote]                 # what's locally edited / drifted
atl jira push <root>/PROJECT/PROJ-1.wiki            # DRY-RUN by default: prints the diff, writes nothing
atl jira push --apply <root>/PROJECT/PROJ-1.wiki    # actually write it back
atl jira push --apply --force <file.wiki>           # write over a drifted remote (re-base + write)
```
Once you edit the `.wiki` directly, `jira apply` refuses (exit 8 — the `.wiki` diverged from the
pulled base) until you push or re-pull. The sole exception is the explicit
`--rebase-pending` binding step above; it never merges a stale md Description.
The
same drift guard, dry-run default, and `--force` semantics from 4b apply. Prefer `issue edit` for a
small, surgical change; use this path when you've hand-authored most of the `.wiki`.

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

For attachment/image work, load the matching section of
[reference/extended-capabilities.md](reference/extended-capabilities.md).

### 7. Planning quality reports

For planning reports, load the matching section of
[reference/extended-capabilities.md](reference/extended-capabilities.md).

### 8. Boards & sprints (Jira Software only)

For Agile board/sprint work, including Kanban-vs-Scrum routing, normalized
column/status views, backlog capability, pagination, and exports, load the matching section of
[reference/extended-capabilities.md](reference/extended-capabilities.md).

### 9. Tempo Structure (read-only)

For read-only Structure work, including fast folder discovery, exact stable
folder selection, normalized Markdown/JSON/JSONL/CSV planning views, and the
distinction from browser saved views, load the matching section of
[reference/extended-capabilities.md](reference/extended-capabilities.md).

## Command reference

Read [reference/commands.md](reference/commands.md) when you need the complete
command/flag lookup. Keep the workflow and safety gates in this file in context.

## Error reference

Load [reference/errors.md](reference/errors.md) when a command fails. Preserve
the preflight and write-path safety gates from this file while troubleshooting.

Tool friction that cost you real turns (repeated failures, misleading errors, unexpected
refusals)? Offer the user a report — see the `atl` skill's feedback flow (consent-gated
sanitized issue + private case file).

## Hard rules
- **Do not treat a bare `<KEY>.md` edit as complete.** Edit generated Description or an
  explicitly editable field section, then run `jira apply`; pull/render
  may replace the derived staging view. `<KEY>.json` is a raw snapshot and is
  never an edit surface. Pending fields live under `.atl/pending/jira/`; the native body lives in `<KEY>.wiki`; edit it directly for
  what the md view can't express, then `jira push` (or `jira issue update --from-file`).
- **Transient `jira issue view` output is read-only context.** It has no synced
  base or drift guard; run `jira pull` before any mirror-based edit/apply/push.
- **Choose one guarded large-field path.** If the pulled view already records
  the field as an editable `section` + `jira_wiki`, edit it with Description and
  use the combined apply/push flow. Otherwise use `jira issue field set`, not an
  inline body value. Preview first, review normalized JSON plus `expected_updated` and aggregate
  `proposal_hash`, then repeat with the same files, exact `--allow-fields`, both
  expected gates, and `--apply`. A changed file must be previewed again. Only custom
  fields are accepted; Markdown always becomes a string, while raw top-level JSON
  objects/arrays stay structured. The 64 MiB aggregate cap is fail-closed. When
  Description and field are separate writes, push Description first, then
  preview/apply the field so the Jira `updated` gate is not invalidated.
- **Author bodies in markdown via `--from-md`** (fail-closed conversion, exit 8 names any
  unconvertible block). Raw `--from-file` bodies are **Jira wiki markup, not Markdown**
  (`*bold*`, `h2.`, `{code}` — see [wiki-markup.md](reference/wiki-markup.md)); Markdown
  syntax pasted there publishes as literal characters.
- Structure commands are read-only inspection tools; do not infer that `atl` can write Structure data. Prefer `structure folders` → `--folder-id`; use fuzzy `--root` only when fuzzy selection is explicitly intended.
- No version gate → always `get` right before `update`. (`issue edit` checks implicitly:
  the `--old` match fails closed if the text moved.)
- Before setting a status, field value, or link type, confirm it exists (`transitions`,
  `field-options`, `link-types`) — Jira rejects unknown names.
- For bulk link/plan work, load the guarded-plan section of
  [reference/extended-capabilities.md](reference/extended-capabilities.md).
