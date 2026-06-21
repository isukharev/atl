---
name: atl
description: Work with Confluence pages and Jira issues as local files using the atl CLI — mirror them to disk in native format, use them as grounding context, edit, and push changes back under a version gate. Use when the user mentions Confluence, a wiki page, Jira, a ticket/issue/epic, a spec or runbook that lives in Atlassian, JQL or CQL, or wants Atlassian content available locally to read or edit.
---

# Working with Confluence & Jira via `atl`

`atl` is a Git-style CLI that mirrors Confluence pages and Jira issues to local disk in their
**native formats** (Confluence Storage Format `.csf`; Jira wiki), so you can treat Atlassian
content as part of your file working set — read it with Read/Grep/Glob, edit it, and push it back.

This skill orients you. For the actual command flows, use the focused skills:
- **Confluence pages** (pull / edit `.csf` / validate / push under the version gate) → the
  `confluence` skill.
- **Jira issues** (search / pull / create / update / transition / comment / link) → the `jira`
  skill.
- **First-time install & config** (`atl` binary, auth, backend URLs, mirror dir) →
  run `/atl:setup`.

If `atl` is not installed (`command -v atl` fails), tell the user to run `/atl:setup` first.

## Mental model

Mirrored Atlassian content becomes local files you operate on like code: the bytes are the
substrate, edits are diffed and pushed deliberately, and concurrent remote changes are caught by a
version gate. This is what makes `atl` "AI-native" — the agent works the content with its normal
file tools instead of round-tripping every read/write through an API.

See [mental-model.md](reference/mental-model.md) for when to reach for `atl` vs the live Atlassian
MCP, and for the spec-driven "living doc" workflow where `atl` fits best.

## Two habits that matter most

1. **Search first, read narrow, edit precise.** Don't bulk-dump everything and grep it. Use
   `conf search` / `jira issue search` (CQL/JQL) to find the few relevant items, `pull` only those,
   read the rendered `.md` to locate, and open the raw substrate only for the thing you will edit.
2. **`push` is the one deliberate checkpoint.** The safe loop is: pull fresh → edit → validate →
   review a dry-run diff → push under the version gate. On a conflict, a human decides whether to
   re-pull or force — never auto-force.

The mirror lives **outside the user's code repository** by default (`~/.atl/<workspace>/`) so it is
fully greppable yet never committed into their project's git history. Full rules, the safe loop,
and how to search a two-root layout are in [workflow.md](reference/workflow.md).

## Reacting to results

`atl` prints JSON to stdout by default (use `-o text` only for a human view) and signals outcomes
through exit codes. Parse the JSON; map the exit code per [exit-codes.md](reference/exit-codes.md)
(e.g. `5` = version conflict → re-pull and reconcile before considering `--force`; `3` = auth →
`/atl:setup`).
