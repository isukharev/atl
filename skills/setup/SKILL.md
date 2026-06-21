---
name: setup
description: Install the atl CLI and configure Confluence/Jira authentication, backend URLs, and the local mirror directory. Run this once (/atl:setup) before using atl.
disable-model-invocation: true
allowed-tools: Bash(command -v atl) Bash(atl version) Bash(atl config show) Bash(atl config set *) Bash(atl auth status) Bash(atl auth login *) Bash(atl conf search *) Bash(atl jira fields)
---

# Set up the atl CLI

Get the user from zero to ready: install the `atl` binary, point it at their Confluence/Jira,
authenticate, and agree on where the local mirror lives. Work through the steps in order; skip a
step only if its check shows it is already done.

## 1. Detect an existing install

```bash
command -v atl && atl version
```

If `atl` is found, report the version and skip to step 3. Otherwise continue.

## 2. Install the binary

Preferred (prebuilt, SHA-256 verified, installs to `~/.local/bin/atl`):

```bash
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

This is a network install that runs a shell script — Claude Code will ask the user to approve it.
That prompt is expected; do not try to bypass it.

Fallback, if the curl path fails and Go is installed (the `main` package lives in `cmd/atl`, so
the module path must end in `/cmd/atl`):

```bash
go install github.com/isukharev/atl/cmd/atl@latest
```

After installing, confirm `atl` resolves **in this shell** (a successful install does not put it on
`PATH` automatically):

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

`config show` prints `confluence_url`, `jira_url`, `update_base_url`. A non-https URL for a
non-loopback host is rejected at set time.

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

Verify (this never prints the token, only where it resolves from):

```bash
atl auth status
```

## 5. Agree on the mirror directory

`atl` mirrors pages/issues to disk. Keep the mirror **out of the user's code repository** so it is
fully greppable by the agent and never committed into their project's git history.

- **Default:** `~/.atl/<workspace>/`, where `<workspace>` is a meaningful name (the code repo's
  basename or the Confluence space key). Example: `~/.atl/payments-service/`.
- **Fix it once with `ATL_MIRROR_ROOT`** so `conf pull` / `conf status` / `jira pull` default to the
  same place without re-passing `--into` every time. Record it where later sessions will pick it up —
  either export it in the shell profile, or add a line to the project's `CLAUDE.md`:
  `atl mirror lives at ~/.atl/<workspace>/ (export ATL_MIRROR_ROOT=~/.atl/<workspace>/)`.
- An explicit `--into <dir>` still overrides `ATL_MIRROR_ROOT`. `conf push` does not read the env
  var — it finds the mirror root by walking up from the target file to the nearest `.atl`, so as long
  as you push files from inside that same root it lines up automatically.

(See the `atl` orientation skill's workflow reference for the full rationale and the in-repo /
scratch alternatives.)

## 6. Smoke test

Confirm auth + connectivity with a cheap read:

```bash
atl conf search --cql 'type = page' --limit 1   # if they use Confluence
atl jira fields                                   # if they use Jira
```

`atl` prints JSON by default. A clean result means setup is complete — tell the user they can now
ask Claude to work with Confluence pages or Jira issues (the `confluence` and `jira` skills engage
automatically).

## Exit codes (so you can react)

`2` usage · `3` auth (the server **rejected** the token → re-run step 4 with a valid PAT) ·
`4` not-found · `5` version-conflict · `6` forbidden (token lacks permission) ·
`7` not configured (backend URL or PAT **not set** yet → finish step 3/4). Anything else is `1`.
