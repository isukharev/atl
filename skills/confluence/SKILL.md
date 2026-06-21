---
name: confluence
description: Pull, read, edit, validate, and push Confluence pages with the atl CLI in their native storage format (CSF). Use when the user wants to read, search, summarize, edit, update, create, or publish a Confluence or wiki page, or work with .csf / storage-format content.
---

# Confluence pages with `atl`

Edit Confluence by editing the page's **`.csf`** bytes on disk and pushing under a version gate.
`atl` prints JSON by default.

## Before the first command (preflight)

`atl` must be installed **and** configured (Confluence URL + PAT). Check once at the start of a
session; if either is missing, **run `/atl:setup` and stop** rather than letting a command fail with
an obscure error:

```bash
command -v atl >/dev/null || echo 'NOT INSTALLED → run /atl:setup'
atl config show   # confluence_url must be non-empty; exit 7 from any command also means "not set up"
```

The mirror lives at `~/.atl/<workspace>/` by default; if the workspace exported `ATL_MIRROR_ROOT`,
the commands below already default `--into`/the status dir to it, so you can omit `--into` (an
explicit `--into` still wins). See the `atl` skill's workflow reference for the rationale.

## The canonical loop

### 1. Find the pages
```bash
atl conf search --cql '<CQL>' --limit 25
```
→ `{ "results": [ {id, title, space, version, excerpt, url} ], "next_cursor": "<offset>" }`
(`--cql` is required; default `--limit` is 25; paginate with `--cursor <next_cursor>`.)

For a whole space's hierarchy: `atl conf space tree --space <KEY> [--depth N]`.

### 2. Pull only what you need
```bash
atl conf pull --id <id> --assets --into ~/.atl/<workspace>/
# or:  --cql '<CQL>'   (caps at 1000 pages — see warning below)
# or:  --space <KEY>   (caps at 2000 pages; --depth N to limit)
```
→ `{ "root": "...", "pages": [ {id, title, path, version, assets} ] }`

> ⚠️ **`--cql` stops at 1000 pages.** When the cap is hit, the result includes
> `"truncated": true, "truncated_at": 1000` and `atl` prints a `warning:` line to stderr — the
> remaining matches are **not** mirrored. If you see this, narrow the CQL or pull by `--space`
> instead of assuming you got everything.

On disk per page:
```
<root>/<SPACE>/<ancestors…>/<page-slug>/
    <page-slug>.csf        # native CSF — the ONLY editable file
    <page-slug>.md         # rendered view — READ-ONLY (regenerated every pull)
    <page-slug>.meta.json  # metadata + fragments — auto-managed
    <page-slug>.assets/    # only with --assets: diagram/image renders (for vision)
<root>/.atl/               # sync baseline — do not edit, do not commit
```

### 3. Read to locate, open the substrate to edit
Read the `.md` to understand and find the spot; **edit only the `.csf`**. Use the `.assets/` images
for visual context. See [csf.md](reference/csf.md) for fragments and what the bytes contain.

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

## Creating, moving, commenting
- New page: `atl conf page create --space <KEY> --title '<T>' [--parent <id>] --from-file body.csf`
  (the body is validated; malformed CSF is rejected and the page is not created).
- `atl conf page get --id <id> --format csf|view`, `atl conf page meta --id <id>`,
  `atl conf page history --id <id>`, `atl conf page move --id <id> --parent <id>`,
  `atl conf page delete --id <id>`.
- Comments: `atl conf comment list --id <id>` / `atl conf comment add --id <id> --from-file c.csf`.

## Hard rules
- **Edit only `.csf`.** The `.md` and `.meta.json` are regenerated on every pull — edits to them are
  lost. There is no Markdown round-trip; CSF is the byte-stable substrate.
- Validate before pushing; review the dry-run; push the exact bytes you reviewed.
