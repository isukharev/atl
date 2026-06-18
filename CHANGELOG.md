# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

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

[Unreleased]: https://github.com/isukharev/atl/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/isukharev/atl/releases/tag/v0.1.0
