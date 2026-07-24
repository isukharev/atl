# `atl` Output Contract

This document is the authoritative reference for how `atl` communicates results and failures.
It is derived from `internal/cli/root.go` (`codeFor`, `emit`, `emitID`, `writeError`, exit constants).

---

## Output formats

`atl` accepts a global `-o` / `--output` flag (default `json`). The three modes:

| Mode | Flag | What is written to stdout |
|---|---|---|
| **json** | `-o json` (default) | Indented, HTML-unescaped JSON; one object per command |
| **text** | `-o text` | Human-readable text for commands with an explicit text projection; unsupported commands return exit 2 before config, stdin, or network access and never emit JSON |
| **id** | `-o id` | Primary identifier(s) one per line (issue keys, page IDs, attachment IDs) — for safe piping into `xargs`. Only commands that register an id projection support this; others return exit 2 before config, stdin, self-update, or network access |

Shell completion for the three values is registered on the root flag.

### `emit()` — JSON / text output

`emit(cmd, v, textFn)` is the standard result renderer:

- With `-o json`: writes `v` as indented JSON to stdout. HTML escaping is disabled (`&`, `<`, `>` pass through literally).
- With `-o text` and a non-nil `textFn`: calls `textFn()` and writes the result to stdout.
- With `-o text` and a nil `textFn`: returns exit 2 as a defensive backstop;
  the command-tree preflight normally rejects unsupported text before `RunE`.
- With `-o id`: returns exit 2 (usage error) — use `emitID` for commands that export identifiers.

### `emitID()` — JSON / text / id output

`emitID(cmd, v, textFn, idsFn)` extends `emit` with an id projection:

- With `-o id`: calls `idsFn()` and prints each returned string on its own line. No JSON envelope.
- With `-o json` or supported `-o text`: delegates to `emit` (same rules as above).
- Commands that have no meaningful identifier set `ids = nil`; `emitID` then returns exit 2 for `-o id`.

The reviewed text/id inventories annotate the command tree before execution.
They are also the source of truth for `atl capabilities`; the catalog cannot
advertise an output mode that the root preflight would refuse.

### Maintainer-only private workspace migration

The repository's `agent-eval` maintainer tool is outside the shipped `atl`
command tree, but its migration output is also a stable privacy boundary.
Previewing `agent-eval private migrate` emits only this content-free JSON shape:

```json
{
  "schema_version": 1,
  "status": "ready",
  "from_schema_version": 3,
  "to_schema_version": 4,
  "source_sha256": "<hex>",
  "candidate_sha256": "<hex>",
  "migration_sha256": "<hex>",
  "preserved_run_sets": 2,
  "preserved_spec_references": 3,
  "preserved_run_records": 4
}
```

`status` is `ready` for an ordinary preview. The apply result uses the same
schema version, source/target versions, and migration digest with status
`migrated`; an exact interrupted dual-manifest, staged-source, or archived-source
transition returns `recovered`.
After flag parsing, migration-operation errors contain a closed reason code and
never include paths, run-set aliases, case identities, reviewer identities,
models, pricing, or source content.
Apply requires `--expected-migration-sha256` and `--confirm MIGRATE`.

### Qualified Confluence search page

`atl conf search` returns
`{schema_version:1,query,results,count,complete,truncated,partial_reason?,next_cursor}`.
`complete:true` requires a qualified terminal backend page: no continuation
cursor and no pagination anomaly. Legacy/unqualified stores remain
`complete:false`, even with an empty cursor. `-o text` carries the same signal
above a Markdown candidate table; `-o id` remains page ids only. Agents must
continue a cursor or disclose partial search before making an absence claim.

### Capability catalog

`atl capabilities` is an offline, deterministic routing contract. JSON is
`{schema_version:1,routing:{match,reference_load,stop},selection:{task?,service?,access?,id?,count},capabilities:[...]}`.
Each capability contains stable `id`, exact `task_class`, `service`, ordered
`role`/`priority`, `summary`, command path without the `atl` prefix, derived
`access`, derived `output_modes`, `evidence`, `completeness`, and a one-hop
`skill`/`reference` route. `-o text` is a Markdown table and `-o id` emits only
capability ids. The command reads neither config nor credentials and performs
no self-update or backend request. `routing.reference_load` tells an agent to
invoke the named skill first and resolve the reference relative to it; a
filesystem search is deliberately outside the route.
For `jira/structure-planning`, the ordered catalog exposes hierarchy rows,
explicit Structure values with `completeness:"per-row"`, and transient issue
export as separate capabilities.
For `jira/edit`, `jira.issue.worklog.list` exposes the complete baseline and
`jira.issue.worklog.add` routes to the guarded preview/apply command with
`evidence:"hash-bound"` and `completeness:"reconciled"`; catalog entries do
not themselves grant write authority.

### MCP tool results

`atl mcp serve` is a separate stdio protocol transport, so global CLI output
flags and process exit envelopes do not apply to individual tool calls. Each of
the fifteen registered tools has inferred input/output JSON Schema and returns
typed `structuredContent`; compatible clients may also expose the SDK's text
projection. Tool failures set the MCP error result and contain a JSON text
object with stable `kind`, `remediation`, and diagnostic `message` fields.
For transport/API failures, `message` is deliberately coarse and omits backend
paths, query values, and response bodies.

