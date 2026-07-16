<!-- Generated from skills-src/atl/reference/mental-model.md — edit the source and run 'make gen-plugins'. -->
# `atl` mental model: when and where it fits

## What `atl` is for

`atl` mirrors Confluence/Jira to disk in native format and pushes edits back under an optimistic
version gate. Its sweet spot is work where content should be **part of the agent's file context**:

- **Grounding a coding task in a spec/runbook** that lives in Confluence.
- **Bulk edits** across many pages/issues (cheaper in tokens than per-item API calls).
- **Diff-before-push** review and an audit trail of exactly what changed.
- **Offline / scriptable** work: grep, edit, validate without a live connection.

## Durable CLI vs transient MCP

The atl plugin includes its own typed remote-read-only MCP surface for bounded
Jira/Confluence evidence. If the user also has the Atlassian (Rovo) MCP server,
treat all three routes as complementary:

- Use the atl CLI/mirror for durable files, Structure, export, offline
  diff/plan, attachments, scripts, and every guarded write.
- Use atl MCP for one real-time bounded read when its seven typed tools cover the
  task and content must not be persisted to disk.
- Use an independently configured Atlassian/Rovo MCP when its OAuth scope or a
  capability absent from atl is specifically required.

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

- A single real-time lookup with no edit → atl MCP, another approved MCP, or a
  one-off `conf page view` / `jira issue view` is lighter than standing up a mirror.
- Content you must never persist to disk → don't mirror it; read it transiently.
