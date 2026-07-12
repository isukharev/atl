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

- **`-o json` (default):** `{"error":"<message>","code":N,"kind":"<stable-kind>","remediation":"<stable-action>"}` (one JSON object, newline-terminated).
- **`-o text`:** `error: <message>`.

The existing `error` and `code` fields remain compatible. `kind` is always
present; `remediation` is deterministic guidance, not an instruction to execute
automatically. Both are derived from local sentinels/typed metadata, never by
parsing backend prose. Current exit classes map to `unexpected_error`,
`usage_error`, `authentication_failed`, `not_found`, `version_conflict`,
`forbidden`, `configuration_error`, and `check_failed`. Typed specializations
include `read_only_policy`, `transport_error`, and `api_error` without changing
their exit code. A missing command registration invariant is
`internal_error`/`report_bug` (still exit 8), not a user check failure.

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
| `7` | `exitConfig` | `domain.ErrConfig` | Invalid/incomplete configuration, including a missing backend URL/PAT or invalid named view |
| `8` | `exitCheckFailed` | `domain.ErrCheckFailed` | A check or safety precondition failed, including read-only policy refusal |

### Practical notes

When read-only policy blocks a mutation, the normal JSON error envelope keeps
`error` and `code:8` and adds stable `policy:"read_only"` plus the full
`command` path. The values come from typed local policy metadata, never backend
text. Text output remains one concise `error:` line.

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
corrupts the JSON stream. HTTP API error strings use the same query-value redaction and omit URL
fragments, so a failed request does not reintroduce JQL/CQL/selectors through stderr.

---

## Stable Snapshot Notes

`atl jira issue view <KEY>` is the non-persistent counterpart to a mirror view.
It writes no files and emits `{"key":<KEY>,"markdown":<configured-view>}` by
default; under `-o text`, stdout is the exact raw Markdown string with no
emitter-added newline (matching `conf page view`). Advisory render
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
  "status_id": "11",
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

Every Confluence derived page view begins with
`<!-- atl:document confluence-page v3 -->` and has reserved generated
metadata/body/comments/Jira-query boundaries. `conf apply` rejects missing, legacy, or
unknown versions and additions/removals/renames/reordering in the reserved marker sequence inside
the editable body before any substrate write. Marker-looking prose already
present in the native page remains valid when unchanged. Legacy/unmarked
migration uses `conf render` or a fresh pull;
callers preserve and reapply existing edits because render replaces `.md`.
Unknown/future versions require an updated binary and must not be downgraded.
The marker line may end in LF or CRLF. Atl strips only the CR attached to that
first line for version classification; remaining Markdown bytes stay
significant for dirty/edit/relocation checks.

JQL-bearing Confluence Jira macros keep a readable query placeholder in the
editable body and, when Jira read access succeeds, append a generated readonly
`# Jira Queries` suffix rendered by the shared IssueList Markdown table. Macro
columns override the selected named list view; otherwise the view's
`confluence_macro` projection is used. Pull persists a page/macro-hash-bound
`<slug>.jira-macros.json` snapshot so offline render and apply remain
byte-stable without network access. Per-query failures are bounded warnings and
leave placeholders; invalid or stale recorded enrichment is never merged into
CSF and makes apply fail closed pending a fresh pull. One page resolves at most
20 JQL macros and 2000 aggregate rows, with a 1000-row per-macro cap.
`render.confluence.jira_macros` and the per-run `--jira-macros auto|off`
override control whether page-provided JQL may execute. `off` is resolved before
Jira credentials are loaded, performs no Jira search, keeps placeholders, and
emits no query sidecar. The config key is global-only; mirror-local config is
untrusted for authenticated-read policy and cannot enable it. Post-push refresh uses the same sidecar-aware view
constructor as render/apply/relocation, preserving generated suffix bytes.
Read-only refusal diagnostics distinguish `# Jira Queries` from `# Comments`.

When `page_fields` is enabled, the read-only prefix contains
`<!-- atl:section page-fields readonly -->` followed by a `# Metadata` table and
optional `<!-- atl:section page-field.<id> readonly -->` sections. Descriptors
are stored with the view state so apply/push reproduce the exact prefix. Values
are single-line escaped plain text, not executable Markdown. `restricted` is
absent/unknown unless explicitly projected; offline render never converts
unknown into `false`.

