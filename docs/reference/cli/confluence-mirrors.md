# Confluence mirrors

Search, pull, inspect, validate, reconcile, plan, stage, and push Confluence mirror data.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [`atl conf search`](#atl-conf-search)
- [`atl conf space tree`](#atl-conf-space-tree)
- [`atl conf pull`](#atl-conf-pull)
- [`atl conf status`](#atl-conf-status)
- [`atl conf snapshot`](#atl-conf-snapshot)
- [`atl conf diff`](#atl-conf-diff)
- [`atl conf reconcile preview` / `atl conf reconcile stage`](#atl-conf-reconcile-preview--atl-conf-reconcile-stage)
- [`atl conf plan create` / `atl conf plan preview` / `atl conf plan apply`](#atl-conf-plan-create--atl-conf-plan-preview--atl-conf-plan-apply)
- [`atl conf validate`](#atl-conf-validate)
- [`atl conf edit`](#atl-conf-edit)
- [`atl conf apply`](#atl-conf-apply)
- [`atl conf push`](#atl-conf-push)
<!-- reference-navigation:end -->

## `atl conf search`

Search pages by raw CQL or convenience filters. JSON is a versioned bounded-page
envelope with the exact `query`, `results`, `count`, `complete`, `truncated`,
optional `partial_reason`, and nullable `next_cursor`. Each result carries
`id`, `title`, `space`, `version`, and `excerpt`. Pass either `--cql` or at
least one convenience filter; the two modes cannot be combined.

`complete:true` is emitted only when qualified backend pagination proves the
page terminal. A no-next page exactly at `--limit` remains incomplete unless a
supported exact total proves the observed end is exhaustive. If
`truncated:true`, continue with `--cursor` when present; without one, narrow the
query or investigate the backend evidence. Do not treat missing hits as
evidence of absence. `-o text` renders the same qualification followed by a
Markdown candidate table; `-o id` emits only page ids.

```json
{
  "schema_version": 1,
  "query": "space=DOCS and title~\"API\"",
  "results": [],
  "count": 0,
  "complete": true,
  "truncated": false,
  "next_cursor": null
}
```

```
atl conf search --cql "space=DOCS and title~\"API\"" --limit 10
atl conf search --space DOCS --title API --type page --limit 10
```

Flags:

| flag | description |
|---|---|
| `--cql` | Confluence CQL query (mutually exclusive with convenience filters) |
| `--space` | convenience filter by space key |
| `--title` | convenience substring filter by title |
| `--label` | convenience filter by label |
| `--type` | convenience filter by content type |
| `--limit` | max results, `1..100` (default 25; explicit 0 is invalid) |
| `--cursor` | pagination cursor (start offset returned by the previous call) |

## `atl conf space tree`

Return the page hierarchy of a space. `depth 0` means unlimited.

```
atl conf space tree --space DOCS
atl conf space tree --space DOCS --depth 2
```

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--depth` | maximum depth (0 = unlimited) |

The listing returns at most 2000 pages that match the requested depth while
independently scanning at most 20000 raw pages. Depth-filtered descendants do
not consume the result budget. If either bound prevents proving a complete
listing, the JSON result carries `"truncated": true` and a `warning:` line goes
to stderr.

## `atl conf pull`

Mirror pages to disk. Downloads `.csf` (native storage format), `.md`
(versioned staging view), `.meta.json`, and optionally renders draw.io / image assets and
mirrors page comments.

```bash
# single page
atl conf pull --id 12345678

# all pages in a space
atl conf pull --space DOCS --into my-mirror

# pages matching a CQL query
atl conf pull --cql "label=public and space=DOCS" --assets

# complete historical bootstrap beyond the ordinary 1000/2000 caps; an
# interrupted run resumes its exact private selector snapshot automatically
atl conf pull --complete --cql 'space=DOCS and type=page' --into my-mirror

# Opt in only after reviewing backend capacity: overlap at most four native
# page-body reads while pacing every Confluence/Jira attempt to eight starts/s.
atl conf pull --complete --cql 'space=DOCS and type=page' \
  --page-prefetch 4 --requests-per-second 8 --into my-mirror

# also bring page comments into the mirror
atl conf pull --id 12345678 --comments

# complete changed-page delta; bootstrap with one reviewed absolute instant
atl conf pull --incremental --cql 'space=DOCS and type=page' \
  --since '2026-07-01T00:00:00+02:00' --into my-mirror

# later runs reuse the watermark bound to this exact selector
atl conf pull --incremental --cql 'space=DOCS and type=page' --into my-mirror

# inspect the selected refresh without changing files, state, or checkpoints
atl conf pull --space DOCS --into my-mirror --dry-run
```

Flags:

| flag | description |
|---|---|
| `--id` | single page id |
| `--cql` | CQL query selecting pages |
| `--space` | space key (mirrors the whole space) |
| `--depth` | depth limit when using `--space` (0 = unlimited) |
| `--assets` | download draw.io PNG renders and inline images |
| `--comments` | mirror schema-v2 comment evidence to `<slug>.comments.json`, render its qualified read-only tree in the main `.md`, and refresh the flat `.comments.md` compatibility view |
| `--dry-run` | perform selection and local qualification without writing mirror files, state, watermarks, checkpoints, or stashes |
| `--overwrite-local` | explicitly replace a qualified locally edited native `.csf`; never bypasses derived-view or baseline-integrity failures |
| `--stash-local` | before replacement, preserve a qualified locally edited `.csf` in the immutable content-addressed `.atl/stash/` store; mutually exclusive with `--overwrite-local` |
| `--complete` | build and consume an exact resumable two-pass selector snapshot; requires `--cql` or `--space` and does not support `--depth` |
| `--restart-complete` | explicitly replace an unfinished complete snapshot after a fresh stable selection and local preflight |
| `--incremental` | exhaustively select changes since a persisted selector watermark; requires `--cql` or `--space` |
| `--since` | first-run lower boundary as an exact RFC3339 minute with explicit `Z` or numeric offset |
| `--max-pages` | selection cap: incremental defaults to 10000; complete mode uses `0` as no configured cap (the local one-million-identity / 64 MiB checkpoint guards still apply) |
| `--page-prefetch` | ordered native-page-body read window for CQL/space, incremental, and complete pulls (`1` default, max `8`); mirror writes/checkpoints remain serial |
| `--requests-per-second` | shared request-start pace across Confluence plus optional Jira-macro traffic for a scheduled pull (`0` default means no proactive delay; max `1000`) |
| `--jira-view` | named `jira_list_views` projection for Jira JQL macros whose macro configuration does not specify columns |
| `--jira-macros` | `auto` (default) or `off`; `off` keeps placeholders and performs no Jira credential read/search |
| `--into` | mirror root directory (default `mirror`) |
| `--render-profile` | `.md` view profile: `minimal` \| `default` \| `full` (see [Render profiles](rendering.md#render-profiles)) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |

At most one of `--id`, `--cql`, `--space` may be given.

Pull is non-destructive by default. Before each page-body GET, atl reconciles
the tracked path, sidecar hash, pristine base, native `.csf`, metadata, and
derived Markdown view. A local native edit or unqualified artifact is preserved,
reported under `local_safety`, and skipped while safe siblings continue. The
command then exits `8`, so automation cannot mistake a partial refresh for a
complete one. `--overwrite-local` and `--stash-local` apply only to a native
edit whose baseline is fully qualified; they do not discard unapplied Markdown,
future/unsupported views, missing artifacts, path drift, or corrupt state.
Stashes contain the exact previous native bytes and are named by their SHA-256.
Incremental watermarks and complete-pull checkpoints never advance past a
blocked page.

Ordinary `--cql` and `--space` pulls remain on the unscheduled service path by
default. Explicit `--page-prefetch 2..8` overlaps only their native body reads;
`--requests-per-second N` may instead install a rate-only schedule with one
request in flight. Selection order, ordinary caps and truncation reporting,
local qualification, and every mirror write remain unchanged and serial.
Explicit default values `--page-prefetch 1 --requests-per-second 0` do not
install or report a scheduler.

Complete mode is the explicit historical bootstrap for a selector larger than
the ordinary CQL/space caps. It exhausts qualified search pagination twice,
requires the same unique page-id set in both passes, canonicalizes that set
locally, and only then starts page-body GETs. Missing/duplicate identities,
repeated cursors, contradictory totals, a full no-next page without trusted
terminal evidence, unreachable advertised results, selection drift, or an
explicit cap fail with exit `8` before any body request or new checkpoint.
User CQL containing `ORDER BY` is rejected; atl does not depend on an
undocumented id-order guarantee from the backend.

The exact identity snapshot and its durable prefix live in private,
schema-versioned state under `.atl/complete-pulls/`: an immutable mode-0600
`<selector-sha256>.json` id manifest, a small progress file, a bounded accepted
page journal, and at most one mode-0700 publication directory. Control files
contain only ids, hashes, paths, render state, bounded content-free write
tokens, and progress — never credentials, backend URLs, titles, or page bodies.
The transient publication directory holds one page's exact private payloads
until its canonical artifact set is durable. Progress updates do not rewrite
the large manifest. Repeating the same command resumes the remaining prefix
without repeating accepted body GETs. Assets,
comments, effective render settings, and the resolved Jira-macro list view are
hash-bound; option drift fails closed. `--restart-complete` replaces an old
snapshot only after a fresh two-pass selection and local overwrite preflight
succeed, so a failed restart leaves the previous resume point intact.

Before body reads, native/Markdown local edits and partial/corrupt tracked
artifacts block the exact remaining set. Every destination-side atomic write
uses an exact temp name owned beforehand by the surviving publication intent or
journal. Recovery removes or reuses only that exact bounded regular-file
residue, accepts only reviewed pre/post images, and preserves anything else for
inspection. Accepted pages enter the bounded journal before batch sidecar and
progress commits, so a hard crash does not repeat their body GETs and cannot
skip them. Page downloads
are serial by default. `--page-prefetch N` may overlap up to `N` native body
GETs and can therefore read a bounded tail beyond the first page that later
fails; only the canonical sequential consumer claims paths, resolves/writes
assets and sidecars, performs relocation, or advances a checkpoint. This mode
intentionally costs two metadata search passes plus one body GET per selected
page; it runs only when explicitly requested and performs no background or
calibration queries. Requested comment truncation does not advance past that
page. Completion removes the checkpoint; neither a snapshot nor its absence is
evidence of remote deletion.

Incremental mode is deliberately inclusive at its lower minute. The first
`--since` is an absolute RFC3339 instant, so a DST fold or the timezone of the
machine running atl cannot change it. Atl canonicalizes the watermark to UTC.
Confluence CQL date literals have only minute precision and carry no offset;
the effective backend parser timezone is therefore reported as unknown rather
than inferred through hidden calibration searches. Every CQL read renders a
UTC-based literal 48 hours before the absolute boundary and locally discards
older hits by their exact REST timestamps. Across the IANA offset range, a
different backend CQL zone can only over-fetch rather than omit; the explicit
`--max-pages` cap remains fail-closed. Atl records
every page id/version at the completed absolute minute. A repeat skips only
those exact pairs: a new page or newer version in the same minute is still
fetched. Proven `absolute-overlap-v1` watermarks migrate to canonical UTC only
after a complete successful run; older state without a bound absolute instant
is rejected with guidance to preserve the old mirror. Results are paged until the
backend proves exhaustion, then the metadata pass is repeated and its
`(id,version,updated)` set must match before any body is fetched. Only
`type=page` hits are admitted. A repeated cursor, contradictory or unreachable
advertised total, explicit cap, or malformed timestamp exits `8` and leaves the
watermark unchanged. `ORDER BY` in user CQL is rejected because atl appends
`lastmodified asc`; there is no dependency on an undocumented id tie-breaker.

Before the first page body fetch/write, the entire selected local set is
preflighted. Native CSF edits, unapplied Markdown edits, partial page artifacts,
or corrupt state block the batch. A supported v5/v4 legacy `.md` is accepted
only if its version-specific renderer reproduces every byte;
`view_migrations` counts those proven views, and each is rewritten
to the current format only when its page pull succeeds. A changed legacy view
gets a legacy-specific reconciliation error, while unversioned and
unknown/future views are preserved and never downgraded. A network/permission failure may leave pages
already mirrored through the ordinary atomic path, but never advances
`.atl/incremental.json`; rerunning replays the same inclusive range safely.
Empty deltas still commit a valid first watermark. Absence from a delta is
never interpreted as deletion or permission loss. Requests are serial in this
mode by default; the stability check intentionally doubles search-page GETs
but not body GETs. Opt-in prefetch has the same sequential write/watermark
boundary as complete mode. `--comments` truncation also prevents watermark
advancement.

Both large modes, plus any ordinary pull with an effective scheduling opt-in,
expose a `scheduling` result with `page_prefetch`, `max_in_flight`, and
`requests_per_second`. The command-scoped scheduler is
shared by Confluence and optional Jira-macro clients and wraps each actual
transport hop, including retries, redirects, comments, and streamed assets. It
holds an in-flight permit until the response body reaches EOF or closes, paces
request starts, and publishes a bounded server `Retry-After` cooldown to all
clients. Existing requests may finish, but no newly admitted attempt bypasses
that cooldown. Defaults are `1/1/0`, so incremental and complete pulls do not
increase backend load. Unscheduled ordinary pulls omit `scheduling`. The
limits are proactive safety bounds, not an adaptive throughput promise.

The `--render-*` flags override the configured profile for this run; the pull
result JSON is unchanged by the profile (they affect only the `.md` view).

Jira JQL macros are enriched on a best-effort read path when Jira credentials
are configured. Their original placeholder stays in `# Content`; resolved rows
use the shared Jira IssueList Markdown table under generated readonly `# Jira
Queries`. Explicit macro columns win, otherwise `--jira-view` selects the
`confluence_macro` projection (`default` when omitted). Pull records the typed
snapshot in `<slug>.jira-macros.json`, allowing offline render and apply to
reproduce the exact generated suffix without rerunning JQL. A missing Jira
configuration or one failed query retains the placeholder and emits a bounded
stderr warning; it never blocks the native page pull. Resolution is capped per
page at 20 JQL macros and 2000 total rows (1000 per macro); omitted macros stay
as placeholders and are reported.

Set `render.confluence.jira_macros` to `off`, or pass `--jira-macros off` to
`conf pull` / `conf page view`, when page-provided JQL should not execute with
the current user's Jira identity. The opt-out is resolved before Jira
credentials are loaded, retains readable placeholders, removes any generated
query sidecar on pull, and emits a bounded warning. `--jira-view` is invalid
while expansion is off.

`--comments` is opt-in: without it, no comment endpoint is contacted and no
comment files are written. Comments are auxiliary read-only data — they never
enter the page content hash or the version gate, so a page carrying comment
sidecars still reports Clean in `conf status`. Each comment retains a plain-text
`body` fallback and, when supplied, native `body_storage` CSF so the readonly
Markdown preserves paragraphs, lists, links, emphasis, and headings. It is not
part of the page write substrate. A re-pull **with**
`--comments` rewrites the sidecars; a re-pull **without** `--comments` leaves any
existing comment files untouched (they are never auto-deleted). If a page's
comment listing hits the fetch safety cap, the sidecar is incomplete, the meta
carries `comments_truncated: true`, and a stderr warning fires. Comment-enabled
complete/incremental pulls do not advance their checkpoint when comment or
thread completeness is false; anchor-only partiality remains durable but does
not block selection progress.

**Mirror layout after pull**

```
mirror/
  DOCS/
    parent-page/
      child-page/
        child-page.csf           ← edit this
        child-page.md            ← derived staging view; with schema-v2 comments, includes the qualified read-only tree
        child-page.meta.json     ← id, version, hierarchy, labels, updated, optional restricted, content_hash, fragments, comment state
        child-page.comments.json ← only with --comments: qualified schema-v2 envelope (legacy arrays remain readable)
        child-page.comments.md   ← only with --comments: best-effort flat compatibility view
        child-page.assets/
          diagram.png
  .atl/
    state.json                 ← remote sync, render, and staged-local lineage
    incremental.json           ← completed selector-bound lower boundaries (0600)
    base/
      12345678.csf             ← pristine copy for diff
```

Confluence pull/render/apply/push and mirror-local `conf edit` are serialized by one persistent advisory
lock under `.atl`; contention exits `8` before page/state writes. Wait for the
active operation—do not remove the lock file. Read-only status stays lock-free.
When Jira and Confluence share the root, their sidecar patches also use the
backend-neutral `.atl/state.lock`: a collision gets a brief bounded retry
window, then fails closed rather than losing the other service's `state.json`
entries.

Pull and direct page reads require the requested body projection from the
backend (`body.storage.value` for CSF, `body.view.value` for rendered view). A
successful partial response that omits it exits `8` before output/artifacts are
treated as an empty page; an explicitly present, zero-byte body is accepted.
If only the refresh after a successful push omits the body, atl preserves
the local mirror and reports a re-pull warning instead of replacing it with an
empty page.

Missing local targets for `conf render`, `conf apply`, and `conf push` all map
to exit `4` (`not found`). Malformed target kinds or incompatible flag
combinations remain exit `2` (`usage`). Offline render may migrate pristine
v5/v4 views after exact version-specific reconstruction, but refuses to
overwrite edited legacy, older historical, unversioned, or unknown/future
views. Directory renders inspect every selected
view first and make no view changes if any selected marker is unsupported.

## `atl conf status`

Show which mirrored pages have local edits and which have drifted on the
remote since the last pull.

```bash
atl conf status
atl conf status my-mirror
atl conf status --into my-mirror
atl conf status --remote          # checks remote versions in bounded qualified batches
```

Local edits are shown with `M`; remote drift with `M↯` in text mode.
Missing, corrupt, or id-less sibling `.meta.json` is a mirror integrity failure:
the scan stops and returns exit `8` instead of silently omitting that page.

Flags:

| flag | description |
|---|---|
| `[DIR]` | initialized mirror root directory |
| `--into` | initialized mirror root directory (mutually exclusive with `[DIR]`) |
| `--remote` | also check remote for drift (qualified batches for multiple pages; exact read for one) |

With neither explicit form, inspection uses `ATL_MIRROR_ROOT`, then the nearest
initialized `.atl` walking up from the current directory, then `mirror`.
An absent or uninitialized selected root returns not-found exit 4 before config
or network access.

## `atl conf snapshot`

Return exact, content-free health cardinalities for a durable Confluence mirror.
The offline default needs no backend URL, PAT, or config and performs no writes.
It takes a shared advisory lock only when the persistent mutation lock already
exists. An active mutation fails closed with exit `8` before inspection; a
legacy mirror without that lock remains write-free and is re-inspected if a
current writer creates the lock during the first read.

```bash
ATL_READ_ONLY=1 atl conf snapshot
ATL_READ_ONLY=1 atl conf snapshot my-mirror
ATL_READ_ONLY=1 atl conf snapshot --into my-mirror
ATL_READ_ONLY=1 atl conf snapshot my-mirror --remote
```

The JSON partitions local clean/edited and tracked/untracked pages, every native
diff state, baseline presence/validity, candidate CSF validity, and derived-view
marker state. Current, known legacy, missing-marker, unsupported/future,
missing, and unreadable views remain distinct. `renderer_compatible` reports
only whether this binary understands the observed format; it does not prove a
view has no edits, and the command never rewrites that view.

Every section has a `reconciled` flag and the top-level flag requires all
partitions to agree. `complete:false` means some requested evidence is not
usable (for example malformed or corrupt native state, an unreadable derived
view, or a failed requested remote probe); it is separate from arithmetic
reconciliation. Any incomplete local evidence stops before remote setup or
probing. A corrupt baseline still emits the qualified snapshot and returns exit
`8`, even when `--remote` was requested. If writing that snapshot to stdout
fails, the write failure is reported together with the inspection failure and
the exit code stays the inspection code. If inspection otherwise succeeds, the
write failure is returned on its own with generic exit `1`.

`--remote` keeps the exact metadata endpoint for one eligible canonical tracked
page. Larger selections use completeness-qualified metadata batches of at most
100 page ids and 16 KiB of escaped selector input. Each batch gets one transport
attempt with automatic replay-safe retries disabled; it is accepted only when
the response contains every requested id exactly once with a positive version
and proves terminal pagination. A typed error, omitted/duplicate/unexpected row,
invalid version, or unqualified continuation makes that whole batch unavailable
without per-page fallback or permission inference. Redirect responses are not
followed. Untracked/non-canonical pages remain `not_attempted`; failures
increment `unavailable` and never `in_sync`. The output never includes page ids,
titles, paths, hashes, validation text, or native/derived content. Use `conf
diff` only when page-level identity or exact change evidence is required.
Snapshot accepts the same mutually exclusive `[DIR]`/`--into` forms, root
precedence, and pre-network exit-4 initialized-root check as `conf status`.

## `atl conf diff`

Compare the exact last-synced native `.csf` baseline with one current page or
every tracked page in a subtree. The command is offline, makes no backend
requests, and never converts the write substrate through Markdown.

```bash
ATL_READ_ONLY=1 atl conf diff mirror/DOCS/guide/guide.csf
ATL_READ_ONLY=1 atl conf diff mirror/DOCS/ -o text
```

JSON uses `schema_version: 1` and reports a stable path-ordered `pages` array.
Each page state is one of `unchanged`, `added`, `removed`, `modified`,
`malformed`, `missing_baseline`, `baseline_mismatch`, or `unreadable`. Root and
target are canonical absolute identities, so relative roots and contained
symlink aliases cannot split one mirror into duplicate path namespaces.
Modified pages include:

- semantic block changes using canonical DOM fingerprints (attribute order and
  equivalent entity spelling do not create semantic changes);
- aggregate macro and opaque-fragment count deltas without copying page text;
- exact SHA-256/byte-count evidence for the changed byte window;
- candidate and baseline validation consequences.

`byte_only:true` means native bytes changed while the understood block and
feature projections remained equivalent. `complete:false` means at least one
comparison lacked a usable baseline or a body was malformed/unreadable. Missing
pre-upgrade baselines are never guessed: re-pull to establish one, preserving
any local edits first. `baseline_mismatch` specifically means the pristine base
bytes no longer match the tracked sync hash; preserve local candidates and
repair/re-pull the mirror rather than treating it as an unreadable page.
Directory scans fail closed on corrupt metadata,
descendant symlinks, or unreadable entries instead of silently omitting pages.
The Markdown projection is a compact review table. Its paths are relative to
the mirror root, `Review` is the closed `semantic|byte-only|none|n/a`
classification, and `Deltas` counts understood block plus feature changes. Use
`-o text` first for directory triage; use JSON when an agent needs canonical
absolute evidence paths, block hashes, feature deltas, byte windows, or
validation details.

Flags:

| flag | description |
|---|---|
| `[file.csf\|DIR]` | page or subtree; omitted uses the configured mirror root |
| `--into` | explicit mirror root (otherwise nearest `.atl`) |

## `atl conf reconcile preview` / `atl conf reconcile stage`

Use reconcile after a version conflict or before deciding how to preserve
independent local and remote edits to one tracked page:

```bash
ATL_READ_ONLY=1 atl conf reconcile preview mirror/DOCS/guide/guide.csf
atl conf reconcile stage mirror/DOCS/guide/guide.csf
```

Both modes perform exactly one single-attempt remote page read after the local
canonical path, sidecar, metadata, and pristine baseline have been qualified.
They compare exact `base`, `ours`, and `theirs` native bytes and report the
closed state `unchanged|local_only|remote_only|diverged`; `converged:true`
means local and remote made the same byte-exact change. Output contains hashes,
sizes, versions, content-free block deltas, explicit bounds, and a proposal
hash, never page bodies. Invalid CSF, inconsistent same-version bytes, more
than 16 MiB per body, more than 4096 semantic blocks, or more than one million
alignment cells fails closed with exit `8`.

`preview` is read-only and never creates files. `stage` is separately
mutation-classified and writes exact immutable review artifacts beneath
`.atl/reconcile/confluence/`; it never changes the working `.csf`, `.md`,
metadata, baseline, or sidecar. Repeating the same stage is idempotent; a
different pre-existing artifact is preserved and blocks the operation. ATL
does not delete these artifacts automatically.

## `atl conf plan create` / `atl conf plan preview` / `atl conf plan apply`

Use a durable plan when several native page updates must be reviewed as one
closed set. Plan creation is offline and accepts the same page/subtree target as
`conf diff`:

```bash
export ATL_READ_ONLY=1
atl conf plan create mirror/DOCS/ --out .atl-private/docs-plan.json
```

The output file has mode `0600`, schema `atl.confluence.plan/v1`, deterministic
bytes, and a proposal hash over the complete artifact. It includes only
`update` entries for modified, canonical, valid, baseline-backed pages. Every
entry declares page content type and binds the content id, title, space, mirror-relative path, expected
version, exact baseline/candidate SHA-256, validation warnings, semantic block
and feature consequences, and byte-window evidence. Native CSF bodies are not
copied into the plan. Added, removed, malformed, missing-baseline,
baseline-mismatch, unreadable, or relocated pages make creation fail before the
artifact is written.
Do not reformat or convert the line endings of a plan: apply requires the exact
canonical bytes as well as the embedded and externally reviewed hashes.
`--out` must name a new file: atl never replaces an existing reviewed artifact.

Plan files contain page titles and local workspace paths. Keep them private; do
not commit or publish them even though body prose is omitted.

Preview without leaving the intentionally exported global read-only environment:

```bash
atl conf plan preview .atl-private/docs-plan.json
```

Preview revalidates the complete local plan, then GETs every remote page before
any write. Each entry becomes `would_apply`, `already_satisfied`, `stale`,
`blocked`, or `not_checked`. If any local/remote binding changed, the batch is
blocked with zero PUTs.

After reviewing the exact proposal hash and obtaining approval:

```bash
env -u ATL_READ_ONLY atl conf plan apply .atl-private/docs-plan.json \
  --expected-proposal-hash <64-hex-hash> \
  --confirm APPLY
```

Apply repeats the complete preflight, then sends one version-gated PUT per
pending entry. Every response is reconciled with a native GET. Exact
`expected_version+1` candidate state is `applied` (and may be marked
`reconciled`); a prior exact success is `already_satisfied` and is never
replayed. A failed or unknown outcome stops the remaining writes, marks them
`not_attempted`, returns non-zero, and must not be automatically replayed.
Rerunning the same plan is safe after inspection: exact applied entries are
recognized and their mirror state is refreshed, while any other state is
blocked. There is no force mode, remote delete, create, move, or automatic
merge in v1.

`conf plan apply` is execution-only: omitting either the exact external hash or
`--confirm APPLY` exits 2 before config, plan loading, or network access.

Create flags:

| flag | description |
|---|---|
| `[file.csf\|DIR]` | one page or subtree; omitted uses the configured mirror |
| `--into` | explicit mirror root |
| `--out` | required new durable plan path; stdout and replacement are unsupported |

Apply flags:

| flag | description |
|---|---|
| `--confirm APPLY` | required exact confirmation for execution |
| `--expected-proposal-hash` | exact reviewed hash; required for execution |

## `atl conf validate`

Validate a `.csf` file for XML well-formedness, supported structural depth, and
common sanity issues. Well-formedness and `max-depth` errors (severity
`"error"`) block a push. Sanity problems (severity `"warning"`) are advisory.

```bash
atl conf validate mirror/DOCS/guide/guide.csf
```

`<file.csf>` is the required local file to validate.

Flags:

| flag | description |
|---|---|
| `--cloud-compat` | also report advisory Confluence Cloud compatibility findings (`cloud-compat/*` warnings; never blocks) |

Output (JSON):

```json
{
  "file": "mirror/DOCS/guide/guide.csf",
  "ok": false,
  "problems": [
    {
      "severity": "error",
      "line": 14,
      "col": 5,
      "rule": "well-formedness",
      "message": "malformed CSF: XML syntax error on line 14: unexpected end element </p>"
    }
  ]
}
```

Exits 8 (`check_failed`) when any error-severity problem is found; the result
and `problems[]` are still emitted. Exits 0 when there is no blocking problem.

`max-depth` rejects CSF nested beyond 1024 elements before recursive rendering
or inspection. The diagnostic contains only the observed depth and limit, not
document content.

Advisory `invisible-chars` warnings flag characters that render invisibly but
defeat exact-string editing — non-breaking spaces (`U+00A0`), zero-width
characters, soft hyphens — one warning per class with the occurrence count and
first position. They never block a push; use `atl conf edit` (tolerant
matching) when they are present.

#### `--cloud-compat` — advisory Cloud compatibility inventory

Opt-in. Without the flag nothing changes: the default diagnostics and the
default JSON object (`{file, ok, problems}`) are exactly as before. With the
flag, `conf validate` appends `cloud-compat/*` findings to `problems[]` in
document order after the default diagnostics, and adds one `cloud_compat`
object that identifies the rule pack:

```json
{
  "cloud_compat": {
    "rule_pack": "v1",
    "source_date": "2026-07-25"
  },
  "file": "mirror/DOCS/guide/guide.csf",
  "ok": true,
  "problems": [
    {
      "severity": "warning",
      "line": 1,
      "col": 4,
      "rule": "cloud-compat/macro-not-insertable",
      "message": "macro \"info\" cannot be inserted in the Confluence Cloud editor; Atlassian documents a Cloud editor migration or conversion path for existing content"
    },
    {
      "severity": "warning",
      "line": 2,
      "col": 23,
      "rule": "cloud-compat/nested-table",
      "message": "table nested inside another table; the Confluence Cloud editor does not support nested tables"
    }
  ]
}
```

Every `cloud-compat/*` finding has severity `"warning"`, so the flag can never
change `ok`, the push gate, or the exit status of an otherwise valid page.

The `v1` rule pack is closed — these five stable rule names and no others:

| rule | what it reports |
|---|---|
| `cloud-compat/macro-not-insertable` | a listed macro that cannot be inserted in the Cloud editor; Atlassian documents how existing content migrates or converts |
| `cloud-compat/macro-view-only` | a listed macro removed from both Cloud editors; existing instances stay visible but cannot be inserted or edited |
| `cloud-compat/macro-removed` | a listed macro removed from Confluence Cloud |
| `cloud-compat/nested-bodied-macro` | a macro carrying a body nested inside another macro, which the Cloud editor does not natively support |
| `cloud-compat/nested-table` | a table nested inside another table, which the Cloud editor does not support |

The macro category is carried by `rule`, never by the message prose, so tooling
can branch on the rule name without parsing text. Findings are anchored with
1-based `line`/`col` into the original bytes.

`rule_pack` and `source_date` are part of the output because Atlassian's
support pages change over time: `source_date` records when the pack was last
reconciled against the official documentation on
[what migrates with the Confluence Cloud Migration Assistant](https://support.atlassian.com/migration/docs/what-migrates-with-the-confluence-cloud-migration-assistant/),
[which macros are being removed](https://support.atlassian.com/confluence-cloud/docs/learn-which-macros-are-being-removed/),
and [legacy editor vs. Cloud editor differences](https://support.atlassian.com/confluence-cloud/docs/differences-using-confluence-legacy-editor-and-cloud-editor/).
Record both values alongside any finding you keep.

The pack is deliberately conservative. Read its output as an inventory, not a
verdict:

- It does not predict or guarantee migration success or failure. A clean run is
  not a promise that the page migrates intact, and a finding is not a claim that
  migration will fail.
- Only macro keys named explicitly on Atlassian's official compatibility list
  are classified. An unlisted marketplace app, user, or unknown macro is never
  guessed at, so an absent finding says nothing about it.
- It converts nothing. There is no CSF-to-ADF conversion, no Cloud or Data
  Center API call, no migration execution, and no file is written — validation
  stays offline and read-only.
- A body that is not well-formed short-circuits before the pack runs, because
  the rules need a parsed document. The result still carries `cloud_compat` and
  the well-formedness error, but no `cloud-compat/*` finding — and their absence
  is not a clean bill of health. Fix the XML error, then re-run with the flag.

## `atl conf edit`

Replace text in a local file with tolerance for the invisible bytes that break
exact-match editing of real CSF (non-breaking spaces `U+00A0`, zero-width
characters, `&nbsp;`/`&#160;`/`&#xa0;` entities). Matching runs in layered
passes — exact bytes, then invisible-tolerant, then whitespace-run-tolerant —
and the replacement is spliced into exactly the matched byte range; every
surrounding byte is preserved verbatim. The replacement itself is inserted
literally.

```bash
atl conf edit page.csf --old 'Запрос предназначен для получения' --new 'Запрос возвращает'
atl conf edit page.csf --old-file old.txt --new-file new.txt --dry-run
atl conf edit page.csf --old '<td>ok</td>' --new '<td>done</td>' --all
atl conf edit page.csf --old ' obsolete sentence.' --new ''          # delete
```

Flags:

| flag | description |
|---|---|
| `<file>` | local file to edit (positional, required) |
| `--old` | text to find (tolerant matching) |
| `--old-file` | read the text to find from a file (`-` for stdin; one trailing newline stripped) |
| `--new` | replacement text, inserted verbatim (`--new ''` deletes) |
| `--new-file` | read the replacement from a file (one trailing newline stripped) |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match without writing |

Output includes which pass matched (`"pass"`), match count and byte offsets,
and quoted `region_before`/`region_after` context for review. For `.csf`
files the result is validated automatically: `"csf_ok"` plus `"problems"`
appear in the output, and a not-well-formed result prints a stderr warning
(the file is still written; `conf push` remains the gate).

When the file belongs to an initialized mirror, dry-run and write both join the
same persistent Confluence mutation lock as pull/render/apply/push; contention
exits 8 before reading or changing the file. Symlink targets are resolved before
lock discovery; an alias outside the mirror joins the target mirror's lock, and
an alias visibly inside a mirror that resolves outside it is refused.

Exit codes: `4` — the target file is missing or the text was not found in any
pass (a no-match error carries a quoted dump of the closest region, exposing
hidden bytes); `2` — the match is ambiguous (make `--old` more specific or pass
`--all`).

Usage notes: keep `--old`/`--new` short and inline — match an anchor around
the change, not a whole sentence or table row; `--old-file`/`--new-file` are
for content that already lives in a file, not worth a write-then-clean-up
ceremony. The command is atomic (a miss leaves the file untouched), so
`--dry-run` is only needed for risky substitutions such as `--all`. For long
spans (deleting a section, replacing a table row) splice between two short
boundary anchors with a checked script instead of matching the full span —
see the confluence skill's CSF reference for the decision table.

## `atl conf apply`

Merge edits from a page's markdown view (`page.md`) into its `.csf`, block by
block. The markdown file becomes an editable surface: blocks you did not touch
keep their **exact base bytes**; changed or new blocks are converted from a
strict markdown subset (headings, paragraphs, lists, task lists, simple
tables, fenced code, blockquotes/admonitions, links, legacy `[[Page Links]]`
(canonicalized to identity-bearing `confluence-page:` links),
`[KEY](jira:KEY)`); opaque elements in edited blocks (macros, mentions,
links, images) keep their original bytes. Local only — `conf push` remains
the write path to the server.

Tables with editor styling or native structure (table/row/cell attributes,
caption/column metadata, header topology, wrapper divs, or spans) are merged
**row/cell-wise** rather than converted: untouched rows keep
their exact bytes; an edited cell has its converted content spliced into the
existing cell wrapper (styles and classes survive); a deleted row drops its
byte range (the fragment-loss gate still applies to macros/mentions it held);
an inserted row clones the byte structure of a neighboring row, so numbering
columns and per-cell styling carry over. Mentions and links copied from an
untouched row into an edited cell are cloned byte-exactly; macros are never
cloned (a copy would duplicate the macro identity).

```bash
atl conf apply guide.md --dry-run              # report without writing
atl conf apply mirror/DOCS/guide/guide.md
atl conf apply guide.md --allow-fragment-loss  # intentional macro/mention removal
```

| flag | description |
|---|---|
| `<page.md>` | the page's markdown view (positional, required) |
| `--dry-run` | report without committing `.csf` or regenerated `.md` view changes |
| `--allow-fragment-loss` | proceed when the edit drops opaque fragments |
| `--into` | mirror root (defaults to nearest `.atl`) |

The first line must be `<!-- atl:document confluence-page v6 -->`. V5 predates
the closed fence/break/table staging contract, while v4 predates
the qualified main-view comment tree, and v3 predates the recorded
display-timezone contract. Apply rejects missing/legacy/unknown
versions and additions, removals, renames, or reordering of reserved
`<!-- atl:... -->` marker text in the editable body before writing. Marker prose
that already came from native page content is allowed when left unchanged.
Pristine v5/v4 views migrate only after exact version-specific reconstruction.
Dirty v5/v4, older historical, and unversioned views are preserved and refused;
an unknown/future version requires a newer `atl` and is never downgraded.

The v6 body renderer chooses a code fence longer than any backtick run in the
native body, reversibly escapes paragraph text that would otherwise parse as a
fence or thematic break, and renders native `<br>` nodes as protected `<br>`
markers. Editing adjacent prose preserves the exact original break bytes.
Deleting protected break/structural markers participates in the explicit
fragment-loss gate; unrepresentable structural rewrites fail before `.csf`
changes. Code-macro metadata that cannot be expressed by a safe Markdown fence
(including unsafe language text, macro ids, or extra parameters) is likewise
loss-gated instead of being silently discarded by a body edit.

All views carry generated document/body boundaries. When the page was pulled
under every profile the body starts at visible `# Content`; `full` also carries
read-only `# Metadata` and `# Comments` sections. Native page headings keep
their original levels; comment headings are nested under their comment entry.
Generated regions are **read-only** in the view:
`apply` reproduces them from the recorded render settings (`.atl/state.json`) and
merges only the editable body between them, so an untouched `full`-profile page
applies to a byte-identical `.csf` — the decorations are never converted into
page content. Editing generated page fields or the `# Comments` section is
refused (exit `8`); use the relevant dedicated metadata/comment command where
available rather than editing the derived view.

Resolved Jira macro tables are generated/read-only too. Editing `# Jira
Queries` is refused; change the native macro in Confluence or select another
`jira_list_views` projection on the next pull. The
`.jira-macros.json` sidecar is bound to the page id and ordered macro
descriptors. Missing or stale enrichment never becomes editable page content:
apply fails closed and names the generated section that changed. A corrupt or
non-empty mismatched sidecar gives a non-looping recovery step: remove only the
generated `.jira-macros.json`, then run `conf pull`. When an explicitly
loss-approved body edit removes the native macro, apply retires the obsolete
sidecar automatically. Post-push refresh rebuilds the same suffix, so an
untouched subsequent apply remains byte-stable.

Output: `{path, csf_path, dry_run, report: {unchanged, moved, converted,
removed, merged_tables?, removed_fragments?, problems?}, csf_ok, wrote,
warning?}`. After a successful apply the `.md` is regenerated from the merged
body so both surfaces agree (keeping the `full` decorations when they were
present); if that refresh cannot be written the apply still succeeds and
`warning` reports that the `.md` may be stale.

Success also records an internal staged-local binding to the exact page id,
relative `.csf` path, native hash, and unchanged remote-base hash. You may edit
the refreshed `.md` and apply again before pushing: the next merge uses those
exact ATL-produced bytes while status/diff/push remain anchored to the original
remote baseline. A direct `.csf` edit, path mismatch, changed base, or corrupt
binding exits `8`; pull and successful post-push refresh clear the binding when
they establish a new remote baseline.

Pass `-o text` for a compact human loss-review — block counts, each removed
fragment, validation problems, and a contextual next-step hint:

```text
dry-run: no files written
blocks: 3 unchanged, 1 moved, 2 converted, 1 removed, 1 table merged
removed fragments:
  - drawio "diagram-1"
validation: ok
next: restore the marker(s) in the .md, or re-run with --allow-fragment-loss to accept the loss
```

The merge is **fail-closed** (exit `8`, nothing written) when: an edited block
cannot be converted faithfully (unsupported markdown, edits inside an
unrecognized wrapper element, an ambiguous mention whose display name collides
with prose); a table edit crosses what the row/cell mapping can carry
(changing a cell that spans rows/columns from a continuation slot, deleting a
row a rowspan passes through, adding/removing columns, editing inside a nested
table, copying a macro-bearing cell) — make that edit in the `.csf` directly
(`conf edit`); the edit drops opaque fragments and `--allow-fragment-loss`
was not given; or the local `.csf` matches neither the last-synced base nor the
exact staged output of the preceding `conf apply` (direct `.csf` edits win —
preserve and push them, or use pull's explicit stash/overwrite policy).
Exit `4`: the page was never pulled (no meta/base). The merged body is always
validated; `conf push --dry-run` remains the final gate before the server.

## `atl conf push`

Validate and push a `.csf` file (or all dirty files in a directory) back to
Confluence under an optimistic version gate.

```bash
# push one file
atl conf push mirror/DOCS/guide/guide.csf

# push all locally-edited files under a directory
atl conf push mirror/DOCS/

# dry run: show what would change without pushing
atl conf push --dry-run mirror/DOCS/guide/guide.csf

# override version conflict (last-write-wins — use with care)
atl conf push --force mirror/DOCS/guide/guide.csf
```

Push output lists each file's outcome and any removed/added fragments so you
can confirm that a macro or diagram was not accidentally deleted from the CSF.
An error-severity CSF problem returns the item and its `problems[]` with
`check_failed` / exit 8 before any write and, on an uncontended local mirror
snapshot, before backend configuration. An active mirror mutation keeps its
existing error precedence. Repair the file and re-run validation rather than
retrying the unchanged push.

Flags:

| flag | description |
|---|---|
| `--dry-run` | validate and diff fragments without pushing |
| `--force` | bypass the version gate (ignores remote drift) |
| `--into` | mirror root, when the file path is outside the default `mirror/` tree |
