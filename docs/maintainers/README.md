# Maintainer workflows

Use this index for repository work. User-facing command behavior belongs in the
[task-first documentation](../README.md); binding cross-agent invariants remain
in [`AGENTS.md`](../../AGENTS.md). These runbooks hold repeatable procedure so
agents do not have to reconstruct it from old issues, transcripts, or CI logs.

| Task | Runbook |
|---|---|
| Start or implement a repository change | [Development and verification](development.md) |
| Carry an issue and PR through review, CI, and merge | [Landing a change](landing-a-change.md) |
| Resume after compaction, interruption, or a new session | [Session recovery](session-recovery.md) |
| Exercise a configured backend safely | [Live validation](live-validation.md) |
| Bootstrap an owner-local Jira or Confluence evaluation dataset | [Private benchmark onboarding](private-benchmark-onboarding.md) |

Other canonical maintainer references:

- [Architecture](../architecture.md)
- [GitHub issue model](../github-issue-workflow.md)
- [Generated client plugins and skills](../plugins.md)
- [Release procedure](../RELEASING.md)
- [Documentation catalog](../catalog.v1.json)
- [CLI documentation coverage](../command-coverage.v1.json)
- [Change-impact verification map](../maintainer-impact.v1.json)
- [Production hotspot and timing ratchets](../maintainability-ratchets.v1.json)
- [Agent evaluator substrate decision](agent-evaluator-substrates.md)
- [Private evaluator lifecycle](../agent-benchmark-private-workspace.md)
- [Private benchmark onboarding](private-benchmark-onboarding.md)

Repository-scoped Codex skills under `.agents/skills/` are concise routers to
these runbooks. They are development aids, not shipped ATL client skills. Edit
client skills only in `skills-src/` and regenerate their output trees with
`make gen-plugins`.

## Add or change a repository skill

1. Create or update one canonical runbook in this directory. Register it in
   `docs/catalog.v1.json` with lane `maintainers` and link it from this index.
2. Keep the skill tree exact: `SKILL.md` plus a four-line
   `agents/openai.yaml`. The skill body routes to the runbook rather than
   copying it.
3. Add the skill, its required activation terms, and at least two positive and
   two negative activation examples to `.agents/skills/catalog.v1.json`.
4. Route `CLAUDE.md` to every cataloged runbook so provider agents share the
   canonical procedure. Never copy the skill into `skills-src/`, `skills/`, or
   `plugins/atl/skills/`.
5. Run `make check-repository-skills` and `make check-docs-catalog`. If Make or
   workflow wiring changed, also run `make check-maintainer-contract`.

The checker enforces names, strict skill metadata/tree shape, activation
fixtures, context budgets, non-symlink references, catalog routes, generated
tree separation, and CI/release wiring. Update the catalog and canonical
runbook first; an undeclared directory fails intentionally.

## Instruction ownership

Update one owner instead of copying procedure back into root instructions:

| Knowledge | Owner |
|---|---|
| Binding product, authority, architecture, safety, privacy, and handoff invariants | `AGENTS.md` |
| Provider compatibility | provider file such as `CLAUDE.md`, as a route to shared owners |
| Preflight, code ownership, and test selection | `development.md` |
| Issue, review, CI, merge, and cleanup sequence | `landing-a-change.md` |
| Context-loss reconstruction and checkpoints | `session-recovery.md` |
| Live read/write/cleanup boundaries | `live-validation.md` |
| Fresh-clone private dataset design and offline case authoring | `private-benchmark-onboarding.md` |
| CLI flags and behavior | `docs/reference/cli/` |
| JSON, exits, completeness, and recovery | `docs/reference/output/` |
| CLI-leaf and mutation-safety documentation routes | `docs/command-coverage.v1.json` |
| Changed-path to verification-gate selection | `docs/maintainer-impact.v1.json` |
| Reviewed production growth allowances and observe-only gate timings | `docs/maintainability-ratchets.v1.json` |
| Generated client skill pipeline | `docs/plugins.md` and `skills-src/` |
| Release trust and publication | `docs/RELEASING.md` |
| Private evaluator operation | `docs/agent-benchmark-private-workspace.md` |
| New-maintainer private benchmark bootstrap | `docs/maintainers/private-benchmark-onboarding.md` |
