**English** · [Русский](README.ru.md)

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Main smoke](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=main%20smoke)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Documentation](docs/README.md) · [Compatibility](docs/compatibility.md) ·
[Roadmap](ROADMAP.md) · [Release notes](CHANGELOG.md) · [Contributing](CONTRIBUTING.md) ·
[Security](SECURITY.md)

**Lossless local workflows for Jira and Confluence Server/Data Center.**

`atl` lets people and coding agents inspect, mirror, diff, and update
Atlassian content with ordinary local tools. Confluence `.csf` and Jira
`.wiki` bytes remain the write substrate; Markdown is a readable staging view,
not a lossy replacement. Remote changes pass explicit version, baseline, or
proposal gates instead of silently overwriting concurrent work.

```sh
export ATL_READ_ONLY=1
atl jira issue search --jql 'order by updated DESC' --limit 5
atl conf search --cql 'type = page' --limit 5
```

Pulling writes local mirror files but never mutates Jira or Confluence. Keep the
read-only policy until one exact write proposal has been reviewed.

> `atl` is an independent open-source project. It is not affiliated with,
> endorsed by, or sponsored by Atlassian Pty Ltd.

## Start with your task

| Goal | Short guide |
|---|---|
| Install and prove one backend works | [Five-minute setup](docs/getting-started.md) |
| Give a coding agent safe access | [Agent setup](docs/agent-setup.md) |
| Mirror, edit, and publish safely | [Safe writes](docs/safe-writes.md) |
| Refresh or recover an existing mirror | [Mirrors and recovery](docs/mirrors-and-recovery.md) |
| Trace Jira links, docs, and code identities | [Jira artifact graph](docs/jira-artifact-graph.md) |
| Read or change Confluence discussions | [Qualified comments](docs/confluence-comments.md) |
| See the core guarantees without credentials | [Reproducible demos](docs/demos/README.md) |
| Diagnose setup, access, or conflict errors | [Troubleshooting](docs/troubleshooting.md) |

The [task-first documentation index](docs/README.md) leads to focused workflows.
The exhaustive [command reference](docs/reference/cli/README.md) and
[output contract](docs/reference/output/README.md) are available when exact flags or
wire fields matter; neither is required before the first useful read.

## Install

Linux and macOS release binaries are available for amd64 and arm64.

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256. Releases also publish checksums, signatures,
and SLSA provenance. Alternatives:

```sh
brew install isukharev/tap/atl
```

