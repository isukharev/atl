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
  private-workspace.v2.json
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
3. Create and review a dry plan. Never infer consent from an old transcript or
   from the mere presence of credentials.
4. Execute only the exact plan hash, sequentially, with the explicit run
   confirmation. Do not construct ad-hoc shell loops around low-level `run`.
5. Apply deterministic checks before qualitative review. For a review panel,
   prepare every predeclared reviewer's fixed-layout packet before assessing
   any of them; do not discover raw result paths with an ad-hoc filesystem
   scan. A judge cannot rescue an incorrect, unsafe, incomplete, or over-budget
   run.
6. Promote only a complete reviewed surface set whose panel has reached one
   consensus. A disagreement is not promotable. Compare offline against a
   baseline bound to the same singleton or panel contract.
7. Preview pruning, review the content-bound prune hash, and then confirm the
   same plan. Baselines, cases, active runs, and paths outside the marked root
   are never eligible.
8. Keep raw prompts, responses, transcripts, commands, routes, identifiers, and
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
| Plan | `private plan` | None | Writes one immutable plan | Data-boundary flags and `CONSENT` |
| Execute | `private run` | Reviewed model and read-only backend routes | Writes one candidate run | Exact plan SHA-256 and `RUN` |
| Recover study | `private study recover` | None | Closes an interrupted study without replaying the provider after the operator establishes provider-process quiescence | Exact plan SHA-256 and `PROVIDER_STOPPED_RECOVER` |
| Prepare review | `private review prepare` | None | Copies one result, answer, rubric, and hash-bound template into an owner-only packet | Completed plan and explicit surface; every panel member before assessment |
| Assess | `private review assess` | No tools in the reviewer session | Records one member; the last member writes one median consensus | Completed bounded review; bound blind assignment where required |
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

Edit the generated private manifest locally. Schema v2 gives each run set a
`kind`: `comparison` accepts one to three unique surfaces, while
`activation-study` requires exactly four otherwise-identical Codex
`private-live` `cli-skill` v5 specs carrying `implicit`, `explicit`, `developer`,
and `combined` once each. An omitted kind is the legacy comparison form. Runtime
bindings are environment-variable names, never literal paths or credentials.
Keep provider-specific profiles outside the repository as required by the
existing private-live transport. The public
[JSON Schema](../benchmarks/agent-eval/private-workspace.schema.json) supports
editor validation, and the
[generic example](../benchmarks/agent-eval/private-workspace.example.json)
shows a comparison without backend-specific values. The Go decoder remains the
authoritative strict validator.

```json
{
  "schema_version": 2,
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
    "reviewer_reserve_microusd": 1000000
  }]
}
```

An activation study requires the panel shown above and cannot use the legacy
singleton policy. Its four treatment caps plus a positive `reviewer_reserve_microusd` must
fit under the workspace execution maximum. A comparison may still use
`qualitative_review_required: true` for one legacy review, or declare the same
panel form. A run set cannot combine the singleton setting with
`qualitative_review_panel`. A panel declares exactly three or five reviewers,
their generic ids, kinds, exact model identities, the fixed
`criterion-median-v1` method, and a `max_criterion_range_bps` threshold from 1
through 9999. Human reviewers may omit `model`; model reviewers may not. A
comparison with `qualitative_review_required: false` and no panel keeps review
disabled.

Reviewer ids are terminal-visible filesystem slot names. Keep them generic
(`reviewer-01`, not a person, team, provider account, or backend identity); they
are restricted to one lowercase path component. Schema v1 manifests remain
readable as legacy comparisons but cannot declare a kind or reviewer reserve;
create and review a schema v2 manifest for an activation study.

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

## Review, run, and assess

A v4 plan binds the exact comparison or activation-study contract and execution
identity: case inputs, ordered surfaces, skill activation and private
prompt-contract digest, ATL and wrapper binaries, plugin/skill tree, agent
runtime, repository commit, backend-config identity, external profile when
used, cost cap, and consent expiry. Credential bytes are never hashed into a
plan or retained in a run/baseline.

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
execution. The plan also partitions the sum of the four treatment caps and the
positive explicit reviewer reserve under the workspace maximum. The runner
does not launch panel reviewers, so this reserve records reviewed authorization
rather than durable reviewer-spend receipts; supervise model-reviewer cost
separately. Treatment cost assurance is detection-only, not a preventive or
provider-side hard cap: after each provider call, the runner records the event
chain and checks reported cost and coverage. Detected exhaustion, unknown cost,
provider uncertainty, or failure to persist or contain the state stops the
remaining cells; a safety violation does too. None of those checks can undo
cost already incurred.

Schema v1 comparison manifests and legacy plans remain readable. Four treatments
collected under separate legacy plans are descriptive compatibility observations
only: they do not acquire the study's ordering, shared state, or causal gates and
must not be used to estimate channel effects or interaction.

Run-spec schema v5 carries the four treatments. Existing v4 `implicit` and
`explicit` specs retain their meanings when deliberately migrated. Legacy v3
specs that named a skill through provider instructions are not silently mapped
to `developer`; create and review a new activation-bound v5 spec and baseline.
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

`--agent-binary` must identify a reviewed single-file native executable for the
host OS and architecture. A symlink is accepted when its canonical target is
such an executable; scripts, JavaScript/package launchers, malformed binaries,
and binaries for another platform are rejected. Do not assume that the first
result from `command -v` satisfies this contract. Resolve and review the
provider's native executable once, keep its absolute path in a session-local
variable, and pass the same value to plan and run. The lifecycle structurally
parses ELF, Mach-O/classic fat Mach-O with `LC_MAIN`, or PE as appropriate and
hashes the canonical target path and bytes without retaining that path. It does
not execute
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
be prepared before the first assessment;
this prevents early scores from influencing which later reviewers are asked.

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
one representative preparation slot. Only after every slot is prepared, use the
second form to assess each packet in a fresh no-tools reviewer session:

```sh
/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01

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
complete clean safety, and panel review pass without disagreement before the
command may update the activation-study `current` pointer:

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

Current manifests use schema v2, results use schema v6, aggregates use schema
v6, private plans use schema v4, and review packets use schema v2. The decoder
still accepts schema v1 manifests as comparisons, prompt-bound result schema v5,
result schema v3/v4, and private plan schema v1/v2/v3 for lifecycle inspection
and retention. Legacy plans are not silently re-executed or reclassified as
activation studies; create a fresh schema v2 run set, v4 plan, and consent for a
causal study. Older binaries reject the new artifacts rather than accepting them
under a misleading old version.

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
