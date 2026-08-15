# Durable Confluence mirror sync

Load this reference for new mirrors, historical complete pulls, recurring
incremental refresh, render migration, or bounded request scheduling. Keep
`ATL_READ_ONLY=1` exported for every pull/status/render-only shell.

## Select and establish the mirror

Use one stable id/CQL/space selector and one absolute root. Add assets/comments
only when the task needs them. Attachment inventory is a separate complete-pull
policy and needs explicit per-page pagination and item caps; attachment bodies
also need an exact MIME allowlist and byte caps. Ordinary selectors have documented caps; never
treat `truncated:true` as complete absence evidence.

```bash
export ATL_READ_ONLY=1
atl conf pull --id <id> --assets --into <absolute-root>
# add --comments only when comment context is required
```

Every pull result has stable `assets` and `comments` include rows; requested
complete-pull attachments append an `attachments` row. Read
`requested` and `qualification` first; `complete` is proof-only and is omitted
until work has actually proved complete or incomplete. Omitted flags are
`not_requested`. Requested dry-run work is `deferred` with
`reason:preview_deferred` and performs no comment-list or asset-download GET.
Actual complete publication is `qualified,complete:true`; incomplete coverage
is `partial,complete:false` with `resolution_incomplete`,
`inventory_incomplete`, or `not_attempted`; read/publication failure is
`failed,complete:false` with `read_failed` or `staging_failed`. No backend prose
belongs in `reason`.

Qualification advances only after the page and all staged artifacts for that
dimension are durably published. A page, sidecar, asset, shared flush, or
publication failure demotes every affected staged dimension to
`failed/staging_failed` before the original non-zero error. Restored legacy
progress without include evidence is `partial/not_attempted`, never proof of a
complete durable prefix.

On an existing root, run remote status before pulling. Preserve local edits;
refresh only a clean remote-drifted mirror. Mirror identity and view state live
under the nearest `.atl`; never edit or copy that state by hand.

For an ordinary multi-page CQL/space pull, opt into bounded scheduling only
after reviewing backend capacity:

```bash
atl conf pull --space <KEY> --page-prefetch 2 --requests-per-second 10 \
  --into <absolute-root>
```

Prefetch `2..8` overlaps native body GETs while the canonical consumer and all
mirror writes stay serial. A positive rate with prefetch `1` is a rate-only,
one-in-flight schedule. Omitting the flags, or explicitly passing their `1/0`
defaults, retains the ordinary unscheduled path. Never add shell parallelism.

When the question asks for health counts rather than page identities, start
with the aggregate snapshot instead of recounting status/diff rows:

```bash
atl conf snapshot <absolute-root>
# add --remote only when current drift evidence is required
```

Status/snapshot accept positional `[DIR]` or `--into`, never both. With neither
they use `ATL_MIRROR_ROOT`, nearest initialized `.atl`, then `mirror`; an absent
or uninitialized root is exit 4 before config/network.

The default is offline and content-free. Require top-level and nested
`reconciled:true`; treat `complete:false` as unavailable evidence, not as an
arithmetic failure. A qualified exit 8 for corrupt baseline evidence remains
actionable stdout and must not be retried. If the aggregate cannot be written,
the command reports that write failure with the inspection failure and keeps
the inspection exit code; after an otherwise clean inspection, it returns the
write failure alone. Never read a missing aggregate as clean. Use `conf diff`
only to expand exact page-level change evidence.

## Complete historical bootstrap

```bash
export ATL_READ_ONLY=1
atl conf pull --complete --cql '<stable CQL without ORDER BY>' --into <absolute-root>
# interrupted run: repeat the exact command
atl conf pull --complete --cql '<same stable CQL>' --into <absolute-root>

# attachment inventory is explicit and bounded for every selected page
atl conf pull --complete --cql '<same stable CQL>' --into <absolute-root> \
  --attachments --max-attachment-pages-per-page <pages> \
  --max-attachments-per-page <items>
```

