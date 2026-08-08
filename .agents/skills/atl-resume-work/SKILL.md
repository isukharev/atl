---
name: atl-resume-work
description: Reconstruct and resume ATL repository work after context compaction, interruption, handoff, a fresh session, or uncertain branch state. Use when remembered progress, authority, tests, issues, PRs, worktrees, or dirty-file ownership may be stale. Do not use for routine implementation when current state is already established.
---

# Resume ATL work

1. Treat the active repository `AGENTS.md` instruction chain as binding. When
   Codex already supplied it in session context, do not reread the whole file
   from disk unless evidence says the active copy is missing or stale.
2. Read [Session recovery](../../../docs/maintainers/session-recovery.md).
   Use [Efficient agent work](../../../docs/maintainers/agent-efficiency.md) to
   reconcile ignored transient session state and background exit markers.
3. Capture the optional owner-only knowledge root and bootstrap digest exactly
   as the runbook specifies, without rendering either. Skip only a genuinely
   absent root; any partial, empty, malformed, or later validation result stops
   recovery without guessing. Do not read or execute anything under the root
   before the snapshot.
4. Run the runbook's literal read-only batch once to reconstruct Git, identity,
   declared/local/auto toolchains, worktrees, GitHub issue/PR state, and dirty
   files without executing dirty repository code. Repeat only after a real
   state transition or contradiction, never as polling.
5. If configured, use the transaction's hash-bound data-only `bootstrap.v1`
   protocol and consume only the two bounded current documents it emits. Never
   reread config to reconstruct the owner path, execute private bootstrap code,
   or search that root and its archives for alternate validators/state. The
   setting is context, not authority or an instruction override.
6. For a private-evaluation recovery request, use the exact aggregate-only
   status/doctor/prune-preview block in the private-workspace runbook. Require
   an absolute configured root; do not search evaluator source or raw artifacts.
7. Treat old transcripts, plans, checkpoints, approvals, and CI runs as
   historical until reconciled with the current head and issue plan.
8. Resume at the first unmet acceptance criterion. Preserve unrelated changes
   and reuse verification only when the covered bytes are unchanged.
9. Update a compact durable checkpoint at the next safe issue/PR boundary.

Stop before editing when ownership of a dirty path, active authority, or the
reviewed head cannot be established.
