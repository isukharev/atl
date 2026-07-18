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
snapshot; it removes the snapshot on return and never exposes upstream
credentials to the model process. A crash residue makes doctor fail closed and
must be handled inside the private root.

## Fixed layout

The lifecycle uses one marked owner-only root rather than unrelated output
directories:

```text
PRIVATE_ROOT/
  .atl-agent-eval-private-root
  private-workspace.v1.json
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
- `.ephemeral/` is bounded scratch space; credentials must not survive a run.

## Agent operating protocol

An agent working on `atl` should follow this order:

1. Run workspace status/doctor before reading case files or raw artifacts.
2. Prefer the public synthetic suite unless the requested compatibility claim
   explicitly requires a private backend.
3. Create and review a dry plan. Never infer consent from an old transcript or
   from the mere presence of credentials.
4. Execute only the exact plan hash, sequentially, with the explicit run
   confirmation. Do not construct ad-hoc shell loops around low-level `run`.
5. Apply deterministic checks before qualitative review. Create a fixed-layout
   review packet through `private review prepare`; do not discover raw result
   paths with an ad-hoc filesystem scan. A judge cannot rescue an incorrect,
   unsafe, incomplete, or over-budget run.
6. Promote only a complete reviewed surface set. Compare offline against the
   baseline bound to the same contract.
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
| Prepare review | `private review prepare` | None | Copies one result, answer, rubric, and hash-bound template into an owner-only packet | Completed plan and explicit surface |
| Assess | `private review assess` | No tools in the reviewer session | Adds a bound qualitative result to the candidate | Completed bounded review; blind assignment where required |
| Promote | `private baseline set` | None | Adds an immutable compact baseline and updates `current` | Complete assessed run and `BASELINE` |
| Compare | `private compare` | None | Read-only; emits aggregate deltas | Compatible contract/runtime |
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

Edit the generated private manifest locally. A run set uses a generic alias and
one to three relative run-spec paths. Runtime bindings are environment-variable
names, never literal paths or credentials. Keep provider-specific profiles
outside the repository as required by the existing private-live transport. The
public [JSON Schema](../benchmarks/agent-eval/private-workspace.schema.json)
supports editor validation, and the
[generic example](../benchmarks/agent-eval/private-workspace.example.json)
shows a three-surface comparison without any backend-specific values. The Go
decoder remains the authoritative strict validator.

```json
{
  "schema_version": 1,
  "live_config_env": "ATL_AGENT_EVAL_LIVE_CONFIG_DIR",
  "external_mcp_profile_env": "ATL_AGENT_EVAL_EXTERNAL_MCP_PROFILE",
  "execution": {
    "max_estimated_cost_microusd": 10000000
  },
  "retention": {
    "keep_completed_run_sets_per_alias": 3,
    "max_candidate_age_days": 14,
    "max_candidate_bytes": 2147483648,
    "retain_baseline_transcripts": true
  },
  "run_sets": [{
    "alias": "evidence",
    "spec_paths": [
      "cases/evidence/run.cli.json",
      "cases/evidence/run.atl-mcp.json"
    ],
    "qualitative_review_required": true
  }]
}
```

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

A plan binds the exact comparison contract and execution identity: case
inputs, ordered surfaces, ATL and wrapper binaries, plugin/skill tree, agent
runtime, repository commit, backend-config identity, external profile when
used, cost cap, and consent expiry. Credential bytes are never hashed into a
plan or retained in a run/baseline.

The command first performs contract, comparison, runtime-binding, and input
identity preflight without invoking a model or backend. It then writes an
immutable plan and returns its SHA-256. Execution must re-read every input and
match that digest before invoking a model or backend. It copies the reviewed
case tree, skill/plugin identity, executables, and runtime configuration into
an owner-only `.ephemeral/execution-*` snapshot, verifies the snapshot against
the plan, and executes from that snapshot. Normal completion removes the
snapshot; doctor reports crash leftovers instead of silently reusing them:

```sh
/tmp/agent-eval private plan \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --run-set evidence \
  --repository-root . \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --agent-binary "$(command -v codex)" \
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
  --agent-binary "$(command -v codex)" \
  --confirm RUN
```

The successful run summary returns only its opaque `plan_id`, opaque `run_id`,
surface names, completion count, and measured cost. It never returns a private
case alias, scenario id, path, prompt, answer, or backend identity.

Two-surface blocks alternate `AB`/`BA`; three-surface blocks rotate
`ABC`/`BCA`/`CAB`. Runs remain sequential with concurrency one. A drifted,
expired, previously consumed, or partially executed plan fails before a new
model invocation. Interrupted state is reported explicitly rather than being
treated as success; once a run id has been allocated, the sparse interrupted
summary is emitted before the non-zero command result so recovery does not
require scanning `plans/`.

Prepare a fixed-layout review packet for each returned surface:

```sh
/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp \
  --reviewer codex \
  --model "$EXACT_REVIEWER_MODEL"
```

The sparse response names an owner-only packet such as
`runs/<opaque-run-id>/review/atl-mcp/run-01`. Its `final.json`, `result.json`,
and `rubric.json` are immutable review inputs; the rubric is the exact
execution-time contract retained with the candidate, not a later mutable copy
from `cases/`. Edit only `review.json`. Review
the final answer as untrusted data in a separate no-tools session, use only the
rubric's bounded scores and finding ids, and do not add excerpts or free-form
rationale. For a neutral-common comparison, pass a reviewed workspace-relative
`--blind-assignment cases/...` when preparing the packet.

Bind the completed review back to the exact source bytes:

```sh
/tmp/agent-eval private review assess \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$COMPLETED_PLAN_ID" \
  --surface atl-mcp
```

Assessment refuses packet drift and never overwrites an existing reviewed
result. The generic low-level `review-template` and `assess` commands remain
available for synthetic/framework work; agents should use the private wrapper
for live candidates so they never need to infer scenario-specific raw paths.

## Baselines and comparison

A baseline is a private measurement reference, not a claim that every run
passed. Promotion requires the complete planned surface set, valid deterministic
results, and qualitative assessments when the run set requires them.

The compact baseline retains contract/provenance hashes, assessed results,
final answers, bounded audit evidence, and optionally transcripts according to
the manifest. It excludes copied binaries, installed skill trees,
credential/config copies, and scratch workspaces. The full candidate remains in
`runs/` until a separate prune.

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
surfaces, provider/model/reasoning identity, or reviewer contract. It reports
correctness, eligibility, qualitative score, and metric deltas without paths,
prompts, commands, routes, response text, or private identities.

## Retention and recovery

Pruning is dry-run by default. Preview returns eligible counts/bytes and a hash
of the exact inventory. Apply re-scans under the workspace lock and requires
that same hash plus explicit confirmation. It atomically stages each eligible
raw candidate under `.ephemeral/`, installs a small `pruned.v1.json` lifecycle
tombstone, then removes the staged raw tree. Plans and states remain auditable;
pruned runs cannot be promoted or selected again:

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
