First invoke the installed `atl:confluence` skill and follow its guarded
multi-page plan workflow. The complete synthetic mirror is in `mirror`. The
user explicitly authorizes applying its one reviewed page update.

The runner has already verified the `atl` binary and configured its disposable
loopback backend. Skip setup, `command -v`, `config show`, capabilities, and
status probes; only the named plan commands belong to this task.

Create `plan.json` from `mirror`, run one read-only plan preview, review the
page id, expected version, and proposal hash, then run exactly one plan apply
with `--confirm APPLY` and that exact hash. If atl reports that reconciliation
could not prove the outcome, treat the update as possibly committed, do not
replay it, and require explicit reconciliation before any retry. Do not inspect
files, refresh the mirror, rebuild the plan, or call another atl command.

This write-enabled benchmark is isolated to a disposable synthetic loopback
backend. Use plain `atl` for create and preview so they remain read-only through
the broker. For the one reviewed apply, use exactly
`env -u ATL_READ_ONLY atl conf plan apply ...`; no other command may remove the
read-only boundary. Copy the unknown outcome from atl exactly; do not infer it
from the requested action. Return only the requested structured response. The
shell accepts one command per Bash call; do not add exit-code probes, pipes,
redirects, substitutions, shell variables, `--help`, or any command suffix.
