# Agent workflow for atl

This file is the cross-agent operating guide for work in this repository. Use
`CLAUDE.md` for detailed codebase architecture, commands, invariants, and test
rules. This file focuses on task tracking and handoff.

## GitHub tracking

Non-trivial work should be visible in GitHub before code changes start. A task is
non-trivial when it changes user-facing behavior, public docs, CLI output, release
process, architecture, security posture, or more than one small implementation
detail.

Trivial typo fixes, mechanical formatting, local experiments, and explicitly
private/security-sensitive work may skip public issue creation.

### Standard flow

1. Find or create a GitHub issue for the task.
2. Add the issue to the `atl Roadmap` GitHub Project.
3. Link it to a parent roadmap or quarterly initiative issue when one exists.
4. Comment with the agent plan before editing code.
5. Move Project status to `In progress`.
6. Create or use a linked branch.
7. Implement the change.
8. Open a PR that references the issue and includes verification.
9. Move Project status to `Review`.
10. After merge, rely on PR automation or update the issue/Project to `Done`.

Recommended issue comment before implementation:

```md
## Agent plan

Problem:

Approach:

Files likely to change:

Acceptance criteria:

Verification:

Risks / non-goals:
```

Recommended PR body links:

```md
Refs #<issue>
Parent: #<initiative>
Roadmap: <ID or ROADMAP.md section>
```

### GitHub CLI commands

Check authentication and Project scope:

```sh
gh auth status
gh auth refresh -s project
```

Create an issue:

```sh
gh issue create \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --body-file /tmp/issue.md
```

Create a linked branch for an issue:

```sh
gh issue develop <issue-number> --checkout
```

Add an item to the roadmap Project:

```sh
gh project item-add <project-number> \
  --owner <owner> \
  --url https://github.com/isukharev/atl/issues/<issue-number>
```

Inspect Project fields and items:

```sh
gh project field-list <project-number> --owner <owner> --format json
gh project item-list <project-number> --owner <owner> --format json
```

Update a Project field:

```sh
gh project item-edit \
  --id <item-id> \
  --project-id <project-id> \
  --field-id <field-id> \
  --single-select-option-id <option-id>
```

Create a PR:

```sh
gh pr create \
  --draft \
  --title "feat: add global read-only policy" \
  --body-file /tmp/pr.md \
  --project "atl Roadmap"
```

### Project fields

Use these fields in the `atl Roadmap` Project:

- `Status`: Inbox, Discovery, Ready, In progress, Review, Blocked, Done, Won't do
- `Horizon`: Now, Next, Later, Exploring
- `Quarter`: 2026-Q3, 2026-Q4, Unscheduled
- `Area`: Confluence, Jira, Sync, MCP / agents, Safety, Packaging, Cloud / ADF, Docs
- `Kind`: Feature, Bug, Research, Docs, Infra
- `Confidence`: Committed, Likely, Exploring
- `Impact`: High, Medium, Low
- `Effort`: S, M, L, XL
- `Roadmap ID`: text field, e.g. B6, B4, F1
- `Agent state`: Needs human, Agent working, Human review, Agent blocked

Suggested views:

- `Quarter Plan`: current quarter, grouped by parent issue or Area.
- `Agent Queue`: agent-ready or agent-working items.
- `Public Roadmap`: Now / Next / Later items safe for users.
- `Release`: grouped by milestone or target release.
- `Blocked`: all blocked items.

### Labels

Use labels for search and automation, not as a duplicate Project database.

- `area/confluence`, `area/jira`, `area/sync`, `area/mcp`, `area/safety`,
  `area/packaging`, `area/cloud`, `area/docs`
- `kind/feature`, `kind/bug`, `kind/research`, `kind/docs`, `kind/infra`
- `agent-ready`, `agent-working`, `needs-human`
- `roadmap/now`, `roadmap/next`, `roadmap/later`

## Agent handoff rules

- Do not start broad implementation work from chat-only context when an issue is
  expected; create or update the issue first.
- Keep the issue updated when scope changes.
- If blocked, comment with the blocker, set Project status to `Blocked`, and
  add `needs-human`.
- Do not close an issue just because a local patch exists. Close it through a PR
  (`Fixes #...`) or explicit maintainer decision.
- Never put secrets, PATs, private hostnames, private page IDs, or proprietary
  page content in issues, PRs, commits, or logs.
- Follow `CLAUDE.md` for code architecture, output contracts, write-path safety,
  plugin/docs synchronization, and test expectations.
