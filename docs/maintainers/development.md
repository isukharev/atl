# Development and verification

This runbook starts at a clean understanding of repository state and ends with
a stable integrated diff. Use [Landing a change](landing-a-change.md) after the
implementation is ready for public review. For background commands, bounded
output, and transient state, follow
[Efficient agent work](agent-efficiency.md).

## Read-only preflight

At the start of a session, execute the literal batched snapshot in
[Session recovery](session-recovery.md) once. It invokes only trusted system
commands; do not run a Make target, repository script, hook, or Go package before
the snapshot classifies dirty files that could change execution.

Do not repeat identity, toolchain, HEAD, or auth probes before each commit or
command. Refresh the snapshot after compaction, a branch/worktree switch, a
merge/rebase, an external-state change, or evidence that remembered state is
stale.

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
| Long-running agent execution | `docs/maintainers/agent-efficiency.md` |

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

## Simplify within the contract

Apply these criteria to the touched behavior during ordinary implementation
and review, without requiring a separate simplification request. The outcome
is fewer independently maintained mechanisms and clearer ownership. Line
counts, file counts, and one-liners are not quality targets; a clearer design
may need more lines or files. Keep this within the task's scope and existing
review, not an additional repository-wide audit or approval stage.

- **Find the existing solution first.** Trace the actual flow and its owner
  with `rg` and source reads. Check existing helpers, ports, and service
  patterns before adding an implementation, then consider the standard library
  and already-installed dependencies. Reuse requires matching semantics and
  the permitted dependency direction. For example, `internal/jiramap` shares
  Jira field mapping between REST and offline consumers without importing an
  adapter into the app layer.
- **Fix the cause at its owner.** Inspect affected callers before changing a
  shared helper; use a `go/ast` oracle when completeness must be demonstrated.
  Consolidate behavior only when its contract is shared. Similar-looking code
  with different retry, authorization, or recovery rules is not automatically
  duplication, and a scenario-specific condition need not move into a common
  helper.
- **Justify new mechanisms by current needs.** A new abstraction, dependency,
  configuration option, cache, or mode should serve a current consumer,
  contract, necessary boundary, or measured limitation. Defer speculative
  flexibility, not requested functionality. One implementation or caller does
  not make a port redundant: read/write capability separation and dependency
  isolation are current purposes. Before deleting apparently unused code or
  configuration, check supported external contracts as well as local callers.
- **Compare guarantees before replacing code.** A shorter library call must
  preserve relevant input handling, errors, cancellation, concurrency,
  durability, and platform behavior. For example, replacing
  `safepath.WriteFileAtomic` with `os.WriteFile` would lose atomic replacement;
  reuse the appropriate safe-path primitive instead. Consolidating guards must
  retain coverage of each entry path and failure outcome they protect.
- **Record real limits, not routine simplicity.** When a deliberate tradeoff
  has a known ceiling, explain why it fits the current bounds and name a
  measurable trigger for revisiting it in the owning code or existing issue.
  Ordinary reuse needs no special marker or separate debt ledger.
- **Review the simplification with its evidence.** For a proposed removal or
  replacement, identify the location, mechanism removed, replacement (if any),
  preserved guarantees, and focused verification. Prioritize reduced duplicate
  logic, state, and dependencies over deleted lines. Keep the risk- and
  impact-selected checks below; a smoke test alone does not establish safety,
  compatibility, or equivalence. If no justified simplification remains,
  continue the task without manufacturing a refactor.

## Classify the process contour

State one class in the public issue plan and escalate only when evidence expands
the risk:

- **Low** — prose/docs only, excluding repository instructions, security/safe
  writes, authority, release behavior, executable CLI/output contracts, and
  generated bytes. Run focused mapped documentation checks, `git diff --check`,
  and the privacy scan. Skip unrelated Go gates and independent model review.
- **Standard** — one package/subsystem, ordinary code/contract work, build
  tooling, or repository-maintainer instructions. Run focused checks, every
  gate selected by the impact map, and one independent integrated-diff review.
- **High** — cross-cutting architecture, security, durable format, live write,
  release, credential/redirect/retry boundary, or destructive behavior. Run the
  complete applicable contour, one independent review, and any explicit
  boundary-specific oracle.

