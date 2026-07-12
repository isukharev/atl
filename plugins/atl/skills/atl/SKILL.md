---
name: atl
description: Work with Confluence pages and Jira issues as local files using the atl CLI — mirror them to disk in native format, use them as grounding context, edit, and push changes back under a version gate. Use when the user mentions Confluence, a wiki page, Jira, a ticket/issue/epic, an agile board, sprint, Structure tree, a spec or runbook that lives in Atlassian, JQL or CQL, or wants Atlassian content available locally to read or edit.
---
<!-- Generated from skills-src/atl/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Working with Confluence & Jira via `atl`

`atl` is a Git-style CLI that mirrors Confluence pages and Jira issues to local disk in their
**native formats** (Confluence Storage Format `.csf`; Jira wiki), so you can treat Atlassian
content as part of your file working set — read it with Read/Grep/Glob, edit it, and push it back.

This skill orients you. For the actual command flows, use the focused skills:
- **Confluence pages** (pull / edit the `.md` view → `conf apply`, or the `.csf` directly / validate /
  push under the version gate) → the `confluence` skill.
- **Jira issues** (search / pull / create / update / transition / comment / link) → the `jira`
  skill.
- **First-time install & config** (`atl` binary, auth, backend URLs, mirror dir) →
  run `$setup`.
- **Optional workflow personalization** (consent-gated sample reads, team defaults, private
  structured profile) → explicitly invoke the `onboarding` skill. Runtime work should load only the
  relevant profile slice with `atl profile show --section ...`; for render memory use
  `--section render_defaults --service jira|confluence`. Never copy the profile into a repo.
  Repeated discoveries may become consent-gated `profile suggest` artifacts; they are reviewed and
  applied or rejected explicitly, never learned in the background. Saved `render_defaults` and
  `preferences.mirror_root` remain memory until a separately approved runtime sync; compare them
  with `atl config show` before relying on either.

If `atl` is not installed (`command -v atl` fails), tell the user to run `$setup` first.

## Mental model

Mirrored Atlassian content becomes local files you operate on like code: the bytes are the
substrate, edits are diffed and pushed deliberately, and concurrent remote changes are caught by a
version gate. This is what makes `atl` "AI-native" — the agent works the content with its normal
file tools instead of round-tripping every read/write through an API.

See [mental-model.md](reference/mental-model.md) for when to reach for `atl` vs the live Atlassian
MCP, and for the spec-driven "living doc" workflow where `atl` fits best.

**Working a ticket while coding?** [dev-loop.md](reference/dev-loop.md) is the end-to-end recipe:
take the ticket (`assign --me`, transition), keep it truthful while developing (progress comments,
description updates, links), close with evidence, and update the linked Confluence page under the
version gate.

## Two habits that matter most

1. **Search first, read narrow, edit precise.** Don't bulk-dump everything and grep it. Use
   `conf search` / `jira issue search` (CQL/JQL) to find the few relevant items. For a one-off read
   use `conf page view <ID> -o text` or `jira issue view <KEY> -o text` (configured Markdown, no
   mirror artifacts); `pull` only what needs
   editing or repeatable offline access,
   read the rendered `.md` to locate, and edit there (merge back with `conf apply` / `jira apply`),
   opening the raw substrate only for what the md surface can't express.
   Keep live reads slim too: `--fields` on Jira gets, `--columns` on Jira issue lists, `-o id` for piping, and a `| jq`
   projection when only a few values are needed — include Jira `attachment` when you need the
   presence/names of files, but avoid a bare `issue get` because it drags the whole comment thread
   into context.
2. **`push` is the one deliberate checkpoint.** The safe loop is: pull fresh → edit → validate →
   review a dry-run diff → push under the version gate. On a conflict, a human decides whether to
   re-pull or force — never auto-force.

For a session that must not mutate Jira, Confluence, auth/config, or profile
state, set `ATL_READ_ONLY=1` for the whole agent process (or pass global
`--read-only` on every call). Do not remove or override it inside the workflow.
Exit 8 with `policy:"read_only"` is a deliberate safety refusal, not a retry;
ask the human before changing the launcher/config policy. Pulls, views, status,
validation, and exports remain available.

