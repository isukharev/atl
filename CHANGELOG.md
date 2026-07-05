# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

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
  jsonl|json|csv` writes one analysis artifact plus a sanitized manifest with
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

[Unreleased]: https://github.com/isukharev/atl/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/isukharev/atl/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/isukharev/atl/releases/tag/v0.1.0
