---
name: sprint-dashboard
description: Build a visual read-only Jira sprint or board dashboard with atl. USE WHEN the user wants a standup view, WIP picture, or sprint snapshot. DO NOT USE WHEN the output is a narrative report, raw Jira list, or codebase dashboard.
---
<!-- Generated from skills-src/sprint-dashboard/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Sprint dashboard with `atl`

A decision-support dashboard, not a prose report: its job is to show an
engineering manager or tech lead what needs attention. Strictly read-only —
never create, update, transition, assign, or comment from this recipe; offer
writes as follow-ups only. Command details live in the `jira` skill.

Make `export ATL_READ_ONLY=1` the first statement of every Bash block in this
recipe so every later `atl` call and child process inherits the guard. A prefix
on one `atl` command does not protect later commands in the block. Never
override the export in this workflow.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `/atl:setup` and stop.

## Workflow

### 1. Resolve scope

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira board list --project KEY          # find the board id
atl jira sprint current --board <id>       # exit 4 = no active sprint
```

On exit 4, offer the latest closed sprint (`atl jira sprint list --board <id>
--state closed`) or a plain JQL scope instead. If the user gave a project or
filter rather than a board, go straight to JQL.

### 2. Fetch the data

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira sprint issues <sprintId> --columns position,key,summary,status,assignee,priority,issuetype,updated
```

The Agile API caps each call at 50 — paginate with `--cursor` until exhausted.
JQL fallback:
`atl jira issue search --jql 'project = KEY AND sprint in openSprints()' --limit 100`.
If anything is still truncated, say so **on the dashboard** — never present a
partial picture as complete.

### 3. Compute locally (no further API calls)

- Status columns: To Do / In Progress / In Review / Done — counts and rows.
- **Attention signals**: in-progress items not updated for 2+ days; unassigned
  non-Done work; high-priority items not started; WIP concentration (one
  assignee holding several in-flight items).
- Progress: done vs total. This is issue-count math — label it as such unless
  story points were explicitly fetched (`--fields` with the instance's points
  field).

### 4. Render

Use the richest renderer the current client supports, keeping content
identical across formats:

1. An interactive HTML artifact, when the environment can host one.
2. Otherwise a self-contained `dashboard.html` file (inline CSS, no external
   assets) delivered to the user.
3. Otherwise markdown in chat.

Layout, in order: header (sprint name, dates, done/total progress bar);
a "needs attention" strip — each signal with issue key and one-line reason;
status columns with compact cards (key, truncated summary, assignee, priority
marker); per-assignee load table; appendix (board/sprint id or JQL, fetch time,
truncation notes). Link keys to `<jira-url>/browse/KEY` — the base URL comes
from `atl config show`.

### 5. Close the loop

After the dashboard, give a three-bullet text digest of the top attention
items, then offer — without performing — follow-up writes (assign, move,
comment).
