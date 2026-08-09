# Architecture

`atl` follows a hexagonal (ports & adapters) design: the domain defines
abstract interfaces; use-cases depend only on those interfaces; adapters
implement them; the CLI and any future server tier sit at the outermost ring
and are interchangeable transport layers.

See also: [../README.md](../README.md) · [CLI reference](reference/cli/README.md) ·
[csf-and-fragments.md](csf-and-fragments.md) · [self-update.md](self-update.md) ·
[network-egress.md](network-egress.md)

---

## Layering diagram

```
┌──────────────────────────────────────────────────────────┐
│  transport layer  (internal/cli, internal/mcpserver)     │
│  cobra commands or typed MCP tools call shared use-cases │
└───────────────┬───────────────────────────────┬──────────┘
                │ calls                         │ assembles through
┌───────────────▼──────────────────────┐  ┌─────▼───────────┐
│  use-case layer  (internal/app)      │  │ internal/compose│
│  transport-agnostic orchestration;   │◄─┤ config/auth/TLS │
│  depends on domain ports             │  │ + concrete ports│
└──────────┬──────────────────┬────────┘  └─────┬───────────┘
           │ DocStore port     │ Tracker port   │ constructs
     ┌─────▼──────┐      ┌────▼───────┐◄────────┘
     │ confluence │      │ jira       │  internal/adapter/{confluence,jira}
     │ adapter    │      │ adapter    │  (swappable; new backend = new adapter)
     └─────┬──────┘      └────┬───────┘
           │                  │
           └────────┬─────────┘
                    │ all adapters use
┌───────────────────▼──────────────────────────────────────┐
│  shared infrastructure                                   │
│  internal/httpx  — HTTP client, retries, PAT auth        │
│  internal/auth   — PAT resolution (env → keychain file)  │
│  internal/config — URL config + config dir               │
└──────────────────────────────────────────────────────────┘

cross-cutting (no import of adapters or CLI):
  internal/domain   — ports, Resource, Ref, sentinel errors
  internal/csf      — read-only CSF DOM parser + validator
  internal/fragment — opaque-fragment extraction + resolution
  internal/jiramap  — pure Jira snapshot → domain mapping
  internal/mirror   — on-disk layout + dirty/drift detection
  internal/diagnostic — stable transport-neutral error classes and recovery
  internal/selfupdate, internal/version
```

`internal/corpus` owns the backend-neutral filesystem boundary for creating,
sealing, publishing, and verifying private corpus generations. It performs no
backend I/O or rendering and remains separate from the existing aggregate
`atl manifest create` path; see
[Sealed corpus generations](corpus-generations.md).
`internal/app` owns the zero-egress projection from locked pristine Jira and
Confluence mirror snapshots into the canonical indexer-v1 members; the CLI only
parses local roots and emits the content-free receipt and generation summary.

---

## Package reference

### `internal/domain`

The hub of the design. Every other package either implements or consumes
types from here; adapters and CLI never import each other.

**Ports**

`DocStore` is the backend interface for document stores (Confluence today):

```go
type DocStore interface {
    Search(ctx, query, limit, cursor) ([]PageRef, nextCursor, error)
    Tree(ctx, space, depth) ([]PageRef, error)
    GetPage(ctx, id, opts) (*Resource, error)
    GetMeta(ctx, id) (*PageMeta, error)
    History(ctx, id) ([]Version, error)
    UpdatePage(ctx, id, expectVersion, title, body, force) (newVersion, error)
    CreatePage(ctx, space, parent, title, body) (*Resource, error)
    MovePage(ctx, id, newParent, expectVersion, title, body) (newVersion, error)
    DeletePage(ctx, id) error
    ListComments / AddComment / ListAttachments / DownloadAttachment
}
```

`Tracker` is the backend interface for issue trackers (Jira today):

```go
type Tracker interface {
    GetIssue / Search / Create / Update / Transition
    AddComment / Link / LinkEpic
    ListAttachments / DownloadAttachment
    Fields / FieldOptions / Transitions / LinkTypes
}
```

Adding a new backend (Notion, Linear, GitLab Issues) means writing a struct
that satisfies one of these interfaces, constructing it in `internal/compose`,
and injecting the port into the applicable app service.

**Optional capability ports.** Some features are not part of every backend's
surface, so they live in their own narrow interfaces rather than bloating the
core port — a backend implements them only if it can, and the service composes
the same adapter instance across several capability fields (as
`ConfluenceService` does with `store`/`users`/`assets`/`verifier`):

- `Verifier` (`Whoami`) — confirms a PAT before `auth login` persists it.
- `QualifiedConfluenceCommentReader` — returns the Confluence-specific,
  source-qualified footer/inline/resolved inventory without changing the
  generic flat `DocStore.ListComments` compatibility. Comment-enabled pulls
  persist the qualified read model through a strict versioned mirror codec and
  render a deterministic read-only tree in the main page view. Historical flat
  sidecars remain read-compatible and comment bytes stay out of page hashes and
  writeback baselines.
