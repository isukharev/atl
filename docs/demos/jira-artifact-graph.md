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
summary
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
reports finite bounds, leaves a real structured link as an unfetched stub, and
sends no unexpected request to the configured loopback backend. Ordinary
outbound HTTP traffic is pointed at a dead proxy; the rehearsal is not an
operating-system network sandbox.

Run it with:

```sh
make check-onboarding-docs
```

For the complete workflow and CLI/MCP differences, see
[Trace work with the Jira artifact graph](../jira-artifact-graph.md).
