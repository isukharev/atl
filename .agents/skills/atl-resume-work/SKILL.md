---
name: atl-resume-work
description: Reconstruct and resume ATL repository work after context compaction, interruption, handoff, a fresh session, or uncertain branch state. Use when remembered progress, authority, tests, issues, PRs, worktrees, or dirty-file ownership may be stale. Do not use for routine implementation when current state is already established.
---

# Resume ATL work

1. Read repository `AGENTS.md`; it is binding.
2. Read [Session recovery](../../../docs/maintainers/session-recovery.md).
3. Reconstruct current Git, identity, worktree, GitHub issue/PR, check, and dirty
   state read-only.
4. If current instructions identify an owner-only knowledge root, validate it
   and read only its current state/handoff. Never guess or publish that path.
5. Treat old transcripts, plans, checkpoints, approvals, and CI runs as
   historical until reconciled with the current head and issue plan.
6. Resume at the first unmet acceptance criterion. Preserve unrelated changes
   and reuse verification only when the covered bytes are unchanged.
7. Update a compact durable checkpoint at the next safe issue/PR boundary.

Stop before editing when ownership of a dirty path, active authority, or the
reviewed head cannot be established.
