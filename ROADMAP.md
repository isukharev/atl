# atl roadmap

This is the public product roadmap for `atl`. It is intentionally shorter than
the internal product plan in `PRODUCT-ROADMAP.md`: this file explains what users
can expect, what is being explored, and what is explicitly out of scope.

Roadmap items are not release commitments. Priorities can change based on
feedback, Data Center migration pressure, and safety findings.

## Now

Work planned for the next release cycle.

- Agent and human ergonomics:
  - richer error envelopes with stable `kind` and remediation text;
  - TTY-aware output defaults while keeping JSON stable for scripts;
  - field projection for smaller machine-readable outputs.
- Read-only safety:
  - global CLI read-only policy for agent sessions and CI;
  - write commands fail before network access when read-only policy is active.
- Early MCP distribution:
  - read-only `atl mcp serve` surface for search/get/pull/status;
  - MCP responses return paths to local mirror files, not page bodies.
- Table-stakes Jira and Confluence coverage:
  - Jira worklogs, assign/watchers, and attachment upload/download;
  - Confluence label management and blogpost creation.
- Packaging and trust:
  - clearer zero-egress/security model documentation;
  - release and package management improvements.

## Next

Work likely to follow once the "Now" items are stable.

- Sync at scale:
  - first-class bulk `conf push <dir>` workflow;
  - unbounded space pull and removal of quiet CQL caps;
  - incremental pull by change watermark;
  - concurrency and rate-limit controls.
- Safety and review:
  - semantic `conf diff`;
  - fragment inventory/check commands;
  - machine-readable `conf plan` / deterministic apply.
- Migration readiness:
  - `conf validate --cloud-compat` to detect content that may lose fidelity
    during Cloud/ADF migration.
- Windows support:
  - config-dir and path handling;
  - Scoop/Winget packaging.

## Later

Important directions that need more validation or depend on earlier safety work.

- Write-capable MCP tools that respect the global read-only policy and version
  gate.
- Markdown input only for creating new pages, never for updating existing page
  bodies.
- Draw.io source workflows and macro insertion helpers.
- Public CSF parser/validator library.
- Archive/export workflows for full-fidelity Data Center backups and migration
  preparation.

## Cloud

Cloud support is not planned for immediate development. If it is built, it will
be ADF-native, not a storage-format compatibility layer.

The trigger to start Cloud work is explicit user demand: multiple Data Center
users or design partners asking for Cloud migration/write support, or sustained
issue volume around Cloud/ADF. The first Cloud step would be fidelity spikes:
round-trip ADF tests, JSON hashing/canonicalization, and endpoint format checks.

## Not planned

- Lossy Markdown-to-CSF updates for existing pages.
- Hosted RAG/vector indexing, cloud brokers, or background services that move
  your content out of your environment.
- WYSIWYG/rich text editor.
- Terminal UI.
- Space administration, user management, workflow configuration, or ScriptRunner
  style automation.
- A plugin system inside `atl`; composition should happen through the CLI, skills,
  MCP, shell scripts, or GitHub workflows.
- Automatic merge of CSF bodies.

## Influence the roadmap

- Open a feature request for concrete user-facing needs.
- Open a roadmap task when the work already maps to a roadmap area.
- Use Discussions for broad product questions, alternatives, and design feedback.
- Link real examples whenever possible: page shape, macro type, Jira workflow,
  Data Center version, and the command you wanted to run.
