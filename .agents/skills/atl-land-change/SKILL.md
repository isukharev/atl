---
name: atl-land-change
description: Carry an ATL repository change from a shaped issue through linked branch, draft PR, integrated review, hosted CI, authorized merge, main synchronization, and cleanup. Use when opening, reviewing, checking, merging, or recovering an ATL PR. Do not use for local exploration that is not entering the issue and PR lifecycle.
---

# Land an ATL change

1. Treat the active repository `AGENTS.md` instruction chain as binding. Do
   not reread the whole file when Codex already supplied it in session context
   unless evidence says the active copy is missing or stale.
2. Read [Landing a change](../../../docs/maintainers/landing-a-change.md).
3. Before opening a draft, verify issue plan, branch ownership, author identity,
   dirty state, and the current coherent scope. Open it early enough to preserve
   traceability.
4. Before marking ready, verify the final diff, class-selected local gates,
   privacy scan, issue links, and the class-required independent review.
5. For a material fix, name changed paths and rerun only their impact-map gates;
   request a bounded follow-up only for material correctness, security, or
   design changes.
6. Mark ready, then block once under an outer timeout with
   `gh pr checks <number> --required --watch --fail-fast`. Never poll checks or
   workers with repeated short waits. Inspect mergeability after the watch.
7. Merge only when the author/authority rule permits it. Synchronize `main`,
   remove the working label, and update the durable checkpoint.

Do not merge another author's PR without exact maintainer authorization. Do not
discard unrelated work or publish owner-private evidence while cleaning up.
