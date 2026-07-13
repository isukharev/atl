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
can change. Relocation requires a pristine id-matched old CSF/Markdown pair and
an unoccupied destination; preserve both paths on exit 8 and never recursively
delete the old directory.

## Guarded page move

```bash
atl conf page move <id> --parent <new-parent-id>
atl conf page move <id> --parent <same-parent-id> --apply \
  --expected-version <preview-version> \
  --expected-parent <preview-current-parent> \
  --expected-proposal-hash <preview-hash>
```

For a top-level source pass explicit `--expected-parent=`. The schema-v2 hash
binds the target version; apply fresh-reads source/target, then re-reads target
identity/version/hierarchy immediately before PUT. It rejects changed targets,
cycles, and incomplete projections, preserves body/title, and never replays.
The second read narrows but cannot eliminate the backend's two-page race.
Re-pull after `applied`.

## Other lifecycle and metadata operations

```bash
atl conf page meta --id <id-or-same-origin-url>
atl conf page history --id <id>
atl conf page open --id <id>
atl conf page copy --id <id> --title '<title>' [--space <KEY>] [--parent <id>]
atl conf page delete --id <id>
```

Add `-o text` for compact metadata/version records. Unknown restriction state
is printed as `restricted unknown`, not as unrestricted.

Create a page from the Markdown subset when possible:

```bash
atl conf page create --space <KEY> --title '<title>' [--parent <id>] --from-md body.md
```

Exit 8 names the first unconvertible block and creates nothing. Use a validated
CSF file via `--from-file` for constructs outside the subset. Markdown and CSF
inputs are mutually exclusive.

Create a native blog post through the dedicated content-type-safe command:

```bash
atl conf blog create --space <KEY> --title '<title>' --from-md body.md
```

Creation requires exact returned type, space key, title, positive version, and
body presence. Data Center does not document a safe case/Unicode/whitespace
equivalence for those identity fields, so a normalized response is `unknown`.
Do not replay an ambiguous create; inspect the target space or ask the user.

There is no page parent. The body must be non-empty. Raw CSF is preserved;
Markdown uses the same fail-closed subset as page creation. Treat an
unverifiable response as `unknown` and never replay the POST automatically.

## Comments: validate, deduplicate, reconcile

Comments are non-idempotent POSTs. Prepare the CSF body in an owner-only private
file, not argv or a public workspace file:

```bash
atl conf validate <private-comment.csf>
atl conf comment list --id <page-id>
atl conf comment add --id <page-id> --from-file <private-comment.csf>
```

For review, `-o text` prints each stable comment id, author/time, and plain
body; JSON remains the machine contract.

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