- `ConfluenceCurrentUserReader` — supplies the stable actor identity required by
  guarded footer-comment proposals. The application combines it with exact
  page metadata, documented public-REST capability, and a complete root-only
  footer inventory; missing or partial evidence fails closed before POST.
- Content labels, Jira watchers, and Jira worklogs expose separate reader and
  writer ports, plus the focused `JiraCurrentUserReader` identity port. Their
  compatibility aggregates remain available to existing broad services, while
  the selected feature services and read-only tests depend only on the exact
  capabilities they consume.
- `Agile` (`Boards`/`Board`/`Sprints`/`Sprint`/`SprintIssues`/
  `MoveIssuesToSprint`/`MoveIssuesToBacklog`) — Jira Software boards & sprints
  over the Data Center Agile API `/rest/agile/1.0/`. Requires GreenHopper, so a
  Jira Core/Service-Management-only instance (or a future non-agile tracker)
  simply omits it.

**Registry ports (in `registry.go`)**

`AssetSink` — the mirror hands this to fragment handlers so they can write
fetched asset bytes to the correct on-disk path without knowing the layout.

`AssetResolver` — fetches the rendered bytes of a visual fragment (draw.io
PNG at a specific revision, inline image) from the backend. The Confluence
adapter implements it; the fragment package consumes it.

`UserResolver` — a function type `func(ctx, userkey) (string, error)` that
maps an opaque Confluence userkey to a display name. Passed as a closure so
the caller can substitute a stub for tests.

**Core types**

`Resource` is the unified unit shared by the mirror and both service layers:

| field | meaning |
|---|---|
| `ID` | backend id (Confluence content-id or Jira issue key) |
| `Title` | page/issue title |
| `SpaceKey` | Confluence space key or Jira project key |
| `Version` | backend version, used as the optimistic-lock gate |
| `Body` | native-format bytes (CSF or Jira wiki) — never converted |
| `Hash` | sha256 of `Body` — drives dirty detection |
| `Refs` | resolved opaque fragments (draw.io, users, links, images) |
| `Ancestors` | ancestor titles top→down — drives the mirror folder path |

`Ref` is a resolved opaque fragment (see [csf-and-fragments.md](csf-and-fragments.md)):

| field | meaning |
|---|---|
| `Kind` | `drawio` / `user` / `attachment` / `page-link` / `image` |
| `Key` | raw backend key (userkey, filename, diagram name, page title) |
| `Display` | human-readable label after resolution |
| `Asset` | relative path to a fetched render file (PNG, etc.) |
| `Params` | handler-specific extras (e.g. `revision` for draw.io) |

**Sentinel errors**

Sentinel errors in `errors.go` are the sole way the CLI layer learns what
exit code to use:

| sentinel | exit code | cause |
|---|---|---|
| `ErrAuth` | 3 | 401 from the backend |
| `ErrNotFound` | 4 | 404 from the backend |
| `ErrVersionConflict` | 5 | 409 / optimistic-lock refused |
| `ErrForbidden` | 6 | 403 from the backend |
| `ErrUsage` | 2 | bad CLI arguments or state |

All adapter errors wrap one of these via `fmt.Errorf("%w: ...", domain.ErrXxx)`;
`errors.Is` in the CLI layer unwraps them to the right exit code.

---

### `internal/adapter/confluence`

Implements `domain.DocStore`, `domain.AssetResolver`, and exports
`ResolveUser` (matching the `domain.UserResolver` signature).

- Uses `internal/httpx.Client` for all HTTP — bearer PAT auth, retries, host
  verification, and status→sentinel error mapping.
- `GetPage` fetches either `body.storage` (CSF bytes) or `body.view` (rendered
  HTML) depending on `PullOpts.Format`. Bodies are passed verbatim to callers;
  the adapter never converts them.
- `UpdatePage` implements the optimistic version gate: it sends
  `version.number = expectVersion + 1`; Confluence returns 409 (mapped to
  `ErrVersionConflict`) if the remote has moved past `expectVersion`. The
  `force` flag re-reads the current version and targets `current + 1` instead,
  bypassing the gate.
- `Resolve` (implements `AssetResolver`) downloads draw.io PNGs via
  `/download/attachments/<pageID>/<name>.png?version=<rev>` for the exact
  diagram revision captured at pull time, and fetches inline images by
  filename.
- `ResolveUser` tries `/rest/api/user?key=` then `/rest/api/user?accountId=`
  to cover both Data Center (userkey) and Cloud (account-id) styles.

### `internal/adapter/jira`

Implements `domain.Tracker` against the Jira REST v2 API.

Raw Jira field maps are converted by the transport-neutral `internal/jiramap`
package. The REST adapter and offline renderer share that mapper without making
ordinary app use-cases import transport code.

- `Transition` resolves the target status by name (case-insensitive) against
  the live list from `/transitions`, so callers pass human names rather than
  numeric IDs.
