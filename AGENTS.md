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

The workflow is intentionally issue-first and does not depend on GitHub Projects.
Issues, parent/sub-issues, labels, comments, linked branches, and PR links provide
enough traceability without heavy GraphQL usage.

### Standard flow

1. Find or create a GitHub issue for the task.
2. Link it to a parent roadmap or quarterly initiative issue when one exists.
3. Add labels for area/kind/roadmap horizon and agent state.
4. Comment with the agent plan before editing code.
5. Create or use a linked branch.
6. Implement the change.
7. Open a PR that references the issue and includes verification.
8. Use PR review/CI and issue comments as the visible status trail.
9. Close the issue through a PR (`Fixes #...`) or explicit maintainer decision.

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

Check authentication:

```sh
gh auth status
```

Create an issue:

```sh
gh issue create \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --label area/safety \
  --label kind/feature \
  --body-file /tmp/issue.md
```

Create a sub-issue under a parent initiative:

```sh
gh issue create \
  --parent <parent-issue-number> \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --body-file /tmp/issue.md
```

Create a linked branch for an issue:

```sh
gh issue develop <issue-number> --checkout
```

Post or update the agent plan:

```sh
gh issue comment <issue-number> --body-file /tmp/agent-plan.md
```

Update issue state with labels instead of Project fields:

```sh
gh issue edit <issue-number> --add-label agent-working
gh issue edit <issue-number> --remove-label agent-ready
gh issue edit <issue-number> --add-label needs-human
```

Create a PR:

```sh
gh pr create \
  --draft \
  --title "feat: add global read-only policy" \
  --body-file /tmp/pr.md
```

### Labels

Use labels for search, queueing, and lightweight automation.

- `area/confluence`, `area/jira`, `area/sync`, `area/mcp`, `area/safety`,
  `area/packaging`, `area/cloud`, `area/docs`
- `kind/feature`, `kind/bug`, `kind/research`, `kind/docs`, `kind/infra`
- `agent-ready`, `agent-working`, `needs-human`
- `roadmap/now`, `roadmap/next`, `roadmap/later`

Suggested issue searches:

```sh
gh issue list --label agent-ready --state open
gh issue list --label agent-working --state open
gh issue list --label needs-human --state open
gh issue list --label roadmap/now --state open
```

## Agent handoff rules

- Do not start broad implementation work from chat-only context when an issue is
  expected; create or update the issue first.
- Keep the issue updated when scope changes.
- If blocked, comment with the blocker and add `needs-human`.
- Do not close an issue just because a local patch exists. Close it through a PR
  (`Fixes #...`) or explicit maintainer decision.
- Never put secrets, PATs, private hostnames, private page IDs, or proprietary
  page content in issues, PRs, commits, or logs.
- Internal planning and strategy docs live in the gitignored `local-docs/`
  directory. This is a **public** repository: never commit that content (or the
  files) to it, move them out of `local-docs/`, or name/reference them from public
  docs, issue forms, PRs, or commit messages. Internal roadmap IDs stay internal —
  reference the public `ROADMAP.md` instead.
- Follow `CLAUDE.md` for code architecture, output contracts, write-path safety,
  plugin/docs synchronization, and test expectations.
