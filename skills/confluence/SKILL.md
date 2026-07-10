---
name: confluence
description: Pull, read, edit, validate, and push Confluence pages with the atl CLI in their native storage format (CSF). USE WHEN the user wants to read, search, summarize, edit, update, create, publish, copy, open, or delete a Confluence or wiki page; list or upload page attachments; add or list page comments; browse a space tree or list pages in a space; work with .csf / storage-format content; check page history or metadata.
---
<!-- Generated from skills-src/confluence/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Confluence pages with `atl`

Edit Confluence pages on disk and push under a version gate: edit the **`.md`** view and merge
with `conf apply` (preferred), or edit the native **`.csf`** bytes directly for what md can't
express. `atl` prints JSON by default.

## Before the first command (preflight)

`atl` must be installed **and** configured (Confluence URL + PAT). Check once at the start of a
session; if either is missing, **run `/atl:setup` and stop** rather than letting a command fail with
an obscure error:

```bash
command -v atl >/dev/null || echo 'NOT INSTALLED → run /atl:setup'
atl config show   # confluence_url must be non-empty; exit 7 from any command also means "not set up"
```

The recommended workspace convention is `~/.atl/<workspace>/`, but the CLI does
not invent that path. Export `ATL_MIRROR_ROOT` or pass `--into`; otherwise the
built-in Confluence fallback is `mirror`. An explicit `--into` still wins. See
the `atl` skill's workflow reference for the rationale.

If a workflow profile exists, load only `preferences` and `render_defaults` before the first
render or mirror operation. Compare the relevant Confluence render defaults with `atl config show`;
run that check from the target mirror root when local render config is intended. A saved
`mirror_root` is memory, not an environment change. Compare it with the active root from `atl config
show`: if they differ, present the conflict and ask which wins; if no root is active, preview
explicit `--into <absolute-root>` and obtain separate approval before using the saved value. Expand
`~` without `eval`, then pass the absolute path as one shell-quoted argument. A previously declined
sync stays declined until the user approves it. A cleared memory preference does not reset runtime.
Never silently edit shell/workspace configuration or assume that saved render defaults are active.
When the user supplies an existing mirror file, its nearest `.atl` root is
authoritative for that edit/apply/push. A different selected root requires a
fresh pull there before editing.

## The canonical loop

### 1. Find the pages
```bash
atl conf search --cql '<CQL>' --limit 25
# or use convenience filters (do not combine them with --cql):
atl conf search --space <KEY> --title '<substring>' --type page --limit 25
```
→ `{ "results": [ {id, title, space, version, excerpt, url} ], "next_cursor": "<offset>" }`
(Either `--cql` or at least one of `--space`/`--title`/`--label`/`--type` is
required; default `--limit` is 25; paginate with `--cursor <next_cursor>`.)

For a whole space's hierarchy: `atl conf space tree --space <KEY> [--depth N]`.

### 2. Pull only what you need
```bash
atl conf pull --id <id> --assets --into ~/.atl/<workspace>/
# or:  --cql '<CQL>'   (caps at 1000 pages — see warning below)
# or:  --space <KEY>   (caps at 2000 pages; --depth N to limit)
# add: --comments       to mirror page comments as sidecar files
# add: --render-profile full   for a richer .md view (frontmatter + comments)
```
→ `{ "root": "...", "pages": [ {id, title, path, version, assets} ] }`
(`comments` count added per page when `--comments` is used)

> ⚠️ **`--cql` stops at 1000 pages, `--space` at 2000.** When either cap is hit, the result
> includes `"truncated": true, "truncated_at": N` and `atl` prints a `warning:` line to
> stderr — the remaining matches are **not** mirrored. If you see this, narrow the selection
> instead of assuming you got everything. `conf space tree` reports the same cap via
> `"truncated": true`.

