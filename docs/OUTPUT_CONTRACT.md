# `atl` Output Contract

This document is the authoritative reference for how `atl` communicates results and failures.
It is derived from `internal/cli/root.go` (`codeFor`, `emit`, `emitID`, `writeError`, exit constants).

---

## Output formats

`atl` accepts a global `-o` / `--output` flag (default `json`). The three modes:

| Mode | Flag | What is written to stdout |
|---|---|---|
| **json** | `-o json` (default) | Indented, HTML-unescaped JSON; one object per command |
| **text** | `-o text` | Human-readable plain text; only available for commands that supply a text renderer — commands without one fall back to JSON |
| **id** | `-o id` | Primary identifier(s) one per line (issue keys, page IDs, attachment IDs) — for safe piping into `xargs`. Only commands that register an id projection support this; others return exit 2 |

Shell completion for the three values is registered on the root flag.

### `emit()` — JSON / text output

`emit(cmd, v, textFn)` is the standard result renderer:

- With `-o json`: writes `v` as indented JSON to stdout. HTML escaping is disabled (`&`, `<`, `>` pass through literally).
- With `-o text` and a non-nil `textFn`: calls `textFn()` and writes the result to stdout.
- With `-o text` and a nil `textFn`: falls back to JSON (no text view defined for this command).
- With `-o id`: returns exit 2 (usage error) — use `emitID` for commands that export identifiers.

### `emitID()` — JSON / text / id output

`emitID(cmd, v, textFn, idsFn)` extends `emit` with an id projection:

- With `-o id`: calls `idsFn()` and prints each returned string on its own line. No JSON envelope.
- With `-o json` or `-o text`: delegates to `emit` (same rules as above).
- Commands that have no meaningful identifier set `ids = nil`; `emitID` then returns exit 2 for `-o id`.

### Error output

On failure `atl` writes to **stderr**, never stdout, so a piped JSON result on stdout is never
contaminated. The format follows `-o`:

- **`-o json` (default):** `{"error": "<message>", "code": <exit-code>}` (one JSON object, newline-terminated).
- **`-o text`:** `error: <message>`.

The `code` field in the JSON error object echoes the process exit code so a caller that captured only
stderr can still classify the failure without inspecting the exit code separately.

---

## Sentinel → exit-code matrix

Adapters wrap domain conditions as `fmt.Errorf("%w: ...", domain.ErrXxx)`. The CLI's `codeFor`
maps them via `errors.Is`:

| Exit code | Constant | Sentinel | Meaning |
|---|---|---|---|
| `0` | `exitOK` | — | Success |
| `1` | `exitGeneric` | (default) | Unexpected error; read the message |
| `2` | `exitUsage` | `domain.ErrUsage` | Bad flags/args; flag-parse errors are also mapped here |
| `3` | `exitAuth` | `domain.ErrAuth` | Server **rejected** the token (expired/revoked/wrong instance) |
| `4` | `exitNotFound` | `domain.ErrNotFound` | Resource does not exist or is not visible |
| `5` | `exitVersionConfl` | `domain.ErrVersionConflict` | Confluence push: remote moved past synced version |
| `6` | `exitForbidden` | `domain.ErrForbidden` | Authenticated but lacks permission for this object |
| `7` | `exitConfig` | `domain.ErrConfig` | Backend URL or PAT not set — setup incomplete |
| `8` | `exitCheckFailed` | `domain.ErrCheckFailed` | `jira issue check`: a `--require` field is empty (gate failed) |

### Practical notes

- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token configured);
  `3` = "the token you gave me was refused." React differently: `7` → run `/atl:setup`;
  `3` → replace the PAT via `auth login`.
- Codes `3` vs `6` are distinct: `3` = authentication failure (re-auth); `6` = authorization
  failure (the identity is known but lacks permission — surface to the user).
