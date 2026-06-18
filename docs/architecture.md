# Architecture

`atl` follows a hexagonal (ports & adapters) design: the domain defines
abstract interfaces; use-cases depend only on those interfaces; adapters
implement them; the CLI and any future server tier sit at the outermost ring
and are interchangeable transport layers.

See also: [../README.md](../README.md) · [usage.md](usage.md) ·
[csf-and-fragments.md](csf-and-fragments.md) · [self-update.md](self-update.md)

---

## Layering diagram

```
┌──────────────────────────────────────────────────────────┐
│  transport layer  (internal/cli)                         │
│  cobra commands — parse flags, call use-cases, emit JSON │
└───────────────────────┬──────────────────────────────────┘
                        │ calls
┌───────────────────────▼──────────────────────────────────┐
│  use-case layer  (internal/app)                          │
│  ConfluenceService / JiraService — transport-agnostic    │
│  orchestration; depends on ports only                    │
└────┬──────────────────┬──────────────────────────────────┘
     │ DocStore port     │ Tracker port
┌────▼──────┐     ┌─────▼──────┐
│ confluence│     │ jira       │  internal/adapter/{confluence,jira}
│ adapter   │     │ adapter    │  (swappable; new backend = new adapter)
└────┬──────┘     └─────┬──────┘
     │                  │
     └──────────────────┘
              │ all adapters use
┌─────────────▼────────────────────────────────────────────┐
│  shared infrastructure                                   │
│  internal/httpx  — HTTP client, retries, PAT auth        │
│  internal/auth   — PAT resolution (env → keychain file)  │
│  internal/config — URL config + config dir               │
└──────────────────────────────────────────────────────────┘

cross-cutting (no import of adapters or CLI):
  internal/domain   — ports, Resource, Ref, sentinel errors
  internal/csf      — read-only CSF DOM parser + validator
  internal/fragment — opaque-fragment extraction + resolution
  internal/mirror   — on-disk layout + dirty/drift detection
  internal/selfupdate, internal/version
```

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
    MovePage(ctx, id, newParent) error
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
that satisfies one of these interfaces; no other package changes.

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
        page-title.md         ← read-view (best-effort; lossy by design)
        page-title.meta.json  ← id, title, version, content_hash, fragments
        page-title.assets/
          diagram-name.png    ← resolved draw.io PNG (with --assets)
          photo.jpg           ← resolved inline image (with --assets)
  .atl/
    state.json                ← sidecar: last-synced version + hash per id
    base/
      <id>.csf                ← pristine copy of body at last sync
  .gitignore                  ← auto-created; excludes .atl/, *.pat, etc.
```

**Key operations**

- `PageDir(space, ancestors, title)` — computes the directory path using a
  `slugify` function that preserves unicode letters and digits (Cyrillic
  included), lowercases, and collapses everything else to hyphens, truncated
  at 80 runes.
- `Write(dir, slug, page, refs)` — writes all four artefacts and updates the
  sidecar; the markdown render is best-effort (a parse error in `RenderMarkdown`
  is silently skipped so a pull never fails because of the read-view).
- `LoadCSF(path)` — reads a `.csf` file, its `.meta.json`, and the sidecar
  entry; computes `Dirty = currentHash != syncedHash`.
- `ListCSF()` — walks the tree (skipping `.atl/`), loads every `.csf`, sorts
  by path.
- Sidecar (`state.json`) tracks `{id, version, hash, path}` per page and is
  read/updated atomically on every write. The `base/` directory stores
  pristine body copies so `push` can diff fragments without a network round-
  trip.

---

### `internal/app`

Transport-agnostic use-cases. `ConfluenceService` and `JiraService` are
assembled in `wire.go` by wiring the config-loaded URL + PAT-resolved adapter
and storing it behind the port interface. This layer is what a hypothetical
future HTTP server tier would also call — no cobra, no stdin, no filesystem
beyond the mirror.

Notable behaviors:

- `Pull` resolves page IDs from `--id` / `--cql` / `--space`, fetches each
  page in CSF format, runs `fragment.Extract` + `fragment.Resolve`, and calls
  `mirror.Write`. Up to 1 000 pages via CQL, 2 000 via space tree.
- `Push` validates CSF (`csf.HasErrors` → refuse), computes a fragment diff
  against the pristine base, then calls `store.UpdatePage` under the version
  gate; on success it re-fetches and refreshes the mirror entry.
- `Status` walks the mirror's `.csf` files, compares hashes, and optionally
  fires one `GetMeta` per page to detect remote drift.
- `JiraService.Images` downloads only `image/*`-typed attachments; the others
  are skipped.
- `JiraService.Pull` exports issues as frontmatter-Markdown + raw-fields JSON
  (one pair per issue key, under `mirror-jira/<PROJECT>/`).

---

### `internal/cli`

The cobra command tree. Commands are thin:

1. Parse flags.
2. Call one use-case method.
3. Render via `emit(cmd, value, textFn)` — JSON by default; text when
   `-o text` and a `textFn` is provided.
4. Return an error; `codeFor(err)` maps it to the process exit code via
   `errors.Is` against the domain sentinels.

`PersistentPreRun` on the root command calls `runSelfUpdate` before every
subcommand. The cobra `SilenceUsage` and `SilenceErrors` flags are set so the
CLI's own error message is the only output on stderr.

---

### `internal/httpx`

Shared HTTP infrastructure used by both adapters. Features:

- Bearer auth (`Authorization: Bearer <token>`) injected automatically, but
  only when the request host matches the configured backend host — server-
  supplied attachment URLs pointing elsewhere do not receive the PAT.
- Three retries with exponential backoff (200 ms → 400 ms → 800 ms, capped at
  5 s) for 429 and 5xx responses; honours `Retry-After`.
- Status → sentinel: 401 → `ErrAuth`, 403 → `ErrForbidden`, 404 →
  `ErrNotFound`, 409 → `ErrVersionConflict`.
- `GetJSON`, `SendJSON`, `GetBytes` convenience wrappers.

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
`CONFLUENCE_URL`, `ATL_JIRA_URL` / `JIRA_URL`, `ATL_UPDATE_URL`); env always
wins.

---

### `internal/selfupdate` and `internal/version`

See [self-update.md](self-update.md) for the full description.

`version.Version` is injected via `-ldflags` at build time (default `"dev"`).
`version.DefaultUpdateURL` bakes the GitHub Releases download base into the
binary.

---

## Extension points

| extension | what to do |
|---|---|
| New document backend (Notion, GitHub Wiki) | Implement `domain.DocStore` in a new `internal/adapter/<name>` package; add a `New<Name>` wiring function in `internal/app/wire.go`. |
| New issue tracker (Linear, GitLab Issues) | Implement `domain.Tracker` in a new adapter package; wire analogously. |
| New opaque fragment type (Mermaid, PlantUML) | Add a case in `fragment.Extract`'s `Walk` callback; add a `Resolve` handler in the `AssetResolver` adapter if the fragment renders to a file. |
| Server/MCP tier | Add a transport layer that calls `app.ConfluenceService` / `app.JiraService`; the use-cases are already transport-agnostic. |
| OS-keychain auth backend | Replace `loadStore`/`saveStore` in `internal/auth` with a keychain call; `Token` / `Login` / `Logout` signatures stay the same. |
