# `atl` documentation

Choose the task or audience lane that matches what you are doing. Focused
guides explain workflows and safety decisions; the large references are for
exact flags and output fields, not required first reading.

English is the canonical language for detailed documentation. The English and
Russian project READMEs are maintained as equivalent entry points and route to
the same canonical English guides. A Russian link label does not imply that the
linked guide is translated.

[Project overview](../README.md) · [Русский README](../README.ru.md)

<a id="first-use"></a>

## Start with a task

| Goal | Guide | Outcome |
|---|---|---|
| Install and prove one backend works | [Getting started](getting-started.md) | First bounded read and local mirror |
| Connect Claude Code or Codex | [Agent setup](agent-setup.md) | Focused skills and typed read-only MCP |
| Use repeatable Jira and Confluence workflows | [Agent recipes](agent-recipes.md) | Runnable task-oriented examples |
| Edit and publish safely | [Safe writes](safe-writes.md) | Reviewed native diff and guarded apply |
| Refresh, adopt, or recover a mirror | [Mirrors and recovery](mirrors-and-recovery.md) | Non-destructive refresh and three-way evidence |
| Trace Jira links, docs, and code identities | [Jira artifact graph](jira-artifact-graph.md) | Bounded relationship graph with completeness |
| Read or change Confluence discussions | [Confluence comments](confluence-comments.md) | Qualified threads and guarded mutations |
| Recover from an error | [Troubleshooting](troubleshooting.md) | Exit-code-first remediation |
| Rehearse guarantees without credentials | [Reproducible demos](demos/README.md) | Lossless edit, conflict refusal, and graph examples |

### Confluence task routes

| Task | Route |
|---|---|
| Read, create, move, copy, or trash a page | [Page lifecycle commands](reference/cli/confluence-pages.md#atl-conf-page-resolve) |
| Inspect or extract tables | [Table extraction](reference/cli/confluence-tables.md#atl-conf-table-extract) and [table summary](reference/cli/confluence-tables.md#atl-conf-table-summary) |
| List, download, upload, or delete attachments | [Attachment commands](reference/cli/confluence-pages.md#atl-conf-attachment-listgetuploaddelete) |
| Understand native storage and Markdown staging | [CSF and fragments](csf-and-fragments.md) |

### Jira task routes

| Task | Route |
|---|---|
| Read, create, edit, transition, or delete an issue | [Issue lifecycle commands](reference/cli/jira-issues.md#atl-jira-issue-get) |
| Plan fields and guarded writeback | [Jira guarded-writeback model](jira-guarded-writeback.md) |
| Inspect boards and sprints | [Planning commands](reference/cli/jira-planning.md#atl-jira-board-listgetconfigissuesbacklogviewexport-and-atl-jira-sprint-listgetcurrentissuesaddremove) |
| Read or export a Structure hierarchy | [Structure commands](reference/cli/jira-structure.md#atl-jira-structure-getviewforestrowsfoldersvaluespull-issuesexport) |

<a id="common-workflows"></a>

## Concepts

Read these when you need the model behind a workflow rather than one command.

| Document | Canonical topic |
|---|---|
| [CSF and fragments](csf-and-fragments.md) | Native Confluence bytes, versioned staging views, opaque content, and resolution |
| [Jira guarded writeback](jira-guarded-writeback.md) | Baselines, proposal hashes, drift checks, and fail-closed write behavior |
| [Project roadmap](../ROADMAP.md) | Product direction: shipped foundation, now, next, and later |

<a id="canonical-references"></a>

## Reference

| Reference | Use it for |
|---|---|
| [Command reference](reference/cli/README.md) | Every CLI command, flag, environment variable, and scripting pattern by concern |
| [Output contract](reference/output/README.md) | JSON/text/id schemas, exit classes, completeness, and recovery by concern |
| [MCP reference](mcp.md) | Typed read-only tools, profiles, bounds, and CLI fallback |
| [Changelog](../CHANGELOG.md) | User-visible changes by released version |
| `atl --help` | The command tree shipped by the installed binary |
| `atl capabilities` | Offline, versioned task-to-command/MCP routing |

Discover a route without config or credentials:

```sh
ATL_NO_UPDATE=1 atl --help
ATL_NO_UPDATE=1 atl jira --help
ATL_NO_UPDATE=1 atl conf --help
ATL_NO_UPDATE=1 atl capabilities --task jira/evidence
```

## Operations and security

| Document | Use it for |
|---|---|
| [Compatibility](compatibility.md) | Supported deployments, qualified evidence, and explicit provider pins |
| [Network egress](network-egress.md) | Runtime destinations, read-only/update controls, and air-gap boundaries |
| [Self-update](self-update.md) | Signed-update trust, disable controls, and maintainer signing boundary |
| [Security policy](../SECURITY.md) | Vulnerability reporting, trust guarantees, and release-key handling |

<a id="contributors-and-maintainers"></a>

## Maintainers

Start with [Contributing](../CONTRIBUTING.md). `AGENTS.md` is the binding
cross-agent repository contract; provider-specific files may add instructions
but do not replace it.

| Document | Canonical topic |
|---|---|
| [Repository standards map](../STANDARDS.md) | Authority and routing among maintainer documents |
| [Agent operating contract](../AGENTS.md) | Repository safety, ownership, workflow, and architecture invariants |
| [Claude Code overlay](../CLAUDE.md) | Claude-specific execution guidance subordinate to `AGENTS.md` |
| [Maintainer workflows](maintainers/README.md) | Preflight, verification, landing, recovery, and live-validation runbooks |
| [Architecture](architecture.md) | Hexagonal layers, dependency rules, and extension points |
| [Issue and PR workflow](github-issue-workflow.md) | Issue-first planning, linked branches, review, and handoff |
| [Generated plugins](plugins.md) | `skills-src` source of truth and Claude Code/Codex plugin generation |
| [Context7 indexing](context7.md) | Public documentation selection and refresh |
| [Release runbook](RELEASING.md) | Versioning, signing, publishing, and verification |
| [Durable-view testing](csf-markdown-testing.md) | Format-marker review, migration, corpus, and fuzz expectations |
| [Agent evaluation methodology](agent-benchmarking.md) | Deterministic scenarios and model-in-the-loop evidence |
| [Private evaluation operations](agent-benchmark-private-workspace.md) | Owner-controlled workspace lifecycle and publication boundary |
| [Public evaluation inventory](../benchmarks/agent-eval/README.md) | Committed scenario contracts, schemas, and synthetic fixtures |
| [Documentation catalog](catalog.v1.json) | Machine-readable audience, topic, landing, and language ownership |

## Support

Use [GitHub Issues](https://github.com/isukharev/atl/issues/new/choose) for
questions, feature requests, compatibility reports, and reproducible defects.
Security reports follow the [security policy](../SECURITY.md).

Public reports must be sanitized: no credentials, private hosts, object IDs,
titles/content, user identity, company data, or private local paths.
