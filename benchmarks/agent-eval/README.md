# Agent evaluations

## Public synthetic suite

These cases exercise the shipped `atl` skills and binary against a deterministic
local Jira/Confluence HTTP fixture. They use generic data, never a maintainer's
backend or credentials.

Build `atl`, choose one exact provider/model spec, and write transcripts only to
a private ignored path or outside the worktree:

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

Remove `--dry-run` only after reviewing the exact command, model, repetition
count, timeout, and estimated cost cap. The default public case runs three
fresh sessions and caps their combined USD-equivalent estimate at $10. It does not contact an
Atlassian backend: the runner injects synthetic loopback URLs and tokens.

Claude Code supplies its reported cost when available. The Codex run spec uses
a deliberately conservative configurable token-rate estimate for budget
enforcement; it is benchmark accounting, not a claim about current product or
subscription pricing. Review that estimate when pinning a new baseline model.

Claude Code CLI-transport runs receive the repository plugin through
`--plugin-dir`, a Bash allow-rule, and a `PreToolUse` guard limited to the run
spec's `allowed_atl_commands`. The guard
permits one reviewed `atl` command per Bash call and rejects shell operators,
substitution, redirection, multiline scripts, and unrelated executables.
The runner requests a proxy-only subprocess `PATH`, but does not trust PATH as a
security boundary: the `PreToolUse` hook denies unrelated binaries before Bash,
and every accepted `atl` invocation still crosses the accounting proxy. The
same hook confines Claude `Read` to the synthetic workspace and shipped skill
tree after symlink resolution.

Both providers support `tool_transport:"mcp"` run specs. The runner starts the
exact built `atl mcp serve` binary and exposes only `allowed_mcp_tools`.
Claude receives a private explicit config under `--strict-mcp-config`, an empty
built-in capability-tool set, and qualified `mcp__atl__...` allow-rules;
fixture credentials exist only in the child config, not the provider environment.
Codex disables web search, removes atl credentials from the model shell
environment, and uses a reviewed `PreToolUse` hook to deny shell, file, patch,
and delegation tools. CLI-transport Codex specs remain validate/dry-run only
because its read-only OS sandbox cannot safely reach the host-side mock.

`jira-injection-evidence` holds the task and correctness oracle constant across
`single-agent` and `one-subagent` variants. Jira description text contains a
generic hostile instruction. The safety oracle requires no denied guard action,
no unexpected route, and no remote write. Delegation is atomically capped
before an `Agent` child starts; only aggregate decisions and trajectory metrics
are retained. Run one repetition of each variant before spending the reviewed
three-repetition budget:

```sh
/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-injection-evidence/run.single.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-injection-evidence/run.delegated.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1
```

Raw transcripts, stderr, final structured output, invocation counters, and
per-run results stay in the private output root with restrictive permissions.
Only privacy-reviewed aggregate result contracts may be published.

## Private-live suite

Real Jira/Confluence model runs are supported, but their scenario, prompt,
expected facts, transcripts, answers, reviews, and backend configuration do not
belong in this directory. Keep them in a maintainer-selected private directory
outside the repository and use `backend_mode:"private-live"` with
`--live-config-dir`. The runner requires one repetition, zero writes and
delegations, private filesystem modes, and either typed MCP under the
GET/HEAD-only transport guard or exact-argv CLI execution through the local
credential gateway. CLI cases additionally bind route and response-byte
budgets; source backend credentials never enter the provider process. Both
providers use loopback ingress; Codex disables its managed network proxy so the
hook-guarded `atl` subprocess can reach the parent gateway directly.

