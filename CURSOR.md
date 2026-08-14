# Cursor guidance

Read [`AGENTS.md`](AGENTS.md) completely before repository work. It is the
binding cross-agent contract for architecture, authority, privacy, write safety,
issue-first work, verification, review, and handoff. This file is only a Cursor
compatibility route and does not duplicate or override those rules.

## Repository workflow

Choose the smallest relevant maintainer runbook:

- [Development and verification](docs/maintainers/development.md)
- [Efficient agent work](docs/maintainers/agent-efficiency.md)
- [Landing a change](docs/maintainers/landing-a-change.md)
- [Session recovery](docs/maintainers/session-recovery.md)
- [Live validation](docs/maintainers/live-validation.md)
- [Private benchmark onboarding](docs/maintainers/private-benchmark-onboarding.md)

The exact current command and output contracts live under
`docs/reference/cli/` and `docs/reference/output/`. Inspect `atl --help` or the
relevant parent help instead of copying the command tree into this file.

## Delegated Cursor sessions

A Cursor session is a delegated worker only when the caller's brief explicitly
says so. Then:

- stay inside the objective, files, authority, and non-goals in the brief;
- treat omitted edit authority as read-only and do not delegate again;
- preserve and report pre-existing dirty state;
- do not push, mutate GitHub, merge, release, or contact an authenticated live
  backend unless the exact action is authorized;
- for review tasks, report findings with file/line evidence and do not edit.

The root owns integration and final verification. Never add assistant
attribution or `Co-Authored-By` trailers to commits, PRs, or generated public
text.

## Skill boundary

`skills-src/` owns the ATL client workflows distributed to Claude Code and
Codex. `skills/` and `plugins/atl/skills/` are generated with
`make gen-plugins`; never edit them directly. Repository-maintenance skills in
`.agents/skills/` are development aids and are not shipped client content or an
alternative source of truth.

For a fresh-clone request to create an owner-local Jira or Confluence
evaluation dataset, follow the private benchmark onboarding runbook above.
Begin in a read-only plan, require an exact additional private root, isolate
ambient MCP and integration tools, and stop before backend access,
private-data disclosure, or benchmark execution unless the current request
grants that exact authority.
