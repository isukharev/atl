# Agent evaluator substrate decision

Status: current as of 2026-08-09. Revisit only through the evidence gates below.

## Decision

Keep the independent Go evaluator module in the ATL monorepo as the authoritative
execution-policy, attempt-lifecycle, artifact, and scoring boundary. Do not
replace it with Harbor, Inspect AI, or Promptfoo today.

Inspect AI is the strongest candidate for a future optional execution adapter.
Harbor is the strongest candidate when container-task and separate-verifier
orchestration dominate. Promptfoo is useful as a declarative matrix or reporting
layer. None of them currently replaces ATL's immutable sampling, failed-attempt
retention, private workspace, process supervision, and deterministic scorer
without an ATL-owned wrapper that preserves most of the code we intended to
remove.

This is a measured no-change decision, not a permanent rejection. The evaluator
must also stop growing generic runner features that one of these substrates can
own.

## Repository placement

Retain the evaluator as the independent nested module at
`internal/agenteval`. This is a no-change decision about physical placement,
not permission to merge it back into the product module. The module boundary,
dependency direction, and independent gates remain mandatory.

The module now separates an in-memory neutral `core` from the explicit built-in
`profile/atl` adapter. The existing root package remains the ATL
compatibility/composition facade and still owns durable schemas, selected-binary
and route policy, execution, receipts, and private lifecycle code. This reduces
generic API coupling without changing the physical-placement decision or
claiming a standalone SDK. A bounded `interchange/agentskills` leaf may project
external authoring artifacts into `core`, but it owns no execution, lifecycle,
grader, or sandbox semantics. A recursive exact import ledger and vocabulary
oracle enforce the direction; additional process, lifecycle, and distribution
extraction remains subject to the issues and evidence gates that own it.
The neutral `agentadapter` leaf owns the semantic agent contract and normalized
observation graph. It imports no evaluator package. The root facade composes its
two immutable built-in adapters, binds their reviewed implementation identity,
executable, and configuration digests before spawn, and retains legacy launch
and receipt compatibility. This
is a typed seam inside the nested module, not a downloadable plugin registry or
an extraction decision.
The repository command now also contains a machine-tested pre-release
coordinator for local validation, deterministic grading, comparison,
inspection, and Agent Skills import/export. That source surface is unsigned,
does not expose the Go packages as an SDK, does not move the module, and does
not satisfy the distribution or external-consumer gates below.

The root facade now composes a neutral append-only `lifecycle` leaf. Direct
runs allocate their complete ordered roster before process entry; extension,
review, calibration, qualification, and selected-binary paths use the same
durable session; and aggregate reconstruction requires current receipts to bind
exactly one successful attempt. The store retains verified prefixes and makes
ambiguous tails terminal `unknown` to inspection rather than replayable work.
Private activation remains the stricter consent/order owner and keeps its
historical state bytes. This closes the current no-retry/failed-attempt
retention requirement without making the lifecycle package a public SDK or
granting an external substrate replay authority.

The nested module's schema-v1 extension protocol is also implementation
evidence, not extraction evidence. It defines a closed process seam for
profiles, agent adapters, execution backends, graders, and reporters, plus an
internal `verify-extension-protocol` maintainer command. The resulting report
is scoped to framing and protocol behavior. The current local host does not
enforce filesystem, network, credential, or general resource isolation for an
arbitrary child, so it cannot satisfy the credential-free prototype below or
the adoption gate's confinement requirements. Those claims remain blocked on
the qualified execution boundary owned by #1320.
The narrower internal `verify-agent-adapter` command layers the semantic
adapter contract and durable attempt receipts over that host. It proves that an
out-of-module process can negotiate the agent role, normal execution,
normalization, preparation, cancellation, bounded I/O, malformed-frame refusal,
cleanup reporting, and receipt identities without importing evaluator Go code.
It deliberately inherits the same confinement nonclaims and cannot be promoted
to whole-product compatibility by itself.

The neutral `executionbackend` leaf now supplies a bounded in-memory hermetic
reference and a separate `local_process` projection for the retained ATL
runner. The reference has no process, filesystem, environment, network, or
credential entry point; it operates on canonical content-addressed snapshots,
copies declared artifacts, isolates the verifier by cloning bytes, and binds
its plan into the append-only attempt lifecycle. The local projection instead
declares ambient network/credentials and every unproved isolation/resource
dimension, so it preserves current runner behavior without an assurance
upgrade. `verify-execution-backend` composes the semantic contract and plan
with the existing process protocol, but remains protocol-only for arbitrary
children. This is a qualified reference boundary, not evidence that the legacy
host or an external substrate is hermetic.

