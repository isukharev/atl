# Development and verification

This runbook starts at a clean understanding of repository state and ends with
a stable integrated diff. Use [Landing a change](landing-a-change.md) after the
implementation is ready for public review.

## Read-only preflight

Run these before changing files:

```sh
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
git config --get user.email
git config --get user.useConfigOnly
gh auth status
```

Identify every pre-existing modification before editing. It belongs to its
current owner: never stash, reset, clean, absorb, or overwrite it. Confirm the
configured author and `user.useConfigOnly` satisfy `AGENTS.md`. Refresh remote
state before assuming the branch is current.

Classify the request before acting:

- Answer/review/report: inspect and explain; do not mutate external state.
- Diagnose: establish the cause; do not implement a fix unless requested.
- Change/build: follow issue-first workflow for non-trivial work, implement,
  verify, review, and land.
- Live validation: read [Live validation](live-validation.md) before using a
  configured backend.

## Find the owner before editing

| Concern | Canonical owner |
|---|---|
| Domain types, ports, sentinels | `internal/domain` |
| Transport-agnostic use cases | `internal/app` |
| REST behavior | `internal/adapter/{confluence,jira}` through `internal/httpx` |
| Cobra parsing and output selection | `internal/cli` |
| Native mirror bytes, state, and baselines | `internal/mirror` |
| Confluence parsing | `internal/csf`, `internal/fragment` |
| Command prose | `docs/reference/cli/` |
| Output/wire contracts | `docs/reference/output/` |
| Shipped client skills | `skills-src/`; generate `skills/` and `plugins/atl/skills/` |
| Repository-only agent workflow | `.agents/skills/` and this directory |

Read the smallest canonical file that owns the behavior. Use `rg` and
`rg --files` before broader searches. Inspect the live command tree with
`ATL_NO_UPDATE=1 ./atl --help` or the relevant parent help; do not maintain a
second handwritten command inventory.

Trace behavior across layer vocabulary rather than assuming one method name is
shared end to end. An app service may call a differently named domain port,
which an adapter implements under a third receiver type. Start from the command
or output contract, then follow construction in `internal/app/wire.go`, port
interfaces, and implementations.

Keep one implementation owner per overlapping file set. Delegated work is
bounded by the brief; the root integrates and verifies the final diff.

## Implement by invariant

- Preserve the ports-and-adapters dependency direction in
  [architecture.md](../architecture.md).
- Preserve native Jira wiki and Confluence Storage Format bytes on write paths.
  Markdown is a derived staging view, never a remote payload.
- Keep sentinel errors, JSON-default stdout, stderr diagnostics, and stable exit
  classes aligned across domain, app, CLI, docs, and golden tests.
- Treat redirects, credentials, server-controlled paths, body sizes, retries,
  and write ambiguity as security boundaries.
- Review the durable document-format marker whenever derived view bytes change.
- Use a system temporary directory for one-off Go helpers. A stray Go file
  anywhere inside the module can enter `go list ./...` and break local gates.
- Regenerate a CLI golden only with the narrow owning test, for example
  `go test ./internal/cli -run TestName -update`, then rerun it without
  `-update` and inspect the bytes. Normalize ports, paths, timestamps, and other
  volatile values before accepting a golden.
- Preserve `testdata/fuzz/` crash corpora. In containment fuzzers, join a
  sanitized value as its own path component; appending a suffix such as `.csf`
  can turn a bare `..` regression into a harmless filename and mask the bug.
- Keep examples synthetic. Never copy configured backend values into tracked
  fixtures, docs, diffs, commits, issues, or PRs.

## Select verification once the diff is stable

Iterate with the smallest focused test, then run the full gates once. Repeat a
full gate only after a material fix that can affect it.

| Change | Focused verification before full gates |
|---|---|
| App or CLI behavior | `go test ./internal/app ./internal/cli -count=1` |
| One Go package | `go test ./path/to/package -count=1` |
| Concurrency or shared state | focused `go test -race`, then `make race` |
| Generated client skills | `make gen-plugins && make check-plugins` |
| Repository runbooks or `.agents/skills/` | `make check-repository-skills && make check-docs-catalog` |
| Public documentation | `make check-docs-catalog && make check-context7-docs` |
| CLI leaves, safety docs, or changed-path routing | `make check-docs-freshness` |
| Onboarding routes | `make check-onboarding-docs` |
| Large reference headings | `make update-reference-navigation && make check-context7-docs` |
| CLI/output reference moves | `make check-reference-split` |
| Toolchain/build contract | `make check-maintainer-contract` |
| Production hotspot allowances or timing observations | `make check-maintainability` |
| Package ownership | `make check-package-boundary` |

Stable non-trivial diffs normally finish with:

```sh
make test
make lint
make vet
git diff --check
```

For a change relative to a branch or commit, ask the maintained impact map for
the applicable existing gates:

```sh
ATL_DOCS_BASE=origin/main make check-docs-freshness
```

Set `ATL_DOCS_HEAD` only when checking two committed endpoints instead of the
current index and working tree. An owner may also set
`ATL_PRIVATE_MARKERS_FILE` to an untracked marker-registry path; the checker
then scans added diff lines and untracked public files. A match emits only a
generic failure, never a marker, path, line, or content. Keep that registry
outside tracked files.

Selected production file and function spans are reviewed in
`docs/maintainability-ratchets.v1.json`. The allowances are growth tripwires,
not quality scores: add headroom only with a rationale, and lower a limit after
a responsibility-based split lands. Timing rows record hosted observations in
`observe` mode; they do not impose runtime thresholds.

Use `make agent-eval-contract` only for evaluator/corpus changes, and the live
targets only when the change and authority require them. Run a privacy scan over
the complete public diff, excluding unrelated owner changes. Review the
integrated diff once; add a bounded follow-up only after a material finding,
design change, or security-boundary fix.
