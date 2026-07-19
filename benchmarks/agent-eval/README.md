# Agent evaluations

## Comparison categories

`neutral-common` scenarios compare surfaces with one byte-identical
outcome-driven prompt and response schema plus semantically identical JSON
scenario, rubric, fixture, budget, and semantic oracle contracts. Each run
declares the same sorted semantic `data_capabilities`; validation derives those
grants from typed tools or CLI routes so a richer interface cannot enter a
neutral cohort unnoticed. Prompts must not name commands, tools, or a preferred route. They also
declare non-empty `required_semantic_checks`, use a positive generic interface
budget and the required `interface_invocations` metric, and cannot use
transport-specific `atl_*` check aliases. Run-spec loading rejects qualified
MCP names, exact `atl jira`/`atl conf` routes, and typed Jira/Confluence tool
spellings in a neutral core prompt while allowing ordinary product prose.
`surface-native` scenarios measure realistic capabilities that may be
unsupported elsewhere. `route-fixed` scenarios pin an execution route to catch
contract regressions and must not be used as a general surface ranking. The
existing committed cases are explicitly route-fixed; missing category fields
in older private cases default to that safer interpretation.

Results distinguish `cli-skill`, `atl-mcp`, and the private-live-only
`external-mcp` surface. Old results without a surface remain
`legacy-unspecified`. Generic
comparison contracts use `interface_invocations` and `interface_*` checks;
legacy `atl_invocations` fields continue to validate.

Observations classify runs as `supported`, `unsupported-capability`, or
`invalidated-backend-drift`. Ineligible runs retain safety and budget failures
but are excluded from task pass/fail. Aggregate schema v4 reports eligibility
counts and coverage, conditional success, and computes neutral/surface-native
efficiency and qualitative summaries only from supported deterministically
valid runs. Missing eligibility in older observations means `supported`.

Private `external-mcp` execution requires `--external-mcp-profile`. The
owner-only profile and its directory stay outside the repository. Header
values are bindings to the existing private ATL config, never literals. Dry-run
validates the profile structure and scenario caps without reading credentials
or contacting the upstream. Catalog identity canonicalizes each tool and sorts
by tool name; harmless response reordering is accepted, while additions,
removals, duplicates, and content drift are rejected. A profile may pin at most
seven additional exact catalog and per-tool input-schema digests when every
variant was reviewed; unlisted variants remain blocked. The full catalog pin
already includes each schema, so independently invented cross-combinations
cannot pass. The model connects only to a disposable loopback
policy proxy; selected tool identities are visible, while the upstream origin
and credentials are not. Because the external server's Atlassian HTTP hop is
opaque, backend request, duplicate,
method, and remote-write coverage is unavailable rather than reported as zero.
Comparison sets containing that surface cannot require backend request,
duplicate, or write metrics; the validator rejects that false common oracle.
Optional MCP read/destructive annotations strengthen that review when present:
an explicit unsafe hint is always rejected. Their absence is accepted only
under the owner-only `reviewed_ro` assertion plus exact catalog/schema/tool,
read-capability, argument, and invocation bindings.
Client-supplied protocol `_meta` is accepted as an envelope compatibility field
but stripped before the upstream hop; it cannot alter reviewed tool arguments.

Use at least three fresh synthetic repetitions per surface. For three-surface
blocks rotate order as `ABC`, `BCA`, `CAB`, compare efficiency only among
deterministically correct answers, review answers blind to surface identity,
and report per-class macro summaries plus a Pareto view rather than one total
score.

## Public synthetic suite

These cases exercise the shipped `atl` skills and binary against a deterministic
local Jira/Confluence HTTP fixture. They use generic data, never a maintainer's
backend or credentials.

Validate and inventory the complete corpus before spending model budget:

```sh
go run ./scripts/agent-eval inventory benchmarks/agent-eval
make agent-eval-contract
```

