---
name: triage-issue
description: Triage a bug report or error message against Jira with the atl CLI — search for duplicates and prior fixes first, then create a well-structured ticket or comment on an existing one. USE WHEN the user pastes an error, stack trace, or bug description and asks "is this known", "is this a duplicate", "has this been reported/fixed before", or "file a bug for this".
---

# Triage an issue with `atl`

Find duplicates **before** filing. Never create a ticket without first showing
the user what already exists. Command details live in the `jira` skill.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run the setup skill and stop.

## Workflow

### 1. Extract the error signature

From the report, pull: the exception/error type, the failing
class/function/endpoint, and the stable message words. Strip volatile parts —
ids, timestamps, hostnames, line numbers — they poison text search. Build 2–3
query variants from specific to broad.

### 2. Search for duplicates (parallel, one message)

```sh
atl jira issue search --jql 'project = KEY AND text ~ "NullPointerException PaymentProcessor" AND type = Bug ORDER BY created DESC' --limit 10
atl jira issue search --jql 'project = KEY AND summary ~ "timeout checkout" ORDER BY updated DESC' --limit 10
```

Useful refinements:

- recent recurrence: `AND created >= -30d`
- fixed before (regression check):
  `AND status in (Done, Closed, Resolved) ORDER BY resolutiondate DESC`
- open neighbours: `AND status not in (Done, Closed) AND component = "X"`
- Quote multi-word phrases inside `text ~ "..."`; JQL reserved words must be
  quoted too.

### 3. Classify and present — no writes yet

Compare promising hits with `atl jira issue get KEY-123`, then tell the user
which case this is and what you propose:

- **Duplicate (open)** → offer to add a comment with the new occurrence and any
  new context.
- **Regression (was fixed)** → offer a new bug linked to the old one
  (`atl jira link-types` lists the instance's link names).
- **New** → offer to create a ticket.

Stop and wait for the user's choice.

### 4a. Comment on an existing issue

```sh
printf '%s' "$BODY_MD" | atl jira issue comment add KEY-123 --from-md -
```

### 4b. Create a new bug

Compose the description in markdown — Summary / Steps to reproduce / Expected /
Actual / Environment / error excerpt in a code fence / links to similar issues —
then:

```sh
atl jira issue create --project KEY --type Bug --summary '<Component>: <symptom> <condition>' --from-md desc.md
atl jira issue link add NEW-1 --to OLD-9 --type Relates     # when prior history exists
```

Title formula: component + observable symptom + triggering condition — not the
raw exception line.

### 5. Report back

Return the created/updated key, what was linked, and the duplicate-search
queries you ran, so a human can re-check the negative result.

## Pitfalls

| Symptom | Cause / fix |
|---|---|
| exit 8 on `create --from-md` | a markdown construct isn't convertible — the error names it; simplify that block |
| link add rejected | link type names are instance-specific — check `atl jira link-types` first |
| assignee rejected | Server/DC takes a **username**: find it with `atl jira user search '<name>'`, set via `--field 'assignee={"name":"<username>"}'` |
| `text ~` too noisy | add `type = Bug`, a component, or `created >= -90d` |
