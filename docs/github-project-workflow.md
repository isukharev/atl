# GitHub Project workflow

This document describes the development process for making agent work visible and
traceable in GitHub.

The goal is to preserve the chain:

```text
roadmap initiative -> issue -> agent plan -> branch -> PR -> verification -> done
```

Commits alone show what changed. Issues and Projects show why the change existed,
which roadmap item it served, who/what is working on it, and what remains blocked.

## Layers

- `PRODUCT-ROADMAP.md`: internal strategy, tradeoffs, anti-patterns, research.
- `ROADMAP.md`: public user-facing direction.
- GitHub Project `atl Roadmap`: execution board.
- GitHub issues: task context, acceptance criteria, decisions, agent plans.
- GitHub PRs: implementation, review, verification, and closing links.

## Project setup

Create or use a repository-linked GitHub Project named `atl Roadmap`.

Bootstrap after authenticating `gh`:

```sh
gh auth login
gh auth refresh -s project

gh project create --owner isukharev --title "atl Roadmap"
gh project link <project-number> --owner isukharev --repo atl
```

Create the search/automation labels used by issue forms and agent workflow:

```sh
gh label create roadmap --description "Roadmap-tracked work" --color 0E8A16
gh label create agent-ready --description "Ready for an AI coding agent" --color 5319E7
gh label create agent-working --description "An AI coding agent is working on this" --color 1D76DB
gh label create needs-human --description "Needs human decision or input" --color D93F0B

gh label create area/confluence --color 0052CC
gh label create area/jira --color 0052CC
gh label create area/sync --color 0052CC
gh label create area/mcp --color 0052CC
gh label create area/safety --color B60205
gh label create area/packaging --color 5319E7
gh label create area/cloud --color C5DEF5
gh label create area/docs --color 0E8A16

gh label create kind/feature --color A2EEEF
gh label create kind/bug --color D73A4A
gh label create kind/research --color FBCA04
gh label create kind/docs --color 0075CA
gh label create kind/infra --color BFDADC

gh label create roadmap/now --color 0E8A16
gh label create roadmap/next --color FBCA04
gh label create roadmap/later --color C5DEF5
```

Required fields:

| Field | Type | Values |
|---|---|---|
| `Status` | Single select | Inbox, Discovery, Ready, In progress, Review, Blocked, Done, Won't do |
| `Horizon` | Single select | Now, Next, Later, Exploring |
| `Quarter` | Single select or iteration | 2026-Q3, 2026-Q4, Unscheduled |
| `Area` | Single select | Confluence, Jira, Sync, MCP / agents, Safety, Packaging, Cloud / ADF, Docs |
| `Kind` | Single select | Feature, Bug, Research, Docs, Infra |
| `Confidence` | Single select | Committed, Likely, Exploring |
| `Impact` | Single select | High, Medium, Low |
| `Effort` | Single select | S, M, L, XL |
| `Roadmap ID` | Text | Internal roadmap ID such as B6 or F1 |
| `Agent state` | Single select | Needs human, Agent working, Human review, Agent blocked |

Suggested views:

- `Quarter Plan`: filter to the current quarter; group by parent issue or Area.
- `Agent Queue`: `agent-ready`, `agent-working`, or `Agent state != blank`.
- `Public Roadmap`: group by Horizon; hide sensitive/internal work.
- `Release`: group by milestone or target release.
- `Blocked`: status Blocked or label `needs-human`.

## Issue hierarchy

Use parent issues for quarterly initiatives:

- `Q3: Agent tier + read-only safety`
- `Q3: Sync at scale`
- `Q3: Zero-egress and packaging`

Use sub-issues for concrete deliverables. A sub-issue should have:

- problem statement;
- acceptance criteria;
- non-goals;
- roadmap ID;
- verification plan;
- links to related docs, prior art, or user reports.

## Agent task lifecycle

1. `Inbox`: task exists, not yet shaped.
2. `Discovery`: agent or human is refining scope and options.
3. `Ready`: acceptance criteria and constraints are clear.
4. `In progress`: branch exists or implementation has started.
5. `Review`: PR exists and awaits review/CI.
6. `Blocked`: human input or external state is required.
7. `Done`: merged, rejected with rationale, or intentionally closed.

For agent-driven work, the agent should comment before implementation:

```md
## Agent plan

Problem:

Approach:

Files likely to change:

Acceptance criteria:

Verification:

Risks / non-goals:
```

If the plan changes materially, the agent should add a short comment explaining
the change instead of silently drifting from the issue.

## GitHub CLI

The `gh project` commands require the `project` token scope:

```sh
gh auth status
gh auth refresh -s project
```

Common commands:

```sh
gh issue create --title "agent: ..." --label agent-ready --body-file /tmp/issue.md
gh issue develop <issue-number> --checkout
gh issue comment <issue-number> --body-file /tmp/agent-plan.md
gh pr create --draft --title "..." --body-file /tmp/pr.md --project "atl Roadmap"
```

Adding an issue or PR to the Project:

```sh
gh project item-add <project-number> \
  --owner <owner> \
  --url https://github.com/isukharev/atl/issues/<issue-number>
```

Updating Project fields requires IDs:

```sh
gh project view <project-number> --owner <owner> --format json
gh project field-list <project-number> --owner <owner> --format json
gh project item-list <project-number> --owner <owner> --format json
gh project item-edit \
  --id <item-id> \
  --project-id <project-id> \
  --field-id <field-id> \
  --single-select-option-id <option-id>
```

For repeat automation, wrap the field lookup in a small script after the Project
exists and field option IDs are known.

## PR requirements

Every non-trivial PR should include:

- linked issue (`Refs #...` or `Fixes #...`);
- parent initiative when one exists;
- roadmap ID or `ROADMAP.md` section;
- implementation summary;
- verification commands and results;
- docs/plugin updates when CLI behavior changes.

## What not to put in GitHub

Do not put these in issues, PRs, comments, screenshots, or logs:

- PATs, credentials, keys, or tokens;
- private hostnames;
- private page IDs, issue keys, or customer names;
- proprietary page bodies or Jira descriptions;
- security vulnerability details before following `SECURITY.md`.
