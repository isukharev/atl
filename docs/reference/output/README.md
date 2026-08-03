# Output-contract reference

These files are the canonical stdout, exit, completeness, and recovery contracts. The legacy [single-file output index](../../OUTPUT_CONTRACT.md) preserves published links but does not own contract prose.

[Documentation home](../../README.md) · [CLI reference](../cli/README.md)

```json
{"status":"ok"}
```

## Shared contracts

- [Common output and errors](common.md)
- [Configuration and environment](configuration.md)
- [Agent interfaces](agent-interfaces.md)
- [Registration and write guards](registration-and-write-guards.md)
- [Local artifacts](local-artifacts.md)

## Service contracts

- [Confluence](confluence.md)
- [Jira](jira.md)
- [Jira planning and Structure](jira-planning.md)

## Legacy mixed groups

Older links grouped some unrelated contracts together. Their canonical owners
are now explicit:

- Exports, tables, and reports: [Confluence tables](confluence.md#confluence-tables) and [Jira exports and reports](jira.md#jira-exports).
- Page and section reads: [Confluence page sections](confluence.md#page-and-section-reads) and [Jira epic digest](jira.md#jira-epic-digest).
