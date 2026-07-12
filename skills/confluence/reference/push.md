<!-- Generated from skills-src/confluence/reference/push.md — edit the source and run 'make gen-plugins'. -->
# Pushing Confluence pages: the version gate

## Optimistic version gate

`atl conf push` sends `version = syncedVersion + 1`. If the remote has moved on (someone else
edited the page since you pulled), Confluence rejects it with a 409, which `atl` maps to a
**version conflict → exit code 5**.

This is the safety net that makes a slightly-stale mirror safe to write from. The recovery loop:

1. Exit 5 → tell the user the page drifted; do **not** silently overwrite.
2. Re-pull the page (`atl conf pull --id <id> --into ~/.atl/<workspace>/`).
3. Reconcile your edit against the new base.
4. Re-validate, re-dry-run, push again.

`--force` re-reads the current remote version and targets `current + 1` — i.e. it **clobbers** the
drift (last-writer-wins). Use it only after a human has decided the remote change is safe to
overwrite. Never auto-`--force`.

## Dry-run output

`atl conf push --dry-run <file|DIR>` returns `{ "items": [ … ] }` where each item has:

- `path`, `id`
- `dry_run: true`
- `remote_drifted` — true if the remote already moved past your synced version
- `added_fragments`, `removed_fragments` — fragment changes your edit introduces/drops
- `problems` — validation problems (if any)

Review this before the real push. Push the exact bytes you reviewed — don't regenerate the body in
between, or the diff you approved won't match what gets written.

## Real push output and outcomes

Each item in a real `atl conf push` reports one of:

- `pushed: true` with `new_version` — success.
- `skipped: "<reason>"` — nothing to do (e.g. unchanged).
- `failed: "<reason>"` — e.g. `forbidden` (exit 6), `not-found` (exit 4), `auth` (exit 3).
- `problems: [...]` with `error` severity — invalid CSF; fix and retry.
- `warning: "<text>"` — non-fatal (e.g. the post-push refresh of the mirror failed; the push
  itself succeeded). Multiple independent refresh degradations are preserved in
  that field. If the native Jira-macro set changed, atl retires its obsolete
  generated snapshot and the warning asks you to re-pull current query rows.

## Pre-push checklist

1. `atl conf validate <file.csf>` → no `error`-severity problems.
2. `atl conf status ~/.atl/<workspace>/ --remote` → not unexpectedly `remote_drifted`.
3. `atl conf push --dry-run <file>` → diff and `removed_fragments` look intended.
4. `atl conf push <file>` → confirm `pushed: true`.
