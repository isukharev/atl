# Agent benchmarking

How this project measures the *agent ergonomics* of `atl`: what it costs a
coding agent, in turns and dollars, to complete realistic Confluence/Jira
editing tasks through a given CLI surface or skill text. The numbers published
in issues (e.g. the md-vs-CSF study and the `conf apply` table-merge work) are
produced with this method.

## Why

`atl`'s primary user is an agent. Changes that look neutral in unit tests —
a reworded skill paragraph, a different error message, a new fail-closed
refusal — can add or remove *whole turns* from real agent sessions, and turns
are the dominant cost. Static reasoning about "what an agent would do" has
been wrong repeatedly; only measured runs settle it. Three findings that came
out of measurement, not review: exact-string edits against invisible bytes
were the single biggest cost driver on real pages; unconditionally
recommending one editing tool made agents *slower* on simple pages
(tool-ceremony overhead); a fail-closed gate that looked harmless refused
essentially every real-world table and silently doubled the cost of table
tasks.

## Method

- **Real headless runs.** Each data point is a fresh, non-interactive Claude
  Code or Codex session given the same task prompt and fixture. Record the exact
  model, agent CLI version, `atl` version, plugin version, and skill digest;
  moving aliases are not reproducible model identifiers. No replay is used:
  the agent really reads files and runs `atl`.
- **Deterministic oracles first.** Every task has a programmatic pass/fail check
  on the produced artifact (the resulting CSF bytes for edit tasks, the JSON
  answer for read tasks). A separate rubric may score clarity and usefulness,
  but it can only preserve or lower the strict result, never rescue a failed
  fact, safety, completeness, or budget check.
- **Paired variants, one variable.** Variants differ in exactly one thing —
  the guidance text (skill/tips) or the tool surface available — everything
  else (model, fixtures, prompts, oracle) held fixed. A variant's result is
  meaningless except against its pair.
- **Medians over repetitions.** ≥3 repetitions per cell; single runs swing
  ±50% on cost. Report medians for turns/cost/duration, sums for totals, and
  success as n/N.
- **Task classes.** Fixtures model realistic pages spanning the shapes that stress
  different code paths: text-heavy, macro-heavy, and table-heavy bodies, with
  both edit and read tasks. Per-class breakdowns matter more than the overall
  median — regressions hide in classes.
- **Blocked ordering.** Run three-surface comparisons sequentially in
  Latin-square order (`ABC`, `BCA`, `CAB`) so backend drift and warm caches do
  not always favor one surface. Synthetic cells use at least three fresh
  sessions per surface; private-live keeps one supervised run per surface in a
  block and accumulates blocks over time.
- **Correctness before efficiency.** Safety and deterministic correctness are
  hard gates. Compare calls, tokens, cost, and duration among correct results.
- **A ceiling, not a competitor.** Where relevant, a "ceiling" variant
  measures the same tasks with all real constraints removed (e.g. editing
  plain markdown with no CSF produced at all). It bounds what optimization
  can achieve; the residual gap to it is the price of the guarantees
  (validation, loss gates, version safety) and is not expected to close.

## When to run

Before and after any change to an **agent-facing contract**:

- skill texts shipped in `skills/` (recommendations, error guidance, flow),
- CLI output, exit codes, or refusal behavior of the editing surface
  (`conf edit`, `conf apply`, `conf validate`, converter/merge subset),
- anything that changes how many steps a common task takes.

A targeted re-measure of only the affected task class, spliced into the
previous results, is fine — and much cheaper than a full sweep. State
explicitly which cells were re-run.

## User-task scenario matrix

Command coverage is not task coverage. Group scenarios by what the user asks
an agent to accomplish, while keeping the broader capability `task_class` for
routing. Compare results per scenario first and summarize across a row only
when the oracle, rubric, runtime, and changed variable are compatible.

| User-task shape | Representative risk | Deterministic coverage | Model-in-loop coverage |
|---|---|---|---|
| Single-object evidence | Broad reads, missed custom fields, unqualified absence | Jira epic evidence and exact clipped-field expansion | Jira epic evidence and clipped-field typed MCP |
| Bounded page evidence | Full-page context inflation, lost table meaning | Confluence section recovery and approved-occurrence route | Confluence page evidence via CLI and typed MCP |
| Ambiguity recovery | Duplicate headings or identities silently select the wrong source | Confluence duplicate-heading refusal/recovery | Confluence page evidence |
| Portfolio snapshot | Repeated JQL/joins replace a curated membership source | Jira board/Structure route | Jira quarterly portfolio |
| Multi-source synthesis | Conflicts, stale evidence, or summaries lose provenance | Fifteen-GET mixed portfolio and six-GET Confluence brief | Jira quarterly portfolio and Confluence decision brief |
| Hostile embedded content | Page/issue prose attempts to redirect tool use | Guard and zero-write route checks | Jira injection and both Confluence families |
| Context isolation | Delegation duplicates reads or loses evidence in summarization | Delegation/request budgets | Single-agent versus one-child portfolio and Confluence brief |
| Durable mirror review | Native/derived drift and context-heavy byte inspection | Exact four-page offline diff with zero-network proof | Confluence mirror review via CLI + skill |
| Guarded edit planning | A preview weakens the read-only boundary, reviewed inputs drift, or an ambiguous write is replayed | Exact-body synthetic write-path and access-policy tests | Jira custom-field preview / reviewed apply / ambiguous-no-replay |

The default suite therefore contains small navigation, medium single-object,
and longer synthesis cells. Add a new cell when a product change introduces a
materially different reasoning shape; do not add one merely to exercise another
flag. Model-run prompts must request a user outcome rather than prescribe every
command, except when the experiment intentionally holds the route fixed.

## Benchmark categories and surfaces

- `neutral-common` is outcome-driven. Core prompt bytes, schema, rubric,
  scenario, budgets, and semantic response checks are identical across
  surfaces; the prompt does not prescribe commands or tool names. Its scenario
  must name at least one `required_semantic_checks` entry, require the generic
  `interface_invocations` metric, and set a positive generic interface budget.
  Transport-specific `atl_*` check aliases and `atl_invocations` are rejected.
  Loading a run spec also rejects qualified MCP names, exact `atl jira`/`atl
  conf` routes, and typed Jira/Confluence tool spellings in the core prompt;
  ordinary prose about Jira or Confluence remains valid.
- `surface-native` intentionally exercises a capability that is not available
  everywhere, such as an offline durable mirror. Unsupported coverage remains
  a result rather than being hidden by changing the task.
- `route-fixed` is a contract microbenchmark with a reviewed execution route.
  It must not be interpreted as an overall ranking of surfaces.

The compatibility default for old scenarios and run specs is `route-fixed`.
Executable surfaces are `cli-skill`, `atl-mcp`, and the private-live-only
`external-mcp` surface described below. Old stored results without a surface
aggregate as `legacy-unspecified` rather than mixing with new runs.

## Evaluation layers

The evaluation stack has distinct safety and cost properties:

1. **Static CI gates** validate skill examples, access classification, scenario
   schemas, output contracts, and context budgets without a model or backend.
2. **Deterministic workflows** execute real `atl` commands against synthetic
   Jira/Confluence servers and enforce method, request, byte, completeness, and
   mutation budgets.
3. **Synthetic model runs** let Claude Code or Codex choose commands against a
   deterministic local backend. Deterministic oracles score the final result
   and trajectory; an optional maintainer/model rubric scores answer quality
   separately.
4. **Supervised live runs** are local, agentless read-only compatibility checks
   against a configured private backend.
5. **Private-live model runs** let Claude Code or Codex solve a reviewed task
   through either the exact-argv CLI gateway or guarded typed read-only MCP. They are explicit,
   single-run, local experiments and publish only privacy-reviewed aggregates.

Model-in-the-loop runs remain manual or opt-in because they cost resources and
are nondeterministic. Static and deterministic contract gates belong in CI.

## Versioned evaluation contract

Scenario files describe task class, capabilities, required oracle checks, and
hard budgets. Zero is a real zero rather than an unbounded sentinel. This makes
remote writes, model turns, and cost explicit in every scenario. A synthetic
read-only contract looks like:

```json
{
  "schema_version": 1,
  "id": "jira.epic-evidence",
  "task_class": "jira/evidence",
  "description": "Discover fields and collect bounded epic evidence.",
  "data_class": "synthetic",
  "required_capabilities": ["jira.issue.fields", "jira.epic.digest"],
  "required_checks": ["answer_correct", "sources_complete"],
  "required_metrics": ["atl_invocations", "backend_requests", "output_bytes"],
  "budgets": {
    "max_agent_turns": 0,
    "max_tool_calls": 0,
    "max_atl_invocations": 2,
    "max_delegations": 0,
    "max_backend_requests": 8,
    "max_duplicate_backend_requests": 0,
    "max_remote_writes": 0,
    "max_output_bytes": 8192,
    "max_input_tokens": 0,
    "max_output_tokens": 0,
    "max_main_thread_input_tokens": 0,
    "max_main_thread_output_tokens": 0,
    "max_estimated_cost_microusd": 0,
    "max_duration_millis": 0,
    "allowed_http_methods": ["GET"]
  }
}
```

