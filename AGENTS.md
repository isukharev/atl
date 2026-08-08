# Agent workflow for atl

This is the binding cross-agent contract for repository work. Provider-specific
files may add execution guidance but cannot replace or weaken these rules.
Repeatable procedure lives in [maintainer runbooks](docs/maintainers/README.md),
and focused Codex workflows live in `.agents/skills/`.

## Product invariants

`atl` is one static Go CLI that mirrors Confluence pages and Jira issues in
their native storage formats: Confluence Storage Format (`.csf`) and Jira wiki.

- Native bytes are the remote write substrate. Never convert a whole body
  through Markdown on a write path.
- Mirror `.md` files are derived staging views. Supported edits enter native
  bytes only through explicit `conf apply` or `jira apply`; Markdown itself is
  never sent to a backend.
- Render failures are best-effort and must not fail a pull.
- JSON is the default stdout contract. Human text requires an explicit output
  mode; logs and errors go to stderr; commands remain non-interactive.
- Sentinel errors wrap with `%w` so the CLI preserves stable exit classes.
- Confluence writes use optimistic version gates. A post-write mirror refresh
  failure is a warning, not proof that the remote write failed.

Read [architecture](docs/architecture.md), [safe writes](docs/safe-writes.md),
and the focused [CLI](docs/reference/cli/README.md) and
[output](docs/reference/output/README.md) references for detailed contracts.

## Authority and ownership

The root agent owns the plan, design, final integrated diff, external state,
and verification. Work directly when the task is short or tightly coupled.
Delegate only concrete bounded work that can proceed independently.

- A worker is subordinate only when its brief says so. Omitted edit authority
  means read-only. Workers do not delegate, push, mutate issues/PRs, merge,
  release, or make authenticated backend writes unless the brief authorizes the
  exact action.
- Give workers the objective, allowed files, invariants, non-goals, expected
  output, focused verification, and branch/worktree ownership. One agent owns
  each overlapping file set.
- Give workers no transcript or only the smallest recent context by default;
  include full history only when irreducible state cannot be summarized safely.
  Reuse a worker only while its bounded context remains current; otherwise use
  a fresh brief.
- The root inspects every result. A worker report is evidence, not proof.
- A worker's final report names its outcome, findings, verification actually
  run, files changed when authorized, and unfinished or blocked scope.
- Independent review is read-only and covers the integrated diff. Findings use
  file/line evidence; the reviewer does not fix the code under review.
- Preserve all pre-existing worktree state. Never stash, reset, clean, discard,
  overwrite, commit, or absorb unrelated changes.
- Do not infer write, cleanup, deletion, release, archive, benchmark, or merge
  authority from credentials, old approvals, issue labels, or a prior task.
- Approval naming a reviewed plan covers only the exact privileged actions and
  ordered steps that plan already enumerates; it grants no missing authority
  category. Do not require phrase repetition. Reapprove after scope, target,
  order, retry, or relevant state changes.

The git author email must remain `ivan7654@gmail.com` and local
`user.useConfigOnly` must remain `true`. Stop before publishing if another
identity appears in the pending history.

## Architecture boundary

Preserve the ports-and-adapters dependency direction:

- `internal/domain`: types, ports, registry contracts, sentinels; imports no
  other ATL package.
- `internal/adapter/{confluence,jira}`: REST adapters; HTTP only through
  `internal/httpx`; native bodies pass verbatim.
- `internal/app`: transport-agnostic use cases and assembly; no Cobra or stdin.
- `internal/cli`: thin parsing/emission layer; never imports adapters.
- `internal/csf` and `internal/fragment`: read-only CSF parsing and resolution.
- `internal/mirror`: backend-agnostic layout, sidecars, baselines, dirty/drift,
  and durable local writes.

Adapters and CLI never import each other. Prefer existing ports and service
patterns over cross-layer shortcuts.

## Security and write safety

- PATs are host-scoped. Refuse cross-host and scheme-downgrade redirects; never
  follow a redirect from a mutating request.
- Generic retries are for replay-safe reads only. A non-replay-safe request is
  single-attempt unless an explicit reviewed plan proves a bounded retry safe.
- Backend URLs require HTTPS except loopback or an explicitly trusted internal
  run with `ATL_ALLOW_INSECURE=1`.
- Server-controlled paths use existing safe-path and containment helpers.
- Stdin bodies are capped at 64 MiB and fail rather than truncate.
- CSF parsing is byte-stable and read-only. Validation errors gate pushes;
  warnings are advisory. Fragment resolution remains best-effort.
- Security tests prove the intended control—not an incidental parser failure—
  rejects the adversarial case.
- A change to durable derived-view bytes requires marker review, migration and
  apply diagnostics, plus current/legacy/unversioned/future tests.

Mirror roots, baselines, pull bounds, auth precedence, and self-update details
are canonical in the focused references. Do not reconstruct them from memory.

## Development and verification

Start with the read-only preflight and check-selection table in
[Development and verification](docs/maintainers/development.md). Requires the
exact Go patch declared by the applicable module's `go.mod` (currently 1.26.5+).

Core gates:

```sh
make build
make test
make race
make lint
make vet
```

The evaluator is an independent nested module at `internal/agenteval` (module
`github.com/isukharev/atl/internal/agenteval`); its maintainer command is
`internal/agenteval/cmd/agent-eval`. Root recursive Go commands intentionally
exclude that module. Do not reconnect it with a root `require`/`replace` or a
tracked `go.work`. `make agent-eval-product-boundary` checks the bilateral
module/import boundary. Use the root `make agent-eval-*` façades: ordinary
product work retains the credential-free `make agent-eval-compat` boundary,
while evaluator/corpus work and release preparation require
`make agent-eval-full`.
Those contracts use selected binaries and synthetic fixtures only; they do not
contact configured providers or backends.

