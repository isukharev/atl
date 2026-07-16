<!-- Generated from skills-src/atl/reference/workflow.md — edit the source and run 'make gen-plugins'. -->
# `atl` workflow: mirror location and the safe edit loop

## Where the mirror lives

**Recommended convention: `~/.atl/<workspace>/`** — outside the user's code
repository, where `<workspace>` is meaningful (for example,
`~/.atl/payments/`). Export it as `ATL_MIRROR_ROOT` or pass `--into`; otherwise
the CLI falls back to `mirror` for Confluence and `mirror-jira` for Jira.

Mirror I/O is rooted at the selected directory: descendant symlinks cannot
redirect writes outside it. `status` and directory `push` fail if a substrate
or metadata entry cannot be read, so never treat a scan error as a clean or
complete mirror; repair the local entry or re-pull it first.

Why outside the repo:
- A coding agent's `Grep` (ripgrep) **skips git-ignored files by default**, so an in-repo *ignored*
  mirror would be invisible to search. A separate tree is fully greppable.
- Mirrored company content never lands in the code repo's git history (no leaks, no bloat).
- `atl` stores a sidecar `.atl/` (sync baseline: last-synced version + content hash, and pristine
  copies for diffing). That state is **essential, not a cache** — losing it resets the version gate
  and drift detection — so it stays co-located with the mirror, never in `~/.cache`.

Pass `--into ~/.atl/<workspace>/` to `conf pull` / `jira pull` and the same root
to `conf status`, or export it once as `ATL_MIRROR_ROOT`.

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
4. **Review offline semantics** (`conf diff <file>`) — use JSON for block/feature
   fingerprints and `-o text` for a compact human summary. This never contacts the backend.
5. **Review a write dry-run** (`conf push --dry-run`) — confirm the consequences,
   remote drift, and any added/removed fragments.
6. **Push** under the version gate.
7. **On conflict** (Confluence exit `5`), surface it and let a human decide: re-pull and
   reconcile, or `--force` (last-writer-wins). **Never auto-`--force`.**

Push the bytes you reviewed — don't regenerate the body between the dry-run and the push.
For a large Confluence mirror that needs historical completeness, first run
`conf pull --complete` with a stable CQL/space selector and repeat the exact
command to resume an interruption. It costs two search passes before serial
body GETs and keeps its exact private checkpoint under `.atl`; option drift is
fail-closed. Then use `conf pull --incremental` for recurring changes.
Bootstrap incremental mode once with a reviewed RFC3339 `--since` instant
carrying an explicit offset, then reuse the exact selector/root. Only
`complete:true` advances its inclusive minute watermark; exit 8 is resume-safe
and absence never proves deletion.
For several Confluence pages, replace directory push with `conf plan create`,
then read-only `conf plan preview`, then gated `conf plan apply` with the exact
hash + `--confirm APPLY`. A plan is a
private review artifact; any blocked/stale entry means zero initial PUTs, and an
`unknown` partial outcome must never be replayed automatically.

Jira note: Jira issue updates have **no version gate** (last-writer-wins). Run `jira issue get`
immediately before an `update` to avoid blindly overwriting someone else's change. The mirror
write-back path — edit the `.md` view, `jira apply`, then `jira status` / `jira push` (or edit
`<KEY>.wiki` directly as a fallback) — adds the equivalent guard in
software: `jira push` is dry-run by default and refuses on drift with exit `8`, never exit `5`.
`--force` can override Description drift only; pending opt-in rich-text fields always fail closed on
drift. Reconcile those by fresh pull + raw/visible comparison + explicit `jira apply
--rebase-pending`; no field outside the explicit pending set is written.
