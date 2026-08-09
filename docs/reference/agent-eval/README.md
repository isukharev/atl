# Standalone agent-eval contract

Status: normative pre-release contract. Contract series: `0.1`. No standalone distribution currently conforms to it.

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

The standalone CLI reserves these command families. #1315 owns their exact flags and help.

| Journey | Reserved command | Authority ceiling before execution |
|---|---|---|
| Create a local project | `agent-eval init` | Local project writes only; no agent, provider, backend, credential, private-root, or network access |
| Import an Agent Skills eval artifact | `agent-eval import` | Read the named source and write a local candidate only; no execution or network access |
| Validate inputs and compatibility | `agent-eval validate` | Read-only, provider-free, backend-free, and network-free |
| Compile an experiment plan | `agent-eval plan` | Local reads and an explicit local destination; no model or backend execution |
| Start admitted attempts | `agent-eval run` | Only explicitly selected components and admitted authority |
| Continue an interrupted plan | `agent-eval resume` | Same immutable plan and authority ceiling; never replay an uncertain attempt |
| Classify an uncertain attempt | `agent-eval reconcile` | Evidence reads only; never repeats the original action |
| Apply deterministic graders or an explicit judge | `agent-eval grade` | Deterministic by default; a judge requires explicit adapter and execution authority |
| Compare compatible results | `agent-eval compare` | Existing local artifacts only; no provider or backend execution |
| Produce a projection | `agent-eval report` | Existing local artifacts only; reporters receive no execution authority |
| Preview or apply a migration | `agent-eval migrate preview|apply` | Preview is read-only and network-free; apply writes only an explicit local destination |
| Verify a component or profile bundle | `agent-eval compat verify` | Content-addressed local inputs, isolated processes, and no provider/backend credentials |
| Inspect the public registry | `agent-eval capabilities` | Offline and read-only |
| Inspect one artifact or resolved plan | `agent-eval inspect` | Offline, read-only, and content-minimized |
| Inspect supported artifact schemas | `agent-eval schema inspect` | Offline and read-only |
| Inspect build and protocol identity | `agent-eval version` | Offline and read-only |

Commands are non-interactive: no prompts, pagers, browsers, confirmation reads from stdin, or default provider selection. A local mutation requiring confirmation must receive all confirmation material in the original invocation and fail before writing when it is absent.

`import`, `validate`, `plan`, `migrate preview`, `compare`, `report`, `capabilities`, and provider-free `compat verify` must be structurally unable to construct an agent, provider, configured product backend, or network client. Dry-run is not a substitute for this boundary.

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

No command discovers or imports ambient provider credentials, an ATL backend, a private evaluation workspace, proxy settings, cloud metadata, or network authority. Credentials are resolved only by an explicitly selected adapter after admission and never enter plans or public durable artifacts. Secret bytes are never echoed in errors, previews, or publication-safe digests.

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

`contract_version` follows Semantic Versioning. Pre-`1.0.0` versions describe this pre-release contract and do not make a distribution stable. After `1.0.0`, patch releases may clarify or add compatible diagnostics, minor releases may add optional stable members while preserving defaults and readers, and incompatible stable changes require a new major version.

Stable deprecation lasts at least two consecutive minor releases and 180 days, whichever is longer; removal still requires the next major. A security issue may disable execution sooner, but safe inspection, reporting, or migration remains when it does not recreate the vulnerability, and historical meaning is never silently reinterpreted.

Readers in a supported major line must read every stable artifact that line emitted. A later reader either preserves meaning directly or offers an explicit previewable migration. Future schemas are preserved and refused, never treated as empty, downgraded, or partially decoded as current.

Compatibility is the tuple `(core, contract, profile, agent adapter, execution backend, grader, reporter, artifact schemas, process protocols)`. `compatible:true` requires every required tuple member and capability to be known and supported; omission is not compatibility.

## Component responsibilities

