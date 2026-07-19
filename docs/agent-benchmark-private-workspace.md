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
| Prepare review | `private review prepare` | None | Copies one result, answer, rubric, and hash-bound template into an owner-only packet | Completed plan and explicit surface; every panel member before assessment |
| Assess | `private review assess` | No tools in the reviewer session | Records one member; the last member writes one median consensus | Completed bounded review; bound blind assignment where required |
| Promote | `private baseline set` | None | Adds an immutable compact baseline and updates `current` | Complete assessed run without panel disagreement and `BASELINE` |
| Compare | `private compare` | None | Read-only; emits aggregate deltas | Compatible contract/runtime and review mode |
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
    "qualitative_review_required": false,
    "qualitative_review_panel": {
      "method": "criterion-median-v1",
      "reviewers": [
        {"id": "reviewer-01", "kind": "codex", "model": "gpt-test-reviewer"},
        {"id": "reviewer-02", "kind": "codex", "model": "gpt-test-reviewer"},
        {"id": "reviewer-03", "kind": "codex", "model": "gpt-test-reviewer"}
      ],
      "max_criterion_range_bps": 2500,
      "blind_assignment": "cases/evidence/blind-assignment.txt"
    }
  }]
}
```

`qualitative_review_required: true` remains the explicit legacy singleton
policy. It selects one review whose reviewer kind, exact model, and optional
blind assignment are supplied at review time. A run set cannot combine that
setting with `qualitative_review_panel`. A panel declares exactly three or five
reviewers, their generic ids, kinds, exact model identities, the fixed
`criterion-median-v1` method, and a `max_criterion_range_bps` threshold from 1
through 9999. Human reviewers may omit `model`; model reviewers may not.
`qualitative_review_required: false` with no panel keeps qualitative review
disabled.

Reviewer ids are terminal-visible filesystem slot names. Keep them generic
(`reviewer-01`, not a person, team, provider account, or backend identity); they
are restricted to one lowercase path component. The manifest remains schema
v1 because the panel is an additive optional policy, but binaries predating the
panel reject manifests that use the new field rather than silently ignoring it.

The optional panel `blind_assignment` is a workspace-relative file below
`cases/`; it is required for a `neutral-common` run set. The complete roster,
policy, and assignment digest are bound into the immutable plan before any
model or backend execution. They are copied into the retained run contract, so
changing a reviewer, exact model, threshold, or assignment invalidates the
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

A plan binds the exact comparison contract and execution identity: case
inputs, ordered surfaces, ATL and wrapper binaries, plugin/skill tree, agent
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

The private CLI instruction tells the model to select the installed
task-matching skill, then requires evidence retrieval through the literal
`atl` executable. This is the intended `cli-skill` surface: when selected, the
installed skill can supply command-shape guidance, while the reviewed broker
still decides whether that exact invocation is allowed. The instruction never
reveals a case-specific command.
When the reviewed `data_capabilities` are Jira-only or Confluence-only, the
instruction names `$atl:jira` or `$atl:confluence` respectively. Mixed
capability sets name both in a stable order; unknown families stay on generic
task matching.
This route discloses only the already-reviewed service family, not a selector,
field, expected answer, backend, or command allowlist.
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
  --run-set evidence \
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
`runs/<opaque-run-id>/review/atl-mcp/reviewer-01`. Its `final.json`, `result.json`,
and `rubric.json` are immutable review inputs; the rubric is the exact
execution-time contract retained with the candidate, not a later mutable copy
from `cases/`. Edit only `review.json`. Review
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

Repeat assessment for the remaining roster ids. Intermediate responses report
bounded `prepared_reviews` and `assessed_reviews` counts with status `recorded`;
they do not publish a provisional consensus. The final member produces exactly
one `criterion-median-v1` result. Each criterion uses the odd-panel median, and
the overall normalized score is computed from those medians.

The consensus status is `disagreement` when individual reviewers split between
overall pass and fail, when any criterion splits across its pass boundary, or
when a criterion's normalized max-minus-min range is greater than
`max_criterion_range_bps`. Disagreement fails the candidate and blocks baseline
promotion. A unanimous low-disagreement result may be promoted even when its
consensus is `fail`: a baseline is a measurement reference, not a claim of
success.

Assessment refuses packet drift and never overwrites a different member or
consensus result; an exact retry reconciles the already-recorded bytes. Panel
prepare/assess accepts only `--reviewer-id`; do not add
`--reviewer`, `--model`, or `--blind-assignment`. The legacy singleton path is
still available with `private review prepare --reviewer ... --model ...` and an
optional `--blind-assignment`; its `private review assess` has no reviewer id.
The generic low-level `review-template` and `assess` commands remain available
for synthetic/framework work; agents should use the private wrapper for live
candidates so they never need to infer scenario-specific raw paths.

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
surfaces, provider/model/reasoning identity, or reviewer contract. Legacy
singleton and panel results are incompatible and are not silently migrated;
start a new baseline when adopting a panel. It reports
correctness, eligibility, qualitative score, and metric deltas without paths,
prompts, commands, routes, response text, or private identities.

Comparison and aggregate grouping include the assignment digest for both the
legacy singleton and panel workflows. Differently randomized mappings are not
pooled even when every other rubric and runtime field matches. The digest is an
internal grouping key and is omitted from aggregate JSON because a short answer
mapping may be dictionary-guessable.

Panel results use result schema v4 and review packets use review schema v2.
The decoder still accepts result schema v3 without a panel and review schema v1
without a reviewer id, so existing singleton baselines remain readable. Older
binaries do not accept the new panel artifacts under a misleading old version.

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
