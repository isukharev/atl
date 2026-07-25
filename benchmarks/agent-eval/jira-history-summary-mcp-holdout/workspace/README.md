# Held-out synthetic Jira history-summary workspace

This held-out topology reuses the same read-only typed Jira route as its primary
cohort but carries a different synthetic changelog: different identities,
timestamps, field vocabulary, and pagination behavior. The runner supplies a
deterministic loopback backend; nothing here contains the expected provenance,
counts, ordering state, buckets, or partial reason, and the task must not
inspect repository fixtures or any other local source.
