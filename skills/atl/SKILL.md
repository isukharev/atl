---
name: atl
description: Work with Confluence pages and Jira issues as local files using the atl CLI — mirror them to disk in native format, use them as grounding context, edit, and push changes back under a version gate. Use when the user mentions Confluence, a wiki page, Jira, a ticket/issue/epic, an agile board, sprint, Structure tree, a spec or runbook that lives in Atlassian, JQL or CQL, or wants Atlassian content available locally to read or edit.
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
fully greppable yet never committed into their project's git history. A workspace can fix one
location by exporting `ATL_MIRROR_ROOT`, which becomes the default for `conf pull` / `conf status` /
`jira pull` (an explicit `--into` still wins). Full rules, the safe loop, and how to search a
two-root layout are in [workflow.md](reference/workflow.md).

## New capabilities (cloud-CLI parity)

Recent additions expand both surfaces — check the focused skills for full flag details:

**Global flags (all commands):**
- `-o id` — print just the primary identifier(s) one per line (issue keys, page IDs) for safe piping into `xargs` or scripts. Not all commands support it; those that don't return an error.
- `--verbose` / `ATL_VERBOSE=1` — trace every HTTP request/response to stderr (token never logged).
- Shell completion for fixed-value flags (e.g. `--output`, `--format`, `--status`) is registered.

**Confluence additions:** `conf page list --space [--status]`, `conf page open --id`, `conf page copy --id --title [--space] [--parent]`, `conf attachment {list,get,upload,delete}`, `conf me`, `conf search --space/--title/--label/--type` convenience filters (no `--cql` needed), `.md` view renders internal links as `[[Title]]`.

**Jira additions:** `jira issue history <KEY>`, `jira issue check <KEY> [--require] [--warn]` (non-zero exit if a required field is empty — good as a CI gate), `jira issue refs [KEY|--jql ...]` for artifact links, `jira issue tree --jql ... --epic-field ...` for standalone epic/child grouping, `jira issue link suggest --csv ...` for read-only missing-link candidates, `jira issue plan apply --csv ...` for guarded dry-run/apply plans, `jira issue delete <KEY> --force` (permanent on DC), `jira issue labels <KEY> --add/--remove`, `jira me`, `jira user search <q>` / `jira user get <username>`, `jira planning report` for deterministic planning quality gaps/refs/epic children, and `jira export --jql ... --out FILE --format jsonl|json|csv` for compact analysis artifacts with sanitized manifests, generated `--ids/--keys` batches, and `jira export diff`. **Boards & sprints** (Jira Software only, via the DC Agile API): `jira board {list,get}` and `jira sprint {list,get,current,issues,add,remove}` — addressed by numeric id; `board list --project` discovers board ids, `sprint current --board ID` gives the active sprint, `sprint add/remove` move issues into a sprint or back to the backlog. **Structure** (Tempo Structure plugin): `jira structure {get,forest,rows,values,pull-issues,export}` reads metadata, raw forests, parsed row hierarchies, selected row values, referenced issue snapshots, and offline tree artifacts; `rows`, `pull-issues`, and `export` accept `--root` for a first matching subtree. **Breaking renames:** `comment add|list|delete` (was `comment <KEY>`), `link add|list|delete` (was `link <KEY>`). `jira issue transition` now accepts `--field k=v` to set fields on the transition.

**Local manifests:** `atl manifest create --root DIR [--service jira|confluence|generic]` writes a sanitized local mirror/snapshot manifest with file counts, selectors, fields, ATL version, and backend URL hashes only.

## Reacting to results

`atl` prints JSON to stdout by default (use `-o text` only for a human view) and signals outcomes
through exit codes. Parse the JSON; map the exit code per [exit-codes.md](reference/exit-codes.md)
(e.g. `5` = version conflict → re-pull and reconcile before considering `--force`; `7` = not
configured → run `/atl:setup`; `3` = the server rejected the token → re-`auth login` with a valid
PAT).
