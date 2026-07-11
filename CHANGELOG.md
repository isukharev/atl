# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- **Kanban/Scrum board snapshots for agent planning.** Jira board commands now
  expose workflow configuration, ranked issue pages, Scrum backlog pages, and a
  normalized status-to-column view with JSON/JSONL/safe CSV/Markdown exports.
  Pagination and truncation are explicit, unknown statuses remain visible, and
  Kanban routing never calls incompatible sprint/backlog endpoints.

- **Normalized Tempo Structure snapshots for agent planning.** `jira structure
  view` joins hierarchy rows with compact Jira fields by stable issue identity and
  renders JSON, Markdown tables, or row ids without inflating cells with raw
  Jira objects. Offline exports now share that projection and add self-contained
  JSONL alongside JSON, safe CSV, and Markdown. Snapshots record that browser
  saved-view columns were not reproduced and mark per-row permission gaps.
  Stored folders receive best-effort labels; calculated grouping rows stay
  technical rather than risking an incorrect row/value join.

### Changed

- **Structure export now uses the normalized snapshot contract.** During the
  beta, the former aggregate `{rows,issue_ids,issues}` export and its
  export-only `--root-fields`/`--limit` flags were replaced by the same compact
  projection as `jira structure view`. Use `pull-issues` when raw Jira snapshots
  or an issue limit are required.

### Security

- **Release signing-key continuity is machine-enforced.** Before building a
  tagged release, CI derives the public key from the protected signing secret
  and requires it to match the trust key embedded in the latest published
  stable client. Missing, malformed, non-canonical, or prematurely rotated keys
  stop publication; the staged bridge-release path remains supported.

### Fixed

- **Small agent contracts are explicit.** A missing `conf edit` target returns
  not-found instead of usage, unknown non-empty Jira field objects render as
  `[object]` rather than disappearing, and normalized Structure JSON is pinned
  by a CLI golden test.

- **Lenient Jira validation is scoped to generated Structure joins.** Ordinary
  `jira issue search` and every other user-authored JQL path again use Jira's
  strict semantic validation, while Structure id batches can still retain rows
  for deleted or permission-hidden issues.

- **Offline render cannot partially downgrade future derived views.** Jira now
  enforces the same marker-version preflight as Confluence, and both services
  inspect the entire selected render batch before rewriting any sibling.
  Confluence marker reads also use root-contained mirror I/O.

- **Structure and board projections remain honest at visibility and format
  boundaries.** Jira search no longer rejects a whole id batch because one issue
  is deleted or hidden; only issue-typed Structure rows can receive issue values,
  subtree counts follow emitted rows, and Markdown tables preserve punctuation,
  backslashes, and requested board fields. Jira issue JSON now explicitly
  documents the optional stable status id used for board-column mapping.

- **Confluence relocation recovery and local diagnostics are fail-closed but
  actionable.** A re-pull now repairs stale sidecar state when all old primary
  page artifacts were deliberately removed, while partial remnants still block
  with recovery guidance. Legacy relocation views receive migration-specific
  instructions, future derived views cannot be overwritten by offline render,
  missing local targets consistently return not-found, direct page reads reject
  omitted native bodies, `conf edit` commits atomically, shared state updates
  tolerate brief safe contention, and HTTP transport errors expose only a
  coarse URL-free cause category.

- **Confluence re-pulls retire stale metadata paths safely.** When a title or
  ancestor change moves a tracked page, pull now binds relocation by page id,
  refuses native/Markdown edits and target collisions, publishes the new
  sidecar path, then retires only the old page's primary artifacts. Descendants,
  assets, comment caches, and unrelated files are preserved. Guarded moves also
  bind the reviewed target version and re-read its hierarchy immediately before
  the write.

- **Guarded workflow edge cases now share one contract.** Jira field proposal
  hashes bind the issue key; Confluence title/move and Jira field no-op outcomes
  still require reviewed freshness gates; unknown restriction metadata remains
  unknown; `conf edit` resolves symlinks and joins the real mirror mutation
  lock; Jira transient text no longer gains an emitter newline; and Jira
  render/merge block recognizers have one source of truth. Future Jira view
  markers now require a newer binary.

- **Readable, identity-safe Confluence view v2.** Derived page views now expose
  clear `# Metadata`, `# Content`, and `# Comments` regions; comments retain
  native CSF formatting; page links show label plus space/target identity; and
  colored spans use readable HTML. Apply preserves exact native bytes for
  protected constructs, detects link/color loss, and permits unchanged
  marker-looking prose that originated in page content.

- **Cross-service mirror state updates no longer lose entries.** Jira and
  Confluence batches merge only their changed records into the latest
  `.atl/state.json` under a shared root-contained state lock. Concurrent
  sidecar updates now either preserve both services' records or fail closed.

- **HTTP transport failures no longer disclose selectors.** DNS, TLS, timeout,
  and other pre-response failures report the method plus a query-redacted URL
  on buffered, streamed-upload, and streamed-download paths. Cancellation and
  sentinel identity remain testable without exposing URL-bearing causes to
  generic unwrapping loggers.

- **Confluence native-body projections fail closed.** Pull, transient view,
  table extraction, and copy refuse successful-but-partial page responses that
  omit storage-format content. A partial post-push refresh preserves local
  files and sync state and returns a re-pull warning; an explicitly empty body
  remains valid.

- **Serialized Confluence mirror mutations.** Pull, render, apply, and push now
  hold one persistent root-contained advisory lock through page/view/base/state
  updates. Contention fails before partial writes; status remains lock-free.

- **Versioned Confluence derived views.** Every page view and unavailable-view
  stub now carries a versioned document marker plus stable generated section
  boundaries. Apply rejects stale/unknown formats and reserved-marker collisions
  before writes and no longer falls back to ambiguous raw Markdown when the
  pristine CSF cannot be parsed.

- **Versioned Jira views and bounded private workflow state.** Jira derived
  views now carry an explicit format version and apply refuses stale, missing,
  or unknown markers before writes. Profile paths reject control characters;
  rejection/revalidation state keeps deterministic bounded windows; persisted
  failure summaries redact network locations; and renderer/merge wiki scanners
  share list and multiline-table boundary rules.

