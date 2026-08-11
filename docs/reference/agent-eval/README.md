# Standalone agent-eval contract

Status: normative pre-release contract. Contract version: `0.1.0-pre-release`.
The repository source implements a bounded pre-release subset; no signed
standalone distribution is released or stable.

[Documentation home](../../README.md) ·
[Evaluation methodology](../../agent-benchmarking.md) ·
[Evaluator substrate decision](../../maintainers/agent-evaluator-substrates.md) ·
[Roadmap](../../../ROADMAP.md)

This document defines the public boundary for a local-first standalone
`agent-eval` product. “Must”, “must not”, “should”, and “may” are normative.
Source implementation does not expose an internal Go API, start the stable
compatibility clock, or authorize provider, backend, network, release, or
private-workspace work.

## Product status

| Surface | Current status | Compatibility promise |
|---|---|---|
| Repository maintainer command at `internal/agenteval/cmd/agent-eval` | Implemented for ATL repository evaluation | Historical command names, helper executable names, environment registry, and private-workspace operations remain internal unless this document explicitly admits them |
| Source-built standalone coordinator in that command | `implemented_pre_release` for the rows marked `pre_release` below | The selected operations, JSON envelopes, exit classes, help, completion, project configuration, and one-request process surface are machine-tested; they are unsigned and carry no support or stable-compatibility promise |
| Signed standalone `agent-eval` distribution | Not released | Compatibility starts only at `first-conforming-signed-standalone-release` after the complete distribution gate passes |

The coordinator now routes the explicitly admitted subset before the hidden
maintainer dispatcher. Those operations emit the structured envelopes and exit
classes below. Historical maintainer aliases keep their prior behavior and are
not made public by this routing. Reserved rows fail with
`compatibility_error` before configuration or authority acquisition. Callers
may describe only the marked rows as **pre-release source implementations**;
they must not call them stable, supported, or distributed.

The repository implementation now contains an internal, in-memory neutral core
and one explicitly composed ATL profile. The root evaluator package remains the
compatibility facade for every historical JSON generation, provider runner,
selected-binary check, and private workflow. Strict ATL DTOs are validated
before they are projected into neutral identities; their source bytes, JSON
tags, schema versions, and digests are not rewritten. This is implementation
evidence for the component split below, not a public Go API or standalone
conformance claim.

## Stability vocabulary

| Status | Meaning |
|---|---|
| `stable` | Released, conformance-tested, and covered by this compatibility and deprecation policy |
| `experimental` | Explicitly opt-in and namespaced; may change in a minor release but must not silently alter stable artifact meaning |
| `pre_release` | Implemented and conformance-tested in repository source, but unsigned, unreleased, and outside the stable compatibility/support clock |
| `internal` | Repository or implementation detail with no public compatibility promise |
| `reserved` | Normatively shaped here but not shipped; callers must not probe for it or treat its spelling as implemented |

A surface is not stable merely because it exists in source, appears in help, has a schema version, or has historical fixtures. A release must identify each stable surface in the conformance registry. Unmarked extensions are rejected rather than inferred to be experimental.

## Operations and user journeys

Each row below is an independent authority ceiling, not an implicit grant.
`pre_release` rows are implemented by the source coordinator; `reserved` rows
refuse before configuration or authority. `Y` means the dimension may be
admitted only from the explicit invocation and resolved plan; `N` means the
operation must be structurally unable to acquire it.