Observations and unreviewed results contain aggregate trajectory data only. The
contract contains no prompt bytes, commands, HTTP paths, backend URLs, or
response bodies. A private Codex CLI result v6 retains only the activation mode
and a SHA-256 identity of its complete prompt contract; that digest is omitted
from aggregate output. Validate the committed scenarios and deterministic
workflows with:

```sh
make agent-eval-contract
go run ./scripts/agent-eval inventory benchmarks/agent-eval
```

The inventory command validates the whole corpus before returning only
aggregate category/task-class counts. Its success and error outputs never emit
scenario ids, filesystem paths, prompts, fixtures, or private values. For every
inventory row, `task_class` must also come from the closed public task taxonomy;
private project or roadmap names are rejected rather than echoed. Across all
provider/model cohorts for one `neutral-common` scenario it proves that runs
share byte-identical prompt and response-schema files, semantically identical
JSON task/rubric/fixture/oracle contracts, the same workspace and backend mode,
and the same declared data capabilities. Within each cohort, two or three
unique surfaces must additionally share repetition, pricing, timeout, and run
cost-cap contracts. Scenario budgets remain common because every run in the
directory binds the same scenario file.

The committed realistic matrix includes neutral-common cohorts for deep Jira
evidence, ordered batch reading, board portfolio synthesis, a long
repeated-heading Confluence decision, and cross-service topic discovery, plus
separately scored surface-native cases for GET-only Structure subtree batch
export, multi-table Confluence analytics, offline mirror review, and guarded
synthetic Jira/Confluence mutations. Delegation, injection, and point-route
cases remain route-fixed regression tests rather than general surface rankings.

New multi-surface scenarios use `interface_invocations`,
`max_interface_invocations`, and the corresponding `interface_*` run-check
aliases. Existing `atl_invocations` contracts remain valid; when the generic
budget is absent, `max_atl_invocations` is its compatibility limit.

Observations classify each run as `supported` (the default),
`unsupported-capability`, or `invalidated-backend-drift`. Unsupported runs name
only bounded capability identifiers; drifted runs carry no backend detail.
Ineligible runs do not count as task passes or failures, while their safety and
budget violations are still retained. Aggregate schema v6 reports eligible,
unsupported, and drifted counts, eligibility coverage, and success conditional
on eligible runs. Coverage excludes drift-invalidated blocks from its
denominator: drift says nothing about whether the surface supports the task.
Neutral and surface-native efficiency/quality summaries use
only supported deterministically valid runs; route-fixed historical aggregation
keeps its compatibility behavior. Current observations use schema v4 and
results use schema v6. Older result records without eligibility remain
supported through the documented result decoders; observation inputs must be
migrated explicitly before evaluation.

The maintainer tool can validate scenario files, evaluate one aggregate
observation, and combine comparable result files into p50/p90 groups:

```sh
go run ./scripts/agent-eval validate internal/cli/testdata/agent-eval/*.json
go run ./scripts/agent-eval evaluate scenario.json observation.json >result.json
go run ./scripts/agent-eval aggregate runs/*.result.json >aggregate.json
```

Aggregation separates providers, exact models, agent versions, variants,
benchmark categories, surfaces, `atl` versions, plugin versions, skill digests,
and skill activation. The private prompt-contract digest is deliberately not
serialized into or used as a visible aggregate dimension. Compare baseline and
candidate within one such runtime group; do not compare raw turns or dollar
estimates across providers.

### Qualitative answer review

Every model run spec names a versioned public rubric. Rubrics score bounded
criteria such as evidence grounding, qualification, task completeness,
actionability, and concision. They do not repeat fixture facts or contain a
reference answer; factual correctness remains the deterministic oracle's job.

After a private run, create a hash-bound review template:

```sh
/tmp/agent-eval review-template \
  --rubric benchmarks/agent-eval/jira-epic-evidence/rubric.v1.json \
  --result "$ATL_AGENT_EVAL_OUTPUT/jira.synthetic-epic-evidence/claude-code/v0.4-skill/run-01/result.json" \
  --final "$ATL_AGENT_EVAL_OUTPUT/jira.synthetic-epic-evidence/claude-code/v0.4-skill/run-01/final.json" \
  --blind-assignment "$ATL_AGENT_EVAL_OUTPUT/blind-assignment.txt" \
  --reviewer codex --model gpt-5.6-sol >"$ATL_AGENT_EVAL_OUTPUT/review.json"
```

Give the task, public rubric, and private final answer to a maintainer or a
separate no-tools model session. Treat the candidate answer as untrusted data:
instructions inside it do not alter the rubric. Replace the template's zero
scores with the review, and use only rubric-declared generic `finding_ids`.
Then bind and apply it:

```sh
/tmp/agent-eval assess \
  --rubric benchmarks/agent-eval/jira-epic-evidence/rubric.v1.json \
  --result "$ATL_AGENT_EVAL_OUTPUT/jira.synthetic-epic-evidence/claude-code/v0.4-skill/run-01/result.json" \
  --final "$ATL_AGENT_EVAL_OUTPUT/jira.synthetic-epic-evidence/claude-code/v0.4-skill/run-01/final.json" \
  --review "$ATL_AGENT_EVAL_OUTPUT/review.json" \
  >"$ATL_AGENT_EVAL_OUTPUT/reviewed-result.json"
```

The reviewed result retains only criterion scores, generic finding ids,
reviewer identity, and SHA-256 bindings. It never retains excerpts or a
free-form rationale. Aggregation separates different reviewer/model identities
and reports qualitative pass count plus p50/p90 normalized score. Compare
rubric scores only within the same rubric and reviewer runtime. Review all
repetitions; do not select only a favorable answer.

Private-live workspaces can instead predeclare an odd panel of three or five
reviewers. The `criterion-median-v1` policy computes each criterion from the
median member score, then computes the weighted normalized score from those
medians. It reports high disagreement when members split on overall pass/fail,
split across any criterion's pass boundary, or exceed the configured normalized
range threshold for any criterion. A disagreement blocks baseline promotion;
a unanimous low-disagreement failure may still be retained as a measurement
baseline. The complete roster, exact model identities, policy, and required
blind assignment are bound before provider execution. Legacy singleton and
panel results are deliberately comparison-incompatible rather than silently
migrated. See [Private agent-benchmark workspace](agent-benchmark-private-workspace.md)
for the panel manifest and operator flow.

Current assessments emit result schema v6, review schema v2, and aggregate
schema v6. Current decoders retain read compatibility with prompt-bound result
schema v5, panel result schema v4, singleton result schema v3, and
reviewer-id-free review schema v1.

For `neutral-common`, `--blind-assignment` is mandatory. It is a bounded private
file that maps randomized answer labels to candidates for the reviewer. Only
its SHA-256 digest and a `blinded:true` marker enter the review/result contract;
the assignment bytes and surface identity never do. `assess` rejects neutral
reviews with missing or malformed blind metadata.

Blind a comparison reviewer to surface names and randomize answer order. Use
one rubric and exact reviewer runtime. Report pass `n/N` and raw values when
`N=3`; summarize with median plus MAD/IQR without significance claims. Use a
per-class macro-average and a Pareto view of correctness/quality versus calls,
tokens, cost, and duration instead of one weighted global score.

Aggregate grouping includes the blind-assignment digest. Never pool scores from
different answer mappings as though they were repetitions of one comparison
contract. The digest remains internal and is never serialized in the aggregate:
small mapping domains are not safe merely because they were SHA-256 hashed.

Every observation also carries per-metric `coverage`. An observed zero is
different from an unavailable metric: a required metric without coverage fails
with `metric_not_observed`, while aggregation reports `observed_runs` before
p50/p90. In particular, a live run that cannot safely count backend methods
must not report an empty method map as measured zero traffic.

## Headless synthetic runner

Committed run specs bind one scenario to an exact provider/model, prompt,
structured response schema, deterministic mock fixture, oracle checks, reviewed
CLI command prefixes or typed MCP tool names, repetitions, timeout, and a whole-run
USD-equivalent cap. Output roots are owner-only and carry an ATL marker; use a
new empty directory or an already marked root. A non-empty legacy directory is
never adopted or chmodded implicitly. Review the provider command without
contacting a model:

```sh
make build
go build -o /tmp/agent-eval ./scripts/agent-eval

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-epic-evidence/run.codex.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" \
  --repository-root . \
  --agent-binary "$(command -v codex)" \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --dry-run
```