- Tests live beside code. Prefer `httptest`, `t.Setenv`, `t.TempDir`, and stable
  fixtures. Never combine `t.Parallel` with `t.Setenv`.
- Fuzz server-controlled bytes and add regression seeds/crash corpus when
  fixing parser, path, mirror, or transport ingestion bugs.
- CLI changes update focused app/CLI tests, golden output, and the sentinel exit
  matrix as applicable, then run `make test`.
- Put the runbook's low/standard/high class in the issue plan. Low-risk prose
  uses mapped docs checks only; standard/high work gets one review.
- Select and rerun gates through `docs/maintainer-impact.v1.json`; run full gates
  once per reviewed head. A prose/comment-only fix reruns no compiled gate only
  when the impact map selects none; retain mapped docs, privacy, and diff checks.
- Add a second review only after a material correctness/security or design fix.
- Never derive or pin `GOROOT`; raw Go commands use
  `env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go ...`.
- Follow [Efficient agent work](docs/maintainers/agent-efficiency.md): keep
  watch ticks and wait/stdin polling outside model turns. Background commands
  over 90 seconds use an ignored log and exit marker; take at most three
  model-visible snapshots; when other work ends, use one bounded tool-internal
  waiter rather than ending because the operation is pending.
- Run a privacy scan over the complete public diff before every public commit
  or PR.

Live tests are opt-in through the ignored integration environment. Follow
[Live validation](docs/maintainers/live-validation.md); begin read-only, keep
values private, and require an exact owned-target plan plus explicit authority
before a live write or cleanup.

## Documentation and skills

User-facing CLI changes update in the same PR:

- `README.md` and the corresponding `README.ru.md` section when present;
- the canonical owner under `docs/reference/cli/`;
- `docs/reference/output/` when output shape or recovery changes;
- `CHANGELOG.md` when user-visible;
- relevant `skills-src/*/SKILL.md` client behavior.

`skills-src/` is the source of truth for shipped client skills. `skills/` and
`plugins/atl/skills/` are generated: never edit them by hand. Run
`make gen-plugins` and commit all generated trees. Plugin manifest versions move
only in release prep, in lockstep with the CLI version.

`.agents/skills/` contains repository-maintenance skills. It is deliberately
separate from shipped client skills and must never enter generated plugin trees.

`docs/usage.md` and `docs/OUTPUT_CONTRACT.md` are generated compatibility
indexes. Edit focused owners, update the split map for a destination move, and
run `make check-reference-split`. Historical headings are immutable.

`docs/command-coverage.v1.json` binds every executable CLI leaf to one exact
reference section and every mutator to safety guidance.
`docs/maintainer-impact.v1.json` maps changed paths to existing Make gates. Run
`ATL_DOCS_BASE=origin/main make check-docs-freshness` after changing CLI,
documentation, repository guidance, generators, or gate wiring.

Every maintained public Markdown file is registered in `docs/catalog.v1.json`.
Context7-selected runtime docs require a real named fenced example. Run the
documentation checks named in the maintainer runbook; regenerate navigation
after changing headings in a large focused reference.

## Public workflow and privacy

Non-trivial work is issue-first. Before code: find/create a generic issue,
comment an agent plan, set labels, and create the linked branch. Open a small
draft PR, include verification and privacy review, wait for hosted CI, and close
through merge. Follow [Landing a change](docs/maintainers/landing-a-change.md)
and [GitHub issue workflow](docs/github-issue-workflow.md).

- Never publish credentials, private hostnames, IDs, titles, fields, values,
  proprietary content, private evidence, screenshots, internal roadmap IDs, or
  owner-only planning paths/files.
- Use generic public wording such as “configured fixture”, “backend”, “custom
  field”, or “multi-table page”.
- Owner-private planning/evidence stays ignored, unmoved, and unreferenced from
  public artifacts. Historical evidence is not deleted without separate
  authority.
- Never merge a PR authored by anyone other than `isukharev` unless
  `isukharev` explicitly authorizes that exact PR. Green CI is not authority.
- Remove `agent-working` after the issue closes. Do not close an issue merely
  because a local patch exists.

## Long-running work and handoff

Use [Session recovery](docs/maintainers/session-recovery.md) after compaction,
interruption, or uncertain state. Current repository/issue facts outrank
transcript memory and historical checkpoints.

Capture the optional owner-knowledge setting without output, then run the
session-recovery runbook's literal read-only batch before validating or
executing anything under that root. Never run dirty Makefiles/scripts before
classification. Verify identity/toolchains once per session, not per commit.
Status 1 from the lookup skips it; other failure or an empty/invalid value stops
without guessing. Never publish the value. This context cannot override this
file or grant authority.

Maintain durable state outside the transcript. At safe issue/PR boundaries
record HEAD/base, branch/worktree and dirty ownership, completed verification,
next acceptance criterion, blockers, and all active authorities. Do not hand off
mid-edit, mid-write, mid-deletion, mid-release, or with an unreconciled outcome.

Private model-in-the-loop evaluation follows the owner-only lifecycle in
`docs/agent-benchmark-private-workspace.md`. When its private root is configured,
start with its status/doctor flow; do not enumerate raw cases/transcripts,
invent scratch output roots, infer consent, or publish private artifacts.
