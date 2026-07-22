# Private agent-benchmark workspace

This guide is for maintainers who run supervised model-in-the-loop evaluations
against a private Jira or Confluence backend. It defines the local operating
boundary around the generic framework in
[agent-benchmarking.md](agent-benchmarking.md). It does not authorize a run,
identify a backend, or make a private result publishable.

Public synthetic scenarios remain the default for CI and routine development.
Use a private workspace only when a change needs backend-compatibility evidence
that a deterministic fixture cannot provide.

## Trust boundary

A private run may send the reviewed prompt and the evidence selected through
the allowed CLI or MCP surface to the configured model provider. Local cleanup
does not delete provider-side or upstream-service logs. Create a reviewed plan
only after the owner has accepted that boundary for the named provider, model,
data class, and short consent window.

Persistent cases, plans, runs, baselines, and reports never contain source
credentials. The manifest stores only environment-variable names that point to
an owner-only ATL config directory or external MCP profile. During an approved
run, the parent may copy those inputs into an owner-only `.ephemeral` execution
snapshot. Codex runs also create an isolated provider-home capsule there and
copy only the validated owner-only file-backed `auth.json`; ambient global
instructions, config, user skills, history, sessions, memories, and caches are
not copied. Each surface/repetition starts with a fresh capsule; only bounded,
syntactically valid JSON auth refreshes flow forward through an in-memory plan
session. The
operator's source `auth.json` is read once and never modified. Normal return
removes both transient trees and model-spawned tools never receive upstream or
provider credentials. A crash residue makes doctor fail closed and must be
handled inside the private root.

## Fixed layout

The lifecycle uses one marked owner-only root rather than unrelated output
directories:

```text
PRIVATE_ROOT/
  .atl-agent-eval-private-root
  private-workspace.v4.json
  cases/
  plans/
  runs/
  baselines/
  reports/
  .ephemeral/
```

Directories are `0700`, regular files are `0600`, and symlinks are refused.
When the root is inside a worktree, the repository must ignore it. An existing
non-empty unmarked directory is never adopted or chmodded implicitly.

- `cases/` is the private source of truth for prompts, scenario contracts,
  response schemas, rubrics, workspace templates, and per-surface run specs.
- `plans/` contains immutable, expiring, hash-bound approvals.
- `runs/` contains raw candidate attempts and remains private.
- `baselines/` contains compact promoted evidence and an atomic current pointer.
- `reports/` contains private offline comparisons.
- `.ephemeral/` is bounded scratch space. Cleanup is attempted on every ordinary
  return; failure fails the run closed and leaves reviewable residue rather than
  hiding it. Crash or cleanup residue is never reused and blocks doctor until
  reviewed recovery.

## Agent operating protocol

An agent working on `atl` should follow this order:

1. Run workspace status/doctor before reading case files or raw artifacts.
2. Prefer the public synthetic suite unless the requested compatibility claim
   explicitly requires a private backend.
3. Qualify the exact native Codex binary, model, and reasoning setting through
   the credential-free loopback inventory check before authorizing a provider
   call.
4. Create and review a dry plan. Never infer consent from an old transcript or
   from the mere presence of credentials.
5. Execute only the exact plan hash, sequentially, with the explicit run
   confirmation. Do not construct ad-hoc shell loops around low-level `run`.
6. Apply deterministic checks before qualitative review. For a review panel,
   prepare every predeclared reviewer's fixed-layout packet before running or
   assessing any of them; do not discover raw result paths with an ad-hoc
   filesystem scan. An executable panel uses one fresh Codex or Claude Code
   context per slot and requires its terminal no-tools receipt. A judge cannot
   rescue an incorrect, unsafe, incomplete, or over-budget run.
7. Promote only a complete reviewed surface set whose panel has reached one
   consensus. A disagreement is not promotable. Compare offline against a
   baseline bound to the same singleton or panel contract.
8. Preview pruning, review the content-bound prune hash, and then confirm the
   same plan. Baselines, cases, active runs, and paths outside the marked root
   are never eligible.
9. Keep raw prompts, responses, transcripts, commands, routes, identifiers, and
   review rationale private. Publication is a separate manual privacy review.

Administrative command output is intentionally sparse: closed finding/action
codes, counts, surfaces, generic task classes, hashes, and aggregate metrics.
Detailed diagnostics are written owner-only inside the workspace rather than
echoed into a terminal or CI log.

## Lifecycle command matrix

| Stage | Command | Model/backend access | Local effect | Required review gate |
|---|---|---|---|---|
| Initialize | `private init` | None | Creates the marked fixed layout | New or empty owner-only root |
| Inspect | `private status` / `private doctor` | None | Read-only | None |
| Preview migration | `private migrate` | None | Read-only manifest projection | Healthy, quiescent schema-v3 workspace |
| Apply migration | `private migrate` with confirmation | None | Installs the exact schema-v4 candidate, then removes the v3 manifest | Exact migration SHA-256 and `MIGRATE` |
| Qualify Codex CLI | `private qualify` | None; one synthetic loopback Responses request | Temporary owner-only runtime removed on return | Exact native binary, model, and reasoning setting |
| Plan | `private plan` | None | Writes one immutable plan | Data-boundary flags and `CONSENT`; reviewed live writes additionally require `--approve-live-writes` and `CONSENT-WRITES` |
| Execute | `private run` | Reviewed model and reviewed backend routes | Writes one candidate run | Exact plan SHA-256 and `RUN`; a write-bound plan requires `RUN-WRITES` |
| Recover study | `private study recover` | None | Closes an interrupted study without replaying the provider after the operator establishes provider-process quiescence | Exact plan SHA-256 and `PROVIDER_STOPPED_RECOVER` |
| Prepare review | `private review prepare` | None | Copies one result, answer, rubric, and hash-bound template into an owner-only packet | Completed plan and explicit surface; every panel member before assessment |
| Run review | `private review run` | One reviewed model request; no backend or model tools | Commits one terminal attempt and content-free receipt | Complete executable roster, exact plan hash, reviewer binary, and `RUN-REVIEW` |
| Assess | `private review assess` | None | Records one member; the last member writes one median consensus | Completed bounded review and, for executable panels, a valid no-tools cost receipt |
| Promote | `private baseline set` | None | Adds an immutable compact baseline and updates `current` | Complete assessed run without panel disagreement and `BASELINE` |
| Compare | `private compare` | None | Read-only; emits aggregate deltas | Compatible contract/runtime and review mode |
| Reference study | `private study reference` | None | Adds an immutable compact activation-study reference | Complete four-cell review and `REFERENCE` |
| Compare study | `private study compare` | None | Read-only; emits privacy-safe treatment metrics, gates, and eligible contrasts | Structurally valid immutable reference |
| Promote study | `private study promote` | None | Updates the activation-study `current` pointer | Strict promotion eligibility and `PROMOTE` |
| Retain | `private prune` | None | Preview is read-only | None |
| Prune | `private prune` with confirmation | None | Removes raw artifacts from eligible runs and leaves compact lifecycle tombstones | Exact inventory SHA-256 and `PRUNE` |

