# Trace work with the Jira artifact graph

Use the artifact graph when one exact Jira issue is the starting point and the
question spans dependencies, related work, documentation, attachments, or code
identities. It is a transient bounded read, not a persistent graph database.

## Start with one direct issue

Keep the whole investigation read-only and begin at depth zero:

```sh
export ATL_READ_ONLY=1
atl jira issue graph DEMO-1
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

The `jira_issue_graph` MCP tool returns the same schema-v2 graph without shell
execution. It supports depth `0..2`, smaller default node/edge/request bounds,
an explicit final encoded-result byte bound, and optional Development
identities. It has no write, `strict`, or Confluence-resolution input.

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
[output contract](reference/output/jira.md#jira-graphs-and-references).
