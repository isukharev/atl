<!-- Generated from skills-src/jira/reference/batch-read.md — edit the source and run 'make gen-plugins'. -->
# Ordered Jira batch read

Use `atl capabilities --task jira/batch-analysis -o text` when the task already
supplies several issue keys or ids. Its primary route is the qualified compact
field matrix; transient export is the broader expansion. Keep selected fields
narrow and do not turn either batch into a shell loop.

```sh
export ATL_READ_ONLY=1
atl jira issue field batch --key PROJ-1 --key PROJ-2 --field summary --field status
```

Require top-level `complete:true` and `reconciled:true`. Preserve key and field
order; `missing_or_inaccessible` does not prove absence. Expand a clipped cell
only when it is needed. Use export only when the task requires broader issue
bodies:

```sh
export ATL_READ_ONLY=1
atl jira export --keys PROJ-1,PROJ-2,PROJ-3 --fields summary,status --format json --out -
```

Read the JSON artifact directly from stdout. Do not pipe it through `jq`, add a
redirection, or combine it with another shell command inside a guarded agent
run. Do not use shell continuations. The output is valid only when atl exits
zero.

Explicit ids/keys preserve first-occurrence selector order, ignore later
duplicate selectors, and omit missing identities. Preserve those as separate
facts: a complete backend page does not mean every requested identity was
found. Do not substitute `*all`, user objects, or a broader JQL query.
