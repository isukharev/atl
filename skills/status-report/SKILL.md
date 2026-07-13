---
name: status-report
description: Generate a project status report from Jira with the atl CLI and optionally publish it to Confluence. USE WHEN the user asks for a status report, weekly or daily update, sprint summary, progress overview, blocker list, or wants a Jira-derived summary posted to a Confluence page.
---
<!-- Generated from skills-src/status-report/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Status report with `atl`

Query Jira → analyze → shape for the audience → optionally publish. Confirm the
scope before querying and **always ask before publishing**. Command details live
in the `jira` and `confluence` skills.

Make `export ATL_READ_ONLY=1` the first statement of every read-only Bash block
so every later atl call and child process inherits the guard. Publishing is a
separate explicitly approved block that removes the guard for one command.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `/atl:setup` and stop.

## Workflow

### 1. Confirm scope

Ask only for missing information: scope kind (one/more epics, sprint/board, or
whole project), key/id, time period (default: last 7 days), audience (team /
manager / executive), and destination (chat only or Confluence).

### 2. Discover unfamiliar fields once

Do this only when the project uses unknown narrative, DoD, or risk fields. The
inventory contains no values; choose promising exact names/ids before reading
content.

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue fields PROJ-1 --metadata-only
```

### 3. Choose the narrowest evidence path

For one or a few epics, prefer the aggregate evidence contract. Pass selected
field names only when discovery found them:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira epic digest PROJ-1 --since 2026-07-01 --until 2026-07-07 \
  --status-field 'Delivery Notes' --dod-field 'Definition of Done'
```

Require top-level and named `sources.*.complete`; preserve every warning and
staleness reason. For multiple epics, run one bounded digest per key.

For a sprint, resolve it and page the shared IssueList projection:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira sprint current --board <id>
atl jira sprint issues <sprint-id> \
  --columns position,key,summary,status,assignee,priority,updated --limit 50
```

Follow `page.next_cursor` until `page.complete:true`, or label the report
partial. For a whole project, use explicit server-side buckets; do not fetch
full issue bodies for counting:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue search --jql 'project = KEY AND statusCategory = Done AND resolved >= -7d' \
  --columns key,summary,status,assignee,priority,updated --limit 100
atl jira issue search --jql 'project = KEY AND statusCategory != Done' \
  --columns key,summary,status,assignee,priority,updated --limit 100
atl jira issue search --jql 'project = KEY AND priority in (Highest, High) AND statusCategory != Done ORDER BY priority DESC' \
  --columns key,summary,status,assignee,priority,updated --limit 100
atl jira issue search --jql 'project = KEY AND created >= -7d' \
  --columns key,summary,status,assignee,priority,created --limit 100
```

Status names vary per instance ("Blocked" is often a flag, not a status). Check
returned values before building on them. For every IssueList inspect
`page.complete`, `page.truncated`, and `page.next_cursor`; paginate or state the
incomplete scope instead of treating a limit as absence.

### 4. Analyze

Compute done vs in-flight vs newly-created counts, notable completions,
blockers with owners, stale high-priority risks, and unassigned work. Report
what changed over the period, not raw issue lists. Separate observed facts from
interpretation; never convert an incomplete source into zero or green status.

### 5. Shape for the audience

- **Standup/team** — terse Done / In progress / Blocked + owner / Next.
- **Manager** — summary, Highlights / Blockers & risks / Metrics / Next period.
- **Executive** — 3–5 sentences, overall RAG status, decisions needed; issue
  keys in a compact appendix rather than prose.

Every claim must trace to an issue key. Add an Evidence quality line naming
incomplete/truncated sources and queries.

### 6. Publish — only after an explicit yes

```sh
env -u ATL_READ_ONLY atl conf page create --space KEY \
  --title 'Project X status — <date>' --parent <id> --from-md report.md
```

To refresh an existing page, pull it, edit the derived view, preview/apply the
local merge, then use guarded push. Never publish merely because the user asked
for a report; require a separate explicit yes for the destination and content.

### 7. Close the loop

Report the page id/title (or "not published"), JQL/digest periods used, source
completeness, and any status/field names that did not exist and were
approximated.
