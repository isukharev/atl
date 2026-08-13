---
name: status-report
description: Draft an audience-oriented Jira status report with atl. USE WHEN the user wants a weekly/daily progress narrative, blocker summary, or period report. DO NOT USE WHEN the output is a visual dashboard, direct Jira query, knowledge search, or codebase summary.
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
  --status-field 'Delivery Notes' --dod-field 'Definition of Done' \
  --projection compact
```

Require `complete:true` for every named source requested by the report;
preserve every warning and staleness reason plus `projection.omitted` and
`projection.clipped`. Compact omits `children.list`, so when the report needs
issue-key traceability for individual child completions, owners, or risks,
expand only that evidence through the paginated IssueList contract:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue children PROJ-1 \
  --columns key,summary,status,assignee,priority,updated --limit 100
```

Follow `page.next_cursor` until `page.complete:true`, or label those details
partial. Expand a required clipped field through its focused bounded read; do
not repeat the digest in full output. For multiple epics, run one bounded
digest per key.

`issue children` cannot recover linked blockers. If
`projection.omitted` contains `link_summary.blockers[remaining]`, or the report
otherwise needs a complete direct-blocker inventory, read the root's bounded
qualified link topology instead:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue graph PROJ-1 --depth 0 --projection full --strict
```

Use only exact `jira_link` edges whose type and direction match the blocker
relationship required by the report. Require top-level `complete:true`, a
complete `issue_links` source for the root, and reconciled graph counts. Strict
exit 8 leaves qualified partial evidence on stdout, but does not satisfy this
workflow's complete-inventory gate.
Then fetch status and owner data for the selected blocker keys in one bounded
IssueList rather than broad per-issue reads:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue search --jql 'key in (BLOCK-1, BLOCK-2) ORDER BY key' \
  --columns key,summary,status,assignee,priority,updated --limit 2
```

Require `page.complete:true` and exact set equality between the selected
blocker keys and returned `rows[].key`. Preserve every missing key as
owner/status unavailable and label that evidence partial rather than silently
dropping it.

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
  --columns key,summary,status,updated --limit 100
atl jira issue search --jql 'project = KEY AND statusCategory != Done' \
  --columns key,status,assignee,priority,updated --limit 100
atl jira issue search --jql 'project = KEY AND priority in (Highest, High) AND statusCategory != Done ORDER BY priority DESC' \
  --columns key,summary,status,assignee,priority,updated --limit 100
atl jira issue search --jql 'project = KEY AND created >= -7d' \
  --columns key,summary,status,created --limit 100
```

Status names vary per instance ("Blocked" is often a flag, not a status). Check
returned values before building on them. For every IssueList inspect
`page.complete`, `page.truncated`, and `page.next_cursor`; paginate or state the
incomplete scope instead of treating a limit as absence.
Each bucket asks only for fields used by its decision; do not widen all four
projections to one superset or re-fetch an issue body merely because a key
appears in more than one bucket.

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