- **Only Confluence `push` uses the version gate** (`5`). Jira writes are last-writer-wins; `5` is
  never returned from Jira commands. `jira push` guards staleness with an app-layer
  compare-and-swap instead: a drift refusal is exit `8` (`ErrCheckFailed`), not `5`. A server-side
  HTTP 409 on a Jira write (locked issue, workflow veto) stays a generic conflict (exit `1`), also
  distinct from `5` (#66).
- `conf validate` exits non-zero (exit 1) when the CSF is not well-formed. Treat any `"error"`-
  severity problem in its `problems[]` array as a hard blocker before pushing.
- `jira issue check` exits `8` (`ErrCheckFailed`) when a field listed in `--require` is empty — a
  distinct code so a CI gate can tell "fields missing" from a transport/auth error. The full result
  (including `missing_required` and `missing_warn`) is still emitted to stdout before the exit.
- Flag-parse failures (unknown flag, bad value) are wrapped as `ErrUsage` → exit 2.
  This is enforced by a `SetFlagErrorFunc` on the root command, so it applies to every subcommand.

---

## `--verbose` / `ATL_VERBOSE=1`

When set, `httpx.SetTrace` attaches a request/response logger to stderr before any HTTP call.
The bearer token and query values are **never** written to the trace (query parameter names remain
visible with redacted values). stdout stays reserved for the result, so verbose output never
corrupts the JSON stream.

---

## Stable Snapshot Notes

`atl jira pull` writes three files per issue: `<KEY>.wiki` (the native Jira wiki body, byte-for-byte —
the editable substrate), `<KEY>.md` (a read-only Markdown view rendered from the wiki, regenerated
best-effort on every pull), and `<KEY>.json` (the raw-fields snapshot). The pull result's `path`
points at the `.md`; `wiki_path` points at the sibling `.wiki` substrate (edit that file, not the
`.md`). The JSON snapshot is an object with
stable identity at the top level and raw Jira fields under `fields`:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "fields": {
    "summary": "Issue summary"
  }
}
```

`--fields` on `jira pull` adds requested fields to that `fields` map; the command still includes the
core fields needed to render the markdown view and choose the project/key path.

The `jira pull` stdout summary is `{ "into": <root>, "issues": [ { "key", "path", "wiki_path", "assets", "epic_children" }, ... ] }`.
With `--assets`, each issue object gains an `assets` count of image attachments mirrored into
`<KEY>.assets/`, and the top-level result gains `assets_skipped` when some images could not be
downloaded. Both `assets` and `assets_skipped` are `omitempty`: a default (no `--assets`) pull, and a
`--assets` pull where nothing was skipped, produce the same shapes as before. The raw `<KEY>.json`
snapshot is never modified by `--assets` — it mirrors Jira's response and carries no local file paths.

When the opt-in `epic_children` render section is enabled, epic issue objects
gain an `epic_children` count (omitted at zero) and the mirror gains
`<KEY>.epic-children.json`:

```json
{
  "epic": "PROJ-1",
  "epic_field": "customfield_10001",
  "children": [
    {"key": "PROJ-2", "summary": "Implement capability", "status": "Open", "type": "Story"}
  ],
  "truncated": true,
  "truncated_at": 1000
}
```

`children` is always an array. The truncation fields are `omitempty`; when any
related query hits the cap, the top-level pull result also carries
`epic_children_truncated: true` and `epic_children_truncated_at: 1000`, and the
CLI warns on stderr. The sidecar is derived/offline-render data and never enters
the `.wiki` content hash or remote drift gate.

**Render profiles and typed field views do not otherwise change the `pull`
JSON.** Profiles and ordinary include/exclude sections only affect the derived
`.md`; `epic_children` is the explicit exception because it reports related-data
counts/truncation as described above. Unknown section names in an
include/exclude list produce a `warning:` line on **stderr** and are ignored —
never an error, never on stdout.

`atl jira render [DIR|FILE] [--render-*]` and `atl conf render [DIR|FILE]
[--render-*]` regenerate `.md` views offline (no network/PAT). `jira render` emits
`{ "root": <mirror-root>, "rendered": [ { "key", "path" }, ... ] }`; `conf render`
emits `{ "root", "rendered": [ { "id", "title", "path" }, ... ] }`, one entry per
rewritten `.md`. Both leave the `.csf`/`.wiki`/`.json` substrate and the sidecar
`pages` sync entries untouched (they record each view's render settings,
including typed field descriptors and the resolved epic field, in the
sidecar `views` map only, so a later `apply` can reproduce it), so `status` is
unchanged before and after. Render-resolution warnings go to **stderr**, never
stdout.

`atl jira status [DIR] [--remote]` emits `{ "entries": [ { "path", "key", "locally_edited",
"synced", "remote_drifted"?, "remote_error"? }, ... ] }`. `locally_edited` is true when the `.wiki`
differs from the pulled base; `synced` is false for a `.wiki` with no sidecar entry (never-synced —
it also reads `locally_edited`). `remote_drifted` (the remote description differs from the stored
base) and `remote_error` (the remote could not be checked) appear only with `--remote` and are
`omitempty`.

`atl jira push <file.wiki|DIR> [--apply] [--force] [--into ROOT]` emits `{ "items": [ ... ] }`, one
item per file: `{ "path", "key", "pushed", "dry_run"?, "skipped"?, "remote_drifted"?,
"drift_overridden"?, "diff"?, "failed"?, "warning"? }`. It is **dry-run by default**: without
`--apply`, `dry_run` is `true`, `pushed` is `false`, `diff` carries the unified diff of what the
write changes on the server (current remote → local body; equal to base → local when there is no
drift), and no write occurs. On drift without `--force` the item has `remote_drifted:true` and the
command exits `8` (`ErrCheckFailed`) — never `5`. `--force` sets `drift_overridden` and writes.
`--apply` sets `pushed:true`; a post-push mirror-refresh failure surfaces as `warning` on the item,
not an error. `skipped:"unchanged"` marks a clean file.

`atl jira apply <FILE.md> [--dry-run] [--allow-loss] [--into ROOT] [--render-*]` emits the same
shape as `conf apply` with `csf_path` replaced by `wiki_path`:
`{ "path", "wiki_path", "dry_run", "report": { "unchanged", "moved", "converted", "removed",
"removed_constructs"? }, "wrote", "warning"? }`. It is **local only** (no network). Each
`removed_constructs` entry is `{ "kind", "text" }` (`kind` ∈ `panel`, `color`, `mention`, `image`,
`monospace`, `link`, `macro`, …). The merge is fail-closed and exits `8` (`ErrCheckFailed`, nothing
written) on: an unconvertible edited block; a wiki-only construct dropped without `--allow-loss`
(the report still carries `removed_constructs` so the caller can see what would go); an edit to any
section other than `## Description` (the error names the section and its dedicated command); or a
local `.wiki` diverged from the last-synced base. Exit `4` (`ErrNotFound`) when the issue was never
pulled (no base/snapshot). On a successful write `wrote:true`; a failed `.md`-view refresh sets
`warning` and is not an error.