`private plan` is not authorization to execute, and `private run` is not
authorization to promote or delete. Each transition has its own bound gate.

## Initialize and configure

Build the maintainer tool and create an empty workspace. Set `umask 077` before
every private session: the lifecycle normalizes its candidate tree after a
child exits, while the outer umask protects files created by that child during
execution.

```sh
umask 077
go build -o /tmp/agent-eval ./scripts/agent-eval

/tmp/agent-eval private init \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root .
```

Edit the generated private manifest locally. Current schema v4 gives each run set a
`kind`: `comparison` accepts one to three unique surfaces, while
`activation-study` requires exactly four otherwise-identical Codex
`private-live` `cli-skill` v7 specs carrying `implicit`, `explicit`, `developer`,
and `combined` once each. An omitted kind is the legacy comparison form. Runtime
bindings are environment-variable names, never literal paths or credentials.
Keep provider-specific profiles outside the repository as required by the
existing private-live transport. The public
[JSON Schema](../benchmarks/agent-eval/private-workspace.schema.json) supports
editor validation, and the
[generic example](../benchmarks/agent-eval/private-workspace.example.json)
shows a comparison without backend-specific values. The Go decoder remains the
authoritative strict validator.

### Migrate a schema-v3 workspace

A healthy schema-v3 workspace remains readable, but it cannot create a new
plan. Migrate it only while no plan is pending and no run is active. The first
command is a read-only preview: it derives a byte-stable v4 manifest by changing
only the schema version and returns source, candidate, and domain-separated
migration digests plus preserved counts. It does not invent reviewer execution,
pricing, timeout, cap, or reserve settings. Preview may create or reuse the
owner-only advisory lock file in `.ephemeral/`; it does not change a manifest,
case, plan, run, baseline, or report.

```sh
/tmp/agent-eval private migrate \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root .
```

After reviewing the preview, apply that exact projection with its
`migration_sha256`:

```sh
/tmp/agent-eval private migrate \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --expected-migration-sha256 "$REVIEWED_MIGRATION_SHA256" \
  --confirm MIGRATE
```

Apply revalidates the source, candidate, tree, lifecycle, and reviewed digest
under the workspace lock. It writes and verifies `private-workspace.v4.json`,
fsyncs the root directory, durably archives the exact v3 bytes under
`reports/`, then atomically stages the v3 source in `.ephemeral/`. The stage
directory is fsynced before the source directory; the implementation then
revalidates both inode contents and the workspace, removes the staged source,
and fsyncs the stage directory again. An interruption therefore never removes
the only durable valid manifest or the reviewed source bytes. A retry with the
same reviewed digest may finish only when the dual manifests, staged source, or
archive form that exact transition; any different recovery state is left
untouched and rejected. Cases, plans, runs, baselines, existing reports,
ordering, and all v3 manifest fields remain unchanged. Schema v1/v2 and
already-current v4 workspaces are never rewritten.

Preview is available on Windows, but apply fails closed there because the
current implementation cannot prove durable directory-entry ordering with
Windows handles. Perform the reviewed migration on a supported POSIX host; do
not copy or rename manifests manually to bypass this boundary.

The migrated manifest keeps existing panels manual. Add a complete executable
roster and its matching reserve as a separate owner-reviewed v4 edit, run
`private doctor`, and create a fresh immutable plan. The plan binds those exact
model, reasoning, pricing, timeout, cap, and reserve choices before any provider
or backend call.

```json
{
  "schema_version": 4,
  "live_config_env": "ATL_AGENT_EVAL_LIVE_CONFIG_DIR",
  "external_mcp_profile_env": "ATL_AGENT_EVAL_EXTERNAL_MCP_PROFILE",
  "execution": {
    "max_estimated_cost_microusd": 50000000
  },
  "retention": {
    "keep_completed_run_sets_per_alias": 3,
    "max_candidate_age_days": 14,
    "max_candidate_bytes": 2147483648,
    "retain_baseline_transcripts": true
  },
  "run_sets": [{
    "kind": "activation-study",
    "alias": "activation-study",
    "spec_paths": [
      "cases/activation-study/run.implicit.json",
      "cases/activation-study/run.explicit.json",
      "cases/activation-study/run.developer.json",
      "cases/activation-study/run.combined.json"
    ],
    "qualitative_review_required": false,
    "qualitative_review_panel": {
      "method": "criterion-median-v1",
      "reviewers": [
        {"id": "reviewer-01", "kind": "codex", "model": "gpt-test-reviewer"},
        {"id": "reviewer-02", "kind": "codex", "model": "gpt-test-reviewer"},
        {"id": "reviewer-03", "kind": "codex", "model": "gpt-test-reviewer"}
      ],
      "max_criterion_range_bps": 2500,
      "blind_assignment": "cases/activation-study/blind-assignment.txt"
    },
    "reviewer_reserve_microusd": 1000000,
    "calibration_max_estimated_cost_microusd": 500000
  }]
}
```

