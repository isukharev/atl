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

`atl jira issue view <KEY>` is the non-persistent counterpart to a mirror view.
It writes no files and emits `{"key":<KEY>,"markdown":<configured-view>}` by
default; under `-o text`, stdout is the raw Markdown document. Advisory render
warnings remain on stderr. The selected render root is read only for its local
presentation config and gains no snapshot, sidecar, assets, or writeback state.
Consequently transient output cannot be applied or pushed: pull the issue fresh
before editing it.

`atl jira pull` writes three files per issue: `<KEY>.wiki` (the native Jira wiki body, byte-for-byte —
the editable substrate), `<KEY>.md` (a derived Markdown staging view rendered from the wiki and
regenerated best-effort on pull/render), and `<KEY>.json` (the raw-fields snapshot). The pull
result's `path` points at the `.md`; `wiki_path` points at the sibling `.wiki` substrate. To use the
friendly surface, edit generated `# Description` and/or field sections explicitly configured as
editable, then run `jira apply`. Description changes merge into `.wiki`; field changes become an
explicit `.atl/pending/jira/<KEY>.json` write set. The raw issue snapshot is not changed until a
successful push refreshes it. `.md` is never sent directly and a later pull/render can replace it. Edit
`.wiki` directly for constructs the staging view cannot express. Generated issue fields appear in a
read-only `# Metadata` Markdown table; update them through dedicated commands, not by editing the
table. A typed field section is editable only with `editable:true`, `placement:"section"`, and
`format:"jira_wiki"`; transient `jira issue view` output remains read-only. Generated regions carry hidden stable `atl:section`
markers; Jira rich-text headings are nested below their generated owner. Human-facing
datetime values are compacted to minute precision, while the JSON snapshot keeps
the exact raw server value. The JSON snapshot is an object with
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
  "epic_selector": "Epic Link",
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
the `.wiki` content hash or remote drift gate. Offline render/apply accept it
only when its epic key, configured selector (when present), and resolved epic
field match the issue/view affinity; otherwise it is ignored and render warns
to re-pull. `epic_selector` is omitted for auto-detection and retained for any
explicit configured selector (display name or field id), so changing that
selector cannot reuse a stale sidecar resolved from a different field.

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
"synced", "pending_fields"?, "local_error"?, "remote_drifted"?, "field_drifted"?, "remote_error"? }, ... ] }`.
`locally_edited` is true when the `.wiki` differs from the pulled base or a configured field is
pending; `synced` is false for a `.wiki` with no sidecar entry (never-synced — it also reads
`locally_edited`). `remote_drifted` covers description or pending-field drift; `field_drifted`
identifies the latter. They and `remote_error` appear only with `--remote` and are
`omitempty`. `local_error` is independent of `--remote` and reports a broken
pending-to-mirror binding such as a missing or moved `.wiki`.

`atl jira push <file.wiki|DIR> [--apply] [--force] [--into ROOT]` emits `{ "items": [ ... ] }`, one
item per file: `{ "path", "key", "pushed", "dry_run"?, "skipped"?, "remote_drifted"?,
"drift_overridden"?, "diff"?, "fields"?: [{"id","diff"?}], "field_drifted"?, "failed"?,
"warning"? }`. It is **dry-run by default**: without
`--apply`, `dry_run` is `true`, `pushed` is `false`, `diff` carries the unified diff of what the
write changes on the server (current remote → local body; equal to base → local when there is no
drift), and no write occurs. Field-only pending issues are included in directory pushes. Description
drift without `--force` exits `8`; `--force` sets `drift_overridden`. Pending-field drift sets both
`remote_drifted` and `field_drifted` and always exits `8`, even with `--force`. When Description and
fields changed they are sent in one typed update. `--apply` sets `pushed:true`; a post-push
transport/local mirror-refresh failure surfaces as a `warning`, not an error.
A successful verification read that no longer matches the reviewed end state
retains pending, sets drift/failed details, and exits `8` even though
`pushed:true` records that the write request was sent. `skipped:"unchanged"`
marks a clean file.

`atl jira apply <FILE.md> [--dry-run] [--allow-loss] [--rebase-pending] [--into ROOT] [--render-*]` emits the same
shape as `conf apply` for Description, plus pending-field details:
`{ "path", "wiki_path", "pending_path"?, "dry_run", "rebased"?, "report": {...},
"fields"?: [{"id","pending","report"}], "wrote", "warning"? }`. It is **local only** (no network). Each
`removed_constructs` entry is `{ "kind", "text" }` (`kind` ∈ `panel`, `color`, `mention`, `image`,
`monospace`, `link`, `macro`, …). The merge is fail-closed and exits `8` (`ErrCheckFailed`, nothing
written) on: an unconvertible edited block; a wiki-only construct dropped without `--allow-loss`
(the report still carries `removed_constructs` so the caller can see what would go); an edit to any
section other than generated `# Description` or an explicitly editable rich-text field (the error
names the section and its dedicated command); or a
local `.wiki` diverged from the last-synced base. Exit `4` (`ErrNotFound`) when the issue was never
pulled (no base/snapshot). Editable field values are stored under `.atl/pending/jira/` and do not
mutate `<KEY>.json`; `pull`/`render` overlay them in the derived view. On a successful write
`wrote:true`; a failed `.md`-view refresh sets `warning` and is not an error.
`--rebase-pending` is the explicit conflict step after fresh pull/review: raw
snapshot values become the new bases while visible local proposals remain.
Pending commits bind the exact sidecar path and reviewed wiki hash; a hidden
transaction record makes combined Description+field apply crash-recoverable.
Jira mirror mutations use one persistent mirror-internal advisory lock inode;
dry-runs may initialize that coordination file but never change Jira or commit
wiki/pending/view content.

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