| Component | Owns | Must not own or infer |
|---|---|---|
| Standalone core | Registry, strict decoding, admission, planning, durable attempt identity, aggregation, compatibility, and migration orchestration | ATL semantics, provider credentials, sandbox implementation, grader truth, or reconstruction of missing evidence |
| ATL profile | ATL capability vocabulary, selected-binary compatibility, ATL fixtures/projections, and legacy artifact mappings | Generic vocabulary, provider launch, replay, or standalone release authority |
| Agent adapter | Explicit launch, bounded structured exchange, agent identity, activation evidence, and usage receipts | Admission, backend authority, scoring, retries, promotion, or privacy classification |
| Execution backend | Filesystem, process, network, deadline, cleanup, and resource enforcement with a receipt | Agent selection, grader truth, retry policy, or authority beyond the admitted plan |
| Grader | Deterministic checks or an explicitly isolated judge with coverage and provenance | Runner control, hidden retries, source mutation, missing-as-zero coercion, or promotion authority |
| Reporter | Content-minimized projections of validated artifacts | Execution, migration, evidence synthesis, or wider visibility |

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
  "contract_version": "0.1.0",
  "command": "compat verify",
  "status": "failed",
  "components": [
    {
      "role": "agent_adapter",
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

Existing evaluator artifacts retain their bytes and meaning under logical identities of the form `atl-profile/<family>@<schema-version>`, for example `atl-profile/result@8`. Registry metadata is not inserted into historical JSON, included in an old digest, or grounds for rewriting a file.

| Family | Current ATL-profile disposition |
|---|---|
| `atl-profile/scenario@1` | Preserve and validate under its existing closed schema |
| `atl-profile/run-spec@5..7` | Preserve readable generations; current writes use v7 and execution eligibility remains generation-specific |
| `atl-profile/observation@5` | Current evaluation input; older observations require explicit migration before evaluation |
| `atl-profile/result@3..8` | Preserve readable generations; current writes use v8 and cohort restrictions remain |
| `atl-profile/aggregate@7` | Preserve current meaning; never backfill missing coverage or identities |
| `atl-profile/synthetic-run-receipt@1`, `atl-profile/synthetic-root-aggregate@2` | Preserve content-bound provenance and complete-root requirements |
| `atl-profile/review@1..2` | Preserve readable reviews; current writes use v2 and incompatible policies stay separate |
| `atl-profile/private-workspace@1..4`, `atl-profile/private-plan@1..9` | Owner-private compatibility only; readability does not imply execution or promotion eligibility |
| `atl-profile/activation-reference@1..2`, `atl-profile/activation-report@1..2` | Preserve v1 as compare-only and v2 under calibrated rules |
| Other receipts, ledgers, scorecards, checkpoints, fixtures, and wire projections | Require an explicit registry entry and existing decoder policy; no wildcard admission |

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

The lifecycle meanings are:

| State | Meaning |
|---|---|
| `planned` | Immutable inputs exist; authority is not admitted |
| `committed` | Compatibility, policy, and bounds passed; the attempt identity is consumed before spawn |
| `spawning` | A committed component process is being created; replay is unsafe without proof |
| `running` | The committed component is known to be active |
| `succeeded`, `failed` | Known terminal execution outcomes |
| `timed_out` | A terminal timeout with durable proof that the old process cannot continue; otherwise use `unknown` |
| `policy_denied`, `unsupported` | Known terminal pre-execution refusals retained as attempt evidence |
| `unknown` | A committed action lacks safe terminal classification; terminal for automatic scheduling |
| `cancelled` | Replay-safe only with durable proof that cancellation preceded commitment; otherwise `unknown` |

Only `planned` work is automatically resumable. `resume` never creates another
attempt for a committed or unknown slot. `reconcile` may attach bounded evidence
and refine an outcome but cannot repeat the action. A new attempt needs a new
identity and explicit plan decision.

[#1317](https://github.com/isukharev/atl/issues/1317) owns ledger and recovery implementation. This document freezes transition meaning but does not claim current conformance.

## Missing evidence and provider-free conformance

| Metric coverage | Value rule | Meaning |
|---|---|---|
| `observed` | Required, including explicit zero | The measurement boundary proved the value |
| `unknown` | Absent or zero in a legacy paired representation | The boundary could not establish the value safely |
| `unsupported` | Absent or zero in a legacy paired representation | The selected component affirmatively cannot provide the metric |
| `not_applicable` | Absent | The plan proves it has no meaning for this cell |
| missing entry | None | Required coverage is incomplete and the relevant check fails |

Zero is valid only with `coverage:"observed"`; it never means unknown,
unsupported, not applicable, or absent. Historical ATL-profile projections that
pair `coverage:false` with numeric zero must be interpreted as non-observation,
never as a measured zero. Aggregates report each coverage count, summarize only
observed values, and never change denominators or impute silently. Capability
state, attempt outcome, metric coverage, and numeric value are independent.

A stable distribution publishes content-addressed synthetic fixtures. Provider-free conformance proves JSON/error/exit contracts; configuration precedence; no ambient authority discovery; pre-execution capability refusal; historical readability and future rejection; migration binding; missing-versus-zero behavior; component confinement; deterministic rereads; and, after #1317, no replay.

The suite uses a temporary synthetic project, closed environment, no provider/backend credentials, no private workspace, and no external network route. Validation, import, migration preview, comparison, and report fixtures make provider/backend construction impossible and prove forward refusal happens before writes or process launch.

## Privacy, placement, and release

Public fixtures are synthetic. Public projections exclude credentials, sessions, backend identities/URLs, private roots, absolute paths, prompts, response bodies, raw trajectories, tool arguments, proprietary content, and private case identities. A digest is not automatic anonymization; low-entropy or private-value digests remain private. Reporters cannot widen source privacy, and aggregates are not public merely because bodies are absent.

Plans declare privacy class before execution; each component receives the minimum role projection. Unknown privacy classes, redaction failures, or incomplete provenance fail closed. Generic retries are only for proven replay-safe work. Timeout, disconnect, or missing receipt after commitment becomes `outcome_unknown`.

This contract does not move the evaluator. `internal/agenteval` remains an independent nested module; the root module must not add a `require`, `replace`, or tracked workspace for it. ATL behavior stays behind the selected-binary process/JSON boundary. Physical extraction remains governed by the [substrate decision](../../maintainers/agent-evaluator-substrates.md); this contract does not satisfy or authorize its gates.

There is no standalone release, support window, public Go SDK, registry upload, or installation promise. [#1332](https://github.com/isukharev/atl/issues/1332) owns signed distribution and support. Stable status requires a reviewed release identity, compatibility matrix, provider-free bundle, historical-readability evidence, security/support policy, and supported-platform statement. Until then, use the repository maintainer workflow and call the standalone surface pre-release and reserved.