Both `conf apply` and `jira apply` also carry a `-o text` projection — a compact loss-review
(first line dry-run/applied, `blocks:` counts, `removed fragments:`/`removed constructs:` and
`problems:` sections, `validation:` for conf, an optional `warning:`, and a contextual `next:`
hint). The JSON above is unchanged; the text view is a read-only reprojection of the same result.

`atl conf pull` returns a `PullResult` whose `pages[]` entries are `PulledPage`
objects. Each carries `id`, `title`, `path`, `version`, `assets`, and — only when
`--comments` was passed — a `comments` count (omitted otherwise, so the shape is
unchanged without the flag; an explicit `"comments": 0` means the fetch ran and
found none, distinguishable from "not fetched"):

```json
{
  "root": "mirror",
  "pages": [
    { "id": "100", "title": "Alpha", "path": "DOCS/alpha/alpha.csf", "version": 3, "assets": 0, "comments": 2 }
  ]
}
```

With `--comments`, two sidecar files are written next to the page:
`<slug>.comments.json` (a `[{id, author, created, body}]` array, pretty-printed
with a trailing newline) and `<slug>.comments.md` (a derived read view). The
page's `.meta.json` gains `comments_pulled: true` (the explicit "comments were
fetched" marker — present even when the count is zero) plus `comment_count` (and
`comments_truncated: true` when the listing hit the fetch cap) — all omitted
without the flag. Comment bytes
never enter `content_hash` or `.atl/base/`, so they never affect dirty/drift/push
gating. When any page's comment listing is truncated, the result carries
`comments_truncated: true` and the CLI writes a stderr warning; the JSON on
stdout stays clean.

