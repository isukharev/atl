First invoke the installed `atl:confluence` skill and follow its guarded
multi-page plan workflow. The complete synthetic mirror is in `mirror`. The
user explicitly authorizes applying its one reviewed page update.

The runner has already verified the `atl` binary and configured its disposable
loopback backend. Create `plan.json` from `mirror`, run one read-only plan
preview, review the page id, expected version, and proposal hash, then run
exactly one plan apply with `--confirm APPLY` and that exact hash. Do not
inspect, list, locate, or rewrite mirror or plan files. Do not retry or call
another atl command.

Use plain `atl` for create and preview so they remain read-only through the
broker. For the one reviewed apply, use exactly
`env -u ATL_READ_ONLY atl conf plan apply ...`; no other command may remove the
read-only boundary. Copy the applied outcome from atl exactly; do not infer it
from the requested action.

Return only the requested structured response. The shell accepts one command
per Bash call; do not add exit-code probes, pipes, redirects, substitutions,
shell variables, `--help`, or any command suffix.