Complete mode performs two exhaustive metadata passes and requires the same
canonical unique-id set before body reads. It stores a private mode-0600 exact-id
checkpoint, binds content/render options, and resumes only its remaining prefix.
Each page is staged behind a durable publication intent, and accepted pages are
journaled before batch sidecar/progress commits. Exact intent/journal-owned temp
names make hard-crash residue recoverable without deleting unrelated files; a
hard crash neither refetches an accepted body nor skips it. `ORDER BY`, partial
pagination, selection drift, duplicate ids, or local edits fail closed. Use `--restart-complete` only
after preserving edits and explicitly replacing the unfinished snapshot.
Absence from a snapshot never proves deletion.

After reviewing backend capacity, the same `--page-prefetch 2..8` and optionally
`--requests-per-second N` may bound parallel reads. Prefer the smallest useful
values. The shared scheduler covers Confluence and optional Jira-macro reads,
redirects, retries, streams, and `Retry-After`; never add shell parallelism.
Mirror mutation and checkpoints remain serial/canonical.

The default stops before an accepted page when requested comment/thread or
attachment inventory/body evidence is incomplete. The legacy anchor-only comment
detail remains recorded but does not itself block progress. Use
`--allow-partial-artifacts` only when an explicitly partial sidecar is useful:
it is hash-bound, records `partial,complete:false`, and never proves absent
comments or attachments.
The attachment-body aggregate cap is restored from the accepted private prefix
before a resume reads another page; it is one clone-wide cap, not a new allowance
per invocation. Public complete pulls cap that aggregate at 64 MiB and retain
at most 512 eligible bodies per page. Before opening a body, atl reserves the
exact core, staged-asset, Jira-macro, and relocation tombstone bytes; the preflighted attachment-sidecar
upper bound; and ownership-proven retirements in that atomic page transaction.
Strict mode stops before opening any body for an over-count or over-byte page;
partial mode records the deterministic retained prefix and `count_limit` or
`aggregate_limit`.
Each binary selector is immediately revalidated against the inventory ID, but
the metadata and binary requests are not an atomic backend snapshot. A changed
or ambiguous selector stops the strict page; only the explicit partial policy
may retain a failed sidecar record.
A complete pull without `--attachments` atomically retires an earlier
ownership-proven attachment capture when its replacement page would make that
evidence stale. A normal or incremental refresh, including a page relocation,
stops before writing in that case; rerun it with `--complete` rather than
creating a new page identity beside old body evidence.

## Incremental refresh

Inspect time semantics once when calendar boundaries matter:

```bash
export ATL_READ_ONLY=1
atl environment inspect
atl conf pull --incremental --cql '<stable CQL without ORDER BY>' \
  --since '<RFC3339 minute with explicit offset>' --into <absolute-root>
# subsequent run: identical selector/root, omit --since
atl conf pull --incremental --cql '<same stable CQL>' --into <absolute-root>
```

The first boundary is an absolute reviewed instant; atl stores UTC. A fixed
48-hour query overlap plus local exact-timestamp filtering makes an unknown CQL
zone cause extra reads, not omissions. Equal-minute id/version pairs are
rechecked safely; no calibration search or timezone guess is performed.

Atl requires two identical complete metadata passes. Any cap, partial page,
selector drift, inaccessible page, local edit, or error is exit 8 and cannot
advance the private watermark. Treat `complete:true` plus persisted watermark
as one claim. Repeat the same command after fixing the named cause; absence from
a delta never proves deletion.

## View migration and recovery

Incremental preflight accepts an older supported `.md` only when the complete
legacy view reconstructs byte-clean; the successful page pull then writes the
current format. Preserve/reconcile real edits before `conf render`. Unknown or
future markers require a newer atl, never downgrade. Missing/partial native
bodies, corrupt state, or a failed page leave the watermark unchanged.