An activation study requires the panel shown above and cannot use the legacy
singleton policy. Its one calibration cap, four treatment caps, and positive
`reviewer_reserve_microusd` must fit under the workspace execution maximum. A comparison may still use
`qualitative_review_required: true` for one legacy review, or declare the same
panel form. A run set cannot combine the singleton setting with
`qualitative_review_panel`. A panel declares exactly three or five reviewers,
their generic ids, kinds, exact model identities, the fixed
`criterion-median-v1` method, and a `max_criterion_range_bps` threshold from 1
through 9999. Human reviewers may omit `model`; model reviewers may not. A
comparison with `qualitative_review_required: false` and no panel keeps review
disabled.

To automate a panel, add an `executions` entry for every reviewer id. Each
entry binds reasoning, timeout, token pricing, and a per-slot cost cap. Codex
and Claude Code slots may be mixed in one panel. The positive
`reviewer_reserve_microusd` must cover the sum of all slot caps multiplied by
the number of surfaces or activation cells. A panel without `executions`
retains the manual workflow; a partial execution roster is invalid. Reasoning
must be a native level for the selected client (`minimal` is Codex-only and
`max` is Claude Code-only); the shared levels are `low`, `medium`, `high`, and
`xhigh`.

Reviewer ids are terminal-visible filesystem slot names. Keep them generic
(`reviewer-01`, not a person, team, provider account, or backend identity); they
are restricted to one lowercase path component. Schema v1 manifests remain
readable as legacy comparisons. Workspace-manifest v2 activation studies,
outer private-plan v5/v4 artifacts, outer execution-state v2 artifacts, and
their nested lifecycle plan/event v1 records remain inspectable, but cannot
execute, recover, become references, or be promoted. Plan v5 predates the bound
tool-availability result; v4 also predates calibration and attempt evidence.
Workspace-manifest v3 and plan v6 artifacts remain readable for manual review,
but new plans require workspace schema v4. Create and review a v4 workspace for
new measurements.

The panel `blind_assignment` is a workspace-relative file below `cases/`. It is
required for every activation study and for any other `neutral-common` run set. Activation-study
packets are treatment-blinded by the lifecycle even when the task category does
not require a separate assignment file. The complete roster and policy, plus
the assignment digest when present, are bound into the immutable plan before
any model or backend execution. They are copied into the retained run contract,
so changing a reviewer, exact model, threshold, or assignment invalidates the
reviewed plan instead of altering a completed candidate.

Run doctor after every manifest or case change:

```sh
/tmp/agent-eval private doctor \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root .

/tmp/agent-eval private status \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT"
```

Doctor validates the marker, owner-only tree, Git boundary, strict manifest,
case containment, run-spec contracts, comparison equivalence, and lifecycle
state. Runtime bindings and their owner-only modes are validated while creating
the reviewed plan. Stdout does not enumerate case aliases or private paths.

Before creating a Codex activation-study plan, inspect the exact model-facing
local-execution inventory without provider authentication or backend authority:

```sh
/tmp/agent-eval private qualify \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --agent-binary "$REVIEWED_AGENT_BINARY" \
  --model "$REVIEWED_MODEL" \
  --reasoning "$REVIEWED_REASONING"
```

The command hashes and validates the reviewed native binary, executes an
owner-only copy of those exact bytes with a fresh auth-free home, and uses a
nonce-scoped loopback Responses endpoint. It accepts exactly one bounded
request, rejects authentication headers, and returns a fixed synthetic answer;
no model, provider, Jira, or Confluence request is made. Its content-free report
contains only the binary identity, qualification-contract digest, a closed
status, the recognized direct-shell or code-mode route alias when supported,
and zero-authority counters. A code-mode route must be a grammar-bound custom
`exec` host exposing the pinned shell pair plus the exact closed-schema `wait`
companion; mixed and broadened inventories fail closed. Report schema v2 binds
this expanded classifier; legacy schema-v1 reports accept direct-shell aliases
only.
It never retains the prompt, request body, tool schemas, command output, paths,
credentials, or backend identity. Missing, ambiguous, malformed, repeated, or
absent inventory fails closed. Activation-study planning repeats this check
before persisting a plan, and execution repeats it before consuming that plan.

## Review, run, and assess

A v8 plan binds the exact comparison or activation-study contract and execution
identity: case inputs, ordered surfaces, skill activation and private
prompt-contract digest, ATL and wrapper binaries, plugin/skill tree, agent
runtime and tool-availability qualification contract/result, repository commit,
backend-config identity, external profile when used, cost cap, and consent
expiry. Credential bytes are never hashed into a plan or retained in a
run/baseline.

Actual Codex execution requires file-backed provider authentication. The
effective `CODEX_HOME` (or `HOME/.codex` fallback) must be a real directory not
writable by group/other on POSIX, and `auth.json` must be a regular,
non-symlink JSON object no larger than the runner limit (owner-only mode on
POSIX). Actual Codex execution fails closed on Windows until equivalent ACL
ownership validation is available; plan validation and dry-run remain
available. Keyring-only login is rejected before model or backend access. The
runner never adds the source path, credential bytes, or credential hashes to
plan/result/baseline metadata or its own error messages. Raw provider
stdout/stderr/final responses remain unsanitized private artifacts and can
contain anything the provider elects to print; inspect them before baseline
promotion and never publish them without a separate privacy review.

