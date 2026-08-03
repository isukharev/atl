# Read and change Confluence comments safely

ATL separates qualified comment reads from guarded writes. Begin with the
page-scoped read surface; choose a write route only after the exact thread,
page version, and backend capability are known.

## Read a qualified inventory

```sh
export ATL_READ_ONLY=1
atl conf comment list --id 12345
atl conf comment list --id 12345 \
  --location inline --state open --depth all
```

The default performs separate bounded page-child reads for footer, inline, and
resolved comments, then joins inline metadata to exact markers in the page's
native Storage Format. Location, resolution, thread relation, and anchor state
are independent evidence; ATL does not infer one from another.

Inspect these fields before claiming a discussion is absent or resolved:

- top-level `complete` and `partial_reasons`;
- `comments_complete`, `threads_complete`, and `anchors_complete`;
- per-comment `relation`, `location`, `resolution`, and `anchor.status`.

An empty `comments` array proves absence only when `complete:true`.

Bind a read to a page revision when another step depends on it:

```sh
atl conf comment list --id 12345 --expected-version 7
```

A changed page version refuses before comment reads. Even without that caller
gate, every selector and pagination request is bound to the reconciled page
version so comments are not joined to CSF from another revision.

## Read one exact thread

```sh
atl conf comment thread --id 12345 --comment-id 67890
```

The command returns the proven root subtree containing that id and reprojects
diagnostics/completeness to the selected discussion. A complete inventory that
proves the id absent exits `4`; a partial inventory that cannot prove absence
exits `8`. Relationship or ancestry is never guessed.

For an agent that does not need native bodies, the typed MCP list route returns
a minimized body-free inventory and the thread route returns only the selected
plain-text discussion. Carry the returned page-version provenance into later
reasoning. MCP fixes traversal at 32 pages, defaults to 100 items, and applies a
separate encoded-result bound; use the CLI when native CSF or the larger
page-scoped inventory is required.

## Keep comments with a mirror

```sh
atl conf pull --id 12345 --comments --into "$ATL_MIRROR_ROOT"
```

The mirror stores qualified schema-v2 comment evidence in `.comments.json`.
The main Markdown view renders a deterministic read-only thread tree. Native
comment bodies remain in JSON; the Markdown view is not a write substrate.

## Choose the narrow write surface

| Intent | Route | Capability boundary |
|---|---|---|
| Create one root footer comment | `conf comment preview` then guarded `add` | public page-comment REST |
| Create an inline comment | `conf comment mutation preview|apply` | exact activated Data Center provider |
| Reply, resolve, or reopen an inline thread | `conf comment mutation preview|apply` | exact activated Data Center provider |
| Agent/MCP read | `confluence_comment_list` or `confluence_comment_thread` | typed read-only only |

Footer creation starts with a read-only preview:

```sh
ATL_READ_ONLY=1 atl conf comment preview --id 12345 --from-file comment.csf
atl conf comment add --id 12345 --from-file comment.csf \
  --apply --expected-proposal-hash <reviewed-hash>
```

The native-CSF body is file-backed, bounded, validated, and proposal-bound.
Apply reconstructs the proposal, sends at most one POST, and reconciles by a
complete readback. `outcome_unknown` may have committed and must never be
replayed.

Inline create/reply/resolve/reopen additionally require an exact product pin
whose provider has passed remote qualification. Example:

```sh
atl conf comment mutation preview --id 12345 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0
atl conf comment mutation apply --id 12345 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0 --apply \
  --expected-proposal-hash <reviewed-hash>
```

ATL binds exact selection/body bytes, page and rendered geometry, actor,
complete comment evidence, and the pinned provider. It never writes inline
marker CSF itself, never retries or follows a redirect, and accepts success
only after strict readback. These writes are CLI-only; MCP intentionally exposes
no mutation tool.

## Stop safely

- Keep `ATL_READ_ONLY=1` for investigation and inventory commands.
- Do not disable the policy merely because a mutating-classified preview was
  refused; review the exact workflow first.
- Do not convert Markdown into a native comment body.
- Do not replay an ambiguous write result.
- Do not use a provider outside its exact activated product identity.

See [safe writes](safe-writes.md) for the common proposal/apply discipline. The
exhaustive [comment command reference](usage.md#atl-conf-comment-list) and
[output contract](OUTPUT_CONTRACT.md#confluence-pull-and-comments)
remain the canonical flag and wire-shape sources.
