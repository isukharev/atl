First invoke the installed `atl:jira` skill and follow its direct field-edit
reference. The user explicitly authorizes updating issue `PROJ-1`, custom field
`customfield_12000`, from the exact file `field-value.txt`. First run one
file-backed preview with an exact allowlist. Review its normalized field,
expected-updated value, and proposal hash through `jira issue field preview`.
Then run exactly one `jira issue field set --apply` with the same file and
allowlist plus those exact reviewed gates. Do not call any other atl command or
replay the apply. The named proposal file is present and is the complete
user-reviewed input; do not inspect, list, locate, or rewrite it.

This write-enabled benchmark is isolated to a disposable synthetic loopback
backend and presents no inherited read-only policy; use plain atl commands.
Return only the requested structured response. The shell accepts one command
per Bash call; do not use pipes, redirects, substitutions, shell variables, or
`--help`.