Provider changes to the projected authentication are bounded and checked as a
syntactically valid JSON object before the next surface, then discarded when
the plan ends. This is structural validation, not proof that a credential is
usable. Other provider state never crosses the capsule boundary. If the
operator's subscription login has expired or requires renewal after a run,
renew it with the provider CLI; the benchmark does not reconcile credentials
back into ambient state.

The isolated Codex command also pins unrelated provider-managed tool features
off. Account-side Apps, browser/computer tools, image generation, and remote
plugins are not part of a reviewed CLI/MCP comparison. The
benchmark keeps the local hook-guarded shell for `cli-skill` and only the exact
configured MCP server for MCP surfaces. If an agent returns a structured answer
without using the reviewed interface, the surface-specific guarded audit proves
that zero requests crossed the reviewed backend boundary; the run is retained
as a measured failure, not discarded as an execution error. The reviewed Codex
binary must recognize these feature flags; an older incompatible binary fails
closed before model or backend access under `--strict-config`.

For `cli-skill`, the command explicitly enables Codex's local shell/unified
execution features and the capsule supplies `/bin/sh` as a fixed shell. Ambient
shell selection and startup state are never projected. The shell remains inside
the existing hook, filesystem, command-broker, read-only, and GET/HEAD controls;
MCP surfaces do not opt into these CLI-only feature flags.

Claude Code private CLI cells use the same parent-side command broker, keeping
the real binary and disposable backend config out of the provider process. If
an allowed UTF-8 result exceeds 24 KiB, the shim stages it as an immutable,
content-addressed owner-private file and returns paging instructions. Claude's
runner-exposed `Read` tool is admitted only for that exact run-local
directory, a digest-matching mode-0400 file, windows of at most 500 lines, and
eight total reads. The content-free `tool_result_read` audit family is not CLI
or backend evidence. Cross-run, mutable, digest-mismatched, oversized, and
replayed reads remain denied. The emitted pointer gives the sequential offsets
and says explicitly that Bash, assignments, and shell text processors are not
authorized for staged content.

After the credential-free inventory gate succeeds and before the first
treatment, a current activation plan runs one backend-free provider/tool-path
calibration through the same isolated Codex runtime,
installed plugin, shell feature flags, hook, shim, command broker, and
permissions. The only admitted command is one local `atl version`; the
calibration receives no backend config, URL, PAT, gateway, or corporate
credential and must observe one provider command event, one `atl`-family hook
admission, one successful broker record, and bounded non-empty output. Zero
backend authority and zero writes are construction-derived from that stripped
environment plus the exact `atl version` broker policy; they are not claimed as
gateway-observed HTTP telemetry. Its closed response schema explicitly types
every property so the reviewed contract is accepted by strict structured-output
providers. The model must return the exact `version`, `commit`, and
`build_state` values it observed. The confined proxy normalizes the actual
brokered stdout and records only its SHA-256; the response is normalized the
same way and compared in constant time in addition to the authoritative hook
and broker audit. Raw version values remain only in owner-private provider
artifacts; they are not copied into the durable calibration receipt or
sanitized audit/baseline. This prevents a known constant response from passing
when the model skips or misreports the tool. Audit classification distinguishes
provider-process failure, response-schema failure, policy denial, model
non-invocation, invocation failure, and successful brokered evidence. Every
failure is terminal infrastructure evidence and no treatment cell is reserved.
Calibration is not a fifth arm and does not advance the balanced treatment
order. Its cap is a separate reviewed cost partition.
Before calibration writes provider artifacts, the lifecycle initializes the
shared owner-only raw output root with the normal agent-eval marker. Treatments
reuse that same validated root; a missing, changed, loose, symlinked, or
otherwise invalid marker remains a fail-closed execution error.
The calibration timeout is derived rather than separately configured: it is
the reviewed treatment timeout capped at 300 seconds. Treatment cells keep
their full reviewed timeout. This keeps the local `atl version` preflight
bounded while letting longer evidence tasks use the timeout they need, and the
derived value is part of the immutable calibration contract.

Every activation response schema must also require this closed content-free
member:

```json
{
  "evidence_outcome": {
    "state": "none"
  }
}
```

`state` is exactly one of `none`, `unavailable`, `blocked`, `failed`,
`partial`, or `succeeded`. The model report can explain a zero-call result, but
is never proof of an attempt. Audit-derived counters remain authoritative; a
contradictory report fails closed. A calibrated route plus audit state `none`
therefore means the runtime path worked during calibration and the treatment
made no observed attempt. Public-safe study artifacts retain only these closed
states, bounded counters, scores, and contrasts.

Codex `cli-skill` run specs choose one cell from the complete prompt-channel
2x2. A named skill is derived only from reviewed `data_capabilities`:

| `skill_activation` | User channel (effective stdin) | Developer channel |
| --- | --- | --- |
| `implicit` | Exact core task bytes | Neutral evidence instruction; no skill name |
| `explicit` | `$atl:jira\n\n` or `$atl:confluence\n\n`, then exact core task | Neutral evidence instruction; no skill name |
| `developer` | Exact core task bytes | Evidence instruction names the derived skill |
| `combined` | `$atl:jira\n\n` or `$atl:confluence\n\n`, then exact core task | Evidence instruction names the same derived skill |

For `developer` and `combined`, the developer instruction preserves the exact
pre-v4 compatibility-control wording: `This is an evidence task. Before
answering, select and follow the installed $atl:jira skill implied by the
reviewed data capabilities, then use…` or the corresponding Confluence form.
The remaining safety/evidence text is byte-identical to that control. All
developer variants require evidence through the literal `atl` executable and
never reveal a case-specific command, field selector, expected answer, backend
identity, or command allowlist. `explicit`,
`developer`, and `combined` fail closed unless the capability set is non-empty
and entirely Jira or entirely Confluence; mixed and unknown families are valid
only for `implicit`. Validation completes before credentials, provider
execution, or backend access.

