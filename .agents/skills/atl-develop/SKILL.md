---
name: atl-develop
description: Implement or review changes in the ATL repository with the correct architecture owner, generated-tree boundary, focused tests, full gates, and privacy checks. Use for ATL code, documentation, tests, build tooling, refactors, or bug fixes. Do not use merely to operate the installed ATL CLI against Jira or Confluence.
---

# Develop ATL

1. Read repository `AGENTS.md`; it is binding.
2. Read [Development and verification](../../../docs/maintainers/development.md).
3. Run its read-only preflight and classify pre-existing dirty state before
   editing.
4. For non-trivial work, require an issue and public agent plan before code.
5. Find the canonical subsystem and documentation owner. Never edit generated
   client skill trees directly.
6. Iterate with focused tests. Once the integrated diff is stable, run the
   runbook's full gates, privacy scan, and one independent review.
7. Use `$atl-land-change` when a coherent planned slice is ready for a draft PR
   or when an existing PR needs review, CI, and merge handling.

Stop and report rather than guessing when a dirty path has unknown ownership,
the requested scope lacks authority, or the change would cross an architecture,
durable-format, security, or live-write boundary not covered by the plan.
