Review the distinct owner-configured synthetic Jira holdout mirror through
typed MCP only. Do not delegate, inspect workspace or repository files, execute
shell commands, contact a backend, or mutate anything.

Call `jira_mirror_snapshot` once with `{}` and no other tool. Preserve every
returned local, native, raw-snapshot, pending, render, remote, completeness, and
reconciliation field exactly. Do not supply or discover a path and do not ask
for a remote check.

Return only the exact fixed-shape structured result. It must contain no issue
identity, path, title, filename, or document content.
