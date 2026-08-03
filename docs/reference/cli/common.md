# Common CLI conventions

Pagination, output modes, body input, environment behavior, scripting, version output, and other service-independent CLI rules.

[Reference index](README.md) · [Documentation home](../../README.md)

## Global conventions

The root flags apply before every command:

| flag | description |
|---|---|
| `-o`, `--output` | select the command's supported `json`, `text`, or `id` output contract |
| `--verbose` | trace HTTP requests and responses to stderr without logging bearer tokens; any non-empty `ATL_VERBOSE` enables the same trace |
| `--read-only` | block every mutation before credentials, stdin, self-update, or network access; see [Global read-only policy](configuration.md#global-read-only-policy) |

Verbose diagnostics never change stdout: the selected command result remains
the only stdout payload.

## Pagination limits

One-page reads require a positive explicit `--limit`: `conf search` and
`conf page list` accept `1..100`; Jira issue search/children/user search accept
`1..1000`; board list/issues/backlog and sprint list/issues accept `1..50`.
Omission keeps the documented positive default. Explicit zero, a negative
value, or a value above that command's cap is a usage error (exit 2) before
configuration or network access.

Aggregate Jira reads use a different, explicit contract: `--limit 0` means
paginate to exhaustion subject to their existing safety caps, a positive value
is the documented aggregate cap, and a negative value is a usage error before
network or filesystem effects. This applies to issue refs/tree, pull, export,
planning/quality reports, board view/export (per requested scope), and
Structure pull-issues.

## Output format

By default every command writes JSON to stdout. Pass `-o text` (or
`--output text`) for human-readable output on the same commands that support
it. Text support is an explicit per-command contract: an unsupported request
returns a usage error (exit 2) before config, stdin, or network access and never
falls back to JSON.

`-o id` is also an explicit per-command contract. It prints only primary
identifiers, one per line. Unsupported id output now fails at the same root
preflight, before config, stdin, self-update, or network access.

Every public command-tree node is registered explicitly. A group invoked with
no arguments prints help and exits 0; an unknown child or stray positional
argument is a structured usage error (exit 2) before configuration, self-update,
stdin, or network access.

```
atl conf search --cql "space=DOCS" -o text
atl jira issue view PROJ-1 -o text
```

## Body input (`--from-file`)

Commands that accept a document body (CSF or Jira wiki) read it from a file
path or from stdin when you pass `-`:

```bash
# from a file
atl conf page create --space DOCS --title "New page" --from-file body.csf

# from stdin (pipe a heredoc or a prior command's output)
echo '<p>Hello</p>' | atl conf page create --space DOCS --title "New page" \
    --from-file -
```

The defaults follow one rule: commands whose body is **required** default
`--from-file` to `-` (stdin) — `conf page create`, `conf blog create`, `conf comment
preview`, `conf comment add`, `jira issue comment preview`, and `jira issue
comment add`; commands whose body is **optional** default to no
body — `jira issue create`, `jira issue update`, and the worklog comment on
`jira issue worklog add`. When stdin is an
interactive terminal (nothing piped), reading a body from it is refused with
a usage error (exit 2) instead of hanging forever waiting for input.

## Exit codes

| code | meaning |
|---|---|
| 0 | success |
| 1 | generic error |
| 2 | usage error (bad flags, missing required args, insecure backend URL) |
| 3 | authentication failed (a PAT **was** supplied but the server rejected it) |
| 4 | resource not found |
| 5 | version conflict (preserve the candidate, reconcile with fresh remote state, then make a new preview) |
| 6 | forbidden (per-space or per-issue permission) |
| 7 | not configured (backend URL or PAT **not set** yet; run `atl config set` / `atl auth login`) |
| 8 | safety/check failed (validation, lossy conversion, ambiguous write outcome, or app-layer Jira drift) |

A script can therefore tell three distinct "auth-ish" states apart: `7` = you
have not finished setup (no URL/token) → run setup; `3` = the token you supplied
was refused → replace it; `6` = the token is valid but lacks permission. Note the
split for a bad URL: a *missing* URL is `7`, but a *non-https* (insecure) URL is a
usage error (`2`) — fix the input rather than re-running setup.

---

## Environment variables

## Backend URLs

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_URL` | Confluence base URL (takes priority over `CONFLUENCE_URL`) |
| `CONFLUENCE_URL` | Confluence base URL (fallback) |
| `ATL_JIRA_URL` | Jira base URL (takes priority over `JIRA_URL`) |
| `JIRA_URL` | Jira base URL (fallback) |
| `ATL_ALLOW_INSECURE` | set to any non-empty value to permit a non-https backend URL for a non-loopback host (an internal http-only instance you trust). Loopback hosts are always allowed; otherwise a non-https URL is refused so the PAT is never sent in cleartext |

## Mirror location

| variable | effect |
|---|---|
| `ATL_MIRROR_ROOT` | default mirror root for Confluence/Jira pull plus status/snapshot inspection (and `conf diff`); required by the no-argument MCP mirror snapshot tools, which validate an existing `.atl` directory and never accept a model-supplied path (an explicit CLI path or `--into` still overrides it) |

Mirror writes are contained beneath the selected root even when a checkout
contains descendant symlinks. Mirror listings used by `status` and directory
`push` fail on unreadable/corrupt entries rather than reporting an incomplete
tree as success.

## Authentication

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_PAT` | Confluence Personal Access Token |
| `ATL_JIRA_PAT` | Jira Personal Access Token |

Env vars take priority over the stored credentials file. See `atl auth` below
for how to store PATs on disk.

## Config directory

| variable | effect |
|---|---|
| `ATL_CONFIG_DIR` | override config/credentials directory (default: `$XDG_CONFIG_HOME/atl` or `~/.config/atl`) |
| `XDG_CONFIG_HOME` | standard XDG base directory (used when `ATL_CONFIG_DIR` is not set) |

## Self-update

| variable | effect |
|---|---|
| `ATL_UPDATE_URL` | override the distribution server base URL |
| `ATL_NO_UPDATE` | set to any non-empty value to disable auto-update |
| `ATL_UPDATE_DEBUG` | set to any non-empty value to print self-update diagnostics to stderr |

`ATL_READ_ONLY=1` prevents writes but intentionally permits backend reads;
`ATL_NO_UPDATE=1` disables only the release check. For the complete destination
and trigger inventory, package-manager behavior, and air-gap recipe, see
[network-egress.md](../../network-egress.md).

---

## Scripting & CI

`atl` is built for non-interactive use: JSON to stdout, diagnostics to stderr,
stable exit codes, no prompts. A robust CI/script harness looks like this:

```bash
#!/usr/bin/env bash
set -euo pipefail

# 1. Configure entirely from the environment (URLs + PATs); no on-disk config.
export ATL_CONFLUENCE_URL="https://confluence.example.com"
export ATL_CONFLUENCE_PAT="$CONFLUENCE_TOKEN"   # from your CI secret store

# 2. Disable the best-effort self-update so a command never spends time probing
#    the release server (it is throttled, but a fresh runner has no throttle file).
export ATL_NO_UPDATE=1

# 3. Isolate credentials: point at a throwaway config dir so a leftover
#    ~/.config/atl/credentials.json from a previous job can't silently win.
export ATL_CONFIG_DIR="$(mktemp -d)"

# 4. Fail fast with a clear signal if setup/connectivity is wrong.
if atl conf search --cql 'type = page' --limit 1 >/dev/null; then
  : # connected
else
  code=$?
  case $code in
    7) echo "atl is not configured (URL/PAT missing)"   >&2 ;;
    3) echo "atl PAT was rejected by the server"          >&2 ;;
    *) echo "atl connectivity check failed (exit $code)"  >&2 ;;
  esac
  exit $code
fi

atl conf pull --cql 'label = runbook' --into "$PWD/mirror"
```

Notes for scripts:

- **Errors are JSON too.** On success `atl` prints a JSON result to stdout; on
  failure it prints `error`, the unchanged numeric `code`, stable `kind`, and
  deterministic `remediation` and schema-v1 `recovery` to **stderr** (use
  `-o text` for a plain `error: <msg>` line). Branch on `kind`/exit code;
  remediation/recovery are guidance for
  the agent to present, never permission to retry or mutate automatically.
  `rate_limited` / `wait_before_retry` means the bounded replay-safe read retry
  policy was exhausted; wait before a later read instead of immediately
  repeating it, and never retry a write automatically.
  `output_limit_exceeded` / `narrow_or_raise_bound` means the selected
  `max_bytes` rejected the complete encoded result; it is not partial evidence,
  so narrow the query/selection or deliberately choose a larger allowed bound.
- **Ordinary `--cql` pull caps at 1000 pages; `--space` at 2000.** When either cap is
  hit the result carries `"truncated": true` / `"truncated_at": N` and a
  `warning:` line is printed to stderr — the rest is not mirrored. Narrow the
  selection, or use explicit resumable `--complete` for a full historical
  selector.
- **`--from-file -` (stdin) is bounded at 64 MiB**; larger input is rejected
  with a usage error (exit 2) — pass a file path for bigger bodies.
- **Direct REST fallback:** when you must call an uncovered Server/Data Center
  endpoint yourself, keep PATs out of argv and shell history. Put the token in an
  env var, disable shell tracing, and feed curl's header through stdin:

  ```bash
  set +x
  {
    printf 'url = "%s/rest/api/2/myself"\n' "$ATL_JIRA_URL"
    printf 'header = "Authorization: Bearer %s"\n' "$ATL_JIRA_PAT"
  } | curl --fail --silent --show-error --config -
  ```

---

## `atl version`

Print the current binary version and informational build provenance. JSON is
the default:

```json
{
  "version": "0.6.0",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "build_state": "clean"
}
```

`commit` is the full source revision when known. `build_state` is one of
`clean`, `dirty`, or `unknown`; it describes tracked and non-ignored untracked
workspace changes for supported Makefile builds. Direct Go builds use compiler
VCS metadata when available. These fields are diagnostic only: self-update and
signature verification do not trust them. Builds intentionally contain no
timestamp.

```
atl version
atl version -o text
```

Text output remains the bare version for script compatibility. Root
`atl --version` also keeps its existing `atl version <version>` form.

---
