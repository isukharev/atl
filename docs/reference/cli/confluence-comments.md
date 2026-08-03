# Confluence comments

Qualified comment inventories, exact threads, previews, and guarded comment mutations.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl conf comment list`

List a schema-v2, evidence-qualified page comment inventory. The default makes
separate bounded page-scoped reads for footer, inline, and resolved comments at
all thread depths, then joins inline metadata to exact markers in the page's
native CSF. Location, resolution, root/reply relation, and anchor status remain
independent; missing backend evidence becomes `unknown` or a partial reason,
never an inferred value. An explicit backend `reopened` state is the semantic
open state and is emitted as `resolution:"open"`; unrecognized states remain
fail-closed as `unknown`.

```sh
atl conf comment list --id 12345678
atl conf comment list --id 12345678 --location inline --state open --depth all
atl conf comment list --id 12345678 --expected-version 7
```

`--location` is `all|footer|inline|resolved`, `--state` is
`all|open|resolved|unknown`, and `--depth` is `root|all`. A positive
`--expected-version` refuses before comment reads when the page revision has
changed. Regardless of that optional caller gate, every qualified selector and
pagination request is internally bound to the reconciled `page_version`.
Inspect `comments_complete`, `threads_complete`, `anchors_complete`,
`partial_reasons`, and per-comment `relation`, `location`, `resolution`, and
`anchor.status`. Anchors are root-thread evidence; a proven reply has a null
anchor and is qualified by explicit ancestry instead. An empty list proves
absence only when `complete:true`.

`-o text` emits the same qualification header followed by a deterministic
indented thread view. Native `body_storage` remains available only in JSON.
During the mirror-schema migration window, explicit `--legacy-flat` preserves
the old `{comments:[...]}` response; it cannot be combined with v2 filters or
the version gate.

## `atl conf comment thread`

Read the proven root subtree containing one exact numeric comment id:

```sh
atl conf comment thread --id 12345678 --comment-id 87654321
atl conf comment thread --id 12345678 --comment-id 87654321 --expected-version 7
```

If a complete inventory proves the id absent, the command exits 4. If the
inventory is partial and cannot prove absence, it exits 8 instead. A selected
comment whose root is unavailable is returned alone with
`threads_complete:false`; the relationship is not guessed.
Thread diagnostics, partial reasons, and completeness are projected again for
the selected root subtree. Global enumeration/transport failures remain, while
diagnostics and orphan markers proven to belong elsewhere on the page are
excluded.

To persist comments alongside the mirrored page instead of printing them, use
`conf pull --comments`. The schema-v2 `.comments.json` is the source evidence,
including completeness and closed diagnostics. The main v6 `.md` renders a
deterministic read-only thread tree with author/time, location/state, qualified
anchors, and an explicit unattached section when ancestry is unavailable or
inconsistent. Only a matched observed selection is labelled current; other
anchor states may show the original selection only as reported. The separate
`.comments.md` remains a best-effort flat compatibility projection.

## `atl conf comment preview|add`

Safely create one root footer comment from exact native CSF. `preview` is a
read-only command and works under `ATL_READ_ONLY=1`. `add` reconstructs the same
proposal but remains classified as mutating even when its default dry-run sends
no POST. Apply requires both `--apply` and the exact reviewed
`--expected-proposal-hash`:

```bash
ATL_READ_ONLY=1 atl conf comment preview --id 12345678 --from-file comment.csf
atl conf comment add --id 12345678 --from-file comment.csf # dry-run; mutating-classified
atl conf comment add --id 12345678 --from-file comment.csf \
  --apply --expected-proposal-hash <hash-from-preview>
