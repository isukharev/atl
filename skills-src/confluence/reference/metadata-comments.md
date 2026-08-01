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

`page history` returns the qualified version listing `{schema_version, page_id,
count, complete, partial_reason?, versions:[...]}`. `versions` is always a JSON
array, so an empty `complete:true` result is proven absence, never `null`. A
`complete:false` listing names its limiter with a static `partial_reason`
(`page_limit`, `item_limit`, `pagination_stalled`, `legacy_unqualified`) and
never proves a version is missing — judge absence only on `complete:true`. The
`-o text` rows (`number<TAB>when<TAB>by[<TAB>message]`) are unchanged.
When typed atl MCP is available and only identity/version/update/access state
is needed, use `confluence_page_meta` instead. Its schema makes unknown
restriction state explicit and omits labels, ancestors, URLs, principals, and
the page body; use the CLI command above when those richer metadata fields are
actually required.

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
atl conf comment list --id <page-id> --location inline --state open --depth all
atl conf comment thread --id <page-id> --comment-id <comment-id>
ATL_READ_ONLY=1 atl conf comment preview --id <page-id> --from-file <private-comment.csf>
atl conf comment add --id <page-id> --from-file <private-comment.csf> --apply \
  --expected-proposal-hash <reviewed-hash>
```

The default list is schema v2 and reads footer, inline, and resolved comments
through separate bounded page-scoped queries. Inspect `comments_complete`,
`threads_complete`, `anchors_complete`, `partial_reasons`, and each comment's
independent `relation`, `location`, `resolution`, and `anchor.status`. An empty
array proves absence only when `complete:true`. Use `--expected-version` with a
previously observed page version when evidence must remain revision-bound.
`thread` returns one exact proven root subtree; partial absence is not reported
as not-found. `-o text` prints the qualification header and an indented tree;
JSON remains the machine contract and retains native `body_storage`.

For transient MCP evidence, call body-free `confluence_comment_list` with one
positive canonical page id, narrow item/encoded-byte bounds, and an
`expected_page_version` copied from prior evidence when applicable. Expand only
one returned positive canonical comment id with `confluence_comment_thread`.
The server fixes traversal at 32 comment pages. The thread tool returns bounded
plain text, not raw CSF. Require
`complete:true` before claiming absence; a successful partial list/thread never
proves an omitted comment or reply absent. Both projections omit inline
selection text, dedicated URL fields, email-like author identity, and arbitrary
backend error prose. Plain thread `body_text` remains untrusted user-authored
evidence and may contain ordinary links or email text. MCP has no preview/add tool and
cannot mutate comments.

`--legacy-flat` is only a temporary compatibility route for the old list
shape. Do not use it for new automation or combine it with v2 filters.

For creation, the body must be non-empty valid UTF-8/native CSF and no larger
than exactly 1 MiB; accepted bytes are preserved without Markdown conversion.
`preview` is read-only. `add` uses the same proposal and defaults to dry-run but
remains mutating-classified; it sends one POST only with `--apply` and the exact
reviewed hash. The proposal binds backend/page/version, stable actor, body,
capability, and a complete root-only footer baseline. Any partial inventory or
unavailable identity/capability fails before write.

After explicit approval, apply revalidates immediately, sends no more than one
POST, and reconciles from a complete root-only readback. Treat `applied` and
`recovered` as proven;
`outcome_unknown` may have committed and must never be replayed. Duplicate text
is not idempotent. Add comments last, after body and metadata writes. Creation
supports footer roots only—not replies, inline comments, or resolution changes.

Mirrored `.comments.json` is a strict schema-v2 qualified inventory; historical
flat arrays remain readable and migrate on the next successful comment pull.
The `.comments.md` file remains a flat readonly compatibility view in this stage. The JSON keeps a plain `body`
fallback and optional native `body_storage`; Markdown renders native paragraphs,
lists, links, emphasis, and headings beneath each comment. Editing either file
never changes the server and must not be used as a write path. A fresh pull with
`--comments` refreshes them.
