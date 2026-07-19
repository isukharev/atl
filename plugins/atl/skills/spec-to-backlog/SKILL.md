---
name: spec-to-backlog
description: Convert a specification into a reviewed Jira epic and backlog with atl. USE WHEN decomposing requirements, RFCs, or designs into linked Jira work. DO NOT USE WHEN the source is meeting notes, only a summary is wanted, or the task is a direct service operation.
---
<!-- Generated from skills-src/spec-to-backlog/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Spec → backlog with `atl`

Read the spec → propose a breakdown → **get approval** → create the Epic
**first**, then children linked to it. `link-epic` needs the Epic to already
exist, so create it first and capture its key. Never create anything before
the user approves the breakdown. Command details live in the `jira` and `confluence`
skills.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `$setup` and stop.

## Workflow

### 1. Fetch the spec

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf page view <id> -o text
# Or, for a long/multi-section spec:
atl conf pull --id <id> --into <dir> # long/multi-section spec; then read .md
```

For a long or multi-section spec, use the second command and read the generated
`.md`.
Ask for the target Jira project key if not given, and confirm the instance's
issue types before planning (a failed `issue create` names the valid types;
many instances lack "Story").

### 2. Analyze and propose — no writes

Break the spec into 5–15 tickets. A good ticket is independently deliverable,
roughly 1–5 days of work, and testable. Split along architectural layers or
user-facing capabilities; add cross-cutting tickets (migration, docs, rollout)
only where the spec implies them.

Present: the proposed Epic name, a table of ticket summaries with type and
rough scope, and — explicitly — anything in the spec you chose **not** to turn
into a ticket. Wait for approval or edits.

### 3. Create the Epic first

```sh
atl jira issue create --project KEY --type Epic --summary '<epic name>' --from-md epic.md
```

Epic description: one-paragraph goal, a link back to the spec page, success
criteria. Capture the returned key — every child needs it.

### 4. Create children, link each to the Epic

Per ticket, sequentially (so a failure can't silently orphan half the batch):

```sh
atl jira issue create --project KEY --type Task --summary '<verb-first title>' --from-md t1.md
atl jira issue link-epic KEY-101 --epic KEY-100
```

Ticket description template: Context (1–2 sentences + spec section) / Scope /
Acceptance criteria (checklist) / Out of scope. Titles start with a verb:
"Add…", "Migrate…", "Expose…".

If a create fails mid-run: **stop**, report every key created so far, and ask
how to proceed. `atl` never retries POSTs (no double-create risk) — do not
retry blindly yourself either.

### 5. Summarize

Table of Epic + child keys/summaries, plus the source page id. Offer
follow-ups without performing them: assignees
(`atl jira user search '<name>'` → `--field 'assignee={"name":"…"}'` via
`issue update`), sprint placement (`atl jira sprint add`), priorities.

## Pitfalls

| Symptom | Cause / fix |
|---|---|
| exit 8 on `--from-md` | unconvertible markdown block — the error names it; keep bodies to plain headings/lists/fences |
| `link-epic` fails | Epic Link is a custom field on some Server/DC instances; report the error and suggest linking manually rather than guessing field ids |
| type rejected | verify types once before the batch, not per ticket |