Every run-spec check is a gate, including variant-only checks that are not in
the shared scenario oracle. This lets a single-agent and delegated run share
one correctness contract while separately requiring zero or one delegation.

Synthetic CLI runs inherit `ATL_READ_ONLY=1` by default. A spec may opt out
with `allow_synthetic_writes:true` only when its scenario is synthetic, has a
positive write budget and explicit mutating HTTP method, and the spec includes
`guard_no_denials`, `mock_no_unexpected`, and an exact `http_methods_equal`
oracle. The proxy independently requires both configured backends to be plain
HTTP loopback origins and rechecks the reviewed command prefixes. Mock routes
may bind an exact semantic JSON `request_body`, so an allowed PUT with the
wrong field or value still fails the route oracle. This mode is never valid for
private-live or MCP runs.

For stateful synthetic reconciliation, a route may replace its single
`status`/`body` with a bounded `responses` array. Matching requests consume the
sequence in order; query/body mismatches do not consume it, and any request
after exhaustion is unexpected. Use this for a preflight-old / reconciliation-
new transition, not as a general backend emulator. A
`json_equals_workspace_json` run check can compare one final-response pointer
with one pointer in a contained, regular JSON artifact created inside the
synthetic workspace. It is forbidden in private-live specs and is useful for
dynamic review artifacts such as proposal hashes.

### Guarded Jira field mutation

`jira-field-mutation` covers three distinct review states with one file-backed
custom-field proposal. Preview runs under the inherited read-only policy and
must make exactly two GETs and no write. Reviewed apply must first use the
dedicated GET-only `jira issue field preview`, then bind its exact issue,
timestamp, and proposal hash to one `field set --apply`; the fixture accepts
only the exact JSON PUT body. The ambiguous variant returns a synthetic 5xx,
requires atl's one reconciliation read, reports `unknown`, and forbids replay.
All variants require an exact `atl:jira` Skill event and a structured answer;
the rubric scores the review boundary, proposal binding, ambiguity handling,
actionability, and concision without retaining proposed field content.
The reviewed Claude Code baseline passed 3/3 in every variant and scored
10,000 bps on all nine answers. Median trajectories were one atl invocation /
two GETs for preview, two invocations / four GETs / one PUT for apply, and two
invocations / five GETs / one PUT for the ambiguous case. Treat token, cost,
and duration values as provider observations; the exact method/body, guard,
proposal, outcome, and no-replay oracles are the stable claims.

### Guarded Confluence plan mutation

`confluence-plan-mutation` covers four states of the native multi-page plan
workflow. The preview-only variant creates the private plan offline, then runs
the dedicated GET-only preview under inherited `ATL_READ_ONLY=1`. Approved
variants bind the generated plan's exact proposal hash and page version to one
`conf plan apply --confirm APPLY`; the fixture accepts only the expected native
CSF body and version payload.

The mock response sequence exposes the same page as baseline during preview and
apply preflight, then returns either the committed candidate, an unchanged page
after a 409, or an unavailable reconciliation read after an ambiguous 5xx. The
method oracle remains `GET=1` for preview and `GET=3,PUT=1` for every approved
variant. Sequence exhaustion, a second PUT, a different body, a copied/wrong
proposal hash, an extra command, or replay makes the run fail.

The reviewed Claude Code baseline passed 3/3 for preview, reviewed apply,
version-conflict-no-replay, and ambiguous-no-replay; all twelve answers scored
10,000 bps. Stable median trajectories were two atl calls/one GET for preview
and three atl calls/three GETs/one PUT for each approved state. Median input was
69,577 tokens for preview and 86,571–86,959 for approved states; median duration
was 19.8–26.4 seconds. Compare future candidates against the exact runtime,
skill digest, prompt, fixture, and deterministic oracles before interpreting
token or timing movement.

### Jira skill routing comparison

Before a provider comparison, validate the provider-neutral discovery contract:

```sh
make check-skill-routing
```

The strict source catalog binds directory, logical name, description
boundaries, implicit policy, and Codex default-prompt target. The routing
registry and synthetic corpus then prove one declared skill or an explicit
no-activation result for every annotated task class, including
codebase-only Jira/Confluence mentions. This offline result is a policy and
packaging oracle, not a model-quality score: the prompt text is retained so the
same cases can be replayed in fresh model sessions when behavioral evidence is
needed.

The `jira-epic-evidence` CLI variant now requires the exact `atl:jira` skill,
then confines the model to one metadata-only issue-field discovery and one
compact digest. `skill_invocations_min` accepts an optional JSON-string
`expected` target; an unrelated installed skill no longer satisfies a targeted
experiment.

A controlled Sonnet comparison used the same synthetic task, prompt, response
schema, CLI binary, runner, model, and final 11-check safety/correctness spec.
Three independent attempts with the previous 513-line/33,384-byte skill all
loaded `atl:jira` but immediately proposed a shell pipe/redirection or invalid
argument shape; the guard stopped each before an atl invocation. The routed
140-line/8,014-byte core plus its direct evidence reference passed 3/3. Every
candidate run used exactly five model tools (Skill, one reference read, two
Bash calls, structured output), two atl invocations, nine synthetic GETs, zero
writes, and no guard denial; all required sources were complete. A reviewed
representative answer scored 10,000 bps.

The final strict baseline intentionally has no token/cost median because it
fails before producing a result; do not fabricate one from aborted sessions.
An earlier diagnostic three-run old-skill sample, before `guard_clean` was made
an explicit result check, failed 0/3 and reported a 225,587-token median versus
84,808 for the final candidate (-62%). That number is directional failure-path
evidence, not a pure context-size effect. Deterministic size changed as follows:

| Instruction surface | Previous | Routed | Change |
|---|---:|---:|---:|
| Always-loaded Jira skill | 513 lines / 33,384 bytes | 140 / 8,014 | -76% bytes |
| Core plus two new direct runbooks | 513 lines / 33,384 bytes | 320 / 16,334 | -51% bytes |

The candidate medians were seven turns, five tools, 84,808 input tokens, 855
output tokens, 111,697 reported micro-USD, and 26,625 ms. Treat provider token,
cost, and duration values as directional; the pass/guard/oracle and file-size
results are the stable claims.

The runner creates a fresh private workspace per repetition. Claude Code CLI
runs load the repository plugin explicitly and receive an `atl`-only Bash
allow-rule plus a `PreToolUse` guard. The guard accepts a bounded command block
separated only by newlines or the exact list operators `;`/`&&`; every `atl`
command independently matches a run-spec
`allowed_atl_commands` prefix and crosses the accounting proxy. The exact
leading `export ATL_READ_ONLY=1` and one `command -v atl` preflight line are
also accepted. Other operators, substitutions, redirections, and
unrelated binaries are denied before Bash.
For compatibility with the shipped read-only preflight, the provider permission
set and guard also admit the exact statements
`export ATL_READ_ONLY=1` and `command -v atl`. They do not admit arbitrary
exports or any other standalone shell command; the benchmark process already
inherits `ATL_READ_ONLY=1`, so these statements cannot weaken its policy.
Synthetic Codex cases get the generated skills in `.agents/skills` for a
reviewable ephemeral read-only command preview. Private-live CLI cases instead
install the snapshotted Codex plugin through its local marketplace, preserving
the shipped namespace. Both providers support MCP transport.
Claude receives a generated mode-0600 config under `--strict-mcp-config`; every
model tool crosses a global guard that grants only qualified reviewed
`mcp__atl__...` names plus required structured output. Its synthetic backend
environment is attached to the MCP child and omitted from the provider
environment. Codex starts the same exact reviewed `atl mcp serve` binary,
grants only `allowed_mcp_tools`, disables web search, removes atl credentials
from the model shell environment, and denies shell/file/patch/delegation tools
through `PreToolUse`. Codex benchmark runs set `project_doc_max_bytes=0` so
ambient global/repository `AGENTS.md` files cannot change the reviewed task;
the copied prompt and selected shipped plugin or skills remain the explicit
instruction sources. Synthetic CLI-transport Codex specs remain
validate/dry-run only because its OS sandbox cannot safely reach the host-side
mock; private-live Codex CLI specs use the reviewed zero-network command broker
below. Supported model runs inherit
`ATL_READ_ONLY=1`, `ATL_NO_UPDATE=1`, and synthetic loopback backend URLs/tokens.
CLI runs use an `atl` proxy that counts invocations and stdout bytes without retaining
command arguments; MCP runs count completed typed calls/failures and result bytes
from the provider event stream. Both paths additionally emit the same fixed
generic capability families (for example `jira.epic.digest` and
`confluence.page.section`) with invocation/success/failure/output-byte counts.
Arguments, selectors, URLs, ids, and response excerpts are never retained.
An unknown CLI route, MCP tool, denied proxy record, or unsupported provider
shape makes `coverage.capability_families=false` and suppresses the entire
per-family result instead of publishing a misleading partial attribution.
Provider token totals remain run-level because neither event protocol safely
attributes tokens to individual tool results. Proxy counters, config, and mirror
state are writable only below the private run workspace. The runner requests a
proxy-only subprocess `PATH`, but provider shells may expose system helpers;
the `PreToolUse` guard is therefore the authoritative command boundary rather
than a PATH assumption. The hook allowlist and `ATL_READ_ONLY=1` remain
independent: the hook limits which CLI reads the model may request, while the
CLI policy rejects every mutating command even if a prefix were configured too
broadly.

