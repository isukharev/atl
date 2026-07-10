# `atl` mental model: when and where it fits

## What `atl` is for

`atl` mirrors Confluence/Jira to disk in native format and pushes edits back under an optimistic
version gate. Its sweet spot is work where content should be **part of the agent's file context**:

- **Grounding a coding task in a spec/runbook** that lives in Confluence.
- **Bulk edits** across many pages/issues (cheaper in tokens than per-item API calls).
- **Diff-before-push** review and an audit trail of exactly what changed.
- **Offline / scriptable** work: grep, edit, validate without a live connection.

## `atl` vs the live Atlassian MCP

If the user also has the Atlassian (Rovo) MCP server, treat the two as **complementary**, not
competing:

| Use `atl` (local mirror) when… | Use the live Atlassian MCP when… |
|---|---|
| Editing one or many pages/issues and pushing back | Doing a single, real-time read ("what's the status of PROJ-12 right now?") |
| Grounding a large task in Atlassian content | You need the freshest possible value, no staleness window |
| You want a reviewable diff before writing | Per-user OAuth / permission-scoped access matters |
| Working offline or scripting a batch | One-off lookups where mirroring is overkill |

Rule of thumb: **`pull` right before you edit** to bound staleness; the version gate (Confluence)
and a fresh `get` before update (Jira) make a slightly-stale mirror safe to write from.

## Where `atl` fits in a development workflow (the living-spec bridge)

Spec-driven tools (Spec Kit, Kiro, etc.) assume the spec is markdown in the git repo. Many teams
instead keep the source of truth in Confluence/Jira. `atl` bridges that gap:

1. The spec/ticket lives in Confluence/Jira.
2. `atl` mirrors it natively to disk → the agent reads it as grounding context.
3. The agent implements the code in the repo.
4. The agent pushes doc/status updates back (a Confluence section, a Jira transition/comment)
   under the version gate — keeping the "living spec" in sync with the code that landed.

## When NOT to use `atl`

- A single real-time lookup with no edit → the live MCP or a one-off `conf page view` / `jira issue
  view` is lighter than standing up a mirror.
- Content you must never persist to disk → don't mirror it; read it transiently.
