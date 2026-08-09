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
If setup or mirror health is uncertain, run offline `atl doctor` before
identity-bearing status output. Use `atl doctor --remote` only when backend
metadata access is intended; it makes one single-attempt version GET per ready
service, with one additional bodyless Confluence reachability HEAD only after a
missing version route, and never reads content or identities. Treat emitted error-severity
`problems[]` as a stop signal even though the qualified report remains on
stdout.
For an explicitly requested version-pinned Data Center workflow, qualify its
separate fail-closed boundary with `atl compatibility status --remote`; never
infer support from ordinary `doctor`, a nearby patch, or an HTTP error shape.
Compatibility providers accept no arbitrary endpoint/header/payload overrides
and are not MCP routes.

For an unfamiliar Jira/Confluence task, query the exact offline route before
loading broad command references:

```bash
atl capabilities --task jira/evidence
atl capabilities --task jira/setup
atl capabilities --task jira/graph-evidence
atl capabilities --task confluence/comments
```

The closed task classes are `confluence/comments`, `confluence/edit`,
`confluence/evidence`, `confluence/mirror`, `confluence/table-analytics`,
`jira/batch-analysis`, `jira/board-portfolio`, `jira/edit`, `jira/evidence`,
`jira/graph-evidence`, `jira/mirror`, `jira/portfolio`, `jira/setup`,
`jira/structure-planning`, and `knowledge/search`. The result is a small ordered set
of stable capability ids with the real command path, backend access class,
supported output modes, evidence/completeness semantics, and one focused skill
reference. Load only the named focused skill/reference, then stop expanding the
route once sufficient complete evidence is available. Use exact filters only;
an unknown task/id is a loud not-found result, not a prompt for fuzzy guessing.
`capabilities` is local/offline and works without valid config or credentials.
The additive `confluence/comments` route keeps qualified list, exact thread,
guarded preview, and guarded add separate. Its list/thread entries have narrower
read-only MCP mappings; preview/add are CLI-only and do not become plugin
mutations.
For an exact Jira Structure id in the `jira/portfolio` route, use
`jira structure get` for metadata qualification before a bounded view. Retain
only id, name, and read-only state for the decision; do not propagate owner,
permission, saved-view, or forest transport payloads.
For Jira metadata discovery, use the ready `jira issue fields --metadata-only`
summary for classification, identifier-quality, and value-type aggregates
instead of recounting the returned field array.
For `confluence/table-analytics`, prefer the content-free `conf table summary`
discovery route before extracting a selected table. Its exact structural and
style cardinalities remove the need to recount content-bearing raw cells. Bind
an index selected from that summary to the same page revision with
`--expected-version`. On extraction, use `returned_table_count` and
`selection_reconciled` for selected-result cardinality; `table_count` remains
page-wide. Treat `cell_count_reconciled:false` as a hard evidence failure: the
source placement/span ledger did not agree with the expanded grid. A native
span above the supported cap fails before expansion and is not absence.
For durable Confluence mirror health, prefer the content-free `conf snapshot`
route before expanding individual pages with `conf diff`; use its reconciled
cardinalities instead of manually joining status, validation, and render rows.
Treat exit `8` during an active mirror mutation as a stop/retry signal; snapshot
coordinates through a shared advisory lock without creating or changing files.
For durable Jira mirror health, use the same content-free pattern with
`jira snapshot`; its exact baseline/raw-snapshot/pending/render buckets are the
preflight before identity-bearing `jira status` or issue-level repair.
Both services' status/snapshot accept `[DIR]` or `--into`, never both, and fail
with exit 4 before config/network unless the resolved root contains `.atl`.
For a single conflicted native object, route to `conf reconcile preview` or
`jira reconcile preview`; use the separately mutation-classified `stage` leaf
only when an external tool needs exact private base/theirs artifacts.