- **Jira paragraph block-markup collisions.** Literal wiki paragraph lines
  that resemble Markdown fences or thematic breaks are escaped in the derived
  view and reversed on apply, preserving untouched and edited round trips. The
  classifier matches the shared block splitter for whitespace variants, and
  genuine leading backslashes use an injective reversible escape.

- **`apply` no longer injects a `full`-profile view's decorations into the body.**
  `conf apply` now reproduces the recorded pristine view and merges only the
  editable body, so an untouched full view applies to a byte-identical `.csf`;
  editing generated metadata or Comments is refused before body merge.

- **`jira apply` no longer refuses after a flag-overridden render.** It now
  reproduces the view from the render settings the `.md` was actually written
  with instead of the ambient config.

- **Jira attachment upload streaming.** Uploads stream from disk, send an exact
  multipart `Content-Length`, avoid the JSON client's whole-request timeout,
  and unblock the producer on early request-building failures.

- **Private HTTP errors and durable streamed I/O.** API errors now redact query values just like
  verbose traces; Confluence attachment uploads stream with an exact Content-Length; streamed Jira
  exports fsync before atomic rename; and missing/corrupt Confluence mirror metadata maps to the
  actionable check-failure exit instead of a generic error. Maintainer retry guidance now matches
  the read-only generic retry policy.

- **Hash-bound Jira field review and reserved view markers.** File-backed custom-field apply now
  requires the aggregate normalized proposal hash returned by dry-run in addition to Jira
  `updated`, so a changed file cannot bypass review. Jira apply also rejects reserved document or
  section markers inside editable values before writing substrate or pending state.

- **Complete CSV spreadsheet safety.** Structure and planning CSV outputs now use the same default
  formula neutralization as primary Jira and Confluence exports, with an explicit CSV-only
  `--raw-csv` escape hatch for trusted non-spreadsheet consumers.

- **Profile/runtime preference handoff.** Onboarding and later learning now distinguish private
  profile memory from active render and mirror settings, require a separate reviewed runtime sync,
  and make Jira/Confluence workflows verify the effective config or pass the confirmed mirror root
  explicitly instead of assuming an applied profile changed runtime.

- **Jira view hierarchy and human dates.** Generated Jira-owned regions now use
  stable hidden section markers and level-one Markdown headings, while headings
  originating in Description, comments, or rich-text custom fields are nested
  below their owner without losing original `h5`/`h6` levels on apply. Displayed
  Jira datetimes use a deterministic minute-precision human form; raw JSON
  snapshots keep the exact server values.

- **Jira Markdown tables and metadata.** Physical line breaks inside one Jira
  table cell now stay in the logical row and render as spaces instead of
  breaking the GFM table. Legacy escaped-brace bold spans render without stray
  braces. Jira issue metadata now uses one readable Markdown table instead of
  YAML frontmatter; typed field descriptors are simplified to `id`, `label`,
  `metadata|section` placement, format, and optional empty output.

- **Documentation and client-skill contracts.** CI examples preserve failing
  exit codes, Confluence search documents both supported selector modes, Jira
  render examples use valid section names, mirror paths distinguish the
  recommended convention from built-in fallbacks, and derived Markdown plus
  backend-identity-hashed manifest semantics are stated consistently.

- **Jira mapper architecture boundary.** Live REST reads and offline snapshot
  rendering now share a transport-neutral mapper; an automated import-boundary
  test prevents ordinary app use-cases from depending on concrete adapters.

- **Safe, bounded tabular I/O.** Jira JSONL/CSV exports stream atomically,
  aggregate JSON is capped, all Jira and Confluence CSV neutralize spreadsheet
  formulas by default with an explicit `--raw-csv` escape hatch, and Confluence
  attachment multipart bodies stream instead of buffering the full file.

- **Guarded Jira plan execution.** CSV plans now require schema version 1 and
  `expected_updated`, validate link metadata and allowlists before writes,
  compare structured fields canonically, fail fast by default, and preserve a
  non-zero audit result with optional `--continue-on-error`.

- **Jira comment pagination.** Comment listing now fails closed at its safety
  guard instead of returning a partial list that could authorize a duplicate
  write.

- **Jira derived-view correctness.** Jira wiki list markers remain lists when
  the server stores leading whitespace, preventing ordered items from becoming
  Markdown headings. Typed field views normalize valid date/datetime values and
  render a scalar `list` as one item. Epic-child discovery resolves
  lazily, can infer localized epics from returned children when the field is
  configured, and refuses stale/mismatched sidecars during offline render/apply.

- **Mirror filesystem containment and scan failures.** Mirror writes now use
  root-scoped filesystem operations, so a pre-existing symlink in any
  descendant directory cannot redirect page, issue, asset, base, or state
  writes outside the selected mirror root. Directory scans now fail loudly on
  walk, substrate, or Confluence metadata read/parse errors instead of silently
  omitting entries from status or directory push.

- **HTTP origin and replay safety.** Server-provided absolute URLs can no
  longer downgrade an HTTPS backend request to plaintext on the same host;
  only HTTP(S) schemes inside the configured origin policy are accepted.
  Generic retries now apply only to replay-safe reads (`GET`/`HEAD`), so a 429
  or ambiguous 5xx cannot duplicate POST/PUT/PATCH/DELETE writes. Verbose traces
  keep query parameter names but redact their values.

### Added

- **Service-scoped profile render memory.** `profile show --section
  render_defaults --service jira|confluence` returns only the selected
  backend's typed render defaults, while partial suggestions preserve the
  other backend and runtime synchronization remains a separate approval.

- **Guarded Confluence page moves.** `conf page move` now previews by
  default, binds apply to the reviewed source version/current parent/proposal
  hash, rejects hierarchy cycles, preserves fresh native body bytes, and
  reconciles ambiguous outcomes without automatic replay.

- **Guarded Confluence title updates.** `conf page title set` previews bounded
  file/stdin input by default, binds apply to the reviewed title hash and page
  version, writes the unchanged native body under the version gate, and verifies
  ambiguous/success outcomes without automatic replay.

- **Typed read-only Confluence page fields.** Confluence mirror and transient
  views share configurable metadata/section descriptors for title, hierarchy,
  labels, version, update time, and opt-in restriction state. Values are safely
  escaped; dates are human-readable; restriction data is fetched only when
  selected and never guessed during offline rendering.

