# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

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
