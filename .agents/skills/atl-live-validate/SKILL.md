---
name: atl-live-validate
description: Validate ATL behavior against configured Jira, Confluence, GitHub, or GitLab backends while preserving privacy, request bounds, write authority, owned-fixture scope, and ambiguous-outcome safety. Use for live integration, smoke, compatibility, VPN/connectivity, or fixture checks. Do not use for synthetic unit tests alone.
---

# Validate ATL live

1. Read repository `AGENTS.md`; it is binding.
2. Read [Live validation](../../../docs/maintainers/live-validation.md).
3. Prove what can be proven synthetically before contacting a backend.
4. Load only the isolated configuration identified by current owner
   instructions. Begin with bounded read-only status/doctor and exact reads.
5. Keep all observations generic in public output. Never echo or persist real
   hosts, IDs, titles, fields, values, content, credentials, or screenshots.
6. Before any write or deletion, present the exact owned targets, content,
   order, request cap, terminal state, reconciliation, and cleanup plan; require
   explicit authority for that plan.
7. Send non-replay-safe writes once. Reconcile any ambiguous outcome before
   considering another request. Preserve and classify failures privately.

Configured access is not write authority. Stop when a VPN/external dependency
must be repaired by its owner or a requested object is not owned by this work.