Premerge CI is explicitly requested after review by moving the PR from draft to
ready. The resulting event binds exact PR head/base revisions. The maintained
impact policy selects hosted module-level gates; `ci-ready` is the required
aggregate under strict up-to-date protection. Focused local verification
supports iteration. Privacy, issue-first tracking, the reviewed revision, and
merge authority remain required for every class.

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

Iterate with the smallest focused local test, then request the selected full
gates once on the reviewed hosted head. Use `docs/maintainer-impact.v1.json`
(through `check-docs-freshness`) for local and hosted selection. Keep mapped
documentation checks, privacy review, and `git diff --check`. A changed head or
base needs a new reviewed ready transition; focused local reruns cover the
affected paths.

Treat the stable integrated diff as one verification boundary. Do not run a
local copy of a hosted gate against identical bytes merely to wait for CI
unless this repository requires local exact-head evidence. Combine changes
only when architecture ownership, process class, impact-selected gates, and
the release window remain the same; otherwise keep separate boundaries.

Prefer Make targets. Never derive `GOROOT` from `go env` or pin a downloaded
toolchain path. For a raw command use:

```sh
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go ...
```

| Change | Focused verification before full gates |
|---|---|
| App or CLI behavior | `go test ./internal/app ./internal/cli -count=1` |
| One Go package | `go test ./path/to/package -count=1` |
| Evaluator module or corpus | focused evaluator tests, then hosted `make agent-eval-full` once stable |
| Concurrency or shared state | focused `go test -race`, then the hosted race lane |
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