- `LinkEpic` discovers the custom field id for "Epic Link" at runtime (DC
  classic boards); warns gracefully if the field is absent.
- `FieldOptions` uses the `createmeta` endpoint to enumerate allowed values
  for a field on a specific project/issue-type pair, which agents need before
  setting dropdowns.

---

### `internal/csf`

A read-only DOM parser for Confluence Storage Format. See
[csf-and-fragments.md](csf-and-fragments.md) for the full write-path safety
argument. Key types:

- `Node` — DOM node with `Type` (Element / Text / CData), `Name`
  (namespace-prefix + local), `Attr`, `Children`, `Data`.
- `Parse(raw []byte) (*Node, error)` — wraps raw bytes in a synthetic `<root>`
  element so body fragments (which may have multiple top-level nodes) parse as
  a single document; configures `xml.HTMLEntity` so `&nbsp;`, `&mdash;`, etc.
  resolve; returns an error for malformed XML.
- `Walk(n, fn)` — depth-first traversal; `fn` returns false to skip a
  subtree.
- `TextContent(n)` — concatenated text of a subtree.
- `Validate(raw []byte) []Problem` — runs well-formedness first (returns a
  single error problem with accurate line/col if the XML is broken), then runs
  sanity checks (advisory warnings: macros without `ac:name`, draw.io without
  `diagramName`, dangling `ri:attachment` refs).
- `Problem` carries `Severity` (`"error"` / `"warning"`), `Line`, `Col`,
  `Rule`, `Message`. `HasErrors` reports whether any problem blocks a push.

---

### `internal/fragment`

Extracts and resolves the opaque fragments embedded in a CSF DOM.

`Extract(root *csf.Node) []domain.Ref` — walks the DOM and returns distinct
refs in document order, deduplicated by `(kind, key)`. Recognized patterns:

| CSF construct | `RefKind` | `Key` |
|---|---|---|
| `<ac:structured-macro ac:name="drawio">` | `drawio` | `diagramName` param |
| `<ac:image>` containing `<ri:attachment>` | `image` | `ri:filename` |
| `<ri:user ri:userkey="…">` | `user` | userkey or account-id |
| `<ri:page ri:content-title="…">` | `page-link` | content-title |
| `<ri:attachment ri:filename="…">` | `attachment` | filename |

`Resolve(ctx, page, refs, deps) []domain.Ref` — mutates each ref's `Display`
and `Asset`:

- `drawio` / `image`: calls `deps.Resolver.Resolve` → `deps.Assets.Put` to
  fetch and save the render; on failure leaves the ref with its raw display
  and no asset path.
- `user`: calls `deps.Users(ctx, key)` to get a display name; caches results
  per-call to avoid duplicate round-trips; degrades to `@key` on failure.
- `page-link` / `attachment`: no network call needed; already human-readable.

All failure paths are swallowed — `Resolve` never returns an error. The
fragment layer is extension-friendly: adding a new opaque type (Mermaid,
PlantUML, LaTeX) means extending `Extract`'s switch; adding a network-fetched
render means implementing `AssetResolver.Resolve` for that kind.

---

### `internal/mirror`

Owns the on-disk layout of the mirror directory. It is backend-agnostic; it
stores `domain.Resource` bytes and knows nothing about HTTP or CSF semantics.

**On-disk layout**

```
mirror/
  SPACE/
    ancestor-title/
      page-title/
        page-title.csf        ← source of truth (verbatim CSF bytes)
        page-title.md         ← v6 staging view; may include a qualified comment tree
        page-title.meta.json  ← id, title, version, content_hash, fragments
        page-title.comments.json ← schema-v2 qualified comment evidence
        page-title.comments.md   ← flat compatibility projection
        page-title.assets/
          diagram-name.png    ← resolved draw.io PNG (with --assets)
          photo.jpg           ← resolved inline image (with --assets)
  .atl/
    state.json                ← remote sync, render, and staged-local lineage
    base/
      <id>.csf                ← pristine copy of body at last sync
  .gitignore                  ← auto-created; excludes .atl/, *.pat, etc.
```

**Key operations**

- `PageDir(space, ancestors, title)` — computes the directory path using a
  `slugify` function that preserves unicode letters and digits (Cyrillic
  included), lowercases, and collapses everything else to hyphens, truncated
  at 80 runes.
- `ClaimPageDir(space, ancestors, title, id)` — the collision-aware wrapper
  writers go through. Slugification is lossy (`Foo Bar` and `Foo-Bar?` both
  slugify to `foo-bar`), so before handing out a dir it checks the existing
  `<slug>.meta.json`: a dir owned by a different page id (or holding page
  files with unreadable meta) diverts the newcomer to an id-suffixed slug
  (`foo-bar-200`), sticky across re-pulls even if the plain dir later frees
  up; if even that dir belongs to someone else the claim refuses
  (`ErrCheckFailed`) rather than overwrite. Known limitation: ancestor path
  segments are still title-derived and collision-blind (ancestor ids are not
  available), so descendants of a diverted page nest under the plain-slug
  ancestor dir — structurally off, but no file is ever overwritten because
  every leaf claim disambiguates.
