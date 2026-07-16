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
| Single-object evidence | Broad reads, missed custom fields, unqualified absence | Jira epic evidence | Jira epic evidence |
| Bounded page evidence | Full-page context inflation, lost table meaning | Confluence section recovery and approved-occurrence route | Confluence page evidence via CLI and typed MCP |
| Ambiguity recovery | Duplicate headings or identities silently select the wrong source | Confluence duplicate-heading refusal/recovery | Confluence page evidence |
| Portfolio snapshot | Repeated JQL/joins replace a curated membership source | Jira board/Structure route | Jira quarterly portfolio |
| Multi-source synthesis | Conflicts, stale evidence, or summaries lose provenance | Fifteen-GET mixed portfolio and six-GET Confluence brief | Jira quarterly portfolio and Confluence decision brief |
| Hostile embedded content | Page/issue prose attempts to redirect tool use | Guard and zero-write route checks | Jira injection and both Confluence families |
| Context isolation | Delegation duplicates reads or loses evidence in summarization | Delegation/request budgets | Single-agent versus one-child portfolio and Confluence brief |
| Durable mirror review | Native/derived drift and context-heavy byte inspection | Pull/status/diff/plan tests | Not yet model-measured |
| Guarded edit planning | A preview weakens the read-only boundary or a write escapes review | Synthetic write-path and access-policy tests | Intentionally excluded from the read-only model runner |

The default suite therefore contains small navigation, medium single-object,
and longer synthesis cells. Add a new cell when a product change introduces a
materially different reasoning shape; do not add one merely to exercise another
flag. Model-run prompts must request a user outcome rather than prescribe every
command, except when the experiment intentionally holds the route fixed.

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
   against that backend through typed read-only MCP only. They are explicit,
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

Observations and unreviewed results contain aggregate trajectory data only. The contract
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

Every observation also carries per-metric `coverage`. An observed zero is
different from an unavailable metric: a required metric without coverage fails
with `metric_not_observed`, while aggregation reports `observed_runs` before
p50/p90. In particular, a live run that cannot safely count backend methods
must not report an empty method map as measured zero traffic.

## Headless synthetic runner

Committed run specs bind one scenario to an exact provider/model, prompt,
structured response schema, deterministic mock fixture, oracle checks, reviewed
CLI command prefixes or typed MCP tool names, repetitions, timeout, and a whole-run
USD-equivalent cap. Review the provider command without contacting a model:

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

The runner creates a fresh private workspace per repetition. Claude Code CLI
runs load the repository plugin explicitly and receive an `atl`-only Bash
allow-rule plus a `PreToolUse` guard. The guard accepts one command per Bash call, optionally
preceded by the exact `export ATL_READ_ONLY=1`, and only when it matches a
run-spec `allowed_atl_commands` prefix. Shell operators, substitutions,
redirections, multiline scripts, and unrelated binaries are denied before Bash.
Codex gets the same generated skills in `.agents/skills` for a reviewable
ephemeral read-only command preview. Both providers support MCP transport.
Claude receives a generated mode-0600 config under `--strict-mcp-config`, no
shell/file/delegation tools, and only qualified `mcp__atl__...` names. Its synthetic
backend environment is attached to the MCP child and omitted from the provider
environment. Codex starts the same exact reviewed `atl mcp serve` binary,
enables only `allowed_mcp_tools`, disables web search, removes atl credentials
from the model shell environment, and denies shell/file/patch/delegation tools
through `PreToolUse`. CLI-transport Codex specs remain validate/dry-run only
because its OS sandbox cannot safely reach the host-side mock. Supported model runs inherit
`ATL_READ_ONLY=1`, `ATL_NO_UPDATE=1`, and synthetic loopback backend URLs/tokens.
CLI runs use an `atl` proxy that counts invocations and stdout bytes without retaining
command arguments; MCP runs count completed typed calls/failures and result bytes
from the provider event stream. Proxy counters, config, and mirror
state are writable only below the private run workspace. The runner requests a
proxy-only subprocess `PATH`, but provider shells may expose system helpers;
the `PreToolUse` guard is therefore the authoritative command boundary rather
than a PATH assumption. The hook allowlist and `ATL_READ_ONLY=1` remain
independent: the hook limits which CLI reads the model may request, while the
CLI policy rejects every mutating command even if a prefix were configured too
broadly. Delegated variants also place `Agent` behind the hook: atomic private
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

