# Held-out synthetic Jira snapshot-reconciliation workspace

This held-out topology reuses the same read-only typed Jira route as its
primary cohort but carries different synthetic identities, stamps, and content.
The runner supplies a deterministic loopback backend; nothing here contains the
expected identities, stamps, reconciliation outcome, or decision, and the task
must not inspect repository fixtures or any other local source.