- **Transient configured Confluence Markdown view.** `conf page view <ID>`
  renders native CSF through local/global presentation settings without writing
  a mirror, assets, baselines, sidecars, or view state. Its body is explicitly
  read-only; optional comments are fetched only when configured.

- **Consent-gated profile learning and revalidation.** Explicit observations
  deterministically produce private hash-bound suggestions without changing the
  active profile; users review and exactly apply or reject them, with rejection
  memory retaining hashes only. Schema revalidation uses explicit cutoffs and
  approved check results, remembers fresh/stale/pending/missing/failed state,
  and routes verified facts back through the same suggestion gate while
  preserving the last verified fact on failures.

- **Composable onboarding and private workflow profiles.** The explicit
  onboarding skill interviews the user, reads only approved sample resources,
  composes declared team defaults, and previews a versioned profile before any
  write. `atl profile show|preview|apply|guidance` separates verified schema
  facts, confirmed preferences, team policy, render defaults, and selectors;
  canonical hashes, optimistic concurrency, an advisory lock, atomic 0600
  storage, and context-efficient section reads keep changes reviewable and
  private.

- **Opt-in editable Jira rich-text sections.** A typed Jira field view can set
  `editable:true` when it uses `section` + `jira_wiki`. `jira apply` stages those
  fields in explicit mirror-private pending state without changing the raw
  snapshot; status and directory push include field-only edits. Push fresh-reads
  every baseline, refuses field drift even with `--force`, sends Description and
  fields in one typed update, reconciles ambiguous responses without replaying,
  and refreshes/clears local state on success. Pending commits are path/hash
  bound and crash-recoverable; `--rebase-pending` provides an explicit reviewed
  conflict workflow. Transient views remain read-only.

- **Guarded file-backed Jira custom fields.** `jira issue field set <KEY>`
  previews by default, reads repeatable `FIELD=PATH` raw or Markdown inputs
  under a 64 MiB aggregate cap, requires exact custom-field allowlists, and
  captures Jira `updated` for a stale-refusing `--apply`. Markdown always
  becomes a Jira-wiki string; raw top-level JSON objects/arrays remain typed.
  JSON review output includes normalized proposals and hashes, while text mode
  and verbose logs omit values.

- **Transient configured Jira Markdown view.** `jira issue view <KEY>` renders
  one live issue through the same profile/typed-field pipeline without writing
  a mirror, snapshot, sidecar, asset, or writeback baseline. Default output is
  `{key,markdown}` JSON; `-o text` emits raw Markdown. The request projection is
  limited to fields required by the resolved view, and `--render-root` can read
  presentation-only local config without modifying it.

- **Typed Jira field views and opt-in epic-child sidecars.** Jira render config
  now accepts `render.jira.field_views`: validated descriptors with a raw field
  id, display label, metadata/section placement,
  `auto|scalar|list|jira_wiki|date|datetime` format, and optional empty-value
  output. Configured fields widen the existing pull search projection, remain
  raw in `<KEY>.json`, render deterministically (long Jira-wiki values can be
  real Markdown sections), and are recorded in `.atl/state.json.views` so
  offline render/apply affinity survives config changes. The existing
  `custom_fields` list remains compatible. A new opt-in `epic_children` render
  section resolves the configured/auto-detected Epic Link field, performs one
  bounded paginated child query per main-search page (never per child), writes
  `<KEY>.epic-children.json`, and renders `# Epic Children` offline. It caps at
  1000 related issues with explicit sidecar/result truncation and a stderr
  warning. Provider-specific browser-only panels remain out of scope.

- **`-o text` loss-review for `conf apply` and `jira apply`.** Both apply commands
  now render a compact, greppable human review under `-o text` (they were JSON-only):
  a dry-run/applied first line, `blocks:` counts (unchanged/moved/converted/removed,
  plus `table merged` for conf), a `removed fragments:`/`removed constructs:` list of
  each dropped fragment or wiki construct, `problems:` and `validation:` for conf, an
  optional `warning:`, and a contextual `next:` hint (write it, accept the loss with
  `--allow-fragment-loss`/`--allow-loss`, or push). Zero counts and empty sections are
  omitted. The JSON contract is unchanged — the text view is a read-only reprojection.

- **`jira apply` — merge markdown-view edits back into the `.wiki` substrate.**
  The Jira analog of `conf apply` closes the loop **pull → edit the `.md` → apply
  → push**: edits to the generated `# Description` section of a `<KEY>.md` view are folded
  block-by-block into the native `<KEY>.wiki`, so agents author in markdown instead
  of hand-writing wiki markup. Untouched blocks keep their exact base bytes (an
  untouched view applies to a byte-identical `.wiki`); changed/new blocks convert
  through the fail-closed `mdwiki` subset. Only generated `# Description` is writable — an
  edit to the generated metadata/title or to the Comments/Links/Image Attachments sections
  is detected and refused (exit 8) with a pointer to the dedicated command, so a
  stray edit never silently vanishes. A wiki-only construct present in the base but
  dropped by the edit (`{panel}`, `{color}`, `[~mention]`, `!embed!`, a macro) is
  reported under `removed_constructs` and refused (exit 8) unless `--allow-loss`.
  Local only — `jira push` remains the guarded write path to the server; the
  precondition mirrors `conf apply` (the local `.wiki` must still match the pulled
  base — a direct `.wiki` edit wins). Flags: `--dry-run`, `--allow-loss`, `--into`,
  and the shared `--render-*` view flags (pass the profile you pulled with). Result
  JSON mirrors `conf apply` (`{path, wiki_path, dry_run, report, wrote, warning?}`).

- **Local per-mirror config layer with `render.*` keys and provenance.** A new
  `<mirror-root>/.atl/config.json` carries presentation-only render settings
  (`render.{jira,confluence}.{profile,include,exclude}`, plus
  `render.jira.custom_fields`). `atl config set` gains a positional dotted-key
  form (`config set render.jira.profile full`) alongside the existing URL flags;
  `--local` (resolving the nearest `.atl` from cwd, or `--into ROOT`) writes the
  per-mirror file. `atl config show` now reports the **effective** merged `render`
  block, a `render_provenance` map (only non-default keys → `global`/`local`), and
  `local_config_path` when a local file is in scope. **Security boundary:** a
  local file may set render keys only — backend/update URLs stay global/env-only
  so a shared or checked-out mirror can never redirect where a PAT is sent.
  `config set --local` refuses any URL flag (exit 2), and at read time any
  credential-adjacent, unknown, or malformed content in a local file is reported
  to stderr and ignored (fail open to global/defaults), never applied. Precedence
  is local > global > default, merged per key. Render settings are stored and
  shown but not yet consumed by the renderers (a later phase threads them through
  `pull`/`render`).