Claude may emit a client-side `No such tool available` while its explicit MCP
server is still becoming visible. The runner counts that as a model tool call,
so its context/turn cost remains observable, but not as an `atl` invocation
because no protocol request reached the server. An object-shaped MCP response,
including an error response, is counted as an invocation; server errors fail
the `atl_all_succeeded` oracle.

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
the provider CLI. The runner deliberately drops API-key and unrelated
credential environment variables instead of exposing the caller's ambient
secrets to the agent process.

## Private-live model-in-the-loop check

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
  "schema_version": 2,
  "backend_mode": "private-live",
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
  "checks": [
    {"name":"atl_succeeded","kind":"atl_all_succeeded"},
    {"name":"guard_clean","kind":"guard_no_denials"},
    {"name":"http_observed","kind":"http_methods_observed"},
    {"name":"no_delegation","kind":"delegations_none"},
    {"name":"used_atl","kind":"atl_invocations_min","minimum":1},
    {"name":"complete","kind":"json_equals","pointer":"/complete","expected":true}
  ]
}
```

Its private scenario must use `data_class:"private-local"`, exactly one
repetition, zero delegations and writes, positive invocation/request limits,
and an explicit `allowed_http_methods` containing only `GET`/`HEAD`. Start with
the smallest MCP tool set and response schema that can answer the user task.
Expected private facts may live in the ignored run spec; never copy them into a
public fixture or PR.

Review without invoking the model or backend, then run once:

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
separate child config containing only loopback URLs and disposable ingress
capabilities.

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
shipped `Skill` surfaces and loads no ambient setting sources. Codex runs
model-generated commands in `workspace-write` with approvals and web search
disabled; command networking is enabled only together with the built-in
workspace network permission. The managed network proxy is disabled because
its isolated loopback cannot reach the parent gateway; instead the hook permits
only confined readers and syntactically safe `atl` blocks, the shim enforces
exact argv/counts, and the loopback gateway is the only backend credential and
route boundary. Its subprocess
environment excludes source URLs/PATs and ambient proxy variables. The
provider process itself keeps its normal subscription-authenticated model
connection; these restrictions apply to commands spawned by the model.

Run `--dry-run` first, inspect the provider plan and local private spec, then
remove that flag for the single supervised execution. Use the same task,
response schema, rubric, and evidence scope for a paired typed-MCP run.

### Paired CLI and MCP comparison

A transport comparison is valid only when the task and evaluation contract are
identical. Keep both specs in one private case directory and use a
transport-neutral prompt: say “use the available atl interface”, not “call this
MCP tool” or “run this shell command”. Variants should identify only the
surface, for example `cli-skill` and `typed-mcp`.

Preflight the pair before either model invocation:

```sh
/tmp/agent-eval validate-pair \
  "$ATL_PRIVATE_EVAL_CASE/run.cli.codex.json" \
  "$ATL_PRIVATE_EVAL_CASE/run.mcp.codex.json"
```

The validator requires one private CLI and one private MCP spec from the same
directory. Provider, model, reasoning, scenario, prompt bytes, response schema,
rubric, workspace, run checks, repetitions, timeout, pricing and cost cap must
match. It intentionally ignores transport-specific tool, command, route and
gateway policies. Success reports only the provider and generic `cli`/`mcp`
transports, not the private scenario identity.

Then dry-run both, inspect both plans, and perform exactly one supervised run
of each with the same built atl/plugin commit:

```sh
for spec in run.cli.codex.json run.mcp.codex.json; do
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

Remove `--dry-run` only after both previews and source config permissions pass.
Do not run transports concurrently: sequential execution keeps backend load
bounded and makes duplicate/request comparisons interpretable. Abort on a
hook/gateway denial, a non-GET/HEAD observation, or an incomplete audit. A
failed deterministic oracle, shim denial, or declared budget violation makes
the pair non-passing, but the other pre-approved transport may still run once
to localize the regression; never relax either side after seeing a result.

For every result with a valid structured final answer, create a review template
from the same rubric and use
the same human or separately prompted model reviewer identity. The reviewer
receives the private final answer as untrusted data and no tools. Compare:

- deterministic status and every required check;
- qualitative score and finding classes;
- source completeness/warnings in the structured answer;
- agent turns, tool/atl calls and denials;
- total and main-thread input/output tokens plus estimated cost;
- backend GET/HEAD requests, duplicates and returned/output bytes;
- duration under the same provider/model/reasoning/runtime identity.

Raw transcripts, answers, review rationales, route labels and per-run files stay
private. A public result may state only generic task class, exact public runtime
versions/commit, transport names and privacy-reviewed aggregate numbers. One
supervised live pair is compatibility evidence, not a statistically stable
model-quality benchmark; use repeated synthetic cells for that purpose.

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
