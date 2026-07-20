**English** · [Русский](README.ru.md)

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=CI)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Roadmap](ROADMAP.md) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)

**A Git-style CLI for Confluence & Jira — built for coding agents.**

`atl` lets a coding agent (e.g. Claude Code or Codex) interact with Confluence and Jira the same way
it interacts with code: mirror documents to disk, search with `ripgrep`, edit the
**native storage format** (Confluence Storage Format, `.csf`), reason in diffs, and push
under an **optimistic version gate** that refuses to silently overwrite concurrent edits.

For investigation-only sessions, set `ATL_READ_ONLY=1` (or pass global
`--read-only`). Mutating commands are rejected before credentials, body files,
self-update, or network access; reads, pulls, status, and exports remain usable.
Persist the guard with `atl config set safety.read_only true`.
Help and shell completion remain available in read-only mode.
Confluence durable-view marker checks accept either LF or CRLF line endings.

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
- **Jira integration** — query, comment, transition issues; mirror an issue set to disk as native
  `.wiki` + rendered `.md`, then edit Description or opt-in rich-text field sections in the `.md`
  view and stage them with `jira apply` (or edit the
  `.wiki` directly), and push with `jira status` / `jira push` (dry-run by default; a drift guard
  refuses stale writes since Jira has no server-side version gate).
- **Bearer PAT auth, per-request** — tokens are sent only to the configured host and never
  stored in the repo or mirror.
- **Signed self-update** — the binary updates itself from GitHub Releases, throttled to every
  6 hours, with SHA-256 checksum and ed25519 signature verification. Homebrew
  installations delegate upgrades exclusively to `brew upgrade atl`. See
  [docs/self-update.md](docs/self-update.md), [network egress](docs/network-egress.md),
  and [SECURITY.md](SECURITY.md).
- **Scripting-friendly** — JSON to stdout, logs/errors to stderr, no interactive prompts,
  well-defined exit codes.
- **Typed read-only MCP** — `atl mcp serve` gives agents nine bounded Jira/Confluence
  evidence tools with no write, shell, raw REST, or arbitrary-file surface.
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
> tap is not yet available, use the quick install or `go install` above. Its launcher disables
> binary self-update so Homebrew remains the sole upgrade owner; use `brew upgrade atl`.

**Requirements:** Linux or macOS (amd64/arm64). Building from source needs Go 1.26.5+; the prebuilt
binary has no runtime dependencies.

---

## Quick start

From zero to your first result with the CLI directly (for agent plugins, see the next section):

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

## Use with coding agents

`atl` ships installable agent workflows for Claude Code and Codex, so an agent can install the CLI
and drive it for you.

### Claude Code