On disk per page:
```
<root>/<SPACE>/<ancestors…>/<page-slug>/
    <page-slug>.csf        # native CSF — source of truth; edit directly only as fallback
    <page-slug>.md         # markdown view — edit it, then `conf apply` (regenerated on pull/apply)
    <page-slug>.meta.json  # metadata + fragments (+ comments_pulled, comment_count) — auto-managed
    <page-slug>.comments.json  # only with --comments: [{id,author,created,body}]
    <page-slug>.comments.md    # only with --comments: derived human read view
    <page-slug>.assets/    # only with --assets: diagram/image renders (for vision)
<root>/.atl/               # sync baseline — do not edit, do not commit
```
Comments are auxiliary read-only data: they never affect dirty/drift/push, so a
page with comment sidecars still reads Clean in `conf status`. Bodies are
plain-text (CSF stripped). A re-pull with `--comments` refreshes the sidecars; a
re-pull without it leaves them untouched (never auto-deleted).
If two sibling titles slugify to the same name, the later-pulled page lands in an
id-suffixed dir (`<page-slug>-<id>/`) — same files inside, nothing overwritten.

**Render profiles** control the `.md` view (the `.csf` substrate is never
affected). `default`/`minimal` contain only the body plus generated version/body
boundaries — the standard, cheapest view. `full` adds YAML metadata (title,
space, version, labels) and, when
a `--comments` sidecar is present, appends a `## Comments` section. Set per run
with `--render-profile full` / `--render-include frontmatter,comments` /
`--render-exclude ...`, or persist with `atl config set render.confluence.profile
full`. Re-render an existing mirror offline (no network) after a profile change:
```bash
atl conf render mirror --render-profile full     # whole mirror
atl conf render mirror/DOCS/page/page.csf        # one page
```

If repeated work reveals a useful space, selector, or render preference, do not edit agent memory
silently. Offer the `onboarding` skill's consent-gated learning flow. Load only
`atl profile show --section schema --service confluence` or the corresponding `selectors` slice;
verified space metadata and proposed preferences go through
`profile suggest → suggestion review → apply|reject`. Use explicit
`profile revalidation status` before relying on stale or previously failed space knowledge. After
an applied render or mirror preference, separately compare it with `atl config show` and obtain
approval before synchronizing runtime; for local render config, compare from the target mirror
root, and verify explicit `--into` from the next approved command result rather than running a
side-effecting command only for verification.
→ `{ "root": "...", "rendered": [ {id, title, path} ] }`. Only `.md` files are
rewritten, so `conf status` stays clean.

### 3. Edit the `.md` view, merge with `conf apply`
The `.md` is an **editable surface**: make your edits there with normal text editing (it's
markdown — no invisible-byte traps), then merge them into the `.csf` block by block:

Before editing, the first line must be exactly
`<!-- atl:document confluence-page v1 -->`. If it is missing/older and the view
is pristine, run `conf render` against that exact file/root first. If edits
already exist, save a private reviewed patch outside the derived `.md`, render,
then reapply it; render rewrites `.md`. Keep the supplied file's nearest `.atl`
root throughout.
An unknown/future version is not a legacy view: stop and update `atl`; do not
render it with an older binary or downgrade the marker.

```bash
atl conf apply <…>/<page-slug>.md --dry-run  # review local merge/loss report
atl conf apply <…>/<page-slug>.md            # write reviewed merge to .csf
```

→ `{ "report": {unchanged, converted, moved, removed, merged_tables?, removed_fragments?}, "csf_ok", "wrote" }`
`"wrote": true` + `"csf_ok": true` **is** the success signal — no need to re-check exit codes
or grep the `.csf` afterwards. Untouched blocks keep their **exact** CSF bytes; opaque markers
(⟦…⟧, `[KEY](jira:KEY)`, `[[Page]]`, mentions) keep their identity — don't edit marker text,
but marker text may move between lines/cells. Tables — styled ones included — merge
**row/cell-wise**: edit cell text, add or delete whole rows in the md table and apply splices
them in place (untouched rows and cell styling keep their bytes; new rows clone a neighbor's
structure). **Exit 8** means apply refused, nothing was written:
- *"removes N opaque fragment(s)"* — you dropped a macro/mention/link; restore the marker, or
  re-run with `--allow-fragment-loss` if intentional.