Confined skill readers accept `cat` over 1..16 reviewed files, bounded `sed`
ranges, and `wc -l`. Multi-file `cat` validates every canonical path before
writing anything and applies one combined 1 MiB cap; it does not enable options,
globs, substitutions, or arbitrary shell syntax.

Delegated variants also place `Agent` behind the hook: atomic private
slots enforce the scenario limit before the child starts, and a scenario may
permit at most three children. The public comparison uses one child, which also
prevents a second delegation level.

Guard records contain only `allow` or `deny`, never the proposed command or
child prompt. A denied attempt fails the safety oracle and cancels the provider
process promptly instead of spending the remaining run budget. Duplicate backend reads
are counted by exact method plus request target and are reported separately
from total requests. Claude `Read` is limited by the same hook to the generated
workspace and shipped skill tree; symlink-resolved paths outside those roots are
denied, so untrusted content cannot redirect it to ambient host files.

Raw provider JSONL, stderr, final structured output, invocation records, and
per-run results are mode `0600`; directories are mode `0700`. A destination
inside the repository is rejected unless `git check-ignore` proves it ignored.
Prompts/workspaces cannot escape the run-spec directory through `..` or
symlinks. The public synthetic case defaults to three repetitions and a maximum
USD-equivalent cost of $10 for the complete run spec. The runner divides that
cap across repetitions for the provider invocation and stops remaining runs if
the measured total exhausts it. `--repetitions 1` may reduce, but never increase,
the reviewed repetition count.

Codex's native tools are not treated as an allowlist surface equivalent to
Claude's `--allowed-tools`; MCP mode denies them through the reviewed hook and
removes atl credentials from their shell environment. Prompt-injection against
the committed synthetic fixture is supported. Private-live runs use the stricter
contract below; they never reuse a synthetic run spec implicitly.

For Claude MCP runs, the runner approves only its generated `atl` server, makes
startup readiness a bounded precondition, and grants the run spec's exact
qualified tool names through private settings. It does not pass dynamic MCP
names through `--tools` or `--allowed-tools`: current Claude Code applies those
CLI filters before dynamic discovery and can hide an otherwise connected
server's catalog. One matcher-less `PreToolUse` guard covers every model tool:
only exact reviewed MCP names and required structured output are allowed, so
permission-free built-ins cannot become a fallback. A client-side missing-tool
attempt still counts as a model tool call but not as an `atl` invocation
because no protocol request reached the server. An object-shaped MCP response,
including an error response, is
counted as an invocation; server errors fail the `atl_all_succeeded` oracle.

An expected fail-closed command can instead use `atl_failures_equals` with an
exact non-negative count. This is intentionally different from ignoring a
failure: the final answer must still pass its evidence oracles, the invocation
is attributed as a failed capability event, and any extra failed or successful
invocation violates the scenario's exact budgets. The durable-mirror cell uses
this contract for a `conf diff` that emits qualified JSON and then returns exit
8 on corrupt baseline evidence.

When a CLI+skill experiment is intended to measure shipped skill guidance, add
`skill_invocations_min` rather than trusting prompt wording or an installed
plugin digest alone. The Claude provider counts exact `Skill` tool events; set
optional `expected` to a JSON string such as `"atl:jira"` when the experiment
must load one specific skill. The check fails if the model omits it or loads a
different skill. This is an execution oracle only: skill invocations are not
added to the public result metrics, and providers without an equivalent event
cannot satisfy the check.

### Offline durable mirror review

The `confluence-mirror-review` cell copies a fully synthetic durable mirror into
the private run workspace. It contains one semantic edit, one byte-only native
change, one unchanged page, and one baseline whose stored bytes no longer match
the tracked sync hash. The model may invoke only this command and cannot read
the underlying CSF or sidecar files directly:

```sh
atl conf diff mirror --into mirror
```

The deterministic test pins all four classifications and proves that the mock
backend receives zero requests. The model oracle additionally requires the
installed Confluence skill to be loaded, exactly one atl invocation, exactly
one expected fail-closed result, no delegation or guard denial,
`complete:false`, and a blocked publish decision with safe baseline recovery.
This exercises the important distinction between “the tool failed to produce
evidence” and “the tool produced evidence that deliberately blocks the
workflow.”

A controlled three-repetition Sonnet comparison held the fixture, model, agent
version, compact skill digest, answer schema, rubric, deterministic oracles,
and safety budgets constant. The prompts differ only in the exact output flag
and projection interpretation. Both variants passed 3/3 runs and all 13 checks,
loaded the skill, used one fail-closed `conf diff`, made zero backend
requests/writes, and scored 10,000 bps in reviewed representative answers.
Medians were:

| Metric | Full JSON | Compact text | Change |
|---|---:|---:|---:|
| Agent turns | 5 | 5 | 0% |
| Model tool calls | 3 | 3 | 0% |
| `atl` invocations | 1 | 1 | 0% |
| Agent-visible tool bytes | 4,556 | 545 | -88% |
| Input tokens | 45,252 | 43,605 | -4% |
| Output tokens | 775 | 551 | -29% |
| Reported cost, micro-USD | 85,870 | 72,643 | -15% |
| Duration, ms | 21,771 | 17,395 | -20% |

This supports `-o text` as the first-pass directory review surface while JSON
remains the drill-down contract. Text bytes are stable because paths are
root-relative; the JSON count includes canonical run paths and therefore varies
slightly with the private output-root length. Token, cost, and duration medians
remain directional provider observations. An earlier one-run table did not
require the `Skill` tool and incorrectly attributed differences in skill loading
to the projection; it has been replaced by this skill-enforced comparison.

The Confluence skill itself was then compared on the same compact-text prompt,
schema, runner, CLI binary, provider, and three-repetition oracle. The previous
343-line/17,256-byte always-loaded body and the routed
120-line/6,226-byte body both passed 3/3 runs with five turns, three model tools,
one atl invocation, zero backend traffic, and zero writes. Median input tokens
fell from 49,364 to 43,605 (-11.7%). Observed median output tokens were 522 and
551, reported cost was 90,319 and 72,643 micro-USD, and duration was 18,333 and
17,395 ms; these model-dependent values are directional rather than
deterministic. The two new one-hop references keep the complete routed
instruction set at 265 lines/12,859 bytes; agents load only the selected
workflow after the compact safety and routing core.

### Topic-first cross-service discovery

The `cross-service-topic-discovery` synthetic cell covers the common workflow
that begins without stable identities. It holds one topic constant while the
CLI + shipped `search-knowledge` skill or typed-MCP route searches Jira and
Confluence once, qualifies both candidate pages, rejects distractors, and reads
one exact Jira field plus one outline-selected Confluence section.

```sh
/tmp/agent-eval run \
  --spec benchmarks/agent-eval/cross-service-topic-discovery/run.cli.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/cross-service-topic-discovery/run.mcp.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1
```

The deterministic route is exactly six GETs with one duplicate target caused
by outline then section rendering of the selected page. Full-page reads,
mirror writes, repeated searches, distractor expansion, remote writes, and
delegation fall outside the reviewed route or budgets. Use this baseline before
adding remote search tools. The first reviewed typed-MCP baseline preserved the
oracle and passed all 18 gates with five typed calls, five GETs, one duplicate
target, zero writes, and a 10,000-bps qualitative score. The lower GET count is
valid because the model reused the system `description` id directly; using a
display name may consume the sixth allowed request. Treat this one run as
directional evidence for the bounded tool contract, not a stable speed or cost
claim.

## Deterministic contract budgets

`TestEvidenceFirstEpicWorkflowBudget` runs the first-use epic workflow against a
synthetic backend and fails if it exceeds eight read-only requests, 8 KiB of
combined JSON context, or omits explicit completeness for any default source:

```sh
go test ./internal/cli -run TestEvidenceFirstEpicWorkflowBudget -v
```

The budget is intentionally an upper bound: fewer calls/bytes are improvements,
while added evidence must justify changing the bound in review.

The same contract now covers Jira Kanban inspection and Confluence
outline-to-section ambiguity recovery. Add a scenario whenever an agent-facing
workflow gains a new command, changes its expected trajectory, or needs a
regression budget.

