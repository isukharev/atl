<!-- Generated from skills-src/jira/reference/batch-read.md — edit the source and run 'make gen-plugins'. -->
# Ordered Jira batch read

Use this route when the task already supplies several issue keys or ids and one
compact transient read is sufficient. Keep the selected fields narrow and do
not turn the batch into a shell loop.

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
