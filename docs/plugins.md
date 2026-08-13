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

.mcp.json                   ← generated Claude plugin MCP definition
plugins/atl/.mcp.json       ← generated Codex plugin MCP definition

scripts/gen-plugins/        the generator (Go; unit-tested)
internal/plugincontract/    compiled interface owner shared by generator and CLI
```

Both output trees and both MCP definitions are committed — the Claude Code
marketplace serves `skills/` and the root manifest consumes root `.mcp.json`,
so they cannot be gitignored. Every generated `.md` carries a header comment
naming its source file; if you find yourself editing a file with that header,
stop and edit the `skills-src/` original instead.

## The edit loop

1. Edit files under `skills-src/`.
2. `make gen-plugins` — regenerates both output trees wholesale, both manifest-
   bound `.mcp.json` definitions, and the versioned skill-catalog companion.
3. Commit **every generated output in the same PR**. When MCP config changes,
   commit root `.mcp.json` and the generated Codex definition. The generator
   verifies that both consuming manifests retain the exact `./.mcp.json`
   reference.

CI runs `make check-plugins` (validate metadata and routing, regenerate, then
`git status --porcelain` over the outputs), so malformed metadata and stale or
hand-edited output trees fail the build. Complete source, routing, and corpus
validation finishes before the generator removes either output tree. A runtime
filesystem failure during publication may leave generated output partial;
rerun `make gen-plugins` to reconstruct it. The same target runs
`check-skill-safety`: a shell fence preceded by
`<!-- atl:read-only-shell -->` must begin with the inherited
`export ATL_READ_ONLY=1` guard. Designated read-only workflow skills also have
minimum marker coverage, so deleting all markers cannot make the check pass.
The same check scans every shipped skill-source shell fence for a command-position
`atl ... | jq` pipeline, requires a `bash` fence, and requires `set -o pipefail`
earlier in that fence. This prevents a successful local projection from hiding
ATL's structured non-zero exit without advertising non-portable syntax as
POSIX `sh`.

## Placeholders

Platform-specific strings use `{{atl.<name>}}` placeholders; the per-platform
values live in the `platforms` table in `scripts/gen-plugins/main.go`:

| Placeholder | Claude Code | Codex |
|---|---|---|
| `{{atl.setup_cmd}}` | `/atl:setup` | `$setup` |
| `{{atl.agent_name}}` | Claude Code | Codex |
| `{{atl.agent_short}}` | Claude | Codex |
| `{{atl.guidance_file}}` | CLAUDE.md | AGENTS.md |
| `{{atl.plugin_update_instructions}}` | interactive `/plugin update atl` guidance | ordered marketplace upgrade, reinstall, and new-session guidance |
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

`plugins/atl/skill-catalog.v1.json` is a generated companion contract for
offline consumers of the Codex package. It contains the sorted skill names and
implicit-invocation policy plus a SHA-256 inventory of every regular file under
`plugins/atl/skills/`. It deliberately lives outside that discovery root, so it
is included in the complete plugin-package identity without appearing as an
extra skill or changing the provider-visible skill-tree digest. Do not edit the
companion by hand; `make gen-plugins` derives it from the same validated source
snapshot and in-memory Codex render as the generated tree.

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
release prep commit and run `make gen-plugins` (see `docs/RELEASING.md`); a
stale version means installed plugins silently never update. Each generated
MCP invocation derives its product-version marker directly from its consuming
manifest, so there is no third release version to synchronize manually. Its
separate interface marker comes from `internal/plugincontract.InterfaceVersion`,
the same compiled owner used by the binary startup check.
