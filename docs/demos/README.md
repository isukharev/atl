# Reproducible ATL demonstrations

These short demonstrations explain three core guarantees without requiring a
real Jira or Confluence instance:

1. [Edit a complex Confluence page without rebuilding untouched native bytes](confluence-lossless-edit.md).
2. [Refuse a Confluence version conflict without losing local work](confluence-conflict-refusal.md).
3. [Build a bounded Jira artifact graph](jira-artifact-graph.md).

The commands shown use generic ids and content. Repository CI runs equivalent
scenarios against loopback-only synthetic backends through the actual supplied
ATL binary:

```sh
make check-onboarding-docs
```

The rehearsal uses a new credential-free config and temp directory, fixed
synthetic tokens, finite request/byte bounds, loopback-only configured
backends, and dead ordinary outbound proxies. It fails on every unexpected
fixture route or backend write and reports workspace-cleanup failures. This is
a hermetic product rehearsal, not an operating-system network sandbox.
