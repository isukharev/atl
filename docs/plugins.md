# Agent plugins: single source, generated per platform

`atl` ships its skills to two agent platforms — Claude Code and Codex — from one
source of truth. This page is the maintainer guide for that pipeline.

## Layout

```
skills-src/                 ← SOURCE OF TRUTH: edit here, and only here
  routing.v1.json             provider-neutral logical ids and exclusive routing boundaries
  <skill>/SKILL.md            plain markdown + a few {{atl.var}} placeholders
  <skill>/reference/*.md      shared reference material (same placeholder rules)
  <skill>/agents/openai.yaml  Codex-only skill metadata (UI text, invocation policy)

skills/                     ← GENERATED: the Claude Code plugin (openai.yaml omitted)
plugins/atl/skills/         ← GENERATED: the Codex plugin (openai.yaml included)

.mcp.json                   ← shared local read-only MCP server definition
plugins/atl/.mcp.json       ← generated copy for the Codex plugin

scripts/gen-plugins/        the generator (Go; unit-tested)
```

Both output trees and the Codex MCP copy are committed — the Claude Code marketplace serves `skills/`
straight from the repo, so they cannot be gitignored. Every generated `.md`
carries a header comment naming its source file; if you find yourself editing a
file with that header, stop and edit the `skills-src/` original instead.

## The edit loop

1. Edit files under `skills-src/`.
2. `make gen-plugins` — regenerates both output trees wholesale and refreshes
   the Codex `.mcp.json` copy.
3. Commit **all three trees in the same PR**. When MCP config changes, commit
   root `.mcp.json`, the generated Codex copy, and both manifest references too.

CI runs `make check-plugins` (validate metadata and routing, regenerate, then
`git status --porcelain` over the outputs), so malformed metadata and stale or
hand-edited output trees fail the build. Complete source, routing, and corpus
validation finishes before the generator removes either output tree. A runtime
filesystem failure during publication may leave a generated tree partial;
rerun `make gen-plugins` to reconstruct both derived trees. The same target runs
`check-skill-safety`: a shell fence preceded by
`<!-- atl:read-only-shell -->` must begin with the inherited
`export ATL_READ_ONLY=1` guard. Designated read-only workflow skills also have
minimum marker coverage, so deleting all markers cannot make the check pass.

## Placeholders

Platform-specific strings use `{{atl.<name>}}` placeholders; the per-platform
values live in the `platforms` table in `scripts/gen-plugins/main.go`:

| Placeholder | Claude Code | Codex |
|---|---|---|
| `{{atl.setup_cmd}}` | `/atl:setup` | `$setup` |
| `{{atl.agent_name}}` | Claude Code | Codex |
| `{{atl.agent_short}}` | Claude | Codex |
| `{{atl.guidance_file}}` | CLAUDE.md | AGENTS.md |
| `{{atl.plugin_update_cmd}}` | `/plugin update atl` | `codex plugin update atl` |
| `{{atl.setup_invocation_note}}` | *(empty — line dropped)* | how to invoke the setup skill |

Rules the generator enforces:

- An unresolved `{{atl.*}}` placeholder is a **hard error** (typo guard), and
  near-miss typos (`{{atl.Setup_cmd}}`, `{{ atl.setup_cmd }}`) are caught by a
  looser stray-remnant check. Plain
  `{{...}}` without the `atl.` prefix passes through untouched — Jira wiki
  markup uses `{{text}}` for monospace, and the jira skill documents it.
- A line consisting solely of a placeholder whose value is empty is dropped,
  without leaving a blank gap. Use this for per-platform notes.
- A file type the generator doesn't know (anything but `.md` and
  `agents/openai.yaml`, plus the consumed top-level `routing.v1.json`) is a hard
  error — extend `renderFile` deliberately.

## Discovery and identity contract

Agent clients initially see a bounded catalog of skill names and descriptions;
the full body is loaded only after selection. Keep the first sentence and the
`USE WHEN` / `DO NOT USE WHEN` boundaries concise and decisive. The routing
declared classes are mutually exclusive: focused workflow, cross-service
discovery, direct single-service work, and the `atl` orientation/mirror role
each have one intended route. Code-only Jira or Confluence mentions are
declared no-activation cases; provider behavior is measured separately.

`skills-src/routing.v1.json` records provider-neutral logical ids, implicit
policy, ownership classes, and exclusion assertions. The synthetic prompts
in `benchmarks/agent-eval/skill-routing.v1.json` are future model-in-the-loop
inputs; the offline oracle uses their reviewed `task_class` annotations, not a
pretend keyword classifier. It proves that every declared boundary has a
reviewed witness and that each case has exactly one route or an explicit
no-activation result:

```sh
make check-skill-routing
```

Explicit corpus cases also bind `invoked_skill` to the exact bare `$skill`
token in the retained prompt; implicit cases cannot set it.

The logical id must equal the directory and `SKILL.md` name. Codex
`default_prompt` uses the documented bare `$skill` invocation; an installed
plugin may present a namespaced inventory id such as `atl:jira` without changing
that logical identity. Codex `short_description` values contain 25..64 Unicode
characters, as required by the client metadata contract.
`disable-model-invocation` and `allow_implicit_invocation` must be exact
inverses. The strict source parser rejects unknown/duplicate/missing fields,
malformed scalar forms, and wrong default-prompt targets. Repository contract
tests and private benchmark provisioning separately reject generated or
installed inventory drift.

## How to extend

- **New platform-specific string:** add a `{{atl.<name>}}` placeholder in the
  source, add the value to **every** platform's var map (a unit test fails if
  the maps diverge), regenerate. If the variable count starts growing past a
  handful, treat it as a signal the platforms are diverging and reconsider the
  shared text rather than adding more knobs.
- **New skill:** create `skills-src/<name>/SKILL.md` (frontmatter first —
  `name`, `description` with both `USE WHEN` and `DO NOT USE WHEN`) plus
  `agents/openai.yaml` for Codex
  (display name, `allow_implicit_invocation`; set it `false` for anything that
  installs software or writes without close user control). Add the logical id
  and its boundaries to `routing.v1.json`, add owned-route and exclusion corpus
  cases, regenerate, and update README.md / README.ru.md and CHANGELOG in the
  same PR.
- **New platform:** add an entry to `platforms` in the generator (output root,
  var values, whether platform metadata files are emitted), regenerate, and add
  the new output tree to `check-plugins` in the Makefile.

## Versioning

Plugin manifests are **not** generated: `.claude-plugin/plugin.json` (Claude)
and `plugins/atl/.codex-plugin/plugin.json` (Codex). Their `version` fields are
the update triggers on user machines — bump **both** to the CLI version in the
release prep commit (see `docs/RELEASING.md`); a stale version means installed
plugins silently never update.