The inventory output is aggregate-only. Neutral common cohorts must contain
two or three unique surfaces with byte-identical prompts/schemas and matching
semantic task, oracle, and data-capability contracts;
surface-native cases are reported separately rather than scored as failures on
interfaces that do not expose the capability.

Run-spec schema v3 adds mandatory `data_capabilities` for neutral-common
comparisons. To migrate an owner-only v2 spec, set `schema_version` to `3`, add
the same sorted semantic capability set to every compared surface, then run
`validate-comparison-set` before any model or backend is contacted. Other
categories still need the version bump but do not declare comparison
capabilities.

The realistic matrix currently contains:

| Category | Scenario | Main stressor |
| --- | --- | --- |
| neutral common | `jira-deep-epic-evidence` | clipped custom narrative, stale children, blockers, and incomplete comments |
| neutral common | `jira-ordered-batch-read` | selector order, duplicates, omitted identity, and CLI batch versus typed search efficiency |
| neutral common | `jira-board-neutral-portfolio` | board membership plus current, stale, and incomplete per-epic evidence |
| neutral common | `confluence-long-decision` | long rich page, repeated heading, and superseded evidence |
| neutral common | `cross-service-neutral-discovery` | bounded topic discovery across Jira and Confluence with distractors |
| surface native | `jira-structure-subtree-export` | GET-only hierarchy rows plus ordered explicit batch export |
| surface native | `confluence-table-analytics` | selected multi-table extraction, merged cells, links, and safe CSV |
| surface native | `confluence-mirror-review` | offline semantic diff and snapshot-delta review |
| surface native | `jira-field-mutation` | reviewed preview/apply and ambiguous-outcome handling on a synthetic backend |
| surface native | `confluence-plan-mutation` | guarded plan preview/apply, conflict, and ambiguous outcome on a synthetic backend |

Injection, point-route, and delegation cases remain route-fixed regressions.
Surface-native mutation and mirror cases are scored only for their supported
CLI workflow and are not used to claim a general surface winner.

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
permits a bounded block of reviewed `atl` commands separated by newlines or
the exact list operators `;`/`&&`. It also
permits only the exact safety preflight statements `export ATL_READ_ONLY=1`
(first line) and `command -v atl`, because the shipped skill uses them before
its first read. Every `atl` line is matched independently and crosses the
accounting proxy. Other exports, operators, substitution, redirection, and
unrelated executables are rejected.
The runner requests a proxy-only subprocess `PATH`, but does not trust PATH as a
security boundary: the `PreToolUse` hook denies unrelated binaries before Bash,
and every accepted `atl` invocation still crosses the accounting proxy. The
same hook confines Claude `Read` to the synthetic workspace and shipped skill
tree after symlink resolution.

Both providers support `tool_transport:"mcp"` run specs. The runner starts the
exact built `atl mcp serve` binary and grants execution only to
`allowed_mcp_tools`. Claude receives a private explicit config under
`--strict-mcp-config`, exact qualified `mcp__atl__...` permission rules, and a
global pre-tool guard that allows only those reviewed MCP names plus required
structured output; every built-in fallback is denied.
Neutral-common specs additionally declare a sorted `data_capabilities` set.
The corpus validator derives that set from built-in CLI or typed-MCP routes,
while private external-MCP runs bind it to the reviewed capability family in
the owner-only profile.
fixture credentials exist only in the child config, not the provider environment.
Codex disables web search, removes atl credentials from the model shell
environment, and uses a reviewed `PreToolUse` hook to deny shell, file, patch,
and delegation tools. It also disables ambient `AGENTS.md` discovery so
machine-local instructions cannot change a comparable run; reviewed prompts
and copied shipped skills remain available. Synthetic CLI-transport Codex specs remain
validate/dry-run only because its read-only OS sandbox cannot safely reach the
host-side mock; private-live Codex CLI specs use the separately documented
zero-network command broker confinement.