```

This replaces the former immediate-write behavior: existing automation must
review a preview and pass both apply gates; invoking `add` without `--apply`
never writes.

The body must be non-empty valid UTF-8 and valid Confluence Storage Format,
with a maximum size of exactly 1 MiB (1,048,576 bytes). Accepted bytes are
preserved exactly; there is no Markdown conversion. The proposal binds the
backend, resolved page id and version, stable current-user id, exact body and
its length/hash, public-REST capability evidence, and a complete sorted
root-only footer-comment baseline and count. It fails closed if current-user
identity, page metadata, capability, comment completeness, thread completeness,
or unique root identities cannot be proven.

Apply rebuilds the proposal, checks the reviewed hash, revalidates it immediately
before one non-retried POST, then performs complete readback reconciliation.
`applied` means the returned identity matches one exact new record; `recovered`
means one exact new actor/body match proves a commit despite an unusable write
response. `conflict` and `not_applied` prove no accepted write from this attempt.
`outcome_unknown` means the POST may have committed: retain the result, inspect
fresh state, and never replay automatically. An identical existing body never
makes append idempotent.

This public-REST create surface supports footer root comments only. The
separate version-qualified mutation surface below handles existing inline
threads.

| flag | description |
|---|---|
| `--id` | page id or supported same-origin URL (required) |
| `--from-file` | exact native-CSF body file or `-` for stdin (default stdin) |
| `--apply` | `add` only: send one guarded POST (default is dry-run) |
| `--expected-proposal-hash` | `add` only: exact reviewed hash, required with `--apply` |

## `atl conf comment mutation preview|apply`

Create a new inline anchor, reply to an existing open inline thread, resolve it,
or reopen it through an
explicitly activated Data Center compatibility profile. The read-only preview
binds the exact page version, stable actor, complete qualified comment inventory,
private exact product activation, and operation inputs. Existing-thread
operations bind the exact root and resolution. `inline-create` additionally
binds the native page bytes, current server-owned marker inventory, canonical
server-rendered `wiki-content` fingerprint, exact file-backed UTF-8 selection,
zero-based occurrence, derived browser highlight geometry, and native-CSF body:

```bash
atl conf comment mutation preview --id 12345678 --thread-id 87654321 \
  --operation reply --from-file reply.csf
atl conf comment mutation apply --id 12345678 --thread-id 87654321 \
  --operation reply --from-file reply.csf --apply \
  --expected-proposal-hash <hash-from-preview>

atl conf comment mutation preview --id 12345678 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0
atl conf comment mutation apply --id 12345678 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0 --apply \
  --expected-proposal-hash <hash-from-preview>
```

`resolve` and `reopen` omit `--from-file`; `inline-create` omits `--thread-id`.
ATL hashes and binds the selection file bytes exactly, then reproduces the
pinned browser client's search normalization: non-breaking spaces become ASCII
spaces and only that client's edge-whitespace set is trimmed. Line feeds are
removed from the provider's `originalSelection`, while raw DOM text and UTF-16
geometry remain unchanged. The occurrence is zero-based in the normalized
rendered root before native exclusion masks are applied; ATL rejects an
occurrence hidden by those masks or inside a native footer-fallback region,
then reports the resulting provider `match_index`. This fail-closed behavior
also rejects layout-dependent floating table headers and DOM shapes the pinned
highlighter cannot traverse.

ATL reads the same server-rendered page shape used by the pinned client plugin,
but excludes volatile request-time and page chrome from reviewed proposal
evidence. Apply revalidates the entire proposal,
then the provider requalifies the owner-pinned exact product identity immediately
before one fixed write. For create, an immediate second preparation must preserve
the stable DOM/geometry evidence; only its fresh server request-time enters the
single POST. ATL never writes an inline marker into page CSF: Confluence owns the
marker, and complete readback must prove that the native body changed only by one
matching marker wrapper plus one exact root comment. The backend may retain the
page's public content version or advance it by exactly one; ATL accepts only
those two transitions and still requires any successfully decoded provider
response to agree with the observed version. An unusable response can become
`recovered` only when the same strict readback proves the exact result. The
provider never follows a redirect, retries, or falls back to an arbitrary
endpoint. Complete readback must prove the exact new root, reply, or state
transition; retain and inspect any `outcome_unknown` without replay. Resolving an
already resolved thread and reopening an open thread are explicit no-op previews.
The commands are JSON-only and are intentionally absent from MCP.

| flag | operation | description |
|---|---|---|
| `--id` | all | page id or supported same-origin URL (required) |
| `--operation` | all | `inline-create`, `reply`, `resolve`, or `reopen` |
| `--thread-id` | reply/resolve/reopen | exact root inline-thread id |
| `--from-file` | inline-create/reply | exact native-CSF comment body file or `-` |
| `--selection-file` | inline-create | bounded UTF-8 browser selection input or `-`; raw bytes are proposal-bound, search text is pinned-client normalized |
| `--occurrence` | inline-create | zero-based normalized occurrence before native masks (default `0`) |
| `--apply` | apply command | permit its single guarded write attempt |
| `--expected-proposal-hash` | apply command | exact reviewed preview hash |

---
