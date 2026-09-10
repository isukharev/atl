# Broker conformance and deployment evidence

Use this runbook to reconcile the implemented subset before admission or to
prepare a separately authorized deployment check. The [semantic contract](../broker-contract.md)
owns schemas, limits and consistency; [live validation](live-validation.md)
owns external authority and privacy. This document grants no backend,
deployment, policy-change or cleanup authority.

## Three distinct claims

1. **Source support:** the selected revision has an available registry row,
   composed service, client consumer and strict argument/effect contract.
2. **Hermetic admission:** independent integrated review and impact-selected
   hosted gates passed for that exact head/base. A test name, local patch,
   earlier green revision or registry flag alone is insufficient.
3. **Deployment qualification:** the actual CLI, Broker, authority and backend
   versions passed an approved owned-fixture allow/deny/revoke/recovery plan.
   Synthetic peers do not establish backend permissions or behavior.

Record these separately. Missing services or authority phases, unqualified
backend versions and stronger-than-supported consistency requirements must not
be converted into supported deployment claims. General direct-mode
[compatibility](../compatibility.md) is not Broker-specific qualification.

## Implemented subset and scope

This is a scope summary, not another executable CLI inventory. Exact flags and
result shapes remain in the linked references. Read-only MCP availability does
not imply that every CLI operation has an MCP counterpart.

