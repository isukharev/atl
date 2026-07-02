# GitHub issue workflow

This document describes the development process for making agent work visible and
traceable in GitHub without depending on GitHub Projects.

The goal is to preserve the chain:

```text
roadmap initiative -> issue/sub-issue -> agent plan -> branch -> PR -> verification -> done
```

Commits alone show what changed. Issues show why the change existed, which
roadmap item it served, what decisions were made, and what remains blocked.

## Layers

- `ROADMAP.md`: public user-facing direction.
- GitHub parent issues: quarterly roadmap initiatives.
- GitHub sub-issues: concrete deliverables.
- GitHub issue comments: agent plans, decisions, blockers, and scope changes.
- GitHub PRs: implementation, review, verification, and closing links.

## Bootstrap

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

## Issue hierarchy

Use parent issues for quarterly initiatives:

- `Q3: Agent tier + read-only safety`
- `Q3: Sync at scale + safety`
- `Q3: Table-stakes Atlassian coverage`
- `Q3: Zero-egress packaging and trust`

Use sub-issues for concrete deliverables. A sub-issue should have:

- problem statement;
- acceptance criteria;
- non-goals;
- roadmap ID;
- verification plan;
- links to related docs, prior art, or user reports.

Create sub-issues with `gh`:

```sh
gh issue create \
  --parent <parent-issue-number> \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --label area/safety \
  --label kind/feature \
  --label roadmap/now \
  --body-file /tmp/issue.md
```

## Lightweight status

Status lives in labels and comments:

| State | Signal |
|---|---|
| Ready for agent | `agent-ready` |
| Agent working | `agent-working` |
| Needs human input | `needs-human` |
| In review | linked draft/ready PR |
| Done | PR merged with `Fixes #...`, or issue closed with rationale |

Useful queues:

```sh
gh issue list --label agent-ready --state open
gh issue list --label agent-working --state open
gh issue list --label needs-human --state open
gh issue list --label roadmap/now --state open
```

This avoids GitHub Projects' GraphQL-heavy custom field updates. The common
path uses issue/PR commands, labels, and comments, which are cheaper and simpler
for agent loops.

## Agent task lifecycle

1. `agent-ready`: task is shaped enough for an agent.
2. `agent-working`: branch exists or implementation has started.
3. Agent comments with plan before implementation.
4. Agent updates the issue if scope changes.
5. Draft PR exists for review/CI.
6. `needs-human`: human input or external state is required.
7. Done: merged, rejected with rationale, or intentionally closed.

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

Common commands:

```sh
gh issue create --title "agent: ..." --label agent-ready --body-file /tmp/issue.md
gh issue develop <issue-number> --checkout
gh issue comment <issue-number> --body-file /tmp/agent-plan.md
gh issue edit <issue-number> --add-label agent-working --remove-label agent-ready
gh pr create --draft --title "..." --body-file /tmp/pr.md
```

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
