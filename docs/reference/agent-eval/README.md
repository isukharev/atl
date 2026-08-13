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

The source tree also contains a neutral experiment compiler and its versioned
artifact readers. The coordinator exposes exactly two composed execution rows.
`run/reference` consumes an already compiled canonical manifest plus one closed
sequential-reference bundle and writes one new local publication;
`resume/reference` reopens only that exact marker-bearing incomplete
publication and dispatches its ledger-proved planned complement. It does not
expose a general experiment planner, configurable runner, generic resume, or
one-request process route. Other experiment source availability does not make
an operation implemented or grant runner authority.

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
| `migrate apply` | `default` | `pre_release` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `migrate preview` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | Y |
| `plan` | `default` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `reconcile` | `evidence-only` | `reserved` | `local_write` | Y | Y | N | N | N | N | N | Y |
| `report` | `default` | `reserved` | `local_read` | Y | N | N | N | N | N | N | N |
| `resume` | `default` | `reserved` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | Y |
| `resume` | `reference` | `pre_release` | `local_write` | Y | Y | N | N | N | N | N | N |
| `run` | `default` | `reserved` | `agent_execution` | Y | Y | Y | Y | Y | Y | Y | N |
| `run` | `reference` | `pre_release` | `local_write` | Y | Y | N | N | N | N | N | N |
| `schema inspect` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `validate` | `default` | `pre_release` | `local_read` | Y | N | N | N | N | N | N | N |
| `version` | `default` | `pre_release` | `none` | N | N | N | N | N | N | N | N |

The user journeys follow directly from those ceilings. Agent Skills import is
read-only; its separately named export writes one non-authoritative
compatibility view to a new explicit destination. The reserved generic
`import` may eventually write a native candidate but is not implemented.
`init` creates only the explicit project. `plan` writes an immutable plan to an
explicit destination. `reconcile` may append only content-minimized local
proof. `compare` and `report` consume existing local artifacts. Migration
preview reads only the explicitly named private root; apply preserves the
reviewed source bytes in the root's archive, installs the current candidate,
and writes one content-minimized receipt. `compat verify` may
spawn an isolated verifier but remains provider-, backend-, network-,
credential-, and private-workspace-free. Deterministic grading has the same
no-contact verifier ceiling even though the current in-process implementation
does not spawn. Judge grading is a distinct, explicit mode: it may receive
provider, network, and credential authority, but never product-backend or
private-workspace authority. The exact `run/reference` and `resume/reference`
profiles have only local read/write authority: they cannot spawn a process,
contact a provider or product backend, use a network, discover credentials, or
open a private workspace. Generic `run` and `resume` remain reserved, receive
only the individually admitted execution dimensions if later implemented, and
remain subject to the no-replay lifecycle below.

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

The internal structural admission path may be composed with the separately
versioned lifecycle-security rule pack. That static layer scans the same
already-captured bytes, never reopens bundle paths, executes content, resolves
providers, downloads, contacts a network, or discovers credentials. Its
content-minimized report covers every admitted regular file as scanned text or
an explicit unsupported status, with a closed file-type classification;
unsupported coverage and every unsuppressed
finding block the layer. Exact suppressions bind one rule/evidence pair, one
logical file, the structural bundle digest, a closed rationale, and an
explicit expiry date. A clean report is not runtime-safety proof, and the
security report is not a SARIF or hosted-scanner interface.

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
are meta surfaces rather than invented product operations. Ordinary help lists
only supported mode values as executable choices and labels a shaped but
unavailable mode under `Reserved modes (unavailable)`; capabilities remains
the complete machine-readable supported/reserved inventory.

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
versions, and a second request fail closed. The exact admitted operations are
`version`, `capabilities`, `validate`, `compare`, `inspect`, `schema inspect`,
`migrate preview`, and `migrate apply`; deterministic grade, Agent Skills
import/export, `run/reference`, meta commands, reserved operations, and all
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
assumed stopped. `migrate preview` has the documented local-read and
private-workspace ceiling; `migrate apply` additionally has local-write and
requires the exact reviewed preview digest plus `MIGRATE` confirmation. Every
other admitted ProcessAPI row is read-only or authority-free. No admitted row
has process, provider, backend, network, or credential authority.

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

The provider-neutral `agentadapter` leaf owns the current semantic adapter
contract and observation model. The root facade composes two immutable built-in
implementations and owns their legacy launch, parser, plugin-layout,
confinement, and receipt projections. Admission binds the selected executable,
configuration, reviewed implementation identity digest, complete capability claims, and
configuration-key inventory before process entry. Unsupported activation,
permission, trajectory, usage, transport, or orchestration requirements fail
before spawn; a provider name or an installed binary is never evidence of
support.