`jira_fields`, `jira_issue_search`, `jira_epic_digest`, and `jira_board_view`
reject a final encoded result larger than `max_bytes` (default 256 KiB,
minimum 1 KiB, maximum 1 MiB). Row/source limits and compact projections remain
independent semantic bounds; exceeding the byte bound is an explicit
`check_failed` result and never silently clips the typed output.

`jira_issue_search` selects ordered returned fields with `columns` (preferred),
`fields`, or `projection`; the latter two are compatibility aliases. At most
one selector may be non-empty, and empty arrays are omitted. The returned
IssueList carries normalized `projection` metadata independently. Unknown input
names and conflicting aliases are rejected before backend access.

`confluence_search` returns the same qualified schema-v1 search envelope as
the CLI, including top-level `complete`, `truncated`, optional
`partial_reason`, and `next_cursor`; candidate page bodies are not included.
`confluence_table_summary` returns the content-free structural summary contract.
`confluence_table_extract` requires one positive table index and returns exactly
that expanded table. Both reject an encoded result larger than `max_bytes`
instead of clipping cells or silently returning a partial structure. Their
error messages do not repeat CSF parser text or malformed cell content.
Each extracted cell's `text` is whitespace-normalized plain text for exact
values and filtering. Its optional `markdown` is also whitespace-normalized and
preserves inline formatting such as links, so clients should select it only when
formatting is part of the requested result. Both representations are untrusted
backend evidence.
`jira_structure_get` projects only `schema_version:1`, `id`, `name`, and
`read_only`; it
never returns owner, permission, saved-view, or raw forest objects.
`jira_structure_view` returns the same normalized schema-v1 snapshot described
below with an explicit field projection. It accepts at most one exact stored
folder selector and fails rather than truncating when the selected hierarchy
exceeds `max_rows` or the encoded snapshot exceeds `max_bytes`. Its row,
unique-issue, projection, accessibility, selection, and completeness fields are
reconciled before emission. A selected snapshot must begin with the exact
selected stored-folder row at relative depth zero; exact path selections are
normalized and compared with the returned path. MCP scans at most 1000 forest
rows and applies that cap before any folder-value query. Raw forest formulas, arbitrary value matrices,
pull, and export are not MCP tools.
`jira_mirror_snapshot` and `confluence_mirror_snapshot` accept an empty object
only. They inspect the exact canonical mirror root selected by
`ATL_MIRROR_ROOT`, require a real `.atl` directory, perform no backend request,
and return the existing fixed-shape Jira or Confluence snapshot contract without
paths, item identities, or content. Local integrity findings are represented by
the returned `complete` and reconciled bucket fields whenever a snapshot can be
formed; root/configuration failures remain classified tool errors. Both always
return `remote_requested:false`.
Unrestricted output properties use the JSON-Schema object form `{}` rather
than the equivalent boolean `true` for broad MCP-client compatibility.

The stable classes come from the same transport-neutral classifier used by CLI
JSON. Clients must branch on `kind`, not parse `message`. Stdout from the server
process is reserved for MCP protocol frames; operational failures are returned
through the protocol rather than mixed into successful tool content.

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

### Binary identity

`atl version` returns the stable object
`{version,commit,build_state}`. `commit` is a full source revision or
`"unknown"`; `build_state` is `"clean"`, `"dirty"`, or `"unknown"`.
Supported Makefile and release builds stamp both values, while an ordinary Go
build may use compiler VCS metadata. The object has no build timestamp and is
informational only: it is not an input to self-update or signature trust.
`atl version -o text` remains the bare version, and `atl --version` retains its
existing one-line Cobra form.

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
including the presentation-only display timezone, typed field descriptors,
and the resolved epic field, in the
sidecar `views` map only, so a later `apply` can reproduce it), so `status` is
unchanged before and after. Render-resolution warnings go to **stderr**, never
stdout.

Every Confluence derived page view begins with
`<!-- atl:document confluence-page v4 -->` and has reserved generated
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

`atl conf blog create` emits
`{id,type,title,space,version,body_present,url}`. Success requires the expanded
POST response to prove a non-empty identity, exact `blogpost` type/space/title,
positive version, and present storage body. `-o text` is one compact
tab-separated record; `-o id` emits only the content id. Invalid/empty CSF and
unsupported/empty Markdown fail before the network. A successful POST with an
incomplete or mismatched response is exit 8 and explicitly an unknown creation
outcome; transport, timeout, throttling, and server failures after dispatch are
unknown for the same reason. None may be automatically replayed. Definitive 4xx
sentinels retain their normal exit mapping.

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

`atl conf snapshot [DIR]` emits the content-free aggregate contract
`{schema_version:1,service:"confluence",remote_requested,complete,reconciled,
local,native,validation,render,remote}`. It intentionally omits root/target,
page identity, title, path, hashes, validation messages, and body/view bytes.
The offline default requires no config or credentials and performs no network
or filesystem writes. Local inspection shares the persistent mutation lock when
it exists. Contention returns a content-free exit `8` before inspection. If a
legacy mirror has no lock yet, the command verifies that no current writer
created it during the read and discards/retries the first result if one did.

`local` partitions `present` into `clean|locally_edited` and
`tracked|untracked`, with `non_canonical` as an explicit untracked subset.
`native` repeats the closed `conf diff` state cardinalities and separately
partitions baselines into `baseline_present|baseline_missing|
baseline_unreadable`, then present baselines into
`baseline_valid|baseline_invalid`. `validation` partitions every native target
into present/absent candidates and every present candidate into valid/invalid;
`unreadable` qualifies inspection failures without exposing their text.

