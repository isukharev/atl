# `atl` workflow: mirror location and the safe edit loop

## Where the mirror lives

**Default: `~/.atl/<workspace>/`** — outside the user's code repository, where `<workspace>` is a
meaningful name (the code repo's basename or the Confluence space key, e.g. `~/.atl/payments/`).

Why outside the repo:
- A coding agent's `Grep` (ripgrep) **skips git-ignored files by default**, so an in-repo *ignored*
  mirror would be invisible to search. A separate tree is fully greppable.
- Mirrored company content never lands in the code repo's git history (no leaks, no bloat).
- `atl` stores a sidecar `.atl/` (sync baseline: last-synced version + content hash, and pristine
  copies for diffing). That state is **essential, not a cache** — losing it resets the version gate
  and drift detection — so it stays co-located with the mirror, never in `~/.cache`.

Always pass `--into ~/.atl/<workspace>/` to `conf pull` / `jira pull`, and pass the same root to
`conf status`.

### Overrides (use only when they apply)

- **Committed in-tree** (`<repo>/atl/`, tracked in git): only when the repo is private, the org
  permits Atlassian content in the code repo, and versioned specs reviewed in PRs are wanted.
- **Git-ignored in-tree scratch**: one-off context only. Because `Grep` skips ignored files, you
  must then search by explicit path or with `rg --no-ignore <pattern> <mirror-dir>`.

## Searching a two-root layout

With the mirror outside the repo, you are working across two trees (the code repo and
`~/.atl/<workspace>/`). To find things in the mirror, run `Grep`/`Glob` with the mirror path
as the search root, or read files there by absolute path. Don't assume a repo-root search covers
the mirror.

## The safe edit loop

Make `push` the single deliberate, human-reviewed checkpoint:

1. **Pull fresh** right before editing — bounds staleness.
2. **Edit** — prefer the `.md` view and merge it back (Confluence `conf apply` / Jira `jira apply`);
   drop to the native substrate (`.csf` / `.wiki`) only for what the md view can't express. One-shot
   Jira field edits still go through commands — see those skills.
3. **Validate** (Confluence `conf validate`) — block on any `error`-severity problem.
4. **Review a dry-run diff** (`conf push --dry-run`) — confirm the changes and any
   added/removed fragments and drift before writing.
5. **Push** under the version gate.
6. **On conflict** (Confluence exit `5`), surface it and let a human decide: re-pull and
   reconcile, or `--force` (last-writer-wins). **Never auto-`--force`.**

Push the bytes you reviewed — don't regenerate the body between the dry-run and the push.

Jira note: Jira issue updates have **no version gate** (last-writer-wins). Run `jira issue get`
immediately before an `update` to avoid blindly overwriting someone else's change. The mirror
write-back path — edit the `.md` view, `jira apply`, then `jira status` / `jira push` (or edit
`<KEY>.wiki` directly as a fallback) — adds the equivalent guard in
software: `jira push` is dry-run by default and refuses on drift with exit `8` (re-pull or `--force`),
never exit `5`, and writes only the description body.