The provider-neutral `executionbackend` leaf owns the current execution
contract, admitted trial plan, and content-minimized trial receipt. A closed
in-memory reference backend consumes only caller-supplied, content-addressed
USTAR snapshots, exposes no process, filesystem, environment, network, or
credential API, copies only declared artifacts, and gives its deterministic
verifier a separate clone after the agent-facing state is closed. Unsupported
or unknown capability claims and mismatched policy fail while the durable
attempt is still `planned`. The existing ATL runner is projected separately as
`local_process`: ambient network and credentials are explicit, unavailable
CPU/memory/process and verifier-isolation guarantees remain unsupported, and
the projection can never be reported as hermetic merely because protocol
conformance passed.

The provider-neutral `experiment` leaf owns five closed schema-v1 artifacts:
the complete runtime capability contract, preregistered design, analysis-plan
identity, compiled manifest, and content-minimized trial record. The compiler
requires exact supported claims for every selected condition, activation
channel, funnel stage, and metric before it produces a manifest. It preserves
none/current/previous/forced/autonomous/retrieved/distractor cells, requires
separately authored digest-bound negative controls, and never derives prompts,
assertions, controls, or expected answers from the skill under test. Williams
blocks bind order while treatment identities exclude analysis role and order;
forced-oracle treatments are never classified as autonomous routing. The root
facade projects bounded Agent Skills identities and historical private
four-cell activation plans, then may allocate the complete planned lifecycle
roster. This boundary executes no agent, schedules no trial, computes no
statistical result, emits no report, and acquires no provider, backend,
credential, network, or private-root authority.

The generic experiment families are `public_or_private`, but a manifest
projected from an owner-private activation study remains owner-private. A digest
is a comparison identity, not anonymization or permission to publish source
material.

### Bounded provider-free reference run and crash-safe resume

The source coordinator accepts only the exact compiled reference profile: one
reference treatment, one current-skill candidate, and one separately authored
near-miss control in the manifest's fixed balanced order. Before creating the
destination it strictly decodes and clones the complete manifest, grading plan,
three execution plans, and all content-addressed input archives; it rejects any
unknown or unsupported capability or profile drift. Each treatment role is
also bound to its exact source mount and declared case, skill, or separately
authored control digest; recompiling a manifest cannot substitute fixture bytes
for the candidate or control while retaining a conforming reference run. The
in-memory reference adapter, hermetic reference backend, append-only lifecycle,
deterministic grader, and neutral scheduler then execute only the admitted fixed
roster. With no scheduling flag, or with `--sequential`, each attempt forms its
own round and preserves the exact historical one-worker manifest order.
`--workers N` admits 1–256 local workers and may run only equal treatment
positions from independent manifest blocks concurrently; a later position
never begins until every started member of the current position is terminal.
The only admitted backend program is the bounded `reference_copy` form.

```shell named-agent-eval-sequential-reference-run
agent-eval run --mode reference \
  --manifest /absolute/experiment-manifest.json \
  --bundle /absolute/sequential-reference-bundle.json \
  --destination /absolute/new-reference-output \
  --workers 4
```

Resume uses the same manifest, bundle, and exact scheduler width selected for
the incomplete publication:

```shell named-agent-eval-reference-resume
agent-eval resume --mode reference \
  --manifest /absolute/experiment-manifest.json \
  --bundle /absolute/sequential-reference-bundle.json \
  --destination /absolute/incomplete-reference-output \
  --workers 4
```

For a new run, the destination must be absolute, clean, and absent. Resume
requires that same path to exist as the exact private incomplete publication.
The incomplete marker binds the exact manifest and selected worker width before
the ledger roster is materialized. The active new run or resume holds an
exclusive advisory lock on that exact marker for its full publication
ownership interval; a concurrent resume refuses before reading or changing the
ledger. The publication contains that manifest, one
durable attempt ledger, the content-minimized scheduler plan and report, and a
manifest-ordered directory of canonical observation,
execution-plan/receipt, grading-plan/receipt, lifecycle, and trial-record
artifacts. The scheduler plan binds immutable attempt-plan identities,
ordinals, rounds, aggregate CPU/memory/storage/process reservations, cumulative
cost, and sorted opaque execution/model/provider cohort limits before
dispatch. The report retains only queue, start, completion, terminal-outcome,
never-started, and stop counters; worker identity and completion order never
enter a result identity.

Raw copied artifact bytes never enter the result or publication. A completion
marker is removed only after two exact, transitive, strict-tree readbacks; the
second brackets its complete physical scan with matching bounded content and
identity inventories, follows the final fault boundary, and rejects
recovery-only ledger residue or changed artifact bytes. Marker removal is the final fallible
process-visible commit operation. Once
that marker has been durably established, every returned run or finish failure
is `outcome_unknown,retry_safe:false` and retains it plus any ledger state
already created; a process or power interruption may conservatively retain the
marker after all result bytes were written. A new run never reopens the same
destination.