The plan and raw result bind the treatment separately and inside a canonical
schema-v1 prompt-contract envelope with the exact core prompt, effective stdin,
and exact developer instruction; SHA-256 covers the whole envelope. Thus every
named channel and the unchanged task bytes are covered without publishing them.
Low-level dry-run exposes only `prompt_contract_bound:true`. Keep the digest
private: it is omitted from preview and aggregate JSON because short private
prompts may be dictionary-guessable.

An `activation-study` plan binds all four treatments under one consent, one
execution snapshot, and one provider-auth session. The four specs must live in
one case directory and match in every contract field except `skill_activation`
and the per-cell `variant`. The plan retains one common-contract digest plus the
exact digest of every treatment spec. Cells remain the same `cli-skill`
surface; never disguise treatments as different surfaces. Ordinary
multi-surface comparison plans continue to accept only `implicit`, because
naming a skill in either channel is a surface confounder.

The order cycle is keyed by the exact reviewed material, not the run-set alias.
Only a terminal execution with a durable commitment immediately before provider
spawn advances it; a bare pre-call launch marker is insufficient. An expired plan or a recovered
pre-provider interruption receives the same order again.
Each reviewer-facing cell id is freshly random and has no derivable mapping
from plan id to treatment.

Every activation study requires the predeclared blinded three- or five-member
panel. All four treatment-by-reviewer packet slots are fixed before provider
execution. The plan also partitions the calibration cap, sum of the four
treatment caps, and positive explicit reviewer reserve under the workspace
maximum. The runner
does not launch panel reviewers, so this reserve records reviewed authorization
rather than durable reviewer-spend receipts; supervise model-reviewer cost
separately. Treatment cost assurance is detection-only, not a preventive or
provider-side hard cap: after each provider call, the runner records the event
chain and checks reported cost and coverage. Detected exhaustion, unknown cost,
provider uncertainty, or failure to persist or contain the state stops the
remaining cells; a safety violation does too. None of those checks can undo
cost already incurred.

Workspace-manifest v1 comparisons remain readable. Legacy activation
workspace-manifest v2, outer private-plan v5/v4, outer execution-state v2, and
nested lifecycle plan/event v1 artifacts remain readable for inspection. Plan
v5 predates the bound tool-availability result; v4 also predates calibration.
Legacy activation artifacts are explicitly
incomparable and cannot be executed, recovered, captured, or promoted. Four
treatments collected under separate legacy plans are descriptive compatibility
observations only: they do not acquire the study's ordering, shared state, or
causal gates and must not be used to estimate channel effects or interaction.

Run-spec schema v7 carries the four treatments, reviewed live-write boundary,
and internal provider-calibration contract. Existing v6 activation specs remain
readable but cannot enter a current calibrated study. Existing v5 activation
specs and v4 `implicit` and
`explicit` specs retain their meanings when deliberately migrated. Legacy v3
specs that named a skill through provider instructions are not silently mapped
to `developer`; create and review a new activation-bound v7 spec and baseline.
Legacy prompt-bound result v5/private-plan v2 artifacts retain only
`implicit`/`explicit`; result v3/v4 and private-plan v1 remain readable under
their earlier rules. None can be silently reclassified into a new treatment.
Private Codex CLI runs add the snapshotted repository as an owner-local
marketplace and install `atl@atl` through Codex's plugin command before provider
launch. The resulting skills therefore retain their shipped `atl:` namespace;
they are not rewritten as project skills. The complete Codex plugin package,
manifest, marketplace descriptor, and routing metadata are hash-bound, while
the installed copy must reproduce the same package digest. Every discovered
bundled MCP server is disabled in the fresh config and the effective inventory
is rechecked before the CLI-only cell. Guarded reference reads admit only that
installed plugin's skill root. Claude
Code runs continue to use the generated root plugin tree. Every selected tree
is copied into the immutable execution snapshot before the plan is revalidated,
so a client-specific package or routing change invalidates the reviewed bytes.
Codex JSONL does not currently expose a trustworthy native skill-load event.
Installation fidelity is therefore proven separately from task behavior: the
neutral-user-task cell passes only when the guarded audit observes the required
reviewed CLI evidence invocation and the answer oracle succeeds. Do not report
that as a direct measurement of skill loading.

Codex may express a generated skill read as a workspace-relative bounded
`cat`, `sed`, or `wc` command. The guard resolves such a path from the exact
canonical ephemeral workspace supplied by the runner. The same canonical
workspace and ordered JSON read-root set are passed to tool subprocesses and
embedded as shell-quoted assignments in the exact PreToolUse hook command. The
same command explicitly binds its guard mode, owner-private audit counter, and
exact MCP tool allowlist, so the safety decision does not depend on ambient hook
environment propagation.
The target must still resolve within the reviewed workspace or installed skill
roots; missing, relative, duplicate, unclean, traversal, symlink-escape, and
unrelated-reader policies are denied. Codex MCP runs need only the generated
workspace tree. CLI cells additionally admit the verified installed-plugin
skill root. These policy values contain no credential or backend identity;
external-MCP cells continue to carry their existing disposable loopback
capability and proxy-bypass fields.

`--agent-binary` must identify a reviewed native executable for the host OS and
architecture. A symlink is accepted when its canonical target is such an
executable; scripts, JavaScript/package launchers, malformed binaries, and
binaries for another platform are rejected. Do not assume that the first result
from `command -v` satisfies this contract. Resolve and review the provider's
native executable once, keep its absolute path in a session-local variable, and
pass the same value to plan and run. The lifecycle structurally parses ELF,
Mach-O/classic fat Mach-O with `LC_MAIN`, or PE as appropriate and hashes the
canonical target path and bytes without retaining that path. On Linux, a native
`codex` distribution that declares the fixed adjacent `codex-resources/bwrap`
sandbox helper also binds that helper's native bytes and relative layout. Only
that one helper is copied into the owner-only snapshot; absence, malformed
bytes, snapshot substitution, or source drift fails closed. It does not execute
`--version` or any other agent command during plan/snapshot preflight, supplies
no provider or backend credentials, and intentionally makes no model or
Atlassian request. The check does not claim that dynamic system libraries are
absent or that every normal execution path is resource-independent; the
operator must review the native provider distribution. Arbitrary package-tree
snapshotting remains intentionally unsupported.

