---
name: setup
description: Install and configure atl authentication, backends, and mirror defaults. USE WHEN the user explicitly requests install, authentication, setup repair, or /atl:setup. DO NOT USE WHEN handling normal Jira, Confluence, search, reporting, or mirror work; explicit-only.
disable-model-invocation: true
allowed-tools: Bash(command -v atl) Bash(atl version) Bash(brew install *) Bash(brew upgrade *) Bash(curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh) Bash(go install *) Bash(go env *) Bash(echo *) Bash(atl config show) Bash(atl config set *) Bash(atl compatibility status *) Bash(atl compatibility pin *) Bash(atl compatibility clear *) Bash(atl auth status) Bash(atl auth login *) Bash(atl conf search *) Bash(atl conf status *) Bash(atl jira fields *) Bash(atl jira status *)
---
<!-- Generated from skills-src/setup/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Set up the atl CLI

Get the user from zero to ready: install the `atl` binary, point it at their Confluence/Jira,
authenticate, and agree on where the local mirror lives. Work through the steps in order; skip a
step only if its check shows it is already done.

If the user wants an investigation-only agent/CI profile, finish all required
URL/auth setup first, then offer `atl config set safety.read_only true` as the
last step. Explain that persistent mode intentionally blocks later `config set`
until a human edits/removes `read_only` in the owner-only global config file.

When setup commands fail, use JSON `kind` and `remediation` rather than parsing
the backend message. The schema-v1 `recovery` action is typed routing guidance,
not permission to mutate configuration; its `retry_safe` flag concerns only an
exact replay. `configuration_error` means complete local setup;
`authentication_failed` means replace/re-enter the rejected credential.
If `config.json` itself is malformed, `atl version`, help/completion, offline
profile/auth diagnostics, and local-only `conf status` / `jira status` still
work; use their evidence, then have the human repair the owner-only file. Do not
add `--remote`, attempt other online reads, or perform mutations.

## 1. Detect an existing install

```bash
command -v atl && atl version
```

If `atl` is found, report `version`, the full `commit`, and `build_state`, then
skip to step 3. `clean` identifies a supported build from an unchanged source
checkout; `dirty` means local source changes were present; `unknown` is valid
for an unstamped build whose Go VCS metadata is unavailable. These are
diagnostics, not signature or update-trust evidence. Otherwise continue.

## 2. Install the binary

Pick the method that fits the platform:

**macOS (or Linuxbrew) with Homebrew — preferred there** (handles `PATH` for you):

```bash
brew install isukharev/tap/atl
```

The Homebrew launcher sets `ATL_NO_UPDATE=1`: Homebrew is the only owner of
that installation, so later upgrades use `brew upgrade atl`. Direct
installer/release binaries retain signed self-update unless their environment
sets `ATL_NO_UPDATE`. Remote work for a due check has one five-second total
startup budget.

**Linux, or macOS without Homebrew** — prebuilt, SHA-256 verified, installs to `~/.local/bin/atl`:

```bash
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

Both the `brew` and `curl | sh` paths are network installs that Claude Code will ask the user to
approve. That prompt is expected; do not try to bypass it.

**Fallback**, if the above fail and Go is installed (the `main` package lives in `cmd/atl`, so the
module path must end in `/cmd/atl`):

```bash
go install github.com/isukharev/atl/cmd/atl@latest
```

After installing, confirm `atl` resolves **in this shell** (the `curl`/`go install` paths do not add
it to `PATH` automatically — `brew` does):

```bash
command -v atl && atl version || echo 'atl is not on PATH — add ~/.local/bin (or $(go env GOBIN)) to PATH'
```

If it is not on `PATH`, give the user the exact line to add to their shell profile (e.g.
`export PATH="$HOME/.local/bin:$PATH"`); do not edit their profile silently. **Do not continue to
step 3 until `atl version` prints a version in the current session** — otherwise every later step
fails with "command not found".

## 3. Configure backend URLs

Ask the user for their Confluence and Jira base URLs (they must be `https://`). Set whichever they
use — both is typical:

```bash
atl config set --confluence-url https://confluence.example.com --jira-url https://jira.example.com
atl config show
```

`config show` prints `confluence_url`, `jira_url`, `update_base_url`, a `mirror` hint block
with the recommended `~/.atl/<workspace>/` root plus active `ATL_MIRROR_ROOT` when set, and the
effective `render` block with `render_provenance` (see below). Its shared
`render.display_time_zone` is an IANA presentation zone (deterministic `UTC` by default); it
changes human Markdown dates only, never JQL/CQL or exact JSON/native timestamps. A non-https URL for a non-loopback
host is rejected at set time.

**Render config layer (presentation-only).** `config set` also takes a positional dotted render key
that tunes the derived `.md` view — `render.{jira,confluence}.{profile,include,exclude}` (profile is
`minimal`|`default`|`full`) plus Jira-only `custom_fields` (comma-separated), typed
`field_views` (JSON descriptor array), and `epic_field`; Confluence has a
closed read-only `page_fields` JSON descriptor array:

```bash
atl config set render.display_time_zone Europe/Berlin # global human-readable Markdown zone
atl config set render.jira.profile full            # global (~/.config/atl/config.json)
atl config set --local render.confluence.profile minimal   # per-mirror <root>/.atl/config.json
atl config set --local render.confluence.page_fields '[{"id":"title"},{"id":"updated","format":"date"}]'
atl config set render.confluence.jira_macros off # global-only: never execute page JQL
atl config set --local render.jira.field_views '[{"id":"customfield_10003","label":"Risk Notes","placement":"section","format":"jira_wiki","editable":true}]'
```

`--local` writes a per-mirror `.atl/config.json` (nearest `.atl` walking up from cwd, or `--into ROOT`).
It is a **security boundary**: a local file may carry render keys only — backend/update URLs are
global/env-only so a shared or checked-out mirror can never redirect where the PAT is sent. `config
set --local` refuses a URL flag (exit 2); at read time any forbidden/unknown key in a local file is
warned about on stderr and ignored. Precedence is local > global > default. Pull/render consume the
effective settings and record the resolved view in `.atl/state.json` for apply affinity.

## 4. Authenticate

`atl` uses a per-service Personal Access Token (PAT). **Never put a PAT on the command line** —
`auth login` reads it from a no-echo prompt, piped stdin, or a file. Recommend a least-privilege,
task-scoped token.

Interactive (the agent runs this; the user types the token at the hidden prompt):

```bash
atl auth login --service confluence
atl auth login --service jira
```

Or from a file: `atl auth login --service jira --from-file ./token.txt` (then delete the file).
Or via environment for CI/agent sessions: `ATL_CONFLUENCE_PAT` / `ATL_JIRA_PAT`.

For rare direct REST fallback calls that `atl` does not cover yet, keep the PAT out of argv/logs:
use env vars, turn off shell tracing, and feed curl config/headers through stdin instead of
`curl -H "Authorization: Bearer $TOKEN"`.

Verify (this never prints the token, only where it resolves from):

```bash
atl auth status
atl doctor
```

`atl doctor` is the preferred share-safe setup report. It is offline by default
and emits no configured URL/hostname, path, identity, token, or mirror content.
If it exits `8`, inspect its emitted `problems[]`; do not paste `config show`
into public chat or an issue.

**Optional exact compatibility provider.** Do not enable one during ordinary
setup. When the owner explicitly needs a provider-specific Data Center
workflow, inspect `atl compatibility status`, review the exact owner-pinned
version/build, then run `compatibility pin` and `compatibility status --remote`.
A nearby patch is unsupported; never substitute a version range or arbitrary
REST fallback. `pin` and `clear` are persistent local mutations and require
explicit write authority.

## 5. Agree on the mirror directory

`atl` mirrors pages/issues to disk. Keep the mirror **out of the user's code repository** so it is
fully greppable by the agent and never committed into their project's git history.