Synthetic CLI specs inherit `ATL_READ_ONLY=1`. The exceptional
`allow_synthetic_writes:true` mode is confined to Claude CLI runs against
loopback-only mock origins and requires exact HTTP method counts, no unexpected
mock request, a clean guard, an explicit scenario write budget, and a mutating
method allowlist. Mock routes can bind the exact semantic JSON request body.
They may also provide a bounded `responses` sequence for repeated reads of one
route. Only a request satisfying the route constraints consumes the next
response; exhausting the sequence is unexpected, so a hidden reconciliation
retry or write replay fails the mock oracle.
This permits write-path model evaluation without granting any route to a real
backend; private-live specs remain strictly GET/HEAD-only.

`jira-field-mutation` uses that boundary for one generic custom field. Its
preview-only variant exercises the dedicated GET-only `jira issue field
preview` under `ATL_READ_ONLY=1`. Reviewed apply uses the emitted timestamp and
proposal hash for exactly one accepted PUT. The ambiguous variant receives a
synthetic server error, performs the built-in reconciliation read, and must not
replay the write. All three variants require the exact `atl:jira` skill and
exact method counts; the write fixtures additionally reject any JSON body other
than the reviewed field/value pair.
The first reviewed Claude Code baseline passed 3/3 for preview, apply, and
ambiguous-no-replay; all nine answers scored 10,000 bps. Stable trajectory
medians were respectively `GET=2`, `GET=4/PUT=1`, and `GET=5/PUT=1`.

`confluence-plan-mutation` exercises the complete CLI+skill plan boundary on a
one-page native mirror. Preview creates a durable plan offline and performs one
GET under inherited `ATL_READ_ONLY=1`. The three approved variants perform the
same preview and exactly one semantic-body-checked PUT, then reconcile success,
a 409 version conflict, or an unavailable verification read. The response
proposal hash is compared with the generated workspace plan rather than merely
checked for presence; workspace-file oracles are synthetic-only and contained
under the copied run workspace.

The reviewed Claude Code baseline passed 3/3 in all four variants and every
answer scored 10,000 bps. Preview medians were five model tools, two atl
invocations, one GET, 69,577 input tokens, and 19.8 seconds. Apply, conflict,
and unknown medians were six tools, three atl invocations, three GETs plus one
PUT; their median input was 86,571–86,959 tokens and duration 22.1–26.4 seconds.
The exact method/body/hash, clean guard/mock, conflict classification, and
no-replay checks are the stable claims; provider token/cost/duration values are
observations for this pinned runtime.

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

`jira-clipped-field-evidence` pins a compact digest whose required decision
marker lies beyond the digest projection boundary. Its typed-MCP route permits
one explicitly complete catalog lookup, one compact digest, and one exact
bounded field expansion;
the three-invocation budget makes a repeated full digest unnecessary and
over-budget. The deterministic MCP test additionally verifies the exact
four-GET, zero-write trajectory with technical-id reuse and that the expansion alone recovers the
marker.

Result schema v4 and aggregate schema v4 retain the `capability_families`
contract introduced in v3, using a
closed generic vocabulary shared by CLI and MCP. Each entry contains only
invocations, successes, failures, and output bytes. Treat the section as
available only when `coverage.capability_families` is true; unknown events
suppress attribution rather than leaking or guessing a route. Token metrics
remain run-level because current provider streams do not assign token usage to
individual tools.

## Private-live suite

Real Jira/Confluence model runs are supported, but their scenario, prompt,
expected facts, transcripts, answers, reviews, and backend configuration do not
belong in this directory. Keep them in a maintainer-selected private directory
outside the repository and use `backend_mode:"private-live"` with
`--live-config-dir`. The runner requires one repetition, zero writes and
delegations, private filesystem modes, and either typed MCP under the
GET/HEAD-only transport guard or exact-argv CLI execution through the local
credential gateway. CLI cases additionally bind route and response-byte
budgets; source backend credentials never enter the provider process. Claude
Code uses loopback ingress. Codex keeps command networking disabled, grants
write access only to an owner-only broker request directory, and receives
results through a separate read-only response directory. A parent-side broker
revalidates exact argv and invocation budgets before running `atl` against the
disposable gateway. A pre-model probe verifies that direct loopback networking
is blocked and the broker is ready; gateway method/route/byte controls remain
independent mandatory layers.