The neutral `grading` leaf owns closed grader, plan, and receipt schemas plus
deterministic checks, a bounded typed verifier DSL, and offline blinded-review
assessment. It imports only neutral core and execution-backend owners. The ATL
profile maps every legacy run-check kind into that closed deterministic catalog,
while the root compatibility facade preserves historical result bytes and adds
generic owner-private receipts for executable private panels. Out-of-tree
grader conformance proves framing, strict bounds, errors, cancellation, and two
identical synthetic grade cases; it remains protocol-only and cannot consume
hidden evidence through the unsandboxed local process host.

| Evidence | Retained nested module | Separate repository now |
|---|---|---|
| Build boundary | Own `go.mod`, dependency lock, command, linter, and full build/test/race/vet/vulnerability/Windows gates; root recursive Go commands do not enter the module | Preserves isolation, but does not remove any required evaluator gate |
| Product coupling | Zero product-private imports; one explicit process/JSON compatibility seam exercises the selected ATL binary and released public contracts | Replaces same-commit contract updates with version pins, artifact publication, and coordinated changes across repositories |
| Corpus and history | Scenario, run, synthetic fixture, released-wire, and generated-skill evidence stay atomically reviewable with the product revision they qualify | Requires either duplicated evidence or a cross-repository fetch and retention protocol before the same revision can be qualified |
| CI and releases | Eligible pull requests whose changed paths avoid the reviewed impact list use the smaller compatibility path; pushes, dispatches, impact-listed paths, and ATL releases retain the full evaluator gate | Could shorten this repository's jobs only after an independently published evaluator still proves the same release candidate |
| Ownership and distribution | Repository guidance declares one combined maintainer workflow and issue/review boundary; the evaluator command remains a repository-maintainer tool | Adds independent versioning, release security, dependency updates, triage, and compatibility support before a proposal has named a separate consumer and owner |

The completed decoupling work repeatedly needed atomic changes to evaluator
consumers and versioned product contracts. Moving the files now would not
remove the selected-binary compatibility gate, release qualification, or
evidence corpus. It would mainly exchange local path coupling for release and
coordination coupling. The smaller checkout and independent issue/release
surface are real benefits, but current evidence does not show that they exceed
that cost.

Reopen physical extraction only when all of these conditions have recorded
evidence:

1. A public proposal names the responsible maintainer, at least one consumer
   outside the ATL repository, the requested release cadence and support
   window, and a concrete workflow that the nested module cannot serve.
2. Starting after this decision, collect the first 20 consecutive merged pull
   requests that touch `internal/agenteval/**` over a period of at least eight
   weeks. An evaluator-contained pull request changes files only under
   `internal/agenteval/**` or `benchmarks/agent-eval/**`. At least 16 of the 20
   must be evaluator-contained. Record the pull-request numbers and changed-path
   classification in the extraction proposal; fewer than 20 is insufficient.
3. A credential-free prototype exposes
   `agent-eval compat verify --atl <binary> --bundle <bundle> -o json`. The
   content-addressed bundle and result schema are published with the prototype.
   The command must exit zero with `compatible:true` for both a SHA-pinned ATL
   binary built from the proposal's base commit and the signed binary from the
   highest published stable semantic GitHub release selected by
   `scripts/select-release-predecessor`. Record binary and bundle SHA-256
   values, source URLs, exit statuses, and complete bounded JSON results. The
   verifier must neither check out nor modify ATL source.
4. The proposed split removes the evaluator source and evaluator-only full-gate
   tooling from ATL while retaining one bounded, fail-closed product
   compatibility job; it does not duplicate or rewrite the evidence corpus.
5. The candidate repository has machine-enforced equivalents of the current
   full module gate, CodeQL, weekly Go dependency updates, and protected release
   workflow. The proposal links green full and CodeQL runs, the weekly
   dependency-update configuration, a release dry run, and snapshots of branch
   and release-environment protection. Its security and maintainer runbooks name
   the responsible account and response target, and tests prove historical
   artifact readability plus the current privacy boundary.

