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
Atlassian content. Native Confluence `.csf` and Jira `.wiki` bytes remain the
write substrate; Markdown is a staging view. Remote
writes require explicit version, baseline, or proposal gates.

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
| Install and configure private PKI trust | [Five-minute setup](docs/getting-started.md) |
| Give a coding agent safe access | [Agent setup](docs/agent-setup.md) |
| Mirror, edit, and publish safely | [Safe writes](docs/safe-writes.md) |
| Refresh or recover an existing mirror | [Mirrors and recovery](docs/mirrors-and-recovery.md) |
| Compare qualified generations | [Sealed corpus generations](docs/corpus-generations.md) |
| Build a private corpus in a container | [Corpus dev-container](docs/corpus-devcontainer.md) |
| Discover Jira projects, create schema, and [links](docs/jira-artifact-graph.md) | [Jira commands](docs/reference/cli/README.md) |
| Read or change Confluence discussions | [Qualified comments](docs/confluence-comments.md) |
| See the core guarantees without credentials | [Reproducible demos](docs/demos/README.md) |
| Diagnose setup, access, or conflict errors | [Troubleshooting](docs/troubleshooting.md) |

The [documentation index](docs/README.md) leads to workflows. Use the
[command](docs/reference/cli/README.md) or
[output](docs/reference/output/README.md) reference for exact flags and wire fields.

## Install

Linux and macOS release binaries are available for amd64 and arm64.

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256; releases also publish checksums, signatures,
and SLSA provenance:

```sh
brew install isukharev/tap/atl
```

Use [GitHub Releases](https://github.com/isukharev/atl/releases) for direct
downloads and `make install` for a source build with repository identity.
Windows and Atlassian Cloud are unsupported; review the
[compatibility matrix](docs/compatibility.md).

## Five-minute first read

Configure only the service you need. This example uses Jira; substitute the
Confluence flags and service name for Confluence.

```sh
atl config set --jira-url https://jira.example.com
atl auth login --service jira
atl auth status
atl doctor --service jira --remote

export ATL_READ_ONLY=1
atl jira issue search --jql 'order by updated DESC' --limit 5
```

`auth login` reads the PAT from a no-echo prompt, stdin, or file—not argv.
`doctor --service jira|confluence` scopes health and stays offline without
`--remote`, which makes bounded body-free product/version probes. `safety`
reports configured/effective read-only state and source
`flag|environment|configuration|none`. JSON defaults to stdout; diagnostics use
stderr.

For Confluence:

```sh
atl config set --confluence-url https://confluence.example.com
atl auth login --service confluence
atl doctor --service confluence --remote
export ATL_READ_ONLY=1
atl conf search --cql 'type = page' --limit 5
```

Claim absence only from `complete:true`; follow `next_cursor`, and never trust
an unqualified full page.

## Three working loops

### 1. Read narrowly

Start with CQL/JQL discovery, then read only selected objects or fields. Use
`atl jira issue graph KEY --depth 0` for links, hierarchy, documentation,
attachments, or Development identities; add `--projection compact` for
qualified URL/SCM JSON. From one GitLab project or Confluence page, use CLI-only
`atl jira issue reference search` with explicit JQL scope, sources, mode, and
limits; only a complete exhaustive result proves absence. Run `atl conf comment
list --id ID` before expanding one thread. These surfaces qualify incomplete
evidence; graph text exposes safe URL-node identities in its `URL` column.

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

The `.csf` file contains the exact native Confluence body; its `.md` sibling is
a derived reading and staging view. After editing Markdown:

```sh
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md" --dry-run
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md"
atl conf validate "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf"
atl conf diff "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf" -o text
```

Untouched native blocks remain byte-identical. Unsupported Markdown, fragment
loss, malformed CSF, or a changed baseline fail before publication. `conf apply`
changes local bytes and remains mutation-classified during dry-run; the scoped
`env -u` preserves the shell-wide policy. Pull refuses to overwrite local edits;
use dry-run, stash, or explicit overwrite recovery. Mirrors are bound to a
content-minimized backend identity to prevent accidental cross-instance push.

Jira uses native `.wiki` files; see [Jira mirrors](docs/reference/cli/jira-mirrors.md)
for ordinary and qualified resumable project pulls, apply, reconcile, and push.

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

Claude Code and Codex plugins include typed read-only MCP.

Inspect offline upper bounds with `atl capabilities --effects`
or `--effects --command "jira issue search"`; they are informational, not
authorization or enforcement.

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

Restart after installing. ATL supports MCP `2026-07-28` and `2025-11-25`. The
[agent setup guide](docs/agent-setup.md) covers routing, safety, mirrors, skew,
the plugin/binary startup gate, and Codex's modern opt-in; standalone `atl mcp
serve` remains supported.

[`agent-eval`](docs/reference/agent-eval/README.md) is pre-release.

## Safety and compatibility

- `ATL_READ_ONLY=1` / `--read-only` blocks mutations before credentials, files,
  self-update, or network.
- [Inspect scoped write grants](docs/reference/cli/policy.md) with `atl policy show`.
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
- [Scoped write policy](docs/reference/cli/policy.md)
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
