# Held-out synthetic Jira reference-summary MCP workspace

This held-out topology reuses the same read-only typed Jira route as its primary
cohort but carries a different synthetic selection: a bounded JQL selection over
two issues instead of one exact key, a shared reference across distinct issues,
and a truncated selection. The runner supplies a deterministic loopback backend;
nothing here contains the expected selection, counts, source qualification, or
reconciliation facts, and the task must not inspect repository fixtures or any
other local source.