`render` partitions every present native page into present/missing/unreadable
views, then present views into `current|legacy|missing_marker|unsupported`.
Recorded/missing view-state counts form a second exact partition.
`renderer_compatible` is false for unsupported/future or unreadable views. It
is only a format-compatibility statement, not proof that rendering would
preserve edits, and never causes an automatic render. With `--remote`, `remote`
partitions all present local pages into attempted/not-attempted; attempted pages
must be an eligible tracked canonical subset. It then partitions attempts into
checked/unavailable and checked results into in-sync/drifted. One metadata probe
is started per attempted page with generic replay-safe transport retries
disabled. Redirect responses are not followed because a second hop would exceed
the one-attempt bound; they count as unavailable. Without `--remote`, all pages
remain not attempted.

Every nested `reconciled` proves its declared equations and top-level
`reconciled` requires all of them. `complete` is evidence availability, not a
health or publish decision: it becomes false for incomplete native comparison,
unreadable views, or requested unavailable remote evidence. Corrupt baseline
evidence preserves the qualified stdout contract and exit `8`. Any incomplete
local evidence stops before remote configuration, credential resolution, or the
first probe.

`atl conf diff [file.csf|DIR]` is an offline, lock-free comparison with
`schema_version:1`. Its top-level contract is
`{schema_version,root,target,complete,summary,pages}`. Pages are sorted by path
and carry `{id?,title?,path,state,baseline,candidate,semantic_changed?,byte_only?,blocks?,features?,byte_evidence?}`.
`root` and `target` are canonical absolute path identities. The closed `state`
set is `unchanged|added|removed|modified|malformed|missing_baseline|
baseline_mismatch|unreadable`; the summary includes optional
`baseline_mismatch` when non-zero without changing valid v1 plan bytes.
The `-o text` projection keeps the same complete/summary qualification and a
path-ordered Markdown table with `State`, `Page`, mirror-root-relative `Path`,
`Review`, and `Deltas`. `Review` is `semantic` for understood content/feature
changes, `byte-only` for native-byte-only differences, `none` for unchanged
pages, and `n/a` for states that cannot be compared semantically. `Deltas` is
the number of block plus feature deltas; it is not a substitute for `Review`.
The two sides expose only presence, byte length, SHA-256, validity, and
validation diagnostics; block changes expose kind/index/fingerprints rather
than page text. Byte evidence identifies the exact common prefix/suffix and
hashes each changed window. `complete:false` means semantic comparison was not
fully available for at least one page. A scan never treats unreadable or corrupt
mirror state as an empty/clean subtree. `baseline_mismatch` distinguishes a
pristine base whose bytes disagree with its tracked sync hash from filesystem
unreadability.

`conf plan create` writes a private `atl.confluence.plan/v1` artifact with
`{schema,root,target,summary,entries,proposal_hash}`. Entries are strictly
path-ordered `update` records bound to `{id,type,title,space,path,expected_version,
baseline_sha256,candidate_sha256,problems?,blocks?,features?,byte_evidence?}`.
Unknown fields/schemas, duplicate or non-canonical paths, invalid hashes,
inconsistent summaries, and trailing JSON are rejected. The proposal hash is
computed with its own field empty and covers every other byte-semantic field.
The file must also remain byte-identical to atl's canonical indented JSON plus
final newline; reformatting or line-ending conversion is a dirty-plan refusal.
The output path is exclusive: an existing or concurrently-created reviewed
artifact is never replaced.

`conf plan preview` and `conf plan apply` emit
`{schema,proposal_hash,root,target,mode,status,complete,entries}`. Each entry
repeats the review-critical identity, baseline/candidate hashes, and safe
block/feature/byte consequences from the plan before adding its outcome. Mode is `preview|apply`;
top-level status is `would_apply|already_satisfied|blocked|partial|applied`.
Per-entry status is `not_checked|would_apply|already_satisfied|stale|blocked|
not_attempted|applied|failed|unknown`, with expected/final version,
`reconciled`, warning, and coarse failure fields when applicable. Preview and
apply perform the same complete local and remote preflight. `blocked` before
execution means zero PUTs. `partial` is non-zero; `unknown` is non-replayable.
`conf plan preview` is read-only and remains available under the global
read-only policy. `conf plan apply` is execution-only and requires both
`--confirm APPLY` and an exact external
`--expected-proposal-hash`. Exact already-applied remote/local state is the only
resume path accepted in addition to the original baseline state.
Missing plan/root paths are not-found; unreadable or identity-unsafe local paths
are check failures. Lock/preflight failures return `blocked` with
`complete:false`. Drift failures distinguish remote identity, version, content,
and local-ahead-of-remote state.

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

`atl jira snapshot [DIR] [--remote]` emits the content-free aggregate contract
`{schema_version:1,service:"jira",remote_requested,complete,reconciled,local,
native,snapshot,pending,render,remote}`. It intentionally omits root/target,
issue identity, path, hashes, field identity, diagnostic text, and native/raw/
derived content. The offline default requires no config or credentials and
performs no pending-transaction recovery, network, or filesystem writes. Local
inspection shares the persistent mutation lock when it exists. Contention
returns a content-free exit `8` before inspection. If a legacy mirror has no
lock yet, the command verifies that no current writer created it during the
read and discards/retries the first result if one did.