- `Write(dir, slug, page, refs)` — writes all four artefacts and updates the
  sidecar; the markdown view is best-effort (it never fails a pull) but may
  never contradict the source of truth: an unparseable body overwrites the
  previous revision's `.md` with an explicit "view unavailable" stub, and a
  failed `.md` write falls back to removing the stale file. How the
  CSF→Markdown view is tested and how to extend its coverage:
  [docs/csf-markdown-testing.md](csf-markdown-testing.md).
- Qualified schema-v2 comments render into the main `.md` as a deterministic
  tree with explicit location/state, completeness, safe anchor labels, and an
  unattached section for unproven ancestry. Safe generic diagnostics may be
  projected without identifiers; authoritative structured evidence stays in
  `.comments.json`, while `.comments.md` remains flat compatibility.
  The v6 format migrates pristine v5/v4 views only after exact
  version-specific reconstruction; edited legacy, older historical,
  unversioned, and future views are preserved and refused.
- `LoadCSF(path)` — reads a `.csf` file, its `.meta.json`, and the sidecar
  entry; computes `Dirty = currentHash != syncedHash`.
- `ListCSF()` — walks the tree (skipping `.atl/`), loads every `.csf`, sorts
  by path. Walk, body, and metadata errors fail the scan; no entry is silently
  omitted from status or a directory push.