The editable body begins visibly at `# Content`; native page headings retain
their original levels beneath that delimiter so Markdown-to-CSF identity is not
changed. A full view ends with readonly `# Comments`. Each comment is headed at
level two, and its native storage-format body is rendered with headings nested
below the comment. The comments sidecar retains both a plain fallback `body`
and optional `body_storage` CSF.

Native page links render as readable synthetic Markdown links whose destination
is `confluence-page:` plus optional space and percent-encoded title; explicit
labels stay separate from targets. Native colored spans render as protected
HTML color spans only for a closed inert CSS-color grammar; other values use a
non-styling `data-atl-color` marker, and literal inner HTML is escaped. Both
remain opaque byte-preserving merge markers. Apply's
loss report counts full page-link identity (space, target, label) and color
spans, so same-label links cannot hide removal of a different target.

The sibling Confluence `.meta.json` persists `ancestors` and `updated` when the
backend supplied them. `restricted` is present as a JSON boolean only when the
pull explicitly selected that descriptor; a narrower later pull removes it.

`atl conf page title set <ID>` is dry-run by default and emits
`{id,mode,status,current_title,title,title_bytes,title_sha256,current_version,
expected_version,final_version?,proposal_hash,reconciled?}`. Apply requires the
reviewed version and aggregate hash, reuses the fresh native CSF bytes unchanged,
and verifies title, body hash, and exactly `current_version+1`. Status is
`would_apply`, `already_satisfied`, `blocked`, `failed`, `applied`, or `unknown`.
`already_satisfied` is returned only after the reviewed version/hash gates pass.
Unknown is non-zero and must never be automatically replayed.

`atl conf page labels list <ID>` emits
`{id,labels:[{id?,prefix?,name,label?}],count,complete,truncated?}`. It follows
offset pagination to exhaustion; hitting a safety cap keeps the collected
prefix but sets `complete:false`, `truncated:true`, and writes a warning to
stderr. Text output is one `prefix<TAB>name` record per line.

`atl conf page labels add|remove <ID> <LABEL>...` emits
`{id,operation,mode,status,requested,current:[label-records],final?:[label-records],proposal_hash,complete,
reconciled?}` and is dry-run by default. The hash binds the page, operation,
normalized request, and complete current prefix/name set. Apply requires that
exact reviewed hash before `already_satisfied` or a write. Writes are sent once;
only `global` labels are mutation targets, while other prefixes remain visible
in the records. The final collection is re-read. Status is `would_apply`, `already_satisfied`,
`blocked`, `failed`, `applied`, or `unknown`; unknown is non-zero and must not
be replayed automatically.

`atl conf page move <ID>` is also dry-run by default and emits
`{id,mode,status,current_parent,parent,current_version,expected_version,
expected_parent,target_version,final_version?,proposal_hash,reconciled?}`.
Apply requires the reviewed source version, exact current parent (including an
explicit empty value for a top-level page), and proposal hash. It validates the
fresh source/target hierarchy, writes the unchanged native body/title once,
and verifies parent, body, title, space, and exactly `current_version+1`.
Proposal-hash schema v2 also binds `target_version`; apply re-reads the target
identity, version, space, and ancestor ids immediately before PUT and blocks if
they changed. This narrows but cannot eliminate the backend's two-page TOCTOU.
`unknown` is non-zero and must never be automatically replayed.
An already-satisfied parent still requires the reviewed source version, current
parent, and proposal hash before it can return success.

`atl conf page view <ID>` is the non-persistent counterpart. Its JSON is
`{"id","title","space","version","markdown"}`; text output is the exact
Markdown string. It uses the same versioned renderer, but marks the body
`readonly`, writes no mirror or view state, and cannot be used as an apply/push
surface. Optional comments are fetched only when selected by the effective
render settings; truncation is warned on stderr. A fresh pull is required before
editing.

