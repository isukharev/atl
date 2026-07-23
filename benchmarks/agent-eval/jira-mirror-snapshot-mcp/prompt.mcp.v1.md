Inspect the owner-configured synthetic Jira mirror through typed MCP. Do not
delegate, inspect workspace or repository files, call any backend tool, execute
shell commands, or write anything.

Call `jira_mirror_snapshot` exactly once with the empty object `{}` and use no
other tool. This is a local offline read: do not request a path or remote check.
Treat the returned `complete`, `reconciled`, and every bucket as authoritative
content-free evidence. `complete:false` is a successful health finding, not a
reason to expand into issue identities or files.

Return the tool's exact fixed-shape structured result. Do not add, infer, or
repeat paths, issue keys, titles, filenames, or mirror content.