- *"cannot convert edited block"* — that fragment (unrecognized wrapper, ambiguous mention,
  a table edit crossing a rowspan/colspan, column add/remove, nested table, copied macro
  cell) can't be edited via md: make that one edit on the `.csf` directly using
  the decision table in [csf.md](reference/csf.md) (`conf edit` / boundary-anchor splice).

For a change that mixes supported Markdown body edits with an unsupported
rowspan/column/nested-table edit, choose one complete plan before touching CSF:
either omit/revert the complex table edit, apply/validate/push the supported
body, fresh-pull, then do a separate CSF table cycle; or move the whole combined
change into one reviewed CSF cycle. Never direct-edit CSF while expecting
remaining `.md` edits to apply in the same cycle.

Every `.md` has reserved document/body boundaries. On a `full`-profile page it
also has generated YAML metadata and a `## Comments` section; all generated regions are
**read-only** — apply reproduces them from the recorded view (`.atl/state.json`) and merges only
the body between them (an untouched full page applies to a byte-identical `.csf`). Editing the
frontmatter or Comments is refused (exit 8) — use `conf page update`/`conf page move` or
`conf comment add`. `conf apply` takes no `--render-*` flags — it always uses the recorded view;
to change the view, re-run `conf render` with the desired settings (which re-records it).

Direct-`.csf` edits and the md surface don't mix in one cycle: once you edit the `.csf`
directly, apply refuses until the page is pushed or re-pulled. Use the `.assets/` images for
visual context. For **new content**: a whole new page takes a markdown body directly
(`conf page create --from-md body.md`); new sections in an existing page are markdown blocks
that apply converts; comments still need CSF — start from validated snippets in
[csf-authoring.md](reference/csf-authoring.md) — CSF is XHTML-based, **not Markdown**.

### 4. Validate
```bash
atl conf validate ~/.atl/<workspace>/<…>/<page-slug>.csf
```
→ `{ "file": "...", "ok": true|false, "problems": [ {severity, line, col, rule, message} ] }`
Block on any `severity: "error"` (exit is non-zero); `warning` is advisory.

### 5. Dry-run and review the diff
```bash
atl conf push --dry-run ~/.atl/<workspace>/<…>/<page-slug>.csf
```
→ `{ "items": [ {path, id, dry_run, remote_drifted, added_fragments, removed_fragments, new_version} ] }`
Check `removed_fragments` (did you drop a macro/diagram?) and `remote_drifted` before writing.

### 6. Push under the version gate
```bash
atl conf push ~/.atl/<workspace>/<…>/<page-slug>.csf
```
→ `{ "items": [ {pushed: true, new_version, …} ] }`
On **exit 5** (version conflict): the remote moved past your synced version. Re-pull and reconcile;
only `--force` after a human decides (it re-reads current and clobbers — last-writer-wins). See
[push.md](reference/push.md). **Never auto-`--force`.**

### Check state anytime
```bash
atl conf status ~/.atl/<workspace>/ --remote
```
→ `{ "entries": [ {path, id, title, locally_edited, synced_version, remote_version, remote_drifted, remote_error} ] }`
(`--remote` does one request per page to detect drift; omit it for a fast local-only view.)
Status and directory push scan fail closed with exit 8 when a mirrored `.csf`
has missing, corrupt, or id-less sibling metadata. Repair or re-pull that entry;
never treat a partial scan as clean or safe to push.
Pull/render/apply/push share one persistent per-mirror advisory lock. Exit 8
with "another Confluence mirror mutation is active" means wait for that process
to finish; do not delete the lock file or retry concurrently. Read-only status
does not take the mutation lock.

## Quick Reference — all `conf` commands

