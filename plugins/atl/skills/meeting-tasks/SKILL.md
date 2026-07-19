---
name: meeting-tasks
description: Turn meeting notes into reviewed Jira task proposals with atl. USE WHEN capturing meeting or retro action items as assigned issues. DO NOT USE WHEN the source is a specification, only a prose summary is wanted, or the task is an ordinary Jira operation.
---
<!-- Generated from skills-src/meeting-tasks/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Meeting notes → Jira tasks with `atl`

Parse the notes → present the extracted items → **get approval** → create.
Never create tickets before the user confirms the list. Command details live
in the `jira` and `confluence` skills.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `$setup` and stop.

## Workflow

### 1. Get the notes

Pasted text as-is, or use a guarded Confluence read:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf search --cql 'title ~ "<meeting title>"' --limit 5 # when id is unknown
atl conf page view <id> -o text
```

### 2. Extract action items

Signals: explicit markers ("AI:", "TODO", "Action:", checkboxes, "@name"),
assignment verbs ("X will…", "X to…", "ask X to…"), and decisions that imply
work ("agreed to migrate…"). Capture per item: the action as a verb phrase,
the assignee as written, a due date if stated, and one sentence of surrounding
context.

Skip pure decisions with no follow-up work and vague intentions ("we should
think about…") — but list what you skipped, so the user can promote an item
back.

### 3. Resolve assignees (Server/DC uses usernames)

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira user search 'Alex Doe'
```

Ambiguous or missing match → leave the task unassigned and flag it. Never
guess between two people.

### 4. Present for approval

A table: # / summary / assignee (or ⚠ unresolved) / due date / source line.
Ask for the target project key if unknown. Wait for confirmation or edits.

### 5. Create tasks — sequentially

```sh
atl jira issue create --project KEY --type Task --summary '<verb-first action>' --from-md item.md \
  --field 'assignee={"name":"<username>"}' --field duedate=<YYYY-MM-DD>
```

Description: Context (meeting name + date + the source lines) / What to do /
link to the notes page if there is one. A mid-run failure → **stop**, report
the keys created so far, ask how to proceed (`atl` never retries POSTs — do
not blind-retry either).

### 6. Summarize and backlink

List created keys grouped by assignee. Offer to leave a "created tasks"
comment on the source Confluence page — note the comment body is **CSF**, not
markdown:

```sh
printf '<p>Follow-ups: KEY-1, KEY-2, KEY-3</p>' | atl conf comment add --id <pageId> --from-file -
```

Only with explicit approval — it writes to a shared page.

## Pitfalls

| Symptom | Cause / fix |
|---|---|
| assignee rejected | must be the DC **username** from `user search`, not a display name or email |
| `duedate` rejected | the field may be off-screen for that project — drop it and note the date in the description |
| exit 8 on `--from-md` | unconvertible markdown block — the error names it; simplify |