`atl profile show` emits `{exists,path,hash,data?}`. A missing profile is a
successful read with `exists:false`, the future profile path, and a stable
64-hex missing-state hash. An existing profile also omits `data` by default.
`--section all|schema|preferences|team_policy|render_defaults|selectors` adds
the requested `data`;
`--service jira|confluence` is valid only for `schema` and `selectors`.

`atl profile preview --from-file FILE` emits
`{path,current_exists,current_hash,candidate_hash,changed,migration_from_schema_version?,sections,normalized_candidate}`.
It is read-only. Each `sections[]` item is `{section,status}` where status is
`added|removed|changed|unchanged`. The normalized candidate uses profile schema
version 1 and keeps schema facts, confirmed preferences, declared team policy,
render defaults, and named selectors separate. When a syntactically valid
future-version profile is present, preview never interprets it: it hashes the
exact bytes, sets `migration_from_schema_version`, and reports every replacement
section as changed.

`atl profile apply --from-file FILE --candidate-hash HASH
--expected-current-hash HASH` emits
`{path,previous_hash,profile_hash,changed}`. Candidate mismatch is exit 8;
current-profile mismatch is exit 5. A successful change atomically writes the
owner-only private profile; an already-current candidate succeeds with
`changed:false`. `atl profile guidance` emits
`{configured,schema_version?,instructions}` and is guaranteed not to project
profile values into `instructions`.

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
`{manifest,issues}`; CSV emits `key,id` followed by the deterministic field list.
JSONL/CSV are streamed atomically; aggregate JSON is limited to 10,000 issues
and 64 MiB of serialized issue data. The row-stream identity index is capped at
250,000 unique issues so exact deduplication remains memory-bounded.
CSV formula-leading cells are apostrophe-prefixed by default. `--raw-csv`
disables that protection and records `csv_raw: true` in the manifest. The manifest
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
  "version": 1,
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
      "expected_updated": "2026-01-02T03:04:05.000+0000",
      "status": "would_apply"
    }
  ]
}
```

Status values are `would_apply`, `already_satisfied`, `applied`, `blocked`,
`failed`, and fail-fast `skipped`. The command defaults to dry-run. Write mode
requires `--apply --confirm APPLY`; `field` operations also require the field
to be included in `--allow-fields`. Every CSV row carries `version=1` and a
review-time `expected_updated` value. Blocked/failed runs still emit the full
audit result on stdout and return exit 8. Default execution stops after the
first runtime failure; `--continue-on-error` processes independent rows but
does not turn the final exit into success. Schema version 1 rejects multiple
rows for the same source issue, preventing one successful write from making a
later row self-stale. Failed-row messages use safe reason categories rather than
raw transport errors, so backend URLs are not copied into the stdout audit.

`atl jira issue field set <KEY>` is a separate single-issue guarded flow. It is
dry-run by default and returns:

```json
{
  "key": "PROJ-1",
  "mode": "dry-run",
  "status": "would_apply",
  "expected_updated": "2026-01-02T03:04:05.000+0000",
  "actual_updated": "2026-01-02T03:04:05.000+0000",
  "fields": [
    {
      "field": "customfield_10001",
      "source": "markdown",
      "kind": "string",
      "bytes": 42,
      "sha256": "<hex>",
      "value": "h2. Progress\n\nOn track."
    }
  ]
}
```

The normalized values are intentionally present in JSON stdout for review and
may be private. `-o text` omits them and prints hashes/sizes. Status is one of
`would_apply`, `already_satisfied`, `applied`, `blocked`, `failed`, or `unknown`.
After any PUT error atl performs one fresh reconciliation read. For a
definitive 4xx rejection, proposals already visible are `already_satisfied`
(another actor may have produced the end state); absent/unreadable proposals
are `failed`. An ambiguous transport/timeout/5xx outcome is `applied` when the
proposals are visible and remains `unknown` otherwise (an
immediate old read cannot prove an in-flight write will not commit). Successful
reconciliation reads carry `"reconciled": true`. A stale
apply still emits the `blocked` result and exits 8. Apply requires
`--expected-updated`; all proposed fields are sent in one request.

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

`atl manifest create --root DIR` writes a backend-identity-hashed local manifest and returns
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

Configured backend entries contain URL hashes only; `atl` does not read or add
stored PATs to this artifact. Caller-provided `command`, selectors, JQL/CQL,
fields, include values, and paths are preserved verbatim and are **not
redacted**. Never pass credentials in that metadata, and review the manifest
before publishing it.