- **Profile-driven markdown views + offline `render` command.** The `.md` read
  view is now configurable per backend by a `minimal` / `default` / `full`
  profile, fine-tuned with `include`/`exclude` section lists. Resolution is
  `--render-profile` / `--render-include` / `--render-exclude` flags **>** local
  config **>** global config **>** built-in `default`, resolved against the
  operation's mirror root. **Jira:** `default` adds `priority` and `parent` to the
  prior view; `full` also renders reporter, created/updated, resolution, due date,
  components, fix versions, sprint, subtasks, a non-image `## Attachments` list,
  and any configured `custom_fields` (by id). `jira pull` widens its API `fields=`
  projection to the active profile, so `full` needs no extra per-issue fetch.
  **Confluence:** `default`/`minimal` stay **byte-identical** to today (body
  only); `full` adds a typed read-only Markdown metadata table and a
  `## Comments` section fed from the `--comments` sidecar when present (absent
  sidecar → section skipped, never an error). New `jira render [DIR|FILE]` /
  `conf render [DIR|FILE]` regenerate `.md` views from the local
  substrate/meta/sidecars **offline** (no network or PAT) so switching profiles
  never forces a re-pull; they rewrite only `.md` and leave the
  `.csf`/`.wiki`/`.json`/sidecar substrate untouched, so `status` stays clean.
  `pull` gains the same three `--render-*` flags; the `pull` result JSON is
  unchanged by profiles (unknown section names surface as a stderr warning).
- **`conf pull --comments` brings page comments into the mirror.** Opt-in and off
  by default (without it, no comment endpoint is contacted and no files are
  written). When set, each mirrored page gains `<slug>.comments.json` (a
  `[{id, author, created, body}]` array) and a derived `<slug>.comments.md` read
  view, and its `.meta.json` reports `comment_count` (plus `comments_truncated`
  when the fetch cap is hit). Comments are auxiliary read-only data — they never
  enter the page content hash or the version gate, so a page with comment
  sidecars still reports Clean in `conf status`. Comment bodies are a plain-text
  read view (CSF stripped). A re-pull with `--comments` refreshes the sidecars; a
  re-pull without it leaves existing comment files untouched.