The command first performs contract, comparison, runtime-binding, and input
identity preflight without invoking a model or backend. It then writes an
immutable plan and returns its SHA-256. Execution must re-read every input and
match that digest before invoking a model or backend. It copies the reviewed
case tree, skill/plugin identity, executables, and runtime configuration into
an owner-only `.ephemeral/execution-*` snapshot, verifies the snapshot against
the plan, and executes from that snapshot. Normal completion removes the
snapshot; doctor reports crash leftovers instead of silently reusing them:

```sh
REVIEWED_AGENT_BINARY=/absolute/path/to/reviewed-native-agent

/tmp/agent-eval private plan \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --run-set activation-study \
  --repository-root . \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --agent-binary "$REVIEWED_AGENT_BINARY" \
  --consent-expires "$REVIEWED_CONSENT_EXPIRY" \
  --approve-provider-data \
  --confirm CONSENT

/tmp/agent-eval private run \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --plan "$REVIEWED_PLAN_ID" \
  --expected-plan-sha256 "$REVIEWED_PLAN_SHA256" \
  --repository-root . \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --agent-binary "$REVIEWED_AGENT_BINARY" \
  --confirm RUN
```

Read-only plans remain the default and cannot gain write authority at execution
time. A private-live CLI comparison may opt into reviewed writes only in a
current run spec with `allow_live_writes:true`, positive scenario write and
request-body budgets, exact mutating gateway paths and methods, exact structured
CLI arguments, and an `http_methods_equal` oracle. The child provider still
starts with `ATL_READ_ONLY=1`; only the literal guarded form
`env -u ATL_READ_ONLY atl ...` may ask the owner-only broker to execute one of
those exact argv rules. The broker owns the real config, and the model receives
only disposable loopback gateway capabilities.

Create and later edit operations belong in separate immutable plans. The create
plan may authorize only its exact collection endpoint; once the new identifier
is known and reviewed, a fresh plan must bind every edit or artifact route to
that exact identifier. Never replace an unknown identifier with a prefix or
wildcard. A write plan uses distinct confirmations:

```sh
/tmp/agent-eval private plan \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --run-set "$REVIEWED_WRITE_RUN_SET" \
  --repository-root . \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --agent-binary "$REVIEWED_AGENT_BINARY" \
  --consent-expires "$REVIEWED_CONSENT_EXPIRY" \
  --approve-provider-data \
  --approve-live-writes \
  --confirm CONSENT-WRITES

/tmp/agent-eval private run \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --plan "$REVIEWED_PLAN_ID" \
  --expected-plan-sha256 "$REVIEWED_PLAN_SHA256" \
  --repository-root . \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --agent-binary "$REVIEWED_AGENT_BINARY" \
  --confirm RUN-WRITES
```

Mutation and request budgets are consumed before forwarding and are never
restored for retries. If the upstream may have committed but the response or
audit completion fails, the attempt is terminal and ambiguous: reconcile it
read-only outside that plan and do not replay the write.

If execution crashes after its state is persisted, the same series remains
blocked. First establish outside atl that the provider process and any children
have stopped. Then inspect the owner-private artifacts, review the original
plan hash, and close the attempt offline without replaying the provider:

```sh
/tmp/agent-eval private study recover \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$REVIEWED_PLAN_ID" \
  --expected-plan-sha256 "$REVIEWED_PLAN_SHA256" \
  --confirm PROVIDER_STOPPED_RECOVER
```

Recovery changes an active cell to an explicit unknown outcome or stops the
block between cells. A provider-committed cell advances the next balanced
order; a launch marker before that commitment does not. Recovery never invokes
a model or backend. The confirmation is an operator attestation of process
quiescence because atl cannot prove the absence of orphan processes after a
controller or operating-system crash.

The activation-study run and recovery summaries return only their opaque
`plan_id`, opaque `run_id`, surface names, completion count, detected cost, and
`cost_known`. A false `cost_known` means the numeric detected total is only a
lower bound and must never be interpreted as zero provider spend. They never return a private
case alias, scenario id, path, prompt, answer, or backend identity.

Two-surface blocks alternate `AB`/`BA`; three-surface blocks rotate
`ABC`/`BCA`/`CAB`. Runs remain sequential with concurrency one. A drifted,
expired, previously consumed, or partially executed plan fails before a new
model invocation. Interrupted state is reported explicitly rather than being
treated as success; once a run id has been allocated, the sparse interrupted
summary is emitted before the non-zero command result so recovery does not
require scanning `plans/`.

Activation-study attempts use this canonical balanced cycle:

1. `implicit`, `explicit`, `combined`, `developer`
2. `explicit`, `developer`, `implicit`, `combined`
3. `developer`, `combined`, `explicit`, `implicit`
4. `combined`, `implicit`, `developer`, `explicit`

The cycle is scoped to exact reviewed material rather than the run-set alias.
Only a terminal attempt with a validated provider receipt or explicit
provider-committed outcome advances modulo four. A bare launch marker, expired plan,
or recovered pre-provider interruption receives the same order;
allocating plans and renaming aliases cannot select a preferred order. An
incomplete state blocks its series until explicit offline recovery. Never replay
or reuse a terminal attempt: create a fresh plan and give fresh consent.