When the installed plugin exposes `atl` MCP tools, prefer them for transient,
bounded evidence reads: typed arguments remove shell construction and the
server registers no mutation or arbitrary-filesystem tool. Load
[mcp.md](reference/mcp.md) for its exact twenty-three-tool route and CLI fallback
boundary. Use bounded Structure metadata/view through MCP. For content-free
health counts of an existing durable mirror, use the no-argument mirror snapshot
tool only when the owner has configured `ATL_MIRROR_ROOT`. Continue using the
CLI for raw Structure forest/values, mirror content/status/diff, exports,
diff/plan, attachments, and every guarded write.

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
   Pull is non-destructive: local native or derived-view edits are preserved,
   clean siblings continue, and a blocked selection exits 8 with content-free
   `local_safety` evidence. Use `pull --dry-run` to inspect a refresh. Use
   `--stash-local` only for an intentional native reset that first retains exact
   bytes; `--overwrite-local` is the explicit discard path. Neither bypasses
   Markdown or baseline-integrity failures.
2. **`push` is the one deliberate checkpoint.** The Confluence safe loop is: pull fresh → edit → validate →
   inspect offline semantics with `conf diff` → review `conf push --dry-run` against current remote state → push under the version gate. On a conflict, a human decides whether to
   re-pull or force — never auto-force.
   Confluence comment append uses its own guarded loop: read-only `conf comment
   preview` → review the complete footer-root baseline-bound proposal → after
   explicit approval, one guarded `conf comment add` with `--apply` and its
   `--expected-proposal-hash`. Although `add` defaults to dry-run, it remains
   mutating-classified. Inline create/reply/resolve/reopen use the separate
   exact-pinned `conf comment mutation preview|apply` loop. Inline create takes
   exact native-CSF body and bounded UTF-8 selection files plus a zero-based
   occurrence; ATL binds the raw selection, applies the pinned-client
   normalization/masks fail-closed, derives raw-DOM geometry, and never edits
   marker CSF. Never replay
   `outcome_unknown`; the mutation surface remains CLI-only.

For every agent-created Bash block that must not mutate Jira, Confluence,
auth/config, or profile state, make this export its first statement:

```bash
export ATL_READ_ONLY=1
atl ...
atl ...
```

Before planning Jira or Confluence writes, run `atl policy show`; when it is
active, plan only within its effective `grants`.

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
`error`. Treat `remediation` and the closed schema-v1 `recovery` object as safe
guidance to present, never authorization to
retry a write or change policy automatically. Backend/API prose cannot set
these classification fields. `rate_limited` / `wait_before_retry` means bounded
replay-safe read retries were exhausted: do not immediately repeat the command
or tool call, and never retry a write automatically.
The shared transport never follows redirects for POST, PUT, PATCH, or DELETE;
a mutating 3xx is an error to reconcile at the original target, not permission
to replay the method or body at a server-selected path.
`output_limit_exceeded` / `narrow_or_raise_bound` means caller-selected
`max_bytes` rejected the complete encoded result; treat it as no evidence and
narrow the selection or deliberately choose a larger allowed bound.
For Confluence table tools, `not_found` /
`summarize_then_select_table` means the selected 1-based index is outside the
content-free table count. Summarize without a table selection, choose from that
inventory, then extract once; do not report the page as missing.
For `confluence_table_extract`, `check_failed` /
`reread_table_summary_then_retry_expected_version` means the page changed after
the table index was selected. Re-read the content-free table summary, select
the table again from that revision, and extract it once with the new exact
`expected_page_version`; do not retry the old positional selection against the
new revision.
For `confluence_page_section` or `confluence_page_sections`, `check_failed` or `not_found` /
`outline_then_select_section` means the occurrence selection is ambiguous or
stale. Refresh the content-free outline, choose the exact heading occurrence,
then read that section once; do not report the page or heading as missing.
`check_failed` / `reread_outline_then_retry_expected_version` means the
`expected_page_version` you supplied no longer matches the page; the message
carries only the expected and current integers. Re-read the outline, re-select
the occurrence there, and request the section once at the new version — do not
retry the previous selection against the new revision.
For `jira_structure_view`, `not_found` or `check_failed` /
`view_then_select_subtree` means the Structure exists but its stored-folder
selector did not resolve exactly. Read one selector-free bounded view with a
narrow field projection and `max_rows` sufficient for the full forest, choose
the folder `row_id`, and request that exact `folder_row` subtree once. Use the
CLI if the full forest does not fit the MCP caps; do not report the Structure
as missing or repeat the failed selector.
`check_failed` / `reread_structure_view_then_retry_expected_forest_version`
means the `expected_forest_signature`/`expected_forest_version` pair you supplied
does not match the current forest; the message carries only the expected and
current integers. Re-read the view, re-select the subtree from that fresh
result, and request it once with the new pair.
A returned pair with either member zero is non-bindable: omit both expected
inputs, keep the selection explicitly ungated, and report that limitation.