`local` partitions every `.wiki` as clean/edited and canonical
tracked/untracked, with non-canonical copies counted inside untracked. `native`
partitions present and tracked-but-removed substrates by unchanged, modified,
removed, untracked, non-canonical,
missing baseline, baseline mismatch, or unreadable baseline, and independently
reconciles baseline present/missing/unreadable plus valid/invalid. `snapshot`
reconciles expected sibling raw snapshots through present/missing,
readable/unreadable, valid/invalid, and key-matched/mismatched buckets.
`pending` partitions stable records into valid/invalid/unreadable and
bound/unbound, and reports only aggregate field-edit and active-transaction
counts. `render` reconciles expected views through present/missing/unreadable,
current/legacy/missing-marker/unsupported format, and recorded/missing view
state. `renderer_compatible` describes marker readability/compatibility only;
it does not claim the view is unedited or safe to overwrite.

With `--remote`, local preflight runs before backend setup. Any qualified local
integrity failure emits the aggregate, returns exit `8`, and performs no request.
Eligible canonical issues with valid baselines then receive at most one
single-attempt GET each; redirect responses are not followed and count as
unavailable. `attempted = checked + unavailable`, `checked = in_sync + drifted`,
and local `present = attempted + not_attempted`; unavailable never means in-sync
and makes `complete:false`. No form of this command mutates the mirror or backend.

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
accepted view begins with `<!-- atl:document jira-issue v3 -->`; a v2, v1,
missing, or unversioned marker exits `8` before any write and requires an offline
`jira render` or fresh pull before editing. V1 identifies the former generated
bullet form of Subtasks/Epic Children; v2 predates the recorded display-timezone
contract. Neither legacy form is reconstructed as current during apply. A
future/unknown version requires a
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

With `--incremental`, the same result additionally carries `incremental`:

```json
{
  "selector_sha256": "<sha256>",
  "watermark_source": "explicit",
  "watermark_instant": "2026-06-30T22:00:00Z",
  "query_literal": "2026-06-28 22:00",
  "query_literal_basis": "UTC",
  "backend_query_time_zone": "unknown",
  "safety_overlap_hours": 48,
  "complete": true,
  "matched": 3,
  "selected": 2,
  "overlap_skipped": 0,
  "boundary_skipped": 1,
  "view_migrations": 1,
  "next_instant": "2026-07-01T07:42:00Z",
  "boundary_count": 2,
  "watermark_advanced": true
}
```

Incremental and complete pulls also carry the exact command-scoped scheduling
policy (defaults shown):

```json
{
  "scheduling": {
    "page_prefetch": 1,
    "max_in_flight": 1,
    "requests_per_second": 0
  }
}
```

`page_prefetch` overlaps native body reads only. Every mirror/path/asset
side-effect and checkpoint stays in canonical serial order. `max_in_flight`
and `requests_per_second` cover every actual Confluence and optional Jira-macro
transport hop, including retries, redirects, comments, and streamed assets.
Server `Retry-After` extends one shared cooldown. Zero rate means no proactive
pacing, not zero requests.

`watermark_source` is `explicit|recorded|migrated`. Watermark instants are
canonical UTC RFC3339 minutes. `query_literal` is deliberately rendered from
UTC 48 hours before `watermark_instant`; `query_literal_basis` describes that
rendering, while `backend_query_time_zone:"unknown"` explicitly avoids claiming
how Confluence interprets the zone-less CQL literal. `overlap_skipped` counts older hits removed locally. This
over-fetch makes a timezone mismatch conservative rather than lossy. `matched`
is the unique complete search set; `selected` excludes overlap hits and exact
id/version pairs already recorded at the inclusive absolute lower minute.
`view_migrations` is omitted when zero and otherwise counts selected supported
legacy Markdown views whose complete bytes matched an exact pristine
reconstruction. Those views are rewritten in the current format only as their
page pull succeeds. Edited legacy views and unknown/future markers fail the
whole preflight before body GETs or local writes.
`complete:true` is emitted only after terminal
pagination evidence and two identical metadata passes. `watermark_advanced` describes whether the successful run
changed or first persisted the watermark. The private `0600`
`.atl/incremental.json` is versioned, service/selector-hash keyed, and written
atomically only after every selected local page commit succeeds. A cap,
pagination anomaly, local dirty/drift refusal, permission/network failure, or
requested-comment truncation leaves it unchanged. No missing result implies a
remote deletion.

With `--complete`, `pages[]` contains only pages fetched during this invocation,
while `complete_pull.completed` includes a durable prefix resumed from an
earlier invocation:

```json
{
  "root": "mirror",
  "pages": [
    {"id":"300","title":"Gamma","path":"DOCS/gamma/gamma.csf","version":2,"assets":0}
  ],
  "complete_pull": {
    "selector_sha256": "<sha256>",
    "selection_sha256": "<sha256>",
    "source": "resumed",
    "complete": true,
    "total": 3,
    "completed": 3,
    "remaining": 0,
    "checkpoint_active": false
  },
  "scheduling": {
    "page_prefetch": 1,
    "max_in_flight": 1,
    "requests_per_second": 0
  }
}
```

