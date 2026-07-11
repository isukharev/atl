<!-- Generated from skills-src/confluence/reference/metadata-comments.md — edit the source and run 'make gen-plugins'. -->
# Confluence metadata, lifecycle, and comments

Load this reference before a title/move/lifecycle/comment write. For a request
that also changes the body, finish and push the body first, then metadata, then
the comment.

## Guarded title update

Keep private title text out of argv:

```bash
atl conf page title set <id> --from-file <private-title-file>
atl conf page title set <id> --from-file <same-file> --apply \
  --expected-version <preview-version> \
  --expected-proposal-hash <preview-hash>
```

Review normalized title/version/hash. There is no force mode. The command
fresh-reads and preserves native body bytes. On `unknown`, inspect the live
page; never replay automatically. Re-pull after `applied` because mirror paths
can change.

## Guarded page move

```bash
atl conf page move <id> --parent <new-parent-id>
atl conf page move <id> --parent <same-parent-id> --apply \
  --expected-version <preview-version> \
  --expected-parent <preview-current-parent> \
  --expected-proposal-hash <preview-hash>
```

For a top-level source pass explicit `--expected-parent=`. The command
fresh-reads source/target, rejects hierarchy cycles and incomplete projections,
preserves body/title, and never replays. Re-pull after `applied`.

## Other lifecycle and metadata operations

```bash
atl conf page meta --id <id>
atl conf page history --id <id>
atl conf page open --id <id>
atl conf page copy --id <id> --title '<title>' [--space <KEY>] [--parent <id>]
atl conf page delete --id <id>
```

Create a page from the Markdown subset when possible:

```bash
atl conf page create --space <KEY> --title '<title>' [--parent <id>] --from-md body.md
```

Exit 8 names the first unconvertible block and creates nothing. Use a validated
CSF file via `--from-file` for constructs outside the subset. Markdown and CSF
inputs are mutually exclusive.

## Comments: validate, deduplicate, reconcile

Comments are non-idempotent POSTs. Prepare the CSF body in an owner-only private
file, not argv or a public workspace file:

```bash
atl conf validate <private-comment.csf>
atl conf comment list --id <page-id>
atl conf comment add --id <page-id> --from-file <private-comment.csf>
```

Before POST, inspect current comments for the intended content so retries do
not duplicate an earlier attempt. Also inspect stderr: if listing warns that it
hit the fetch cap, absence is unproven. Do not POST; use a complete live
inspection/narrower approved method or ask the user. Add comments last, after
body and metadata writes, so their text can refer to final state.

If POST fails ambiguously (transport error, timeout, throttling, or server
error), do not immediately retry. List comments again and reconcile by exact
normalized content plus author and creation window. If that listing is
truncated, reconciliation remains `unknown`. If a match exists in a complete
listing, report success/already present. If state remains uncertain, report
`unknown` and ask the user to inspect; never automate a replay.

Mirrored `comments.json/.md` are readonly context. The JSON keeps a plain `body`
fallback and optional native `body_storage`; Markdown renders native paragraphs,
lists, links, emphasis, and headings beneath each comment. Editing either file
never changes the server and must not be used as a write path. A fresh pull with
`--comments` refreshes them.