`atl config show` emits `{ "confluence_url"?, "jira_url"?, "update_base_url"?, "render", "render_provenance"?, "local_config_path"?, "mirror" }`. `render` is the **effective** merged render configuration (always present; both `jira` and `confluence` sections carry at least `profile`, defaulting to `default`). `render_provenance` maps each dotted render key whose value is *not* the built-in default to its source (`global` or `local`) and is `omitempty` — an all-default mirror emits none, keeping the shape backward-compatible. `local_config_path` appears only when a per-mirror `.atl/config.json` is in scope from the current directory. Warnings about forbidden/unknown keys in a local file go to **stderr** as `warning:` lines; stdout stays clean. `config set` accepts a positional dotted render key (`render.{jira,confluence}.{profile,include,exclude}`, plus `render.jira.custom_fields`, `render.jira.field_views`, and `render.jira.epic_field`) alongside the existing URL flags; `field_views` is a JSON descriptor array. `--local` writes the per-mirror file (render keys only — a URL flag with `--local` is a usage error, exit 2).

`atl jira export --jql ... --out FILE --format jsonl|json|csv` writes one compact artifact and a
sidecar manifest at `FILE.manifest.json`. `--ids` and `--keys` can be used instead of `--jql` to
generate batched `id in (...)` / `key in (...)` queries. Stdout remains the normal `emit()` JSON
summary:

```json
{
  "path": "issues.jsonl",
  "manifest_path": "issues.jsonl.manifest.json",
  "format": "jsonl",
  "count": 1
}
```

JSONL emits one `JiraIssueSnapshot` object per line (`{key,id,fields}`); JSON emits
`{manifest,issues}`; CSV emits `key,id` followed by the deterministic field list. The manifest
stores query mode, generated queries when applicable, fields, format, count, CLI version, and a
backend URL hash only:

```json
{
  "command": "atl jira export",
  "format": "jsonl",
  "query_mode": "jql",
  "jql": "project=PROJ",
  "count": 1,
  "backend": {
    "service": "jira",
    "url_hash": "sha256:..."
  }
}
```

The backend hostname and PAT are never written to the manifest.

`atl jira export diff OLD NEW` reads JSONL/JSON/CSV compact exports and reports issue identifiers:

```json
{
  "old_count": 1,
  "new_count": 2,
  "added": ["PROJ-2"],
  "changed": ["PROJ-1"]
}
```

`atl jira planning report --jql ...` returns deterministic per-issue quality rows:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "score": 4,
      "max_score": 5,
      "level": "warn",
      "gaps": ["missing_artifact_ref"],
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ],
  "summary": {
    "good": 0,
    "warn": 1,
    "poor": 0
  }
}
```

When `--csv FILE` is passed, the same command writes a deterministic CSV sidecar
and includes `csv_path` in the JSON result.

`atl jira issue refs <KEY>` and `atl jira issue refs --jql ...` return
deterministic artifact references per issue:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ]
}
```

`atl jira issue attachment list <KEY>` returns the issue key plus the attachment
metadata Jira exposes. `-o id` prints attachment ids one per line:

```json
{
  "attachments": [
    {
      "id": "42",
      "title": "spec.xlsx",
      "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "fileSize": 12345,
      "version": 0
    }
  ],
  "key": "PROJ-1"
}
```

`atl jira issue attachment get <KEY> --id <ID-or-filename>` downloads one
attachment and returns the written local path. `id` echoes the selector the
caller passed; `name` is the filename Jira reported for the matched attachment:

```json
{
  "id": "42",
  "key": "PROJ-1",
  "name": "spec.xlsx",
  "path": "attachments/spec.xlsx"
}
```

`atl jira issue attachment upload <KEY> --file <PATH>` uploads one local file
and returns the uploaded attachment metadata:

```json
{
  "attachment": {
    "id": "44",
    "title": "spec.xlsx",
    "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "fileSize": 12345,
    "version": 0
  },
  "key": "PROJ-1"
}
```