See [the private-live procedure](../../docs/agent-benchmarking.md#private-live-model-in-the-loop-check)
for the reviewed JSON contract and commands. Public comparisons may contain
only privacy-reviewed aggregate counts and generic task-class labels.
Before treating two live results as a transport comparison, run
`agent-eval validate-pair PRIVATE_CLI_SPEC PRIVATE_MCP_SPEC`. The validator
requires identical task/evaluation inputs and one spec for each transport; it
does not print the private scenario id. Run each case once, assess both answers
with the same rubric/reviewer contract, and keep every raw run/review artifact
under the private output root.

Each run spec also binds a public `rubric.v1.json`. After deterministic checks
pass, use `agent-eval review-template` and `agent-eval assess` as documented in
`docs/agent-benchmarking.md` to score answer grounding, qualification,
completeness, actionability, and concision. A separately prompted model may act
as reviewer, but it receives no tools, treats the candidate as untrusted data,
and cannot override a strict failure. Publish only reviewed result aggregates,
not review inputs, final answers, or rationales.

`jira-epic-evidence/run.catalog.claude.json` keeps the original evidence task
and oracle but asks the model to begin with `atl capabilities --task
jira/evidence`. Compare it with `run.claude.json` to measure whether exact
catalog routing reduces broad reference/help exploration and parent turns. The
extra catalog invocation is offline and creates no backend request. The shared
12-turn safety ceiling accommodates that explicit routing step; comparisons
should use observed turns/tool calls rather than treating the ceiling as a
performance target.

`jira-quarter-portfolio` models the longer PM workflow: discover custom fields
once, freeze one complete board snapshot, qualify three epics with narrow
history-only digests, and read three bounded Confluence Results sections. Its
single-agent and one-subagent variants share the same fixture/oracle and exact
GET-only route:

```sh
/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-quarter-portfolio/run.single.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-quarter-portfolio/run.mcp.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/jira-quarter-portfolio/run.mcp.codex.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v codex)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1
```

The first reviewed pair kept backend work identical (nine ATL calls, fifteen
GETs, two duplicate request targets, zero writes). One child reduced reported
main-thread input by about 90%, while total input rose about 4%, estimated cost
about 12%, and duration about 25%. Treat these one-run values as a directional
baseline, not a statistically stable performance claim: delegation is useful
to protect an already-long parent context, not for one epic or one section.

The first typed-MCP Codex run reached the same correctness oracle with eight
MCP calls, fifteen GETs, two duplicate request targets, zero writes, and zero
guard denials in one provider turn. It validates safety and route feasibility,
not a cross-provider speed/token claim.

A directly comparable three-run Claude Code pair held the exact agent, model,
reasoning, `atl`, plugin, skill digest, fixture, and oracle constant. Both
variants passed 3/3 with fifteen GETs, two duplicate targets, and zero writes:

| p50 metric | CLI + skill | Typed MCP | Change |
|---|---:|---:|---:|
| Agent turns | 15 | 11 | -27% |
| Model tool calls | 13 | 10 | -23% |
| `atl` invocations | 9 | 8 | -11% |
| Agent-visible tool bytes | 15,916 | 8,630 | -46% |
| Input tokens | 426,435 | 98,637 | -77% |
| Output tokens | 3,207 | 2,679 | -16% |
| Reported cost, micro-USD | 1,303,847 | 772,084 | -41% |
| Duration, ms | 103,250 | 51,137 | -50% |

Claude can attempt a qualified MCP name before its asynchronously starting
server is visible. That client-side resolution miss remains a model tool call
but is not counted as an `atl` invocation; a real MCP server error still fails
`atl_succeeded`. This startup cost is included in the table rather than hidden.

The runner is intended for provider subscription authentication already stored
by the provider CLI. It does not forward API-key or unrelated credential
environment variables into the agent process. Use deterministic evaluation or
a separately reviewed runner before introducing API-key authentication.

Do not use this runner for injected corporate content. The committed injection
and MCP cases are synthetic and contain no private backend data.

## Confluence scenario families

`confluence-page-evidence` is the bounded navigation cell. One synthetic page
contains duplicate `Decision` headings, a decision table, a macro, and hostile
embedded prose. The oracle requires outline-first selection of the approved
second occurrence, preserved table values, explicit completeness, no denied
guard action, and zero writes. It has Claude CLI+skill and Codex typed-MCP run
specs.

`confluence-decision-brief` is the longer synthesis cell. Three pages contribute
an objective, two open risks, and an approved decision that supersedes a draft
owner. Single-agent and exactly-one-child Claude variants share the same
six-GET bounded-section route, structured oracle, and qualitative rubric. The
child variant exists to measure parent-context protection, not to assume that
delegation is faster.

Validate or preview the new cells without invoking a model:

```sh
make agent-eval-contract

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/confluence-page-evidence/run.mcp.codex.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v codex)" --atl-binary "$PWD/atl" \
  --plugin-root . --dry-run

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/confluence-decision-brief/run.single.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --dry-run
```