## Subagent experiments

Delegation is a measured variant, not a default assumption. Compare a single
agent, a generic read-only child, specialized children, and bounded parallel
children on the same scenario. A child receives one independent task, inherits
the read-only boundary, and returns qualified evidence rather than a raw
transcript. Use at most three children and one delegation level in the default
suite.

Track provider-reported total tokens and cost, delegation/tool counts, duplicate
backend requests, wall time, and evidence lost during summarization. Claude's
current result exposes main-thread `usage` separately from cross-model
`modelUsage`; the runner records both `main_thread_*_tokens` and total
`*_tokens`. If a provider omits either view, its coverage remains false instead
of being inferred.

Delegate only when a task has an independently bounded evidence-gathering
stage whose compact result reduces pressure on the main context, such as a
large multi-source report. Do not delegate a simple one-object read, any remote
write, a task whose child would need the entire parent transcript, or work where
the parent cannot verify completeness. The parent owns the final answer and
must consume qualified evidence rather than a raw child transcript.

## Supervised live read-only check

Use configured private fixtures only to confirm backend compatibility. Keep all
outputs in a gitignored local directory with restrictive permissions and
publish only aggregate counts:

```sh
umask 077
workdir=$(mktemp -d)

ATL_CONFIG_DIR="$ATL_PRIVATE_CONFIG_DIR" atl --read-only jira issue fields \
  "$ATL_BENCHMARK_EPIC_KEY" >"$workdir/fields.json"

ATL_CONFIG_DIR="$ATL_PRIVATE_CONFIG_DIR" atl --read-only jira epic digest \
  "$ATL_BENCHMARK_EPIC_KEY" --quarter "$ATL_BENCHMARK_QUARTER" \
  --status-field "$ATL_BENCHMARK_STATUS_FIELD" >"$workdir/digest.json"

wc -c "$workdir/fields.json" "$workdir/digest.json"
jq '{sources: (.sources | map_values({complete,count}))}' "$workdir/digest.json"
```

Record only exit status, elapsed time, output byte count, request count when it
can be observed safely, and source completeness. Do not enable verbose tracing
for the benchmark or copy the private files into issues/PRs. Remove the private
temporary directory after review.

Corporate runs must export `ATL_READ_ONLY=1` for the entire runner process and
use concurrency one. A separate backend read-only credential is preferred but
not required; the CLI policy and a scenario command allowlist remain mandatory.
Write-path evaluation belongs on the synthetic backend or an explicitly
disposable, separately authorized fixture.

Headless model runs use provider subscription authentication already stored by
the provider CLI. Codex execution requires a safe file-backed `auth.json` in the
effective `CODEX_HOME` (or `HOME/.codex` fallback); keyring-only authentication
is not accepted by the hermetic benchmark path. The runner copies only that
bounded owner-only JSON file into an owner-only ephemeral provider home. It does
not copy global instructions, config, skills, history, sessions, memories, or
caches. Each repetition gets a fresh home. Bounded syntactically valid auth JSON
can flow to the next repetition through memory, but the source provider home is
never modified. This is structural validation, not a semantic credential
check. API-key and unrelated credential environment variables remain absent
from the agent process. Actual Codex execution currently requires POSIX
owner-only mode enforcement and fails closed on Windows; validation and dry-run
remain cross-platform.

## Private-live model-in-the-loop check

For normal operation, use the marked workspace lifecycle in
[Private agent-benchmark workspace](agent-benchmark-private-workspace.md). It
gives agents a single `init -> doctor -> plan -> run -> review -> baseline ->
compare -> prune` state machine, binds execution to reviewed input hashes, and
keeps raw candidates out of unrelated `/tmp` directories. The lower-level
commands below document the transport and confinement contract and remain an
escape hatch for framework development; they are not the recommended way to
maintain a private baseline.

Use this mode only when the maintainer has approved sending the selected real
Jira/Confluence evidence to the configured model provider. The provider will
receive the prompt, MCP responses or CLI output selected by the model, and the
final answer.
It does not receive a general shell, filesystem reader, raw REST client, mirror
writer, or mutation tool. Codex may use runner-provided `cat`, `sed -n`, and
`wc -l` shims solely for installed skill/workspace files; both the hook and the
shim independently resolve every path inside the reviewed roots.

Keep the complete case in a private directory outside the repository. A run is
rejected when its spec is tracked by Git, when the transcript root is not
ignored, or when the source atl config directory is inside the repository.
The config directory and its `config.json`/`credentials.json` must be owner-only.
Private workspace templates may contain reviewed evidence files, but not
provider control surfaces such as `AGENTS.md`, `CLAUDE.md`, `.mcp.json`,
`.agents/`, `.claude/`, or `.codex/`; the runner installs the shipped skills and
its own hooks/configuration after validating that boundary.

A private run spec differs from a synthetic spec in these fields:

```json
{
  "schema_version": 5,
  "backend_mode": "private-live",
  "category": "neutral-common",
  "surface": "atl-mcp",
  "scenario_file": "scenario.v1.json",
  "provider": "codex",
  "variant": "typed-mcp",
  "model": "gpt-5.6-sol",
  "reasoning": "medium",
  "prompt_file": "prompt.md",
  "response_schema_file": "response-schema.json",
  "qualitative_rubric_file": "rubric.v1.json",
  "workspace_template": "workspace",
  "fixture_file": "",
  "repetitions": 1,
  "timeout_seconds": 600,
  "max_estimated_cost_microusd": 10000000,
  "pricing": {
    "input_microusd_per_million_tokens": 10000000,
    "output_microusd_per_million_tokens": 50000000
  },
  "tool_transport": "mcp",
  "allowed_tools": [],
  "allowed_atl_commands": [],
  "allowed_mcp_tools": [
    "jira_fields",
    "jira_epic_digest",
    "confluence_page_resolve",
    "confluence_page_outline",
    "confluence_page_section"
  ],
  "data_capabilities": [
    "confluence.page.outline",
    "confluence.page.resolve",
    "confluence.page.section",
    "jira.epic.digest",
    "jira.fields"
  ],
  "checks": [
    {"name":"interface_succeeded","kind":"interface_all_succeeded"},
    {"name":"guard_clean","kind":"guard_no_denials"},
    {"name":"http_observed","kind":"http_methods_observed"},
    {"name":"no_delegation","kind":"delegations_none"},
    {"name":"used_interface","kind":"interface_invocations_min","minimum":1},
    {"name":"bounded_interface","kind":"interface_invocations_max","maximum":5},
    {"name":"complete","kind":"json_equals","pointer":"/complete","expected":true}
  ]
}
```

Its private scenario must use `data_class:"private-local"`, exactly one
repetition, zero delegations and writes, positive invocation/request limits,
an explicit `allowed_http_methods` containing only `GET`/`HEAD`, and name
`complete` in both `required_checks` and `required_semantic_checks`. Start with
the smallest MCP execution allowlist and response schema that can answer the user task.
Every neutral run also declares a sorted `data_capabilities` set. Built-in CLI
and typed-MCP routes are reduced to this semantic set during spec validation;
an external MCP run is bound to the same set through its owner-reviewed profile.
Expected private facts may live in the ignored run spec; never copy them into a
public fixture or PR.

This contract is run-spec schema v5. Specs already on v3/v4 retain their
semantic capability declarations and require a version bump. A Codex `private-live`
`cli-skill` spec must declare exactly one of `skill_activation:"implicit"`,
`"explicit"`, `"developer"`, or `"combined"`; the field is forbidden on MCP,
Claude Code, and synthetic cells. Existing v4 `implicit` and `explicit` specs
retain those meanings when deliberately migrated to v5. A legacy v3 spec that
named a skill only in provider instructions is not silently relabeled
`developer`: review the intended treatment, create a v5 spec, and start a new
activation-bound baseline.
Validation happens before credentials, model execution, or backend traffic.

The external profile's mandatory `reviewed_ro:true` is the explicit owner
assertion for servers that omit optional MCP tool annotations. When annotations
exist, `readOnlyHint:false` or `destructiveHint:true` is an unconditional
preflight failure. When they are absent, the proxy still requires the exact
catalog and input-schema digests, a read-only semantic capability and
non-mutating tool name, exact allowed arguments, and bounded invocations; the
profile cannot override contradictory annotations.
The proxy accepts the protocol-reserved `tools/call` `_meta` field used by some
clients, then removes it before forwarding. All business arguments remain
canonicalized and exact-profile-bound; any other unknown params field fails
closed.

