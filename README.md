**English** · [Русский](README.ru.md)

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=CI)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Roadmap](ROADMAP.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)

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

### Homebrew

```sh
brew install isukharev/tap/atl
```

> The formula (`atl.rb`, pinned to each binary's SHA-256) is published with every release. If the
> tap is not yet available, use the quick install or `go install` above.

**Requirements:** Linux or macOS (amd64/arm64). Building from source needs Go 1.26+; the prebuilt
binary has no runtime dependencies.

---

## Quick start

From zero to your first result with the CLI directly (for Claude Code, see the next section):

```sh
# 1. Install (Linux/macOS) — then add ~/.local/bin to PATH if the installer asks
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh

# 2. Point atl at your instance(s) — Server/Data Center, https required
atl config set --confluence-url https://confluence.example.com \
               --jira-url       https://jira.example.com

# 3. Add a Personal Access Token (no-echo prompt; never on argv)
atl auth login --service confluence

# 4. Verify, then run a cheap read
atl auth status
atl conf search --cql 'type = page' --limit 1
```

A clean JSON result from step 4 means you're ready. If a command exits **7**, the URL or PAT is not
configured yet (finish steps 2–3); **3** means a PAT was supplied but the server rejected it.
Automating this in CI? See [docs/usage.md → Scripting & CI](docs/usage.md#scripting--ci).

---

## Use with Claude Code

`atl` ships a [Claude Code](https://claude.com/claude-code) plugin (this repo is also a plugin
marketplace), so an agent can install the CLI and drive it for you. Add the marketplace and install
the plugin:

```
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

`/atl:setup` installs the `atl` binary if it is missing, configures your Confluence/Jira auth and
backend URLs, and agrees on a local mirror directory. After that, Claude Code automatically uses the
bundled skills when relevant:

- **`atl`** — orientation: when to use `atl` (vs a live Atlassian MCP), the search-first workflow,
  and where the mirror lives.
- **`confluence`** — pull, edit `.csf`, validate, and push pages under the version gate.
- **`jira`** — search/pull issues and create/update/transition/comment/link via commands.

The skills are bundled under [`skills/`](skills/) and defined by
[`.claude-plugin/`](.claude-plugin/); you can also try them locally with
`claude plugin validate .`.

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

> **Server / Data Center only.** `atl` authenticates with a **bearer Personal Access Token**, which
> is the Confluence/Jira **Server & Data Center** token model. Atlassian **Cloud** (`*.atlassian.net`)
> uses email + API-token Basic auth and is **not** supported.
>
> - **Base URL** — what you type in the browser to reach the instance, e.g.
>   `https://confluence.example.com` (no `/wiki`, `/display/…`, or page path). Must be `https`
>   (an internal http-only host needs `ATL_ALLOW_INSECURE=1`).
> - **PAT** — create one in the web UI: your profile → **Personal Access Tokens** → *Create token*.
>   Use a least-privilege, task-scoped token; Confluence and Jira each need their own.

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

### 3. Edit, validate & push

```sh
# Easiest: edit the markdown view, then merge it into the .csf block-by-block.
# Untouched blocks keep their exact bytes; unconvertible edits fail closed.
$EDITOR mirror/DOCS/acme-adr/acme-adr.md
atl conf apply mirror/DOCS/acme-adr/acme-adr.md

# Or edit the native storage format directly
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
atl conf table extract --id 123456 --format json
atl conf table extract --id 123456 --table 2 --format csv
atl conf table extract --id 123456 --format xlsx --out tables.xlsx
atl conf page create  --space DOCS --parent 123456 --title "My Page" --from-file body.csf
atl conf page create  --space DOCS --title "From markdown" --from-md body.md
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
atl jira issue assign PROJ-1 --me
atl jira issue comment add PROJ-1 --from-md note.md
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
- Logs and errors to **stderr** — on failure, `{"error": "...", "code": N}` JSON by default
  (or a plain `error: <msg>` line under `-o text`).
- Request bodies via `--from-file <path>` or `--from-file -` (stdin, capped at 64 MiB;
  larger input is rejected, not truncated).
- Never interactive.

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Usage / bad arguments (incl. an insecure non-https URL) |
| 3 | Auth failure — a PAT **was** supplied but the server rejected it |
| 4 | Not found |
| 5 | Version conflict (optimistic lock) |
| 6 | Forbidden (token lacks permission) |
| 7 | Not configured — backend URL or PAT **not set** yet |
| 8 | Check failed — `jira issue check` found empty required fields |

`7` vs `3`: `7` means "finish setup" (no URL/token); `3` means "replace the token" (it was refused).
For scripting and CI patterns (env-only config, disabling self-update, isolating credentials,
handling the `--cql` page cap), see [docs/usage.md → Scripting & CI](docs/usage.md#scripting--ci).

---

## Troubleshooting

| Symptom | Likely cause & fix |
|---------|--------------------|
| `command not found: atl` after install | `~/.local/bin` (or `$(go env GOBIN)`) is not on `PATH` — add it to your shell profile and reopen the shell. |
| Exit **7** / "URL not set" / "no PAT found" | Setup is incomplete — run `atl config set --confluence-url …` and `atl auth login --service …` (or set `ATL_*_URL` / `ATL_*_PAT`). |
| Exit **3** on every call | The PAT was refused (expired/revoked, or it belongs to a different instance) — create a fresh token and re-`auth login`. |
| "refusing to send the PAT over http…" | The backend URL is non-https on a non-loopback host. Use `https`, or `export ATL_ALLOW_INSECURE=1` for an internal http instance you trust. |
| Exit **5** on push | The remote page moved since your last pull (expected) — re-pull, reapply your edit, and push again; `--force` only after a human decides. |
| A `--cql` pull seems to miss pages | It caps at 1000 (`"truncated": true` + a stderr `warning:`). Narrow the CQL or pull by `--space`. |
| Direct REST debugging needs a PAT | Keep the token out of argv/logs; use env vars and feed curl headers via stdin (see `docs/usage.md`). |
| Structure API says the forest spec/body is missing | Check that the request body is sent exactly as a file or stdin payload; avoid shell expansions that turn it into an empty body. |
| Cloud (`*.atlassian.net`) won't authenticate | Not supported — `atl` uses Server/Data Center bearer PATs, not Cloud API tokens. |

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
