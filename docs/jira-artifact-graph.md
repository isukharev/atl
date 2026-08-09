# Trace work with the Jira artifact graph

Use the artifact graph when one exact Jira issue is the starting point and the
question spans dependencies, related work, documentation, attachments, or code
identities. It is a transient bounded read, not a persistent graph database.

## Start with one direct issue

Keep the whole investigation read-only and begin at depth zero:

```sh
export ATL_READ_ONLY=1
atl jira issue graph DEMO-1
atl jira issue graph DEMO-1 --projection compact
```

Depth zero expands only `DEMO-1`. Related Jira issues, Confluence page ids,
attachments, and safe URL targets remain depth-one stubs. ATL does not fetch a
linked issue, download an attachment, resolve a page, or follow an external URL
unless a documented option explicitly requests that work.

Before using the graph, check:

- top-level `complete` and `partial_reasons`;
- every requested source's `complete`, `stability`, and reason;
- reconciliation counts for nodes, edges, and evidence;
- `bounds` request/response usage;
- the bounded `frontier` when expansion stopped early.

An empty or absent relation is evidence of absence only when the source and the
whole relevant selection are complete.

The omitted or explicit `--projection full` form is the byte-compatible
schema-v2 graph. Use JSON-only `--projection compact` when the task needs the
same qualification plus a smaller fact inventory. Compact defaults to `urls`;
with `--include-development` it defaults to `urls,scm`. A repeatable or
comma-separated `--select urls|scm|none` narrows facts only after the full
bounded graph has been collected. It never reduces requests. Inspect compact
`projection.selected`, `projection.omitted`, incomplete-source, frontier,
warning, bound, and reconciliation fields before treating an empty fact list
as absence.

## Start from one external target

Use the inverse-reference command when the starting point is one exact GitLab
project or Confluence page and the result should be the Jira issues that refer
to it. This route is CLI-only; there is no typed MCP counterpart.

```sh
export ATL_READ_ONLY=1
atl jira issue reference search \
  --target 'https://gitlab.example.test/platform/widget' \
  --target-kind gitlab-project \
  --scope-jql 'project = DEMO' \
  --mode exhaustive \
  --sources description,comments,remote-links,development \
  --max-issues 100 \
  --max-requests 1000 \
  --max-response-bytes 16777216 \
  --strict
```

Every policy choice is explicit: target, caller-qualified JQL scope, mode,
source set, and issue/request/response-byte bounds. Select `fields` only with
exact technical ids in `--fields`. Other source choices are `description`,
`comments`, `remote-links`, `worklogs`, `development`, and `properties`.

Use `exhaustive` when absence matters. It performs two terminal key-ordered
selection passes, rejects selection drift, and verifies every selected source
for every selected issue. This detects candidate-set drift but is not atomic
snapshot isolation. Only a reconciled `complete:true` exhaustive result with
zero matches sets `absence_proven:true`. `fast` uses one target-derived Jira
selection and is always incomplete with `reason:"mode_fast"`; use it only for
qualified discovery. `--strict` still emits the result before exit 8, so retain
and inspect that JSON.

The result contains issue keys and content-free qualification, never the raw
target, JQL, URLs, titles, source text, property keys, or backend errors. ATL
matches selected Jira values locally. It never contacts GitLab or dereferences
a discovered URL. A caller-supplied Confluence display or short target may be
resolved against the configured Confluence origin under the shared bounds;
direct ids and id-bearing URLs avoid that request. Discovered Confluence values
match only direct same-origin id-bearing links, and no page body or backlink
query is made.

## Expand only structured Jira relations

Use the smallest depth that answers the question:

```sh
atl jira issue graph DEMO-1 --depth 1
atl jira issue graph DEMO-1 --depth 2 --strict
```

CLI depth may be `0..3`. Traversal is canonical breadth-first and follows only
structured Jira links or hierarchy. A key mentioned in description, comment,
worklog, or a text custom field is recorded as evidence but never followed as
if it were an authoritative relationship.

`--strict` still emits the qualified graph, then exits `8` when requested
evidence is incomplete. Use it in CI or an agent workflow that must not proceed
on partial evidence.

CLI defaults and hard maxima are deliberately finite:

| Limit | Default | Maximum |
|---|---:|---:|
| nodes | 100 | 2048 |
| edges | 500 | 4096 |
| evidence | 500 | 4096 |
| physical requests | 100 | 128 |
| buffered backend bytes | 16 MiB | 64 MiB |

Narrow depth or selected sources before increasing a bound.

## Resolve only the Confluence metadata you need

The CLI can resolve discovered canonical Confluence page ids to id/title
metadata:

```sh
atl jira issue graph DEMO-1 --resolve confluence
```

This does not read page bodies or follow arbitrary URLs. Omit the option when a
qualified page stub is sufficient. The typed MCP route is Jira-only and has no
Confluence-resolution input.

## Add Development identities explicitly

Request the experimental Jira Development source only for a code, commit,
branch, or merge-request question:

```sh
atl jira issue graph DEMO-1 --include-development
atl jira issue graph DEMO-1 --include-development --projection compact --select scm
```

ATL emits only closed GitLab coordinates: normalized lowercase host and project
path, full commit SHA, exact bounded branch name, or positive merge-request iid
and normalized state. It does not contact GitLab, fetch artifact URLs, clone a
repository, or forward Jira credentials.

Treat every returned SCM coordinate as untrusted evidence. Before a later
lookup, require exact equality with an owner-approved lowercase host and use a
separately authenticated read-only GitLab client for that host. Omission of the
Development option is not proof that no development work exists.

## Use the typed MCP route for transient agent reads

The `jira_issue_graph` MCP tool returns the same full-v2 or compact-v1
projection without shell execution; full remains the default. It accepts the
same `projection` and `select` vocabulary, supports depth `0..2`, smaller
default node/edge/request bounds, an explicit final encoded-result byte bound,
and optional Development identities. It has no write, `strict`, or
Confluence-resolution input. Compact output is still subject to the final
encoded bound and is never clipped.

For durable review, larger CLI traversal, exact text rendering, or Confluence
metadata resolution, use the CLI. For a single bounded agent question, prefer
the typed tool and inspect its completeness fields directly.

## Reproduce the bounded workflow

The repository's credential-free onboarding rehearsal runs a synthetic graph
through the real supplied binary and proves depth zero, four physical requests,
finite bounds, qualified stubs, and zero external fetches:

```sh
make check-onboarding-docs
```

See [the short graph demonstration](demos/jira-artifact-graph.md) for the exact
user-facing sequence. Exhaustive flags and wire fields remain in the
[command reference](reference/cli/jira-graph.md#atl-jira-issue-graph) and
[output contract](reference/output/jira.md#jira-graphs-and-references). The
inverse direction is specified under
[`jira issue reference search`](reference/cli/jira-graph.md#atl-jira-issue-reference-search).