- **Guarded Jira write-back: `jira status` and `jira push`.** `jira pull` now
  wires each issue through the mirror sidecar — recording the `.wiki` body's
  hash in `.atl/state.json` and a pristine `.atl/base/<KEY>.wiki` base copy — so
  an edited `.wiki` can be pushed back. `jira status [DIR] [--remote]` reports
  which issues are locally edited (and, with `--remote`, drifted on the server),
  content-hash based, the Jira analog of `conf status`; a `.wiki` with no
  sidecar entry reads never-synced (`synced:false`). `jira push
  <file.wiki|DIR>` writes an edited description back — **dry-run by default**
  (`--apply` to write), previewing a unified diff. Jira has no server-side
  version gate, so staleness is an app-layer compare-and-swap against the pulled
  base: a drift is refused with **exit 8** (`ErrCheckFailed`, "re-pull or
  `--force`"), **never** exit 5, and this CAS's inherent TOCTOU window is
  documented rather than hidden. Only the description body is written (no other
  field); a server-side HTTP 409 stays a generic conflict (#66). `--force`
  overrides the drift refusal; a directory push touches only locally-edited
  files. On `--apply` success the mirror is refreshed; a refresh failure is a
  warning, not an error.
- **`jira pull` now writes a native `<KEY>.wiki` substrate beside a rendered
  `<KEY>.md` view.** The issue's Jira wiki body is stored byte-for-byte in
  `<KEY>.wiki` — the editable source of truth, mirroring the role `.csf` plays
  for Confluence (written even when empty so the substrate always exists). A new
  best-effort, lossy wiki→Markdown renderer (`internal/wikimd`) produces the
  `.md` read view: headings, `*bold*`/`_italic_`/`{{mono}}`/`-strike-`,
  `{code}`/`{noformat}`/`{quote}`/`{panel}`, `*`/`#` lists, `||`/`|` tables,
  `[text|url]` links, `!image!` embeds (resolved to the downloaded `.assets/`
  path when a `--assets` pull ran, else shown as unresolved-image inline code),
  `{color}`, and `[~mentions]`. The renderer never fails a pull — a panic
  degrades one section to a stub that points at `<KEY>.wiki`. The `<KEY>.json`
  snapshot is unchanged; the pull result JSON keeps `path` pointing at the `.md`
  and gains `wiki_path` pointing at the editable substrate. Generated markdown
  image links escape markdown-significant filename characters, and code fences
  widen past any backtick runs in `{code}`/`{noformat}` bodies.
- **`jira pull --assets` mirrors image attachments into per-issue asset dirs.**
  The opt-in `--assets` flag streams each pulled issue's image attachments
  (media type `image/*`) into `<KEY>.assets/<attachment-id>-<filename>` and links
  them from a generated `## Image Attachments` section in the read-only `.md`
  (placed between the description and links). Bytes stream directly by each
  attachment's content URL, adding no extra per-issue network call. Download is
  best-effort: a failed image is skipped, counted in `assets_skipped`, and
  reported via a single stderr warning; the issue is still written and only
  images on disk are linked. Duplicate filenames stay distinct via the id
  prefix, and hostile filenames/ids cannot escape the assets directory.
  Attachments with an empty or `application/octet-stream` media type are skipped
  (same as `jira issue images`). Default `jira pull` output is unchanged (the new
  `assets` / `assets_skipped` JSON fields are omitted at zero), and the raw
  `<KEY>.json` snapshot never carries local paths.
- **Jira issue attachments can now be listed, downloaded, and uploaded directly.**
  `jira issue attachment list <KEY>` exposes attachment ids for piping
  (`-o id`), and `jira issue attachment get <KEY> --id <ID-or-filename>`
  downloads any attachment type through atl's existing Jira auth, host-scoped
  HTTP client, and safe path handling. `jira issue attachment upload <KEY>
  --file <PATH>` uploads a local file through the Jira multipart attachment API.
  The image-only `jira issue images` helper remains unchanged.

### Changed

- **Confluence skill progressive disclosure.** The always-loaded workflow now
  focuses on safe read/edit/write decisions and routes command inventory,
  metadata/comments, tables/attachments, errors, and advanced CSF details to
  one-hop references.

- **`.atl/state.json` gains a `views` map, and `apply` reproduces the recorded
  view.** Every `pull`/`render`/`apply` now records the resolved render settings
  (the enabled section list, not the profile name) per resource, keyed the same
  as the `pages` sync entries. `conf apply` / `jira apply` rebuild the pristine
  view from that record instead of the ambient config, so `--render-*` flags are
  no longer needed on `apply` (they still override the recorded view). `render`
  writes only the `views` map — never a `pages` entry — so `status` stays clean.
  Pre-upgrade mirrors with no recorded view fall back to the ambient config
  (re-run `render` once to record it).
- **`conf comment list` no longer truncates silently.** When a page's comment
  listing hits the pagination safety cap, the command now writes a `warning:`
  line to stderr (the returned set is incomplete); the JSON result on stdout is
  unchanged.
- **The Jira mirror `<KEY>.md` is now a pure rendered read view.** It previously
  embedded the raw wiki body verbatim under a `## Description (Jira wiki)`
  section; it now shows a rendered `## Description` (and rendered comment bodies)
  produced by `internal/wikimd`, with the verbatim body moved to the new
  `<KEY>.wiki` file. Existing mirrors regenerate to the new layout on the next
  `jira pull` (the old single-`.md` envelope is replaced and `.wiki` appears
  alongside); edit `<KEY>.wiki`, not the `.md`, to change a body.
- **`mdwiki` preserves intra-paragraph line breaks.** A soft-wrapped markdown
  paragraph now converts to wiki with a real newline between lines instead of a
  single space, so the line structure visible in a Jira `.md` view is the line
  structure Jira renders. This fixes `jira apply`: editing one word of a
  multi-line paragraph through the markdown view no longer collapses the
  paragraph's visual line breaks. It also changes `jira issue create/update/
  comment --from-md` — multi-line paragraphs now publish with line breaks.
  Inline markup is converted per line, so an emphasis span that wraps across a
  line break (`**bold\nwrapped**`) no longer pairs and falls back to its escaped
  literal; each inner line is guarded so one Jira would parse as its own block
  (a heading/blockquote line) is refused and a leading list marker is escaped to
  render literally.

## [0.3.0] - 2026-07-05

### Added

- **Plugin versions are now locked to CLI releases.** The release workflow
  refuses to build unless both plugin manifests (`.claude-plugin/plugin.json`
  and `plugins/atl/.codex-plugin/plugin.json`) carry the tag's version, so
  installed plugins update in lockstep with the self-updating binary instead
  of silently freezing at their install-time skills. The `atl` and `setup`
  skills gained a "version skew" note teaching agents to diagnose an
  unknown-command error (exit 2) by comparing `atl version` with the plugin
  version and updating the lagging side.
- **Codex plugin packaging.** The repo now also ships the atl skills as a
  Codex plugin (`plugins/atl` + a repo-local marketplace at
  `.agents/plugins/marketplace.json`): `codex plugin marketplace add
  isukharev/atl`, then `codex plugin add atl@atl`. Both plugins are generated
  from a single source of truth in `skills-src/` by `make gen-plugins`
  (per-platform strings via `{{atl.var}}` placeholders), so the Claude Code
  skills keep their native `/atl:setup` wording while Codex gets `$setup` —
  CI rejects stale or hand-edited generated trees. Pipeline guide:
  `docs/plugins.md`.
- **Six workflow recipe skills in the shipped Claude Code plugin**:
  `search-knowledge` (cited answers from Confluence + Jira), `triage-issue`
  (duplicate/regression search before filing), `status-report` (Jira-derived
  report, optional Confluence publish), `spec-to-backlog` (spec page → Epic +
  linked tickets), `sprint-dashboard` (read-only visual sprint snapshot), and
  `meeting-tasks` (action items from notes → assigned tasks). Recipes
  orchestrate end-to-end processes on top of the existing `atl`/`confluence`/
  `jira` reference skills, and every write path requires explicit user
  approval before anything is created.

### Fixed

- **Attachment downloads stream to disk instead of buffering up to 1 GiB in
  RAM, and are no longer killed by the 60-second whole-request timeout.**
  `conf attachment get` and `jira issue images` now write through an atomic
  temp-file copy (bounded memory; an interrupted transfer never leaves a
  truncated file) and the transfer is limited by inactivity — a stall of 60s
  fails with a clear "download stalled" error, but a slow live transfer of any
  size completes. Host-scoped auth, redirect refusal, and retry-until-headers
  semantics are unchanged.
- **`jira pull` no longer re-fetches every issue after the search.** The
  search projection already carries the requested fields through the same
  adapter mapping, so the per-issue `GET` doubled HTTP round trips (and 429
  exposure) on large pulls for zero data gain. A failed `.json` snapshot
  write now also aborts the pull loudly instead of being silently discarded —
  a disk-full run can no longer report issues as pulled with missing or stale
  snapshots.
- **A stale `.md` read-view can no longer survive a pull whose body fails to
  parse.** Previously the old revision's `.md` stayed on disk next to the new
  `.csf` with no signal; now it is overwritten with an explicit
  "markdown view unavailable for this revision" stub, so the read-view never
  contradicts the source of truth. An `.md` write failure now degrades
  (removing the stale view) instead of failing the pull — matching the
  documented best-effort contract. `conf apply` upholds the same invariant
  after a merge: an unparseable merge result stubs the `.md`, and a failed
  refresh write is reported in a new `warning` field instead of failing an
  apply whose `.csf` write already succeeded.
- **The mirror sidecar (`.atl/state.json`) is now crash-safe and honest about
  corruption.** Saves are atomic (temp + fsync + rename), so an interrupted
  write can never leave a half-written file; a corrupt sidecar now fails
  `status`/`push`/`pull`/`apply` with an actionable error (exit 8) instead of
  silently resetting every page to "never synced" (which quietly disabled
  drift detection). A pull also now loads and saves the sidecar once per run
  instead of once per page — on an aborted pull, pages already mirrored keep
  their sync state.
- **`conf pull` can no longer silently overwrite a different page whose title
  slugifies to the same directory.** Page-dir slugs are lossy (`Foo Bar` and
  `Foo-Bar?` both become `foo-bar`); the mirror now checks the existing
  `.meta.json` before writing and diverts the newcomer to an id-suffixed dir
  (`foo-bar-<id>`, stable across re-pulls). A dir holding page files whose id
  cannot be read is treated as foreign, and an unresolvable collision refuses
  loudly (exit 8) instead of overwriting.

### Added

- **`jira issue edit` — one-command targeted description edit.** Fetches the
  description, replaces `--old` with `--new` via the same
  whitespace/invisible-tolerant matcher as `conf edit`, and writes it back —
  no more get → compose → update ceremony for small changes. Unique match
  required unless `--all` (ambiguous → exit 2); a missed needle refuses with
  exit 4 and a quoted closest-region dump instead of overwriting, which also
  doubles as the drift guard on a backend with no version gate. Supports
  `--old-file`/`--new-file` (`-` for stdin), `--new ''` to delete the match,
  and `--dry-run`. A whitespace-tolerant match that would cross a line break
  is refused (exit 8) — Jira wiki is line-sensitive, and a cross-line splice
  could silently merge lines.
- **`conf apply` — the markdown view becomes an editable surface.** Edit
  `page.md`, run `atl conf apply page.md`, and the edits merge into the
  `.csf` block by block: untouched blocks keep their **exact** base bytes,
  changed/new blocks convert from a strict markdown subset (headings,
  paragraphs, lists, task lists, simple tables, fenced code,
  blockquotes/admonitions, links, `[[page links]]`, `[KEY](jira:KEY)`), and
  opaque elements in edited blocks (macros, mentions, links, images, colored
  spans) are substituted back from their original bytes so identity survives.
  Fail-closed with nothing written (exit 8) on unconvertible blocks,
  ambiguous mentions, dropped fragments (macros counted by name; override
  with `--allow-fragment-loss`), or a `.csf` that diverged from the
  last-synced base; the merged body is always validated and `conf push`
  remains the only write path to the server. Underpinned by new element byte
  offsets in the CSF parser, a block-segmented renderer, and a fuzzed md→CSF
  converter; a no-edit apply reproduces every page in a 78-page real-content
  corpus byte-identically.
- **`conf apply` merges styled tables row/cell-wise.** Tables carrying editor
  boilerplate (cell `style`/`class` attributes, wrapper divs, spans) — i.e.
  nearly every table saved from the Confluence UI — are no longer refused
  wholesale: untouched rows keep their exact bytes, an edited cell splices
  its converted content into the existing cell wrapper (styling survives), a
  deleted row drops its byte range under the fragment-loss gate, and an
  inserted row clones a neighboring row's byte structure. Mentions/links
  copied from untouched rows are cloned byte-exactly; macros are never cloned
  (identity). Still fail-closed: edits through rowspan/colspan continuations,
  row deletion across a rowspan, column add/remove, nested tables, header
  relocation. The apply report gains `merged_tables`.
- **`conf page create --from-md` — author new pages in markdown.** The body
  converts whole-document through the same fail-closed md→CSF converter that
  powers `conf apply`: every block must be inside the supported subset, the
  first unconvertible block aborts with exit 8 naming it (nothing is created),
  and an empty document is refused. Mutually exclusive with `--from-file`;
  the converted body still passes the CSF validation gate before the API call.
- **`jira --from-md` — author issue bodies in markdown.** `jira issue create`,
  `issue update`, and `issue comment add` accept `--from-md <file|->`: the body
  converts through a new fail-closed md→wiki converter (`internal/mdwiki`,
  fuzzed) — headings, emphasis, lists, GFM tables, fenced code, blockquotes,
  links, `[KEY](jira:KEY)` issue links, `[~username]` mention passthrough;
  wiki-active characters in prose are escaped automatically. The first
  unconvertible block (task lists, images, mid-word emphasis, pipes in table
  cells) aborts with exit 8 naming it, and nothing is sent. Mutually exclusive
  with `--from-file`, which stays the raw wiki path.
- **Skill: consent-based friction reports.** When `atl` itself causes real
  friction for an agent (repeated failures, forced fallbacks, misleading
  errors), the shipped skill now offers the user — behind two separate,
  explicit consent gates — a sanitized public issue (with a strict redaction
  checklist) and/or a detailed private local case file
  (`atl-feedback/<date>-<slug>.md`) the user can hand to their internal
  development team for reproduction and a fix. Nothing is ever reported
  automatically.
- **Agent-friendly output** — `-o id` prints just the primary identifier(s), one
  per line, for safe piping (`atl jira issue search … -o id | xargs …`); wired
  into `jira issue search`/`create` and `conf search`.
- **`--verbose` / `ATL_VERBOSE=1`** — trace every HTTP request/response to stderr
  (method, URL, status). The bearer token is never logged.
- **`jira issue assign`** — first-class assignee changes via the dedicated DC
  endpoint: `--to <username>`, `--me` (resolves the authenticated user), or
  `--none` (unassign). Avoids the `--field 'assignee={"name":...}'` escape
  hatch; the `--field` help on `create`/`update` now also documents that JSON
  object/array values are sent as JSON.
- **Jira**: `issue history` (changelog via the DC-universal `?expand=changelog`,
  not the Cloud `/changelog` sub-resource); `issue comment {list,delete}` and
  `issue link {list,delete}`; `transition --field k=v` (set fields on a
  transition); `issue check` (audit required/important fields — reports on stdout
  and exits non-zero when a required field is empty, for CI/pre-transition gating);
  `issue delete --force`; `issue labels --add/--remove`; `jira me` and
  `jira user {search,get}` using the DC username/userkey identity model.
- **Jira boards & sprints** (Jira Software / GreenHopper, via the Data Center
  Agile API `/rest/agile/1.0/`): `jira board {list,get}` and `jira sprint
  {list,get,current,issues,add,remove}`. `board list --project` and `sprint list
  --board ID --state active|closed|future` drive discovery; `sprint current
  --board ID` returns the active sprint (exit 4 when none); `sprint add <ID>
  <KEY>…` / `sprint remove <KEY>…` move issues into a sprint / back to the
  backlog. Boards and sprints are addressed by numeric id (name resolution is
  deferred to a future metadata cache).
- **Jira Structure read-only exports** — `jira structure {get,forest,rows,values}`
  reads Tempo Structure metadata, raw forests, parsed row hierarchies, and
  selected row attribute values via `/rest/structure/2.0/`, exposing
  `inaccessible_rows` when the backend reports permission gaps.
- **Jira analytical snapshots** — `jira pull --fields` includes requested custom
  fields in each issue's JSON snapshot, and `jira fields` can be narrowed with
  `--name-like` or `--id`.
- **Jira compact exports** — `jira export --jql ... --out FILE --format
  jsonl|json|csv` writes one analysis artifact plus a backend-identity-hashed manifest with
  query, fields, count, CLI version, and a backend URL hash (no hostname or
  PAT). `--ids` / `--keys` generate safely batched JQL, and `jira export diff`
  compares compact snapshots for added/removed/changed issues.
- **Jira planning reports** — `jira planning report --jql ...` produces
  deterministic per-issue planning quality gaps, extracted artifact references,
  optional estimate/required-field checks, epic child lists, and optional CSV
  output without any Jira writes.
- **Jira guarded writeback design** — documented the approval-gated safety model
  for future Jira link/label/field writeback commands; no writeback
  implementation is included.
- **Confluence**: `conf search` convenience flags (`--space/--title/--label/--type`
  build escaped CQL); `conf page list` (flat listing in a space, `--status`);
  `conf page open` (open in the system browser); `conf page copy` (client-side
  copy that preserves native CSF bytes — no markdown round-trip); `conf attachment
  {list,get,upload,delete}`; `conf me`; internal page links now render as
  `[[Title]]` in the read-only `.md` view.
- **Shell completion** for fixed-value flags (`-o`, `--format`, `--status`,
  `--service`).
- **Skill authoring references** — the shipped plugin now teaches agents the
  body syntax, not just the commands: a Jira wiki-markup cheat sheet
  (`skills/jira/reference/wiki-markup.md`, "this is NOT Markdown"), a validated
  CSF snippet library for new pages/sections/comments
  (`skills/confluence/reference/csf-authoring.md`), and corrected `--field`
  value-shape guidance (object-typed fields take JSON, e.g.
  `priority={"name":"High"}`; the old `--field priority=High` example was
  rejected by Jira DC).
- **`conf edit`** — precise in-place replacement for local CSF files that
  tolerates the invisible bytes defeating exact-match editing (`U+00A0`,
  zero-width characters, `&nbsp;`-family entities). Layered matching (exact →
  invisible-tolerant → whitespace-run-tolerant) locates the target and splices
  the replacement into exactly the matched byte range, preserving every
  surrounding byte; refuses ambiguity (exit 2, unless `--all`) and dumps the
  closest region's hidden bytes when nothing matches (exit 4). `.csf` results
  are auto-validated (`csf_ok`); `--dry-run`, `--old-file`/`--new-file`
  supported.
- **`conf validate` warns about invisible characters** — a new advisory
  `invisible-chars` rule reports non-breaking spaces (`U+00A0`), zero-width
  characters, and soft hyphens (count + first position, one warning per
  class), so the trap is visible *before* an exact-string edit misses.
  Warnings remain non-blocking.
- **CSF editing tips in the skill** — measured on real pages, agents lose time
  not writing CSF but *matching* it (single-line bodies with invisible
  `U+00A0` bytes defeat exact-string edits). `skills/confluence/reference/csf.md`
  now prescribes short unique anchors, byte-level inspection after one failed
  match, and checked scripted replacement for table rows.
- **Situational CSF-editing guidance in the skill** — measured across editing
  techniques, no single one is cheapest everywhere: `conf edit` wins insertions
  and macro-dense regions but over-costs long spans when fed whole sections via
  helper files; scripted replacement wins long spans but flails on invisible
  bytes. `skills/confluence/reference/csf.md` now opens with a decision table
  (exact-match edit with a short unique anchor, one attempt → `conf edit` on a
  miss / for insertions / on an `invisible-chars` warning → boundary-anchor
  scripted splice for whole sections and table rows) plus anti-ceremony rules
  (inline `--old`/`--new`, no helper files, no routine `--dry-run`, no separate
  validate after `conf edit`).
- **Dev-loop recipe** — `skills/atl/reference/dev-loop.md`: the end-to-end
  sequence for driving a ticket from a coding agent (take it, keep it truthful
  while developing, close with evidence, update the linked Confluence page
  under the version gate), with the safety rails restated inline.
- **Dev tooling** — `make install-hooks` installs a gofmt pre-commit hook; CI
  gained a `go mod tidy` drift check and a `CGO_ENABLED=0` static-build assertion.

### Fixed

- **Body-from-stdin no longer hangs on an interactive terminal.** Commands
  whose `--from-file` defaults to stdin (`conf page create`, `conf comment
  add`, `jira issue comment add`) used to block forever when nothing was
  piped; they now exit 2 immediately with a message naming the remedy. The
  default convention is now documented: body-required commands read stdin by
  default, body-optional ones (`jira issue create/update`) default to no body.
- **Jira HTTP 409 no longer masquerades as a version conflict.** Jira DC has
  no optimistic version gate, so a 409 on a Jira write (locked issue, closed
  sprint, workflow veto) now surfaces as a generic error (exit 1) carrying the
  backend's own 409 body, instead of exit 5 — whose re-pull/`--force`
  remediation only exists for Confluence. Confluence 409 handling is
  unchanged.
- **`--space` truncation is now visible** — a `conf pull --space` or
  `conf space tree` that hits the 2000-page safety cap now reports
  `"truncated": true` (pull also sets `"truncated_at"`) and prints a
  `warning:` line to stderr; previously the listing stopped silently and the
  mirror looked complete.
- **`jira issue comment list` and `conf page history` now return everything** —
  both previously fetched a single server page (comments: first page only;
  history: first 50 versions) and silently dropped the rest; they now page
  internally until the listing is exhausted.
- **Corrupt `--cursor` values are rejected** — a non-numeric or negative cursor
  previously restarted silently from the first page; it is now a usage error
  (exit 2) across Jira/Confluence search and board/sprint listings.
- **Oversized stdin bodies are rejected, not truncated** — `--from-file -`
  previously dropped everything past 64 MiB without a signal, so an oversized
  Jira body could be published incomplete; it now fails with a usage error
  (exit 2) naming the limit.
- **`conf push` no longer panics on an unresolvable target path** — a target
  that cannot be stat'ed (typo'd file, missing directory) previously crashed
  with a nil-pointer dereference under `-o text` and printed a stray `null` in
  JSON mode; it now reports a usage error and exits 2 instead of 1.
- **`jira plan apply` / `jira link suggest` idempotency** — existing links are
  now matched by the canonical link-type name as well as the directional
  phrase, so re-applying a satisfied plan row (e.g. type `Duplicate`, phrase
  "duplicates") reports `already_satisfied` instead of creating a duplicate
  link. Issue links additionally expose `type_name` (canonical name) next to
  the human-readable `type`.
- **`conf page history` on Confluence Data Center** — list page versions via
  `/rest/experimental/content/{id}/version` instead of the Cloud-style
  `/rest/api/content/{id}/version`, which returns 404 on DC.
- **`jira field-options` on Jira Data Center 9.x** — resolve a field's allowed
  values through the two-step `/issue/createmeta/{projectKey}/issuetypes[/{id}]`
  endpoints; the older expand-based `/issue/createmeta?expand=…` query was removed
  in newer Jira DC and returned 404.
- **Confluence markdown table fidelity** — the read-only `.md` view now repeats
  `rowspan` values across covered rows, preserves ordinary links in table cells,
  and marks colored spans instead of making them indistinguishable from plain
  text.
- **Setup and REST guidance** — `config show` now includes mirror-root hints, and
  the docs cover direct Server/Data Center REST fallback calls without placing
  PATs in argv.

### Changed

- **Stray positional arguments are now usage errors (exit 2).** Flag-only
  commands (`conf search`, `jira issue search`, `jira fields`, `conf page
  create`, …) previously accepted and silently dropped extra positional
  arguments — `atl jira issue search --jql … PROJ-1` ran on the full JQL
  result. Every flag-only command now rejects them, and any arity violation
  on positional commands also exits 2 (previously the generic 1), matching
  how flag-parse errors are already reported.
- **BREAKING** — `jira issue comment <KEY>` is now `jira issue comment add
  <KEY>`, and `jira issue link <KEY> --to … --type …` is now `jira issue link
  add <KEY> …`. The `comment` and `link` verbs became subcommand groups so they
  can host the new `list`/`delete` subcommands (consistent with `conf comment`).
- Live integration tests now run via `make integration`, which sources a
  gitignored `.env.integration` (template: `.env.integration.example`) so backend
  URLs and PATs stay local. Adds read-only coverage for `conf page history` and
  `jira field-options` against a real DC.
- `jira pull` sidecar JSON is now an identity snapshot (`{key,id,fields}`) instead
  of a bare raw fields map, so scripts can reliably join snapshots back to Jira.

---

## [0.2.0] - 2026-06-22

### Added

- **Interactive `atl auth login` setup wizard** (gh-style) — run without flags to
  be prompted per service for the base URL and PAT, validate the token against the
  backend, and store both; any service can be skipped.
- **Exit code `7` ("not configured")** — a missing backend URL or a missing PAT
  now exits `7` with an actionable message (the exact `atl config set` /
  `atl auth login` command), distinct from `3` (a PAT was supplied but the server
  rejected it). A corrupt/unreadable credentials file stays a generic `1`.
- **`ATL_MIRROR_ROOT`** — default mirror root for `conf pull`, `conf status`, and
  `jira pull`, so a workspace fixes one location without re-passing `--into`
  (an explicit `--into` still wins; `conf push` resolves the nearest `.atl`).
- **`--cql` truncation is now visible** — a pull that hits the 1000-page cap sets
  `"truncated": true` / `"truncated_at"` in the JSON result and prints a
  `warning:` line to stderr instead of implying the mirror is complete.
- **Homebrew formula** published as a release asset (`atl.rb`, each platform's
  URL pinned to its SHA-256) via `make homebrew`; `brew install isukharev/tap/atl`.
  The release workflow can auto-push the formula to the tap when a
  `HOMEBREW_TAP_TOKEN` secret is configured (otherwise it is copied manually).
- Documentation: a **Quick start**, a **Scripting & CI** guide, a Server/Data
  Center vs Cloud note, a Troubleshooting table (README EN + RU), and a
  `docs/` index.

### Changed

- **Breaking (scripts/CI):** a *missing* backend URL or PAT now exits `7` instead
  of `2`/`3` respectively; branch on `7` for "not set up yet".
- **Breaking (scripts/CI):** on failure the error is written to stderr as JSON
  `{"error": "...", "code": N}` by default (use `-o text` for the previous
  `error: <msg>` line).

### Fixed

- `setup` skill: corrected the Go fallback to
  `go install github.com/isukharev/atl/cmd/atl@latest` (the module root has no
  `main` package, so the old path failed).

---

## [0.1.0] - 2026-06-20

### Added

- **First public release** of `atl` — an agent-native CLI for Confluence and
  Jira Data Center, designed for use inside coding agents and automated
  pipelines.
- **On-disk mirror** of Confluence pages and Jira issues in native storage
  format (Confluence Storage Format / CSF), enabling diff-friendly edits
  without round-tripping through lossy Markdown conversion.
- **Optimistic version-gate push** — writes are rejected if the server version
  has advanced since the last pull, preventing silent overwrites during
  concurrent edits.
- **draw.io / diagram fragment resolution** — attachments and embedded diagrams
  are fetched and stored alongside page content so agents can inspect them.
- **Automatic, signature-verified background self-update** — on each command
  start the binary checks for a new release from GitHub Releases (at most
  once every 6 hours) and verifies the SHA-256 checksum and ed25519 signature
  before replacing itself.

---

<!-- link references -->

[Unreleased]: https://github.com/isukharev/atl/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/isukharev/atl/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/isukharev/atl/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/isukharev/atl/releases/tag/v0.1.0