| Consumer / family | Selector and qualification | Boundary |
|---|---|---|
| [Jira issue get](../reference/cli/jira-issues.md#atl-jira-issue-get), execution v1 | One key; explicit `summary`, `description`, `updated`; qualified numeric ID/project/update evidence | Authorized metadata then business read; `identity_snapshot_v1`, no arbitrary fields/JQL |
| [Confluence page get](../reference/cli/confluence-pages.md#atl-conf-page-get), execution v1 | One numeric ID, native storage or metadata; space/version/ancestor evidence | Exact page; no nested-resource grant or atomic current-space claim |
| [Jira project page](../reference/cli/jira-issues.md#atl-jira-issue-project-page), execution v2 / discovery v3 | One project and bounded cursor; up to 15 issues, summary/description or identity-only | Identity/business pages separately qualified; each page reauthorizes; not a complete-project snapshot |
| [Jira attachment get](../reference/cli/jira-issues.md#atl-jira-issue-attachment-listgetupload), execution v3 / internal discovery v4 | One issue key and numeric attachment ID; metadata before open and each release | CLI-only, 16 MiB / 60 seconds, one body GET; `step_snapshot_v1`; verified terminal before atomic local publication |
| [Comment preview/apply](../reference/cli/jira-issues.md#atl-jira-issue-comment-previewaddlistdelete), execution v1 / discovery v2 | Canonical issue, exact native body, actor/evidence and ticket; apply additionally needs exact proposal and last-hop write clearance | Explicit host opt-in and existing journal; one append, no update/delete/create/upload grant |
| [Comment outcome](../reference/cli/jira-issues.md#atl-jira-issue-comment-previewaddlistdelete), execution v1 / discovery v2 | One same-owner ticket, independently configured observer session | Journal only; zero Jira reads and no ambiguous-mutation replay |
| [Qualified corpus handoff](../reference/cli/local-artifacts.md#atl-corpus-handoff-qualified), cache v2 | Clean complete sealed Confluence-only generation, authority-resolved capture scope and immutable local evidence | Not execution permission; no Broker capture/refresh, Jira/mixed generations or generations with comments/attachments |

Discovery is advisory. Its ordinary CLI/MCP resources cover the documented
v2/v3 discovery families; attachment discovery v4 stays inside the download
command. Unsupported surfaces remain explicit: free-form JQL/CQL, generic HTTP,
filename-selected Broker downloads, range/resume, exports without a closed
operation, whole-issue create/update/delete, Confluence writes, attachment
upload/delete and general mirror/corpus capture do not follow from this table.
A comment grant does not authorize a remote-link write.

## Hermetic evidence owners

Use existing tests, not a parallel runner. Links locate evidence; record tests
and subcases actually executed at the reviewed head.

| Concern | Evidence owner |
|---|---|
| Exact CLI reads without PAT-store access; zero-I/O denial, drift and leases | [CLI fixture](../../internal/cli/broker_client_test.go), [read service](../../internal/app/broker_read_test.go), [leases](../../internal/app/broker_read_lease_test.go) |
| Advisory discovery and sealed-cache reuse | [selected MCP discovery](../../internal/brokerserver/discovery_process_test.go), [selected corpus CLI](../../internal/brokerserver/cache_corpus_cli_process_test.go), [cross-context reuse](../../internal/brokerserver/cache_corpus_process_test.go) |
| Project-page CLI/MCP output, sibling/field denial and stale sessions | [selected project-page oracle](../../internal/brokerserver/project_page_process_test.go) |
| Comment preview/apply/outcome, restart, crash around durable Admit/Claim and duplicate fencing | [selected guarded-comment daemon](../../internal/cli/broker_guarded_comment_process_unix_test.go) |
| Attachment empty/max body through configured daemon and ordinary scheduler; atomic failures | [selected daemon](../../internal/brokerserver/attachment_daemon_process_test.go), [shared CLI cases](../../internal/brokerserver/attachment_cli_process_test.go) |
| Attachment scope, malformed/incomplete/trailing wire, shutdown and actual timeout | [scope](../../internal/brokerserver/attachment_scope_process_test.go), [wire with positive control](../../internal/brokerserver/attachment_wire_process_test.go), [cancellation](../../internal/brokerserver/attachment_shutdown_process_test.go) |
| Credential boundaries and transferred-buffer cleanup | [scanner](../../internal/brokerserver/attachment_credential_scanner_test.go), [exceptional unwind](../../internal/brokerserver/attachment_cleanup_test.go) |

For a focused attachment-scope iteration:

```sh named-broker-focused-scope-oracle
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go test -race \
  ./internal/brokerserver -run '^TestSelectedAttachmentCLIScopeRefusalPrecedesJiraIO$' -count=1
```

This selector is not a premerge gate or complete conformance proof. Follow
[development](development.md) and [landing](landing-a-change.md) for selected
hosted checks; do not duplicate full gates locally. Process fixtures use owned
synthetic peers. A race-enabled test executable does not imply its separately
built CLI/daemon children were race-built. Retain failures and classify their
cause instead of treating a successful retry as sole evidence.

## Prepare an opt-in live plan

No concrete backend version is qualified by the hermetic table. Keep the
executable plan and raw evidence in the owner-selected private workspace,
without naming its path publicly. Before execution record exact CLI/Broker build
identities, protocol/schema/registry digests, authority version and required
phases, backend product/version, selected consistency and isolation boundaries.
Do not invent version values or infer authorization from readiness.

Identify owned allowed and forbidden-sibling fixtures, selected fields/body/
attachment identities, immutable inputs, actors/sessions, ordered requests and
ceilings, expected refusal points, terminal state and reconciliation. Concrete
values stay private. Reuse the exact write/cleanup approval rules in
[live validation](live-validation.md); read-only approval does not cover policy
edits, revocation, process disruption or a comment append.

Start with approved bounded reads and verify qualification-versus-business
counts. Only then exercise separately approved authority revision/revoke and
restart scenarios. Distinguish already released stream bytes from later denied
steps; neither a late denial nor cache qualification erases downloaded bytes.
For comments approve the exact native candidate, ticket/writer/observer workflow
and journal ownership before one mutation. On ambiguity observe the same ticket
and reconcile; never repeat the write or recreate storage to appear fresh.

For each case retain exact tested versions, expected/observed outcome, authority/
qualification/business attempts, completion/unknown state and gate identity.
Separately record untested versions, unsupported operations and external
isolation assumptions. Public summaries are generalized results, not target
values, credentials, policy/backend prose or private evidence routes. Cleanup
requires exact approved owned targets; reconcile unknown outcomes before any
deletion. A prepared plan without observations remains **not run**, never a pass.
