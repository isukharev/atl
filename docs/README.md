# `atl` documentation

Start with the task, not the command tree.

## First use

| Goal | Guide | Outcome |
|---|---|---|
| Install and prove one backend works | [Getting started](getting-started.md) | First bounded read and local mirror |
| Connect Claude Code or Codex | [Agent setup](agent-setup.md) | Focused skills and typed read-only MCP |
| Edit and publish safely | [Safe writes](safe-writes.md) | Reviewed native diff and guarded apply |
| Check deployment support | [Compatibility](compatibility.md) | Explicit evidence and limitations |
| Recover from an error | [Troubleshooting](troubleshooting.md) | Exit-code-first remediation |

The [project README](../README.md) is the compact product overview. The guides
above are the canonical first-use path; you do not need the exhaustive
reference before the first successful workflow.

## Common workflows

| Guide | Audience | What it contains |
|---|---|---|
| [agent-recipes.md](agent-recipes.md) | Agents / users | Runnable Jira, board, Structure, Confluence, and guarded-write recipes |
| [csf-and-fragments.md](csf-and-fragments.md) | Confluence users | Native `.csf`, opaque macros/assets, and derived Markdown views |
| [jira-guarded-writeback.md](jira-guarded-writeback.md) | Jira automation | Reviewed batch and field write contracts |
| [mcp.md](mcp.md) | Agent clients | Exact typed read-only tools, profiles, bounds, and CLI fallback |
| [network-egress.md](network-egress.md) | Security / platform | Runtime destinations, read-only/update controls, and air-gap boundaries |
| [self-update.md](self-update.md) | Security / platform | Signed update trust model and disable controls |

## Canonical references

| Reference | Use it for |
|---|---|
| [usage.md](usage.md) | Every command, flag, environment variable, and scripting pattern |
| [OUTPUT_CONTRACT.md](OUTPUT_CONTRACT.md) | JSON/text/id output, exit classes, completeness, and recovery schemas |
| `atl --help` | The command tree shipped by the installed binary |
| `atl capabilities` | Offline, versioned task-to-command/MCP routing |

Discover a parent route without config or credentials:

```sh
ATL_NO_UPDATE=1 atl --help
ATL_NO_UPDATE=1 atl jira --help
ATL_NO_UPDATE=1 atl conf --help
ATL_NO_UPDATE=1 atl capabilities --task jira/evidence
```

## Contributors and maintainers

| Doc | What's in it |
|---|---|
| [architecture.md](architecture.md) | Hexagonal layout, dependency rule, and extension points |
| [plugins.md](plugins.md) | Generated Claude Code/Codex skills and plugin pipeline |
| [github-issue-workflow.md](github-issue-workflow.md) | Issue-first development and agent handoff |
| [context7.md](context7.md) | Public documentation indexing and refresh |
| [RELEASING.md](RELEASING.md) | Signing, release, Homebrew, and verification |
| [agent-benchmarking.md](agent-benchmarking.md) | Public deterministic evaluation contracts |
| [agent-benchmark-private-workspace.md](agent-benchmark-private-workspace.md) | Owner-private benchmark lifecycle and publication boundary |
| [csf-markdown-testing.md](csf-markdown-testing.md) | Durable-view migration and corpus verification |

## Support

Use [GitHub Issues](https://github.com/isukharev/atl/issues/new/choose) for
questions, feature requests, compatibility reports, and reproducible defects.
Security reports follow [SECURITY.md](../SECURITY.md).

Public reports must be sanitized: no credentials, private hosts, object IDs,
titles/content, user identity, company data, or private local paths.