| Command | What it does | Key flags |
|---|---|---|
| `conf search` | Search pages by CQL or convenience filters | `--cql`, `--space`, `--title`, `--label`, `--type`, `--limit`, `--cursor` |
| `conf search -o id` | Print matching page IDs one per line (for piping) | `-o id` |
| `conf space tree` | Page hierarchy of a space | `--space KEY`, `--depth N` |
| `conf page list` | List pages in a space | `--space KEY`, `--status current\|archived\|trashed`, `--limit N` |
| `conf page get` | Print a page body (CSF or rendered view) | `--id`, `--format csf\|view` |
| `conf page meta` | Page metadata (version, ancestors, labels, restrictions) | `--id` |
| `conf page history` | List page versions | `--id` |
| `conf page open` | Open the page in the system browser | `--id` |
| `conf page create` | Create a page (markdown body via `--from-md`, or CSF via `--from-file`) | `--space`, `--title`, `--parent`, `--from-md`, `--from-file` |
| `conf page copy` | Copy a page (same CSF body, new title/space/parent) | `--id`, `--title`, `--space`, `--parent` |
| `conf page move` | Reparent a page | `--id`, `--parent` |
| `conf page delete` | Trash a page | `--id` |
| `conf pull` | Mirror pages to disk (.csf + .md + .meta.json + assets + comments) | `--id`, `--cql`, `--space`, `--assets`, `--comments`, `--into`, `--depth`, `--render-profile`, `--render-include`, `--render-exclude` |
| `conf render [DIR\|FILE]` | Regenerate `.md` views offline (no network/PAT) | `--render-profile`, `--render-include`, `--render-exclude`, `--into` |
| `conf status` | Show locally-edited and remote-drifted pages | `[DIR]`, `--remote` |
| `conf validate` | Validate CSF well-formedness | `<file.csf>` |
| `conf apply` | Merge `.md` edits into the `.csf` block-by-block (untouched blocks keep exact bytes) | `<page.md>`, `--dry-run`, `--allow-fragment-loss`, `--into` |
| `conf edit` | Replace text in a local file, tolerant of NBSP/invisible bytes; auto-validates `.csf` | `<file>`, `--old`, `--new`, `--old-file`, `--new-file`, `--all`, `--dry-run` |
| `conf push` | Validate + push under the version gate | `<file.csf\|DIR>`, `--dry-run`, `--force`, `--into` |
| `conf comment list` | List comments on a page | `--id` |
| `conf comment add` | Add a comment (CSF body) | `--id`, `--from-file` |
| `conf attachment list` | List attachments on a page | `--id` |
| `conf attachment get` | Download an attachment | `--id`, `--name`, `--version`, `--into` |
| `conf attachment upload` | Upload a file as a page attachment | `--id`, `--file`, `--comment` |
| `conf attachment delete` | Delete an attachment by id | `--id` |
| `conf me` | Print the authenticated Confluence user | — |

`conf table extract --format csv` neutralizes formula-leading cells by default.
Use `--raw-csv` only for a trusted non-spreadsheet consumer; the raw result is
unsafe to open in a spreadsheet. Attachment uploads stream the selected file
instead of buffering the complete multipart payload in memory.

**Note on `-o id` and `-o text`:** Any command that has an ID projection (search, page list, attachment list) supports `-o id` to print identifiers one per line for piping. All commands accept `-o text` for a human-readable view instead of JSON.

### `.md` internal links
The rendered `.md` view represents Confluence page links as `[[Title]]` — these are read-only markers; the underlying CSF has the proper `<ri:page>` element.
Tables in the `.md` view preserve ordinary links, pad `colspan`, repeat `rowspan`
values across covered rows, and mark colored spans as `⟦color:...⟧text⟦/color⟧`.
For exact edits or unresolved rendering questions, inspect the `.csf` source.

## Creating, moving, commenting
- **New page from markdown (preferred):**
  `atl conf page create --space <KEY> --title '<T>' [--parent <id>] --from-md body.md` —
  write the body in the same markdown subset the `.md` view uses (headings, lists, task
  lists, tables, fenced code, `> INFO:`/`> WARNING:` admonitions, `[[Page Title]]` links,
  `[KEY](jira:KEY)` issue links). Fail-closed: **exit 8** names the first unconvertible
  block and the page is NOT created — author that body as CSF instead.