Confluence pull/render/apply/push and mirror-local `conf edit` acquire one persistent mirror-internal
advisory lock for their complete mutation/preview critical section. Contention
is exit `8` before page/state writes. The file persists so every process locks
the same inode; process exit releases ownership. Read-only status is lock-free.
Jira retains its own workflow lock, while both services additionally merge
sidecar patches under the shared `.atl/state.lock`; cross-service state
contention is retried for a brief fixed window, then fails closed and cannot
lose unrelated entries.

When a Confluence re-pull computes a different path for an already tracked page
id, relocation is fail-closed. The old native body must match its synced hash,
the old Markdown must exactly match its recorded pristine view, metadata must
prove the same page id, and the destination must be unoccupied. Pull records the
new canonical path before removing only the old `.csf`, `.md`, and
`.meta.json`. Descendants, assets, comment caches, and unrelated files are never
recursively removed. A local relocation ownership marker reserves their old
directory for the same page id so a future slug collision cannot inherit them.
The `<slug>.relocated.json` marker is atl-managed reserved state: do not edit or
remove it. A pre-existing invalid/different-owner marker blocks relocation and
is never overwritten.
When all three old primary artifacts are absent, pull treats the old copy as
deliberately abandoned and replaces its stale sidecar path with the new
canonical path. Partial absence remains exit `8` because ownership and local
edits cannot be proven. A legacy v1 view produces migration-specific guidance;
an unknown/future view is preserved and requires a newer binary.
If cleanup is interrupted, path-aware state lookup keeps an old copy
untracked/dirty rather than presenting it as current.
Such a copy is reported by status with `non_canonical:true` and
`canonical_path`; text output uses `S! <id> <old> (canonical: <new>)`. Remote
drift probing is skipped for this stale copy. Push/dry-run refuses it with exit
`8` even under `--force`.

A successful Confluence response that omits the requested body projection is
not equivalent to an empty page. Pull and native-CSF reads require
`body.storage.value`; `conf page get --format view` requires `body.view.value`.
Either omission fails with exit `8` before output/artifacts are treated as an
empty page. After a successful push, the
same partial refresh is advisory: local body/base/state bytes are preserved and
the item reports a re-pull warning. `BodyPresent=true` with zero body bytes is a
valid explicitly empty page.

Missing local page targets for Confluence render/apply/push use
`ErrNotFound`/exit `4`; syntactically invalid target types continue to use
`ErrUsage`/exit `2`. Transport failures expose a fixed coarse category
(`dns`, `tls`, `timeout`, `connection-refused`, `connection-lost`,
`unreachable`, `canceled`, or `network`) alongside a query-redacted URL. The
raw cause remains non-unwrappable and no category includes cause text.

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
accepted view begins with `<!-- atl:document jira-issue v2 -->`; a v1, missing,
or unversioned marker exits `8` before any write and requires an offline
`jira render` or fresh pull before editing. V1 identifies the former generated
bullet form of Subtasks/Epic Children and is intentionally not reconstructed as
v2 during apply. A future/unknown version requires a
newer binary and must not be rendered or downgraded by the current one. A
directory render preflights every existing view before rewriting any sibling,
so one future marker cannot produce a half-migrated batch. It repeats each
target check under the mirror mutation lock immediately before writing; `pull`
uses the same locked check before changing that issue's artifacts. A CRLF on
the marker line is recognized without normalizing the rest of the file.
Unreadable or malformed `.json` snapshots remain advisory skips, but each is
named in a stderr warning instead of disappearing silently. Since render
rewrites the derived `.md`, callers preserve any existing edits externally and
reapply them after migration.
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
`<slug>.comments.json` (a `[{id, author, created, body, body_storage?}]` array, pretty-printed
with a trailing newline) and `<slug>.comments.md` (a derived read view). The
page's `.meta.json` gains `comments_pulled: true` (the explicit "comments were
fetched" marker — present even when the count is zero) plus `comment_count` (and
`comments_truncated: true` when the listing hit the fetch cap) — all omitted
without the flag. Comment bytes
never enter `content_hash` or `.atl/base/`, so they never affect dirty/drift/push
gating. When any page's comment listing is truncated, the result carries
`comments_truncated: true` and the CLI writes a stderr warning; the JSON on
stdout stays clean.