`resume/reference` requires that marker, the exact manifest and bundle, the
exact scheduler plan implied by the selected worker width, the complete planned
ledger roster, and every present staged or terminal artifact. It dispatches
only attempts still durably `planned`. A committed nonterminal crash tail is
first closed as absorbing `unknown`; terminal and unknown identities are
preserved and never executed again. Missing, extra, forged, ambiguous, or
never-started artifacts fail closed. An existing scheduler report is accepted
only when every roster member is already terminal, so it can finish publication
but cannot authorize further work. Resume removes the marker only after the
same final exact reread.

Each new destination receives a fresh random ledger identity and therefore
fresh physical attempt IDs, while manifest, treatment, trial, outcome, and
artifact semantics remain deterministic. Cancellation and timeout are terminal
only under the lifecycle evidence actually recorded. The success envelope
exposes only the manifest digest; trial and terminal-outcome counts; admitted
worker width; and queued, started, completed, never-started, and stop counters.
Completed-publication inspection is a read-only contour: it requires the
existing private ledger lock, opens it without create/write access, bounds the
attempt directory read by the exact manifest roster, and rejects every extra
ordinal, crash tail, temporary member, unexpected file, or non-private attempt
directory. Recovery inspection may tolerate such residue; completed analysis
never does.

This profile is provider-free, not a general sandbox claim. The implementation
uses the existing Unix durable ledger and therefore refuses before destination
creation on Windows.
Held-root identity checks and exclusive writes protect the publication from
path drift detectable by the process, but they do not prove isolation from a
hostile same-UID process. A digest binds the declared bytes; it is not
anonymization or permission to publish a private bundle.

### Paired analysis of a completed reference publication

The provider-neutral `analysis` leaf reads no path and owns no execution. The
root facade first applies the complete sequential-publication inspector above,
then passes the canonical manifest and its exact trial-record multiset to the
analyzer. The command therefore refuses an incomplete marker, missing or extra
artifact, digest drift, duplicate JSON member, unknown generation, or broken
transitive binding before computing a statistic:

```shell named-agent-eval-sequential-reference-analysis
agent-eval compare --kind experiment \
  --root /absolute/completed-reference-publication
```

