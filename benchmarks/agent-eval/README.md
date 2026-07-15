# Synthetic agent evaluations

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

Claude Code receives the repository plugin through `--plugin-dir`. Codex gets
the same generated skills copied into the isolated workspace's
`.agents/skills/` directory for command review, but actual Codex model execution
is fail-closed: its read-only OS sandbox cannot safely reach the host-side mock,
while granting broader network/filesystem access would violate this runner's
trust boundary. Codex specs support validation and `--dry-run` only until a
typed tool or external container supplies the missing isolation. Claude Code is
the supported model-in-loop provider and receives both a Bash allow-rule and a
`PreToolUse` guard limited to the run spec's `allowed_atl_commands`. The guard
permits one reviewed `atl` command per Bash call and rejects shell operators,
substitution, redirection, multiline scripts, and unrelated executables.
The runner requests a proxy-only subprocess `PATH`, but does not trust PATH as a
security boundary: the `PreToolUse` hook denies unrelated binaries before Bash,
and every accepted `atl` invocation still crosses the accounting proxy.

Raw transcripts, stderr, final structured output, invocation counters, and
per-run results stay in the private output root with restrictive permissions.
Only privacy-reviewed aggregate result contracts may be published.

The runner is intended for provider subscription authentication already stored
by the provider CLI. It does not forward API-key or unrelated credential
environment variables into the agent process. Use deterministic evaluation or
a separately reviewed runner before introducing API-key authentication.

Do not use this runner for injected corporate content. Prompt-injection model
runs require a separately reviewed synthetic Claude case; Codex requires the
stronger typed-tool/container boundary above.
