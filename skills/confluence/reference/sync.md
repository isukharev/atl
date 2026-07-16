<!-- Generated from skills-src/confluence/reference/sync.md — edit the source and run 'make gen-plugins'. -->
# Durable Confluence mirror sync

Load this reference for new mirrors, historical complete pulls, recurring
incremental refresh, render migration, or bounded request scheduling. Keep
`ATL_READ_ONLY=1` exported for every pull/status/render-only shell.

## Select and establish the mirror

Use one stable id/CQL/space selector and one absolute root. Add assets/comments
only when the task needs them. Ordinary selectors have documented caps; never
treat `truncated:true` as complete absence evidence.

```bash
export ATL_READ_ONLY=1
atl conf pull --id <id> --assets --into <absolute-root>
# add --comments only when comment context is required
```

On an existing root, run remote status before pulling. Preserve local edits;
refresh only a clean remote-drifted mirror. Mirror identity and view state live
under the nearest `.atl`; never edit or copy that state by hand.

## Complete historical bootstrap

```bash
export ATL_READ_ONLY=1
atl conf pull --complete --cql '<stable CQL without ORDER BY>' --into <absolute-root>
# interrupted run: repeat the exact command
atl conf pull --complete --cql '<same stable CQL>' --into <absolute-root>
```

Complete mode performs two exhaustive metadata passes and requires the same
canonical unique-id set before body reads. It stores a private mode-0600 exact-id
checkpoint, binds content/render options, and resumes only its remaining prefix.
Committed page writes/checkpoints are serial; a hard crash may replay at most
the current batch, never skip it. `ORDER BY`, partial pagination, selection
drift, duplicate ids, or local edits fail closed. Use `--restart-complete` only
after preserving edits and explicitly replacing the unfinished snapshot.
Absence from a snapshot never proves deletion.

After reviewing backend capacity, `--page-prefetch 2..8` and optionally
`--requests-per-second N` may bound parallel reads. Prefer the smallest useful
values. The shared scheduler covers Confluence and optional Jira-macro reads,
redirects, retries, streams, and `Retry-After`; never add shell parallelism.
Mirror mutation and checkpoints remain serial/canonical.

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