Binary paired dimensions retain every complete pair's opaque ID and two
Boolean observations (so equal pairs still distinguish both-false from
both-true), then report the four-cell table, risk difference, a
deterministic preregistered percentile bootstrap interval, and the exact
two-sided binomial form of [McNemar's paired
test](https://doi.org/10.1007/BF02295996). Continuous count metrics retain every
complete pair's opaque pair ID and exact signed delta, then report the exact
mean and median, the paired-sign effect
`(candidate_higher-reference_higher)/complete_pairs`, the same deterministic
interval policy, and an exact two-sided sign test. Confirmatory families use
the preregistered step-down [Holm
adjustment](https://doi.org/10.2307/4615733); exploratory comparisons retain raw
probabilities and are labeled unadjusted. A confirmatory family retains every
preregistered comparison/stratum/dimension slot: unavailable or descriptive
slots act as probability one, so missingness cannot shrink the Holm family.
Direction-adjusted effects and
regression flags preserve the manifest's declared higher/lower-is-better
semantics, while Pareto status keeps outcome, cost, token, and duration axes
separate instead of collapsing them into a hidden score. Every comparison,
activation summary, funnel, and repeated-attempt projection is emitted per
declared randomization stratum; v1 never pools distinct stratum bindings.
Because experiment-manifest v1 historically admits inference and repeated-attempt
thresholds against its aggregate block count, the analysis consumer performs a
narrower pre-read check: every preregistered `k`, and every non-compatibility
minimum-inference threshold, must fit each stratum's per-treatment fixed roster.
An aggregate-only fit is rejected as unsupported input rather than pooled or
rendered as a silently empty estimate.
The bootstrap draws paired deltas with replacement from a SHA-256 counter keyed
by the preregistered seed, comparison, stratum, and dimension. After sorting
the exact rational replicate means, it selects indices
`floor(samples*(10000-confidence_basis_points)/20000)` and
`samples-1-index`; no platform float or post-hoc random source enters the
interval.

The standalone artifact may be as large as 16 MiB. The one-request ProcessAPI
retains its separate 1 MiB response ceiling and fails closed if the same valid
comparison cannot fit that transport envelope; callers needing the full
bounded artifact use the direct command surface.
V1 also caps the supplied trial-record multiset at 8,192 members, emitted
dimension rows at 16,384, opaque paired-observation/delta rows at 65,536, repeated-attempt
rows at 4,096, and primitive bootstrap selections at 16,777,216; limit
exhaustion rejects the comparison instead of truncating or sampling it. No
single paired-observation or delta list may exceed the manifest's 4,096-block
ceiling, and the JSON reader enforces these structural counts before typed
slice allocation. Clean-singleton coverage retains at most the manifest's
eight closed stage and 64 metric projections per trial, with an aggregate
294,912-projection ceiling enforced before typed allocation.

Missing, duplicate, excluded, and complete pairs remain explicit. With zero
complete pairs a result is `insufficient`; below the preregistered minimum it
is `descriptive`; only the admitted minimum enables `inferential`. Repeated
attempt projections use the declared fixed roster and the exact combinatorial
estimators used for pass@k in the [Codex evaluation
methodology](https://arxiv.org/abs/2107.03374); `pass_power_k` is separately
defined as the probability that all `k` draws pass. A `none` repeated-attempt
policy emits no pass rows; `all` requires a declared outcome metric and an
exactly complete per-stratum roster. Activation
precision/recall, false activation among expected-inactive observations
(`FP/(FP+TN)`), unnecessary load among all observed loads (`FP/(TP+FP)`), and
per-treatment funnel conversion remain separate observations. The report contains only declared
identities, Boolean pair observations, counts, reduced rational values, and digests: no prompt, evidence,
artifact body, path, provider, credential, holdout selection, tuning,
promotion, or automatic decision authority.
Its coverage projection retains the exact sorted manifest trial roster with
each member's record multiplicity and single record-level exclusion. Only a
clean singleton additionally retains the closed presence class and Boolean
value for every declared funnel stage, plus the closed presence class and
Boolean value (for binary metrics only) in manifest metric order. Missing,
duplicate, and record-excluded members retain empty observation projections;
absolute count-metric values remain omitted. This is content minimization, not
anonymization. It lets the reader reconstruct selected-dimension pair reasons
and exactly recompute labeled activation, funnel-transition, and fixed-roster
pass summaries without retaining prompts, paths, bodies, or absolute count
observations. The reader requires the exact bound experiment manifest,
deterministically replays every retained-delta bootstrap interval, proves one
feasible bounded continuous-observation graph, and rechecks pair, comparison,
dimension, stratum, treatment, funnel, activation, fixed-roster, and
preregistered `k` membership; the report digest alone is not publisher
authentication. Cancellation is checked before and after bounded publication
and ledger reads, between artifact decoding boundaries, throughout chunked
publication-body reads, statistical loops, and bootstrap draws.

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
| `atl-profile/synthetic-run-receipt@1..2` | v1–v2 | v2 | v1–v2 | `preserve`; `content_minimized`; `explicit` migration; v1 is readable legacy evidence without an attempt binding |
| `atl-profile/private-workspace@1..4` | v1–v4 | v4 | v4 | `preserve`; `owner_private`; `partial_explicit` migration; v1–v3 are readable only |
| `atl-profile/private-plan@1..9` | v1–v9 | v9 | v9 | `preserve`; `owner_private`; `compare_only`; v1–v8 are readable only |
| `atl-profile/private-review-attempt@1..2` | v1–v2 | v2 | v1–v2 | `preserve`; `owner_private`; `compare_only`; v1 is historical evidence without a generic attempt binding |
| `atl-profile/private-review-receipt@1..2` | v1–v2 | v2 | v1–v2 | `preserve`; `owner_private`; `compare_only`; v1 is historical evidence without a generic attempt binding |
| `atl-profile/activation-reference@1..2` | v1–v2 | v2 | — | `preserve`; `owner_private`; `compare_only` reference envelope |
| `atl-profile/activation-report@1..2` | v1–v2 | v1–v2 | — | `preserve`; `content_minimized`; `compare_only` |

Here, “readable” means accepted by the exact generation reader, “emitted” means the maintained evaluator can write that generation, and “executable” means the generation may enter its existing execution path. An empty column is a deliberate refusal, not missing registry data. In particular, a write-only aggregate can be compared only under its named projection contract; it cannot be reintroduced as source evidence or treated as a readable canonical artifact.

The embedded closed schema registry is the sole machine authority for every
artifact family, owner, generation set, byte bound, privacy class,
disposition, schema resource, and migration policy. The standalone product
contract is an exact projection of that registry, not a second inventory. A
content-addressed inspection reports the family entry and its migration graph;
changing an owner, bound, policy, resource, edge, or implementation identity
changes the registry digest and fails the compatibility oracle. Registry
membership does not make a readable artifact executable, comparable,
promotable, or public.

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
| `agent-eval/experiment-capability-contract` | Complete agent, model, environment, adapter, execution-backend, grader, harness, budget, authority, treatment, channel, funnel-observation, and metric capability claims |
| `agent-eval/experiment-design` | Immutable case, explicit treatments and separately authored controls, strata, balanced order seed, stopping rule, and capability/analysis bindings |
| `agent-eval/analysis-plan` | Preregistered comparison roles, funnel-stage and metric identities/families, repeated-attempt policy, exclusions, confidence/bootstrap parameters, and multiplicity identity; not an analysis result |
| `agent-eval/analysis-report` | Content-minimized paired coverage, exact effects/tests, multiplicity decisions, activation/funnel summaries, repeated-attempt projections, and manifest/input/report bindings; never executable |
| `agent-eval/experiment-manifest` | Canonical treatment, balanced block/order, pair, trial-roster, capability, design, and analysis-plan handoff |
| `agent-eval/trial-record` | Content-minimized lifecycle, eligibility, exclusion, separately qualified funnel-stage observations, metric observations, and source receipt identities for one manifest member |
| `agent-eval/adapter-manifest` | Closed component identity, one declared role and its operations, capabilities, protocol versions, configuration keys, and executable binding |
| `agent-eval/adapter-message` | One bounded process-protocol frame under the selected role, operation, session, and attempt identity |
| `agent-eval/extension-conformance-bundle` | Content-addressed ordinary cases for every supported operation, the grader's required identical repeat, and one synchronized cancellation case in the manifest's declared role |
| `agent-eval/extension-conformance-report` | Content-minimized protocol-only result; never proof of whole-product compatibility or host confinement |
| `agent-eval/agent-adapter-contract` | Content-minimized selected adapter identity, implementation/executable/configuration digests, complete semantic capability claims, and closed configuration-key inventory |
| `agent-eval/agent-observation` | Content-minimized normalized activation, event graph, terminal state, parent/tree usage, consumed-child evidence, and explicit coverage issues |
| `agent-eval/execution-backend-contract` | Complete capability claims plus implementation/content identity and assurance class for one selected backend |
| `agent-eval/trial-plan` | Content-addressed definition, fixture, skill, policy, resource, artifact, program, and verifier admission for one trial |
| `agent-eval/trial-receipt` | Content-minimized terminal verdict, input/resource usage, artifact identities, verifier evidence, cleanup, network, credential, and termination coverage |
| `agent-eval/grader-contract` | Complete check-family, mode, implementation/content identity, authority, and limit claims for one grader |
| `agent-eval/grading-plan` | Preregistered input, environment, check, hidden-verifier, rubric, blind-assignment, reviewer, and resource identities |
| `agent-eval/grade-receipt` | Content-minimized per-check coverage, evidence citations, reviewer provenance, usage, and disagreements |
| `agent-eval/migration-preview` | Content-minimized reviewed binding of source, candidate, registry, migration implementation, graph, and counts |
| `agent-eval/migration-result` | Content-minimized idempotent receipt for one applied reviewed migration |
| `agent-eval/scheduler-plan` | Content-minimized immutable task, ordinal, round, worker, resource, cumulative-cost, and opaque cohort admission |
| `agent-eval/scheduler-report` | Content-minimized queue, dispatch, terminal-outcome, never-started, and stop counters bound to one scheduler plan |
| `agent-eval/schema-registry` | Public closed inventory of artifact ownership, generations, policies, bounds, resources, and reviewed migration edges |
| `agent-eval/sequential-reference-bundle` | Exact manifest binding, deterministic grading plan, three admitted reference execution plans, and bounded content-addressed input snapshots for `run/reference` |

The compatibility ledger records project config, the schema registry, the five
experiment artifacts, the content-minimized analysis report, the two
migration artifacts, the three durable attempt families
(`agent-eval/attempt-ledger`, `agent-eval/attempt-plan`, and
`agent-eval/attempt-event`), each of the four extension families, the semantic
adapter contract, normalized observation, execution-backend contract, trial
plan, trial receipt, grader contract, grading plan, grade receipt, scheduler
plan/report, and the sequential-reference bundle at generation 1. Project
config, registry, experiment capability/design/analysis
and manifest, attempt records, adapter manifest, message, bundle, adapter contract, execution-backend
contract, trial-plan, and grade-receipt generations are readable, emitted, and executable;
experiment trial records, analysis reports, migration artifacts, extension reports, normalized agent observations, and
trial receipts and scheduler reports are readable and emitted but never
executable. Scheduler plans are readable, emitted, and executable only by the
bounded local dispatcher. Grader contracts,
grading plans, and grade receipts are readable, emitted, and executable. A
grade receipt may enter grading only with its exact admitted plan and attempt
identity; it cannot launch a process, select a provider, or acquire authority
by itself. Project config is
`public_or_private` and capped at 64 KiB. Manifests are public and capped at
64 KiB. Attempt headers are capped at 16 KiB and attempt plans and events at
64 KiB per record; all three are `preserve`, `content_minimized`, and use
explicit migration. Adapter contracts are `content_minimized` and capped at
64 KiB; agent observations are `content_minimized` and capped at 1 MiB.
Experiment capability contracts are `public_or_private` and capped at 64 KiB;
experiment designs and analysis plans are `public_or_private` and capped at
1 MiB; compiled manifests are `public_or_private` and capped at 16 MiB; trial
records are `content_minimized` and capped at 1 MiB. The sequential-reference
bundle is `public_or_private`, capped at 64 MiB, preserved, and readable,
emitted, and executable only by the exact reference composition. Executable experiment
rows may enter only the compiler and planned-roster composition path described
above; they do not authorize process launch. Analysis reports are
`content_minimized`, capped at 16 MiB, preserved under `compare_only`, readable
and emitted at v1, and never executable. Execution-backend contracts and receipts are `content_minimized` and capped at
64 KiB; trial plans are `content_minimized` and capped at 256 KiB. Grader
contracts are `content_minimized` and capped at 64 KiB; grading plans are
`public_or_private` and capped at 1 MiB; grade receipts are
`content_minimized` and capped at 4 MiB. Scheduler plans are
`content_minimized`, capped at 4 MiB, preserved, and executable; scheduler
reports are `content_minimized`, capped at 64 KiB, preserved, and never
executable. Messages are
`public_or_private` and capped at 1 MiB, extension conformance bundles are
public and capped at 1 MiB, and reports are `content_minimized` and capped at
1 MiB. Migration
previews and results are `content_minimized`, capped at 64 KiB, and preserved;
the public registry is capped at 1 MiB. All use explicit migration. These
pre-release registry rows
do not make a distribution or command stable. Public conformance cases have
nonnull configuration and input arrays, use only public input and expected
output references, and require `output_privacy:"public"`. The machine rejects
every non-public classification. Bundle authors remain responsible for using
only genuinely public synthetic IDs and digests: structural validation cannot
prove that a `public` label is truthful or make a low-entropy private value safe.

Every stable artifact has a closed `schema` and positive integer `schema_version`. Contract, producer, component, and source identities are separate fields. Strict decoders reject duplicate keys, trailing values, invalid JSON encoding, unknown required vocabulary, and fields outside an explicit namespaced extension object. Invalid known schemas and unknown future schemas fail before mutation or execution.

Source bytes are immutable evidence. Import may create a normalized candidate but records the source identity and digest and never overwrites it. Migration never relabels an old hash as belonging to new bytes.

Migration is preview/apply. The current registry contains exactly one reviewed
edge, `atl-profile/private-workspace` v3 to v4. Every other historical readable
generation is inspectable under its declared policy but has no inferred
migration path.

1. `schema inspect --namespace <namespace> --kind <kind> --output json`
   reads only the embedded public registry and reports its content hashes.
2. `migrate preview --namespace atl-profile --kind private-workspace --from 3
   --to 4 --root <absolute-private-root> --repository-root <root> --output
   json` strictly reads the explicitly selected root without provider, backend,
   credential, or network authority. It reports source/candidate,
   implementation, graph, registry, and preview digests plus preserved counts,
   never artifact contents or paths.
3. `migrate apply` requires the same arguments plus the unchanged
   `--expected-preview-sha256` and `--confirm MIGRATE` in the original
   invocation. Confirmation is never read interactively.
4. Apply revalidates every binding, uses the owner-private workspace lock,
   preserves the exact v3 bytes in the migration archive, installs only the
   canonical v4 candidate, syncs the durability boundaries, and records one
   exclusive content-minimized receipt under the selected private root.
5. Repeating the reviewed apply returns the same receipt. Missing, changed,
   stale, ambiguous, interrupted, future, unversioned, or conflicting state
   fails closed without replacing preserved evidence or inventing fields.
6. The same three operations are available through the strict one-request
   Process API. A deadline after entry without a completion acknowledgement is
   `outcome_unknown` and must not trigger automatic replay; the stored receipt
   and workspace state support an explicit idempotent recovery invocation.

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
operation; a grader has one additional identical `grade` case, and one
additional case proves the synchronized `canceled`
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
Any arbitrary process requirement that needs those controls must still refuse
before spawn. The qualified hermetic reference below deliberately avoids a
child process; it does not retrofit confinement onto this local host.
Once the admitted process successfully starts, any handshake, protocol,
terminal, or cleanup ambiguity produces the absorbing, no-replay `unknown`:
without isolation, the child could already have side effects. Only a refusal
proved before spawn remains a compatibility failure. Neither timeout,
cancellation, an adapter acknowledgment, nor a missing process is enough to
infer otherwise.

The current ATL profile remains an explicitly composed built-in Go component.
The `profile` protocol role does not turn it into a downloadable plugin, add a
dynamic registry, or stabilize any internal `ATL_EVAL_*` environment input.

For the `agent-adapter` role, the generic process manifest is only the transport
contract. A session also binds one canonical
`agent-eval/agent-adapter-contract@1`; its component ID/version, executable
digest, supported process operations, and exact configuration-key names must
match the manifest before spawn. The internal provider-free reference command
is:

```sh
agent-eval verify-agent-adapter \
  --manifest testdata/synthetic-agent-manifest.json \
  --adapter /tmp/agent-eval-synthetic/adapter \
  --bundle testdata/synthetic-agent-conformance.json \
  --contract testdata/synthetic-agent-contract.json \
  --ledger /tmp/agent-eval-synthetic/attempt-ledger
```

It executes the same strict bounded process host, creates one durable attempt
per conformance case, and emits the canonical content-minimized extension
report. Every receipt transitively binds the manifest, executable, semantic
adapter contract, process session, and terminal protocol result. The command is
still an internal protocol diagnostic: it is not `compat verify`, does not
claim whole-product compatibility, and does not add filesystem, network,
credential, resource, or durable termination confinement beyond the host
limits described above.

For the `execution-backend` role, the generic process manifest is likewise
only the transport contract. The semantic diagnostic additionally binds one
canonical `agent-eval/execution-backend-contract@1` and one admitted
`agent-eval/trial-plan@1` before spawn:

```sh
agent-eval verify-execution-backend \
  --manifest testdata/synthetic-backend-manifest.json \
  --backend /tmp/agent-eval-synthetic/backend \
  --bundle testdata/synthetic-backend-conformance.json \
  --contract testdata/synthetic-backend-contract.json \
  --plan testdata/synthetic-trial-plan.json \
  --ledger /tmp/agent-eval-synthetic/attempt-ledger
```

The manifest ID/version/executable, role operations, supported protocol
capabilities, semantic contract, and external-adapter plan must agree exactly.
The resulting extension report and durable case receipts are protocol-only;
they retain the declared `local_process` gaps and do not claim sandbox or
hermetic assurance. The retained local runner copies the provider-relevant
plugin/marketplace projection, the selected ATL binary, the evaluation wrapper,
the initial workspace, and installed benchmark skills into the owned attempt
tree and revalidates their identities at the durable commit boundary. It also
stable-reads the selected agent launcher again immediately before each launcher
entry. The launcher still executes by its selected path so package-managed
scripts, native companions, dynamic libraries, and other transitive runtime
dependencies keep working; a hostile same-UID path replacement between that
read and process entry remains unproved and is one reason this backend stays
`local_process`. By contrast, the built-in `reference-hermetic` backend is
an in-memory test oracle. It admits only exact network-deny, no-credential,
fresh/read-only snapshot, declared-artifact, deadline/storage, separate-copy
verifier, and logical-cleanup requirements. Its timeout/cancel receipt closes
an empty logical process tree. CPU, memory, process-count, arbitrary-command,
and arbitrary-filesystem enforcement are intentionally unsupported.

The neutral grading boundary has three authority tiers. `deterministic`
evaluates typed mechanical checks in-process. `script_dsl` interprets a closed,
bounded boolean DSL over the same immutable evidence snapshot and admits only
the exact `reference-hermetic` backend identity; it never executes a shell,
host script, or arbitrary bytecode. `judge_assessment` accepts only completed
offline three- or five-member reviews. It launches no provider, grants no
tools, and requires a fixed rubric, blind-assignment digest, reviewer/model and
environment identities, per-reviewer token/cost bounds, and content-addressed
citations for every decision. Each qualitative criterion preregisters its exact
sorted evidence-ID projection; a reviewer cannot cite another captured item.

The closed mechanical families are file existence/metadata/SHA-256, JSON value
and schema fields, command exit and output digest, tree diff, tool and action
sequence, skill activation and use, resource budget, and policy violation
count. Missing, inaccessible, wrong-visibility, or destroyed evidence is
`unknown`; it cannot pass. The evidence snapshot owns cloned bytes and exposes
only a content-minimized citation catalog. Receipts preserve each declared
dimension independently—there is no universal weighted score—and record both
reviewer disagreement and deterministic-versus-judge disagreement.

Current ATL run checks use this boundary rather than a second scoring switch.
Before each attempt, the compatibility facade translates the closed ATL check
set into one content-addressed deterministic grading plan bound to the attempt
identity. After execution it projects the final response, audited counters and
sequences, and declared workspace artifacts into immutable evidence; the
neutral grader alone decides every check. The owner-private run directory keeps
the canonical plan and receipt, and the terminal lifecycle receipt binds the
grade-receipt digest. Historical result JSON remains unchanged.

The internal semantic process diagnostic is:

```sh
agent-eval verify-grader \
  --manifest testdata/synthetic-grader-manifest.json \
  --grader /tmp/agent-eval-synthetic/grader \
  --bundle testdata/synthetic-grader-conformance.json \
  --contract testdata/synthetic-grader-contract.json \
  --ledger /tmp/agent-eval-synthetic/attempt-ledger
```

It binds the grader contract into every durable conformance attempt and
requires two distinct grade cases with identical configuration, input
references, policy, and expected terminal output, plus validate and synchronized
cancel coverage. This proves deterministic process behavior only for the
public synthetic references in that bundle. It does not authorize the process
to read hidden evidence and does not upgrade the local process host to an
isolated verifier. Arbitrary external grader execution remains refused until
an execution backend can enforce the declared isolation.

Normalized observations use a closed event graph with one primary node and at
most two child levels. The graph distinguishes single-agent, generic-child,
specialized-child, and parallel-child profiles; native, developer-instruction,
forced-injection, combined, and unavailable activation; observed,
self-reported, and unavailable use evidence; and observed, unknown,
unsupported, and not-applicable metrics. Numeric zero is observed only when its
metric state is `observed`.

Parent and whole-tree usage are separate projections. A reported tree total is
fully covered only when the event graph structurally attributes it to the
primary and admitted child nodes. Legacy aggregate provider totals with no
stable child identities are preserved as reported tree usage, but receive
`tree_usage_unattributed` and `coverage:false`; they cannot be silently assigned
to the parent or invented child attempts. An unknown terminal state is retained
but makes coverage false. Handoff evidence contributes only
when the child is terminal and explicitly marked consumed. Malformed ordering,
orphans, cycles, depth, role/profile, capability-subset, duplicate, or
incomplete-node violations likewise fail closed through explicit issues.

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

The current pre-release implementation enforces this relation in the neutral
`lifecycle` package and stores one owner-private append-only directory ledger.
The store bounds attempts and events, uses canonical one-line JSON, assigns
monotonic ordinals itself, hash-binds every plan and event, fsyncs before
advancing, rereads every append, and serializes readers and writers through an
advisory lock. It refuses Windows storage before creation because the current
durability primitive requires directory fsync; Windows decoding and compile
coverage remain available, but no weaker durable-write claim is made. The
owner-private root is the trust anchor: this store does not claim isolation
from a hostile process already running as the same OS user.

`RunHeadless` writes the complete preflight-plus-repetition roster beneath the
selected output root before its first evaluation-component process entry; the
content-minimized repository-ignore check remains part of local output-root
admission. Extension conformance,
capability verification, calibration, CLI-route and tool-availability probes,
selected-binary synthetic execution, and automated private review enter
through the same session owner on hosts where the persistent ledger can prove
its owner-only and directory-durability contract. Current synthetic run receipts are v2 and bind
their result to the exact attempt binding; v1 receipts remain readable only for
historical roots that contain no generic ledger. Aggregate reconstruction
rejects a missing, reused, mismatched, nonterminal, or incomplete current
binding. Private activation remains the stricter ordering/consent authority;
its generic ledger is nested in each raw run and its execution receipt binds
the resulting aggregate. Automated review writes its generic ledger inside
the owner-private review packet. Historical private records are not rewritten.

The current persistent ledger is implemented on Unix hosts and fails closed on
Windows before any evaluation-component process entry. In particular, the
internal `verify-extension-protocol` file facade returns `outcome_unknown`
before admitting the executable when it cannot create that ledger. Hosted
Windows tests continue to exercise the bounded extension process protocol and
separately prove this no-entry refusal; they do not manufacture an in-memory or
weaker durable record. Windows persistence remains unsupported until directory
entry durability and owner-only storage have a runtime-proved implementation.

On a clean nonterminal prefix, recovery closes `planned` as proven
precommit `canceled` and closes every postcommit state as absorbing `unknown`;
it never launches work. A torn or corrupt tail remains byte-for-byte on disk,
the inspection projection reports terminal `unknown` plus a closed tail code,
and append/replay stays blocked. Evidence-only reconciliation creates a new
linked plan and never mutates the unknown predecessor. The internal maintainer
surface is:

```sh
agent-eval attempt-ledger inspect --root /absolute/owner-private/attempt-ledger
agent-eval attempt-ledger reconcile \
  --root /absolute/owner-private/attempt-ledger \
  --attempt aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --evidence bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
```

These commands emit content-minimized reports only. They are the current
maintainer alias for reserved standalone `reconcile/evidence-only`, not a
stable public lifecycle CLI and not authority to replay the predecessor.

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

A stable distribution publishes content-addressed synthetic fixtures. Provider-free conformance proves JSON/error/exit contracts; configuration precedence; no ambient authority discovery; pre-execution capability refusal; historical readability and future rejection; migration binding; missing-versus-zero behavior; component confinement; deterministic rereads; and no replay. The current source suite additionally exercises the production ledger and transition matrix, crashes a subprocess immediately before and after every allowed append, verifies conservative recovery, and inventories every production process-entry call against its durable owner. This remains pre-release source evidence, not a signed-distribution compatibility promise.

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
