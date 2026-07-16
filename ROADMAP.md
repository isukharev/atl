# atl roadmap

This is the public product roadmap for `atl`. It explains what users can expect,
what is being explored, and what is explicitly out of scope.

Roadmap items are not release commitments. Priorities can change based on
feedback, Data Center migration pressure, and safety findings.

## Shipped foundation

Recent beta work established the contracts the next wave builds on:

- process-wide read-only policy with pre-network mutation refusal;
- stable JSON error `kind` and remediation, explicit text/id projections, and
  named Jira list views;
- guarded Jira and Confluence writes, versioned derived views, contained mirror
  I/O, complete pagination signals, and build/update provenance;
- compact Jira field/history/reference/digest workflows, transient batch
  exports, Structure/board reads, and bounded Confluence page sections;
- Jira attachments/watchers and Confluence labels with review-bound mutation
  flows;
- guarded Jira worklog list/add and native Confluence blogpost creation for
  routine delivery updates;
- a complete multi-page Confluence review/sync chain: deterministic native
  semantic diff, hash-bound plan/apply, and selector-bound incremental refresh
  with explicit completeness and resume evidence;
- explicit historical Confluence selector pulls beyond the ordinary caps, with
  two-pass membership qualification and private exact-prefix resume state;
- absolute UTC synchronization boundaries, deterministic human display zones,
  and bounded GET-only diagnostics that qualify server/user/query time evidence
  without hidden calibration searches;
- a versioned offline capability catalog that maps exact agent task classes to
  bounded command/reference routes and derives access/output facts from the
  executable CLI contract;
- a typed remote-read-only MCP stdio server with seven bounded evidence tools,
  stable error classes, plugin distribution, and a synthetic Codex route that
  proves the same fifteen-GET quarterly portfolio result with zero writes;
- a same-runtime Claude Code CLI/MCP portfolio comparison whose three-run MCP
  median preserves correctness and backend traffic while materially reducing
  turns, context, cost, and duration.
- explicit package-update ownership: Homebrew launchers delegate exclusively to
  `brew upgrade atl`, while a consolidated egress contract separates read-only,
  update-disable, backend traffic, and externally enforced air-gap controls.

## Now

The daily-operation, Confluence review/sync, first agent-evaluation sequence,
package ownership, and complete historical bootstrap are shipped. Current work
keeps scale ready for the next stable release:

- add bounded concurrency or rate-limit scheduling only as a complete
  cross-request control that covers pages, comments, assets, and Jira-macro
  expansion rather than accelerating one partial path.

## Next

Work likely to follow once the current sequence is stable.

- Extend read-only MCP only when benchmark evidence justifies another bounded
  app-level tool; Structure, mirror writes, pull/status, and full page bodies
  remain CLI-only for now.
- Sync at scale after incremental correctness:
  - bounded concurrency and rate-limit controls;
  - standalone fragment inventory/check commands when diff/plan evidence shows
    that a separate surface is useful.
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
- TTY-aware output defaults and a universal JSON field-projection language.
  Existing explicit projections, named views, and standard JSON tooling remain
  preferable until concrete gaps justify a broader contract.
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

- Lossy Markdown-to-CSF updates for existing pages. (The shipped `conf apply`
  path is non-lossy by construction: it merges block-by-block, keeps untouched
  blocks byte-identical, preserves opaque fragment bytes, and fails closed on
  anything it cannot convert faithfully.)
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