- `LoadWiki(path)` / `ListWiki()` — the Jira analogs over `.wiki` substrate
  files. There is no neighboring `.meta.json`, so the sidecar key is the issue
  key (the file's basename); dirty detection is otherwise identical. Walk and
  body errors likewise fail the scan.
- `SaveBaseExt(id, body, ext)` / `BaseBodyExt(id, ext)` — the ext-aware base
  store; the plain `saveBase`/`BaseBody` are the `.csf` specialization. Jira
  records its pristine base under `.atl/base/<KEY>.wiki`. `SyncBatch.Record`
  lets a backend that writes its own substrate files (Jira's `.wiki`) share the
  batch's single sidecar load/save without going through `writePageFiles`.
- Sidecar (`state.json`) tracks remote `{id, version, hash, path}` per page,
  render settings per derived view, and optional staged-local
  `{id, path, hash, base_hash}` lineage written by `apply`. Staged lineage never
  advances the remote baseline: it only proves that a later `apply` is merging
  from exact ATL-produced native bytes. A successful pull or post-push refresh
  clears it atomically with the new remote sync state. Mirror
  directories and files are accessed through Go's root-scoped filesystem API.
  Intermediate descendant symlinks are rejected; reads reject a final symlink,
  while atomic writes replace it without following it. The selected root itself
  remains the caller's trust anchor. Saves use temp + fsync + root-scoped rename, so a
  crash can never leave a half-written file. A corrupt sidecar is a loud
  error on every path that consults it (`status`, `push`, `pull`) — never a
  silent reset to "never synced", which would quietly disable drift
  detection. Multi-page writes go through `BeginSync`/`SyncBatch.Write`/
  `Flush` so a pull loads the file once, and `ListCSF` loads it once for the
  whole walk. On flush, each batch acquires the backend-neutral
  `.atl/state.lock`, re-reads the latest sidecar, and merges only the page/view
  entries it changed before the atomic save. This prevents Jira and Confluence
  processes sharing one root from overwriting each other's state; contention
  fails closed instead of silently disabling drift detection. The `base/`
  directory stores pristine body copies so `push` can diff
  fragments without a network round-trip.

---

### `internal/app`

Transport-agnostic use-cases. `ConfluenceService` and `JiraService` are
assembled from domain ports and neutral backend-identity projections in
`wire.go`; concrete adapters, credentials, transport security, and scheduler
construction stay outside this package. App code may read `config.Config` only
for the render and derived-list-view settings owned by `Render` and
`JiraListViews`; a type-checked architecture oracle rejects every other config
field. This layer is what a hypothetical future HTTP server tier would also
call — no cobra, no stdin, no filesystem beyond explicit storage use-cases.
The app layer orchestrates filesystem-backed use-cases through
`internal/mirror`, `internal/safepath`, and narrow helpers; `internal/mirror`
owns layout, sidecar, baseline, and dirty/drift primitives. Plan inputs,
exports, manifests, attachments, and caller-selected output files use
bounded/atomic I/O where applicable.

Transient registration and crash-recoverable publication APIs accept only
constructed mirror-relative artifact paths. Public construction rejects the
reserved `.atl` component, including ASCII case aliases; private construction
admits only non-empty descendants of the exact `.atl/base/` subtree. Durable
journals and sidecars keep their existing string bytes, but mirror reparses
those strings as untrusted paths before recovery or filesystem use. Historical
Jira `.wiki` and Confluence `.csf` sidecar spellings written with Windows
separators are normalized on read only, then bound again to the sidecar map key,
state ID, version, and native extension before the same public-path checks; new
durable paths remain canonical slash-separated strings. Root-scoped resolution
and symlink checks remain mandatory at the actual I/O boundary.
The complete-pull transaction service is also closed: Confluence keeps its
schema-2 journal/publication and progress-v1 bytes, requires exactly one native
artifact and one mode-0600 `.atl/base/<page-id>.csf`, and binds both payload
hashes to the accepted page state. It retains the positive page-version
contract. Legacy Jira schema-3 transaction bytes remain readable. New Jira
publications use schema 4 with a
positive immutable issue ID separate from its mutable key, version `0`,
canonical `.wiki` state, explicit `native|metadata|view|base|auxiliary`
artifact roles, and service-bound progress v2. The stable ID is also recorded
additively in the private sidecar. A key relocation binds the exact predecessor
state/view and old artifact pre-images, atomically replaces the sidecar key,
then retires only the persisted exact old files; recovery accepts either the
predecessor or exact replacement state, never an unrelated midpoint. A crash after replacing a
checkpoint with the other service's selection resets the stale progress prefix
to zero. A non-empty legacy Jira asset directory is not inferred to be owned:
it blocks key relocation and remains intact for manual reconciliation.
Cross-service schema, extension, identity, version, role, and path
combinations fail before a destination is staged or accepted.

Notable behaviors:

- `ConfluenceLabelService`, `JiraWatcherService`, and `JiraWorklogService` are
  focused use-case owners assembled directly from their reader, writer, and
  identity ports. Their guarded mutations share only the pure
  preview/apply/hash/status decision. Each feature retains its own proposal
  schema, single-attempt transport call, readback rules, ambiguity diagnostic,
  and result/error contract.
- `Pull` resolves page IDs from `--id` / `--cql` / `--space`, fetches each
  page in CSF format, runs `fragment.Extract` + `fragment.Resolve`, and calls
  `mirror.Write`. Ordinary mode caps CQL at 1 000 and space tree at 2 000;
  explicit complete mode qualifies an exhaustive selector twice and consumes
  a private resumable exact-id checkpoint.
- `Push` validates CSF (`csf.HasErrors` → refuse), computes a fragment diff
  against the pristine base, then calls `store.UpdatePage` under the version
  gate; on success it re-fetches and refreshes the mirror entry.
- `AddFooterCommentGuarded` validates at most 1 MiB of exact native CSF and
  hashes backend/page/version, stable actor, body, capability, and the complete
  footer-root baseline into a proposal. The dedicated preview is read-only;
  apply revalidates immediately before one single-attempt POST and reconciles
  `applied|recovered|outcome_unknown` without replay. That documented-REST
  surface creates footer roots only; inline create/reply/resolution uses the
  separately activated compatibility provider described below.
- `Status` walks the mirror's `.csf` files, compares hashes, and optionally
  detects remote drift through exact one-page metadata or bounded qualified
  multi-page batches with whole-batch reconciliation.
- `JiraService.Images` downloads only `image/*`-typed attachments; the others
  are skipped.
- `JiraService.Pull` exports each issue as three files under
  `mirror-jira/<PROJECT>/`: `<KEY>.wiki` (the native Jira wiki body, byte-for-byte
  — the editable substrate, mirroring `.csf`'s role for Confluence), `<KEY>.md`
  (a best-effort derived Markdown staging view rendered from the wiki by
  `internal/wikimd`, regenerated on every pull — a render failure degrades one
  section to a stub, never failing the pull), and `<KEY>.json` (raw fields
  snapshot). The `.md` `path` is what the pull result reports. The pull also
  records the `.wiki` body in the sidecar plus a `.atl/base/<KEY>.wiki` base
  copy so the write-back cycle can detect edits and drift. `.md` Description
  edits are merged into `.wiki` by `jira apply`; typed rich-text field sections
  explicitly configured editable are staged under `.atl/pending/jira/`. The raw
  `.json` snapshot and assets remain read-only until a successful push refreshes
  the snapshot.
  `internal/wikiscanner` owns the Jira heading/macro/hr/list/table recognition
  rules consumed by both `wikimd` and `wikimerge`, so renderer and apply block
  boundaries cannot drift through duplicated regular expressions.
  `internal/blockalign` owns the bounded deterministic LCS alignment shared by
  the Confluence and Jira native-byte merge paths; its tie rule is a durable
  byte-selection contract, not an interchangeable implementation detail.

Confluence path relocation is id-based rather than directory-based. A re-pull
reconstructs the recorded pristine view in `app`, while `mirror` hash-binds and
retires only the old page's primary artifacts after the replacement sidecar
path is durable. State lookup also requires path identity, so a crash-left old
copy is untracked/dirty. Descendant and auxiliary directories are never removed
recursively.
  Pending commits bind the recorded sidecar path and reviewed `.wiki` hash. A
  non-discoverable transaction is published only after the atomic wiki write;
  status/push recover an interrupted commit from its before/after hashes.
  A stable mirror-global advisory lock inode serializes Jira mirror mutations
  through sidecar flush; atomically replacing `.wiki` cannot bypass that lock.
- `JiraService.Status` walks the mirror's `.wiki` files and pending-field state,
  compares hashes (`locally_edited`), and with `--remote` keeps an exact
  `GetIssue` for one eligible issue or uses qualified 100-key / 16 KiB metadata
  batches for larger selections. Whole-batch identity/projection reconciliation
  precedes comparison of remote description/fields to stored bases
  (`remote_drifted`); a file with no sidecar entry reads never-synced
  (`synced:false`).
- `JiraService.Push` is the guarded write-back. It is **dry-run by default**
  (`--apply` to write) because Jira has **no server-side version gate**: the
  staleness guard is an app-layer compare-and-swap — a fresh remote read is
  compared to pristine bases, and a mismatch is refused as `ErrCheckFailed`
  (exit 8), **never** `ErrVersionConflict` (#66). `--force` may override only
  Description drift; pending fields always fail closed. Description and the
  explicit pending field set are sent in one typed update. Ambiguous responses
  are reconciled by a fresh end-state read without replay; retry also treats
  remote==proposal as already applied and repairs local state only. Definitive
  4xx errors are not reconciled, and backend response bodies are sanitized. A server-side HTTP
  409 stays a generic conflict. On `--apply` success it re-fetches and refreshes
  `.wiki`/`.md`/`.json`/base/sidecar and clears pending state. Transport/local
  refresh failures are warnings; a successful verification read whose values
  mismatch the full proposal retains pending and returns `ErrCheckFailed`.
- Permanent Jira issue deletion is a separate preview/apply boundary. It binds
  the canonical key, immutable numeric id, exact `updated`, backend identity,
  complete permission-relative subtask identities, and cascade intent. Apply
  revalidates immediately before one DELETE by numeric id; only acknowledged
  DELETE plus exact-id not-found readback is success, and ambiguous attempts
  remain non-replayable even when permission-relative absence is observed.

---

### `internal/profile`

Owns the versioned private workflow profile independently of backend adapters
and mirrors. It strictly decodes and normalizes schema facts, confirmed user
preferences, explicitly sourced team policy, render defaults, and named
selectors. Canonical JSON hashes drive a two-phase preview/apply contract;
apply rechecks both candidate and current hashes under a config-root advisory
lock, then atomically replaces `profile.json` with mode 0600. The package never
reads Atlassian content or applies runtime render config—those consent decisions
remain in the onboarding skill/CLI orchestration.

Learning uses two more owner-only artifacts without weakening that boundary.
Versioned observations deterministically build a hash-bound suggestion; review
reuses the normal profile preview and apply guards, while rejection retains only
the suggestion hash. Revalidation state records explicit verified/missing/failed
metadata checks separately from the last verified profile fact. No background
reader, clock-based mutation, or policy inference exists; callers supply an
absolute stale cutoff and approved read results.

### `internal/compose`

The outer composition owner shared by CLI and MCP. It loads non-secret config,
resolves host-scoped credentials, validates backend URLs and CA bundles,
constructs schedulers and concrete Jira/Confluence adapters, and injects them
into app services through domain ports. Optional sibling backends remain lazy,
and doctor receives path-free app-owned projections rather than config, auth,
or TLS implementation types. `internal/app`, `internal/cli`, and
`internal/mcpserver` do not import adapter packages directly. Focused feature
constructors reuse the same secure adapter factories as the broad services, so
URL, credential, CA-bundle, scheduler, and write-authorizer behavior cannot
drift while CLI commands receive narrower app surfaces. Invocation options are
resolved before adapter construction: verbose CLI roots inject only their own
stderr trace sink, lazy sibling adapters inherit that same sink, and MCP or
other default composition remains silent.

### `internal/cli`

The cobra command tree. Commands are thin:

1. Parse flags.
2. Call one use-case method.
3. Render via `emit(cmd, value, textFn)` — JSON by default; text when
   `-o text` and a `textFn` is provided.
4. Return an error; `codeFor(err)` maps it to the process exit code via
   `errors.Is` against the domain sentinels.

JSON failure rendering also calls the shared diagnostic recovery classifier
with a closed semantic operation context. CLI and MCP therefore emit the same
schema-v1 recovery object for the same typed application error and operation,
while retaining their existing human message policies. The classifier never
parses error prose: only type/sentinel identity, closed context, and validated
numeric facts may cross the recovery boundary. Exact-repeat safety is true only
for explicitly modeled reads; writes and changed-argument workflows fail
closed.

`PersistentPreRun` on the root command calls `runSelfUpdate` before every
subcommand. The cobra `SilenceUsage` and `SilenceErrors` flags are set so the
CLI's own error message is the only output on stderr.

---

### `internal/httpx`

Shared HTTP infrastructure used by both adapters. Features:

- Immutable per-client trace, conflict, and write-clearance policy. Confluence
  construction keeps the optimistic-version 409 mapping; Jira construction
  selects generic 409 errors because Jira has no equivalent version gate. No
  process-global transport toggle can cross concurrent CLI roots or hosts.
- Bearer auth (`Authorization: Bearer <token>`) injected automatically, but
  only when the request host matches the configured backend host — server-
  supplied attachment URLs pointing elsewhere do not receive the PAT.
- Three retries with exponential backoff (200 ms → 400 ms → 800 ms, capped at
  5 s) for replay-safe reads (`GET`/`HEAD`) on 429 and 5xx responses; honours
  `Retry-After`. Writes are never retried generically after an ambiguous
  response and must reconcile at the endpoint/use-case layer.
- Status → sentinel: 401 → `ErrAuth`, 403 → `ErrForbidden`, 404 →
  `ErrNotFound`; 409 uses the client construction policy described above.
- `GetJSON`, `SendJSON` convenience wrappers; `GetStream` for binary
  downloads — retries apply until the 2xx headers arrive, then the body
  streams (never buffered in httpx) bounded by an inactivity deadline instead
  of the JSON client's whole-request timeout, so large transfers on slow
  links are limited by stalls, not total wall-clock.

The public `Client` facade remains in `client.go`. Private owners separate
construction options and tracing (`options.go`), TLS trust (`tls.go`), request
transport and PAT scoping (`transport.go`), response classification
(`attempt.go` and `errors.go`), retry timing (`retry.go`), and bounded body
consumption (`body.go`). An AST contract keeps this ownership inventory closed.

---

### `internal/auth`

PAT resolution for `confluence` and `jira`. Resolution order (first non-empty
wins):

1. `ATL_CONFLUENCE_PAT` / `CONFLUENCE_PAT` / `TEST_CONFLUENCE_PAT` (env)
2. `ATL_JIRA_PAT` / `JIRA_PAT` / `TEST_JIRA_PAT` (env)
3. `~/.config/atl/credentials.json` (keyed by service name, mode 0600)

`auth.Login` writes to the credentials file. `auth.Source` reports where a
token was found without revealing it (used by `atl auth status`).

---

### `internal/config`

Non-secret settings. `Config` holds `ConfluenceURL`, `JiraURL`,
`UpdateBaseURL`. Config directory resolution:

1. `ATL_CONFIG_DIR` env
2. `$XDG_CONFIG_HOME/atl`
3. `~/.config/atl`

`Load` reads `config.json` then overlays env vars (`ATL_CONFLUENCE_URL` /
`CONFLUENCE_URL`, `ATL_JIRA_URL` / `JIRA_URL`, `ATL_UPDATE_URL`, and the
backend-scoped `ATL_CONFLUENCE_CA_BUNDLE` / `ATL_JIRA_CA_BUNDLE`); env always
wins.

---

### `internal/selfupdate` and `internal/version`

See [self-update.md](self-update.md) for the signed update mechanism and
[network-egress.md](network-egress.md) for the runtime destination and air-gap
boundary.

`version.Version`, `version.Commit`, and `version.BuildState` are injected via
`-ldflags` by supported Makefile/release builds. `version.Current` falls back to
Go compiler VCS settings for unstamped builds and normalizes unavailable
provenance to `unknown`; no build timestamp is embedded. The commit/state are
diagnostic only and do not participate in update trust.
`version.DefaultUpdateURL` bakes the GitHub Releases download base into the
binary.

### `internal/mcpserver`

The stdio MCP transport registers a closed read-only tool inventory and calls
application services directly. It never shells back into Cobra or imports a
backend adapter; lazy remote dependencies are obtained through
`internal/compose`. A missing Jira configuration therefore does not suppress
Confluence tools, and vice versa. Local mirror snapshots have a separate lazy
dependency: an explicit owner-configured root, validated before the existing
offline content-free snapshot services run. The server shares
`internal/diagnostic` classifications with CLI errors while MCP and CLI retain
their transport-specific envelopes.

The explicit registration list is the security boundary. There is no generic
command dispatcher, raw REST tool, mutation, arbitrary filesystem access, or
mirror-writing tool. See [mcp.md](mcp.md) for the public inventory and bounds.

---

## Confluence comment mutation provider boundary

Qualified comment reads and single-attempt footer-comment creation remain core
`atl` capabilities over documented Confluence Data Center REST resources.
Footer creation is a non-idempotent POST and is not retry-safe. Full inline
creation, reply, and resolve/reopen are provider-gated because they use the
product-bundled inline-comment client protocol rather than the documented core
REST contract. No separately deployed Data Center app is required: ATL acts as
a narrowly versioned client of the existing bundled module.

The boundary is deliberately split across layers:

- `internal/domain` exposes only a closed semantic operation matrix and typed
  highlight geometry—never arbitrary paths, headers, payload JSON, or endpoint
  templates;
- owner-only compatibility settings activate one compiled provider for one
  exact product version/build, and the adapter requalifies that identity before
  every preparation and write;
- inline-create preparation performs one bounded, non-redirecting read of the
  server-rendered page, requires unambiguous page/version/request-time metadata
  and one `#content .wiki-content` root, reproduces the pinned client's
  NBSP/trim normalization, exclusion masks, overlapping-match indexing, and
  footer-fallback predicates, then derives raw-DOM UTF-16 selection geometry
  with an HTML5 DOM. Layout-dependent or unsupported DOM fails closed;
- the reviewed proposal binds a canonical stable content-subtree fingerprint,
  exact occurrence/match count/geometry, native page and marker inventory,
  complete comment baseline, actor, backend, activation, and content hashes.
  Volatile server request-time is neither emitted nor hashed;
- apply repeats the full snapshot and preparation immediately before the write.
  Stable evidence must match; only the fresh request-time enters one fixed POST;
- ATL never edits an inline marker into CSF. Readback must prove exactly one new
  root and a page-body change consisting solely of the matching server-owned
  marker wrapper. Replies and resolution transitions likewise require complete
  inventory reconciliation.

The provider has no arbitrary REST escape hatch, redirect, retry, or replay.
An ambiguous attempt becomes `outcome_unknown` and must be inspected, never
automatically repeated. Exact product pins intentionally provide no adjacent
version promise; additional community versions require a reviewed compiled
profile and their own evidence. Mutation commands remain JSON-only CLI routes
and are never exposed through the read-only MCP server.

### Guarded page copy boundary

Page copy is a preview/apply create boundary. A canonical proposal binds the
backend identity, complete exact-current source state, resolved target and
optional target-parent state, and optional mirror-registration root identity.
Apply revalidates all remote inputs immediately before one non-replayed POST.
Success requires one exact current readback with version 1 and byte-identical
native body; registration consumes that same readback and commits state last.
There is no title-search reconciliation because titles are not idempotency keys.

### Guarded page trash boundary

Page trashing is a separate destructive boundary. The CLI is preview-first and
requires an exact reviewed version, canonical proposal hash, and `TRASH`
confirmation before apply. The proposal binds the normalized backend identity
and the page's exact status, hierarchy identity, native bytes, and title without
emitting content. Because Confluence exposes no version compare-and-set on
DELETE, the app repeats the complete snapshot immediately before dispatch,
sends exactly one non-redirected and non-retried DELETE explicitly qualified as
`status=current`, and never treats an ambiguous transport result as permission
to replay. It never requests permanent purge of trashed content.

Reconciliation uses a narrow domain port for explicit `current` and `trashed`
status namespaces. Only an exact trashed projection matching the reviewed
identity and native bytes proves application. Any unavailable, missing,
current, or mismatched readback remains `outcome_unknown`; unsupported exact
status reads fail closed before the destructive request. The route is CLI-only
and remains absent from the read-only MCP surface.

### Guarded attachment deletion boundary

Permanent attachment deletion is also preview-first, but its authorization and
reconciliation unit is a complete attachment inventory rather than a single
content record. The app brackets two independently exhausted qualified
pagination passes with exact current-page reads, requires their canonical
agreement, and canonicalizes every attachment by id and hashed metadata. The
proposal binds that reconciled inventory, the selected target, exact
page revision/native identity, and normalized backend identity.

Apply re-reads the same complete proposal state before exactly one
non-redirected and non-retried DELETE. Only unchanged exact page evidence plus
two agreeing complete final inventories exactly equal to the reviewed inventory
minus that target prove `applied` or `recovered`; target absence with page or
sibling drift is insufficient. Partial,
legacy, malformed, or unavailable inventory fails closed, and every ambiguous
post-attempt state remains `outcome_unknown`. The guarded route is CLI-only and
is absent from the read-only MCP surface.

---

## Extension points

| extension | what to do |
|---|---|
| New document backend (Notion, GitHub Wiki) | Implement `domain.DocStore` in a new `internal/adapter/<name>` package; add concrete construction in `internal/compose` and inject the port through `internal/app/wire.go`. |
| New issue tracker (Linear, GitLab Issues) | Implement `domain.Tracker` in a new adapter package; compose and inject it analogously. |
| New opaque fragment type (Mermaid, PlantUML) | Add a case in `fragment.Extract`'s `Walk` callback; add a `Resolve` handler in the `AssetResolver` adapter if the fragment renders to a file. |
| New MCP evidence tool | Add an explicit typed wrapper around an existing read-only app use-case, define hard context bounds, annotate it accurately, and extend the exact inventory/security tests. Do not expose CLI dispatch or raw REST. |
| OS-keychain auth backend | Replace `loadStore`/`saveStore` in `internal/auth` with a keychain call; `Token` / `Login` / `Logout` signatures stay the same. |
