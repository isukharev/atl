# Jira mirrors

Pull, inspect, reconcile, apply Markdown staging, and guarded native wiki push.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [`atl jira pull`](#atl-jira-pull)
- [`atl jira status`](#atl-jira-status)
- [`atl jira snapshot`](#atl-jira-snapshot)
- [`atl jira reconcile preview` / `atl jira reconcile stage`](#atl-jira-reconcile-preview--atl-jira-reconcile-stage)
- [`atl jira apply`](#atl-jira-apply)
- [`atl jira push`](#atl-jira-push)
<!-- reference-navigation:end -->

## `atl jira pull`

Export issues matching a JQL query to disk. Each issue becomes three files:
`<KEY>.wiki` (the native Jira wiki body, stored byte-for-byte — the editable
source of truth, the Jira analog of a Confluence `.csf`), `<KEY>.md` (a
derived Markdown staging view rendered from the wiki, regenerated best-effort on every
pull), and `<KEY>.json` (identity plus raw Jira fields). Edit the generated `# Description`
and any explicitly editable rich-text field sections in the `.md` view, then merge/stage
them with `jira apply` (the recommended cycle, below), or
edit the `.wiki` directly for what the md view can't express — a bare `.md` edit
never reaches the server on its own.

```bash
atl jira pull --jql "project=PROJ and sprint in openSprints()" \
  --into my-jira-mirror --limit 200
atl jira pull --jql "project=PROJ" --fields customfield_10001,customfield_10002
# also mirror each issue's image attachments and link them from the .md
atl jira pull --jql "project=PROJ and status=Open" --assets
# inspect what a refresh would do without changing the mirror
atl jira pull --jql "project=PROJ" --into my-jira-mirror --dry-run
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--into` | output root directory (default `mirror-jira`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to include in JSON snapshots; core fields needed for rendering are always included |
| `--assets` | also download each issue's image attachments into a per-issue `<KEY>.assets/` directory and link them from the `.md` (opt-in; off by default) |
| `--dry-run` | select and qualify issues without writing mirror files, state, or stashes |
| `--overwrite-local` | explicitly replace a qualified locally edited native `.wiki`; never bypasses derived-view or baseline-integrity failures |
| `--stash-local` | preserve qualified local `.wiki` bytes in immutable `.atl/stash/` storage before replacement; mutually exclusive with `--overwrite-local` |
| `--render-profile` | `.md` view profile: `minimal` \| `default` \| `full` (see [Render profiles](rendering.md#render-profiles)) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |

Under `full`, `pull` widens its API `fields=` projection to cover the active
profile's sections, so no extra per-issue fetch is needed. The pull result JSON is
unchanged by the profile.

With `--assets`, image attachments (media type `image/*`) are streamed into
`<KEY>.assets/<attachment-id>-<filename>` and referenced from a generated
`# Image Attachments` section in the `.md`, placed between the description and
the links. The attachment id prefix keeps duplicate filenames distinct.
Download is best-effort: a failed image is skipped (counted in `assets_skipped`
and reported via a single stderr warning), the issue is still written, and only
images that landed on disk are linked. Attachments with an empty or
`application/octet-stream` media type are skipped (same as `jira issue images`).
The raw `<KEY>.json` snapshot is unchanged — it never carries local paths.

Like Confluence pull, Jira pull is non-destructive by default. It preserves and
reports local `.wiki` edits and unapplied/unsupported `.md` views, continues
refreshing clean siblings, then exits `8` when anything was blocked. Explicit
overwrite/stash recovery is limited to a native edit with intact sidecar/base
evidence. Use `jira apply` or `jira push` for intentional work; use
`--stash-local` when discarding the working native bytes only after retaining an
exact content-addressed copy.

Output layout:

```
mirror-jira/
  PROJ/
    PROJ-1.wiki             # native Jira wiki body, verbatim — the editable source
    PROJ-1.md               # derived staging view; edit supported sections, then jira apply
    PROJ-1.json
    PROJ-1.assets/          # only with --assets, when the issue has images
      10001-screenshot.png
    PROJ-2.wiki
    PROJ-2.md
    PROJ-2.json
```

The `.md` is a lossy, best-effort staging view (headings, emphasis, `{code}`/
`{quote}`/`{panel}`, lists, tables, links, `!image!` embeds, `{color}`,
`[~mentions]`); a render failure degrades that one section to a stub comment and
never fails the pull. To change supported content, edit generated `# Description` and/or
an opt-in editable rich-text field section in the `<KEY>.md` view and run `jira apply` (block-level,
non-lossy — the recommended loop just below), or edit `<KEY>.wiki` directly for what
the md view can't express (a bare `.md` edit never pushes on its own). Either way,
`jira push` is the only path to the server, and
`jira issue update --from-file <KEY>.wiki` remains the one-shot alternative.

The pull also records the `.wiki` body in the mirror sidecar (`.atl/state.json`)
plus a pristine base copy (`.atl/base/<KEY>.wiki`), which `jira status` and
`jira push` use to detect local edits and remote drift. Mirrors pulled by an
older `atl` have no sidecar entry: those issues read as never-synced (and are
not pushable) until re-pulled.

`<KEY>.json` shape:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "fields": {
    "summary": "Issue summary",
    "customfield_10001": "custom value"
  }
}
```

## `atl jira status`

Report which mirrored issues have local `.wiki` edits or pending editable-field
updates and, with `--remote`, which have drifted on the server since their bases
were captured. Content-hash based, the
Jira analog of `conf status`.

```bash
atl jira status                     # env, nearest .atl, then mirror-jira fallback
atl jira status my-jira-mirror
atl jira status --into my-jira-mirror
atl jira status --remote            # remote drift via bounded qualified batches
```

Each entry carries `locally_edited` (the `.wiki` differs from the pulled base or
at least one field is pending), optional `pending_fields`,
`synced` (`false` for a `.wiki` with no sidecar entry — never pulled through the
sidecar, so `locally_edited` + `synced:false` means "never-synced"), and, with
`--remote`, `remote_drifted` (the remote description or a pending field differs
from its stored base), optional `field_drifted`, or `remote_error` (the remote could not be checked — an uncheckable issue
is never reported in-sync). Drift needs a baseline: an issue with no base copy is
never reported drifted.

With one eligible issue, `--remote` keeps the exact issue endpoint. Larger
selections use completeness-qualified batches of at most 100 keys and 16 KiB
of escaped selector input. Each batch gets one transport attempt and must
return every requested key exactly once, case-insensitively, with a unique
canonical positive numeric issue id and an explicit Description projection. A
typed
error, partial page, omitted/duplicate/unexpected row, or malformed projection
makes that whole batch unavailable without per-issue fallback or permission
inference. Accepted rows are projected back to canonical local order.

Status accepts either positional `[DIR]` or `--into`, never both. With neither,
it uses `ATL_MIRROR_ROOT`, the nearest initialized `.atl` from the current
directory, then `mirror-jira`. An absent or uninitialized root returns exit 4
before config or network access.

## `atl jira snapshot`

Return exact, content-free health cardinalities for a durable Jira mirror. The
offline default needs no backend URL, PAT, or config and performs no
transaction recovery, filesystem write, or network request. It takes a shared
advisory lock only when the persistent mutation lock already exists. An active
mutation fails closed with exit `8` before inspection; a legacy mirror without
that lock is re-inspected if a current writer creates it during the first read.

```bash
ATL_READ_ONLY=1 atl jira snapshot
ATL_READ_ONLY=1 atl jira snapshot my-jira-mirror
ATL_READ_ONLY=1 atl jira snapshot --into my-jira-mirror
ATL_READ_ONLY=1 atl jira snapshot my-jira-mirror --remote
```

The JSON partitions local clean/edited and canonical tracked/untracked issue
substrates (including tracked-but-removed entries), wiki baseline presence and integrity, sibling raw-snapshot
presence/readability/key binding, pending-record validity/binding, and derived
view marker state. Current, known legacy, missing-marker, unsupported/future,
missing, and unreadable Markdown views remain distinct. The aggregate emits no
issue key/id, path, hash, field id, diagnostic text, wiki/raw-snapshot content,
or derived-view bytes.

`complete` means every inspected byte source needed for a trustworthy snapshot
was readable, internally valid, and stably bound; it does not mean the mirror
is clean. `reconciled` means every documented partition adds up exactly. A
baseline mismatch, malformed/misbound raw snapshot, invalid/unbound pending
record, active pending transaction, or unreadable source returns the qualified
snapshot with exit `8`. Missing optional/legacy evidence remains an explicit
count rather than silently reading as present. If writing that snapshot to
stdout fails, the write failure is reported together with the inspection
failure and the exit code stays the inspection code. If inspection otherwise
succeeds, the write failure is returned on its own with generic exit `1`.

`--remote` first completes that local preflight before loading backend config or
credentials. A failed preflight makes zero requests. Otherwise one eligible
canonical tracked issue keeps its exact GET. Larger selections use the same
qualified 100-key / 16 KiB batches as remote status, with one single-attempt
request per batch and no per-issue fallback. Generic replay-safe retries are
disabled and redirect responses are not followed. Remote `attempted = checked +
unavailable`, `checked = in_sync +
drifted`, and local `present = attempted + not_attempted`. A redirect or other
unavailable probe sets `complete:false` and never counts as in-sync. The command
never writes or repairs mirror state.
Snapshot shares the status `[DIR]`/`--into` exclusivity, root precedence, and
pre-network initialized-root exit-4 contract.

## `atl jira reconcile preview` / `atl jira reconcile stage`

Use one tracked `.wiki` (or its neighboring `.md`) to compare the exact
last-synced Description, current local Description, and one fresh remote
Description:

```bash
ATL_READ_ONLY=1 atl jira reconcile preview mirror-jira/EXAMPLE/EXAMPLE-1.wiki
atl jira reconcile stage mirror-jira/EXAMPLE/EXAMPLE-1.wiki
```

The result uses `unchanged|local_only|remote_only|diverged` and binds the
remote issue id, key, canonical `updated` marker, all three hashes, and any
pending editable wiki fields into one proposal hash. Pending fields are
classified but never rewritten or materialized as misleading Description
files. A pending transaction, noncanonical copy, broken baseline, structured
remote field, missing projection, or body above 16 MiB fails before a claim is
made. Pending state additionally has a 64 MiB serialized-record and 256-field
aggregate bound; every individual base/proposed/remote wiki value keeps the
16 MiB body bound. The single-attempt remote request projects Description, `updated`, and
the exact pending field ids together.

`preview` is read-only. `stage` writes only exact Description base/theirs files
under `.atl/reconcile/jira/`; it does not recover transactions, change the
working `.wiki`/`.md`/snapshot/pending state, update the baseline, or contact a
write endpoint. Existing differing artifacts are never overwritten or deleted.

## `atl jira apply`

Merge supported edits made in an issue's markdown view (`<KEY>.md`) into its
`<KEY>.wiki` substrate and explicit pending-field set, block by block — the Jira analog of `conf apply`. This closes the
authoring loop **pull → edit the `.md` → apply → push**: you edit a familiar
markdown view instead of hand-writing Jira wiki markup, and `apply` folds the
change into the guarded local write set that `jira push` sends to the server.

The generated `# Description` section and field sections whose descriptor has
`editable:true`, `placement:"section"`, and `format:"jira_wiki"` are writable.
Blocks you did not touch keep their **exact base bytes**; changed or new blocks convert from the same strict
markdown subset as `jira issue create --from-md` (headings, paragraphs, lists,
simple tables, fenced code, blockquotes, links). Description is written to
`.wiki`; field values are stored under `.atl/pending/jira/` without modifying
the raw issue snapshot. Local only — `jira push` remains the write path to the server.

```bash
atl jira apply my-jira-mirror/PROJ/PROJ-1.md
atl jira apply PROJ-1.md --dry-run     # report the merge without writing
atl jira apply PROJ-1.md --allow-loss  # intentional {panel}/{color}/mention/embed removal
# after a fresh pull, compare raw remote vs visible local proposal first
atl jira apply PROJ-1.md --rebase-pending
```

| flag | description |
|---|---|
| `<FILE.md>` | the issue's markdown view (positional, required) |
| `--dry-run` | report the merge without writing files |
| `--allow-loss` | proceed when the edit drops wiki-only constructs |
| `--rebase-pending` | explicitly adopt freshly pulled raw field values as new pending drift bases after review |
| `--into` | mirror root (defaults to nearest `.atl`) |
| `--render-profile` / `--render-include` / `--render-exclude` | override the recorded view (normally unnecessary) |

`apply` reproduces the pristine view from the render settings the `.md` was last
written with (recorded on `pull`/`render` in `.atl/state.json`), diffed against
your edit — so no `--render-*` flags are needed. Pass them only to override that
recorded view; a mismatched profile will then flag the (unchanged) decorations as
edited. A pre-upgrade mirror with no recorded view falls back to the ambient
config — re-run `jira render` once to record it.

If you edit `.wiki` directly while fields are pending, its exact hash no longer
matches the reviewed combined write set and push refuses. Review both changes,
then `jira apply --rebase-pending` explicitly binds the proposals to that exact
wiki and regenerates `.md` without merging its stale Description.

Output: `{path, wiki_path, pending_path?, dry_run, rebased?, report: {...}, fields?:
[{id,pending,report}], wrote, warning?}`. After a successful apply the `.md` is regenerated from
the merged body so both surfaces agree; a failed refresh sets `warning` and the
apply still succeeds.

Pass `-o text` for a compact human loss-review — block counts, each removed
construct, and a contextual next-step hint (the Jira analog of `conf apply`'s):

```text
applied: PROJ/PROJ-1.wiki
blocks: 2 unchanged, 1 converted
removed constructs:
  - panel "{panel:title=Note}…"
next: run `jira push PROJ-1.wiki` to publish
```

The first line is the versioned format marker
`<!-- atl:document jira-issue v3 -->`; v2, v1, missing, or unversioned markers fail
closed and require `jira render` (or a fresh pull) before editing. V1 used the
former generated bullet form for Subtasks/Epic Children; v2 predates the
recorded display-timezone contract. Apply never guesses that an old generated
region was a user edit. A future or
unknown version requires updating `atl`; never render/downgrade it with the
older binary. Directory render checks all selected markers before rewriting the
first view. Because
render rewrites `.md`, save any existing edits as a reviewed external patch,
render the exact file/root, then reapply them. The
`<!-- atl:document ... -->` and `<!-- atl:section ... -->` prefixes are reserved
view boundaries. If either appears inside an editable Description or field
value, apply fails closed before changing `.wiki`, snapshot, or pending state;
remove it or edit the native `.wiki` substrate deliberately.

Each successful apply binds the exact issue key, relative `.wiki` path, native
hash, and unchanged remote-base hash in internal mirror state. Consecutive
Markdown edits can therefore be applied before push without moving the remote
baseline. A pull clears that generic binding when it advances remote state; if
it intentionally preserves a validated pending-field transaction, that exact
pending wiki hash remains the local lineage. Manual native edits still require
the explicit reviewed `--rebase-pending` path when fields are pending.

The merge is **fail-closed** (exit `8`, nothing written) when: an edited block
cannot be converted to wiki (a construct outside the subset) — make that edit in
the `.wiki` directly; a wiki-only construct present in the base is dropped by the
edit (`{panel}`, `{color}`, `[~mention]`, `!embed!`, a macro) and `--allow-loss`
was not given (the dropped constructs are listed in `removed_constructs`); an edit
touches any section other than generated `# Description` or an opt-in editable field (Metadata, Comments,
Links, Image Attachments) — the refusal names the section and the dedicated
command (`jira issue update`, `jira issue comment add`, `jira issue link add`,
`jira issue attachment upload`); or the local `.wiki` matches neither the
last-synced base nor exact ATL-staged/pending lineage (a direct `.wiki` edit
wins — preserve and push it, or explicitly rebase pending fields). Exit `4`:
the issue was never pulled (no base or snapshot).

## `atl jira push`

Push an edited `<KEY>.wiki` description and any pending opt-in rich-text fields
back to its issue. **Dry-run by
default** — without `--apply` it only previews the unified diff and any drift,
writing nothing. The diff shows what the write changes **on the server**
(current remote → local body), so under `--force` the remote-only changes about
to be overwritten are visible in the preview. No field outside the explicit
pending set is written. Description and fields are sent in one typed update when
both changed. This is the Jira analog of `conf push`, but deliberately stricter:

A `.wiki` whose issue key is tracked at another sidecar path is a stale copy:
preview and apply both fail closed with `skipped:"non-canonical-path"`, including
under `--force`, before any Jira read or write. Use the canonical mirror path.

```bash
# preview one file (dry-run: shows the diff, writes nothing)
atl jira push my-jira-mirror/PROJ/PROJ-1.wiki

# preview every locally-edited issue under a directory
atl jira push my-jira-mirror/

# actually write the change back
atl jira push --apply my-jira-mirror/PROJ/PROJ-1.wiki

# write over a drifted remote (re-base on current remote, then write)
atl jira push --apply --force my-jira-mirror/PROJ/PROJ-1.wiki
```

Flags:

| flag | description |
|---|---|
| `--apply` | actually write the change (default is a dry-run preview only) |
| `--force` | override description drift only; pending-field drift still refuses |
| `--into` | mirror root (defaults to the nearest `.atl`) |

A single-file target is pushed if changed (or with `--force` when clean); a
directory target pushes locally-edited files and field-only pending issues under it (`--force` does
not resurrect clean files). A file that was never pulled through the sidecar is
refused (exit 2 — pull it first).

**No server-side version gate.** Jira Data Center has no optimistic version gate,
so the staleness guard is an app-layer compare-and-swap: `jira push` re-reads the
remote description and every pending field. If the description changed since
pull, the push is **refused** with exit 8 ("remote
description changed since pull … re-pull or push `--force`") and nothing is
written unless explicitly forced. A pending field that no longer equals its
captured base is always refused, including with `--force`; re-pull and reconcile
it. A fresh pull keeps the local proposal visible in `.md` and puts the remote
value in raw `<KEY>.json`; when Description is also pending in `.wiki`, pull
preserves that reviewed local body while advancing its remote base. Compare the
versions, edit the proposal if needed, then run
`jira apply --rebase-pending` to adopt that snapshot as the new base. The next
push still fresh-reads it and refuses if it changed again. This CAS has an inherent time-of-check-to-time-of-use (TOCTOU) window —
the remote can still change between the check and the write — which `--force`
opts out of the refusal for rather than closing. A drift refusal is exit 8
(`ErrCheckFailed`), **never** exit 5 (`ErrVersionConflict`): exit 5 is reserved
for Confluence's real version gate. A server-side HTTP 409 (a locked issue, a
workflow veto) stays a generic conflict, distinct from local drift.

Typed writes are not replayed after an ambiguous response; atl performs one
fresh end-state read and accepts success only when all proposed values match.
If Jira already equals the proposal after a failed local refresh, the next push
repairs refresh/clear state without replaying the write.
On `--apply` success the mirror is refreshed (the `.wiki`, `.md`, raw `.json`,
base copy, and sidecar are rewritten, and pending state is cleared). A transport
or local-filesystem refresh failure is a warning — re-pull if you see one. If
the verification read succeeds but Jira no longer matches the full reviewed
proposal, pending state is retained and the command fails closed with exit 8.
