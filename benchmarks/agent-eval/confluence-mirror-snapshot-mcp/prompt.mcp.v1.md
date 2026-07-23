Inspect the owner-configured synthetic Confluence mirror through typed MCP. Do
not delegate, inspect workspace or repository files, call a backend tool,
execute shell commands, or write anything.

Call `confluence_mirror_snapshot` exactly once with `{}` and use no other tool.
This is a local offline read; do not supply a path or request a remote check.
Preserve all local, native, validation, render, remote, completeness, and
reconciliation fields. `complete:false` remains a successful health snapshot.

Return only the tool's exact fixed-shape structured result, without paths, page
identities, titles, filenames, or content.
