# Agent evaluator substrate decision

Status: current as of 2026-08-05. Revisit only through the evidence gates below.

## Decision

Keep the independent Go evaluator module as the authoritative execution-policy,
attempt-lifecycle, artifact, and scoring boundary. Do not replace it with Harbor,
Inspect AI, or Promptfoo today.

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

## Required contract

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