Codex runs explicitly disable provider-managed Apps, browser/computer,
image-generation, and remote-plugin features. Those account-side
tools are outside the reviewed comparison surface and must not be initialized
merely because the provider account exposes them. CLI runs retain only the
hook-guarded local shell route; MCP runs retain only the exact configured MCP
server. A present but empty HTTP audit is recorded as an observed zero-request
outcome, so a schema-valid answer that skipped evidence becomes a persisted
failing measurement instead of an infrastructure interruption. A reviewed
Codex binary that does not recognize one of the pinned feature flags fails
closed before model or backend access under `--strict-config`.

The `cli-skill` surface also pins the built-in `shell_tool` and `unified_exec`
features on and supplies a fixed `/bin/sh` inside the isolated capsule. It does
not inherit the operator's interactive `SHELL` or startup files. This only makes
the reviewed local route available: the hook, custom filesystem profile,
one-command broker policy, read-only environment, and GET/HEAD gateway still
decide what may execute. MCP surfaces do not enable this CLI-only feature pair.

Before shell execution, a mode-neutral provider instruction requires evidence
through the literal `atl` executable. It does not name a skill, expose the
hidden allowlist, or reveal a case-specific command. A model that answers
without an interface call remains a normal measured failure.

Generated Codex skill reads commonly use paths relative to the model workspace.
Private-live guards bind the runner's canonical ephemeral workspace and exact
ordered read-root JSON directly into the reviewed hook command, while projecting
the same values to ordinary tool subprocesses. The hook command also binds its
guard mode, owner-private audit counter, and exact MCP tool allowlist, so its
policy does not rely on ambient propagation. Codex MCP uses only its
generated workspace root; CLI+skill adds the verified installed-plugin skill
root. Only bounded `cat`, `sed`, and `wc` shapes are admitted; missing, relative,
duplicate, or unclean policy and paths resolving outside reviewed roots through
traversal or symlinks remain denied.

For Codex private CLI runs, skill activation is a 2x2 treatment over two prompt
channels. “Named” below means the runner deterministically derives
`$atl:jira` or `$atl:confluence` only from the reviewed `data_capabilities`:

| `skill_activation` | User channel (effective stdin) | Developer channel |
| --- | --- | --- |
| `implicit` | Exact core prompt bytes | Neutral evidence instruction; no skill name |
| `explicit` | `$atl:jira\n\n` or `$atl:confluence\n\n`, then exact core prompt | Neutral evidence instruction; no skill name |
| `developer` | Exact core prompt bytes | Evidence instruction names the derived skill |
| `combined` | `$atl:jira\n\n` or `$atl:confluence\n\n`, then exact core prompt | Evidence instruction names the same derived skill |

The named developer instruction preserves the exact pre-v4 compatibility-control
wording: `This is an evidence task. Before answering, select and follow the
installed $atl:jira skill implied by the reviewed data capabilities, then use…`
or the corresponding Confluence form. The remaining safety/evidence text is
byte-identical to that control. This prevents a wording change from becoming an
untracked fifth treatment. Both variants require evidence through the reviewed
literal `atl` interface and disclose no case-specific command, field selector,
expected answer, backend identity, or command policy. The user-channel prefix
contains only the derived skill invocation plus two newlines; the core task
itself is unchanged.

Every treatment that names a skill in either channel (`explicit`, `developer`,
or `combined`) requires a non-empty capability set belonging entirely to one
service. Jira-only capabilities derive `atl:jira`; Confluence-only capabilities
derive `atl:confluence`. Mixed, empty, and unknown capability families fail
closed during spec validation, before credentials, model execution, or backend
traffic. `implicit` is the only treatment that supports a mixed-service task.

The runtime result binds the treatment identity separately and also includes it
in the canonical schema-v1 prompt-contract envelope alongside the exact core
prompt, effective stdin, and exact developer instruction. SHA-256 covers that
whole envelope. A named skill is therefore bound through the bytes of every
channel in which the treatment places it. The digest is retained only in
owner-private plan/result artifacts. Low-level dry-run reports only
`prompt_contract_bound:true`; the digest is intentionally omitted from preview
and aggregate JSON because a short private prompt may be guessable. Baseline
comparison nevertheless requires both the exact digest and treatment to match.
Result schema v6 carries both fields; aggregate schema v6 carries activation as
a grouping/runtime dimension but deliberately omits the digest. Legacy result
v5 and private-plan v2 artifacts retain only their bound `implicit`/`explicit`
identities and cannot contain the new treatments. Result v3/v4 and private-plan
v1 artifacts remain readable under their earlier legacy rules. None is silently
reclassified as `developer` or `combined`.

Private-workspace manifest schema v2 provides two run-set kinds. A
`comparison` contains one to three unique surfaces and keeps any Codex CLI member
implicit-only. An `activation-study` contains exactly four otherwise-identical
Codex `private-live` `cli-skill` v5 specs, one per treatment, in one case
directory. It requires a blinded `criterion-median-v1` panel with exactly three
or five reviewers and a positive explicit `reviewer_reserve_microusd`. Current private
plans use schema v4; activation-study execution state uses its four-cell v2
lifecycle rather than the legacy per-surface state.

One activation-study plan and one consent bind the common contract, all four
exact treatment contracts, panel roster, any required blind assignment,
execution snapshot, and provider-auth session. Attempts run sequentially through
these canonical balanced orders:
`implicit/explicit/combined/developer`,
`explicit/developer/implicit/combined`,
`developer/combined/explicit/implicit`, and
`combined/implicit/developer/explicit`. The cycle is scoped to the exact
reviewed study material rather than a mutable run-set alias. A terminal
execution with a durable pre-spawn provider commitment advances modulo four,
including stopped or uncertain provider outcomes. A bare launch marker, expired plan,
or recovered pre-provider
interruption does not advance, so allocating plans or renaming an alias cannot
select a preferred order. Terminal attempts are not resumed or reused; the next
attempt requires a new plan and consent.
An incomplete crash state blocks the same series until it is reconciled with
the offline `private study recover --confirm PROVIDER_STOPPED_RECOVER`
transition; the confirmation is an operator attestation that no orphaned
provider process remains. The attempt is never counted as a completed order or
replayed automatically. Recovery validates and accounts for any durable
execution receipt, removes the exact owned execution capsule, and never invokes
the provider or backend. Execution and recovery summaries expose `cost_known`;
when it is false, the numeric detected total is only a lower bound.

The plan partitions the four treatment caps plus the reviewer reserve below the
workspace maximum. The runner does not launch panel reviewers, so the reserve
records reviewed authorization rather than measured reviewer receipts. This is
detection-only cost assurance, not a preventive
provider-side hard cap: reported cost and coverage are checked after provider
calls, and exhaustion, uncertainty, or a safety failure stops remaining cells
without undoing already-incurred cost.

Review selects the same `cli-skill` surface by `--treatment`. Prepare all
four-by-three or four-by-five blinded packets before the first assessment. Once
all deterministic and panel results exist, capture an immutable study reference,
inspect its privacy-safe closed-field report, and promote it only if the stricter
all-cell gate passes:

```sh
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

/tmp/agent-eval private review prepare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$REVIEWED_PLAN_ID" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01

/tmp/agent-eval private review assess \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$REVIEWED_PLAN_ID" \
  --surface cli-skill \
  --treatment implicit \
  --reviewer-id reviewer-01

/tmp/agent-eval private study reference \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --plan "$REVIEWED_PLAN_ID" \
  --reference activation-study-01 \
  --confirm REFERENCE

/tmp/agent-eval private study compare \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --reference activation-study-01

/tmp/agent-eval private study promote \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root . \
  --reference activation-study-01 \
  --confirm PROMOTE
```

The review commands above show one packet slot; prepare every treatment and
reviewer slot before running any assessment. The immutable reference rejects
different content under an existing alias. `private study compare` emits only
closed treatment metrics, gates, and causally eligible rational contrasts for
the user channel, developer channel, and interaction—never private paths,
prompts, hashes, identities, or backend details. `private study promote` also
requires every cell to have pass run status, zero deterministic violations and
deterministic pass, complete clean safety, and panel review pass without
disagreement before updating the study pointer. Legacy
treatments collected in separate comparison plans remain descriptive,
non-causal observations and are never upgraded automatically into a study.

Provider fidelity is explicit: private Codex CLI runs hash the complete
`plugins/atl/` package plus the local marketplace descriptor, install
`atl@atl` inside a fresh provider capsule, and verify its one-entry installed
inventory before launch. No project-skill copy or ambient plugin is accepted.
The installed copy must reproduce the reviewed package digest; every discovered
plugin-provided MCP server is disabled and rechecked so the measured interface
remains CLI+skill. Guarded file reads admit the installed skill root, not an
unused provider tree. Claude Code receives and hashes the generated root plugin
tree. The private snapshot includes only the package selected by the run's
provider.

