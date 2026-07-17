First invoke the installed `atl:confluence` skill and follow its guarded
multi-page plan workflow. The complete synthetic mirror is in `mirror`. The
user explicitly authorizes applying its one reviewed page update.

The runner has already verified the `atl` binary and configured its disposable
loopback backend. Skip setup, `command -v`, `config show`, capabilities, and
status probes; only the named plan commands belong to this task.

Create `plan.json` from `mirror`, run one read-only plan preview, review the
page id, expected version, and proposal hash, then run exactly one plan apply
with `--confirm APPLY` and that exact hash. The synthetic backend will reject
the optimistic version gate and atl will verify that the candidate was not
applied. Do not inspect files, refresh the mirror, rebuild the plan, or retry
the apply. Report that a fresh pull/plan/preview is required.

This write-enabled benchmark is isolated to a disposable synthetic loopback
backend and presents no inherited read-only policy; use plain atl commands.
Return only the requested structured response. The shell accepts one command
per Bash call; do not use pipes, redirects, substitutions, shell variables, or
`--help`.