Repository extraction remains a separate initiative. Meeting these conditions
allows that initiative to be proposed; it does not authorize a move.

## Required contract

A standalone implementation or external substrate must first conform to the
[normative pre-release agent-eval contract](../reference/agent-eval/README.md).
That contract defines the generic product boundary and marks the bounded rows
implemented in repository source while this decision remains authoritative for
repository placement, current ATL-profile behavior, and the evidence gates for
extraction or adapter adoption.

A replacement or hybrid must preserve all of these properties:

- explicit non-interactive Codex and Claude Code policies without parsing human
  terminal output;
- immutable calibration, regression, and holdout samples (`n=1`, unchanged
  `n=3`, separate `n=1`) with no retry-fishing;
- one durable record for every started, failed, timed-out, or uncertain attempt;
- owner-controlled private workspace and content-minimized public projections;
- scorer independence from both the agent and the execution framework;
- hard request, process, output, deadline, and cost admission bounds;
- readable versioned historical artifacts;
- one closed content-addressed schema registry for artifact ownership,
  generations, privacy, bounds, resources, and reviewed migration edges;
- a materially smaller maintenance and release surface than the current module.

## Evidence boundary

The comparison separates three kinds of evidence:

1. **Official-source facts** are linked to the reviewed release, source, or
   current project documentation.
2. **Synthetic observations** come from credential-free local commands and are
   limited to the exact environment and versions named below.
3. **Inferences** are architecture decisions derived from those facts. They are
   not claims that an upstream project promises ATL's policy.

No model, provider, configured backend, private workspace, registry upload, or
hosted viewer was used for this comparison.

## Reviewed snapshots