For any JSON failure, branch on stable `kind` and numeric `code`, not words in
`error`. Treat `remediation` as safe guidance to present, never authorization to
retry a write or change policy automatically. Backend/API prose cannot set
these classification fields.

The recommended convention keeps the mirror **outside the user's code
repository** at `~/.atl/<workspace>/`, so it is fully greppable yet never
committed. The CLI uses that path only when the workspace exports
`ATL_MIRROR_ROOT` or passes `--into`; otherwise built-in fallbacks are `mirror`
(Confluence) and `mirror-jira` (Jira). Full rules are in
[workflow.md](reference/workflow.md).

## New capabilities (cloud-CLI parity)

Recent additions expand both surfaces — check the focused skills for full flag details:

**Global flags (all commands):**
- `--read-only` / `ATL_READ_ONLY=1` — fail closed on every mutating command before credentials/body/network access.
- `-o id` — print just the primary identifier(s) one per line (issue keys, page IDs) for safe piping into `xargs` or scripts. Not all commands support it; those that don't return an error.
- `--verbose` / `ATL_VERBOSE=1` — trace every HTTP request/response to stderr (token never logged).
- Shell completion for fixed-value flags (e.g. `--output`, `--format`, `--status`) is registered.
  Help and completion remain usable while global read-only policy is active.

**Confluence additions:** typed read-only `render.confluence.page_fields` shared
by mirror and transient views; the `render.confluence.jira_macros=auto|off`
safety policy; guarded file-backed `conf page title set` and
review-bound `conf page move`; complete/guarded `conf page labels
list|add|remove`; `conf page list --space [--status]`, `conf page
open --id`, `conf page copy --id --title [--space] [--parent]`, `conf attachment
{list,get,upload,delete}`, `conf me`, `conf search --space/--title/--label/--type`
convenience filters (no `--cql` needed), `.md` view renders internal links as `[[Title]]`.

**Jira additions:** typed `render.jira.field_views` (including opt-in editable
rich-text sections with explicit pending state) and opt-in `epic_children` views;
compact non-empty named issue-field inspection;
issue history/check/attachments/refs/tree; guarded link suggestions and
versioned plan apply; guarded file-backed custom-field preview/apply; labels;
complete/guarded watcher list/add/remove;
users, planning reports, and compact exports.
`jira export` manifests hash configured backend identity but retain
selectors/fields verbatim and may still be private. Boards/sprints use the Jira Software Agile API. Structure
commands read metadata, forests, rows, values, issue snapshots, and offline
exports, with `--root` subtree selection where supported. Breaking command
groups are `comment add|list|delete` and `link add|list|delete`; transitions can
set `--field k=v`.

**Local manifests:** `atl manifest create --root DIR [--service
jira|confluence|generic]` omits credentials and raw backend identity, but retains
file counts, selectors, fields, paths, ATL version, and URL hashes. Caller
metadata is not redacted: never put credentials in those flags, and review the
manifest before publishing.

## Reacting to results

`atl` prints JSON to stdout by default (use `-o text` only for a human view) and signals outcomes
through exit codes. Parse the JSON; map the exit code per [exit-codes.md](reference/exit-codes.md)
(e.g. `5` = version conflict → re-pull and reconcile before considering `--force`; `7` = not
configured → run `$setup`; `3` = the server rejected the token → re-`auth login` with a valid
PAT).

## Version skew (plugin vs binary)

The plugin and the `atl` binary version together: each release ships both under one number, the
binary self-updates within ~6h of a release, and the plugin updates when its version changes. If a
command **documented by these skills** fails as `unknown command`/`unknown flag` (exit 2), don't
improvise a workaround — suspect skew: run `atl version` and compare with the installed plugin's
version. An older binary catches up on its next run (self-update applies on the following
invocation); an older plugin updates with `codex plugin update atl`. Re-check the exact syntax
with `--help` before retrying.

## When something went wrong

If `atl` itself caused real friction — repeated failures on one operation, a forced fallback, a
misleading error, an unexpected refusal — offer the user to report it (never report on your own):
with consent, a **sanitized public issue** in `isukharev/atl`, and/or with separate consent a
**detailed private case file** (`atl-feedback/<date>-<slug>.md`, kept out of VCS) that the user
can hand to their internal development team for reproduction and a fix. Triggers, both consent
gates, the redaction checklist, and both templates: [feedback.md](reference/feedback.md).
