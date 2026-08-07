# atl roadmap

This is the public product roadmap for `atl`. It explains what users can expect,
what is being explored, and what is explicitly out of scope.

Roadmap items are not release commitments. Priorities can change based on
feedback, Data Center migration pressure, and safety findings.

## Shipped foundation

The released foundation and current `main` provide:

- lossless native Jira/Confluence mirrors with derived Markdown views,
  non-destructive refresh, three-way reconciliation, backend identity binding,
  and deterministic native semantic diff;
- a process-wide read-only policy plus preview/hash/version-gated Jira and
  Confluence mutations with at-most-once transport and explicit ambiguous-outcome
  reconciliation;
- qualified bounded Jira fields, history, boards, Structure, artifact graphs,
  experimental Development identities, and Confluence sections, tables,
  attachments, and comment threads;
- an offline capability catalog and a typed read-only MCP stdio server with
  twenty-three bounded evidence tools, two content-free mirror snapshots, and
  closed Jira/Confluence/offline profiles;
- signed self-update and release provenance, contained filesystem writes,
  cross-platform Linux/macOS evidence, and a required Windows source-compile
  gate without a Windows support claim;
- generated Claude Code and Codex skills plus deterministic public and private
  evaluation contracts kept outside ordinary product test/package paths;
- explicit historical Confluence pulls beyond ordinary selector caps, bounded
  scheduling, durable resume/publication state, and advisory Cloud-compatibility
  inventory without a Cloud write-path claim.

## Now

The current release line is v0.7.0; published artifacts are identified by the
exact signed release tag. New surface remains evidence- or concrete-workflow-
gated; current owner and colleague use is valid product evidence and does not
impose a user-count freeze.

## Next

Work likely to follow once the current sequence is stable.

- Extend read-only MCP only when benchmark evidence justifies another bounded
  app-level tool; Structure, mirror writes, pull/status, and full page bodies
  remain CLI-only for now.
- Migration readiness beyond the shipped advisory `conf validate --cloud-compat`
  rule pack: later packs as Atlassian's documentation moves, and — only if
  demand justifies it — space-level reporting and third-party app assessment,
  both of which are deliberately out of scope today.
- Windows runtime support and Scoop/Winget packaging only after the existing
  source-compile gate is supplemented by runtime, install, update, and recovery
  evidence.

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
- Open a question/support issue for product questions, alternatives, and design
  feedback.
- Link real examples whenever possible: page shape, macro type, Jira workflow,
  Data Center version, and the command you wanted to run. Sanitize private
  hosts, object identifiers, content, and user/company data first.