See [the private-live procedure](../../docs/agent-benchmarking.md#private-live-model-in-the-loop-check)
for the transport/security contract and the
[private workspace runbook](../../docs/agent-benchmark-private-workspace.md)
for the recommended operator lifecycle. Public comparisons may contain
only privacy-reviewed aggregate counts and generic task-class labels.
Before treating two live results as a transport comparison, run
`agent-eval validate-pair PRIVATE_CLI_SPEC PRIVATE_MCP_SPEC`. The validator
requires identical task/evaluation inputs and one spec for each transport; it
does not print the private scenario id. Run each case once, assess both answers
with the same rubric/reviewer contract, and keep every raw run/review artifact
under the private output root.

For two or three surfaces, prefer
`agent-eval validate-comparison-set SPEC SPEC [SPEC]`. It requires unique
surface identities and identical category, provider/model, scenario and
budgets, core prompt bytes, schema, rubric, workspace, and semantic checks.
Mechanical guard/skill/invocation checks may differ only within a closed
classification. `validate-pair` remains the compatibility form for
`cli-skill` plus `atl-mcp` and still requires exact equality of the complete
check list. The broader comparison-set validator requires every scenario-named
semantic check to exist and remain semantic, so a guard or shape check cannot
silently replace the answer oracle. `external-mcp` additionally requires the
owner-only credential-isolation profile described in the private-live guide.

Each run spec also binds a public `rubric.v1.json`. In the marked private
workspace, use `agent-eval private review prepare` and `private review assess`
to create a fixed-layout, source-bound packet without discovering raw paths.
The generic `review-template` and `assess` commands remain the low-level form
for synthetic/framework work. Both score grounding, qualification,
completeness, actionability, and concision. A separately prompted model may act
as reviewer, but it receives no tools, treats the candidate as untrusted data,
and cannot override a strict failure. Publish only reviewed result aggregates,
not review inputs, final answers, or rationales.

`jira-epic-evidence/run.catalog.claude.json` keeps the original evidence task
and oracle but asks the model to begin with `atl capabilities --task
jira/evidence`. Compare it with `run.claude.json` to measure whether exact
catalog routing reduces broad reference/help exploration and parent turns. The
extra catalog invocation is offline and creates no backend request. The shared
12-turn safety ceiling accommodates that explicit routing step, while the
variant checks cap atl calls at three without the catalog and four with it,
including the optional configured-state preflight. A
help probe, separate value read, guessed-period retry, or second digest is
therefore a benchmark failure even when the final answer is correct.
Comparisons should use observed turns/tool calls rather than treating the
ceiling as a performance target.

The primary CLI variant also requires an exact observed `atl:jira` Skill event.
This is stronger than recording a plugin digest: loading another installed
skill does not satisfy its targeted `skill_invocations_min` check. In a
controlled Sonnet comparison, three independent attempts with the previous
513-line/33,384-byte Jira skill were all stopped by the command guard after the
skill directed or failed to prevent pipes, redirects, or invalid argument
shapes. The routed 140-line/8,014-byte core plus one direct evidence reference
passed 3/3 and all 11 checks with five model tools, exactly two atl reads, nine
GETs, complete sources, zero writes, and zero guard denials. A representative
answer scored 10,000 bps.

The strict old attempts abort before result accounting, so they have no valid
token/cost median. A separate pre-guard-oracle diagnostic old-skill sample
failed 0/3 and reported 225,587 median input tokens versus 84,808 for the final
candidate; treat that -62% as directional failure-path evidence. The
deterministic claim is the 76% reduction in always-loaded skill bytes and 51%
reduction in the complete core-plus-two-new-runbooks surface.

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

`confluence-mirror-review` is the offline durable-state cell. Its synthetic
mirror contains a semantic edit, a byte-only edit, an unchanged page, and
corrupt baseline evidence. The model can run only one `conf diff` and cannot
read raw mirror files. The expected exit-8 result is not waived:
`atl_failures_equals` requires exactly one failed invocation while answer
oracles require all four classifications and a blocked publish decision.
`skill_invocations_min` also proves that the installed Confluence skill was
actually loaded; prompt wording and a recorded plugin digest alone are not
enough. Any retry, backend request, write, delegation, missing skill load, or
guard denial fails the run.

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

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/confluence-mirror-review/run.cli.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1

/tmp/agent-eval run \
  --spec benchmarks/agent-eval/confluence-mirror-review/run.text.claude.json \
  --output-root "$ATL_AGENT_EVAL_OUTPUT" --repository-root . \
  --agent-binary "$(command -v claude)" --atl-binary "$PWD/atl" \
  --plugin-root . --repetitions 1
```

The current controlled Sonnet pair passed 3/3 runs and all 13 deterministic
checks for both projections, with one loaded skill, one `conf diff`, zero
backend requests, and zero writes. Reviewed representative answers scored
10,000 bps. Compact text reduced observed agent-visible tool output from
4,556 to 545 bytes in this controlled output root. The JSON count varies
slightly with canonical run-path length; compact text uses root-relative paths.
Median input tokens were 45,252 for JSON and 43,605 for text; both variants used
five turns and three model tool calls. Token, cost, and duration differences are
directional provider observations, not projection guarantees.

The same forced-skill compact-text cell also compared the previous
343-line/17,256-byte Confluence skill with the routed
120-line/6,226-byte body. Both passed 3/3 with identical turn/tool/atl counts;
median input tokens fell from 49,364 to 43,605 (-11.7%). The benchmark requires
the `Skill` event specifically because an earlier one-run projection comparison
allowed the text run to skip skill loading and therefore overstated its effect.

## Cross-service discovery family

`cross-service-topic-discovery` starts from a topic rather than a known Jira
key or Confluence id. The primary CLI + shipped `search-knowledge` skill and
the typed-MCP variant must search both services once, freeze complete candidate
pages, reject superseded and unrelated hits, then expand only one exact Jira
field and one bounded Confluence section. The six-GET deterministic oracle
rejects full-page interface outputs, mirrors, repeated searches, distractor expansion,
writes, delegation, and embedded-instruction compliance. It also measures the
generic `jira.issue.search` and `confluence.search` capability families.

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

The first reviewed Sonnet MCP baseline passed all 18 deterministic checks and
the qualitative rubric at 10,000 bps: five typed calls, five GETs, one duplicate
page target, zero writes, 2,889 tool-output bytes, 110,499 input tokens, 1,632
output tokens, 112,457 reported micro-USD, and 31,074 ms. The single run is a
directional route baseline, not a stable provider-performance estimate. The
five-GET path reused the system `description` field id directly; resolving a
display name may use the sixth allowed GET.

Against the earlier one-run Sonnet CLI baseline for the same fixture, prompt
contract, schema, rubric, and oracle, the directional measurements were:

| Metric | CLI + skill | Typed MCP | Change |
|---|---:|---:|---:|
| Agent turns | 10 | 7 | -30% |
| Model tool calls | 8 | 6 | -25% |
| `atl` invocations | 5 | 5 | 0% |
| Backend GETs | 6 | 5 | -17% |
| Agent-visible tool bytes | 3,001 | 2,889 | -4% |
| Input tokens | 133,410 | 110,499 | -17% |
| Output tokens | 1,795 | 1,632 | -9% |
| Reported cost, micro-USD | 203,533 | 112,457 | -45% |
| Duration, ms | 25,830 | 31,074 | +20% |

The result supports the smaller typed route and lower context/cost for this
cell, while the slower single MCP run is a reason to retain repetitions before
making any latency claim.
