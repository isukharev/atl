---
name: atl-develop
description: Implement or review changes in the ATL repository with the correct architecture owner, generated-tree boundary, risk-classified verification, and privacy checks. Use for ATL code, documentation, tests, build tooling, refactors, or bug fixes. Do not use merely to operate the installed ATL CLI against Jira or Confluence.
---

# Develop ATL

1. Treat the active repository `AGENTS.md` instruction chain as binding. Do
   not reread the whole file when Codex already supplied it in session context
   unless evidence says the active copy is missing or stale.
2. Read [Development and verification](../../../docs/maintainers/development.md).
3. Run the runbook's literal read-only snapshot once per session boundary and
   classify every dirty path before executing Makefiles/scripts or editing. Do
   not repeat identity/toolchain probes per commit.
4. For non-trivial work, require an issue and public agent plan that states the
   low, standard, or high process class before code.
5. Find the canonical subsystem and documentation owner. Never edit generated
   client skill trees directly.
6. Iterate with focused tests. Once stable, run only impact-mapped gates plus
   the class-required privacy/review contour. Raw Go uses the runbook's automatic
   toolchain environment; never derive or pin `GOROOT`.
7. Use `$atl-land-change` when a coherent planned slice is ready for a draft PR
   or when an existing PR needs review, CI, and merge handling.

Stop and report rather than guessing when a dirty path has unknown ownership,
the requested scope lacks authority, or the change would cross an architecture,
durable-format, security, or live-write boundary not covered by the plan.