| ID | Mode | Status | `authority` | `local_read` | `local_write` | `process_spawn` | `provider_contact` | `backend_contact` | `network` | `credential_access` | `private_workspace_access` |
|---|---|---|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `capabilities` | `default` | `pre_release` | `none` | N | N | N | N | N | N | N | N |
| `compare` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `compat verify` | `provider-free` | `reserved` | `verifier_execution` | Y | N | Y | N | N | N | N | N |
| `export` | `agent-skills` | `pre_release` | `local_write` | Y | Y | N | N | N | N | N | N |
| `grade` | `deterministic` | `pre_release` | `verifier_execution` | Y | N | Y | N | N | N | N | N |
| `grade` | `judge` | `reserved` | `provider_execution` | Y | N | Y | Y | N | Y | Y | N |
| `import` | `agent-skills` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `import` | `default` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | N |
| `init` | `default` | `reserved` | `local_write` | N | Y | N | N | N | N | N | Y |
| `inspect` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `migrate apply` | `default` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `migrate preview` | `default` | `reserved` | `local_read` | Y | N | N | N | N | N | N | Y |
| `plan` | `default` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `reconcile` | `evidence-only` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `report` | `default` | `reserved` | `local_read` | Y | N | N | N | N | N | N | N |
| `resume` | `default` | `reserved` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | Y |
| `run` | `default` | `reserved` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | N |
| `schema inspect` | `default` | `reserved` | `local_read` | Y | N | N | N | N | N | N | N |
| `validate` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `version` | `default` | `pre_release` | `none` | N | N | N | N | N | N | N | N |

The user journeys follow directly from those ceilings. Agent Skills import is
read-only; its separately named export writes one non-authoritative
compatibility view to a new explicit destination. The reserved generic
`import` may eventually write a native candidate but is not implemented.
`init` creates only the explicit project. `plan` writes an immutable plan to an
explicit destination. `reconcile` may append only content-minimized local
proof. `compare` and `report` consume existing local artifacts. Migration
preview reads; apply writes a new explicit destination. `compat verify` may
spawn an isolated verifier but remains provider-, backend-, network-,
credential-, and private-workspace-free. Deterministic grading has the same
no-contact verifier ceiling even though the current in-process implementation
does not spawn. Judge grading is a distinct, explicit mode: it may receive
provider, network, and credential authority, but never product-backend or
private-workspace authority. `run` and `resume` receive only the individually
admitted execution dimensions, and resume remains subject to the no-replay
lifecycle below.

Commands are non-interactive: no prompts, pagers, browsers, confirmation reads from stdin, or default provider selection. A local mutation requiring confirmation must receive all confirmation material in the original invocation and fail before writing when it is absent.

Every row whose contact or access dimension is `N` must be structurally unable to construct or discover that authority. In particular, `import`, `validate`, `plan`, `reconcile`, migration, `compare`, `report`, `capabilities`, deterministic grading, and provider-free `compat verify` cannot construct a provider, configured product backend, or network client. Dry-run is not a substitute for this boundary.

## Configuration and authority

Configuration resolves in this exact high-to-low order:

1. explicit command flags;
2. the project configuration selected for that invocation;
3. environment values from an explicitly enabled, closed allowlist.

Project configuration is loaded only from an exact `--config` path or
`.agent-eval/config.json` inside an exact `--project` root. The CLI does not
walk parents, inspect unrelated repository metadata, or merge user or system
configuration implicitly. Schema `agent-eval/project-config@1` is capped at
64 KiB and contains only optional `profile`, `model`, and `repetitions`
identity defaults. It has no path, provider, backend, credential, network,
private-root, or authority member. Explicit JSON `null`, duplicate or unknown
members, trailing values, future generations, symlinks, non-regular files,
unstable rereads, identifiers over 1,024 bytes, and repetitions outside
`1..1000` fail closed.

```json named-agent-eval-project-config
{"schema":"agent-eval/project-config","schema_version":1,"contract_version":"0.1.0-pre-release","profile":"synthetic-local","model":"model-a","repetitions":3}
```

Environment input is disabled by default. Enabling it names a reviewed projection whose accepted names and value classes are visible in `agent-eval capabilities`. Unknown projected keys, malformed values, and duplicate configuration keys fail closed. The projection must not admit the current internal `ATL_EVAL_*` registry wholesale.

The implemented `portable-v1` projection admits exactly
`AGENT_EVAL_PROFILE`, `AGENT_EVAL_MODEL`, and `AGENT_EVAL_REPETITIONS` under
the same bounds. Any other `AGENT_EVAL_*` name is an error. Agent Skills
import/export deliberately accept neither project configuration nor the
environment projection: their roots, baseline, format, mapping, and
destination must all be explicit flags.