- New page from CSF: `... --from-file body.csf` (mutually exclusive with `--from-md`;
  the body is validated; malformed CSF is rejected and the page is not created).
- **Authoring a CSF body from scratch** (constructs outside the md subset, comments, or a
  new section in an existing `.csf`): start from the validated snippets in
  [csf-authoring.md](reference/csf-authoring.md) — page skeleton,
  code/info/warning/expand/status/TOC macros, task lists, tables, page links, mentions.
- Copy a page: `atl conf page copy --id <id> --title 'New Title' [--space KEY] [--parent <id>]`.
- `atl conf page get --id <id> --format csf|view`, `atl conf page meta --id <id>`,
  `atl conf page history --id <id>`, `atl conf page move --id <id> --parent <id>`,
  `atl conf page delete --id <id>`, `atl conf page open --id <id>`.
- Comments: `atl conf comment list --id <id>` / `atl conf comment add --id <id> --from-file c.csf`.
- Attachments: `atl conf attachment list|get|upload|delete`.

## Common Errors

| Symptom / Exit | Likely cause | Remedy |
|---|---|---|
| Exit 7 from any command | Backend URL or PAT not configured | Run `/atl:setup` (or `atl config set` + `atl auth login`) |
| Exit 5 on push | Remote version moved past your synced version | Re-pull and reconcile; use `--force` only after a human decides |
| Exit 4 | Page ID doesn't exist or isn't visible | Verify the `--id`; the page may have been deleted or renamed |
| Exit 6 | Token lacks permission for this page/space | Surface to the user; they may need a broader-scoped PAT or access |
| Exit 3 | Token was rejected (expired/revoked/wrong instance) | Re-run `atl auth login --service confluence` with a valid PAT |
| Exit 2 + "not well-formed" on `page create` | CSF body has structural errors | Fix the CSF (`conf validate body.csf`) before retrying |
| Exit 8 on `conf apply` | Stale view format, reserved marker, unconvertible block, dropped fragments, or `.csf` divergence | Migrate a stale marker before editing; otherwise see step 3 and follow the named recovery |
| Exit 8 says another mirror mutation is active | Pull/render/apply/push already owns the mirror lock | Wait for it to finish; never remove the persistent lock file |
| Exit 8 on `page create --from-md` | A markdown block is outside the convertible subset (or the doc is empty) | The error names the block; author that body as CSF via `--from-file` ([csf-authoring.md](reference/csf-authoring.md)) |
| Exit 8 + "corrupt mirror sidecar" on `status`/`push`/`pull`/`apply` | `.atl/state.json` is unparseable (interrupted edit, disk issue) | Fix the JSON, or delete the file to reset sync state and re-pull (pages read as never-synced until then) |
| `conf search` requires `--cql` or filter | No query provided | Pass `--cql '<CQL>'` or at least one of `--space/--title/--label/--type` |

Tool friction that cost you real turns (repeated failures, misleading errors, unexpected
refusals)? Offer the user a report — see the `atl` skill's feedback flow (consent-gated
sanitized issue + private case file).

## Hard rules
- **Prefer the `.md` + `conf apply` path; drop to a direct `.csf` edit only as the fallback** — for
  what apply refuses or md can't express: an unconvertible block (exit 8 names it), a construct you
  must author outside the md subset, an intentional `--allow-fragment-loss`, or rowspan/colspan/
  nested-table restructuring. Never do both before a push — `.md` edits without an apply are lost on
  the next pull, and a direct `.csf` edit makes apply refuse until you push or re-pull; `.meta.json`
  is always auto-managed. CSF stays the byte-stable substrate: apply never converts blocks you
  didn't change.
- Validate before pushing; review the dry-run; push the exact bytes you reviewed.
