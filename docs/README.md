# `atl` documentation

Start with the project [README](../README.md) for install, quick start, and the Claude Code/Codex
agent plugins.
This directory holds the deeper references:

| Doc | Audience | What's in it |
|-----|----------|--------------|
| [usage.md](usage.md) | **Users / scripts** | Full command reference, global conventions, exit codes, environment variables, and a **Scripting & CI** harness. |
| [agent-recipes.md](agent-recipes.md) | **Agents / users** | Compact runnable recipes for high-frequency Jira, board, Structure, Confluence, and guarded-write workflows. |
| [csf-and-fragments.md](csf-and-fragments.md) | Power users | Confluence Storage Format (`.csf`) and how opaque fragments (drawio, images, users, links) are extracted and resolved. |
| [self-update.md](self-update.md) | Security-conscious users | The signed self-update trust model and how to disable it (`ATL_NO_UPDATE`). |
| [network-egress.md](network-egress.md) | Security-conscious users / agents | Runtime egress inventory, independent read-only/update controls, and air-gapped operation. |
| [architecture.md](architecture.md) | Contributors | Hexagonal (ports & adapters) layout, the dependency rule, and extension points (new backend, new fragment type). |
| [github-issue-workflow.md](github-issue-workflow.md) | Maintainers / agents | GitHub Issues, parent/sub-issues, labels, and PR process for roadmap-driven AI-agent work. |
| [agent-benchmarking.md](agent-benchmarking.md) | Maintainers | Versioned agent-evaluation contracts, deterministic workflow budgets, headless comparisons, privacy rules, and re-measurement guidance. |
| [agent-benchmark-private-workspace.md](agent-benchmark-private-workspace.md) | Maintainers / agents | Owner-private live-evaluation workspace, reviewed plans, compact baselines, offline comparison, retention, and publication boundary. |
| [plugins.md](plugins.md) | Contributors | Agent-plugin pipeline: `skills-src/` source of truth, the generator, placeholders, and how to add a skill or platform. |
| [context7.md](context7.md) | Maintainers / agents | Public-doc indexing scope, one-time registration, lookup verification, and refresh policy for Context7. |
| [RELEASING.md](RELEASING.md) | Maintainers | Signing key, release workflow, the Homebrew formula, and verification. |

New here? The [Quick start](../README.md#quick-start) gets you from install to a first result in four
commands; for non-interactive use jump to [usage.md → Scripting & CI](usage.md#scripting--ci).

Discover the command surface before choosing a workflow:

```sh
atl --help
atl jira --help
atl conf --help
```