`source` is `new|resumed|restarted`. A successful result always has
`complete:true`, `remaining:0`, and `checkpoint_active:false`; failures are
reported through the normal error envelope and retain the private resume
checkpoint. Before the first body GET for a new/restarted snapshot, two
complete metadata passes must produce the same unique id set and the remaining
local artifacts must pass overwrite preflight. Under the mode-0600
`.atl/complete-pulls/` state, immutable `<selector-sha256>.json` stores only
schema/service hashes and canonical ids; a small
`<selector-sha256>.progress.json` stores the matching hashes and `next_index`.
Neither contains credentials, URL, title, or body, and progress writes do not
rewrite the large manifest. Pull-affecting options are hash-bound. Graceful
failures flush mirror state before advancing `next_index`; a hard crash may
replay the current 25-page batch but cannot skip an uncommitted page. Both are removed
only after every selected page and the final mirror sidecar are durable.
`view_migrations` is present only when supported pristine legacy views were
recognized during preflight. No missing page or retired checkpoint proves a
remote deletion.

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

### Environment time diagnostics

`atl environment inspect` emits an identity- and URL-free
`EnvironmentInspectResult`:

```json
{
  "complete": true,
  "display_time_zone": {"value":"UTC","evidence":"default","source":"default"},
  "jira": {
    "configured": true,
    "status": "available",
    "server_utc_offset": {"value":"+00:00","evidence":"observed","source":"jira_server_time"},
    "user_time_zone": {"value":"Europe/Berlin","evidence":"observed","source":"jira_current_user"},
    "jql_time_zone": {"value":"Europe/Berlin","evidence":"assumed","source":"jira_current_user_time_zone"}
  },
  "confluence": {
    "configured": true,
    "status": "partial",
    "user_time_zone": {"evidence":"unknown","source":"confluence_current_user","reason":"field_not_returned"},
    "cql_time_zone": {"evidence":"unknown","source":"confluence_cql","reason":"not_exposed_by_backend_metadata"}
  },
  "confluence_incremental": {
    "query_literal_time_zone": {"value":"UTC","evidence":"configured","source":"incremental_protocol_v2"},
    "backend_query_time_zone": {"evidence":"unknown","source":"confluence_cql","reason":"not_exposed_by_backend_metadata"},
    "safety_overlap_hours": 48,
    "exact_timestamp_filter": true,
    "hidden_calibration_requests": false
  }
}
```

`evidence` is the closed set `observed|configured|default|assumed|unknown`.
Unknown facts omit `value` and use a closed privacy-safe `reason`; raw transport
or backend error text is never embedded. Backend `status` is
`available|partial|unavailable|not_configured|credentials_missing|credentials_unavailable|invalid_configuration`.
`complete` is false when a configured backend is not `available`; unconfigured
backends remain explicit but do not make another backend incomplete. With both
services available the command makes exactly three sequential GETs and no
search/content request. The command is read-only-policy compatible and has JSON
and text projections.

