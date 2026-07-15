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
- **Deterministic oracles.** Every task has a programmatic pass/fail check on
  the produced artifact (the resulting CSF bytes for edit tasks, the JSON
  answer for read tasks). No human judging, no LLM judging.
- **Paired variants, one variable.** Variants differ in exactly one thing —
  the guidance text (skill/tips) or the tool surface available — everything
  else (model, fixtures, prompts, oracle) held fixed. A variant's result is
  meaningless except against its pair.
- **Medians over repetitions.** ≥3 repetitions per cell; single runs swing
  ±50% on cost. Report medians for turns/cost/duration, sums for totals, and
  success as n/N.
- **Task classes.** Fixtures are real pages spanning the shapes that stress
  different code paths: text-heavy, macro-heavy, and table-heavy bodies, with
  both edit and read tasks. Per-class breakdowns matter more than the overall
  median — regressions hide in classes.
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

## Evaluation layers

The evaluation stack has distinct safety and cost properties:

1. **Static CI gates** validate skill examples, access classification, scenario
   schemas, output contracts, and context budgets without a model or backend.
2. **Deterministic workflows** execute real `atl` commands against synthetic
   Jira/Confluence servers and enforce method, request, byte, completeness, and
   mutation budgets.
3. **Synthetic model runs** let Claude Code or Codex choose commands against a
   deterministic local backend. Deterministic oracles score the final result
   and trajectory; no LLM judge is required.
4. **Supervised live runs** are local, read-only compatibility checks against a
   configured private backend. They never run in public CI and publish only
   aggregate measurements.

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
    "max_backend_requests": 8,
    "max_remote_writes": 0,
    "max_output_bytes": 8192,
    "max_input_tokens": 0,
    "max_output_tokens": 0,
    "max_estimated_cost_microusd": 0,
    "max_duration_millis": 0,
    "allowed_http_methods": ["GET"]
  }
}
```

Observations and results contain aggregate trajectory data only. The contract
has no fields for prompts, commands, HTTP paths, backend URLs, or response
bodies. Validate the committed scenarios and deterministic workflows with:

```sh
make agent-eval-contract
```

The maintainer tool can validate scenario files, evaluate one aggregate
observation, and combine comparable result files into p50/p90 groups:

```sh
go run ./scripts/agent-eval validate internal/cli/testdata/agent-eval/*.json
go run ./scripts/agent-eval evaluate scenario.json observation.json >result.json
go run ./scripts/agent-eval aggregate runs/*.result.json >aggregate.json
```

Aggregation separates providers, exact models, agent versions, variants,
`atl` versions, plugin versions, and skill digests. Compare baseline and
candidate within one such runtime group; do not compare raw turns or dollar
estimates across providers.

Every observation also carries per-metric `coverage`. An observed zero is
different from an unavailable metric: a required metric without coverage fails
with `metric_not_observed`, while aggregation reports `observed_runs` before
p50/p90. In particular, a live run that cannot safely count backend methods
must not report an empty method map as measured zero traffic.

## Headless synthetic runner

Committed run specs bind one scenario to an exact provider/model, prompt,
structured response schema, deterministic mock fixture, oracle checks,
repetitions, timeout, and a whole-run USD-equivalent cap. Review the provider
command without contacting a model:

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

The runner creates a fresh private workspace per repetition. Claude Code loads
the repository plugin explicitly and receives an `atl`-only Bash allow-rule.
Codex gets the same generated skills in `.agents/skills` and runs with an
ephemeral session, ignored user config, and read-only filesystem sandbox;
Claude loads project settings only. Both inherit
`ATL_READ_ONLY=1`, `ATL_NO_UPDATE=1`, synthetic loopback backend URLs/tokens,
and an `atl` proxy that counts invocations and stdout bytes without retaining
command arguments in the result contract. The synthetic subprocess `PATH`
contains only that proxy, reducing accidental use of `curl`, `jq`, or unrelated
shell helpers; absolute executable paths remain a provider limitation and are
one reason live model runs are not enabled. Codex tool subprocesses additionally
receive an explicit environment allowlist, so ambient API keys and tokens are
not inherited by model-generated shell commands.

Raw provider JSONL, stderr, final structured output, invocation records, and
per-run results are mode `0600`; directories are mode `0700`. A destination
inside the repository is rejected unless `git check-ignore` proves it ignored.
Prompts/workspaces cannot escape the run-spec directory through `..` or
symlinks. The public synthetic case defaults to three repetitions and a maximum
USD-equivalent cost of $10 for the complete run spec. The runner divides that
cap across repetitions for the provider invocation and stops remaining runs if
the measured total exhausts it. `--repetitions 1` may reduce, but never increase,
the reviewed repetition count.

Codex currently has no runner-level shell command allowlist equivalent to
Claude's `--allowed-tools`. Therefore the headless Codex path is restricted to
synthetic credentials/backends. Corporate model-in-the-loop runs remain
disabled until the agent can access the backend through an isolated typed tool
surface; supervised corporate checks below stay agentless and read-only.

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

Track total tokens and cost across all threads, main-thread context bytes,
duplicate backend requests, wall time, and evidence lost during summarization.
Do not delegate simple one-object reads or parallel remote writes.

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
the provider CLI. The runner deliberately drops API-key and unrelated
credential environment variables instead of exposing the caller's ambient
secrets to the agent process.

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