`atl jira issue tree --jql ... --epic-field ...` returns a normalized
epic-to-child tree:

```json
{
  "jql": "project=PROJ",
  "epic_field": "customfield_10001",
  "count": 3,
  "epics": [
    {
      "key": "PROJ-1",
      "summary": "Parent",
      "type": "Epic",
      "children": [
        {
          "key": "PROJ-2",
          "summary": "Child",
          "type": "Story",
          "epic": "PROJ-1"
        }
      ]
    }
  ]
}
```

`external_epics` contains children whose epic key is not part of the selected
JQL result. `orphans` contains selected non-epic issues with no epic field. Both
fields are omitted when empty.

`atl jira issue link suggest --csv links.csv` is read-only and returns missing
link candidates from a reviewed CSV plan:

```json
{
  "path": "links.csv",
  "planned_count": 2,
  "count": 1,
  "candidates": [
    {
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "dependency found during review",
      "row": 2
    }
  ]
}
```

Rows whose outward link already exists on the source issue are omitted from
`candidates`. The command performs no Jira writes.

`atl jira issue plan apply --csv plan.csv` returns a guarded dry-run/apply
report:

```json
{
  "path": "plan.csv",
  "mode": "dry-run",
  "count": 1,
  "results": [
    {
      "row": 2,
      "op": "link",
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "reviewed dependency",
      "status": "would_apply"
    }
  ]
}
```

Status values are `would_apply`, `already_satisfied`, `applied`, `blocked`, and
`failed`. The command defaults to dry-run. Write mode requires `--apply
--confirm APPLY`; `field` operations also require the field to be included in
`--allow-fields`.

`atl jira structure rows <ID>` returns a parsed read-only view of a Tempo Structure forest:

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "rows": [
    {
      "row_id": 100,
      "depth": 0,
      "item_type": "issue",
      "item_id": "10001",
      "position": 0
    }
  ]
}
```

For non-root rows, `parent_row_id` is present. `-o id` prints Structure row ids
one per line. `--root` emits the first matching row plus descendants; matching is
by row metadata first and then by Structure values fetched through
`--root-fields` (default `key,summary`).

`atl jira structure values <ID> --rows ... --fields ...` preserves the backend
value matrix under `responses` and `raw`; if the backend reports permission
gaps, normalized row ids are also exposed as `inaccessible_rows`. The field is
always present; when there are no reported gaps it is `[]`.

`atl jira structure pull-issues <ID>` returns:

```json
{
  "structure_id": 123,
  "version": {"signature": 55, "version": 7},
  "rows": [],
  "issue_ids": ["10001"],
  "issues": [{"key": "PROJ-1", "id": "10001", "fields": {}}],
  "count": 1
}
```

`atl jira structure export <ID> --out FILE --format json|csv|md` writes the
artifact and returns a small result object:

```json
{
  "path": "structure.json",
  "format": "json",
  "structure_id": 123,
  "row_count": 1,
  "issue_count": 1
}
```

JSON export artifacts contain `{structure_id,version,rows,issue_ids,issues}`.
CSV export artifacts contain row metadata (`row_id`, `depth`, `parent_row_id`,
`item_type`, `item_id`, `issue_key`, `issue_id`) plus requested issue fields.
Markdown export artifacts render an indented tree for review.

`atl manifest create --root DIR` writes a sanitized local manifest and returns
the written path plus the manifest body:

```json
{
  "path": "mirror/manifest.json",
  "manifest": {
    "created_at": "2026-01-01T00:00:00Z",
    "command": "atl manifest create",
    "root": "mirror",
    "service": "jira",
    "selectors": ["jql=project=PROJ"],
    "fields": ["summary", "status"],
    "counts": {
      "files": 2,
      "bytes": 42,
      "extensions": {
        ".json": 1,
        ".md": 1
      }
    },
    "backend": [
      {
        "service": "jira",
        "url_hash": "sha256:..."
      }
    ],
    "atl_version": "0.2.0",
    "elapsed_ms": 1
  }
}
```

Backend entries contain URL hashes only, never backend hostnames or tokens.