`atl config show` emits `{ "read_only", "confluence_url"?, "jira_url"?, "update_base_url"?, "render", "jira_list_views", "jira_list_views_error"?, "render_provenance"?, "local_config_path"?, "mirror" }`. `render` is the **effective** merged render configuration (always present; `display_time_zone` defaults to deterministic `UTC`, and both `jira` and `confluence` sections carry at least `profile`, defaulting to `default`). `render_provenance` maps each dotted render key whose value is *not* the built-in default to its source (`global` or `local`) and is `omitempty` — an all-default mirror emits none, keeping the shape backward-compatible. `local_config_path` appears only when a per-mirror `.atl/config.json` is in scope from the current directory. Warnings about forbidden/unknown keys in a local file go to **stderr** as `warning:` lines; stdout stays clean. `config set` accepts `safety.read_only`, Jira list views, or a positional dotted render key (`render.display_time_zone`, `render.{jira,confluence}.{profile,include,exclude}`, plus `render.jira.custom_fields`, `render.jira.field_views`, and `render.jira.epic_field`) alongside the existing URL flags; `field_views` is a JSON descriptor array. The display zone changes only human Markdown date/datetime projections; exact JSON/native timestamps and JQL/CQL semantics are unchanged. `--local` writes the per-mirror file (render keys only — a URL flag with `--local` is a usage error, exit 2).

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
generate batched `id in (...)` / `key in (...)` queries. Explicit selectors are
de-duplicated by first occurrence and found issues are emitted in that order
across pages and batches. Missing/inaccessible identities are omitted without
disturbing the relative order of found rows. User JQL retains backend order.
Stdout remains the normal `emit()` JSON summary:

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
  "row_order": "backend",
  "jql": "project=PROJ",
  "count": 1,
  "backend": {
    "service": "jira",
    "url_hash": "sha256:..."
  }
}
```

For `query_mode: keys|ids`, the manifest instead carries `row_order:
"selector"` and `missing_identity_behavior: "omit"`. Ordering is identical in
JSONL, aggregate JSON, and CSV, for files and artifact-only stdout. Explicit
selection buffering is bounded to one generated batch and 64 MiB of encoded
issue data; the global 250,000 identity safety cap remains in force.

The backend hostname and PAT are never written to the manifest.

`atl conf table summary` returns a bounded content-free table inventory:

```json
{
  "page_id": "123456",
  "table_count": 1,
  "returned_table_count": 1,
  "selection_reconciled": true,
  "tables": [{
    "index": 1,
    "row_count": 3,
    "column_count": 2,
    "rectangular": true,
    "header_row_count": 1,
    "header_cell_count": 2,
    "expanded_cell_count": 6,
    "origin_cell_count": 5,
    "repeated_cell_count": 1,
    "synthetic_empty_cell_count": 0,
    "cell_count_reconciled": true,
    "nonempty_text_cell_count": 6,
    "nonempty_markdown_cell_count": 6,
    "nonempty_raw_cell_count": 2,
    "styled_cell_count": 0,
    "style_entry_count": 0,
    "distinct_style_marker_count": 0,
    "linked_cell_count": 1,
    "rowspan_metadata_cell_count": 2,
    "rowspan_source_cell_count": 1,
    "rowspan_covered_cell_count": 1,
    "colspan_metadata_cell_count": 0,
    "colspan_source_cell_count": 0,
    "colspan_covered_cell_count": 0,
    "warning_count": 0
  }]
}
```

Selecting `--table N` adds `selected_table:N`, limits `tables` to that one
entry, and keeps the page-wide `table_count`; `returned_table_count` and
`selection_reconciled` make that relationship explicit. Every cell count uses
the expanded representation. `origin_cell_count` counts native `th`/`td`
origins, `repeated_cell_count` counts span-covered copies, and
`synthetic_empty_cell_count` counts rectangular padding. A true
`cell_count_reconciled` proves those three counts equal `expanded_cell_count`
and the reported row/column shape.

Direct `rowspan_metadata_cell_count` / `colspan_metadata_cell_count` count every
expanded cell carrying that span metadata, including covered copies; the
existing source and row/column-covered counts retain their coordinate-based
semantics. Non-empty text, Markdown, and raw-attribute counts are separate.
`style_entry_count` sums style-object entries, while
`distinct_style_marker_count` counts distinct key/value pairs. Only the counts
are emitted: the command never emits page titles, cell content, URLs, style
keys/values, raw attributes, or warning text.

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

`atl jira fields` and typed MCP `jira_fields` share one value-free catalog
contract:

```json
{
  "schema_version": 1,
  "source": "jira-field-catalog",
  "complete": true,
  "total": 2,
  "count": 1,
  "fields": [
    {
      "id": "customfield_10001",
      "name": "Delivery Notes",
      "custom": true,
      "schema": "string"
    }
  ]
}
```

`total` describes the source snapshot before client-side filters; `count`
describes the emitted match set. Filtering never upgrades or downgrades source
completeness. Jira's `/rest/api/2/field` response is atomic and non-paginated,
so a successfully decoded non-empty response is `complete:true`. An empty or
legacy/unqualified source is `complete:false` with `partial_reason`; malformed
ids, duplicates, and contradictory qualification fail with exit 8. Field
values are never part of this contract. The text projection begins with
`complete`, `source`, `count`, and `total`, followed by compact tab-separated
field records.

`atl jira issue refs <KEY>` and `atl jira issue refs --jql ...` return
deterministic, provenance-qualified artifact references per issue:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "complete": true,
  "selection": {
    "mode": "jql",
    "count": 1,
    "limit": 100,
    "complete": true
  },
  "summary": {
    "issue_count": 1,
    "complete_issue_count": 1,
    "incomplete_issue_count": 0,
    "reference_count": 1,
    "reference_kind_counts": {"doc": 1},
    "source_count": 2,
    "source_value_counts": {"comments": 2, "description": 1},
    "complete_source_count": 2,
    "incomplete_source_count": 0,
    "truncated_source_count": 0,
    "count_matches_issues": true,
    "selection_count_matches_issues": true,
    "reference_count_matches_kinds": true,
    "issue_summaries_reconciled": true,
    "complete_matches_inputs": true,
    "truncated_matches_inputs": true
  },
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "complete": true,
      "sources": {
        "comments": {"complete": true, "count": 2},
        "description": {"complete": true, "count": 1}
      },
      "reference_summary": {
        "reference_count": 1,
        "reference_kind_counts": {"doc": 1},
        "source_count": 2,
        "source_value_counts": {"comments": 2, "description": 1},
        "complete_source_count": 2,
        "incomplete_source_count": 0,
        "truncated_source_count": 0,
        "reference_count_matches_kinds": true,
        "complete_matches_sources": true,
        "truncated_matches_sources": true
      },
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

The top-level `complete` combines JQL/key selection completeness with every
issue's contributing sources. `selection.truncated:true` means `--limit`
stopped a JQL result while Jira advertised more rows. Each issue qualifies
`description`, `comments`, and every requested `field.<id>` with `complete`,
input-value `count`, optional `text_truncated`, and a bounded warning. Comments
come from the complete paginated comment endpoint; a recoverable comment-source
failure may retain embedded comments but marks that source and the issue
incomplete.

Each issue's additive `reference_summary` is derived from its final emitted
`sources` and deduplicated `refs`. `reference_count` therefore counts a URL once
per issue even if several narrative sources contained it, and always equals the
sum of `reference_kind_counts` when `reference_count_matches_kinds:true`.
`source_value_counts` preserves the existing source names and sums their input
value counts. The top-level `summary` combines those issue summaries, reports
complete/incomplete/truncated source and issue cardinalities, and exposes exact
reconciliation with top-level `count`, `selection`, `complete`, and `truncated`.
References repeated by different issues are counted once for each issue; atl
does not assert that cross-issue URLs represent one evidence use. Consumers
should use these deterministic aggregates instead of recounting nested arrays.

`--fields` selectors are resolved once through the shared Jira field catalog:
technical ids remain direct, while exact case-insensitive display names map to
technical ids before selection and extraction. Field source keys always contain
the resolved technical id. A JQL selection performs one complete paginated
comment listing per issue; callers should use a narrow query and explicit limit
when budgeting backend requests.
All narrative values use the same 128 KiB per-value evidence cap as `epic digest`.
Missing requested fields and clipped values remain incomplete. `-o text` starts
with completeness/selection status, then emits the shared escaped Markdown
table and bounded warnings. An empty `refs` array is evidence of absence only
when both result and issue completeness are true.

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

`atl jira issue field preview <KEY>` and the dry-run form of
`atl jira issue field set <KEY>` share one deterministic single-issue proposal
result. The dedicated preview command is GET-only and available under the
process-wide read-only policy; `field set` is classified as mutating regardless
of flags. The result is:

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
reconciliation reads carry `"reconciled": true`. A stale apply still emits the
`blocked` result and exits 8. Only `field set --apply` can write, and it requires both
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

`atl jira issue worklog list <KEY>` emits
`{key,worklogs:[{id,issue_id?,author:{name?,key?,display_name?,active},comment?,
started,created?,updated?,time_spent?,time_spent_seconds}],total,complete}`.
The adapter consumes every advertised page and rejects missing/changing totals,
offset anomalies, empty incomplete pages, and missing/duplicate worklog ids.
Authors are a closed compact projection: email, avatars, self URL, and timezone
are never present. `-o text` is an escaped Markdown table and `-o id` emits one
worklog id per line.

`atl jira issue worklog add <KEY>` is dry-run by default and emits
`{key,mode,status,time_spent,time_spent_seconds,comment?,started?,author,
current_count,baseline_sha256,proposal_hash,created?,complete,reconciled?}`.
`baseline_sha256` is a deterministic digest of the complete sorted worklog-id
set; it exposes no comment or author value. The schema-v2 proposal hash binds
that baseline digest together with the issue key, normalized
seconds/comment/start time, and current compact author identity. Apply requires
the reviewed hash after a fresh complete baseline, sends exactly one non-retried POST with
`adjustEstimate=leave`, and returns `applied`, `blocked`, `failed`, or
`unknown`. An intervening worklog changes both hashes and blocks before POST.
After an ambiguous response, only one exact newly observed match can
prove `applied`, and that proof requires an explicit review-bound `--started`
timestamp. Every other outcome is non-zero `unknown` and must not be
automatically replayed.

`atl jira issue fields <KEY>` emits
`{key,mode,non_empty_only,count,omitted_empty?,fields:[{id,name,custom,
schema?,empty?,value_type?,value?,truncated?,original_bytes?}]}`. Default mode is `compact`
and omits empty fields. Exact repeatable `--field` selectors accept ids or
case-insensitive display names; ambiguous names fail before the issue read.
Compact user values omit email/avatar/self data, known options/named values use
closed projections, and unknown objects expose only bounded non-empty key names.
Explicit `--include-empty` returns the union of catalog fields and fields
actually observed on the issue, so a populated plugin/private field absent from
the catalog cannot disappear. Explicit `--raw` switches mode
to `raw`, preserves unprojected private values, and writes a privacy warning to
stderr. Explicit `--metadata-only` switches mode to `metadata`, omits `value`
entirely, and emits only the closed coarse `value_type` alongside field
identity/schema/emptiness. It preserves non-empty and `--include-empty`
semantics, including observed plugin fields absent from the catalog, and
conflicts with `--raw` before config/network access. Its `-o text` table has no
value column; compact/raw keep their existing escaped Markdown table.

`atl jira issue field get <KEY> --field <ID-or-name>` emits one qualified,
bounded expansion:

```json
{
  "schema_version": 1,
  "issue": {"id": "10001", "key": "PROJ-1", "updated": "2026-07-01T10:00:00.000+0000"},
  "field": {"id": "customfield_10002", "name": "Delivery Notes", "custom": true, "schema": "string", "present": true, "empty": false, "value_type": "string"},
  "projection": "compact",
  "max_value_bytes": 16384,
  "original_value_bytes": 24,
  "emitted_value_bytes": 24,
  "complete": true,
  "truncated": false,
  "value": "Current delivery status"
}
```

The command resolves exactly one field and reads it together with Jira
`updated`; a technical id does not require a catalog request and uses the id as
its fallback display name. Missing update provenance, ambiguous names, and malformed
values fail closed. `complete` qualifies the compact projection; properties
deliberately excluded by that projection (email, avatar, self URL, and other
transport noise) are outside the contract. The encoded compact `value` is at
most `max_value_bytes` (default 16 KiB, hard range 256 bytes..128 KiB).
`-o text` emits a one-row escaped Markdown table with issue/update/field/value.

Online Jira get/pull/view field selectors resolve exact names through the same
catalog. Render selectors are stored as resolved ids in view state, so offline
render/apply does not depend on a later metadata lookup. Existing technical ids
remain valid without an extra field-catalog request.

`atl jira issue history <KEY>` emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,history,
summary,last_changes?}`. Each history item preserves both `field` and `field_id`
when Jira supplies them. `summary` is derived from the final filtered `history`
array without another backend request. It contains entry/item totals, non-empty
identity/author/timestamp/field counts, explicit `history_id_missing_count` and
`history_nonempty_ids_unique` facts, emitted non-empty `from`/`to` member
counts, status-item count, multi-item-entry count, stable per-field buckets, and
the `count_matches_history` / `fetched_matches_total` consistency checks. Field
buckets use the case-insensitive technical id when available and otherwise the
trimmed case-insensitive display name, then sort by id/name. Thus
`distinct_item_field_count == len(summary.fields)`.

