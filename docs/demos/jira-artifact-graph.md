# Demo: build a bounded Jira artifact graph

Start with one exact issue and no traversal:

```sh
export ATL_READ_ONLY=1
atl jira issue graph DEMO-1 --depth 0 --max-requests 8
```

The result expands only `DEMO-1`. Structured Jira relations and a discovered
Confluence identity remain qualified stubs; a narrative mention is evidence
but is not followed.

Inspect:

```text
schema_version
complete
sources
reconciliation
bounds.requests_used
frontier
nodes
edges
evidence
```

Only increase depth when the task requires linked Jira work:

```sh
atl jira issue graph DEMO-1 --depth 1 --max-nodes 20 --max-requests 12 --strict
```

The credential-free repository rehearsal serves one synthetic Jira snapshot,
empty comments/worklogs/remote links, and no external artifact endpoints. It
proves the depth-zero command uses exactly four single-attempt Jira reads,
reports finite bounds, leaves discovered objects unexpanded, and sends no
request outside the loopback server.

Run it with:

```sh
make check-onboarding-docs
```

For the complete workflow and CLI/MCP differences, see
[Trace work with the Jira artifact graph](../jira-artifact-graph.md).
