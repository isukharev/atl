# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- **Agent-friendly output** — `-o id` prints just the primary identifier(s), one
  per line, for safe piping (`atl jira issue search … -o id | xargs …`); wired
  into `jira issue search`/`create` and `conf search`.
- **`--verbose` / `ATL_VERBOSE=1`** — trace every HTTP request/response to stderr
  (method, URL, status). The bearer token is never logged.
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
- **Jira analytical snapshots** — `jira pull --fields` includes requested custom
  fields in each issue's JSON snapshot, and `jira fields` can be narrowed with
  `--name-like` or `--id`.
- **Confluence**: `conf search` convenience flags (`--space/--title/--label/--type`
  build escaped CQL); `conf page list` (flat listing in a space, `--status`);
  `conf page open` (open in the system browser); `conf page copy` (client-side
  copy that preserves native CSF bytes — no markdown round-trip); `conf attachment
  {list,get,upload,delete}`; `conf me`; internal page links now render as
  `[[Title]]` in the read-only `.md` view.
- **Shell completion** for fixed-value flags (`-o`, `--format`, `--status`,
  `--service`).
- **Dev tooling** — `make install-hooks` installs a gofmt pre-commit hook; CI
  gained a `go mod tidy` drift check and a `CGO_ENABLED=0` static-build assertion.

### Fixed

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

### Changed

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
