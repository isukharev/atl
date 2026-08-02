**English** · [Русский](README.ru.md)

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Main smoke](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=main%20smoke)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Documentation](docs/README.md) · [Compatibility](docs/compatibility.md) ·
[Roadmap](ROADMAP.md) · [Contributing](CONTRIBUTING.md) ·
[Security](SECURITY.md)

**Lossless local workflows for Jira and Confluence Server/Data Center.**

`atl` lets people and coding agents inspect, mirror, diff, and update
Atlassian content with ordinary local tools. Confluence `.csf` and Jira
`.wiki` bytes remain the write substrate; Markdown is a derived staging view.
Remote changes pass explicit version, baseline, or proposal gates instead of
silently overwriting concurrent work.

```sh
export ATL_READ_ONLY=1
atl conf search --cql 'type = page' --limit 1
atl conf pull --id 123456 --into "$HOME/.atl/example-workspace"
atl conf diff "$HOME/.atl/example-workspace" -o text
```

Pull writes local mirror files but never mutates Jira or Confluence. Remove the
read-only policy only after reviewing a concrete write proposal.

> `atl` is an independent open-source project. It is not affiliated with,
> endorsed by, or sponsored by Atlassian Pty Ltd.

## Start with a task

| Goal | Guide | Outcome |
|---|---|---|
| Install and prove one backend works | [Getting started](docs/getting-started.md) | First bounded read and local mirror |
| Give a coding agent safe access | [Agent setup](docs/agent-setup.md) | Focused skills plus typed read-only MCP |
| Mirror, edit, review, and publish | [Safe writes](docs/safe-writes.md) | Native local diff and one guarded write |
| Check whether an environment fits | [Compatibility](docs/compatibility.md) | Supported, unverified, and unsupported boundaries |
| Recover from an error | [Troubleshooting](docs/troubleshooting.md) | Exit-code-first recovery |

The exhaustive [command reference](docs/usage.md) and
[output contract](docs/OUTPUT_CONTRACT.md) remain available, but neither is
required before the first successful workflow.

Given one exact Jira issue, `atl jira issue graph PROJ-123` returns its
provenance-qualified, schema-v2 direct work-artifact graph without following
any discovered issue, page, or URL. `--depth 1..3` follows only exact structured
Jira relations under hard request/output budgets; optional
`--resolve confluence` reads only page id/title metadata. The explicit
`--include-development` flag adds content-minimized GitLab project, full commit,
exact branch, and merge-request coordinates from Jira's experimental
Development surface. It never contacts GitLab or fetches a returned artifact
URL, and an incomplete Development source emits no Development facts. Every returned field
is reconciled with its inspection metadata: missing or invalid metadata makes
the named source partial, while issue properties are explicitly experimental.
The typed `jira_issue_graph` MCP tool exposes the same schema-v2 graph under
Jira-only traversal: it accepts no Confluence-resolution input, leaves page
identities as qualified stubs, omits the CLI-only Development source without
implying zero activity, and keeps the fixed backend-response bound separate
from the configurable encoded-result bound.

For Confluence discussions, `atl conf comment list --id 123456` returns a
schema-v2 qualified inventory across footer, inline, and resolved comments,
with proven thread relationships, independent completeness dimensions, and
exact native-CSF anchor matching. `conf comment thread` selects one exact root
subtree and scopes diagnostics/completeness to it; missing ancestry or markers
remain explicit partial evidence. The explicit backend `reopened` state is
normalized to semantic `open`; unknown states remain partial. Qualified backend
reads are bound internally to the reconciled page revision, including every
pagination request.
`conf pull --comments` stores that qualification in a versioned mirror sidecar;
the page's main `.md` renders a deterministic read-only tree with explicit
location/state, completeness, safely qualified anchors, and unattached records.
The schema-v2 `.comments.json` remains the source evidence (including closed
diagnostics), while `.comments.md` remains a flat compatibility view;
historical flat sidecars stay readable and comments never affect page drift.
The offline `confluence/comments` capability route separates list, exact-thread,
preview, and add. MCP exposes only the first two as bounded read-only tools:
body-free `confluence_comment_list` for discovery and
`confluence_comment_thread` for one exact plain-text expansion. Partial results
never prove absence, and preview/add remain guarded CLI-only operations.

