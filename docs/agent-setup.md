# Set up `atl` for coding agents

`atl` ships the same focused Jira, Confluence, setup, and workflow skills for
Claude Code and Codex. The plugin adds guidance plus a typed read-only MCP
surface; the CLI remains the path for durable mirrors and every write.

Complete [CLI setup](getting-started.md) first, or let the explicit setup skill
guide those same steps.

## Claude Code

Add this repository as a marketplace, install the plugin, then run setup:

```text
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

Enable marketplace auto-update if you want plugin skills to follow CLI
releases, and start a new Claude Code session after installing or refreshing
the plugin.

## Codex

Install from the repository marketplace:

```sh
codex plugin marketplace add isukharev/atl
codex plugin add atl@atl
```

Start a new Codex session and invoke the explicit `setup` skill from `/skills`
or with `$setup`. Optional workflow personalization is separate and
consent-gated through `$onboarding`; setup is complete without it.

The generated plugin supplies the per-server
`CODEX_MCP_PROTOCOL_VERSION=2026-07-28` marker. Codex 0.147 uses modern
stateless MCP only when the user also enables its under-development global
feature and restarts:

```sh
codex features enable mcp_2026_07_28
```

Marker only or feature only remains on the supported legacy handshake. The
plugin cannot enable the global feature, and the marker selects client protocol
behavior rather than authenticating ATL or proving package provenance.

## Choose the execution surface

| Need | Preferred surface | Why |
|---|---|---|
| Narrow transient Jira/Confluence evidence | Typed MCP tool | Bounded arguments and typed result |
| Repeatable offline analysis or editing | CLI mirror | Native bytes, baselines, local tools |
| Raw Structure forest/values or exports | CLI | Full explicit projection and file output |
| Any create, update, transition, comment, or push | CLI | Review-bound write gates |
| Capability discovery | `atl capabilities` | Offline, versioned route catalog |

The MCP server is read-only by construction. It exposes no shell, raw REST,
arbitrary filesystem, or mutation tool.

Inspect the offline catalog before loading a broad reference:

```sh
ATL_NO_UPDATE=1 atl capabilities --task jira/evidence
ATL_NO_UPDATE=1 atl capabilities --task confluence/edit -o text
```

Standalone clients may start a closed service profile:

```sh
ATL_NO_UPDATE=1 atl mcp serve --service jira
ATL_NO_UPDATE=1 atl mcp serve --service confluence
ATL_NO_UPDATE=1 atl mcp serve --service offline
```

The default `atl mcp serve` inventory is still read-only. See [mcp.md](mcp.md)
for exact tools and output limits.

At the start of a connection, read `atl://runtime` once to confirm the selected
`default|jira|confluence|offline` profile, global read-only policy/source, and
plugin compatibility classification. Its `access:"hard_read_only"` field is
the structural MCP guarantee; the nested global policy is separate and may be
inactive. The content-free snapshot is captured before stdio and cannot change
within that server process. Its read result is `ttlMs:0` and
`cacheScope:"private"`: retain it only as an observation of that process, and
restart the server after changing persisted config, `ATL_READ_ONLY`, the global
`--read-only` flag, or plugin startup markers.

## Keep the mirror out of the code repository

Agree on one explicit mirror directory and persist it in the agent's private
environment, not in committed project config:

```sh
export ATL_MIRROR_ROOT="$HOME/.atl/example-workspace"
```

Without that variable or `--into`, Confluence and Jira use different built-in
fallback directories. An explicit root removes ambiguity across sessions.

## Read-only investigations

The guard must be exported before a multi-command investigation so every child
process inherits it:

```sh
export ATL_READ_ONLY=1

atl jira issue search --jql 'assignee = currentUser()' --limit 20
atl conf search --cql 'type = page' --limit 20
```

Do not remove the guard inside the workflow. Exit `8` with
`policy:"read_only"` is a deliberate refusal, not permission to retry with the
policy disabled.

## Version skew and fallback

Plugin skills and the CLI release under the same product version. Generated
plugin MCP definitions pass a separate interface-contract marker and their
manifest product version to `atl mcp serve`; they also set the public Codex
protocol-mode marker described above. An incompatible marked interface
fails with exit `2` before config, credentials, dependency construction, or
network access. Product-version mismatch is computed separately and does not
reject an otherwise compatible interface. `atl://runtime` reports only the
closed `unverified|match|mismatch` product classification, not either version;
compare `atl version` with the installed plugin or manifest when diagnosing
skew. Malformed persisted configuration also fails before protocol output.

Protocol selection is independent of those startup checks. ATL remains
dual-era: modern `2026-07-28` uses `server/discover`, while legacy
`2025-11-25` uses initialize/initialized. Its one-page tool inventory carries
the required `ttlMs:0` and `cacheScope:"public"` cache fields in both eras.
Resource discovery and `atl://capabilities` reads remain public; the
invocation-specific `atl://runtime` read is private, with `ttlMs:0`, in both
eras.

A newly generated plugin used with an older binary fails through that binary's
normal unknown-flag parsing. An older unmarked plugin used with a newer binary
cannot be distinguished from supported standalone `atl mcp serve`, so it is
accepted as `unverified`; symmetric rejection is not claimed. If a documented
command is unknown or marked MCP startup is refused:

1. run `atl version`;
2. inspect the installed plugin version;
3. update the older side;
4. start a new agent session.

The CLI remains usable if MCP registration is unavailable. Do not replace a
missing typed tool with an improvised raw REST call; use the documented CLI
route or report the gap.

## Agent safety contract

- Search broadly, then read narrowly.
- Check `complete`, truncation, and cursor fields before claiming absence.
- Keep content and credentials inside the configured environment.
- Use Markdown views for orientation; preserve native write substrates.
- Preview and review every write, and never auto-retry a write.
- Branch on stable `kind` and numeric exit `code`, not backend prose.

For concrete workflows see [agent-recipes.md](agent-recipes.md). For edits see
[safe-writes.md](safe-writes.md).