`atl config show` emits `{ "read_only", "confluence_url"?, "jira_url"?, "update_base_url"?, "render", "jira_list_views", "jira_list_views_error"?, "render_provenance"?, "local_config_path"?, "mirror" }`. `render` is the **effective** merged render configuration (always present; both `jira` and `confluence` sections carry at least `profile`, defaulting to `default`). `render_provenance` maps each dotted render key whose value is *not* the built-in default to its source (`global` or `local`) and is `omitempty` — an all-default mirror emits none, keeping the shape backward-compatible. `local_config_path` appears only when a per-mirror `.atl/config.json` is in scope from the current directory. Warnings about forbidden/unknown keys in a local file go to **stderr** as `warning:` lines; stdout stays clean. `config set` accepts `safety.read_only`, Jira list views, or a positional dotted render key (`render.{jira,confluence}.{profile,include,exclude}`, plus `render.jira.custom_fields`, `render.jira.field_views`, and `render.jira.epic_field`) alongside the existing URL flags; `field_views` is a JSON descriptor array. `--local` writes the per-mirror file (render keys only — a URL flag with `--local` is a usage error, exit 2).

Runtime commands validate all `jira_list_views` before network access and map
an invalid catalog to config exit 7. Recovery is deliberately narrower:
`config show` returns the raw entries and `jira_list_views_error`, and
`config set jira.list_views...` may replace/delete invalid entries one at a
time. A repair deletion can persist while another entry remains invalid; other
commands never consume a partially valid catalog. Malformed `config.json` JSON
also maps to exit 7 and must be repaired as a file rather than overwritten from
an uncertain partial decode. Offline, skip-self-update diagnostic reads may run
without decoding the policy so version/help/profile evidence remains available;
this exception never applies to a mutating command or online read.

`atl profile show` emits `{exists,path,hash,data?}`. A missing profile is a
successful read with `exists:false`, the future profile path, and a stable
64-hex missing-state hash. An existing profile also omits `data` by default.
`--section all|schema|preferences|team_policy|render_defaults|selectors` adds
the requested `data`; `--service jira|confluence` is valid for `schema`,
`render_defaults`, and `selectors`. A service-scoped render read returns only
`data.{jira|confluence}` and never changes runtime configuration. The selected
value is `null` when that service has no saved render memory, independent of
whether the sibling service is configured.

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
profile values into `instructions`. Its generic instructions explicitly state
that saved render/mirror preferences are memory until separately compared with
and synchronized to runtime; it never emits the saved values themselves.

`atl profile suggest --from-file OBSERVATIONS --out SUGGESTION` emits
`{path,suggestion_hash,base_profile_hash,previously_rejected}` and writes the
canonical version-1 suggestion mode 0600 under an already-private parent. It
never writes `profile.json`. Observations are strict and versioned; non-schema
proposals require `{source,observed_at,reason}` evidence and cannot contain team
policy. Preference fields and Jira/Confluence render services merge
independently, so omitted siblings are preserved. Generated artifacts and
private state are bounded to the same 4 MiB read limit before write. Rejection
memory retains the most recent 4096 distinct hashes.
Suggestion output names require `.atl-suggestion.json`; revalidation observation
outputs require `.atl-observations.json`. These reserved non-state suffixes plus
one held parent-directory handle prevent collisions and check/write redirection;
the parent itself must be mode 0700 or stricter.

`atl profile suggestion review --from-file SUGGESTION` emits
`{suggestion_hash,previously_rejected,evidence?,preview}` where `preview` is the
same exact profile-preview contract above. `suggestion apply` requires
`--suggestion-hash`, `--candidate-hash`, and `--expected-current-hash`, returning
`{suggestion_hash,profile}` with the normal apply result nested under `profile`.
`suggestion reject` returns `{suggestion_hash,status:"rejected",changed,path}`;
its owner-only decision file retains hashes only. Content/hash mismatch is exit
8 and base/current profile mismatch is exit 5.

