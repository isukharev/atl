# Agent plugins: single source, generated per platform

`atl` ships its skills to two agent platforms — Claude Code and Codex — from one
source of truth. This page is the maintainer guide for that pipeline.

## Layout

```
skills-src/                 ← SOURCE OF TRUTH: edit here, and only here
  <skill>/SKILL.md            plain markdown + a few {{atl.var}} placeholders
  <skill>/reference/*.md      shared reference material (same placeholder rules)
  <skill>/agents/openai.yaml  Codex-only skill metadata (UI text, invocation policy)

skills/                     ← GENERATED: the Claude Code plugin (openai.yaml omitted)
plugins/atl/skills/         ← GENERATED: the Codex plugin (openai.yaml included)

scripts/gen-plugins/        the generator (Go; unit-tested)
```

Both output trees are committed — the Claude Code marketplace serves `skills/`
straight from the repo, so they cannot be gitignored. Every generated `.md`
carries a header comment naming its source file; if you find yourself editing a
file with that header, stop and edit the `skills-src/` original instead.

## The edit loop

1. Edit files under `skills-src/`.
2. `make gen-plugins` — regenerates both output trees wholesale.
3. Commit **all three trees in the same PR**.

CI runs `make check-plugins` (regenerate + `git status --porcelain` over the
outputs), so a stale or hand-edited output tree fails the build.

## Placeholders

Platform-specific strings use `{{atl.<name>}}` placeholders; the per-platform
values live in the `platforms` table in `scripts/gen-plugins/main.go`:

| Placeholder | Claude Code | Codex |
|---|---|---|
| `{{atl.setup_cmd}}` | `/atl:setup` | `$setup` |
| `{{atl.agent_name}}` | Claude Code | Codex |
| `{{atl.agent_short}}` | Claude | Codex |
| `{{atl.guidance_file}}` | CLAUDE.md | AGENTS.md |
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
  `agents/openai.yaml`) is a hard error — extend `renderFile` deliberately.

## How to extend

- **New platform-specific string:** add a `{{atl.<name>}}` placeholder in the
  source, add the value to **every** platform's var map (a unit test fails if
  the maps diverge), regenerate. If the variable count starts growing past a
  handful, treat it as a signal the platforms are diverging and reconsider the
  shared text rather than adding more knobs.
- **New skill:** create `skills-src/<name>/SKILL.md` (frontmatter first —
  `name`, `description` with USE WHEN) plus `agents/openai.yaml` for Codex
  (display name, `allow_implicit_invocation`; set it `false` for anything that
  installs software or writes without close user control), regenerate. Update
  README.md / README.ru.md skill lists and CHANGELOG in the same PR.
- **New platform:** add an entry to `platforms` in the generator (output root,
  var values, whether platform metadata files are emitted), regenerate, and add
  the new output tree to `check-plugins` in the Makefile.

## Versioning

Plugin manifests are **not** generated: `.claude-plugin/plugin.json` (Claude)
and `plugins/atl/.codex-plugin/plugin.json` (Codex). Their `version` fields are
the update triggers on user machines — bump **both** to the CLI version in the
release prep commit (see `docs/RELEASING.md`); a stale version means installed
plugins silently never update.
