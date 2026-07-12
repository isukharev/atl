---
name: confluence
description: Pull, read, edit, validate, and push Confluence pages with the atl CLI in their native storage format (CSF). USE WHEN the user wants to read, search, summarize, edit, update, create, publish, copy, move, open, or delete a Confluence or wiki page; work with page metadata, comments, tables, attachments, space trees, or .csf/storage-format content.
---

# Confluence pages with `atl`

Use configured Markdown for reading and ordinary body edits. Keep native
Confluence Storage Format (CSF) as the write substrate. `atl` prints JSON by
default.
Durable document markers may use LF or CRLF; atl normalizes only that marker
line and never treats whole-document newline conversion as content-neutral.

## Preflight once per session

```bash
command -v atl >/dev/null || echo 'NOT INSTALLED → run {{atl.setup_cmd}}'
atl config show
```

If `atl` or `confluence_url`/auth is missing, run `{{atl.setup_cmd}}` and stop.
Exit 7 also means setup is incomplete.

When `ATL_READ_ONLY=1`, global `--read-only`, or config read-only policy is
active, keep to search/get/view/pull/render/status/validate/export operations.
Exit 8 with `policy:"read_only"` is a human-decision boundary; do not disable
the policy to apply/push/create/move/delete content.
For other failures, route on stable JSON `kind` and numeric `code`; present
`remediation` as guidance and do not infer actions from backend prose.

Resolve the mirror root before a mirror command. An explicit `--into` wins;
otherwise `ATL_MIRROR_ROOT` or nearest `.atl` root applies, with `mirror` as the
built-in fallback. If the user supplies an existing mirror file, its nearest
`.atl` root is authoritative for edit/apply/push. A different root requires a
fresh pull there.

For an existing mirror, do not pull merely because saved profile memory points
elsewhere. Run `atl conf status <existing-root> --remote` first. If it is locally
edited, preserve and reconcile that work before any pull. If it is clean but
remote-drifted, fresh-pull before editing. A clean, non-drifted mirror is already
a valid edit base.

If a workflow profile exists, load only:

```bash
atl profile show --section preferences
atl profile show --section render_defaults --service confluence
atl config show
```

Profile render/root values are memory, not active config. If saved and active
values differ, present the conflict and ask which wins. Obtain separate approval
before using a saved root or synchronizing render config. Expand `~` without
`eval`; pass the absolute path as one shell-quoted argument. A declined sync
stays declined. Never edit shell/workspace config implicitly.

## Choose the read mode

- One-off read/summarize with no editing or offline reuse: use
  `atl conf page view <id> -o text`. It writes nothing and has readonly markers.
- Editing, attachments/assets, comments, repeated/offline work, or exact CSF
  inspection: use a mirror. Fresh-pull only the needed page when creating that
  mirror or after the existing-mirror status gate says it is safe/needed.
- If a transient view turns into an edit request, discard it and pull fresh.

Find pages with a narrow CQL/filter, then:

```bash
atl conf pull --id <id> --assets --into <absolute-root>
# add --comments only when comment context is needed
```

Read [commands.md](reference/commands.md) for search/pull selectors, caps,
render profiles, output layout, status, bulk commands, and the full inventory.

## Gate the edit before changing files

1. Keep the selected mirror root fixed for the whole cycle.
2. On an existing mirror, check local/remote status and never pull over
   unreviewed local edits.
3. Require first line `<!-- atl:document confluence-page v3 -->` in `.md`.
4. If a pristine view is missing/older, render that exact file/root first. If it
   already has edits, save a private reviewed patch outside the derived view,
   render, then reapply it. For a future marker, update `atl`; never downgrade.
   A directory render preflights all selected markers before changing any view.
5. Treat generated metadata/comment regions as readonly. Use dedicated
   operations for metadata and comments.
   Generated `# Jira Queries` tables are readonly too; change the native macro
   or re-pull with `--jira-view`, never edit the table into page content.
   For an untrusted/heavy page use `--jira-macros off`, which retains the query
   placeholder but loads no Jira credential and performs no Jira search.
6. Choose one body surface for the cycle:
   - ordinary prose/headings/lists/code/simple tables → edit `.md`, then apply;
   - unsupported wrappers, ambiguous mentions, column/rowspan/colspan/nested-table
     restructuring, or byte surgery → edit `.csf` directly.