The recommended convention keeps the mirror **outside the user's code
repository** at `~/.atl/<workspace>/`, so it is fully greppable yet never
committed. The CLI uses that path only when the workspace exports
`ATL_MIRROR_ROOT` or passes `--into`; otherwise built-in fallbacks are `mirror`
(Confluence) and `mirror-jira` (Jira). Full rules are in
[workflow.md](reference/workflow.md).

Before remote work against an existing durable root, inspect its
content-minimized service bindings with `atl mirror backend status <ROOT>`.
Fresh service-empty pulls and explicitly registered creates bind automatically;
an unbound legacy root with service evidence requires the explicit local
preview/apply workflow. After human approval to cross the read-only policy
boundary, copy only the exact preview digest:

```bash
env -u ATL_READ_ONLY atl mirror backend bind <ROOT> --service jira
env -u ATL_READ_ONLY atl mirror backend bind <ROOT> --service jira \
  --apply --expected-backend-sha256 '<exact backend_sha256 from preview>' \
  --confirm BIND
```

The entire bind leaf, including write-free preview, is blocked by
`ATL_READ_ONLY=1`. Bind loads no PAT, makes no backend request, stores only a
tagged digest in private strict-v1 `.atl/backend-bindings.json`, and never
replaces a different binding. A mismatch means use the original backend or a
new mirror. Remote mirror status/snapshot/push/reconcile/plan phases fail before
network access when a required binding is missing or mismatched; offline mirror
work remains available. Persisted Confluence Jira-macro expansion requires a
separate Jira binding.

When creating a Confluence page, copying a page, or creating a Jira issue that
must immediately join a durable mirror, opt in explicitly with both `--register`
and `--into <ROOT>`. Omit both for legacy remote-only creation. Registration
performs one authoritative readback and saves mirror sync state only after the exact
native/base/view artifacts are present. If the remote object was created but
registration fails, retain its stdout id/key and exit 8 as evidence; never replay
the create/copy. Preserve local files and recover only that identity with
`conf pull --id <ID> --into <ROOT>` or
`jira pull --jql 'key = <KEY>' --limit 1 --into <ROOT>`.

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
  default, Markdown with `-o text`, or stable capability ids with `-o id`; JSON
  also states any bounded MCP route/scope or an explicit CLI-only boundary.
- `mcp serve --service jira|confluence|offline` — standalone closed read-only
  profiles; omission preserves the plugin's complete default inventory.

**Confluence additions:** typed read-only `render.confluence.page_fields` shared
by mirror and transient views; the `render.confluence.jira_macros=auto|off`
safety policy; guarded file-backed `conf page title set` and
review-bound `conf page move`; complete/guarded `conf page labels
list|add|remove`; safe same-origin page-reference/short-link resolution and
structural outline/bounded-section reads;
dedicated native `conf blog create` with strict CSF/Markdown validation;
explicit post-create `conf page create --register --into <ROOT>` and guarded
preview/apply `conf page copy [--register --into <ROOT>]` from one authoritative
readback;
opt-in ordered `conf pull --page-prefetch` plus a shared
`--requests-per-second` transport boundary for ordinary CQL/space and
complete/incremental mirrors
while every local write/checkpoint remains serial;
`conf page list --space [--status]`, `conf page
open --id`, guarded `conf page copy --id --title [--space] [--parent]
[--register --into] [--apply --expected-version --expected-proposal-hash]`, `conf attachment
{list,get,upload}`, guarded permanent `conf attachment delete --page-id --id
[--apply --confirm DELETE --expected-version --expected-proposal-hash]`, `conf me`, `conf search --space/--title/--label/--type`
convenience filters (no `--cql` needed), `.md` view renders internal links as `[[Title]]`.

