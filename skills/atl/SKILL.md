---
name: atl
description: Coordinate atl local mirrors across Jira and Confluence. USE WHEN orienting to atl, maintaining or recovering an existing mirror, or combining both services. DO NOT USE WHEN setup/onboarding, one service, a focused workflow, or codebase-only work is primary.
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
  run `/atl:setup`.
- **Optional workflow personalization** (consent-gated sample reads, team defaults, private
  structured profile) → explicitly invoke the `onboarding` skill. Runtime work should load only the
  relevant profile slice with `atl profile show --section ...`; for render memory use
  `--section render_defaults --service jira|confluence`. Never copy the profile into a repo.
  Repeated discoveries may become consent-gated `profile suggest` artifacts; they are reviewed and
  applied or rejected explicitly, never learned in the background. Saved `render_defaults` and
  `preferences.mirror_root` remain memory until a separately approved runtime sync; compare them
  with `atl config show` before relying on either.

If `atl` is not installed (`command -v atl` fails), tell the user to run `/atl:setup` first.

For an unfamiliar Jira/Confluence task, query the exact offline route before
loading broad command references:

```bash
atl capabilities --task jira/evidence
```

The closed task classes are `jira/evidence`, `jira/portfolio`,
`jira/board-portfolio`, `jira/batch-analysis`, `jira/structure-planning`, `jira/edit`, `confluence/evidence`,
`confluence/table-analytics`, `confluence/edit`, and `knowledge/search`. The result is a small ordered set
of stable capability ids with the real command path, backend access class,
supported output modes, evidence/completeness semantics, and one focused skill
reference. Load only the named focused skill/reference, then stop expanding the
route once sufficient complete evidence is available. Use exact filters only;
an unknown task/id is a loud not-found result, not a prompt for fuzzy guessing.
`capabilities` is local/offline and works without valid config or credentials.
For `confluence/table-analytics`, prefer the content-free `conf table summary`
discovery route before extracting a selected table.

When the installed plugin exposes `atl` MCP tools, prefer them for transient,
bounded evidence reads: typed arguments remove shell construction and the
server registers no mutation or filesystem tool. Load
[mcp.md](reference/mcp.md) for its exact nine-tool route and CLI fallback
boundary. Continue using the CLI for durable mirrors, Structure, exports,
diff/plan/status, attachments, and every guarded write.

## Mental model

Mirrored Atlassian content becomes local files you operate on like code: the bytes are the
substrate, edits are diffed and pushed deliberately, and concurrent remote changes are caught by a
version gate. This is what makes `atl` "AI-native" — the agent works the content with its normal
file tools instead of round-tripping every read/write through an API.

See [mental-model.md](reference/mental-model.md) for when to reach for transient typed reads vs
durable atl CLI mirrors, and for the spec-driven "living doc" workflow where `atl` fits best.

For a long multi-source analysis, [delegation.md](reference/delegation.md)
defines the optional one-child evidence pattern. Keep simple reads in the main
thread and never delegate remote writes.

**Working a ticket while coding?** [dev-loop.md](reference/dev-loop.md) is the end-to-end recipe:
take the ticket (`assign --me`, transition), keep it truthful while developing (progress comments,
description updates, links), close with evidence, and update the linked Confluence page under the
version gate.

## Two habits that matter most

1. **Search first, read narrow, edit precise.** Don't bulk-dump everything and grep it. Use
   `conf search` / `jira issue search` (CQL/JQL) to find the few relevant items.
   Require Confluence top-level `complete:true` and Jira `page.complete:true`
   before absence claims. Reuse a numeric Confluence search-result id directly
   for outline/section; resolve only URLs or unknown references. For a one-off read
   use `conf page view <ID> -o text` or `jira issue view <KEY> -o text` (configured Markdown, no
   mirror artifacts); `pull` only what needs
   editing or repeatable offline access,
   read the rendered `.md` to locate, and edit there (merge back with `conf apply` / `jira apply`),
   opening the raw substrate only for what the md surface can't express.
   Keep live reads slim too: `--fields` on Jira gets, `--columns` on Jira issue lists, `-o id` for piping, and a `| jq`
   projection when only a few values are needed — include Jira `attachment` when you need the
   presence/names of files, but avoid a bare `issue get` because it drags the whole comment thread
   into context.
2. **`push` is the one deliberate checkpoint.** The Confluence safe loop is: pull fresh → edit → validate →
   inspect offline semantics with `conf diff` → review `conf push --dry-run` against current remote state → push under the version gate. On a conflict, a human decides whether to
   re-pull or force — never auto-force.

For every agent-created Bash block that must not mutate Jira, Confluence,
auth/config, or profile state, make this export its first statement:

```bash
export ATL_READ_ONLY=1
atl ...
atl ...
```

Every later `atl` call and child process in that shell inherits the guard unless
it is explicitly overridden. A prefix such as `ATL_READ_ONLY=1 atl ...` protects
only that one process, so never use the one-command form for a multi-command
read-only workflow. Do not remove or override the exported guard inside the
workflow. Passing global `--read-only` on every call remains a more repetitive
alternative.
Exit 8 with `policy:"read_only"` is a deliberate safety refusal, not a retry;
ask the human before changing the launcher/config policy. Pulls, views, status,
validation, and exports remain available.

When date boundaries or timezone semantics matter, inspect them once explicitly
instead of inferring them from rendered timestamps:

```bash
export ATL_READ_ONLY=1
atl environment inspect
```

The command performs at most three sequential metadata GETs across configured
Jira/Confluence, never JQL/CQL/search/page reads, and never runs automatically
during incremental pull. Preserve its evidence labels: Jira server offset and
user timezone may be `observed`, JQL is an `assumed` mapping from the user zone,
CQL remains `unknown` unless a backend can prove it, and the Markdown display
zone is `configured` or `default`.

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
- `ATL_NO_UPDATE=1` — disable only the signed release check; it does not block
  Jira/Confluence reads. Homebrew launchers set it automatically and update
  through `brew upgrade atl`. For a true air gap, combine it with offline
  commands and an external network policy; `docs/network-egress.md` is the
  complete destination inventory.
- `-o id` — print just the primary identifier(s) one per line (issue keys, page IDs) for safe piping into `xargs` or scripts. Not all commands support it; those that don't return an error.
- `--verbose` / `ATL_VERBOSE=1` — trace every HTTP request/response to stderr (token never logged).
- Shell completion for fixed-value flags (e.g. `--output`, `--format`, `--status`) is registered.
  Help and completion remain usable while global read-only policy is active.
- `capabilities --task <exact-class>` — offline bounded task routing; JSON by
  default, Markdown with `-o text`, or stable capability ids with `-o id`.

**Confluence additions:** typed read-only `render.confluence.page_fields` shared
by mirror and transient views; the `render.confluence.jira_macros=auto|off`
safety policy; guarded file-backed `conf page title set` and
review-bound `conf page move`; complete/guarded `conf page labels
list|add|remove`; safe same-origin page-reference/short-link resolution and
structural outline/bounded-section reads;
dedicated native `conf blog create` with strict CSF/Markdown validation;
opt-in ordered `conf pull --page-prefetch` plus a shared
`--requests-per-second` transport boundary for complete/incremental mirrors
while every local write/checkpoint remains serial;
`conf page list --space [--status]`, `conf page
open --id`, `conf page copy --id --title [--space] [--parent]`, `conf attachment
{list,get,upload,delete}`, `conf me`, `conf search --space/--title/--label/--type`
convenience filters (no `--cql` needed), `.md` view renders internal links as `[[Title]]`.

**Presentation time:** `render.display_time_zone` is one IANA zone for human
Jira/Confluence Markdown dates (default `UTC`). It is independent of JQL/CQL,
does not alter exact JSON/native timestamps, and is recorded in view state for
deterministic offline render/apply.

**Jira additions:** typed `render.jira.field_views` (including opt-in editable
rich-text sections with explicit pending state) and opt-in `epic_children` views;
value-free metadata and compact named issue-field inspection; qualified, filterable issue
history with explicit completeness, deterministic cardinality/consistency summary, and
last-field-change metadata;
transient multi-key export to artifact-only stdout; deterministic epic evidence
digest and standalone refs with per-source completeness;
check/attachments/refs/tree. For a report
or quarter review, route through the Jira skill's one-hop
`reference/evidence-workflow.md` and stop once sufficient complete evidence is
available;
guarded link suggestions and
versioned plan apply; guarded file-backed custom-field preview/apply; labels;
complete/guarded watcher list/add/remove;
complete worklog reads and guarded single-entry time logging without write
retry (the `jira/edit` route exposes `jira.issue.worklog.list` and
`jira.issue.worklog.add`);
users, planning reports, and compact exports.
`jira export` manifests hash configured backend identity but retain
selectors/fields verbatim and may still be private. Explicit key/id exports preserve de-duplicated
selector order and omit missing identities; JQL exports retain backend order. Boards/sprints use the Jira Software Agile API. Structure
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

`atl` prints JSON to stdout by default. Use `-o text` only where the command
documents a human view; unsupported text requests fail with exit 2 and never
fall back to JSON. The CLI signals outcomes
through exit codes. Parse the JSON; map the exit code per [exit-codes.md](reference/exit-codes.md)
(e.g. `5` = version conflict → re-pull and reconcile before considering `--force`; `7` = not
configured → run `/atl:setup`; `3` = the server rejected the token → re-`auth login` with a valid
PAT).

## Version skew (plugin vs binary)

The plugin and the `atl` binary version together: each release ships both under one number, the
binary self-updates within ~6h of a release, and the plugin updates when its version changes. If a
command **documented by these skills** fails as `unknown command`/`unknown flag` (exit 2), don't
improvise a workaround — suspect skew: run `atl version` and compare its `version` with the installed
plugin's version. Also retain the full `commit` and `build_state` in diagnostics; in a source
checkout, a different commit or `dirty` build can explain behavior that the release version alone
cannot. `unknown` provenance is valid for an unstamped build and is not proof of tampering. An older
binary catches up on its next run (self-update applies on the following
invocation); an older plugin updates with `/plugin update atl`. Re-check the exact syntax
with `--help` before retrying.

## When something went wrong

If `atl` itself caused real friction — repeated failures on one operation, a forced fallback, a
misleading error, an unexpected refusal — offer the user to report it (never report on your own):
with consent, a **sanitized public issue** in `isukharev/atl`, and/or with separate consent a
**detailed private case file** (`atl-feedback/<date>-<slug>.md`, kept out of VCS) that the user
can hand to their internal development team for reproduction and a fix. Triggers, both consent
gates, the redaction checklist, and both templates: [feedback.md](reference/feedback.md).