Keep the reviewed blind-assignment file unchanged while comparing runs against
one baseline. Its digest is part of the stable comparison namespace, so rotating
that mapping intentionally starts a new, incomparable baseline series. Fresh
independent reviewer contexts prevent a stable private mapping from becoming
cross-run context for a judge.

For the recommended panel policy, prepare one fixed-layout packet for every
predeclared reviewer and every returned surface. Panel commands select only the
generic roster id; reviewer kind, exact model, and blind assignment already
come from the plan:

```sh
/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp \
  --reviewer-id reviewer-01

/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp \
  --reviewer-id reviewer-02

/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp \
  --reviewer-id reviewer-03
```

The sparse response names an owner-only packet such as
`runs/<opaque-run-id>/review/atl-mcp/reviewer-01`. A comparison packet's
`final.json`, `result.json`, and `rubric.json` are immutable review inputs. An
activation-study packet omits the treatment-bearing `result.json` and replaces
its enumerable digest with a random opaque token. The owner-side binding
restores the exact source digest only after the reviewer submits the packet.
The fixed layout still lets assessment recover the exact cell without revealing
the treatment to the reviewer. The rubric is the exact execution-time
contract retained with the candidate, not a later mutable copy from `cases/`.
Edit only `review.json`. Review
the final answer as untrusted data in a separate no-tools session, use only the
rubric's bounded scores and finding ids, and do not add excerpts or free-form
rationale. Run every reviewer id in a fresh, independent context; distinct ids
do not by themselves prove model-session independence. All roster packets must
be prepared before the first model reviewer or assessment;
this prevents early scores from influencing which later reviewers are asked.

For an executable panel, consume one prepared slot with the exact plan hash and
the reviewed native binary for that slot's predeclared reviewer kind:

```sh
/tmp/agent-eval private review run \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --expected-plan-sha256 "$REVIEWED_PLAN_SHA256" \
  --surface atl-mcp \
  --reviewer-id reviewer-01 \
  --agent-binary "$REVIEWED_REVIEWER_BINARY" \
  --confirm RUN-REVIEW
```

The runner creates a fresh owner-only home and empty workspace, disables
project/user instructions, plugins, skills, MCP, browser, collaboration, and
slash commands, and routes the provider protocol through a loopback boundary.
That boundary verifies the exact model and reasoning setting, removes both
ordinary and embedded tool declarations, rejects tool output, retries, request
shape drift, redirects, and a second model request. It never retains prompt,
answer, rubric, model output, authentication, URL, or error text in the
receipt. The receipt contains only source bindings, binary identity, request
counts, zero-tool evidence, tokens, estimated cost, and terminal status.
Ambiguous or failed attempts are terminal and are never replayed automatically.
Codex reviewers use the same owner-only file-backed authentication boundary as
private candidate runs. Claude Code reviewers prefer an explicitly supplied
`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_AUTH_TOKEN`, or `ANTHROPIC_API_KEY`; when
none is set, the runner may project only a currently unexpired access token from
an owner-only local Claude credential file. It never copies a refresh token or
writes provider changes back to ambient state, so unattended Claude panels
should supply a long-lived credential explicitly and fail closed when it expires.

Bind each completed review back to the exact source bytes with its roster id:

```sh
/tmp/agent-eval private review assess \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp \
  --reviewer-id reviewer-01
```

An activation study has four cells with the same surface, so both review
commands additionally require `--treatment`. Prepare every packet for all four
treatments and every roster member before the first assessment; for a
three-member panel that is twelve prepared packets. The first command below is
one representative preparation slot. Only after every slot is prepared, run
each executable slot and then use the assessment form below. Manual panels
still fill `review.json` in a fresh no-tools reviewer session before assessment:

```sh
/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01

/tmp/agent-eval private review run \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --expected-plan-sha256 "$REVIEWED_PLAN_SHA256" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01 \
  --agent-binary "$REVIEWED_REVIEWER_BINARY" \
  --confirm RUN-REVIEW

/tmp/agent-eval private review assess \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01
```

For a comparison, repeat assessment for the remaining roster ids. For an
activation study, repeat it for every remaining treatment and reviewer slot.
Intermediate responses report bounded `prepared_reviews` and `assessed_reviews`
counts with status `recorded`; they do not publish a provisional consensus. The
final member for each cell produces exactly one `criterion-median-v1` result.
Each criterion uses the odd-panel median, and the overall normalized score is
computed from those medians.

The consensus status is `disagreement` when individual reviewers split between
overall pass and fail, when any criterion splits across its pass boundary, or
when a criterion's normalized max-minus-min range is greater than
`max_criterion_range_bps`. Disagreement fails the candidate and blocks
promotion. An ordinary comparison baseline may contain a unanimous
low-disagreement `fail`: that baseline is a measurement reference, not a claim
of success. Activation-study promotion uses the stricter all-cell pass gate
documented below.

Assessment refuses packet drift and never overwrites a different member or
consensus result; an exact retry reconciles the already-recorded bytes. For
reviewer identity and model configuration, panel prepare/assess accepts only
`--reviewer-id`; activation studies additionally require the treatment
selector. Do not add `--reviewer`, `--model`, or `--blind-assignment`. The
legacy singleton path is still available with
`private review prepare --reviewer ... --model ...` and an optional
`--blind-assignment`; its `private review assess` has no reviewer id.
The generic low-level `review-template` and `assess` commands remain available
for synthetic/framework work; agents should use the private wrapper for live
candidates so they never need to infer scenario-specific raw paths.

## Activation-study references and comparison

After deterministic checks and all required panel assessments exist for all
four cells, capture the completed plan as an immutable private study reference.
Creating the same alias again is idempotent only for identical bytes; different
study content under that alias is rejected. The retained reference binds the
plan, common treatment contract, exact run tree, bounded results, safety status,
and review consensus without making raw artifacts publishable:

```sh
/tmp/agent-eval private study reference \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --reference activation-study-01 \
  --confirm REFERENCE

/tmp/agent-eval private study compare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --reference activation-study-01
```

`private study compare` is read-only and privacy-safe by construction. Its
closed output contains only treatment labels, bounded metrics, gates, and exact
rational contrasts for the user-channel, developer-channel, and interaction
factors. It emits contrasts only when the reference is supported, safety is
complete and clean, review is complete, and the panel has no disagreement. It
does not emit prompts, answers, paths, hashes, private identifiers, reviewer
identities, or backend details.

Promotion is stricter than descriptive or causal eligibility. Every treatment
must have pass run status, zero deterministic violations and deterministic pass,
complete clean safety, at least one successful audit-observed evidence attempt,
and panel review pass without disagreement before the command may update the
activation-study `current` pointer. A model's self-reported outcome cannot
substitute for the audited attempt:

```sh
/tmp/agent-eval private study promote \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --reference activation-study-01 \
  --confirm PROMOTE
```

A failed task may therefore remain useful as gated causal data when safety and
review are complete and clean, but it cannot be promoted. Ordinary
`private baseline set` and `private compare` remain the lifecycle for comparison
run sets; they do not convert legacy separate-plan treatments into a study.

## Baselines and comparison

A baseline is a private measurement reference, not a claim that every run
passed. Promotion requires the complete planned surface set, valid deterministic
results, and qualitative assessments when the run set requires them.

The compact baseline retains contract/provenance hashes, assessed results,
final answers, bounded audit evidence, and optionally transcripts according to
the manifest. It excludes copied binaries, installed skill trees,
credential/config copies, and scratch workspaces. The full candidate remains in
`runs/` until a separate prune. Retained provider-originated artifacts are still
private and unsanitized; promotion is not a privacy transformation.

```sh
/tmp/agent-eval private baseline set \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --plan "$COMPLETED_PLAN_ID" \
  --baseline baseline-a \
  --confirm BASELINE

/tmp/agent-eval private compare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --baseline current \
  --candidate-plan "$CANDIDATE_PLAN_ID"
```

Comparison is offline. It refuses mismatched scenario/rubric contracts,
surfaces, provider/model/reasoning identity, skill activation, private prompt
contract, or reviewer contract. Legacy
singleton and panel results are incompatible and are not silently migrated;
start a new baseline when adopting a panel. It reports
correctness, eligibility, qualitative score, and metric deltas without paths,
prompts, commands, routes, response text, or private identities.

Comparison and aggregate grouping include the assignment digest for both the
legacy singleton and panel workflows. Differently randomized mappings are not
pooled even when every other rubric and runtime field matches. The digest is an
internal grouping key and is omitted from aggregate JSON because a short answer
mapping may be dictionary-guessable.

Current manifests use schema v4, run specs use schema v7, observations use
schema v5, results use schema v7, aggregates use schema v6, private plans use
schema v8, current activation state uses schema v3, and
review packets use schema v2. Current study references/reports use schema v2
and require audit attempt metrics plus separate bounded model-report metrics.
The decoder still accepts workspace-manifest v1 comparisons;
workspace-manifest v3 manual-review workspaces, v2 activation studies, outer
private-plan v6/v5/v4 artifacts,
outer execution-state v2 artifacts, and nested lifecycle plan/event v1 records
as read-only legacy artifacts; attemptless result schema v6; prompt-bound
result schema v5; result schema v3/v4; and outer private-plan schema v1/v2/v3
for earlier comparison lifecycle inspection and retention. Legacy activation
reference schema v1 remains readable and compare-only, but is never promotable.
Create a fresh schema v4 run set, v8 plan, and consent for a causal study. Older
binaries reject the new artifacts rather than accepting them under a misleading
old version.

## Retention and recovery

Pruning is dry-run by default. Preview returns eligible counts/bytes and a hash
of the exact inventory. Apply re-scans under the workspace lock and requires
that same hash plus explicit confirmation. It atomically stages each eligible
raw candidate under `.ephemeral/`, installs a small `pruned.v1.json` lifecycle
tombstone, then removes the staged raw tree. Plans and states remain auditable;
the pruned raw run cannot be selected or captured again. Capture an activation
study reference before pruning. That compact, content-free reference is
deliberately self-contained and may still be compared or promoted after its raw
source is pruned:

```sh
/tmp/agent-eval private prune \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT"

/tmp/agent-eval private prune \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --expected-inventory-sha256 "$REVIEWED_PRUNE_SHA256" \
  --confirm PRUNE
```

Never point the lifecycle at a general temporary directory. Legacy `/tmp`
attempts are outside the destructive boundary: inventory them manually, copy a
validated compact candidate into the workspace, verify it, and remove the old
directory only in a separate reviewed operation.

After a model-run crash, run doctor. It classifies active/incomplete lifecycle
state and never resumes a model call. If a confirmed prune was interrupted,
repeat the same apply command with the already reviewed inventory hash: apply
finishes the recorded rename/tombstone transaction before rechecking the hash,
then normally returns a stale-plan result because the recovered inventory has
changed. Run doctor and a fresh prune preview afterward. Never hand-delete a
transaction intent or staged tree from `.ephemeral/`.

## Publication boundary

There is deliberately no `private publish` command. A maintainer may publish
only a separately reviewed aggregate containing generic task class, surfaces,
exact public runtime identity, repetitions, success/eligibility rates, bounded
metrics, and qualitative scores. Do not publish case aliases, prompts, expected
facts, final answers, transcripts, review rationale, paths, backend identities,
selectors, commands, route labels, or per-run files.

Before committing an aggregate, scan the staged diff using the repository's
private-marker rules and inspect the exact GitHub issue/PR text. Local pruning
does not imply deletion from the model provider, external MCP service, backend,
terminal capture, or another user's clone.