**Presentation time:** `render.display_time_zone` is one IANA zone for human
Jira/Confluence Markdown dates (default `UTC`). It is independent of JQL/CQL,
does not alter exact JSON/native timestamps, and is recorded in view state for
deterministic offline render/apply.

**Jira additions:** typed `render.jira.field_views` (including opt-in editable
rich-text sections with explicit pending state) and opt-in `epic_children` views;
explicit post-create `jira issue create --register --into <ROOT>` from one
authoritative readback, with state committed last and no create replay on local
registration failure;
preview-first permanent issue deletion bound to immutable id, freshness,
complete permission-relative subtask evidence, and explicit cascade intent;
value-free metadata and compact named issue-field inspection; qualified, filterable issue
history with explicit completeness, deterministic cardinality/consistency summary
including separate missing/duplicate identity facts, a summary-only projection
without raw changelog rows (explicit false is rejected), and last-field-change
metadata;
transient multi-key export to artifact-only stdout; deterministic epic evidence
digest and standalone refs with reconciled per-kind/per-source aggregates;
schema-v2 bounded CLI and typed MCP work-artifact graph with exact
structured Jira traversal, optional CLI-only Confluence id/title resolution, typed
edges, mentions, budgets, frontier, metadata-reconciled fields, and qualified
per-node sources (including experimental issue properties), plus explicit
CLI and typed MCP fail-closed Jira Development project/commit/branch/MR
identities that never trigger GitLab requests; MCP requires explicit
`include_development:true`, omits Development-node URLs, and preserves the
stable request and output profile when omitted or false;
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
groups are `comment preview|add|list|delete` and `link add|list|delete`;
comment preview is GET-only, while add is dry-run by default and applies only
with its reviewed baseline-bound proposal hash. `issue transition preview` is
also GET-only; the parent transition command is dry-run by default and applies
the exact reviewed target/comment/`--field k=v` proposal only with its hash.
Conflict or unverifiable transition/comment outcomes are never replay-safe.

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
(e.g. `5` = version conflict → preserve the candidate and run reconcile preview before any refresh or `--force`; `7` = not
configured → run `$setup`; `3` = the server rejected the token → re-`auth login` with a valid
PAT).

## Version skew (plugin vs binary)

Treat a group name by itself as a help request; any unknown child or stray
positional token is a usage failure (exit 2), never successful help output.

The plugin and the `atl` binary version together: each release ships both under one number, the
binary self-updates within ~6h of a release, and the plugin updates when its version changes. If a
command **documented by these skills** fails as `unknown command`/`unknown flag` (exit 2), don't
improvise a workaround — suspect skew: run `atl version` and compare its `version` with the installed
plugin's version. Also retain the full `commit` and `build_state` in diagnostics; in a source
checkout, a different commit or `dirty` build can explain behavior that the release version alone
cannot. `unknown` provenance is valid for an unstamped build and is not proof of tampering. An older
binary catches up on its next run (self-update applies on the following
invocation). Refresh the lagging plugin with the platform-specific flow:

Run `codex plugin marketplace upgrade atl --json`. If it succeeds, run `codex plugin add atl@atl --json`. Then start a new Codex chat or CLI session before retrying.

Re-check the exact syntax with `--help` before retrying.

## When something went wrong

If `atl` itself caused real friction — repeated failures on one operation, a forced fallback, a
misleading error, an unexpected refusal — offer the user to report it (never report on your own):
with consent, a **sanitized public issue** in `isukharev/atl`, and/or with separate consent a
**detailed private case file** (`atl-feedback/<date>-<slug>.md`, kept out of VCS) that the user
can hand to their internal development team for reproduction and a fix. Triggers, both consent
gates, the redaction checklist, and both templates: [feedback.md](reference/feedback.md).
