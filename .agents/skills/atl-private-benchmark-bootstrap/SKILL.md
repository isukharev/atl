---
name: atl-private-benchmark-bootstrap
description: Bootstrap a private ATL Jira or Confluence benchmark dataset for a new maintainer from a fresh clone with owner-only storage, explicit provider/backend authority, offline validation, and primary/holdout design. Use for private benchmark onboarding or private case authoring. Do not use for public synthetic evaluator work, routine live validation, or an already reviewed benchmark run.
---

# Bootstrap an ATL private benchmark

1. Read [Private benchmark onboarding](../../../docs/maintainers/private-benchmark-onboarding.md) completely, then use the existing private-workspace runbook only for lifecycle details it routes to.
2. Recover repository state before building or editing. Treat every existing private root, case, plan, run, baseline, and report as owner data; never enumerate an unreviewed root.
3. Record the exact private-root mutation, backend-read, backend-write, authoring-provider disclosure, benchmark-provider disclosure, benchmark-execution, promotion, pruning, and publication authorities. Default every omitted category to none. Treat assisted authoring itself as a provider interaction; default it to public contracts and content-free private templates.
4. Propose the smallest useful dataset: one read-only calibration task, one distinct same-class holdout, finite budgets, zero delegation, zero writes, and one provider/surface initially. Prefer an exact owned fixture over broad discovery.
5. Edit only the named owner-private root after explicit filesystem authority. Keep all private values out of the repository, issues, PRs, public logs, and ordinary development-agent context.
6. Use the current public schemas and strict evaluator commands. Run only offline `validate-run`, comparison validation when applicable, `private doctor`, and `private status` during bootstrap. Do not weaken a contract to obtain a pass.
7. Stop before agentless backend reads unless their exact live-read plan is authorized. Stop before provider qualification, plan creation, model execution, baseline promotion, pruning, or publication unless the current request separately authorizes that exact transition.
8. Report only aggregate case-family and primary/holdout coverage, offline validation, remaining authorities, and the next safe action. Never echo private paths or content.