`history_ids_unique` retains its original compatibility semantics over every
emitted id value, including empty values. Use `history_id_missing_count` to
measure absent ids and `history_nonempty_ids_unique` to detect duplicate
non-empty ids without conflating the two conditions.

`summary.chronological_comparable` is false if any emitted timestamp cannot be
parsed. In that state `chronological_ascending` is JSON `null`, rather than a
misleading false; otherwise it is true for a non-decreasing sequence (including
an empty history) or false for an out-of-order sequence. A true
`fetched_matches_total` alone is not proof of completeness: only top-level
`complete:true` means every entry advertised by the chosen backend
representation was consumed. `complete:false` always carries a reason and must
not be interpreted as proof that an omitted change did not happen.
`source` is `paginated`, `embedded`, or `legacy`. Repeatable exact `--field`
selectors and inclusive `--since`/`--until` boundaries are applied locally
after the qualified read. A date-only boundary adds
`filters.boundary_time_zone`, `boundary_time_zone_source:"jira_current_user"`,
and canonical `since_instant` / `until_exclusive_instant`; atl performs one
current-user metadata GET and uses the observed IANA calendar (including DST).
For each requested civil date, the canonical interval spans from its first real
instant through one second after its last real instant. This includes midnight
gaps, folds, and historical repeated-date transitions without omitting
evidence; an entirely skipped requested date has no truthful boundary and fails
closed with exit 8. The local calculation adds no backend request.
Explicit-offset boundaries add only their canonical instant fields and perform
no timezone lookup. Missing/invalid required user timezone fails closed with
exit 8. `last_changes` reports the newest matching change per
selected resolved field within those boundaries. When a selected matching
change carries an unsupported server timestamp, latest-change ordering is
unknowable and the command fails closed with exit 8 instead of emitting
misleading metadata. `-o text` is a status line and a structurally escaped
Markdown table.