- **Recommended convention:** `~/.atl/<workspace>/`, where `<workspace>` is a
  meaningful name (the code repo's basename or the Confluence space key).
  Example: `~/.atl/payments-service/`. This is not the pull/write fallback:
  without `ATL_MIRROR_ROOT` or `--into`, Confluence pull uses `mirror` and Jira
  pull uses `mirror-jira`; status/snapshot first detect a nearest initialized
  `.atl` as described below.
- **Fix it once with `ATL_MIRROR_ROOT`** so Confluence/Jira pull plus
  status/snapshot inspection default to the same place without re-passing the
  root every time. Status/snapshot also accept positional `[DIR]` or `--into`
  (never both), then try the nearest initialized `.atl` before the service
  fallback. Record the environment setting where later sessions will pick it up —
  either export it in the shell profile, or add a line to the project's `CLAUDE.md`:
  `atl mirror lives at ~/.atl/<workspace>/ (export ATL_MIRROR_ROOT=~/.atl/<workspace>/)`.
- An explicit `--into <dir>` still overrides `ATL_MIRROR_ROOT`. Inspection
  requires an initialized `.atl` root and returns exit 4 before config/network
  when it is absent. `conf push` does not read the env
  var — it finds the mirror root by walking up from the target file to the nearest `.atl`, so as long
  as you push files from inside that same root it lines up automatically.

(See the `atl` orientation skill's workflow reference for the full rationale and the in-repo /
scratch alternatives.)

## 6. Smoke test

Confirm auth + connectivity with a cheap read:

```bash
atl doctor --remote
atl conf search --cql 'type = page' --limit 1   # if they use Confluence
atl jira fields --summary-only                   # if they use Jira
```

`doctor --remote` makes one single-attempt product/version GET per ready
service. Only when the Confluence version route returns `404` may it add one
bodyless reachability HEAD; that proves REST availability but leaves
compatibility unverified. It performs no search, page/issue body read, identity
read, or write. The following bounded service read proves a useful
permission/data route.
`atl` prints JSON by default. A healthy doctor result plus a clean, complete
service result means technical setup is complete. Offer one concrete next route
instead of loading the full command reference: a bounded focused read, a pull
into the agreed mirror followed by local status/diff, or a non-writing preview
for a user-selected change. Use the `confluence` or `jira` skill for that route.
Do not perform a write merely to prove setup.

Offer the separate explicit `onboarding` skill if they want atl to learn their
recurring workflow, approved field/schema facts, render defaults, and common
selectors. Do not run that interview or inspect sample content unless they opt
in; technical setup remains complete without it.

The installed Claude Code/Codex plugin also bundles `atl mcp serve`, a typed
read-only evidence surface. Remote tools use the same configured host-scoped
credentials, while the two offline mirror snapshot tools use only an explicit
`ATL_MIRROR_ROOT`; neither configuration is copied into plugin files. The
surface becomes available after starting a new agent session with `atl` on
`PATH`. Its absence does not make CLI setup incomplete; use the CLI fallback and
report a plugin/binary version skew instead of inventing raw REST calls.
Standalone clients that need only one reviewed boundary may launch
`atl mcp serve --service jira`, `--service confluence`, or
`--service offline`. The default plugin launch remains the full read-only
inventory; do not turn the profile flag into a model-selected arbitrary
allowlist.

## Version skew

Plugin and binary release under one version number. If a command documented by the skills is
rejected as unknown (exit 2), compare `atl version` with the plugin version and update the lagging
side: a direct-install binary self-updates on its next run, while a Homebrew binary
uses `brew upgrade atl`. Refresh the lagging plugin with the platform-specific flow:

Use Claude Code's `/plugin update atl` command.

## Exit codes (so you can react)

`2` usage · `3` auth (the server **rejected** the token → re-run step 4 with a valid PAT) ·
`4` not-found · `5` version-conflict · `6` forbidden (token lacks permission) ·
`7` not configured (backend URL or PAT **not set** yet → finish step 3/4). Anything else is `1`.
