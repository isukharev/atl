# Standalone agent-eval contract

Status: normative pre-release contract. Contract version: `0.1.0-pre-release`. No standalone distribution currently conforms to it.

[Documentation home](../../README.md) ·
[Evaluation methodology](../../agent-benchmarking.md) ·
[Evaluator substrate decision](../../maintainers/agent-evaluator-substrates.md) ·
[Roadmap](../../../ROADMAP.md)

This document reserves the public boundary for a local-first standalone `agent-eval` product. “Must”, “must not”, “should”, and “may” are normative. The contract does not make an unreleased command available, expose an internal Go API, or authorize provider, backend, network, extraction, or release work.

## Product status

| Surface | Current status | Compatibility promise |
|---|---|---|
| Repository maintainer command at `internal/agenteval/cmd/agent-eval` | Implemented for ATL repository evaluation | Current command names, Go flags, plain-text errors, helper executable names, environment registry, and private-workspace operations are internal unless this document explicitly admits them |
| Reserved standalone `agent-eval` product | Pre-release and not implemented | This document reserves its operation, schema, output, authority, and compatibility rules; conformance starts only when a distribution implements and tests them |

The maintainer command remains valid implementation evidence, not an accidental public CLI. It currently writes plain diagnostics to stderr and normally maps every error to exit `1`. The production CLI, global flags, structured errors, and exit mapping are deferred to [#1315](https://github.com/isukharev/atl/issues/1315). Until that work passes conformance, callers must describe the standalone surface as **reserved**, not supported or stable.

## Stability vocabulary

| Status | Meaning |
|---|---|
| `stable` | Released, conformance-tested, and covered by this compatibility and deprecation policy |
| `experimental` | Explicitly opt-in and namespaced; may change in a minor release but must not silently alter stable artifact meaning |
| `internal` | Repository or implementation detail with no public compatibility promise |
| `reserved` | Normatively shaped here but not shipped; callers must not probe for it or treat its spelling as implemented |

A surface is not stable merely because it exists in source, appears in help, has a schema version, or has historical fixtures. A release must identify each stable surface in the conformance registry. Unmarked extensions are rejected rather than inferred to be experimental.

## Operations and user journeys

The standalone CLI reserves these command families. #1315 owns their exact flags and help. Each row below is an independent authority ceiling, not an implicit grant. `Y` means the dimension may be admitted only from the explicit invocation and resolved plan; `N` means the operation must be structurally unable to acquire it.

| ID | Mode | `authority` | `local_read` | `local_write` | `process_spawn` | `provider_contact` | `backend_contact` | `network` | `credential_access` | `private_workspace_access` |
|---|---|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `capabilities` | `default` | `none` | N | N | N | N | N | N | N | N |
| `compare` | `default` | `local_read` | Y | N | N | N | N | N | N | N |
| `compat verify` | `provider-free` | `verifier_execution` | Y | N | Y | N | N | N | N | N |
| `grade` | `deterministic` | `verifier_execution` | Y | N | Y | N | N | N | N | N |
| `grade` | `judge` | `provider_execution` | Y | N | Y | Y | N | Y | Y | N |
| `import` | `default` | `local_write` | Y | Y | N | N | N | N | N | N |
| `init` | `default` | `local_write` | N | Y | N | N | N | N | N | Y |
| `inspect` | `default` | `local_read` | Y | N | N | N | N | N | N | N |
| `migrate apply` | `default` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `migrate preview` | `default` | `local_read` | Y | N | N | N | N | N | N | Y |
| `plan` | `default` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `reconcile` | `evidence-only` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `report` | `default` | `local_read` | Y | N | N | N | N | N | N | N |
| `resume` | `default` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | Y |
| `run` | `default` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | N |
| `schema inspect` | `default` | `local_read` | Y | N | N | N | N | N | N | N |
| `validate` | `default` | `local_read` | Y | N | N | N | N | N | N | N |
| `version` | `default` | `none` | N | N | N | N | N | N | N | N |

The user journeys follow directly from those ceilings. `init` creates only the explicit project. `import` writes a candidate without execution. `plan` writes an immutable plan to an explicit destination. `reconcile` may append only content-minimized local proof. `compare` and `report` consume existing local artifacts. Migration preview reads; apply writes a new explicit destination. `compat verify` may spawn an isolated verifier but remains provider-, backend-, network-, credential-, and private-workspace-free. Deterministic grading has the same no-contact verifier boundary. Judge grading is a distinct, explicit mode: it may receive provider, network, and credential authority, but never product-backend or private-workspace authority. `run` and `resume` receive only the individually admitted execution dimensions, and resume remains subject to the no-replay lifecycle below.

Commands are non-interactive: no prompts, pagers, browsers, confirmation reads from stdin, or default provider selection. A local mutation requiring confirmation must receive all confirmation material in the original invocation and fail before writing when it is absent.

Every row whose contact or access dimension is `N` must be structurally unable to construct or discover that authority. In particular, `import`, `validate`, `plan`, `reconcile`, migration, `compare`, `report`, `capabilities`, deterministic grading, and provider-free `compat verify` cannot construct a provider, configured product backend, or network client. Dry-run is not a substitute for this boundary.

## Configuration and authority

Configuration resolves in this exact high-to-low order:

1. explicit command flags;
2. the project configuration selected for that invocation;
3. environment values from an explicitly enabled, closed allowlist.

Project configuration is loaded only from an exact `--config` path or the fixed configuration path inside an exact `--project` root. The CLI does not walk parents, inspect unrelated repository metadata, or merge user or system configuration implicitly. Relative paths resolve against the owning configuration file.

Environment input is disabled by default. Enabling it names a reviewed projection whose accepted names and value classes are visible in `agent-eval capabilities`. Unknown projected keys, malformed values, and duplicate configuration keys fail closed. The projection must not admit the current internal `ATL_EVAL_*` registry wholesale.

Selection and authority remain separate:

- naming a component, URL, credential reference, or private root does not authorize using it;
- selecting network-capable execution does not grant a destination, method, credential, retry, or write policy;
- configuration and environment cannot widen the authority admitted for the command;
- a component may reduce authority or report it unsupported, but cannot grant authority to itself.

No command discovers any of these ambiently: provider credentials, an ATL backend, a private evaluation root, proxy settings, cloud metadata, or network authority. A private root is available only to a row with `private_workspace_access:Y` and only through an exact invocation input. Credentials are resolved only by an explicitly selected adapter after admission and never enter plans or public durable artifacts. Secret bytes are never echoed in errors, previews, or publication-safe digests.

## Output and error contract

The standalone target uses JSON by default. A successful non-streaming command writes exactly one newline-terminated JSON object to stdout. Diagnostics go to stderr and never contaminate stdout. Human output requires explicit `--output text`; an unsupported projection fails before configuration, credentials, process launch, or network access.

Stable success objects contain `schema`, `schema_version`, `contract_version`, `command`, `status`, and command-specific `result`. Stable error objects contain `schema`, `schema_version`, `contract_version`, `error`, `exit_class`, `kind`, and `retry_safe`. Typed local conditions—not provider or backend prose—select the error class.

| Code | `exit_class` | Meaning |
|---:|---|---|
| `0` | `success` | Operation completed; a task may still have a failing result recorded as data |
| `1` | `internal_error` | Unexpected implementation failure or violated invariant |
| `2` | `usage_error` | Invalid command, flag, argument, or output mode |
| `3` | `configuration_error` | Missing, malformed, conflicting, or forbidden configuration |
| `4` | `input_error` | Missing, malformed, unsafe, or unsupported input artifact |
| `5` | `compatibility_error` | Component, protocol, capability, schema, or profile is incompatible |
| `6` | `policy_denied` | Requested authority is outside the admitted policy |
| `7` | `authentication_failed` | An explicitly selected provider or backend rejected its credential |
| `8` | `execution_failed` | An admitted component failed with a known terminal outcome |
| `9` | `check_failed` | Validation, grading, comparison, or conformance completed and failed |
| `10` | `outcome_unknown` | A non-replay-safe operation may have occurred and cannot be classified safely |
| `11` | `interrupted` | Operation stopped before non-replay-safe commitment and is safe to resume |

Unsupported or unknown required capability is `compatibility_error`, not a task failure. Policy refusal is not authentication failure. A known failed attempt may return `execution_failed`; its durable identity is not proof a retry is safe. The maintainer CLI is nonconforming until #1315, and a wrapper must not manufacture conformance by parsing its error strings.

## Compatibility policy

`contract_version` follows Semantic Versioning. The current value is `0.1.0-pre-release`; it describes this normative pre-release contract and does not make a distribution stable. Compatibility starts at the release boundary named `first-conforming-signed-standalone-release`, never merely when a command, fixture, or unsigned archive exists. After `1.0.0`, patch releases may clarify or add compatible diagnostics, minor releases may add optional stable members while preserving defaults and readers, and incompatible stable changes require a new major version.

The compatibility registry records `minimum_deprecation_days: 180` and `minimum_deprecation_releases: 2`. The release count means two later stable minor releases in the same major after notice; patch releases and pre-release builds do not count. Both minima must elapse, and removal still requires the next major. A security issue may disable execution sooner, but safe inspection, reporting, or migration remains when it does not recreate the vulnerability, and historical meaning is never silently reinterpreted.

Readers in a supported major line must read every stable artifact that line emitted. A later reader either preserves meaning directly or offers an explicit previewable migration. Future schemas are preserved and refused, never treated as empty, downgraded, or partially decoded as current.

Compatibility is the tuple `(standalone-core, contract, atl-profile, agent-adapter, execution-backend, grader, reporter, artifact schemas, process protocols)`. `compatible:true` requires every required tuple member and capability to be known and supported; omission is not compatibility.

## Component responsibilities

| Component | Owns | Must not own or infer |
|---|---|---|
| `standalone-core` | Registry, strict decoding, admission, planning, durable attempt identity, aggregation, compatibility, and migration orchestration | ATL semantics, provider credentials, sandbox implementation, grader truth, or reconstruction of missing evidence |
| `atl-profile` | ATL capability vocabulary, selected-binary compatibility, ATL fixtures/projections, and legacy artifact mappings | Generic vocabulary, provider launch, replay, or standalone release authority |
| `agent-adapter` | Explicit launch, bounded structured exchange, agent identity, activation evidence, and usage receipts | Admission, backend authority, scoring, retries, promotion, or privacy classification |
| `execution-backend` | Filesystem, process, network, deadline, cleanup, and resource enforcement with a receipt | Agent selection, grader truth, retry policy, or authority beyond the admitted plan |
| `grader` | Deterministic checks or an explicitly isolated judge with coverage and provenance | Runner control, hidden retries, source mutation, missing-as-zero coercion, or promotion authority |
| `reporter` | Content-minimized projections of validated artifacts | Execution, migration, evidence synthesis, or wider visibility |

External substrates are adapters. They do not become admission, privacy, scoring, promotion, or lifecycle authority merely because they execute a task or render a report.

## Capability negotiation

Capabilities are namespaced, versioned strings. Each required component reports exactly one state for every capability considered by the plan:

| State | Meaning | Required-capability result |
|---|---|---|
| `supported` | Implemented under reported constraints and conformance identity | May proceed if every other check passes |
| `unsupported` | Affirmatively not implemented | Fail before commitment or execution |
| `unknown` | Support cannot be proved or the core does not recognize the claim | Fail before commitment or execution |
| `not_applicable` | Outside this component role for this plan | Valid only when the responsibility matrix agrees; otherwise fail |

Negotiation never guesses from a name, version, installed file, or prior success. Constraint mismatches are incompatible. Downgrading a plan, dropping a requirement, or substituting a component requires a new plan identity.

This synthetic example is the reserved compatibility result shape:

```json named-agent-eval-compatibility-result
{
  "schema": "agent-eval/compatibility-report",
  "schema_version": 1,
  "contract_version": "0.1.0-pre-release",
  "command": "compat verify",
  "status": "failed",
  "components": [
    {
      "role": "agent-adapter",
      "id": "example.agent",
      "protocol_version": 1,
      "capabilities": [
        {"id": "agent.skills.load_evidence", "state": "supported"},
        {"id": "agent.skills.activation_evidence", "state": "unsupported"}
      ]
    }
  ],
  "compatible": false,
  "reasons": [
    {
      "kind": "unsupported_capability",
      "component": "example.agent",
      "capability": "agent.skills.activation_evidence"
    }
  ]
}
```

## ATL-profile compatibility namespace

Existing evaluator artifacts retain their bytes and meaning under logical identities of the form `atl-profile/<family>@<schema-version>`, for example `atl-profile/result@8`. Registry metadata is not inserted into historical JSON, included in an old digest, or grounds for rewriting a file. The test-only compatibility ledger has these exact families:

| Family | Readable | Emitted | Executable | Current ATL-profile disposition |
|---|---|---|---|---|
| `atl-profile/aggregate@7` | — | v7 | — | `write_only_projection`; `content_minimized`; `compare_only` |
| `atl-profile/capability-catalog@1` | v1 | v1 | v1 | `preserve`; `public`; `explicit` migration |
| `atl-profile/observation@5` | v5 | v5 | v5 | `preserve`; `content_minimized`; `explicit` migration |
| `atl-profile/qualitative-panel@1` | v1 | v1 | v1 | `preserve`; `owner_private`; `explicit` migration |
| `atl-profile/result@3..8` | v3–v8 | v8 | v3–v8 | `preserve`; `content_minimized`; `explicit` migration |
| `atl-profile/review@1..2` | v1–v2 | v2 | v1–v2 | `preserve`; `owner_private`; `explicit` migration |
| `atl-profile/rubric@1` | v1 | — | v1 | `preserve`; `public_or_private`; `explicit` migration |
| `atl-profile/run-spec@5..7` | v5–v7 | v7 | v5–v7 | `preserve`; `public_or_private`; `explicit` migration |
| `atl-profile/scenario@1` | v1 | v1 | v1 | `preserve`; `public_or_private`; `explicit` migration |
| `atl-profile/synthetic-root-aggregate@2` | — | v2 | — | `write_only_projection`; `content_minimized`; `compare_only` |
| `atl-profile/synthetic-run-receipt@1` | v1 | v1 | v1 | `preserve`; `content_minimized`; `explicit` migration |
| `atl-profile/private-workspace@1..4` | v1–v4 | v4 | v4 | `preserve`; `owner_private`; `partial_explicit` migration; v1–v3 are readable only |
| `atl-profile/private-plan@1..9` | v1–v9 | v9 | v9 | `preserve`; `owner_private`; `compare_only`; v1–v8 are readable only |
| `atl-profile/activation-reference@1..2` | v1–v2 | v2 | — | `preserve`; `owner_private`; `compare_only` reference envelope |
| `atl-profile/activation-report@1..2` | — | v1–v2 | — | `write_only_projection`; `content_minimized`; `compare_only` |

Here, “readable” means accepted by the exact generation reader, “emitted” means the maintained evaluator can write that generation, and “executable” means the generation may enter its existing execution path. An empty column is a deliberate refusal, not missing registry data. In particular, a write-only aggregate or report can be compared only under its named projection contract; it cannot be reintroduced as source evidence or treated as a readable canonical artifact.

[#1318](https://github.com/isukharev/atl/issues/1318) owns the exhaustive registry. Until then, existing decoders and tests remain authoritative for exact readable generations. This namespace does not make every readable artifact executable, comparable, promotable, or public.

The internal `ATL_EVAL_*` registry, wrapper basenames, broker records, launch arguments, and package-local Go types remain internal even when tests serialize them.

## Standalone artifacts and migration

| Family | Required role |
|---|---|
| `agent-eval/scenario` | Task, required capabilities, checks, data class, and hard authority/resource budgets |
| `agent-eval/run-spec` | Immutable scenario, experiment cell, component, version, repetition, and policy binding |
| `agent-eval/observation` | Measured evidence with explicit coverage and provenance |
| `agent-eval/attempt` | Durable lifecycle and terminal or unknown outcome for one admitted attempt |
| `agent-eval/result` | Deterministic grading bound to scenario, observation, and grader |
| `agent-eval/aggregate` | Comparable cohort summary with explicit denominators and exclusions |
| `agent-eval/report` | Privacy-tiered projection of validated source artifacts |
| `agent-eval/adapter-message` | Process message reserved for #1314; not stable yet |

Every stable artifact has a closed `schema` and positive integer `schema_version`. Contract, producer, component, and source identities are separate fields. Strict decoders reject duplicate keys, trailing values, invalid JSON encoding, unknown required vocabulary, and fields outside an explicit namespaced extension object. Invalid known schemas and unknown future schemas fail before mutation or execution.

Source bytes are immutable evidence. Import may create a normalized candidate but records the source identity and digest and never overwrites it. Migration never relabels an old hash as belonging to new bytes.

Migration is preview/apply:

1. `migrate preview` strictly reads one named source and target without provider, backend, private-root discovery, or network access.
2. It reports source/candidate identities, domain-separated digests, transformations, preserved counts, and any loss or eligibility change without sensitive content.
3. `migrate apply` requires the unchanged source, target, preview digest, explicit destination, and explicit apply confirmation in one invocation.
4. Apply revalidates, writes a new artifact atomically, and records provenance; it never silently replaces the source.
5. Lossy conversion requires a separately named projection that cannot be mistaken for the source.

## Process boundary and durable attempts

The planned public extension seam is bounded process/JSON, not the Go module, shell prose, or environment conventions. It requires explicit framing and version negotiation; component role/identity; capability constraints; byte, deadline, and process bounds; cancellation; structured terminal errors; and process-tree cleanup. Stdout is protocol-only and stderr cannot change an outcome.

[#1314](https://github.com/isukharev/atl/issues/1314) owns message schemas and fixtures. Until it lands, no wrapper, launcher, proxy, or environment variable is a stable adapter protocol, and `agent-eval/adapter-message` remains reserved.

The state registry is closed:

| State | Phase | Terminal | Automatic resume | Meaning |
|---|---|---:|---:|---|
| `canceled` | Derived | Yes | No | Cancellation has the proof required for the predecessor phase |
| `committed` | Postcommit | No | No | Compatibility, policy, and bounds passed; the attempt identity is consumed before spawn |
| `failed` | Postcommit | Yes | No | Terminal execution or definitive spawn-failure proof establishes failure |
| `planned` | Precommit | No | Yes, with proof | Immutable inputs exist, but commitment has not been proved |
| `policy_denied` | Precommit | Yes | No | A durable policy refusal and complete ledger prove commitment did not occur |
| `running` | Postcommit | No | No | The bounded process or external attempt identity is durably bound and active |
| `spawning` | Postcommit | No | No | A committed component process or external action is being created |
| `succeeded` | Postcommit | Yes | No | A bound terminal receipt and termination proof establish success |
| `timed_out` | Derived | Yes | No | A deadline has the proof required for the predecessor phase |
| `unknown` | Derived | Yes | No | Safe non-execution or terminal classification cannot be proved |
| `unsupported` | Precommit | Yes | No | A durable capability refusal and complete ledger prove commitment did not occur |

Proof predicates are durable, attempt-bound facts with these closed meanings:

- `complete_ledger` is a stable reread of every record for the attempt identity; `no_commit` proves that ledger contains no durable commitment; and `immutable_plan` proves the plan bytes, identity, and authority are unchanged.
- `durable_commit` consumes the attempt identity and binds admitted policy and authority; `durable_spawn_intent` precedes component entry; and `durable_process_identity` binds the complete owned process tree or external attempt.
- `durable_cancel`, `durable_deadline`, `durable_policy_refusal`, and `durable_capability_refusal` are durable trigger or refusal records bound to the attempt.
- `definitive_spawn_failure` proves launch failed before component entry; `non_execution_proof` proves the selected process or external action did not begin and cannot still begin.
- `terminal_receipt` is a structured terminal result bound to the attempt; `termination_proof` proves the complete owned process tree or external action cannot continue.
- `incomplete_terminal_evidence` is a content-minimized evidence digest proving that the records needed for safe non-execution or terminal classification are absent, conflicting, or incomplete.

The allowed transition relation is also closed:

| From | To | Required proof |
|---|---|---|
| `committed` | `canceled` | `durable_cancel` + `non_execution_proof` |
| `committed` | `failed` | `definitive_spawn_failure` + `non_execution_proof` |
| `committed` | `spawning` | `durable_spawn_intent` |
| `committed` | `timed_out` | `durable_deadline` + `non_execution_proof` |
| `committed` | `unknown` | `incomplete_terminal_evidence` |
| `planned` | `canceled` | `complete_ledger` + `durable_cancel` + `no_commit` |
| `planned` | `committed` | `durable_commit` |
| `planned` | `policy_denied` | `complete_ledger` + `durable_policy_refusal` + `no_commit` |
| `planned` | `timed_out` | `complete_ledger` + `durable_deadline` + `no_commit` |
| `planned` | `unknown` | `incomplete_terminal_evidence` |
| `planned` | `unsupported` | `complete_ledger` + `durable_capability_refusal` + `no_commit` |
| `running` | `canceled` | `durable_cancel` + `termination_proof` |
| `running` | `failed` | `terminal_receipt` + `termination_proof` |
| `running` | `succeeded` | `terminal_receipt` + `termination_proof` |
| `running` | `timed_out` | `durable_deadline` + `termination_proof` |
| `running` | `unknown` | `incomplete_terminal_evidence` |
| `spawning` | `canceled` | (`durable_cancel` + `non_execution_proof`) or (`durable_cancel` + `termination_proof`) |
| `spawning` | `failed` | (`definitive_spawn_failure` + `non_execution_proof`) or (`terminal_receipt` + `termination_proof`) |
| `spawning` | `running` | `durable_process_identity` |
| `spawning` | `succeeded` | `terminal_receipt` + `termination_proof` |
| `spawning` | `timed_out` | (`durable_deadline` + `non_execution_proof`) or (`durable_deadline` + `termination_proof`) |
| `spawning` | `unknown` | `incomplete_terminal_evidence` |

Every unlisted state pair is rejected. Every terminal state, including `unknown`, is absorbing. Only `planned` work may resume automatically, and only with `complete_ledger`, `immutable_plan`, and `no_commit`. No state permits same-ID replay. Reconciliation appends content-minimized local proof without changing the original state; it may support an explicit plan decision for a new attempt identity, never refine, resume, or repeat the original attempt. In particular, cancel or timeout after commitment becomes `canceled` or `timed_out` only with the listed `non_execution_proof` or `termination_proof`; without it, the only safe terminal transition is `unknown`.

[#1317](https://github.com/isukharev/atl/issues/1317) owns ledger and recovery implementation. This document freezes transition meaning but does not claim current conformance.

## Missing evidence and provider-free conformance

| Representation and state | Member rule | Meaning |
|---|---|---|
| Standalone `observed` | `state:"observed"`, `coverage:true`, and a numeric `value` are required; zero is valid | The measurement boundary proved the value |
| Standalone `unknown` | `state:"unknown"`; `coverage` and `value` are absent | The boundary could not establish the value safely |
| Standalone `unsupported` | `state:"unsupported"`; `coverage` and `value` are absent | The selected component affirmatively cannot provide the metric |
| Standalone `not_applicable` | `state:"not_applicable"`; `coverage` and `value` are absent | The plan proves the metric has no meaning for this cell |
| Legacy ATL-profile `unknown` or `unsupported` | `coverage:false,value:0` is accepted only as the historical paired placeholder | No numeric observation exists |
| Missing required entry | No members | Coverage is incomplete and the relevant check fails |
| Missing optional entry | No members | Valid omission; it does not alter any required denominator |

Numeric zero is a measurement only with `state:"observed"` and `coverage:true`. The legacy `coverage:false,value:0` pair is a compatibility placeholder, never an observed zero and never input to a numeric summary. `not_applicable` is an explicit state, not missing coverage. Aggregates report each state count, summarize only observed values, and never change denominators or impute silently. Capability state, attempt outcome, metric state, coverage, and numeric value are independent.

A stable distribution publishes content-addressed synthetic fixtures. Provider-free conformance proves JSON/error/exit contracts; configuration precedence; no ambient authority discovery; pre-execution capability refusal; historical readability and future rejection; migration binding; missing-versus-zero behavior; component confinement; deterministic rereads; and no replay. This issue freezes the closed transition and proof vocabulary in test-only contract data; [#1317](https://github.com/isukharev/atl/issues/1317) owns its production ledger, recovery, and runtime conformance.

The future provider-free conformance suite must use a temporary synthetic project, a closed environment, no provider/backend credentials, no configured private workspace, and no external network route. Its validation, import, migration-preview, comparison, and report fixtures must make provider/backend construction impossible and prove forward refusal before writes or process launch. The current test-only freeze does not claim those runtime boundaries are implemented.

## Privacy, placement, and release

Public fixtures are synthetic. Public projections exclude credentials, sessions, backend identities/URLs, private roots, absolute paths, prompts, response bodies, raw trajectories, tool arguments, proprietary content, and private case identities. A digest is not automatic anonymization; low-entropy or private-value digests remain private. Reporters cannot widen source privacy, and aggregates are not public merely because bodies are absent.

Plans declare privacy class before execution; each component receives the minimum role projection. Unknown privacy classes, redaction failures, or incomplete provenance fail closed. Generic retries are only for proven replay-safe work. After commitment, timeout or cancellation is classified as `timed_out` or `canceled` only with the proof required above; a disconnect, missing receipt, or inadequate termination proof produces terminal `unknown` and exit class `outcome_unknown`.

This contract does not move the evaluator. `internal/agenteval` remains an independent nested module; the root module must not add a `require`, `replace`, or tracked workspace for it. ATL behavior stays behind the selected-binary process/JSON boundary. Physical extraction remains governed by the [substrate decision](../../maintainers/agent-evaluator-substrates.md); this contract does not satisfy or authorize its gates.

There is no standalone release, support window, public Go SDK, registry upload, or installation promise. [#1332](https://github.com/isukharev/atl/issues/1332) owns signed distribution and support. Stable status begins only at `first-conforming-signed-standalone-release` and requires a reviewed release identity, compatibility matrix, provider-free bundle, historical-readability evidence, security/support policy, and supported-platform statement. Until then, use the repository maintainer workflow and call the standalone surface pre-release and reserved.