| Substrate | Reviewed identity | Runtime floor | License |
|---|---|---|---|
| Evaluator-owned runner | ATL nested module at the repository revision containing this decision | Go version pinned by its `go.mod` | Repository license |
| Harbor | [v0.20.0](https://github.com/harbor-framework/harbor/releases/tag/v0.20.0), peeled commit [`459ff6e`](https://github.com/harbor-framework/harbor/commit/459ff6ec99417589b7f679d14ddf3b3f0ae4f1dc) | [Python 3.12+](https://github.com/harbor-framework/harbor/blob/v0.20.0/pyproject.toml) plus a container or cloud environment | Apache-2.0 |
| Inspect AI | tag [`0.3.252`](https://github.com/UKGovernmentBEIS/inspect_ai/tree/0.3.252), commit [`d105c61`](https://github.com/UKGovernmentBEIS/inspect_ai/commit/d105c61478c3fc86ff87d79b355c020869ee6a9b) | [Python 3.10+](https://github.com/UKGovernmentBEIS/inspect_ai/blob/0.3.252/pyproject.toml) | MIT |
| Inspect SWE | tag [`0.2.65`](https://github.com/meridianlabs-ai/inspect_swe/tree/0.2.65), commit [`e0b8349`](https://github.com/meridianlabs-ai/inspect_swe/commit/e0b83497f3c7f8126652e06c50069b5d756c5f6f) | Python 3.10+ and Inspect AI | MIT |
| Promptfoo | [0.122.0](https://github.com/promptfoo/promptfoo/releases/tag/0.122.0), tag commit [`7b898cb`](https://github.com/promptfoo/promptfoo/commit/7b898cbdb16205cb7f0e2994baa807d131eb2326) | Node 22.22+ | MIT |

The snapshots are comparison pins, not new production dependencies.

## Comparison

| Dimension | Evaluator-owned | Harbor | Inspect AI / Inspect SWE | Promptfoo | ATL-owned hybrid |
|---|---|---|---|---|---|
| Agent permissions | Exact provider policy, broker, and route admission | Codex wrapper hard-codes bypass; Claude defaults to bypass but exposes other modes | Inspect has approval policies, but pinned Inspect SWE hard-codes Codex bypass; Claude can opt into `auto` | Native agent providers have structured controls, but Promptfoo is not the OS/tool policy authority | ATL keeps admission; substrate receives only admitted work |
| Non-interactive evidence | Structured provider streams and typed ATL gateways | Structured session and stream logs are converted to ATIF | Agent Bridge and Inspect SWE expose structured events/transcripts | Native Codex/Claude providers and custom providers return structured results | Substrate output is a secondary observation, never the authority |
| Fixed sampling | Immutable plans bind exact cells and repetitions | `n_attempts` exists, but the `1/3/1` lifecycle is not immutable by construction | Epochs/attempts exist; eval-set retries default to repeated work | `repeat` exists; cache, resume, and retry features can change attempts | ATL binds stage manifests; substrate executes one named attempt |
| Failed attempts | Append-only attempt and recovery states | `max_retries=0` is available; enabled retry removes the earlier trial directory | Error logs are useful, but eval-set retry/cleanup is enabled by default | Error rows exist, but retry flows are mutable and error normalization can lose the first cause | ATL commits the attempt before launch and retains raw secondary artifacts |
| Privacy | Owner-only roots, bounded projections, isolated provider homes | Network and telemetry default on; logs/artifacts are intentionally rich | Sandboxing is opt-in; logs contain messages, outputs, events, and errors | Exports include config/prompts/outputs; redaction is documented as best-effort | ATL owns roots, environment allowlists, redaction, and publication |
| Scoring | Deterministic evaluator-owned scorers | Separate deterministic verifier is possible, but model judges are also supported | Custom scorers may be deterministic or model-backed and can be rescored | Deterministic assertions exist beside arbitrary scripts/model graders | ATL scorer remains an independent pinned binary |
| Bounds | Request, process, byte, time, and cost admission plus post-run accounting | Time/resource/concurrency controls; no reviewed global request or dollar hard stop | Rich message/token/turn/time/cost limits; model request retries need explicit disabling | Per-call/total time and concurrency; cost/request coverage varies by provider | Substrate limits are defense in depth under ATL's outer supervisor |
| Historical use | Multiple schema generations remain readable | Rich job/trial directories; retry deletion weakens history | Versioned `.eval`/JSON logs and conversion tools; scores/log metadata are mutable | Schema-v3 export/import; exported content is broad | Preserve original plus canonical bounded ATL projection and hashes |
| Operational cost | One Go module, one direct external module, but a large specialized code/test surface | Python 3.12, Docker/cloud providers, images, and agent installation | Python environment, Inspect plus Inspect SWE, usually Docker, and fast-moving CLI bridges | Large Node package and optional agent SDKs; lightest local bootstrap here | Worthwhile only when it deletes more ATL code than the adapter adds |

## Official-source findings

### Harbor

Harbor has a strong task/environment/verifier model, explicit CPU and memory
policies, local and cloud sandboxes, network modes, ATIF trajectories, and rich
trial artifacts. See its [task and network contract](https://www.harborframework.com/docs/tasks),
[artifact contract](https://www.harborframework.com/docs/run-jobs/results-and-artifacts),
and [ATIF documentation](https://www.harborframework.com/docs/agents/trajectory-format).

The safe ATL profile is not the default. Network mode defaults to `public`,
usage telemetry is enabled unless `HARBOR_TELEMETRY=off`, and upload is a
separate explicit operation. More importantly, the reviewed built-in
[Codex adapter](https://github.com/harbor-framework/harbor/blob/v0.20.0/src/harbor/agents/installed/codex.py)
passes `--dangerously-bypass-approvals-and-sandbox`. The reviewed
[Claude Code adapter](https://github.com/harbor-framework/harbor/blob/v0.20.0/src/harbor/agents/installed/claude_code.py)
defaults to `bypassPermissions`, although callers can select another supported
mode. Both derive trajectories from vendor structured session/output logs.

Harbor defaults to one attempt and supports zero retries, but an enabled trial
retry removes the previous trial directory before rebuilding it. Artifact
collection is explicitly best-effort. These are reasonable benchmark-runner
choices, but not ATL's immutable evidence contract.

### Inspect AI and Inspect SWE

Inspect provides the broadest useful orchestration surface: external
[Agent Bridge](https://inspect.aisi.org.uk/agent-bridge.html), fine-grained
[approval policies](https://inspect.aisi.org.uk/approval.html), Docker and
extension [sandboxes](https://inspect.aisi.org.uk/sandboxing.html), rich
[limits](https://inspect.aisi.org.uk/setting-limits.html), and versioned
[evaluation logs](https://inspect.aisi.org.uk/eval-logs.html).

Inspect SWE directly supports Codex and Claude Code. At the reviewed pin, its
[Codex adapter](https://github.com/meridianlabs-ai/inspect_swe/blob/0.2.65/src/inspect_swe/_codex_cli/codex_cli.py)
unconditionally passes `--dangerously-bypass-approvals-and-sandbox`; core
Inspect approval policy does not remove that inner bypass. Its Claude adapter
can select `permission_mode="auto"`, while bypass remains the default.

They therefore still require a new safe adapter or a separately proven outer
sandbox before ATL could execute either agent through this substrate. Claude
refusal and uncaught-error retries also default to three. Inspect
eval-set retries failed tasks up to ten times by default and can remove failed
logs after a later success. Core Inspect approval governs Inspect tool calls;
it does not by itself prove the bridged CLI's internal tool policy. Logs and
sample buffers retain rich content, and cost limits depend on complete pricing
and usage data.

### Promptfoo

Promptfoo has the lightest declarative provider/assertion matrix and the
largest ready-made reporting surface. It supports deterministic assertions,
custom structured providers, fixed concurrency and timeouts, repeated tests,
JSON/JSONL export, and native structured
[Codex app-server](https://www.promptfoo.dev/docs/providers/openai-codex-app-server/)
and [Claude Agent SDK](https://www.promptfoo.dev/docs/providers/claude-agent-sdk/)
providers.

Its defaults are optimized for evaluation throughput rather than immutable
experiments: disk cache is enabled, concurrency defaults above one, timeouts
default to unlimited, and retry layers are available. `maxRetries: 0`,
`--no-cache`, and concurrency one can close part of that gap. Exports still
include prompts, variables, outputs, errors, and configuration; the
[output documentation](https://www.promptfoo.dev/docs/configuration/outputs/)
calls redaction best-effort. Telemetry and sharing need their own explicit
disable controls.

## Credential-free synthetic observations

The comparison host had Node 24.18 but no Docker, `uv`, Python package installer,
or working Python virtual-environment bootstrap. Harbor therefore stopped at its
documented Python 3.12/container prerequisites. Inspect's isolated environment
creation stopped because `ensurepip` was unavailable. These are retained
environment findings, not upstream correctness failures, and neither framework
was credited with an unexecuted local pass.

Promptfoo 0.122.0 executed a deterministic custom provider with telemetry,
updates, sharing, remote generation, caching, and concurrency disabled. The
fixed calibration/regression/holdout shape produced exactly five successful
rows (`1 + 3 + 1`) in 89 ms with zero reported token use and cost. The schema-v3
JSON export also contained the complete synthetic config, prompts, and outputs.

A separate one-shot failure probe configured `maxRetries: 0` and returned a
synthetic rate-limit error. The provider was invoked exactly once and the CLI
exited non-zero, so no replay occurred. After the five-second per-case deadline,
however, the durable result contained a timeout and the console diagnostic
contained `Queue disposed`; the original synthetic cause was not preserved in
either primary surface. The attempt is intentionally not retried. This is
enough to reject Promptfoo as the authoritative failure classifier without an
ATL wrapper.

The installed evaluator module remains comparatively cheap to bootstrap: it has
one direct external Go module and builds a roughly 10.8 MB stripped command in
this environment. It is not small internally: the reviewed tree contains about
47,000 non-test and 86,000 test lines of Go. That size justifies continued
extraction pressure, but not replacement with a weaker evidence lifecycle.

## Adoption gate

Open a new adapter initiative only when all of these can be demonstrated in a
pinned, credential-free environment:

1. One synthetic Codex and one synthetic Claude Code attempt use the intended
   non-interactive permission policy, structured protocol, and exact pinned CLI.
2. No framework, agent, model request, transport, or HTTP retry occurs; one
   injected failure remains one immutable failed attempt with its original
   classification.
3. The agent sees only a disposable fixture, has no unintended host files or
   credentials, and cannot reach an unlisted network destination.
4. Request, output, child-process, time, token, and cost bounds fail closed and
   terminate descendants.
5. The same captured result scores byte-identically twice with the evaluator's
   independent deterministic scorer.
6. Original upstream artifacts and a bounded versioned ATL projection both
   remain readable after an upstream upgrade.
7. The integrated adapter removes at least half of the generic runner,
   sandbox, trajectory, and lifecycle implementation that it replaces. Moved or
   duplicated policy does not count as removal.

Until that gate passes, use external frameworks only for bounded research or as
non-authoritative views. Refresh this document from official sources before any
adoption decision; do not silently convert current defaults into permanent
facts.
