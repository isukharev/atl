# Agent workflow for atl

This file is the cross-agent operating guide for work in this repository. It
mirrors the high-signal architecture, correctness, testing, and handoff rules
from `CLAUDE.md` so agents that only read `AGENTS.md` still preserve the project
invariants.

## Project shape

`atl` is a single static Go binary: a Git-style CLI that mirrors Confluence
pages and Jira issues to disk in their native storage formats (Confluence
Storage Format `.csf`; Jira wiki). The `.csf` bytes are the write substrate:
never convert bodies through Markdown on the write path. Mirror `.md` files are
read-only views regenerated best-effort; render failures must not fail a pull.

Core commands:

```sh
make build
make test
make race
make lint
make vet
go test ./internal/csf/ -run TestParse
```

For CLI changes, run focused tests first (`go test ./internal/app
./internal/cli` or touched packages), then `make test`. Live integration tests
are opt-in through `.env.integration` and `ATL_INTEGRATION=1`; keep backend URLs,
PATs, and live fixture values out of the repo. Use `make integration` for
app-level live checks and `make live-smoke` for CLI-level fixture checks when
relevant. Requires Go 1.26.5+.

## Architecture invariants

The codebase follows hexagonal architecture. Preserve the dependency rule:

- `internal/domain` is the hub. It defines ports, `Resource`/`Ref`, registry
  ports, and sentinel errors. It imports nothing from the rest of the tree.
- `internal/adapter/{confluence,jira}` implement REST adapters. HTTP goes
  through `internal/httpx`; bodies are passed verbatim.
- `internal/app` contains transport-agnostic use cases and assembly in
  `wire.go`. No cobra, stdin, or filesystem beyond mirror operations.
- `internal/cli` is a thin cobra layer: parse flags, call one use case, emit,
  return error.
- `internal/csf` is read-only parsing and validation for Confluence Storage
  Format.
- `internal/fragment` extracts and resolves opaque CSF fragments.
- `internal/mirror` owns on-disk layout, sidecars, baselines, and dirty/drift
  detection. It is backend-agnostic.

Adapters and CLI never import each other. Prefer existing ports and service
patterns over introducing cross-layer shortcuts.

## Correctness rules

- Sentinel errors drive exit codes. Lower layers wrap with `fmt.Errorf("%w:
  ...", domain.ErrXxx)` so `errors.Is` maps usage/auth/not found/version
  conflict/forbidden/config/check failure correctly.
- Output is JSON by default. `emit(cmd, v, textFn)` writes indented,
  HTML-unescaped JSON unless `-o text` and a non-nil text renderer are both
  present. Logs and errors go to stderr; commands are non-interactive.
- Confluence updates use an optimistic version gate. `--force` re-reads current
  state and targets `current+1`; post-push mirror refresh failures are warnings,
  not hard failures.
- PATs are host-scoped. `httpx` only sends bearer tokens to the configured host
  and refuses cross-host or scheme-downgrade redirects.
- Backend URLs must be https except loopback or trusted internal runs with
  `ATL_ALLOW_INSECURE=1`.
- CSF parsing must be byte-stable and read-only. Validation errors gate pushes;
  warnings are advisory.
- Fragment resolution never fails a pull. Unresolved refs keep raw display or
  empty assets.
- Stdin bodies are capped at 64 MiB and rejected with usage errors when larger.

## Mirror and config rules

- `conf pull --cql` caps at 1000 pages and reports truncation; Jira `pull
  --limit 0` means unbounded.
- Mirror roots are auto-detected by walking up to 12 levels for `.atl`; if none
  is found, commands default to `mirror`.
- Drift requires a synced baseline. Dirty detection is content-hash based, not
  timestamp based.
- Slugs must remain unicode-safe and path-safe. Server-controlled path
  components go through the existing safe path helpers.
- PAT resolution is environment first, then host-scoped credentials under the
  ATL config dir. Env URLs overlay config-file URLs.
- Self-update is best-effort before subcommands, skipped for dev/empty versions
  or `ATL_NO_UPDATE`, and must verify signatures and hashes before trusting
  downloaded content.

## Testing and docs

- Tests live alongside code in the same package. Use `httptest`, `t.Setenv`,
  `t.TempDir`, and stable fixtures. Do not combine `t.Parallel` with
  `t.Setenv`.
- Fuzz code that ingests server-controlled bytes, especially `internal/csf`,
  `internal/safepath`, and `internal/mirror`. Add seeds for regressions.
- CLI output is a contract. Golden tests in `internal/cli/testdata/golden/` and
  the sentinel exit-code matrix must be updated when output or sentinels change.
- User-facing CLI changes must update public docs and shipped client skills in
  the same PR: `README.md`, `README.ru.md` when applicable, `docs/usage.md`,
  `docs/OUTPUT_CONTRACT.md` for output shape changes, and the relevant
  `skills-src/*/SKILL.md`. `skills/` and `plugins/atl/skills/` are **generated**
  from `skills-src/` by `make gen-plugins` — never edit them by hand; regenerate
  and commit all three trees together (see `docs/plugins.md`).