Never mix unapplied `.md` edits with direct `.csf` edits. Direct CSF changes
make apply refuse until push or re-pull.

## Body edit cycle

### Markdown path (preferred)

```bash
atl conf apply <page.md> --dry-run
atl conf apply <page.md>
atl conf validate <page.csf>
atl conf push --dry-run <page.csf>
atl conf push <page.csf>
```

For large body edits or any table merge, the apply dry-run is mandatory: review
the merge/loss report before writing CSF. After apply, require `wrote:true` and
`csf_ok:true`. Exit 8 means nothing was written; follow the named refusal rather
than forcing conversion. Review `removed_fragments` and `remote_drifted` in push
dry-run, then push the exact bytes reviewed.

Untouched blocks and table styling keep their exact CSF bytes. Opaque markers
(`⟦…⟧`, Jira/page links, mentions) retain identity; do not edit marker text.
Editing generated metadata or Comments is refused.

### Complex CSF/table path

For a mixed simple-body plus unsupported-table request, choose one complete plan
before editing:

- apply/validate/push the supported Markdown body, fresh-pull, then run a
  separate direct-CSF table cycle; or
- perform the whole combined change in one reviewed direct-CSF cycle.

Never direct-edit CSF while expecting remaining Markdown edits to apply in that
cycle. Read [csf.md](reference/csf.md) for byte-safe editing and fragment rules,
[csf-authoring.md](reference/csf-authoring.md) for validated constructs, and
[tables-attachments.md](reference/tables-attachments.md) for table/attachment
workflows.

## Order multi-surface changes

Use this order so each later operation sees fresh identity/version state:

1. **Body**: finish one apply/validate/dry-run/push cycle.
2. **Metadata**: fresh-read, preview, then apply guarded title/move operations;
   re-pull after `applied` because title/move may change mirror paths. Re-pull
   relocates only a pristine id-matched page; on local edits or a path collision,
   preserve both copies and follow exit-8 recovery instead of deleting dirs.
3. **Comment**: add last from a private validated CSF file. List existing
   comments before POST to avoid duplicates; a truncation warning blocks the
   write. If the outcome is ambiguous, list again and reconcile by
   content/author/time; a truncated reconciliation stays unknown. Never blindly
   retry a comment.

Read [metadata-comments.md](reference/metadata-comments.md) before title, move,
create/copy/delete, metadata/history, or comment writes.

## Push and concurrency safety

On exit 5, remote version moved: re-pull and reconcile. Never auto-`--force`;
use it only after a human decides to overwrite remote changes. Read
[push.md](reference/push.md) for the version gate and outcome checklist.

Pull/render/apply/push and mirror-local `conf edit` share a persistent
per-mirror mutation lock. If exit 8 says another mutation is active, wait;
never delete the lock or retry concurrently. Status and directory push also
fail closed on missing/corrupt sidecars. Jira and Confluence also share a
short-lived state lock when they use the same root, so never bypass a
state-lock refusal after its bounded retry window. Repair or re-pull; never
treat a partial scan or a missing
native-body projection as clean.

## Route details only when needed

- Search/pull/render/status/output shapes, caps, command inventory, bulk/table
  export: [commands.md](reference/commands.md)
- Title/move/create/copy/delete, metadata/history, comments and dedupe:
  [metadata-comments.md](reference/metadata-comments.md)
- Attachments and simple/complex table handling:
  [tables-attachments.md](reference/tables-attachments.md)
- Exit codes and recovery: [errors.md](reference/errors.md)
- Direct CSF editing/fragments/assets: [csf.md](reference/csf.md)
- New CSF page/section/comment constructs:
  [csf-authoring.md](reference/csf-authoring.md)
- Version-gated push and conflict outcomes: [push.md](reference/push.md)

## Hard rules

- Prefer `.md` + apply; use direct CSF only where Markdown cannot represent the
  requested change.
- Validate, review dry-runs, and push the exact bytes reviewed.
- Never edit `.meta.json`, `.atl` state, generated metadata, or comment sidecars.
- Never auto-force, auto-replay an `unknown` write, or retry a non-idempotent
  comment/upload without reconciliation.
- Tool friction that costs real turns should be offered through the `atl`
  skill's consent-gated feedback flow, with public details sanitized.