`atl profile revalidation status --stale-before RFC3339 [--service ...]` emits
`{profile_hash,stale_before,entries}`. Entries contain
`{service,id,name?,status,verified_at?,last_checked_at?,source?,error?}` and status
is `fresh|stale|verified_pending|missing|failed`. `atl profile revalidate
--from-file CHECKS --out OBSERVATIONS` emits
`{path,observations_hash,base_profile_hash,entries}`; immediate check-result
entries use `verified|missing|failed`. It records at most the 1000 newest checks
per service in private state, writes verified facts to a version-1 observations
artifact, and never changes or deletes a profile fact. Persisted failure
summaries reject controls, redact network locations, and are length-capped.

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
and includes `csv_path` in the JSON result. Formula-leading cells are
apostrophe-prefixed by default; `--raw-csv` requires `--csv` and disables that
protection for trusted non-spreadsheet consumers.

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
  "proposal_hash": "<hex>",
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

The aggregate proposal hash uses schema v2 and binds the issue key plus the
complete normalized field set, so a review for one issue cannot authorize the
same values on another. The normalized values are intentionally present in JSON stdout for review and
may be private. `-o text` omits them and prints hashes/sizes. Status is one of
`would_apply`, `already_satisfied`, `applied`, `blocked`, `failed`, or `unknown`.
After any PUT error atl performs one fresh reconciliation read. For a
definitive 4xx rejection, proposals already visible are `already_satisfied`
(another actor may have produced the end state); absent/unreadable proposals
are `failed`. An ambiguous transport/timeout/5xx outcome is `applied` when the
proposals are visible and remains `unknown` otherwise (an
immediate old read cannot prove an in-flight write will not commit). Successful
reconciliation reads carry `"reconciled": true`. A stale
apply still emits the `blocked` result and exits 8. Apply requires both
`--expected-updated` and `--expected-proposal-hash`. The latter binds sorted
field ids, sources, normalized types, and values; a changed local input fails
before backend metadata/read/write calls. All proposed fields are sent in one
request.

`atl jira issue watchers list <KEY>` emits
`{key,watch_count,is_watching,watchers:[{name,key?,display_name?,active}],
complete,truncated?}`. Jira DC does not paginate this endpoint: completeness
requires every counted watcher to have a returned username. A count/identity
mismatch sets `complete:false`, `truncated:true`, and a stderr warning.

`atl jira issue watchers add|remove <KEY>` is dry-run by default and emits
`{key,operation,mode,status,username,identity_source,current,final?,
proposal_hash,complete,reconciled?}`. Exactly one of an explicit DC
`--username` or `/myself`-resolved `--me` is required. The proposal hash binds
issue, operation, resolved username, and complete current membership. Apply
requires the reviewed hash before `already_satisfied` or one non-retried write,
then verifies membership. Status is `would_apply`, `already_satisfied`,
`blocked`, `failed`, `applied`, or `unknown`; unknown is non-zero and must not
be automatically replayed. Incomplete membership refuses every mutation.

`atl jira issue fields <KEY>` emits
`{key,mode,non_empty_only,count,omitted_empty?,fields:[{id,name,custom,
schema?,empty?,value,truncated?,original_bytes?}]}`. Default mode is `compact`
and omits empty fields. Exact repeatable `--field` selectors accept ids or
case-insensitive display names; ambiguous names fail before the issue read.
Compact user values omit email/avatar/self data, known options/named values use
closed projections, and unknown objects expose only bounded non-empty key names.
Explicit `--include-empty` expands the catalog. Explicit `--raw` switches mode
to `raw`, preserves unprojected private values, and writes a privacy warning to
stderr. `-o text` is a structurally escaped Markdown table.

Online Jira get/pull/view field selectors resolve exact names through the same
catalog. Render selectors are stored as resolved ids in view state, so offline
render/apply does not depend on a later metadata lookup. Existing technical ids
remain valid without an extra field-catalog request.