With `--summary-only`, the command performs the same qualified read and emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,summary,
last_changes?}`. The raw top-level `history` member is absent by construction;
the projection neither repeats nor broadens the backend request. Its text
renderer contains deterministic facts and field buckets plus bounded
`last_changes` for explicitly selected fields, never the raw history rows.
Omitting the flag preserves the full JSON and text output byte contract.
An explicitly supplied false value, including a later duplicate override, is
rejected with exit 2 before backend access; callers must omit the flag to
request the full raw-history contract.

`jira epic digest` exposes the same fields under `period`. A quarter is resolved
once in the Jira current-user calendar and the resulting zone is passed into
the nested history filter, so a digest adds at most one current-user GET rather
than one per evidence source. Raw user JQL is not changed by either workflow.

`atl jira export ... --out -` is an artifact stdout mode, not a command-result
mode. JSONL emits one `JiraIssueSnapshot` per line, aggregate JSON emits a bare
snapshot array, and CSV emits its header and rows. It emits no manifest, export
result envelope, or trailing status bytes and creates no files. Diagnostics are
stderr-only. `--format`, not the global output flag, selects those artifact
bytes; `-o text` with `--out -` is rejected with exit 2. Aggregate JSON retains
the 10,000-issue/64 MiB caps; row formats
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

`atl jira epic digest <KEY>` emits schema v1 with
`{schema_version,period,includes,sources,epic,status_field?,dod_field?,children?,
comments?,links?,blockers?,history?,refs?,confluence?,staleness,warnings?}`.
`sources` qualifies each attempted component with `complete`, returned `count`,
optional `count_truncated`/`text_truncated`, and a bounded `warning`;
optional-source failure is never encoded as an empty complete result. Reference
completeness includes description, selected status/DoD fields, and comments
whenever those values contribute source text. `children.list` is the common
IssueList contract.
`staleness` contains `evaluated`, `stale`, selected status-field timestamp,
latest newer evidence timestamp, child/comment counts, and deterministic
reasons. It is evidence, not a score. Quarter/date boundaries are inclusive.
Component count/text/request caps and bounded Confluence `page section` results
remain explicit. Links use a total `(key,type,type_name,direction,id)` order.
`-o text` renders source completeness, selected status text,
and child distribution without inventing narrative conclusions.

With `--projection compact`, the same schema additionally contains
`projection:{name:"compact",omitted:[],clipped:[]}` and summary objects for
comments, links, history, and refs. Raw collection members named in `omitted`
are absent; children retain aggregate counts but omit `children.list`.
`clipped` describes projection-level context reduction, independently of the
source-level `complete` and `*_truncated` signals. Consumers must inspect both:
projection clipping is not evidence-source truncation, and neither can be
interpreted as proof of absence. The default `full` JSON remains unchanged.

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
  "structure": {"id": 123, "name": "Planning", "read_only": false},
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

`structure.read_only` is always present, including when it is `false`, so a
known mutable Structure is not confused with missing metadata. Folder `name`
and `parent_folder_id` are also always present strings: a missing label is
`name:""` while `path` uses the stable `folder:<id>` fallback, and a root folder
has `parent_folder_id:""`. Consumers must not substitute the fallback path into
the empty semantic name. `-o id` emits stable folder item ids, not row ids.
Missing/partial labels keep technical ids and statistics, set `complete:false`,
and add bounded warnings.

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
  "structure": {"id": 123, "name": "Quarter plan", "read_only": true},
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
