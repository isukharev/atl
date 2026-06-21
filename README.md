**English** · [Русский](README.ru.md)

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=CI)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

**A Git-style CLI for Confluence & Jira — built for coding agents.**

`atl` lets a coding agent (e.g. Claude Code) interact with Confluence and Jira the same way
it interacts with code: mirror documents to disk, search with `ripgrep`, edit the
**native storage format** (Confluence Storage Format, `.csf`), reason in diffs, and push
under an **optimistic version gate** that refuses to silently overwrite concurrent edits.

> **Non-affiliation notice:** This project is an independent open-source tool and is NOT
> affiliated with, endorsed by, or sponsored by Atlassian Pty Ltd. See the
> [Trademarks & Disclaimer](#trademarks--disclaimer) section below.

---

## Features

- **Mirror to disk** — pull pages (with assets) into a local directory tree that mirrors
  the Confluence page hierarchy; search with any text tool.
- **Native format editing** — work directly on `.csf` (Confluence Storage Format) bytes;
  no lossy Markdown round-trip means macros, panels, layouts, and diagrams are never silently lost.
- **Optimistic version gate** — `push` refuses on remote drift (exit 5) and reports consequences
  in `--dry-run`; `--force` overrides when you know what you are doing.
- **Diagram awareness** — draw.io macros are resolved to PNGs of the exact revision so a
  vision-capable agent can inspect them.
- **Jira integration** — query, comment, transition issues; mirror an issue set to disk.
- **Bearer PAT auth, per-request** — tokens are sent only to the configured host and never
  stored in the repo or mirror.
- **Signed self-update** — the binary updates itself from GitHub Releases, throttled to every
  6 hours, with SHA-256 checksum and ed25519 signature verification. See
  [docs/self-update.md](docs/self-update.md) and [SECURITY.md](SECURITY.md).
- **Scripting-friendly** — JSON to stdout, logs/errors to stderr, no interactive prompts,
  well-defined exit codes.
- **Single static binary** — `CGO_ENABLED=0`, runs anywhere Go 1.26 runs.

---

## Install

### Quick install (Linux / macOS)

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

Installs to `~/.local/bin/atl` and verifies the SHA-256 checksum. SLSA build
provenance is published with every release for optional out-of-band verification
(see [docs/RELEASING.md](docs/RELEASING.md)); the installer itself does not require `gh`.

### go install

```sh
go install github.com/isukharev/atl/cmd/atl@latest
```

### Binary download

Download a pre-built binary from the [GitHub Releases](https://github.com/isukharev/atl/releases)
page. Checksums and signatures are published alongside each release.

---

## Authenticate

```sh
# 1. Set the base URLs for your Confluence and Jira instances
atl config set \
  --confluence-url https://confluence.example.com \
  --jira-url       https://jira.example.com

# 2. Supply Personal Access Tokens (PAT) — Server/Data Center only.
#    The token is read from a no-echo prompt, stdin, or --from-file — never argv.
atl auth login --service confluence   # prompts without echo
atl auth login --service jira         # prompts without echo

# Or use environment variables (preferred for CI / agent sessions):
export ATL_CONFLUENCE_PAT=<PAT>
export ATL_JIRA_PAT=<PAT>

# 3. Verify
atl auth status
atl config show
```

Tokens are stored in a `0600` credentials file under `~/.config/atl` (or read from the
env vars above). They are never written to the mirror or to any repo.

---

## Confluence workflow

### 1. Pull pages to disk

```sh
atl conf pull \
  --cql 'space=DOCS and title~"Acme"' \
  --assets \
  --into mirror
```

### 2. Explore the mirror

```
mirror/
  DOCS/                         # space key
    acme-adr/
      acme-adr.csf              # source of truth (native storage format)
      acme-adr.md               # read-only view: prose + ⟦fragment⟧ + ![](assets/…)
      acme-adr.meta.json        # id, version, content hash, resolved fragments
      acme-adr.assets/*.png     # draw.io renders + page images (with --assets)
      child-page/…              # folder tree mirrors the page hierarchy
  .atl/                         # sidecar: last-synced versions/hashes + pristine base
  .gitignore
```

Use any text tool to search across the mirror:

```sh
rg "decision" mirror/
```

### 3. Validate & push

```sh
# Edit the native storage format directly
$EDITOR mirror/DOCS/acme-adr/acme-adr.csf

# Validate before pushing (blocks on malformed XML, warns on sanity issues)
atl conf validate mirror/DOCS/acme-adr/acme-adr.csf

# Dry-run to see what push will do
atl conf push mirror/DOCS/acme-adr/acme-adr.csf --dry-run

# Push (exits 5 if the remote has drifted; re-pull + reapply to recover)
atl conf push mirror/DOCS/acme-adr/acme-adr.csf

# Check sync status
atl conf status mirror --remote
```

### Other Confluence commands

```sh
atl conf search --cql 'space=DOCS and label="adr"'
atl conf space tree --space DOCS
# Page ids come from atl conf pull output (meta.json → "id" field) or the page URL.
atl conf page get     --id 123456
atl conf page get     --id 123456 --format csf
atl conf page meta    --id 123456
atl conf page history --id 123456
atl conf page create  --space DOCS --parent 123456 --title "My Page" --from-file body.csf
atl conf page move    --id 123456 --parent 654321
atl conf page delete  --id 123456
atl conf comment list --id 123456
atl conf comment add  --id 123456 --from-file comment.csf
```

---

## Edit model & safety

The `.csf` bytes are the **substrate** — what you write is exactly what gets pushed.
There is no lossy Markdown round-trip, so macros, panels, layouts, and diagrams are
never silently discarded.

| Safeguard | Behaviour |
|-----------|-----------|
| `atl conf validate` | Blocks on malformed XML (reports line/col); warns on structural issues |
| `atl conf push --dry-run` | Reports all consequences without writing anything |
| Version gate | `push` exits **5** when the remote version has advanced since the last pull |
| `--force` | Overrides the version gate; the safe recovery path is re-pull + reapply |
| Diagrams | draw.io macros resolved to PNGs of the exact revision for visual inspection |

---

## Jira

```sh
# Read
atl jira issue get  PROJ-1
atl jira issue search --jql 'project = PROJ AND status = "In Progress"'

# Mirror an issue set to disk
atl jira pull --jql 'project = PROJ' --into mirror-jira

# Write
atl jira issue comment    PROJ-1 --from-file note.txt
atl jira issue transition PROJ-1 --to Done

# Metadata
atl jira fields
atl jira transitions --key PROJ-1
atl jira link-types
atl jira field-options --project PROJ --field <field-id>
```

---

## Conventions & exit codes

- JSON to **stdout** by default; `-o text` for human-readable output.
- Logs and errors to **stderr**.
- Request bodies via `--from-file <path>` or `--from-file -` (stdin).
- Never interactive.

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Usage / bad arguments |
| 3 | Auth failure |
| 4 | Not found |
| 5 | Version conflict (optimistic lock) |
| 6 | Forbidden |

---

## Security & self-update

The binary checks for a new release at most once every 6 hours. Each update is
verified by SHA-256 checksum **and** ed25519 signature against a public key compiled
into the binary. The update fails closed if the release is unsigned or the signature
does not match.

- Disable auto-update: `ATL_NO_UPDATE=1`
- Dev builds never auto-update.
- Full trust model: [docs/self-update.md](docs/self-update.md)
- Vulnerability policy: [SECURITY.md](SECURITY.md)

---

## Build & architecture

```sh
make build   # produces ./atl
make test    # go test ./...
make lint    # golangci-lint run
# or directly:
go build ./...
go test  ./...
```

The codebase follows a **hexagonal (ports & adapters)** architecture:

| Package | Role |
|---------|------|
| `internal/domain` | Ports (interfaces) + `Resource` model |
| `internal/adapter/confluence` | Confluence REST adapter |
| `internal/adapter/jira` | Jira REST adapter |
| `internal/csf` | Confluence Storage Format parser/serialiser |
| `internal/fragment` | Fragment registry |
| `internal/mirror` | Mirror layout, sidecar, sync |
| `internal/app` | Transport-agnostic use-cases |
| `internal/cli` | Thin Cobra command layer |

Further reading: [docs/architecture.md](docs/architecture.md) · [docs/usage.md](docs/usage.md) ·
[docs/self-update.md](docs/self-update.md)

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines, coding conventions, and how to
submit a pull request.

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).  
Third-party notices: [NOTICE](NOTICE).

---

## Trademarks & Disclaimer

This project is an **independent open-source tool** and is **NOT** affiliated with,
endorsed by, or sponsored by **Atlassian Pty Ltd** in any way.

"Atlassian", "Confluence", and "Jira" are registered trademarks of Atlassian Pty Ltd.
These names are used here solely in a **nominative, descriptive sense** — to identify
the software products with which `atl` interoperates — and not to imply any association
with or approval by Atlassian.

Use of this software is subject to the [Apache License 2.0](LICENSE). The authors
and contributors make no warranty of any kind. See [NOTICE](NOTICE) for third-party
attributions.
