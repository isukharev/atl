---
name: atl-land-change
description: Carry an ATL repository change from a shaped issue through linked branch, draft PR, integrated review, hosted CI, authorized merge, main synchronization, and cleanup. Use when opening, reviewing, checking, merging, or recovering an ATL PR. Do not use for local exploration that is not entering the issue and PR lifecycle.
---

# Land an ATL change

1. Read repository `AGENTS.md`; it is binding.
2. Read [Landing a change](../../../docs/maintainers/landing-a-change.md).
3. Before opening a draft, verify issue plan, branch ownership, author identity,
   dirty state, and the current coherent scope. Open it early enough to preserve
   traceability.
4. Before marking ready, verify the final diff, local gates, privacy scan, and
   issue/parent/roadmap links. Use one independent integrated-diff review by
   default.
5. Fix material findings and rerun only affected gates; request a bounded
   follow-up for material correctness or security fixes.
6. Mark ready, wait for required hosted checks on the reviewed head, and inspect
   mergeability.
7. Merge only when the author/authority rule permits it. Synchronize `main`,
   remove the working label, and update the durable checkpoint.

Do not merge another author's PR without exact maintainer authorization. Do not
discard unrelated work or publish owner-private evidence while cleaning up.
