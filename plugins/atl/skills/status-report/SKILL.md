---
name: status-report
description: Generate a project status report from Jira with the atl CLI and optionally publish it to Confluence. USE WHEN the user asks for a status report, weekly or daily update, sprint summary, progress overview, blocker list, or wants a Jira-derived summary posted to a Confluence page.
---
<!-- Generated from skills-src/status-report/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Status report with `atl`

Query Jira → analyze → shape for the audience → optionally publish. This recipe
is interactive: confirm scope before querying, and **always ask before
publishing** — never silently create a Confluence page, and never silently skip
offering it. Command details live in the `jira` and `confluence` skills.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `$setup` and stop.

## Workflow

### 1. Confirm scope

Project key, time period (default: last 7 days), audience (team standup /
manager / executive), destination (chat only, or a Confluence space + parent
page). Skip anything the user already stated.

### 2. Query Jira — one bucket per command, in parallel

```sh
atl jira issue search --jql 'project = KEY AND status = Done AND resolved >= -7d' --limit 50
atl jira issue search --jql 'project = KEY AND status in ("In Progress", "In Review")' --limit 50
atl jira issue search --jql 'project = KEY AND priority in (Highest, High) AND status != Done ORDER BY priority DESC' --limit 50
atl jira issue search --jql 'project = KEY AND created >= -7d' --limit 50
```

Sprint-scoped variant: `atl jira sprint current --board <id>` →
`atl jira sprint issues <sprintId> --columns position,key,summary,status,assignee,priority`
(Agile API caps each call at 50 — paginate with `--cursor`).

Status names vary per instance ("Blocked" is often a flag, not a status) —
check the values coming back in the first result before building on them. If a
bucket hits `--limit`, either paginate or state the truncation in the report.

### 3. Analyze

Compute: done vs in-flight vs newly-created counts; notable completions;
blockers with owners; risks (open Highest/High untouched for a week:
`updated <= -7d`); unassigned open work. Report what *changed* over the
period, not raw issue lists.

### 4. Shape for the audience

- **Standup/team** — terse bullets: Done / In progress / Blocked + owner / Next.
- **Manager** — summary paragraph, then Highlights / Blockers & risks / Metrics
  (done vs opened) / Next period.
- **Executive** — 3–5 sentences, overall RAG status, decisions needed; issue
  keys only in a compact appendix table, not in the prose.

Every claim must trace to an issue key somewhere in the report.

### 5. Publish — only after an explicit yes

```sh
atl conf page create --space KEY --title 'Project X status — <date>' --parent <id> --from-md report.md
```

To refresh an existing report page, edit it through the mirror instead of
recreating: `atl conf pull --id <id> --into <dir>`, edit the `.md` view and
`atl conf apply` (or the `.csf` directly), `atl conf push`. Exit 8 from `--from-md` names the unconvertible markdown
block — simplify it (plain headings, lists, tables, fences convert cleanly).

### 6. Close the loop

Report the page id/title (or "not published"), the JQL used, and any
status/field names that didn't exist on the instance and were approximated.