`atl jira issue history <KEY>` emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,history,
last_changes?}`. Each history item preserves both `field` and `field_id` when
Jira supplies them. `complete:true` means every entry advertised by the chosen
backend representation was consumed; `complete:false` always carries a reason
and must not be interpreted as proof that an omitted change did not happen.
`source` is `paginated`, `embedded`, or `legacy`. Repeatable exact `--field`
selectors and inclusive `--since`/`--until` boundaries are applied locally
after the qualified read. `last_changes` reports the newest matching change per
selected resolved field within those boundaries. `-o text` is a status line and
a structurally escaped Markdown table.

`atl jira export ... --out -` is an artifact stdout mode, not a command-result
mode. JSONL emits one `JiraIssueSnapshot` per line, aggregate JSON emits a bare
snapshot array, and CSV emits its header and rows. It emits no manifest, export
result envelope, or trailing status bytes and creates no files. Diagnostics are
stderr-only. Aggregate JSON retains the 10,000-issue/64 MiB caps; row formats
retain the identity cap and safe-CSV default. Because a late read/write failure
can leave a streamed prefix on stdout, consumers must accept the artifact only
when the process exits zero. File destinations retain the existing atomic
artifact plus `<out>.manifest.json` contract. Exact field display names are
resolved before search and exported under stable field ids.

`atl conf page resolve <ID-OR-URL>` emits
`{id,kind,via?,network_requests,space?,title?}`. `kind` is `id`, `canonical`,
`viewpage`, `rest`, `display`, or `short`; a short link records the final parsed
form in `via`. `network_requests` is zero for direct identity-bearing forms,
one for exact display search or an id-bearing short-link target, and at most two
when a short link ends at an exact display URL. `-o id` and `-o text` emit only
the resolved id. Same-origin/context validation happens
before a request; ambiguous display matches and unsupported/malformed redirect
targets fail closed. Read-only page consumers accept the same references but
continue to emit the backend's stable page id in their existing result shapes.

`atl conf page outline <REF>` emits
`{id,title,space,version,count,total,complete,truncated?,original_bytes,
emitted_bytes,headings:[{index,level,title,path,occurrence}]}`. The 1000-heading
and 262144-byte structural caps are explicit: `count`/`emitted_bytes` describe
emitted records and `total`/`original_bytes` describe parsed records. `-o text`
is an indented Markdown list. Macro/code/table-contained headings are not
entries.

`atl conf page section <REF> --heading ...` emits
`{id,page_title,space,version,heading,level,path,occurrence,markdown,complete,
truncated?,original_bytes,emitted_bytes}`. Duplicate normalized titles require
an explicit 1-based `--occurrence`. The section includes descendant headings
and ends before the next same/higher-level heading. The byte cap is applied at
rendered block boundaries; `complete:false,truncated:true` is never a complete
section. `-o text` emits only `markdown`. No mirror artifact or writeback base
is created.

List-oriented Jira reads (`issue search`, `issue children`, `board
issues/backlog`, and `sprint issues`) share one app-layer contract:

```json
{
  "schema_version": 1,
  "source": {"kind": "board", "id": "5"},
  "selection": {"scope": "board", "jql": "status in (11,12)"},
  "projection": {
    "columns": ["position", "key", "summary", "status", "board.column"],
    "fields": ["summary", "status"],
    "ordering": "backend-rank",
    "view": "default"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "values": {"summary": "First", "status": "Open"},
    "context": {"board": {"column": "To Do", "in_board": true, "in_backlog": false}}
  }],
  "page": {"count": 1, "complete": true, "truncated": false, "next_cursor": null}
}
```

`rows` is always an array. Identity/order fields are fixed; selected Jira fields
live under `values`, and source semantics stay namespaced under `context`.
`projection.fields` exactly names `values`; `projection.columns` preserves the
requested human order. `--columns` derives backend fields and accepts common
identity, Jira field ids, and source-specific names such as `board.column` or
`sprint.id`. Unknown/foreign context columns fail with usage. `-o text` renders
the same rows as one safe Markdown table (or `_None._`); `-o id` prints keys.
The page cursor is `null` at exhaustion and resumable only when non-null.
For board pages, top-level `position` is the zero-based position within the
returned page; ordering is backend rank, but ATL does not expose that index as
a durable Jira rank value.

`projection.view` is `default`, `full`, a configured custom name, or
`explicit` when `--columns`/`--fields` supplied the projection. Applicable
commands accept `--view`; explicit projection flags win. Effective config
always exposes source-specific built-in `default` and `full` entries under
`jira_list_views`; custom entries inherit default arrays they omit. Unknown
views or context columns invalid for the selected source fail with usage before
network access.

`jira issue children <EPIC-KEY>` returns `source.kind:"epic"`, records the
parent key and resolved Epic Link field under `selection`, and namespaces
`parent` plus relation `epic-child` under `rows[].context.epic`. It resolves
field metadata once and executes one paginated generated JQL request; it does
not read every child individually. Its default columns are
`key,summary,status,issuetype,assignee`. The generated epic-children and
subtasks sections in transient/durable issue Markdown use the same table
renderer in embedded mode; an empty related list is `_None._`.

`atl jira board config <ID>` returns the workflow projection used to interpret
board issues:

```json
{
  "id": 5,
  "name": "Quarter plan",
  "type": "kanban",
  "filter_id": "42",
  "kanban_subquery": "fixVersion is EMPTY",
  "constraint_type": "issueCount",
  "columns": [
    {"name": "To Do", "status_ids": ["11", "12"], "max": 7},
    {"name": "Done", "status_ids": ["13"]}
  ],
  "rank_field_id": "10019"
}
```

`board issues` and `board backlog` return one explicit common IssueList page.
The backend request may include `status` when board column context needs its id,
without adding an unrequested value to `projection.fields`. The backlog issue
endpoint is Scrum-only; `board backlog` refuses a Kanban board after reading its
configuration and before calling the incompatible endpoint.

`atl jira board view <ID>` returns a normalized multi-page snapshot:

```json
{
  "schema_version": 1,
  "board": {"id": 5, "name": "Quarter plan", "type": "kanban", "columns": []},
  "scope": "all",
  "projection": {
    "kind": "jira-fields-v1",
    "columns": ["position", "key", "summary", "status", "board.column", "assignee"],
    "fields": ["summary", "status", "assignee"],
    "ordering": "backend-rank"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "board_position": 0,
    "in_board": true,
    "in_backlog": false,
    "status_id": "11",
    "status": "Open",
    "column": "To Do",
    "column_index": 0,
    "column_mapped": true,
    "values": {"summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "complete": true,
  "truncated": false,
  "backlog_fetched": false
}
```

Rows from board scope retain backend rank order. For Scrum `scope:all`, backlog
membership and backlog position are joined by issue key; backlog-only issues
are appended in backlog order. For Kanban, `scope:all` reads board scope only,
sets `backlog_fetched:false`, and never calls backlog or sprint endpoints.
Unknown status ids use `column:"Unmapped"`, `column_index:-1`, and
`column_mapped:false` rather than disappearing.

`--limit 0` follows pagination to exhaustion. A positive limit applies per
requested scope; when more rows exist the output sets `complete:false` and
`truncated:true`. Repeated issues across pages, a non-advancing cursor, or the
pagination safety cap return check-failed (exit 8). There is no board snapshot
version in Jira's API, so `complete` means all reported pages were consumed,
not that concurrent board changes were transactionally excluded.

`board export --format json|jsonl|csv|md` writes the same projection. JSONL
repeats compact board identity, projection, row count, and completeness with each row. CSV contains rank,
scope membership, status/column mapping, and selected fields; formula-leading
cells are neutralized unless `--raw-csv` is explicitly approved. Markdown is a
compact review table rendered by the same primitive as other issue lists. None
of these read paths call rank, sprint, move, or issue
write endpoints.

`atl jira structure folders <ID>` is the fast stored-folder index. It fetches
metadata, one forest, and one batched folder-label value projection; it never
searches Jira issues:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Planning"},
  "forest_version": {"signature": 10, "version": 2},
  "folders": [{
    "folder_id": "100",
    "row_id": 500,
    "name": "Quarter",
    "path": ["Plans", "Quarter"],
    "depth": 1,
    "parent_folder_id": "99",
    "stats": {"descendant_rows": 86, "issue_rows": 72, "unique_issues": 70, "subfolders": 2, "max_relative_depth": 4}
  }],
  "complete": true,
  "warnings": []
}
```

`-o id` emits stable folder item ids, not row ids. Missing/partial labels keep
technical ids and statistics, set `complete:false`, and add bounded warnings.

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

Rows/view/pull-issues/export also accept one mutually exclusive exact selector:
`--folder-id`, `--folder-row`, or `--folder-path`. Exact selectors verify a
stored folder in the same forest snapshot, never fall back to fuzzy matching,
and return not-found or check-failed on absence/ambiguity. Results include
`selection`; selected rows retain absolute `depth` and `parent_row_id` and add
`relative_depth` beginning at zero. `--folder-id` is the durable agent path;
`--folder-row` is snapshot-local and path selection requires complete labels.
Path comparison is case-insensitive and collapses whitespace in every segment;
folder names containing a literal `/` require id/row selection. `complete`
describes the emitted subtree: unrelated missing labels elsewhere in the forest
do not make an id/row/root-selected view partial.

`atl jira structure values <ID> --rows ... --fields ...` preserves the backend
value matrix under `responses` and `raw`; if the backend reports permission
gaps, normalized row ids are also exposed as `inaccessible_rows`. The field is
always present; when there are no reported gaps it is `[]`.

`atl jira structure view <ID>` returns a normalized, version-checked snapshot:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Quarter plan"},
  "forest_version": {"signature": 55, "version": 7},
  "projection": {
    "kind": "jira-fields-v1",
    "source": "list-view",
    "attributes": ["key", "summary", "status", "assignee"],
    "browser_view_reproduced": false
  },
  "rows": [{
    "row_id": 100,
    "depth": 0,
    "item_type": "issue",
    "item_id": "10001",
    "position": 0,
    "accessible": true,
    "values": {"key": "PROJ-1", "summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "issue_count": 1,
  "complete": true,
  "inaccessible_rows": [],
  "warnings": []
}
```

`projection.source` is `list-view` for the built-in default, `full`, and custom
named views; it is `explicit` when `--fields` wins. The selected preset name is
reported separately as `projection.view`.

`-o text` renders emitted `#`, numeric Depth (relative when selected), technical
Type/Item, separate Jira value columns, and Access. It does not duplicate key
and summary in a combined Tree cell or dump raw Jira objects/transport URLs.
Known Jira objects use their human label/name; an unknown non-empty object is
shown as `[object]` so it cannot be mistaken for a missing value without leaking
transport internals.
`-o id` emits row ids. The default attributes are shown above; explicit
`--fields` selects Jira fields and changes both `projection.attributes` and row values. Browser saved
views are deliberately not claimed as the source because Structure's supported
integration API does not expose a stable saved-view column projection.

Issue values are joined only for rows whose type is `issue`, using the forest's
stable numeric issue `item_id` through Jira search, not by Structure row id.
Structure's generated identity join disables Jira's advisory strict-query
validation so one deleted or hidden id cannot reject an otherwise readable
batch; ordinary user-authored JQL remains strict, and Jira parsing and
permission filtering still apply. Issues unavailable to the current
token/read remain usable but visible as gaps: `complete` is false, affected rows have
`accessible:false`, and their ids are listed in `inaccessible_rows`. Stored
folder summaries are best effort; calculated grouping/generator rows retain
their technical identity instead of risking a misleading label.

`issue_count` describes unique issue identities in the final emitted
root/subtree scope rather than the unfiltered forest. Structure may regenerate
row ids for calculated rows without changing the
expanded plan. Treat `row_id` and `parent_row_id` as snapshot-local identities;
issue keys and item ids remain the durable correlation keys.

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

`atl jira structure export <ID> --out FILE --format json|jsonl|csv|md` writes the
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

JSON and Markdown contain the same normalized snapshot as `structure view`.
JSONL has one self-contained record per row, including schema, structure id,
versions, projection, and row, which makes line-oriented filtering safe. CSV
contains row metadata (`row_id`, `depth`, `relative_depth`, `parent_row_id`, `position`,
`item_type`, `item_id`, `accessible`) plus selected Structure attributes. CSV cells use the
same default formula neutralization as `jira export`; `--raw-csv` disables it
only for CSV and is unsafe for spreadsheet use. Use `pull-issues` separately
when raw Jira issue snapshots are required.

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
