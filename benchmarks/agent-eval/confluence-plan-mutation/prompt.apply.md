First invoke the installed `atl:confluence` skill and follow its guarded
multi-page plan workflow. The complete synthetic mirror is in `mirror`. The
user explicitly authorizes applying its one reviewed page update.

The runner has already verified the `atl` binary and configured its disposable
loopback backend. Skip setup, `command -v`, `config show`, capabilities, and
status probes; only the named plan commands belong to this task.

Create `plan.json` from `mirror`, run one read-only plan preview, review the
page id, expected version, and proposal hash, then run exactly one plan apply
with `--confirm APPLY` and that exact hash. Do not inspect, list, locate, or
rewrite mirror or plan files. Do not push, retry, or call another atl command.

This write-enabled benchmark is isolated to a disposable synthetic loopback
backend and presents no inherited read-only policy; use plain atl commands.
Return only the requested structured response. The shell accepts one command
per Bash call; do not use pipes, redirects, substitutions, shell variables, or
`--help`.