Selection and authority remain separate:

- naming a component, URL, credential reference, or private root does not authorize using it;
- selecting network-capable execution does not grant a destination, method, credential, retry, or write policy;
- configuration and environment cannot widen the authority admitted for the command;
- a component may reduce authority or report it unsupported, but cannot grant authority to itself.

No command discovers any of these ambiently: provider credentials, an ATL backend, a private evaluation root, proxy settings, cloud metadata, or network authority. A private root is available only to a row with `private_workspace_access:Y` and only through an exact invocation input. Credentials are resolved only by an explicitly selected adapter after admission and never enter plans or public durable artifacts. Secret bytes are never echoed in errors, previews, or publication-safe digests.

## Agent Skills interchange

The `agent-skills` mode is a bounded compatibility adapter for two explicitly
named upstream authoring layouts. It does not treat the Agent Skills packaging
specification as an evaluator, runner, judge, sandbox, or environment
contract. The reviewed inputs are the pinned
[Agent Skills packaging and evaluation guide](https://github.com/agentskills/agentskills/tree/217be548739f21d6008915c29aefe320ea1a90af)
and the pinned
[Anthropic skill-creator schemas and aggregator](https://github.com/anthropics/skills/tree/f17010c9bb483898c1d9c9f42dde2b3a98889434/skills/skill-creator).
Because those sources disagree in material ways, the adapter never silently
merges their shapes.

`import agent-skills` is read-only. It accepts `--format agent-skills`, one
bounded `--skill-root`, an optional exact `--eval-root`, and an explicit
`--baseline no-skill|previous-skill`. The previous-skill baseline also
requires an exact `--previous-skill-root`; the no-skill baseline forbids it.
The variants are:

- `agentskills-guide-v1`: `evals/evals.json` uses `assertions`; benchmark
  deltas are numeric and review feedback is a map.
- `anthropic-skill-creator-v1`: `evals/evals.json` uses `expectations`;
  benchmark rows and metadata use the pinned richer schema and string-valued
  deltas.
- `auto`: import-only detection from exactly one criteria spelling. Missing,
  mixed, or ambiguous discriminators fail; export never accepts `auto`.

Both importers strictly capture `SKILL.md`, the referenced bounded regular
files, and the selected eval JSON without executing scripts or following
symlinks. They retain exact source and normalized digests, prompt/check order,
baseline identity, and missing-versus-zero metric states. A single source tree
is capped at 64 MiB and 4,096 entries; individual JSON documents are capped at
1 MiB, referenced files at 8 MiB, cases at 256, criteria at 255 per case, and
workspace runs at 4,096. Repeated references and publication files are charged
against aggregate bounds before their bytes are retained.

`export agent-skills` is a separately classified local write. It requires one
explicit variant, source import arguments, an exact workspace root, and an
absolute clean destination that does not yet exist. For Guide workspaces, the
caller supplies one repeated `--case-directory ID=iteration-N/eval-slug` for
every imported case; every mapping must use the same positive iteration.
Cells are `with_skill` and `without_skill`, or `old_skill` only for the
explicit previous-skill baseline. `old_skill` is a reviewed ATL compatibility
extension and is not inferred from the pinned Guide/aggregator layout.
Anthropic workspaces use exact `eval-ID/<cell>/run-N` paths; the legacy
`runs/eval-ID/...` spelling and mixed layouts are refused.

Before publication, the adapter reconciles every grading result with the
source assertion/expectation text, order, and count. Missing or empty criteria,
partial verifier coverage, and mismatched benchmark cells block publication.
The writer re-encodes only grading and benchmark JSON. When a source workspace
is bound, it copies its already captured `outputs/**` and `timing.json` bytes
exactly into the new destination; it never reopens an ambient output path.
The report names source-only or unsupported timing detail, estimated cost,
activation, verifier, runner, judge, sandbox, environment, model metadata,
feedback, and notes instead of silently inventing their meaning.

```sh named-agent-eval-agent-skills-import
agent-eval import agent-skills \
  --format agent-skills \
  --variant agentskills-guide-v1 \
  --skill-root ./skill \
  --baseline no-skill
```

Every import/export result is content-minimized and every export says
`authoritative:false`. Its counts and SHA-256 values are local comparison
identities, not anonymization, publisher authentication, execution evidence,
or publication approval. No prompt, output, feedback, model value, source
path, or destination path is emitted in the command envelope.

The new-destination writer uses contained, no-follow stable reads, exclusive
file creation, exact byte/inventory rereads, and an incomplete marker. This
detects cooperative process interruption and refuses observed identity drift;
it is not an atomic transaction, a power-loss durability protocol, or
protection from a hostile same-UID process renaming a parent. On failure it
does not delete a possibly raced path. Callers must inspect or remove an
incomplete destination themselves under separate authority.

## Output and error contract

The standalone target uses JSON by default. A successful non-streaming command writes exactly one newline-terminated JSON object to stdout. Diagnostics go to stderr and never contaminate stdout. Human output requires explicit `--output text`; an unsupported projection fails before configuration, credentials, process launch, or network access.

Pre-release success objects contain `schema`, `schema_version`,
`contract_version`, `command`, `status`, and command-specific `result`.
Pre-release error objects contain `schema`, `schema_version`,
`contract_version`, `error`, `exit_class`, `kind`, `retry_safe`, and a closed
`recovery` object. Typed local conditions—not provider or backend prose—select
the error class. `agent-eval capabilities` reports exactly the machine-owned
operation/mode rows above, including status, authority dimensions, ProcessAPI
admission, and separate Agent Skills format variants; help/completion/process
are meta surfaces rather than invented product operations.

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

Unsupported or unknown required capability is `compatibility_error`, not a
task failure. Policy refusal is not authentication failure. A known failed
attempt may return `execution_failed`; its durable identity is not proof a
retry is safe. Hidden maintainer routes retain their historical error behavior;
a wrapper must not manufacture standalone conformance by parsing those strings.

### One-request process surface

`agent-eval process` accepts exactly one strictly decoded JSON request of at
most 1 MiB and emits exactly one result or error envelope of at most 1 MiB.
Unknown or duplicate members, explicit `null` arguments, invalid UTF-8,
trailing values, nested collection/depth overflow, future schema or contract
versions, and a second request fail closed. The only admitted operations are
`version`, `capabilities`, `validate`, `compare`, and `inspect`; deterministic
grade, Agent Skills import/export, meta commands, reserved operations, and all
hidden maintainer routes are structurally refused.

```json named-agent-eval-process-request
{"schema":"agent-eval/process-request","schema_version":1,"contract_version":"0.1.0-pre-release","command":"version","mode":"execute","deadline_milliseconds":1000,"configuration":{"source":"none","environment":"none"},"arguments":[]}
```

Mode is `execute`, `dry-run`, or `explain`. Configuration is `none`, one exact
config path, or one exact project root plus `none|portable-v1` environment;
`version` and `capabilities` require all configuration fields to be `none`.
The positive execution deadline is capped at 15 minutes and begins only after
the complete bounded request is read and validated, so a caller must
independently bound delivery of stdin. On expiry the coordinator cancels the
in-process operation. Cooperative completion within 100 ms yields
`interrupted,retry_safe:true`; otherwise it returns absorbing
`outcome_unknown,retry_safe:false` while the uncooperative executor is not
assumed stopped. No admitted ProcessAPI row has local-write, process, provider,
backend, network, credential, or private-workspace authority.

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

In the current repository implementation, the neutral core owns a closed,
immutable profile registry, capability negotiation, attempt-local adapter /
backend / grader ports, normalized observations, deterministic assessment, and
integer aggregation. The ATL compatibility facade supplies the single built-in
profile from the pinned capability catalog and retains all ATL route and durable
artifact authority. Package-direction and exported-vocabulary tests enforce
that the core cannot import the facade or the ATL profile, and that profile
composition is explicit rather than `init`- or plugin-driven.

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
| `agent-eval/project-config` | Invocation-selected profile/model/repetition identity defaults; never authority, paths, credentials, or ambient discovery |
| `agent-eval/adapter-manifest` | Closed component identity, one declared role and its operations, capabilities, protocol versions, configuration keys, and executable binding |
| `agent-eval/adapter-message` | One bounded process-protocol frame under the selected role, operation, session, and attempt identity |
| `agent-eval/extension-conformance-bundle` | Content-addressed ordinary cases for every supported operation plus one synchronized cancellation case in the manifest's declared role |
| `agent-eval/extension-conformance-report` | Content-minimized protocol-only result; never proof of whole-product compatibility or host confinement |

The test-only compatibility ledger records project config and each of those
four extension families at generation 1. Project config, manifest, message,
and bundle generations are readable, emitted, and executable; reports are
readable and emitted but never executable. Project config is
`public_or_private` and capped at 64 KiB. Manifests are public and capped at
64 KiB, messages are
`public_or_private` and capped at 1 MiB, bundles are public and capped at
1 MiB, and reports are `content_minimized` and capped at 1 MiB. All four use
`preserve` disposition and explicit migration. These pre-release registry rows
do not make a distribution or command stable. Public conformance cases have
nonnull configuration and input arrays, use only public input and expected
output references, and require `output_privacy:"public"`. The machine rejects
every non-public classification. Bundle authors remain responsible for using
only genuinely public synthetic IDs and digests: structural validation cannot
prove that a `public` label is truthful or make a low-entropy private value safe.

Every stable artifact has a closed `schema` and positive integer `schema_version`. Contract, producer, component, and source identities are separate fields. Strict decoders reject duplicate keys, trailing values, invalid JSON encoding, unknown required vocabulary, and fields outside an explicit namespaced extension object. Invalid known schemas and unknown future schemas fail before mutation or execution.

Source bytes are immutable evidence. Import may create a normalized candidate but records the source identity and digest and never overwrites it. Migration never relabels an old hash as belonging to new bytes.

Migration is preview/apply:

1. `migrate preview` strictly reads one named source and target without provider, backend, private-root discovery, or network access.
2. It reports source/candidate identities, domain-separated digests, transformations, preserved counts, and any loss or eligibility change without sensitive content.
3. `migrate apply` requires the unchanged source, target, preview digest, explicit destination, and explicit apply confirmation in one invocation.
4. Apply revalidates, writes a new artifact atomically, and records provenance; it never silently replaces the source.
5. Lossy conversion requires a separately named projection that cannot be mistaken for the source.

## Process boundary and durable attempts

The pre-release extension seam is bounded process/JSON, not the Go module,
shell prose, or environment conventions. Schema-v1 manifests and messages use
contract version `0.1.0-pre-release` and process protocol version `1`. The
closed protocol roles and their operations are:

| Role | Operations | Boundary |
|---|---|---|
| `profile` | `capabilities`, `validate` | Describes and validates one explicitly selected profile; it cannot register itself or widen an admitted plan |
| `agent-adapter` | `execute`, `normalize`, `prepare` | Prepares and invokes the selected agent, then returns normalized observations; it does not own backend policy, grading, retries, or lifecycle |
| `execution-backend` | `execute`, `prepare` | Applies the admitted filesystem, process, network, deadline, cleanup, and resource policy; it does not select the agent or decide replay |
| `grader` | `grade`, `validate` | Validates its declared grading contract and returns check decisions with coverage and provenance; it does not execute the task or promote results |
| `reporter` | `report`, `validate` | Produces a privacy-bounded projection from validated artifacts; it cannot synthesize evidence or widen visibility |

One manifest binds one component ID and version, one role and its complete
operation set, one capability-state claim per operation, executable digest,
protocol versions, closed configuration schema, explicit platform pairs, and
required host-enforcement controls. Configuration fields are only `boolean`,
bounded `integer`, or closed `enum`; free-form strings, paths, URLs, arguments,
environment values, and credentials are not manifest values. Role, operation,
capability, configuration, platform, and requirement vocabularies are exact:
duplicates, unsorted sets, unknown members, missing role operations, unknown
future schemas, and unsupported protocol versions fail before process launch.
The closed requirement IDs are `best_effort_process_group_cleanup`,
`bounded_io`, `deadline`, `exact_environment`, `filesystem_isolation`,
`network_isolation`, `private_working_directory`, `resource_limits`, and
`termination_proof`. The current local host admits only the first four plus
`private_working_directory`; the other four refuse before spawn.
One process session selects the manifest's one declared role and one operation.
An executable used for more than one role needs a separate manifest and session
for every role; support is never inferred across roles.

Protocol version 1 is UTF-8 JSON Lines. Each compact JSON object is followed by
one LF and contains no embedded CR or LF. A frame is at most 1 MiB and a session
is at most 4 MiB in each direction. After stable file reads, protocol
verification runs under a shared outer deadline no greater than 15 minutes;
each conformance case independently declares a positive deadline no greater
than 15 minutes. Every frame binds schema/message/protocol
versions, direction, SHA-256 session and attempt IDs, sequence, role, component
ID and version, executable digest, one message type, and exactly its matching
payload.
The normal sequence is host `initialize` at 1, extension `initialized` at 2,
host `invoke` at 3, and extension `result` or structured `error` at 4. An
initialization error is sequence 2. After invoke, host `cancel` is sequence 4
and extension `canceled` is sequence 5. Any other direction, sequence, payload,
or terminal ordering is invalid. The host creates a fresh SHA-256
`invocation_id` immediately before delivery; invoke, result, invoke-error,
cancel, and canceled payloads carry that exact causal identity. It binds a
terminal or acknowledgment to one invocation but grants no retry, replay, or
lifecycle authority.

Ordinary cases use invoke `control:"execute"`. Every conformance bundle also
contains exactly one invoke `control:"await_cancel"` probe. The probe is a
protocol-only synchronization case: the component must not execute the
operation or emit result/error, must wait for the host's cancel frame, and may
answer only with the matching `canceled` frame. This avoids a result-versus-
cancel race while exercising the cancellation transition.

Blank frames, partial final frames, duplicate keys, trailing values, unknown
members, out-of-order messages, identity drift, and output after a terminal
frame fail closed. Stdout is protocol-only. Stderr contents are never parsed as
a protocol message and cannot determine an outcome; exceeding the 64 KiB bound
fails closed. Invocation inputs and outputs are sorted identity,
schema-version, digest, size, and
privacy references; the protocol carries no artifact body or filesystem
handle. Invocation policy explicitly bounds output count/bytes and privacy and
names `replay_safe` or `non_replay_safe`; it never authorizes a retry.

This synthetic initialization frame is one complete JSONL record:

```jsonl named-agent-eval-extension-initialize
{"schema":"agent-eval/adapter-message","schema_version":1,"protocol_version":1,"direction":"host_to_extension","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attempt_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sequence":1,"role":"profile","component_id":"synthetic.profile","component_version":"v0.0.0","executable_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","type":"initialize","initialize":{"offered_protocol_versions":[1],"required_capabilities":["profile.capabilities","profile.validate"]}}
```

The repository maintainer command is internal:

```sh
agent-eval verify-extension-protocol \
  --manifest testdata/synthetic-adapter-manifest.json \
  --adapter /tmp/agent-eval-synthetic/adapter \
  --bundle testdata/synthetic-extension-conformance.json
```

It emits `agent-eval/extension-conformance-report@1` with scope
`extension_protocol`. The report binds the contract, protocol, manifest,
executable, component identity and version, declared role, complete exact capability claims,
sorted closed case outcomes, and cleanup-assurance class by content-minimized
values and SHA-256 identities. Ordinary cases cover exactly every `supported`
operation, and one additional case proves the synchronized `canceled`
transition. Each sorted case records only its ID, operation, terminal kind, and
`passed` status. The report excludes input paths, arguments, environment values, process,
session, attempt, and invocation IDs, stderr, task bodies, prompts, evidence
bodies, and private task or fixture identities. It may echo only the public
structural IDs declared by the admitted manifest and bundle, subject to the same
authoring obligation above. It is not the reserved standalone `compat verify`
command, does not emit whole-product `compatible:true`, and retains the hidden
maintainer command's historical error behavior independently of the public
pre-release coordinator.

The local host copies an explicitly selected digest-bound executable into a
fresh private runtime, supplies a closed synthetic environment and working
directory, bounds framing, deadline, output, and process-tree cleanup, and
removes that runtime after the session. Unix hosts, including macOS, ignore
ambient `TMPDIR` and create the runtime beneath the canonical root-owned sticky
`/tmp`. Owner, mode, and digest admission checks there use no-follow opens and
held handles, but process execution remains path-based: it does not prove
no-replace against a hostile same-UID process or provide same-UID sandbox
isolation. Windows atomically creates the runtime beneath a held non-reparse OS
temporary base with a protected current-user DACL, holds the base and runtime
root identities, and keeps a no-write/delete share-mode executable guard
through process start. It derives `SYSTEMROOT` and `WINDIR` from the OS instead
of ambient values. These controls protect only the private runtime and admitted
executable identity. They do **not** confine arbitrary filesystem, network, or
credential access, enforce general resource isolation, or prove durable
process-tree termination on any platform; a content-minimized report is not
termination proof. The executable SHA-256 binds only the copied primary
executable bytes; it neither authenticates a publisher nor transitively binds
dynamic libraries or other runtime dependencies.
Any requirement that needs those controls must refuse before spawn pending the
qualified execution boundary in [#1320](https://github.com/isukharev/atl/issues/1320).
Once the admitted process successfully starts, any handshake, protocol,
terminal, or cleanup ambiguity produces the absorbing, no-replay `unknown`:
without isolation, the child could already have side effects. Only a refusal
proved before spawn remains a compatibility failure. Neither timeout,
cancellation, an adapter acknowledgment, nor a missing process is enough to
infer otherwise.

The current ATL profile remains an explicitly composed built-in Go component.
The `profile` protocol role does not turn it into a downloadable plugin, add a
dynamic registry, or stabilize any internal `ATL_EVAL_*` environment input.

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

The current pre-release source conformance suite uses temporary synthetic
projects, a closed environment, no provider/backend credentials, no configured
private workspace, and no external network route. It executes every promoted
row, reconciles runtime capabilities with the machine product contract, and
proves reserved/ProcessAPI refusal before reading authority-bearing inputs.
Agent Skills import/export additionally use only bounded local synthetic trees
and one explicit new destination. Runtime conformance for the still-reserved
lifecycle, native import, migration, reporting, provider-backed, and
distribution surfaces remains future work.

## Privacy, placement, and release

Public fixtures are synthetic. Public projections exclude credentials, sessions, backend identities/URLs, private roots, absolute paths, prompts, response bodies, raw trajectories, tool arguments, proprietary content, and private task or fixture identities. A digest is not automatic anonymization; low-entropy or private-value digests remain private. Reporters cannot widen source privacy, and aggregates are not public merely because bodies are absent.

Plans declare privacy class before execution; each component receives the minimum role projection. Unknown privacy classes, redaction failures, or incomplete provenance fail closed. Generic retries are only for proven replay-safe work. After commitment, timeout or cancellation is classified as `timed_out` or `canceled` only with the proof required above; a disconnect, missing receipt, or inadequate termination proof produces terminal `unknown` and exit class `outcome_unknown`.

This contract does not move the evaluator. `internal/agenteval` remains an independent nested module; the root module must not add a `require`, `replace`, or tracked workspace for it. ATL behavior stays behind the selected-binary process/JSON boundary. Physical extraction remains governed by the [substrate decision](../../maintainers/agent-evaluator-substrates.md); this contract does not satisfy or authorize its gates.

There is no standalone release, support window, public Go SDK, registry upload,
or installation promise. [#1332](https://github.com/isukharev/atl/issues/1332)
owns signed distribution and support. Stable status begins only at
`first-conforming-signed-standalone-release` and requires a reviewed release
identity, compatibility matrix, provider-free bundle, historical-readability
evidence, security/support policy, and supported-platform statement. Until
then, call only the marked source implementations pre-release and call every
other row reserved.
