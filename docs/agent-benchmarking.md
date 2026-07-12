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

- **Real headless runs.** Each data point is a fresh, non-interactive agent
  session (`claude -p`, fixed model) given the same task prompt and a working
  directory containing the fixture. No mocking, no replay: the agent really
  reads files, really runs `atl`, really burns turns on whatever friction
  exists.
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

## Why not CI

Deliberately manual: runs cost real money, need live API keys, and are
nondeterministic run-to-run (hence medians). The value is in before/after
comparisons around a specific change, not in a continuously green badge.

Deterministic **contract budgets** are different and do belong in CI. They do
not score an LLM; they pin the tool surface that the measured agent relies on.
`TestEvidenceFirstEpicWorkflowBudget` runs the first-use epic workflow against a
synthetic backend and fails if it exceeds seven read-only requests, 8 KiB of
combined JSON context, or omits explicit completeness for any default source:

```sh
go test ./internal/cli -run TestEvidenceFirstEpicWorkflowBudget -v
```

The budget is intentionally an upper bound: fewer calls/bytes are improvements,
while added evidence must justify changing the bound in review.

## Supervised live read-only check

Use a configured private epic only to confirm backend compatibility. Keep all
outputs outside the repository and publish only aggregate counts:

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

## Public/private boundary

The fixtures are real page content and are **private**; the harness and raw
transcripts live outside this repository. What gets published is aggregates
only: success rates, median turns/cost/duration, totals, and generic class
labels ("table-heavy page", "configured fixture"). Published tables should
name the variant, the cell count, and what changed between variants — enough
to interpret the numbers without the private context.