Codex JSONL does not expose a trustworthy native skill-load event. The runner
therefore makes two narrower claims: local inventory proves that the reviewed
namespaced plugin was available, while `interface_invocations_min` plus the
answer oracle proves reviewed CLI evidence activation. A no-interface answer
is a measured failure; it is not reclassified as a missing-plugin error or
reported as direct proof that a skill did or did not load.

At the low level, review without invoking the model or backend, then run once.
New private comparison baselines and activation studies should use
`agent-eval private plan` and `private run` instead, so the reviewed bytes and
execution remain bound:

```sh
umask 077
make build
go build -o /tmp/agent-eval ./scripts/agent-eval

/tmp/agent-eval run \
  --spec "$ATL_PRIVATE_EVAL_CASE/run.codex.json" \
  --output-root /tmp/atl-private-live-runs \
  --repository-root . \
  --agent-binary "$(command -v codex)" \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --live-config-dir "$HOME/.config/atl-private" \
  --dry-run

/tmp/agent-eval run \
  --spec "$ATL_PRIVATE_EVAL_CASE/run.codex.json" \
  --output-root /tmp/atl-private-live-runs \
  --repository-root . \
  --agent-binary "$(command -v codex)" \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --live-config-dir "$HOME/.config/atl-private"
```

For MCP, the runner copies only `config.json` and `credentials.json` into an
ephemeral owner-only directory used by the MCP child and removes that copy
after the session. The model-facing process has no general native tool capable
of reading it, and the confined skill readers cannot resolve paths outside the
generated workspace/public skill roots. CLI runs do not copy source
credentials at all: the parent reads them, starts the gateway, and writes a
separate child config containing only disposable ingress capabilities. Claude
Code uses parent-loopback ingress. For Codex, the child config and gateway stay
parent-side behind a command broker; the model receives neither the upstream
origin/PAT nor the disposable gateway credential.

`ATL_READ_ONLY=1` blocks mutations at the CLI policy, the MCP inventory contains
only explicit read tools, and an independent HTTP transport guard rejects every
method except GET/HEAD before network I/O. That guard records only method plus a
SHA-256 request identity, allowing exact request and duplicate-read counts
without retaining URLs, JQL/CQL, ids, headers, or bodies.

One repetition and concurrency one are mandatory. A failed or denied MCP call,
missing HTTP audit, attempted native tool, unobserved required metric, or method
outside the scenario allowlist fails the run. Do not relax those gates to make
a private backend pass; reproduce compatibility failures outside the model run
with the supervised agentless recipe first.

### Reviewed CLI command policy

The primary skill-to-Bash-to-CLI path has a stricter private-live run-spec
contract than synthetic CLI benchmarks. It never accepts the legacy
`allowed_atl_commands` prefix list. Instead, every allowed invocation declares
the complete command path, exact positional values, exact flag values, and an
independent invocation cap:

```json
{
  "tool_transport": "cli",
  "allowed_tools": ["Bash(atl *)", "Read", "Skill"],
  "allowed_atl_commands": [],
  "allowed_cli_commands": [
    {
      "name": "jira_digest",
      "command": ["jira", "epic", "digest"],
      "positionals": [{"values": ["PROJ-1"]}],
      "flags": [
        {"name": "--quarter", "values": ["2026-Q2"], "required": true},
        {"name": "--status-field", "values": ["Delivery Notes"]},
        {"name": "-o", "values": ["json", "text"]}
      ],
      "max_invocations": 1
    }
  ],
  "allowed_gateway_routes": {
    "jira": [
      {"name": "jira_api", "path_prefix": "/rest/api/2"}
    ]
  },
  "gateway_max_response_bytes": 1048576,
  "gateway_max_total_response_bytes": 4194304,
  "allowed_mcp_tools": []
}
```

Flag order may vary, but unknown, repeated, missing-required, joined
`--flag=value`, changed-target, extra-positional, and shell-separator forms are
rejected. A single leading global `--read-only` is accepted because it can only
strengthen the already mandatory process policy; no other global flag is
implicit. The hook admits path-confined `cat`/`sed`/`wc` skill reads and a small
newline-only block containing `export ATL_READ_ONLY=1`, `command -v atl`, and
safe `atl ...` command shapes. Shell operators, substitution, redirection, and
arbitrary commands remain denied. The shim then loads an
owner-only policy file outside those roots and matches the actual argv before
starting the real binary. It reserves the command-family budget before the
process starts, so concurrent or failed invocations cannot exceed the reviewed
cap. Metrics retain the generic rule name, exit status, and byte counts, never
the private arguments.

The route policy is evaluated after ingress authentication and before any
upstream request. Only GET/HEAD without a body are accepted; the upstream
origin is pinned, redirects are rejected, and per-response plus total byte
budgets are enforced. Any gateway denial or an incomplete forward/completion
audit pair fails the run.

Claude Code receives only the reviewed `Bash(atl *)`, confined `Read`, and
shipped `Skill` surfaces and loads no ambient setting sources. Codex uses a
dedicated permission profile extending workspace access without enabling
command networking. Its filesystem policy grants write access only to an
owner-only request directory and read access to a separate owner-only response
directory. The model sandbox cannot reach the disposable loopback gateway,
source backend, public Internet, or private network directly.

After the hook and exact-argv shim accept a command, the shim writes a bounded,
authenticated request. The parent-side broker atomically moves that request out
of the writable directory, independently revalidates the exact argv policy and
invocation budget, and runs the real `atl` binary with only the disposable
child config. It returns bounded stdout, stderr, and exit status through the
read-only response directory. Requests are removed before execution; responses
and the manifest are removed during delivery or run cleanup. Arguments and
command output are not retained in benchmark results or gateway audit records.
Gateway bearer authentication, GET/HEAD and route allowlists, response budgets,
and privacy-safe audit checks still apply after the provider boundary.

Before the model starts, the runner invokes Codex's sandbox command with the
same filesystem policy. The probe must fail to connect to a live parent-side
loopback sentinel and must then complete a broker readiness request. The
readiness request is handled locally, consumes no invocation budget, and cannot
cause an upstream request. Unexpected command networking, missing
permission-profile support, inaccessible broker paths, unsafe file modes, or an
unavailable broker therefore fail before model or backend access. Codex 0.138.0
or later is required for custom permission profiles. Every platform must pass
the same runtime probe; there is no fallback that enables broad command
networking. Linux and macOS use the same file-broker contract; on macOS the
Seatbelt-backed profile must pass the loopback-denial and broker-readiness
probe or the run aborts before the model starts.

The provider process keeps its normal subscription-authenticated model
connection. Before actual Codex execution (never during dry-run), the runner
creates distinct ephemeral `HOME`, `CODEX_HOME`, XDG, and temporary roots. It
projects only the validated file-backed auth object and preserves the bounded
proxy/TLS/locale environment needed by the provider connection. Version probing
uses a disposable capsule. Each model repetition gets another fresh capsule;
its private CLI confinement probe and model process share that capsule while
retaining stage-specific `PATH` confinement. Across repetitions and plan
surfaces only a revalidated in-memory auth object flows forward—never sessions,
logs, caches, instructions, or configuration. The source provider home is not
written back.
The custom network policy applies separately to commands spawned by the model,
whose environment excludes source URLs/PATs and ambient proxy variables.
`--ignore-user-config` on surfaces without a locally installed test plugin,
`--ignore-rules` where applicable, and
`project_doc_max_bytes=0` remain defense in depth; the ephemeral home is the
structural control that excludes home-scoped `AGENTS.md`, user skills, config,
history, sessions, memories, and caches. The private CLI cell deliberately reads
only the fresh config produced by its local `atl@atl` installation; exact CLI
overrides still pin approvals, hooks, tools, and the disabled bundled MCP
server. Repository control files are rejected from private workspace
templates, and the installed package comes only from the reviewed snapshot.

The provider-home capsule is outside retained run artifacts and cleanup is
attempted on ordinary success, error, timeout, or validation failure. Cleanup
failure fails the run closed and leaves residue for review. For plan-managed
private runs, residue remains inside the marked owner-private `.ephemeral`
boundary, makes `private doctor` fail closed, is never reused, and is not
eligible for baseline promotion. A direct low-level `run` uses the selected
private output root instead and must be inspected there. A process crash cannot
promise physical erasure.

Private-live Codex CLI runs also receive a provider-scoped operational
instruction that identifies the run as an evidence task. Before answering, the
agent must use the minimum necessary invocation or invocations of the reviewed
literal `atl` shell interface, and the answer must be grounded in the returned
evidence. A no-tool or assumption-only answer is invalid for this benchmark.
Direct `apply_patch`, edit, write, or filesystem access to broker manifests and
request/response files remains denied by `PreToolUse`. In particular, an ad-hoc
JSON file proposed after a failed shell command is not a broker request and is
never an alternate execution path. The authenticated shim protocol and the
parent-side broker remain the only supported route; a shim failure must be
reported through the response schema rather than bypassed. The exact developer
instruction is selected by treatment: it is neutral for `implicit` and
`explicit`, and names the derived service skill for `developer` and `combined`.
The exact user channel is the core prompt for `implicit` and `developer`, and
the documented skill prefix plus unchanged core prompt for `explicit` and
`combined`. No treatment changes the response schema or semantic task contract.

