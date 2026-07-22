First invoke the installed `atl:jira` skill and follow its direct field-edit
reference. The user explicitly authorizes updating issue `PROJ-1`, custom field
`customfield_12000`, from the exact file `field-value.txt`. First run one
file-backed preview with an exact allowlist. Review its normalized field,
expected-updated value, and proposal hash through `jira issue field preview`.
Then run exactly one `jira issue field set --apply` with the same file and
allowlist plus those exact reviewed gates. Do not call any other atl command or
replay the apply. The named proposal file is present and is the complete
user-reviewed input; do not inspect, list, locate, or rewrite it.

The write-enabled benchmark is isolated to a disposable synthetic loopback
backend. Use plain `atl` for the GET-only preview. For the one reviewed apply,
use exactly `env -u ATL_READ_ONLY atl jira issue field set ...`; no other
command may remove the read-only boundary. Copy the applied outcome from atl
exactly; do not infer it from the requested action.

Return only the requested structured response. The shell accepts one command
per Bash call; do not add exit-code probes, pipes, redirects, substitutions,
shell variables, `--help`, or any command suffix.
