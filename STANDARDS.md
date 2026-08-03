# Maintainer documentation authority

This file routes maintainers to the single canonical owner for each topic. It
does not duplicate those policies. User and operator documentation starts at
the [task-first documentation index](docs/README.md).

## Instruction precedence

[`AGENTS.md`](AGENTS.md) is the binding cross-agent repository contract for
safety, ownership, architecture invariants, issue-first work, verification, and
handoff. Provider-specific instruction files, including
[`CLAUDE.md`](CLAUDE.md), may add execution guidance but do not replace or
weaken that contract.

## Canonical owners

| Topic | Canonical document |
|---|---|
| Contributor setup and pull-request expectations | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Agent authority, repository safety, and non-negotiable invariants | [AGENTS.md](AGENTS.md) |
| Architecture and extension points | [docs/architecture.md](docs/architecture.md) |
| CLI usage and flags | [docs/usage.md](docs/usage.md) |
| Output schemas, exit classes, and recovery | [docs/OUTPUT_CONTRACT.md](docs/OUTPUT_CONTRACT.md) |
| Native Confluence storage and staging model | [docs/csf-and-fragments.md](docs/csf-and-fragments.md) |
| Durable Markdown-view testing and migrations | [docs/csf-markdown-testing.md](docs/csf-markdown-testing.md) |
| Issue, branch, PR, review, and handoff lifecycle | [docs/github-issue-workflow.md](docs/github-issue-workflow.md) |
| Generated client plugins and skills | [docs/plugins.md](docs/plugins.md) |
| Public documentation indexing | [docs/context7.md](docs/context7.md) |
| Release preparation and signing | [docs/RELEASING.md](docs/RELEASING.md) |
| Agent evaluation method | [docs/agent-benchmarking.md](docs/agent-benchmarking.md) |
| Private evaluation operations | [docs/agent-benchmark-private-workspace.md](docs/agent-benchmark-private-workspace.md) |
| Public evaluation scenarios | [benchmarks/agent-eval/README.md](benchmarks/agent-eval/README.md) |
| Audience, topic, landing-page, and language ownership | [docs/catalog.v1.json](docs/catalog.v1.json) |

When two documents disagree, fix the non-canonical copy or replace it with a
link. Do not add a third summary. Proposed designs and private evidence stay in
their designated ignored workspace; public maintainer documents must remain
self-contained without linking to it.