High-risk diffs finish with independent review and the complete applicable
hosted contour. Do not duplicate root-wide or evaluator full suites locally as
a default admission requirement. Use the bound ready-event procedure in
[Landing a change](landing-a-change.md#ci-and-merge).

For a change relative to a branch or commit, ask the maintained impact map for
the applicable existing gates:

```sh
ATL_DOCS_BASE=origin/main make check-docs-freshness
```

The same manifest annotates hosted lanes on its path rules. The hosted planner
reads only the committed base/head policies, unions their selections for every
changed path, and includes both sides of renames and copies plus deleted paths.
Legacy policy, an unclassified change, or a policy-file change widens selection
to full; invalid policy blocks the run. An exact `ci-full` label captured by the
ready event only widens the selection. The aggregate recomputes this plan before
accepting any intentionally skipped job.

| Change scope | Hosted contour |
|---|---|
| Prose | Maintainer, generated-tree, documentation, and package contracts |
| Generated/client skill bytes | Contracts and evaluator compatibility |
| Product module | Contracts, complete Ubuntu/macOS product race and coverage, lint, evaluator compatibility, vulnerability and CodeQL scans |
| Published product schemas | Product contour, including published/embedded schema parity tests |
| Evaluator module/corpus | Contracts, evaluator full, Ubuntu/macOS/Windows runtime checks, vulnerability and CodeQL scans |
| Devcontainer example | Contracts and pinned devcontainer smoke |
| Shared tooling/toolchains/workflows or unknown effects | Complete contour |

Product selection retains all root packages and the existing cross-package
coverage denominator. Package-level reverse-dependency selection is deferred
until an exhaustive oracle covers both modules, test imports, build tags,
platform files, embedded data, and generated inputs. Releases keep their full
gates; weekly CodeQL and the bounded main-push build smoke remain automatic.

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

The same manifest reviews every production Go file under `internal/` and
`scripts/` at or above 750 physical lines. Each such file must have either an
owned hotspot row or a bounded, explicitly rationalized exclusion; an exclusion
retains its own measured ceiling and becomes stale after the file drops below
the threshold. Selected safety-critical functions use `no_headroom`, which
requires the checked maximum to equal the current AST span exactly. The checker
reports hotspots, package totals, change-surface coverage, and observe-only
timing evidence as separate JSON members so a maintainer can lower the relevant
allowance mechanically after a split.

The evaluator is an independent nested module at `internal/agenteval`, with its
maintainer command at `internal/agenteval/cmd/agent-eval`. Root recursive Go
commands intentionally exclude it. Use the root `make agent-eval-*` façades:
ordinary product work retains the provider/backend-free
`make agent-eval-compat` compatibility boundary. Its existing filesystem-related
selection includes the stable-read production inventory and its import/counting
oracle because that inventory scans both modules, including product-only changes.
The maintainer contract prevents either oracle from silently leaving compatibility;
this does not select the complete evaluator suite. Evaluator/corpus changes use
`make agent-eval-contract` while iterating, and evaluator-impacting or release
work requires `make agent-eval-full`. The full facade includes the nested
module's build, unit, deterministic contract, race, lint, vet, vulnerability,
tidy, Windows, and bilateral `make agent-eval-product-boundary` gates. Do not
add a root module dependency, a root `replace`, or a tracked `go.work` to make
root recursive commands traverse the evaluator.

The ordinary and release `make agent-eval-full` contour retains every package
under one unsharded `-race -count=1` command with a 45-minute package failure
cap. A selected hosted full contour derives every active evaluator package and
top-level runnable from the exact committed Linux/amd64 race build. It runs
four deterministic nonempty root-package shards whose disjoint union is the
complete discovered inventory; the first shard also runs every non-root
package without a test selector. New packages and tests therefore enter the
race contour automatically or make discovery fail closed. The complete
non-race full gates run once alongside the shards.

Every hosted shard checks the source, toolchain, platform, package inventory,
actual runnable terminal events, and product-binary provenance before emitting
bounded content-free counts, digests, and timing. Product dependency discovery
matches the CGO-free build and certifies production and embedded inputs only;
the evaluator race discovery uses cgo and additionally certifies its compiled
test and test-embed inputs. The verbose-mode guard therefore applies only to
evaluator tests that the JSON race commands execute, not unexecuted product
dependency tests. Child processes receive a
narrow build-only environment rather than ambient `ATL_*`, backend, provider,
or fixture configuration. `GOENV=off`, `GOAMD64=v1`, and empty `GOFLAGS` and
`GOEXPERIMENT` override user Go settings and are attested with the Go version,
Linux/amd64 target, and cgo mode. This sanitizes the spawned process environment;
it does not remove arbitrary filesystem credentials or protect a hostile host.

Normal Go-level test skips remain distinct from workflow-job skips. Each
observed fuzz seed requires one run event and one separate pass or skip event;
the runner does not claim a static count for seeds it did not observe. Because
structured timing uses Go's verbose JSON event mode, active evaluator tests
must not branch on `testing.Verbose`; source certification rejects that semantic
drift. The required aggregate accepts the matrix only when every member
succeeds. Each test command retains the 45-minute test-binary cap, the runner
adds a 47-minute process-tree cap, and
the workflow retains a 75-minute outer cap. These are failure caps, not
performance targets; hosted durations remain observe-only maintainability
evidence and a measured dominant parent test requires a separately reviewed
partition change rather than an omission or caller-controlled selector.

The standalone compatibility facade runs its exact evaluator test selection
recursively across every active package plus the product wire and
selected-binary process and fixture oracles. Selected test names must be
globally unique; inactive, duplicate, or symlinked definitions fail closed so a
subpackage move cannot silently remove an oracle from the compatibility lane.
The full facade reaches those evaluator tests through its complete unit pass
and runs the other compatibility oracles separately, so each evaluator Go test
executes once in the unit lane and once in the race lane rather than receiving
an extra compatibility-only pass.

When an ad hoc evaluator executable is necessary, build it from the repository
root and run the resulting binary there so repository-relative inputs retain
their meaning:

```sh
GOWORK=off go -C internal/agenteval build -o /tmp/agent-eval ./cmd/agent-eval
/tmp/agent-eval inventory benchmarks/agent-eval
```

Run live targets only when the change and authority require them. Run a privacy
scan over the complete public diff, excluding unrelated owner changes. Review
the integrated diff once; add a bounded follow-up only after a material finding,
design change, or security-boundary fix.

The evaluator module's production and test imports are recursively reviewed in
`TestEvaluatorProductDependencyLedger`. Its Go package owners are the root
compatibility facade, neutral `core`, process `extension`, built-in
`profile/atl`, neutral semantic `agentadapter`, neutral execution policy and
reference leaf `executionbackend`, neutral causal-design leaf `experiment`,
neutral analysis leaf `analysis`, neutral append-only `lifecycle`, neutral
grading contract and evaluator `grading`, neutral bounded dispatch leaf
`scheduler`, format-specific
`interchange/agentskills`, owner-private bounded ATIF leaf
`interchange/atif`, immutable dataset-lineage leaf `lineage`, schema metadata
leaf `schemaregistry`, provider-free telemetry projection leaf `telemetry`,
provider-free promotion leaf `promotion`, review-only evolution-proposal leaf
`evolution`, and `cmd/agent-eval`. Their
machine-enforced direction keeps `core`, `extension`, `agentadapter`,
`executionbackend`, `experiment`, `evolution`, `lifecycle`, `promotion`, and
`scheduler` as leaves, keeps `evolution`, `lineage`, `interchange/atif`,
`schemaregistry`, and `telemetry`
dependency-free, permits `analysis` to import only
`experiment`, permits `grading` to import only `core` and `executionbackend`,
permits `profile/atl` to import `core` and `grading`, permits
`interchange/agentskills` to import only `core`, permits the root facade to
compose those owners including `analysis`, `promotion`, and
`scheduler`, and permits the
command to import only the exact root facade. The ledger records every
module-self file, lane, target, and alias, rejects dot or blank self imports,
and retains zero product-private imports. `TestNeutralCoreVocabularyContract`
separately keeps exported declarations and JSON tags in the reusable neutral
packages, including `analysis` and `scheduler`, free of product,
transport-route, and dynamic
registration vocabulary. Any package, edge, alias, or lane change requires
deliberate review, and evaluator paths select the package-boundary gate through
the maintainer impact map. `make agent-eval-compat` keeps the evaluator
wire/parser side and the lightweight product CLI
classification/exit-source side in the same required compatibility gate. Both
sides must match the content-free versioned CLI error wire fixture; changing
only the product or evaluator classification/recovery vocabulary, required
member set, action catalog, or refresh-capability catalog is therefore a
failing compatibility change.

The benchmark fixture schema and bounded synthetic HTTP backend are owned by
the evaluator package. Keep their strict decoding, route matching, ordered
request sequencing, stateful responses, accounting, and cleanup tests there so
an independent evaluator module does not import product test infrastructure.
The product onboarding checks retain their own small backend; do not make core
product tooling import the heavy evaluator package to share test helpers.

Evaluator fixture oracles that claim CLI or MCP compatibility should execute
the exact selected ATL binary through the evaluator-owned synthetic process
boundary. Keep executable identity and capability preflight, closed invocation
policy, an attested owner-only execution copy, allowlisted child environment,
stdout/stderr/protocol byte limits, deadlines, backend reconciliation, and
whole-process-tree cleanup in that boundary. Compatibility oracles must inspect
both structured MCP output and its user-visible text projection. Decode released
JSON into evaluator-owned wire DTOs; do not instantiate product app or MCP
implementation owners merely to remove an import from the dependency ledger.
Committed route expectations may pre-admit a dynamic evidence workflow, but
each follow-up invocation must still be derived from the selected binary's
strictly decoded result; a mismatch is a pre-backend refusal, not a reason to
relax the admission.

The evaluator also owns a frozen, content-free schema-v1 projection of the
released `atl capabilities` wire contract. It must not import product routing
definitions to validate run specs. `make agent-eval-compat` builds the current
binary and verifies its complete offline catalog against that projection;
internal ATL MCP runs repeat the same bounded process-boundary check against
the exact selected binary before any output, provider, or backend work.
When a reviewed capability-contract change intentionally updates that frozen
projection, run `make -C internal/agenteval gen-capability-catalog`, inspect the
complete fixture diff, and rerun the compatibility gate.

Codex skill routing crosses a separate package boundary. The product generator
owns source metadata validation and emits
`plugins/atl/skill-catalog.v1.json`; the evaluator owns a strict bounded decoder
and reconciles that companion with the exact regular-file tree. Keep the file
outside `plugins/atl/skills/` so provider discovery and the skill-tree digest do
not change. `make agent-eval-compat` verifies the current generated package
offline. Do not restore an evaluator import of product skill parsers or replace
this package-owned contract with a query to an independently versioned ATL
binary.

The released catalog is intentionally Codex-only: it is the versioned package
boundary consumed by the evaluator, while the sibling `skills/` tree remains a
generated client distribution without a second compatibility catalog. Both
generated trees still require exact regular non-executable `0644` file modes.
