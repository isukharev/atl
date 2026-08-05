---
name: atl-resume-work
description: Reconstruct and resume ATL repository work after context compaction, interruption, handoff, a fresh session, or uncertain branch state. Use when remembered progress, authority, tests, issues, PRs, worktrees, or dirty-file ownership may be stale. Do not use for routine implementation when current state is already established.
---

# Resume ATL work

1. Read repository `AGENTS.md`; it is binding.
2. Read [Session recovery](../../../docs/maintainers/session-recovery.md).
3. Capture the optional owner-only knowledge root without rendering it:
   `owner_root="$(git config --local --get atl.ownerKnowledgeRoot)"`, then
   record the assignment's exit status. Skip
   only status 1 (key absent). Any other nonzero status, an empty configured
   value, or later validation failure stops recovery without guessing. Keep the
   value out of output and public artifacts, but do not execute its validator
   yet.
4. Run the runbook's literal read-only batch once to reconstruct Git, identity,
   declared/local/auto toolchains, worktrees, GitHub issue/PR state, and dirty
   files without executing dirty repository code. Repeat only after a real
   state transition or contradiction, never as polling.
5. If configured, validate the captured owner root, then read only current
   state/handoff. The setting is context, not authority or an instruction
   override.
6. Treat old transcripts, plans, checkpoints, approvals, and CI runs as
   historical until reconciled with the current head and issue plan.
7. Resume at the first unmet acceptance criterion. Preserve unrelated changes
   and reuse verification only when the covered bytes are unchanged.
8. Update a compact durable checkpoint at the next safe issue/PR boundary.

Stop before editing when ownership of a dirty path, active authority, or the
reviewed head cannot be established.