- Plugin manifest versions (`.claude-plugin/plugin.json`,
  `plugins/atl/.codex-plugin/plugin.json`) are bumped only in the release prep
  commit, in lockstep with the CLI version — never in a feature PR.
- Security-boundary tests should prove the guarantee fails when the control is
  removed, not because of incidental parse or fixture failures.
- Any change to bytes emitted inside a durable derived view requires an explicit
  document-format marker review. If existing pristine views would reconstruct
  differently, bump the marker, add render migration and apply diagnostics, and
  test current, legacy, unversioned, and future-marker behavior.
- Context7-selected runtime documentation must contain at least one real,
  non-empty language-tagged fenced example. Run `make check-context7-docs` when
  adding root Markdown, changing `context7.json`, or editing the indexed corpus.
- Keep PRs small; commit subjects use `<type>: <summary>`.

## GitHub tracking

Non-trivial work should be visible in GitHub before code changes start. A task is
non-trivial when it changes user-facing behavior, public docs, CLI output, release
process, architecture, security posture, or more than one small implementation
detail.

Trivial typo fixes, mechanical formatting, local experiments, and explicitly
private/security-sensitive work may skip public issue creation.

The workflow is intentionally issue-first and does not depend on GitHub Projects.
Issues, parent/sub-issues, labels, comments, linked branches, and PR links provide
enough traceability without heavy GraphQL usage.

### Standard flow

1. Find or create a GitHub issue for the task.
2. Link it to a parent roadmap or quarterly initiative issue when one exists.
3. Add labels for area/kind/roadmap horizon and agent state.
4. Comment with the agent plan before editing code.
5. Create or use a linked branch.
6. Implement the change.
7. Open a PR that references the issue and includes verification.
8. Use PR review/CI and issue comments as the visible status trail.
9. Close the issue through a PR (`Fixes #...`) or explicit maintainer decision.

Recommended issue comment before implementation:

```md
## Agent plan

Problem:

Approach:

Files likely to change:

Acceptance criteria:

Verification:

Risks / non-goals:
```

Recommended PR body links:

```md
Refs #<issue>
Parent: #<initiative>
Roadmap: <ID or ROADMAP.md section>
```

### GitHub CLI commands

Check authentication:

```sh
gh auth status
```

Create an issue:

```sh
gh issue create \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --label area/safety \
  --label kind/feature \
  --body-file /tmp/issue.md
```

Create a sub-issue under a parent initiative:

```sh
gh issue create \
  --parent <parent-issue-number> \
  --title "agent: add global read-only policy" \
  --label agent-ready \
  --body-file /tmp/issue.md
```

Create a linked branch for an issue:

```sh
gh issue develop <issue-number> --checkout
```

Post or update the agent plan:

```sh
gh issue comment <issue-number> --body-file /tmp/agent-plan.md
```

Update issue state with labels instead of Project fields:

```sh
gh issue edit <issue-number> --add-label agent-working
gh issue edit <issue-number> --remove-label agent-ready
gh issue edit <issue-number> --add-label needs-human
```

Create a PR:

```sh
gh pr create \
  --draft \
  --title "feat: add global read-only policy" \
  --body-file /tmp/pr.md
```

### Labels

Use labels for search, queueing, and lightweight automation.

- `area/confluence`, `area/jira`, `area/sync`, `area/mcp`, `area/safety`,
  `area/packaging`, `area/cloud`, `area/docs`
- `kind/feature`, `kind/bug`, `kind/research`, `kind/docs`, `kind/infra`
- `agent-ready`, `agent-working`, `needs-human`
- `roadmap/now`, `roadmap/next`, `roadmap/later`

Suggested issue searches:

```sh
gh issue list --label agent-ready --state open
gh issue list --label agent-working --state open
gh issue list --label needs-human --state open
gh issue list --label roadmap/now --state open
```

## Agent handoff rules

- Never merge a pull request authored by anyone other than `isukharev` unless
  `isukharev` explicitly instructs you to merge that specific PR. Green CI,
  labels, assignments, or general autonomy do not count as authorization.
- Do not start broad implementation work from chat-only context when an issue is
  expected; create or update the issue first.
- Keep the issue updated when scope changes.
- If blocked, comment with the blocker and add `needs-human`.
- Do not close an issue just because a local patch exists. Close it through a PR
  (`Fixes #...`) or explicit maintainer decision.
- Never put secrets, PATs, private hostnames, private page IDs, or proprietary
  page content in issues, PRs, commits, or logs.
- Internal planning and strategy docs live in the gitignored `local-docs/`
  directory. This is a **public** repository: never commit that content (or the
  files) to it, move them out of `local-docs/`, or name/reference them from public
  docs, issue forms, PRs, or commit messages. Internal roadmap IDs stay internal —
  reference the public `ROADMAP.md` instead.
- Follow `CLAUDE.md` for code architecture, output contracts, write-path safety,
  plugin/docs synchronization, and test expectations.