## Install

Linux and macOS release binaries are static and available for amd64 and arm64.

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256. Releases also publish checksums, signatures,
and SLSA provenance.

Homebrew:

```sh
brew install isukharev/tap/atl
```

From source (Go 1.26.5+):

```sh
go install github.com/isukharev/atl/cmd/atl@latest
```

See [GitHub Releases](https://github.com/isukharev/atl/releases) for direct
downloads. Windows is not currently supported; the complete platform and
backend evidence is in [compatibility.md](docs/compatibility.md).

## First read

Configure only the service you need:

```sh
atl config set --confluence-url https://confluence.example.com
# or:
atl config set --jira-url https://jira.example.com

atl auth login --service confluence
# or:
atl auth login --service jira

atl auth status
atl doctor
```

`auth login` reads the bearer PAT from a no-echo prompt, stdin, or a file—never
from argv. `auth status` reports only the credential source. `doctor` checks
build, config permissions, URL policy, credential presence, and optional mirror
health without printing URLs, hostnames, paths, identities, tokens, or content.

Then make one bounded read:

```sh
export ATL_READ_ONLY=1

atl doctor --remote
atl conf search --cql 'type = page' --limit 1
# or:
atl jira issue search --jql 'order by updated DESC' --limit 1
```

`doctor` is offline unless `--remote` is explicit. Remote mode makes one
single-attempt product/version GET per ready backend; only when the Confluence
version route is absent may it add one bodyless reachability HEAD. It reads no
page/issue body, search result, or identity. Reachability without a version is
reported as unverified compatibility. Blocking findings still emit the
qualified report and exit `8`. JSON is the default output. Continue with the
[five-minute guide](docs/getting-started.md).

Experimental Data Center compatibility providers are disabled separately and
never selected by version range:

```sh
atl compatibility status
atl compatibility pin confluence \
  --version "$ATL_CONFLUENCE_VERSION" \
  --build-number "$ATL_CONFLUENCE_BUILD_NUMBER"
atl compatibility status --remote
```

The owner-only pin is separate from ordinary `config.json` and cannot provide
custom endpoints, headers, payloads, or a fallback REST route.

## Three primary workflows

### 1. Read narrow

Use CQL/JQL discovery, then fetch only the selected object or fields. Check
completeness and truncation before claiming something is absent.

```sh
export ATL_READ_ONLY=1
atl jira issue search \
  --jql 'assignee = currentUser() order by updated DESC' \
  --limit 20
atl conf search --cql 'type = page' --limit 20
```

### 2. Mirror and diff

Keep mirrors outside a source repository:

```sh
export ATL_READ_ONLY=1
export ATL_MIRROR_ROOT="$HOME/.atl/example-workspace"

atl conf pull --id 123456
atl conf status "$ATL_MIRROR_ROOT"
atl conf diff "$ATL_MIRROR_ROOT" -o text

# Jira lane:
atl jira pull --jql 'project = EXAMPLE order by key' --limit 20
atl jira status "$ATL_MIRROR_ROOT"
```

Use `.md` for reading and supported staging edits. Native `.csf` / `.wiki`
files preserve constructs that Markdown cannot represent.

### 3. Review a write

The write loop is fresh read → candidate → diff/preview → reviewed
version/baseline/hash → one apply → reconciliation.

```sh
atl conf apply "$ATL_MIRROR_ROOT/SPACE/page/page.md"
atl conf validate "$ATL_MIRROR_ROOT/SPACE/page/page.csf"
atl conf diff "$ATL_MIRROR_ROOT/SPACE/page/page.csf" -o text
atl conf push "$ATL_MIRROR_ROOT/SPACE/page/page.csf" --dry-run
```

After review, repeat the exact guarded command without `--dry-run`. A
Confluence version conflict exits `5`; re-pull and reapply instead of
auto-forcing. For a new Confluence comment, use the read-only `conf comment
preview`, then run `conf comment add` on the exact native-CSF body with `--apply`
and `--expected-proposal-hash`; `add` is dry-run by default but remains a
mutating-classified command. It creates footer roots only and never replays an
ambiguous POST. Existing inline threads can be replied to, resolved, or reopened
through the exact-pinned `conf comment mutation preview|apply` loop. The same
loop creates a new server-owned inline anchor from an exact file-backed text
selection and zero-based occurrence; ATL derives the version-specific browser
geometry and never edits marker CSF itself. Jira writes follow the same reviewed-baseline rule. Follow the
[safe-write guide](docs/safe-writes.md).

## Coding agents

The repository ships the same focused skills for Claude Code and Codex plus a
typed read-only MCP server. The CLI remains the route for durable mirrors,
exports, raw Structure data, and every write.

Claude Code:

```text
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

Codex:

```sh
codex plugin marketplace add isukharev/atl
codex plugin add atl@atl
```

Start a new agent session after installation, then invoke the explicit setup
skill. See [agent setup](docs/agent-setup.md) for version-skew recovery, mirror
placement, read-only policy, and CLI/MCP selection.

## Why `atl`

The project combines four contracts:

- lossless native local storage rather than a Markdown-only write path;
- ordinary offline search, diff, status, and review workflows;
- optimistic or baseline-bound writes with no blind retries;
- bounded JSON/typed MCP evidence designed for automation and agents.

`atl` is deliberately Server/Data Center and local-first. Atlassian CLI and
Rovo MCP serve Atlassian Cloud use cases; community MCP servers prioritize a
broad live tool inventory. Choose `atl` when native local bytes, offline diffs,
and explicit write gates matter. The sourced, non-ranking comparison is in
[compatibility.md](docs/compatibility.md#choosing-a-different-tool).

## Safety and output

- `ATL_READ_ONLY=1` / `--read-only` blocks mutations before credentials, body
  files, self-update, or network access.
- PATs are host-scoped; cross-host and HTTPS-downgrade redirects are refused,
  and mutating requests never follow redirects.
- JSON goes to stdout by default; logs/errors go to stderr.
- Stable exit codes classify usage, auth, not-found, version conflict,
  forbidden, config, and safety failures.
- Reads are bounded and qualify incomplete/truncated results.
- Generic retries apply only to replay-safe reads, never writes.
- Signed self-update has a five-second remote startup budget and can be
  disabled with `ATL_NO_UPDATE=1`.

Details: [output contract](docs/OUTPUT_CONTRACT.md),
[network egress](docs/network-egress.md),
[self-update trust](docs/self-update.md), and [SECURITY.md](SECURITY.md).

## Documentation

- [Task-first index](docs/README.md)
- [Runnable agent recipes](docs/agent-recipes.md)
- [Full command reference](docs/usage.md)
- [Confluence storage and fragments](docs/csf-and-fragments.md)
- [Typed read-only MCP](docs/mcp.md)
- [Architecture](docs/architecture.md)

Questions, compatibility reports, and sanitized defects use
[GitHub Issues](https://github.com/isukharev/atl/issues/new/choose). Never
publish credentials, private hosts, object identifiers, titles/content, user
identity, company data, or private local paths. Security vulnerabilities follow
[SECURITY.md](SECURITY.md).

## Build and contribute

```sh
make build
make test
make lint
```

The code follows a hexagonal ports-and-adapters architecture. See
[architecture.md](docs/architecture.md) and
[CONTRIBUTING.md](CONTRIBUTING.md).

Apache License 2.0 — [LICENSE](LICENSE). Third-party notices:
[NOTICE](NOTICE).

“Atlassian”, “Confluence”, and “Jira” are registered trademarks of Atlassian
Pty Ltd and are used only to identify the products with which `atl`
interoperates. The project makes no warranty; see [NOTICE](NOTICE).
