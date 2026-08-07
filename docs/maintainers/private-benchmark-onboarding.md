# Private benchmark onboarding

This runbook helps a new ATL maintainer turn a fresh clone into an owner-local
Jira or Confluence evaluation dataset with Codex or Claude Code. It covers case
authoring and offline validation. The existing
[private evaluator lifecycle](../agent-benchmark-private-workspace.md) remains
the authority for consent-bound model execution, review, baseline promotion,
comparison, and retention.

Starting Codex or Claude Code is itself a provider interaction. The owner must
accept that provider's handling terms before using assisted authoring. This
bootstrap does not authorize disclosure of Jira or Confluence content to that
assistant, a candidate or reviewer benchmark run, a configured-backend read or
write, baseline promotion, pruning, or publication. Each is a separate
decision. Public synthetic benchmarks remain the default when they can prove
the claim.

## Intended outcome

A completed bootstrap has:

- one absolute owner-only private root outside the repository;
- one small read-only calibration case that represents a real recurring task;
- one distinct same-class holdout before the case becomes a regression gate;
- private scenario, prompt, response-schema, rubric, workspace, and run-spec
  files accepted by the current evaluator;
- an aggregate-only healthy `private doctor` result;
- a written authority record saying what is still **not** approved;
- no private bytes in Git, issues, PRs, terminal transcripts intended for
  publication, or ordinary development-agent context.

“Dataset” here means the private task contracts and their owner-reviewed
evidence lifecycle. It is not a dump of Jira or Confluence. Do not export a
project, space, issue set, page tree, user directory, or search result merely to
make a benchmark corpus.

## Boundaries first

Keep three activities distinct:

1. **Authoring** writes local private case files and runs offline validators.
   Manual authoring needs filesystem authority only. Assisted authoring also
   calls the selected model provider: by default it may see public ATL
   contracts and content-free private templates, but no backend facts.
2. **Agentless evidence qualification** uses exact bounded `atl --read-only`
   commands against named objects so the owner can establish expected facts.
   It needs a reviewed live-read plan; follow
   [Live validation](live-validation.md).
3. **Benchmark execution** sends the reviewed prompt and selected evidence to
   a candidate or reviewer model. It is distinct from interactive authoring and
   needs a short-lived immutable private plan, explicit provider-data consent,
   a cost cap, and a separate `RUN` confirmation.

Credentials or an existing login do not grant any of these authorities. A
healthy private workspace also grants none of them.

Remote writes are outside this onboarding path. Prefer an existing owned test
fixture or a read-only production object whose use was explicitly approved. If
a representative fixture must be created, stop and use a separate exact
live-write and cleanup plan before returning here.

## Fresh-clone prerequisites

From the repository root:

1. Read [`AGENTS.md`](../../AGENTS.md) and this runbook.
2. Use the exact Go patch declared in `go.mod`.
3. Build the product and the independent evaluator without introducing a
   `go.work` file:

   ```sh
   make build
   env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
     go -C internal/agenteval build -o /tmp/agent-eval ./cmd/agent-eval
   ```

4. Configure ATL credentials only through its normal owner-local configuration
   flow. Never put credentials, backend URLs, or an integration environment in
   the repository or private case files.
5. Install and authenticate one supported native provider CLI. Review the
   exact native executable before a later private plan; package launchers and
   scripts do not satisfy the evaluator boundary.

Run the read-only preflight from
[Development and verification](development.md) before the first build. Actual
Codex private execution currently requires a supported POSIX host because the
runner fails closed where it cannot prove owner-only provider-auth semantics;
offline authoring and validation do not waive the platform checks in the
private lifecycle.

Current provider documentation:

- Codex: [CLI command reference](https://learn.chatgpt.com/docs/developer-commands.md?surface=cli),
  [project `AGENTS.md` instructions](https://learn.chatgpt.com/docs/agent-configuration/agents-md.md),
  and [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode.md).
- Claude Code: [setup](https://code.claude.com/docs/en/setup),
  [CLI reference](https://code.claude.com/docs/en/cli-reference), and
  [large-codebase/project guidance](https://code.claude.com/docs/en/large-codebases).

Do not use Codex `--dangerously-bypass-approvals-and-sandbox` or Claude Code
`bypassPermissions` for this workflow.

## Create the private boundary

Choose an absolute path outside the clone. The path is private local state and
must not appear in public artifacts.

```sh
REPOSITORY_ROOT="$(pwd -P)"
ATL_AGENT_EVAL_PRIVATE_ROOT="/absolute/owner-selected/path"
umask 077
test ! -e "$ATL_AGENT_EVAL_PRIVATE_ROOT"
install -d -m 700 "$ATL_AGENT_EVAL_PRIVATE_ROOT"
git config --local atl.benchmarkPrivateRoot \
  "$ATL_AGENT_EVAL_PRIVATE_ROOT"
```

The repository-local Git setting is optional but recommended: recovery can
locate the workspace without scanning the filesystem. It is local-only and
must never be copied into committed configuration.

Initialize only an empty or new owner-only directory:

```sh
/tmp/agent-eval private init \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root "$REPOSITORY_ROOT"

/tmp/agent-eval private doctor \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root "$REPOSITORY_ROOT"
```

An existing non-empty unmarked directory is not a bootstrap target. Do not
adopt it, chmod it recursively, move its contents into the workspace, or ask an
agent to “make it pass”.

## Start the authoring agent

Start from the repository root so the binding repository instructions load.
Grant the private root as an additional directory only for the dedicated
bootstrap session. Isolate this offline session from the maintainer's normal ATL
configuration before starting either agent:

```sh
ATL_BOOTSTRAP_CONFIG_DIR="/absolute/owner-selected/offline-atl-config"
case "$ATL_BOOTSTRAP_CONFIG_DIR" in
  /*) ;;
  *) exit 1 ;;
esac
case "$ATL_BOOTSTRAP_CONFIG_DIR" in
  "$REPOSITORY_ROOT"|"$REPOSITORY_ROOT"/*|"$ATL_AGENT_EVAL_PRIVATE_ROOT"|"$ATL_AGENT_EVAL_PRIVATE_ROOT"/*) exit 1 ;;
esac
test ! -e "$ATL_BOOTSTRAP_CONFIG_DIR"
install -d -m 700 "$ATL_BOOTSTRAP_CONFIG_DIR"
unset ATL_JIRA_URL JIRA_URL ATL_JIRA_PAT JIRA_PAT TEST_JIRA_PAT
unset ATL_CONFLUENCE_URL CONFLUENCE_URL ATL_CONFLUENCE_PAT CONFLUENCE_PAT
unset TEST_CONFLUENCE_PAT ATL_JIRA_CA_BUNDLE ATL_CONFLUENCE_CA_BUNDLE
unset ATL_INTEGRATION ATL_ALLOW_INSECURE ATL_MIRROR_ROOT
export ATL_CONFIG_DIR="$ATL_BOOTSTRAP_CONFIG_DIR"
export ATL_READ_ONLY=1 ATL_NO_UPDATE=1
```

`unset` must not be replaced with a command that prints the old values. Keep
provider authentication available, but do not configure a backend MCP server
or copy an ATL config into this shell. The empty config directory must remain
outside both the repository and the evaluator's fixed private root; an extra
top-level directory inside that root makes `private doctor` fail closed. Use a
different, explicitly reviewed session for later agentless live evidence.

The safe default is **content-free assisted authoring**. Start with an empty
case directory; let the assistant create schemas, prompts, rubrics, budgets,
and explicit placeholders, then end the session before the owner adds expected
facts or source extracts manually. Do not place backend exports or completed
expected facts in the added directory merely because the assistant can read
it. If the assistant should inspect real private facts, record a separate
authoring-provider disclosure covering the exact provider, accepted data
class, files, purpose, and expiry before starting a fresh session. That consent
does not authorize a benchmark run.

For Codex, use a one-shot ephemeral session for intake and planning. The
non-interactive surface can ignore ambient user configuration while still
loading repository `AGENTS.md` and the repository skill:

```sh
codex exec --ephemeral \
  --cd "$REPOSITORY_ROOT" \
  --add-dir "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --ignore-user-config \
  --strict-config \
  --disable apps \
  --disable browser_use \
  --disable computer_use \
  --disable image_generation \
  --disable remote_plugin \
  --sandbox read-only \
  'Use $atl-private-benchmark-bootstrap to plan my private benchmark dataset. Do not access a backend or run a provider benchmark.'
```

After reviewing the proposed filesystem-only plan, start a fresh authoring
session by repeating the command with `--sandbox workspace-write` and an
authoring prompt. Keep every `--ignore-user-config`, `--strict-config`, and
`--disable` option. Non-interactive commands cannot request approval, so run
the write-enabled session only after its exact content-free file scope is
approved. Those options remove ambient user MCP servers, apps, plugins, and
browser/computer integrations while root `AGENTS.md` and the repository skill
remain the workflow authority.

For Claude Code, begin in plan mode:

```sh
cd "$REPOSITORY_ROOT"
claude --add-dir "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --setting-sources project \
  --strict-mcp-config \
  --mcp-config '{"mcpServers":{}}' \
  --no-chrome \
  --tools "Read,Glob,Grep" \
  --permission-mode plan \
  'Follow the private benchmark onboarding runbook. Plan the dataset only; do not access a backend or run a provider benchmark.'
```

After reviewing the plan, start a fresh session with the same project-only
settings, strict empty MCP configuration, and `--no-chrome`; change the tool
list to `--tools "Read,Glob,Grep,Write,Edit"` and use
`--permission-mode manual` to author the approved private files. Run evaluator
commands yourself through the offline validation gate below; the authoring
assistant does not need Bash or network tools. Claude Code loads the root
`CLAUDE.md`, which routes back to this canonical runbook. An additional
directory grants file access but does not make instructions stored there
authoritative.

Do not grant the private root to ordinary feature-development sessions. A
fresh development agent should see public contracts and aggregate comparison
results, not private prompts, expected facts, raw runs, or holdout material.

## Record the bootstrap authority

Before the agent writes a case, answer these questions explicitly. “None” is a
valid and preferred initial answer.

| Decision | Required answer |
|---|---|
| Private-root mutation | Exact root; initialize/edit allowed or not allowed |
| Backend reads | Service, exact owned objects or bounded selectors, exact commands, expiry; default none |
| Backend writes | Exact owned target and cleanup plan; default none and out of scope |
| Authoring-provider disclosure | Provider, accepted private files/data class, purpose, expiry; default content-free templates only |
| Benchmark-provider disclosure | Provider, exact candidate/reviewer model, reasoning, accepted data class, expiry; default none |
| Benchmark execution | Named run set, maximum cost, maximum attempts; default none during bootstrap |
| Baseline promotion | Named completed plan; default none |
| Pruning or cleanup | Exact reviewed inventory digest; default none |
| Publication | Aggregate fields and privacy reviewer; default none |

The agent should repeat the resulting authority summary without private values
and stop if the requested next action is not covered. An approval for authoring
does not imply backend reads. Backend-read approval does not imply provider
disclosure. Provider consent does not imply execution, promotion, or cleanup.

## Choose representative work, not representative data

Start from tasks the maintainer performs repeatedly. Good first tasks have a
stable question, a bounded evidence route, a checkable answer, and a clear
failure mode. Examples include:

- qualify one Jira epic from a bounded issue/history projection;
- summarize one owned sprint snapshot while preserving incomplete evidence;
- resolve and read one exact Confluence section selected from an outline;
- reconcile one cross-service decision from one named Jira item and one named
  Confluence page;
- inspect one local mirror without contacting a backend.

Bad first tasks include broad “understand our Jira” searches, whole-space
exports, user or permission inventories, incident/security content, tasks whose
answer is mostly subjective prose, and any mutation.

Prefer a dedicated non-sensitive test project or space populated with realistic
but non-proprietary content. Otherwise use exact owner-nominated objects and
the smallest projection that proves the task. The agent must not discover a
dataset by crawling, sampling arbitrary records, or expanding from one object
to unrelated neighbors.

### Minimum useful progression

| Stage | Dataset | Use |
|---|---|---|
| Calibration | One read-only primary case, one provider, one surface, one run | Prove the transport, oracle, and cost assumptions; directional only |
| Regression seed | The primary plus one distinct same-class holdout | Detect obvious overfitting before accepting the workflow |
| Development gate | Three separately consented primary observations plus at least one distinct holdout observation | Current regression-acceptance strength |
| Portfolio | Three to five recurring workflow families, each with a primary/holdout design | Guide product prioritization across Jira, Confluence, and mixed work |

Private-live run specs always retain `repetitions: 1`. “n=3” means three
separately planned and completed observations of the same reviewed primary
contract, not changing the spec to `repetitions: 3`. Use the private sampling
lifecycle for an aggregate strength claim.

A holdout must vary the underlying fixture and expected conclusion while
preserving the task class, capability family, budgets, response schema, rubric,
provider, model, reasoning, and surface. Renaming the same object or copying the
same expected facts is not a holdout.

## Author one private case

Keep every case below `PRIVATE_ROOT/cases/CASE_ALIAS/`. A normal read-only case
contains:

```text
cases/CASE_ALIAS/
  scenario.v1.json
  prompt.md
  response-schema.v1.json
  rubric.v1.json
  workspace/
  run.SURFACE.PROVIDER.json
```

Neutral-common surface comparisons also need a private blind assignment. A
holdout should be a separate case directory, not a conditional branch hidden
inside the primary prompt.

Build each file by invariant:

- `scenario.v1.json` is provider-neutral, uses `data_class:"private-local"`,
  declares every required semantic check and metric, permits zero delegation
  and zero remote writes, and gives every budget a finite value.
- `prompt.md` describes the desired outcome and evidence standard. It does not
  prescribe a provider-specific command or reveal the expected answer.
- `response-schema.v1.json` is closed and content-minimized. Require only fields
  needed to judge the task, completeness, and qualification.
- `rubric.v1.json` scores grounding, qualification, completeness,
  actionability, and concision without embedding prose from the source system.
- `workspace/` contains only reviewed task inputs. It must not contain
  `AGENTS.md`, `CLAUDE.md`, `.mcp.json`, `.agents/`, `.claude/`, `.codex/`,
  credentials, or provider control files.
- `run.*.json` uses current schema, `backend_mode:"private-live"`, exactly one
  repetition, the smallest interface allowlist, explicit data capabilities,
  finite request/byte/cost limits, and only `GET`/`HEAD` for the ordinary
  onboarding path.
- expected private facts belong only in the ignored private contract and its
  checks. They never move to a public fixture, issue, PR, or code comment.

Register the case in `private-workspace.v4.json` under one
`kind:"comparison"` run set. Use workspace-relative `cases/...` spec paths,
finite execution and retention budgets, and environment-variable **names** for
the live config or an external MCP profile. The manifest never stores their
resolved paths or values. Keep the config/profile values in the private
session environment only. Add a primary and holdout as separate run sets when
they are separate semantic contracts.

Use the current public contracts as structure references, not as permission to
copy a synthetic scenario blindly:

- [`benchmarks/agent-eval/README.md`](../../benchmarks/agent-eval/README.md)
- [`private-workspace.schema.json`](../../benchmarks/agent-eval/private-workspace.schema.json)
- [`private-workspace.example.json`](../../benchmarks/agent-eval/private-workspace.example.json)
- the private-live example in
  [Agent evaluation methodology](../agent-benchmarking.md#private-live-model-in-the-loop-check)

The Go decoder is authoritative. Do not weaken a schema, remove a semantic
check, raise a budget, or broaden an allowlist merely to make an authored case
validate.

### Establish expected facts agentlessly

The owner should supply the task and exact starting objects. If expected facts
must be refreshed from a configured backend, first create a bounded read-only
plan under [Live validation](live-validation.md). List every command and bound
before running it, force `ATL_READ_ONLY=1`, and keep output inside the private
root or the operator's private terminal.

By default, the owner translates reviewed output into closed expected fields
after the content-free authoring session ends. Do not send those bytes to
Codex, Claude, another model, a public paste, or an issue. An assistant may do
that translation only in a fresh session whose authoring-provider disclosure
explicitly covers the selected files and data class. A broad search result is
not a shortcut for an explicit fixture inventory.

If the owner cannot state which private facts may reach the provider, finish
offline authoring and stop. Provider consent can be added later through an
immutable plan.

## Offline validation gate

Run offline checks after every private case or manifest edit:

```sh
umask 077
/tmp/agent-eval validate-run \
  "$ATL_AGENT_EVAL_PRIVATE_ROOT/cases/CASE_ALIAS/run.SURFACE.PROVIDER.json"

/tmp/agent-eval private doctor \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT" \
  --repository-root "$REPOSITORY_ROOT"

/tmp/agent-eval private status \
  --root "$ATL_AGENT_EVAL_PRIVATE_ROOT"
```

For a same-provider surface comparison, validate the complete set before any
run:

```sh
/tmp/agent-eval validate-comparison-set \
  "$ATL_AGENT_EVAL_PRIVATE_ROOT/cases/CASE_ALIAS/run.cli-skill.codex.json" \
  "$ATL_AGENT_EVAL_PRIVATE_ROOT/cases/CASE_ALIAS/run.atl-mcp.codex.json"
```

One comparison run set keeps the provider, model, reasoning, prompt, schema,
rubric, workspace, semantic contract, and budgets fixed while surfaces vary.
Do not put Codex and Claude Code into one surface-comparison run set. Start with
one provider; add a second provider as a separate run set using the same
provider-neutral task contract only when that comparison is actually needed.

These commands must finish without provider authentication or configured
backend traffic. A validation error is a design finding. Fix the case or narrow
the claim; do not relax the evaluator.

## Consent-bound execution comes later

Dataset bootstrap is complete when offline validation is green. To collect a
measurement, continue with the canonical private lifecycle:

```text
doctor -> qualify when required -> plan -> human review -> run
       -> deterministic checks -> review -> baseline -> compare
```

The private plan binds the exact case bytes, ATL and agent binaries, plugin
tree, repository commit, backend configuration identity, provider/model,
budgets, and a short consent expiry. It is the first stage that may include
`--approve-provider-data --confirm CONSENT`. Execution still requires the exact
plan digest and a separate `--confirm RUN`.

Use [Private agent-benchmark workspace](../agent-benchmark-private-workspace.md)
for the exact current commands. Do not copy plan or run flags from historical
issues. The bootstrap skill must stop here unless the current request contains
an exact reviewed authority for the named provider, model, run set, cost cap,
data class, and consent window.

## Use the dataset during development

1. Establish an immutable baseline on a synchronized `main` commit.
2. End the private session and launch the development agent **without** the
   private root. Give it the product objective and public failure class, not the
   holdout prompt, expected facts, or raw model answer.
3. Implement and verify the product change through the normal issue/PR flow.
4. On the reviewed candidate commit, start a fresh private-evaluation session,
   create a new short-lived plan, and run the unchanged primary contract.
5. Run the unchanged holdout only after implementation choices are fixed.
6. Compare offline. Treat correctness/safety failures as blockers; treat token,
   cost, and latency changes as evidence with the documented sample strength.
7. Promote a baseline, prune raw candidates, or publish aggregates only through
   their separate reviewed transitions.

Never edit a case after seeing a candidate result and then compare it with the
old baseline as though the contract were unchanged. A legitimate task-contract
change starts a new baseline series.

## Handoff and publication boundary

At a stable boundary, record only aggregate state outside the private root:

- repository commit;
- evaluator schema versions;
- healthy/unhealthy status and closed error code;
- case-family counts and primary/holdout coverage;
- provider/model/reasoning labels already approved for disclosure;
- completed verification and the next authority required.

Do not record private paths, prompts, expected facts, object identifiers,
commands with selectors, answers, transcripts, review rationale, backend
identity, credentials, or provider auth state.

A private baseline is still private. Publication requires a separate manual
privacy review and may include only reviewed aggregates and generic task-class
labels. Local deletion cannot remove provider-side or upstream-service logs.

### Aggregate bootstrap report

The authoring agent should finish with this content-free shape in prose or
JSON, without private names or paths:

```json
{
  "private_root": "configured",
  "case_families": 1,
  "primary_cases": 1,
  "holdout_cases": 1,
  "offline_validation": "passed",
  "benchmark_runs": 0,
  "backend_reads": 0,
  "backend_writes": 0,
  "remaining_authority": [
    "agentless evidence qualification",
    "provider consent",
    "benchmark execution",
    "baseline promotion",
    "retention or publication"
  ],
  "next_action": "review the private case contracts"
}
```

Counts describe the authored contract only; they are not correctness or
quality claims.

## Stop conditions

Stop and ask the owner rather than improvising when:

- the private root is missing, symlinked, non-owner-only, inside Git without an
  ignore proof, or already populated but unmarked;
- dirty repository or evaluator bytes make executable identity uncertain;
- no exact owned fixture or bounded read route was nominated;
- the task requires a broad search, user directory, permission inventory,
  attachment body, or unrelated neighbor expansion;
- expected facts cannot be established without unapproved backend access;
- provider/model/data-class consent, cost cap, or expiry is absent;
- a case validates only after widening permissions or budgets;
- a provider or backend outcome is ambiguous;
- a development session can see holdout or raw private evidence;
- any private value appears in a public diff, issue, PR, or log.