This repository is also a [Claude Code](https://claude.com/claude-code) plugin marketplace. Add the
marketplace and install the plugin:

```
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

`/atl:setup` installs the `atl` binary if it is missing, configures your Confluence/Jira auth and
backend URLs, and agrees on a local mirror directory. After that, Claude Code automatically uses the
shared skills listed below when relevant. Plugin versions track CLI releases — enable
auto-update for the atl marketplace (`/plugin` → Marketplaces → Enable auto-update; off by default
for third-party marketplaces) so each release updates the skills together with the self-updating
binary. The plugin also bundles the same typed read-only MCP server described
under Codex below; restart Claude Code after the binary is configured.

### Codex

This repository also includes Codex plugin metadata and a repo-local marketplace. Add the marketplace
and install the same workflow bundle:

```sh
codex plugin marketplace add isukharev/atl
codex plugin add atl@atl
```

Then start a new Codex session, invoke the `setup` skill from `/skills` or with `$setup`, and let it
install/configure the `atl` CLI. Optionally invoke `$onboarding` afterward to build a reviewed,
private workflow profile from explicitly approved examples. After setup, Codex can invoke the same
shared skills when relevant. The plugin also starts the binary's typed read-only MCP surface;
begin a new session after installing/configuring `atl`. See [docs/mcp.md](docs/mcp.md)
for its nine tools, bounds, and standalone setup.

Shipped skill metadata declares mutually exclusive intended discovery
boundaries. Focused workflows, cross-service discovery, direct
Jira/Confluence work, and `atl` mirror or orientation tasks each have one
declared route. Setup and onboarding are explicit-only; codebase-only mentions
are declared no-activation cases. The offline corpus checks this policy, while
actual provider selection is measured separately with model-in-the-loop runs.

Core skills:

- **`atl`** — orientation and genuinely mixed local-mirror work: when to use
  `atl` (vs a live Atlassian MCP), the search-first workflow, and where the
  mirror lives.
- **`confluence`** — pull, edit `.csf`, validate, and push pages under the version gate.
- **`jira`** — search/pull issues, discover exact Structure folders, and inspect normalized Structure and Kanban/Scrum board views,
  and create/update/transition/comment/link via guarded commands.
- **`onboarding`** — optional consent-gated workflow discovery, declared team defaults, and a
  reviewed private profile; later observations become deterministic review/apply/reject
  suggestions, never silent mutations, stale schema facts are revalidated explicitly, and saved
  render/mirror preferences are synchronized to runtime only after separate approval.

For an unfamiliar task, agents can avoid broad help/reference loading with the
versioned offline router:

```sh
atl capabilities --task jira/evidence
atl capabilities --task confluence/edit -o text
atl capabilities --task jira/board-portfolio -o text
atl capabilities --task jira/batch-analysis -o text
atl capabilities --task jira/structure-planning -o text
atl capabilities --task confluence/table-analytics -o text
atl capabilities --task knowledge/search -o text
```

It returns only a small ordered set of real command paths plus their access,
output, completeness, and focused skill-reference contracts. It loads no
config/credentials and makes no network request.
The `jira/structure-planning` route distinguishes hierarchy discovery,
explicit Structure row-value projection, and transient issue export; the value
projection is read-only even though Tempo transports its query payload by POST.

For transient Jira/Confluence evidence, the installed plugins can call the same
application services through typed MCP tools instead of constructing shell
commands. Use the CLI for durable mirrors, Structure, exports, diff/plan, and
all guarded writes; the MCP v1 surface is remote-read-only by construction.

On top of those references, the plugin ships workflow recipes — end-to-end processes with
built-in approval gates before anything is created:

- **`search-knowledge`** — answer questions from Confluence + Jira with cited sources.
- **`triage-issue`** — duplicate/regression search before filing a structured bug.
- **`status-report`** — Jira-derived status report, optionally published to Confluence.
- **`spec-to-backlog`** — turn a Confluence spec into an Epic plus linked tickets.
- **`sprint-dashboard`** — a read-only visual snapshot of the current sprint.
- **`meeting-tasks`** — action items from meeting notes into assigned Jira tasks.

Both platforms ship the same skills, generated from the single source in
[`skills-src/`](skills-src/) (platform pipeline: [docs/plugins.md](docs/plugins.md)). Claude Code
packaging lives in [`.claude-plugin/`](.claude-plugin/); Codex packaging lives in
[`plugins/atl`](plugins/atl) with the repo marketplace at
[`.agents/plugins/marketplace.json`](.agents/plugins/marketplace.json).

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

# Optional: one explicit, bounded GET-only time-semantics diagnostic.
# It runs no JQL/CQL/search/page read and exposes no URL or user identity.
export ATL_READ_ONLY=1
atl environment inspect
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

# Historical bootstrap beyond the ordinary selector caps. Selection is
# verified twice before body reads; repeat the exact command to resume.
atl conf pull --complete --cql 'space=DOCS and type=page' --into mirror

# Optional reviewed load bounds: writes/checkpoints stay serial, while native
# body reads prefetch in order and all Confluence/Jira attempts share 8 starts/s.
atl conf pull --complete --cql 'space=DOCS and type=page' \
  --page-prefetch 4 --requests-per-second 8 --into mirror

# Repeat a large selector without re-fetching unchanged pages. The first run
# needs one reviewed absolute instant. A 48-hour safety overlap prevents an
# unknown backend CQL timezone from omitting pages; no calibration search runs.
atl conf pull --incremental --cql 'space=DOCS and type=page' \
  --since '2026-07-01T00:00:00+02:00' --into mirror
# Later runs reuse the selector-bound UTC watermark; omit --since.
atl conf pull --incremental --cql 'space=DOCS and type=page' --into mirror
```

Incremental preflight automatically migrates a supported legacy `.md` view only
when its bytes exactly match the reconstructed pristine legacy format; edited or
unknown-format views remain blocked before page-body reads.

`atl environment inspect` is the compact way to see these concerns separately:
Jira's observed server offset and current-user timezone, the JQL timezone
assumption, Confluence's observed user field when available, an honest
`unknown` CQL parser timezone, the configured/default Markdown display zone,
and the fixed 48-hour incremental overlap. It is user-invoked only and performs
at most three sequential metadata GETs; incremental pull never runs it or any
calibration probe automatically.

Large pulls remain serial by default. `--page-prefetch` (1–8) is an explicit,
bounded native-body read window; `--requests-per-second` paces every actual
Confluence and optional Jira-macro request through the same scheduler and
shares server `Retry-After`. Path claims, assets/sidecars, mirror writes, and
checkpoints are always committed sequentially in canonical order.

### 2. Explore the mirror

```
mirror/
  DOCS/                         # space key
    acme-adr/
      acme-adr.csf              # source of truth (native storage format)
      acme-adr.md               # derived staging view; supported edits go through conf apply
      acme-adr.meta.json        # id, version, content hash, resolved fragments, comment_count
      acme-adr.comments.json    # [{id,author,created,body,body_storage?}] (with --comments)
      acme-adr.comments.md      # derived human read view (with --comments)
      acme-adr.assets/*.png     # draw.io renders + page images (with --assets)
      child-page/…              # folder tree mirrors the page hierarchy
  .atl/                         # sidecar: last-synced state + pristine base
    incremental.json            # selector-bound completed refresh watermarks
    complete-pulls/*.json       # private resumable exact-id snapshots
  .gitignore
```

Use any text tool to search across the mirror:

```sh
rg "decision" mirror/
```

### 3. Edit, validate & push

```sh
# Easiest: ensure the v4 document marker, edit the markdown view, then merge it into .csf.
# Untouched blocks keep their exact bytes; unconvertible edits fail closed.
$EDITOR mirror/DOCS/acme-adr/acme-adr.md
atl conf apply mirror/DOCS/acme-adr/acme-adr.md --dry-run
atl conf apply mirror/DOCS/acme-adr/acme-adr.md

# Or edit the native storage format directly
$EDITOR mirror/DOCS/acme-adr/acme-adr.csf

# Validate before pushing (blocks on malformed XML, warns on sanity issues)
atl conf validate mirror/DOCS/acme-adr/acme-adr.csf

# Inspect native baseline → candidate changes offline (JSON by default)
ATL_READ_ONLY=1 atl conf diff mirror/DOCS/acme-adr/acme-adr.csf -o text
# Text uses root-relative paths and explicit semantic/byte-only/none review labels;
# use default JSON only when block/hash/validation evidence is needed.
# baseline_mismatch means tracked sync evidence is corrupt: preserve edits and re-pull

# Dry-run to see what the remote write will do
atl conf push mirror/DOCS/acme-adr/acme-adr.csf --dry-run

# Push (exits 5 if the remote has drifted; re-pull + reapply to recover)
atl conf push mirror/DOCS/acme-adr/acme-adr.csf

# Check sync status
atl conf status mirror --remote

# For several edited pages: freeze the exact reviewed set, preview, then apply.
atl conf plan create mirror/DOCS/ --out .atl-private/docs-plan.json
ATL_READ_ONLY=1 atl conf plan preview .atl-private/docs-plan.json
atl conf plan apply .atl-private/docs-plan.json \
  --expected-proposal-hash <HASH-FROM-PREVIEW> --confirm APPLY
```

### Other Confluence commands

```sh
atl conf search --cql 'space=DOCS and label="adr"'
# JSON qualifies this bounded result with complete/truncated/partial_reason;
# -o text is a Markdown candidate table with the same completeness signal.
atl conf space tree --space DOCS
# Page ids come from atl conf pull output (meta.json → "id" field) or the page URL.
atl conf page view 123456 -o text   # configured Markdown, no mirror artifacts
atl conf page view 123456 --jira-view full -o text # readonly Jira-macro tables
atl conf page view 123456 --jira-macros off -o text # placeholders only; no Jira credentials/search
atl conf page get     --id 123456
atl conf page get     --id 123456 --format csf
atl conf page meta    --id 123456  # omitted restricted = unknown, not false
atl conf page history --id 123456
# Guarded title update: title stays in a bounded file/stdin, not argv
atl conf page title set 123456 --from-file title.txt
# Re-run with --apply, --expected-version, and --expected-proposal-hash from that preview
atl conf page labels list 123456
atl conf page labels add 123456 reviewed-label # dry-run; apply with emitted proposal hash
# Typed read-only page metadata (closed field ids; see docs/usage.md)
atl config set render.display_time_zone Europe/Berlin # Markdown only; JSON/native timestamps stay exact
atl config set render.confluence.include page_fields
atl config set render.confluence.page_fields '[{"id":"title"},{"id":"updated","format":"date"}]'
atl config set render.confluence.jira_macros off # default auto; disable page-provided JQL globally
# View v4 separates # Metadata / # Content / generated Jira queries / # Comments; native
# comment formatting and page-link target identity remain readable.
atl conf table extract --id 123456 --format json
atl conf table extract --id 123456 --table 2 --format csv
atl conf table extract --id 123456 --table 2 --format csv --raw-csv # unsafe in spreadsheets
atl conf table extract --id 123456 --format xlsx --out tables.xlsx
atl conf page create  --space DOCS --parent 123456 --title "My Page" --from-file body.csf
atl conf page create  --space DOCS --title "From markdown" --from-md body.md
atl conf blog create  --space DOCS --title "Weekly update" --from-md update.md
# An unverifiable or identity-normalized create result is unknown; never replay it.
# Guarded move: preview first, then apply with the emitted source-state gates
atl conf page move    123456 --parent 654321
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
| `atl conf plan create/preview/apply` | Freezes a multi-page set; read-only complete preview, exact hash + confirmation, no force/replay |
| `atl conf push --dry-run` | Reports all consequences without writing anything |
| Version gate | `push` exits **5** when the remote version has advanced since the last pull |
| `--force` | Overrides the version gate; the safe recovery path is re-pull + reapply |
| Diagrams | draw.io macros resolved to PNGs of the exact revision for visual inspection |

---

## Jira

```sh
# Read
atl jira issue get  PROJ-1
atl jira issue fields PROJ-1 # compact non-empty named fields by default
atl jira issue fields PROJ-1 --metadata-only # lower-token field inventory, no values
atl jira issue fields PROJ-1 --field "Delivery Notes"
atl jira issue field get PROJ-1 --field "Delivery Notes" --max-bytes 16384 # bounded exact expansion
atl jira issue history PROJ-1 --field "Delivery Notes" --since 2026-04-01
atl jira issue refs PROJ-1 --fields "Delivery Notes" # qualified artifact links
# Calendar dates/quarters use one observed Jira current-user timezone lookup;
# RFC3339 with an explicit offset stays exact and skips that lookup.
# Midnight gaps/folds cover the whole real civil day; a skipped date fails closed.
# --keys/--ids preserve de-duplicated selector order; missing identities are omitted
atl jira export --keys PROJ-1,PROJ-2 --fields "Delivery Notes" --out - | jq -s '.'
atl conf page resolve 'https://confluence.example.test/spaces/ENG/pages/42/Page'
atl conf page outline 42 && atl conf page section 42 --heading 'Delivery Notes' -o text
atl jira epic digest PROJ-1 --quarter 2026-Q2 --status-field 'Delivery Notes' --projection compact
atl jira issue view PROJ-1 -o text   # configured Markdown, no files written
atl jira issue search --jql 'project = PROJ AND status = "In Progress"' --columns key,summary,status,assignee
atl jira issue search --jql 'project = PROJ' --view full
atl jira issue children PROJ-100 --columns key,summary,status,assignee
atl capabilities --task jira/portfolio            # bounded quarter/plan route
atl jira board view 5 -o text                  # normalized Kanban/Scrum view
atl jira sprint current --board 5              # resolve the active sprint
atl jira structure folders 123                 # discover exact stored subtrees
atl jira structure view 123 --folder-id 100 -o text
atl jira planning report --jql 'project = PROJ' --limit 100
atl jira quality-report --jql 'project = PROJ' # compatibility alias
atl jira issue link suggest --csv links.csv     # read-only candidate analysis
atl profile show --section render_defaults --service jira
atl jira issue worklog list PROJ-1 -o text
atl jira issue attachment list PROJ-1
atl jira issue attachment get PROJ-1 --id spec.xlsx --into ./attachments

# Mirror an issue set to disk (add --assets to also mirror image attachments)
atl jira pull --jql 'project = PROJ' --into mirror-jira
atl jira pull --jql 'project = PROJ AND status = Open' --assets
# Choose how much the .md view shows: minimal | default | full (see docs/usage.md)
atl jira pull --jql 'project = PROJ' --render-profile full
atl jira render mirror-jira --render-profile default   # re-render offline, no re-pull
atl manifest create --root mirror-jira --service jira --selector 'jql=project = PROJ'
# Pull/render refuse unknown future .md formats; render warns for every unreadable snapshot.
# Typed custom fields (readable metadata/date/list rendering) and identity-checked epic children
# are configured per mirror; see docs/usage.md

# Write
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
atl jira issue assign PROJ-1 --me
atl jira issue watchers list PROJ-1
atl jira issue watchers add PROJ-1 --me # dry-run; apply with emitted proposal hash
atl jira issue worklog add PROJ-1 --time 1h30m --from-file worklog.txt # dry-run
# Review baseline_sha256; apply once with the emitted baseline-bound proposal hash
atl jira issue comment add PROJ-1 --from-md note.md
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'
ATL_READ_ONLY=1 atl jira issue field preview PROJ-1 --from-md customfield_10001=notes.md --allow-fields customfield_10001
# Review expected_updated and proposal_hash; use field set --apply only after approval.
atl jira issue transition PROJ-1 --to Done
# Before editing, re-render views without the current first-line version marker
atl jira render mirror-jira
# Edit supported generated sections, stage them, then push (block-level, non-lossy)
atl jira apply mirror-jira/PROJ/PROJ-1.md --dry-run

# Metadata
atl jira fields # value-free catalog with explicit complete/total/count
atl jira transitions --key PROJ-1
atl jira link-types
atl jira field-options --project PROJ --field <field-id>
```

---

## Conventions & exit codes

- JSON to **stdout** by default; `-o text` for commands with an explicit
  human-readable projection (unsupported requests fail with exit 2, never JSON).
- Logs and errors to **stderr** — on failure, `{"error": "...", "code": N}` JSON by default
  (or a plain `error: <msg>` line under `-o text`).
- Request bodies via `--from-file <path>` or `--from-file -` (stdin, capped at 64 MiB;
  larger input is rejected, not truncated).
- Never interactive.
- Confluence pull/render/apply/push and mirror-local `conf edit` serialize per mirror; on lock contention,
  wait for the active operation and never delete the persistent `.atl` lock.
- A Confluence re-pull that changes a tracked page path refuses local edits or
  collisions, records the new path, and retires only that page's old primary
  files; descendant/unrelated directories are never recursively deleted. If
  all three old primary files were deliberately removed, pull repairs the stale
  path record; a partial removal remains an exit-8 reconciliation error.
- Jira and Confluence updates to a shared mirror's `state.json` merge under one
  backend-neutral state lock; brief contention is retried within a fixed bound,
  then fails closed instead of losing entries.

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Usage / bad arguments (incl. an insecure non-https URL) |
| 3 | Auth failure — a PAT **was** supplied but the server rejected it |
| 4 | Not found |
| 5 | Version conflict (optimistic lock) |
| 6 | Forbidden (token lacks permission) |
| 7 | Invalid/incomplete configuration — for example a missing URL/PAT or invalid named view |
| 8 | Safety/check refusal — incomplete evidence, stale/drifted state, invalid derived view, active lock, or required-field failure |

`7` vs `3`: `7` means "finish setup" (no URL/token); `3` means "replace the token" (it was refused).
JSON errors also include stable `kind` and `remediation` fields derived from
local error types, so agents need not parse backend prose; existing `error` and
`code` remain unchanged.
For scripting and CI patterns (env-only config, disabling self-update, isolating credentials,
handling the `--cql` page cap), see [docs/usage.md → Scripting & CI](docs/usage.md#scripting--ci).

---

## Troubleshooting

| Symptom | Likely cause & fix |
|---------|--------------------|
| `command not found: atl` after install | `~/.local/bin` (or `$(go env GOBIN)`) is not on `PATH` — add it to your shell profile and reopen the shell. |
| Exit **7** / "URL not set" / "no PAT found" | Setup is incomplete — run `atl config set --confluence-url …` and `atl auth login --service …` (or set `ATL_*_URL` / `ATL_*_PAT`). |
| Exit **7** mentions `jira_list_views` | Run `atl config show`, inspect `jira_list_views_error`, then replace or remove invalid presets with `atl config set jira.list_views.<name> …`; if several entries are invalid, delete them one at a time. Runtime reads stay blocked until the complete catalog is valid. |
| Malformed `config.json` blocks normal commands | `atl version`, `atl help`, completion, and offline profile/auth diagnostics remain available. Repair the owner-only file; online reads and all mutations stay blocked. |
| Exit **3** on every call | The PAT was refused (expired/revoked, or it belongs to a different instance) — create a fresh token and re-`auth login`. |
| "refusing to send the PAT over http…" | The backend URL is non-https on a non-loopback host. Use `https`, or `export ATL_ALLOW_INSECURE=1` for an internal http instance you trust. |
| Exit **5** on push | The remote page moved since your last pull (expected) — re-pull, reapply your edit, and push again; `--force` only after a human decides. |
| A `--cql` pull seems to miss pages | Ordinary mode caps at 1000 (`"truncated": true` + a stderr `warning:`). Narrow the CQL, or use explicit resumable `--complete` when a full historical mirror is required. |
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
- Homebrew installs update only through `brew upgrade atl`.
- Dev builds never auto-update.
- Full trust model: [docs/self-update.md](docs/self-update.md)
- Network destinations and air-gap recipe: [docs/network-egress.md](docs/network-egress.md)
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

Supported `make build`/`make install`/release builds stamp the full source
commit and whether the checkout was `clean` or `dirty`. Inspect that identity
with `atl version`; direct, unstamped builds fall back to Go VCS metadata and
use `unknown` when provenance is unavailable. No build timestamp is embedded,
so identical inputs remain reproducible.

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
[agent recipes](docs/agent-recipes.md) · [docs/self-update.md](docs/self-update.md) ·
[network egress](docs/network-egress.md) ·
[agent benchmarking](docs/agent-benchmarking.md) ·
[Context7 integration](docs/context7.md)

Context7 library `/isukharev/atl` follows the latest published release through
the `stable` branch. Development documentation on `main` may describe behavior
that has not shipped yet; query a `/vX.Y.Z` library id for an exact release.

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