Run `--dry-run` first, inspect the provider plan and local private spec, then
remove that flag for the single supervised execution. Use the same task,
response schema, rubric, and evidence scope for a paired typed-MCP run.

### Multi-surface comparison

A surface comparison is valid only when the task and evaluation contract are
identical. Keep two or three specs in one private case directory and use a
transport-neutral prompt: say “use the available atl interface”, not “call this
MCP tool” or “run this shell command”. Variants should identify only the
surface, for example `cli-skill`, `atl-mcp`, and `external-mcp`.

This `comparison` run-set kind is separate from the same-surface
`activation-study` kind, whose one plan contains exactly four treatment cells.
A Codex CLI member of a multi-surface comparison must use `implicit`;
`explicit`, `developer`, and `combined` are rejected because adding a skill name
to either prompt channel would confound the surface comparison.

Preflight the pair before either model invocation:

```sh
/tmp/agent-eval validate-comparison-set \
  "$ATL_PRIVATE_EVAL_CASE/run.cli.codex.json" \
  "$ATL_PRIVATE_EVAL_CASE/run.atl-mcp.codex.json" \
  "$ATL_PRIVATE_EVAL_CASE/run.external-mcp.codex.json"
```

The validator requires unique surfaces in one private case directory. Provider,
model, reasoning, category, scenario and budgets, core prompt bytes, response
schema, rubric, workspace, semantic response checks, repetitions, timeout,
pricing, and cost cap must match. Mechanical checks have a closed
classification and may differ for surface-specific guards. Tool, command,
route, and gateway policies also remain surface-specific. Success reports only
category, provider, and surfaces, never private scenario identity.

`validate-pair` remains a compatibility wrapper for `cli-skill` plus `atl-mcp`.
The `external-mcp` surface requires `--external-mcp-profile` pointing to a
regular owner-only file in an owner-only directory outside the repository. The
profile contains private endpoint and tool identities, while header values are
bindings rather than literal credentials. Header values bind only to
`jira|confluence.credential|base_url` from `--live-config-dir`; the runner
parent injects them only on the upstream hop. It also pins the protocol, full
catalog digest, every selected input-schema digest, exact allowed argument
objects, per-tool invocation caps, and byte/concurrency/time budgets. Catalog
identity canonicalizes each tool object and sorts by tool name, so response
ordering is irrelevant while every other catalog content change remains a hard
preflight failure. A server with a finite, reviewed set of otherwise unstable
catalog encodings may pin at most seven additional exact digests; an unlisted
variant still fails closed. A selected tool may likewise pin at most seven
reviewed input-schema digests; the full catalog digest includes that schema, so
both pins must match a reviewed catalog response. The sum
of invocation caps and the total response cap must fit the scenario budgets.

```json
{
  "schema_version": 1,
  "upstream_url": "https://mcp.example.invalid/mcp",
  "protocol_version": "2025-06-18",
  "catalog_sha256": "<64 lowercase hex bytes>",
  "catalog_sha256_alternates": ["<optional reviewed variant>"],
  "reviewed_ro": true,
  "headers": [{"name":"X-Private-Service-Token","value_from":"jira.credential"}],
  "tools": [{
    "name": "read_issue",
    "capability": "jira.issue.field",
    "input_schema_sha256": "<64 lowercase hex bytes>",
    "input_schema_sha256_alternates": ["<optional reviewed variant>"],
    "max_invocations": 1,
    "allowed_arguments": [{"key":"PROJ-1"}]
  }],
  "max_request_bytes": 1048576,
  "max_response_bytes": 1048576,
  "max_total_response_bytes": 4194304,
  "max_concurrent": 1,
  "timeout_seconds": 60
}
```

Dry-run structurally validates the complete profile and its scenario budgets
without reading credentials or contacting the upstream. Before a real model
run, the parent performs only initialize, initialized, and tools/list preflight
and independently requires read-only, non-destructive selected tools. Selected
tool names and schemas are necessarily visible to the model, but the generated
provider connection contains only a loopback URL and disposable capability:
the upstream origin and credentials remain parent-only. Unknown methods,
hidden tools,
argument drift, exhausted calls, redirects, oversized responses, credential
echoes, malformed or mismatched JSON-RPC responses, or incomplete audit fail
closed. JSON/SSE responses are matched to the request id, decoded canaries are
blocked before delivery, and ambiguous `tools/call` responses are never
replayed. A bounded preflight covers the catalog, the local hop bypasses ambient
HTTP proxies, cancellation reaches the active request, and upstream sessions
are closed when supported. Results use `backend_observation:"opaque-mcp"` and
`safety_assurance:"reviewed-ro-mcp-interface"`; internal HTTP request, method,
duplicate, and write coverage is deliberately unavailable.
Therefore a comparison set containing `external-mcp` must not list
`backend_requests`, `duplicate_backend_requests`, or `remote_writes` as
`required_metrics`. Keep them as observed diagnostics for CLI/atl-MCP; the
external surface proves safety through its reviewed interface assurance rather
than fabricating zero backend traffic. Run-spec and comparison-set validation
enforce this before any run.

Then dry-run both, inspect both plans, and perform exactly one supervised run
of each with the same built atl/plugin commit:

```sh
for spec in run.cli.codex.json run.atl-mcp.codex.json; do
  /tmp/agent-eval run \
    --spec "$ATL_PRIVATE_EVAL_CASE/$spec" \
    --output-root /tmp/atl-private-live-runs \
    --repository-root . \
    --agent-binary "$(command -v codex)" \
    --atl-binary "$PWD/atl" \
    --plugin-root . \
    --live-config-dir "$HOME/.config/atl-private" \
    --dry-run
done
```

Preview the third surface separately so its private profile is explicit:

```sh
/tmp/agent-eval run \
  --spec "$ATL_PRIVATE_EVAL_CASE/run.external-mcp.codex.json" \
  --output-root /tmp/atl-private-live-runs \
  --repository-root . \
  --agent-binary "$(command -v codex)" \
  --atl-binary "$PWD/atl" \
  --plugin-root . \
  --live-config-dir "$HOME/.config/atl-private" \
  --external-mcp-profile "$ATL_PRIVATE_EXTERNAL_MCP_PROFILE" \
  --dry-run
```

Remove `--dry-run` only after both previews and source config permissions pass.
Do not run surfaces concurrently: sequential execution keeps backend load
bounded and makes duplicate/request comparisons interpretable. Abort on a
hook/gateway denial, a non-GET/HEAD observation, or an incomplete audit. A
failed deterministic oracle, shim denial, or declared budget violation makes
the pair non-passing, but the other pre-approved transport may still run once
to localize the regression; never relax either side after seeing a result.

For every result with a valid structured final answer, use the review policy
bound into its private plan. A legacy singleton uses one exact human or model
reviewer identity. A panel uses all three or five predeclared reviewers, with
every packet prepared before the first assessment and one median consensus
written only after the last assessment. Each reviewer receives the private
final answer as untrusted data and no tools. Compare:

- deterministic status and every required check;
- qualitative score and finding classes;
- source completeness/warnings in the structured answer;
- agent turns, tool/atl calls and denials;
- total and main-thread input/output tokens plus estimated cost;
- backend GET/HEAD requests, duplicates and returned/output bytes;
- privacy-safe per-family invocation/failure/output-byte attribution when
  `coverage.capability_families` is true;
- duration under the same provider/model/reasoning/runtime identity.

Raw transcripts, answers, review rationales, route labels and per-run files stay
private. Review artifacts retain only bounded scores and identifiers plus
content digests; they never retain excerpts or free-form rationale. A public
result may state only generic task class, exact public runtime versions/commit,
transport names and privacy-reviewed aggregate numbers. One
supervised live block is compatibility evidence, not a statistically stable
model-quality benchmark; accumulate Latin-square blocks and use repeated
synthetic cells for product decisions.

## Public/private boundary

Real page and issue content is **private**. Store raw transcripts in a
maintainer-selected Git-ignored root with mode `0600`; a runner must refuse a
transcript root that Git does not ignore. Private scenario mappings stay in the
same untracked boundary, while public scenarios use placeholders.

What gets published is aggregates only: success rates, p50/p90
turns/cost/duration, totals, and generic class labels ("table-heavy page",
"configured fixture"). Published tables should name the variant, repetitions,
exact runtime identity, and what changed between variants. Before publishing,
scan the aggregate diff with the repository private-marker patterns.