Direct downloads are on [GitHub Releases](https://github.com/isukharev/atl/releases).
Source contributors should clone the repository and use `make install`, which
stamps the repository version and build identity.
Windows and Atlassian Cloud are not currently supported; review the
[compatibility matrix](docs/compatibility.md) before deployment.

## Five-minute first read

Configure only the service you need. This example uses Jira; substitute the
Confluence flags and service name for Confluence.

```sh
atl config set --jira-url https://jira.example.com
atl auth login --service jira
atl auth status
atl doctor --remote

export ATL_READ_ONLY=1
atl jira issue search --jql 'order by updated DESC' --limit 5
```

`auth login` reads the bearer PAT from a no-echo prompt, stdin, or a file—never
from argv. `doctor` is offline unless `--remote` is explicit. Remote mode makes
bounded product/version probes without reading page or issue bodies. JSON is
the default output; logs and errors stay on stderr.

For Confluence:

```sh
atl config set --confluence-url https://confluence.example.com
atl auth login --service confluence
export ATL_READ_ONLY=1
atl conf search --cql 'type = page' --limit 5
```

An empty result proves absence only when its completeness/truncation fields say
the selection is complete.

## Three working loops

### 1. Read narrowly

Start with CQL/JQL discovery, then read only the selected object or fields.
Use `atl jira issue graph KEY --depth 0` when the question spans structured
links, hierarchy, documentation, attachments, or Development identities. Use
`atl conf comment list --id ID` before expanding one exact thread. Both
surfaces qualify incomplete evidence instead of treating a failed or bounded
collector as an empty answer.

Typed MCP offers smaller, read-only projections for agents. The CLI remains the
route for native bodies, durable mirrors, large bounded traversals, exports,
and every write.

### 2. Mirror and review locally

Keep a mirror outside a source repository and pass its root explicitly:

```sh
export ATL_READ_ONLY=1
export ATL_WORKSPACE_ROOT=/absolute/path/to/atl-workspace

atl conf pull --id 123456 --into "$ATL_WORKSPACE_ROOT"
atl conf status --into "$ATL_WORKSPACE_ROOT"
atl conf diff "$ATL_WORKSPACE_ROOT" -o text
```

The `.csf` file contains the exact native Confluence body. Its `.md` sibling is
a derived view for reading and supported staging edits. After editing Markdown:

```sh
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md" --dry-run
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md"
atl conf validate "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf"
atl conf diff "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf" -o text
```

Untouched native blocks remain byte-identical. Unsupported Markdown changes,
fragment loss, malformed CSF, or a changed baseline fail before publication.
`conf apply` changes local native bytes, so it is mutation-classified even
during dry-run; the scoped `env -u` leaves the shell-wide policy intact.
Pull refuses to overwrite local native or derived-view edits; use its dry-run,
stash, or explicit overwrite recovery rather than losing work. Durable mirrors
are also bound to a content-minimized backend identity so a staging mirror
cannot be pushed accidentally to another configured instance.

Jira follows the same local pattern with native `.wiki` files, `jira pull`,
`jira status`, `jira apply`, `jira reconcile preview`, and `jira push`.

### 3. Preview, apply once, reconcile

The write loop is fresh read → candidate → diff/preview → reviewed
version/baseline/hash → one apply → reconciliation. A push preview is still a
mutating-classified command, so remove the read-only policy only for that exact
process while leaving it set in the shell:

```sh
env -u ATL_READ_ONLY atl conf push \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf" --dry-run
```

After reviewing the complete result, run the same command without `--dry-run`.
A Confluence version conflict exits `5`; preserve the local candidate and use
`conf reconcile preview` instead of auto-forcing. Proposal-bound comment,
create/copy, trash, Jira field, transition, and deletion workflows require the
emitted expected values and never retry ambiguous writes. Follow the
[safe-write guide](docs/safe-writes.md) for exact apply and recovery commands.
Confluence page trash accepts only a canonical positive numeric `--id`; page
aliases, URLs, signs, leading zeroes, and surrounding whitespace fail before
configuration or backend access.

## Coding agents

The repository ships matching Claude Code and Codex plugins plus a typed
read-only MCP server.

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

Start a new agent session after installation. The [agent setup guide](docs/agent-setup.md)
covers focused skills, MCP/CLI routing, read-only policy, mirror placement, and
version-skew recovery.

## Safety and compatibility

- `ATL_READ_ONLY=1` / `--read-only` blocks remote mutations before credentials,
  body files, self-update, or network access.
- PATs are host-scoped; cross-host and HTTPS-downgrade redirects are refused.
  Mutating requests never follow redirects or use generic retries.
- Stable exit codes distinguish usage, authentication, not-found, version
  conflict, forbidden, configuration, and failed safety checks.
- Reads are bounded and report incomplete or truncated evidence explicitly.
- Signed self-update verifies the manifest and binary before replacement and
  can be disabled with `ATL_NO_UPDATE=1`.

`atl` targets Jira and Confluence Server/Data Center with bearer PATs. See
[compatibility](docs/compatibility.md), [network egress](docs/network-egress.md),
[self-update trust](docs/self-update.md), and [SECURITY.md](SECURITY.md).

## Documentation and contributing

- [Task-first documentation index](docs/README.md)
- [Runnable agent recipes](docs/agent-recipes.md)
- [Confluence native storage and fragments](docs/csf-and-fragments.md)
- [Typed read-only MCP](docs/mcp.md)
- [Architecture](docs/architecture.md)

Questions and sanitized compatibility reports belong in
[GitHub Issues](https://github.com/isukharev/atl/issues/new/choose). Never
publish credentials, private hosts, object identifiers, titles/content, user
identity, company data, or private local paths. Security vulnerabilities follow
[SECURITY.md](SECURITY.md).

```sh
make build
make test
make lint
```

The code follows a hexagonal ports-and-adapters architecture. See
[CONTRIBUTING.md](CONTRIBUTING.md). Apache License 2.0 — [LICENSE](LICENSE).
Third-party notices and the Atlassian trademark disclaimer are in
[NOTICE](NOTICE).
