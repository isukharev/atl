# Compatibility and support matrix

This page separates product intent from evidence. “Supported” means the
repository maintains the contract and accepts bug reports. It does not claim
that every vendor patch or plugin combination has been independently tested.

Capture the client side of a compatibility report without printing
credentials:

```sh
atl version
atl auth status
atl doctor --remote
```

`doctor --remote` emits no configured URL/hostname, identity, path, token, or
raw backend error. It normally qualifies the product/version metadata route.
When an older Confluence lacks that route, one bodyless HEAD may qualify REST
reachability only; compatibility remains unverified without a version. A
healthy result does not certify every product feature or Marketplace app.

Version-pinned compatibility providers are a separate opt-in boundary for
reviewed product-UI protocols that are not part of the documented public REST
surface. Start with an offline status. Obtain the exact three-component version
and decimal build from the deployment's trusted administrative/version evidence,
pin those values explicitly, and only then request the bounded identity probe:

```sh
atl compatibility status

export ATL_CONFLUENCE_VERSION='<exact observed version>'
export ATL_CONFLUENCE_BUILD_NUMBER='<exact observed build number>'
env -u ATL_READ_ONLY atl compatibility pin confluence \
  --version "$ATL_CONFLUENCE_VERSION" \
  --build-number "$ATL_CONFLUENCE_BUILD_NUMBER"

# Only an exact remote match sets qualified:true.
atl compatibility status --remote

# Disable the provider when the deployment changes or it is no longer needed.
env -u ATL_READ_ONLY atl compatibility clear confluence
```

The provider settings live in owner-only `compatibility.json`, separate from
ordinary `config.json`. Enabling a provider explicitly binds a compiled protocol
profile to one exact version and build; every remote use must match that private
pin. `pin` and `clear` are local mutation-classified commands, so an exported
read-only policy must be removed only for that exact process. Nearby patches are
never inferred. With no configured pin, `status --remote` remains disabled and
does not probe: ATL neither discovers an identity nor silently authorizes it.
After pinning, the remote status probe accepts no custom endpoint, header,
payload template, or provider download. Clear and requalify after any product
upgrade; do not copy a pin from another deployment.

## Atlassian products

| Product | Deployment/API | Authentication | Read | Mirror/diff | Reviewed write | Evidence and limits |
|---|---|---|---|---|---|---|
| Confluence | Server/Data Center REST API | Bearer PAT | Supported | Supported, native `.csf` | Supported with validation and page version gate | Automated adapter/CLI contracts on Linux and macOS plus bounded maintainer live checks; no public per-version certification yet |
| Confluence inline-comment Data Center profile 1 | One owner-pinned exact build | Same host-scoped PAT | Bounded server-rendered DOM preparation plus qualified readback | No additional mirror format | Guarded inline create/reply/resolve/reopen over a fixed compiled protocol | Explicit owner-only activation; fail-closed on exact identity or stable-evidence mismatch, one non-replayed write, no range or arbitrary REST escape hatch |
| Jira | Server/Data Center REST API v2 plus Agile API where required | Bearer PAT | Supported | Supported, native `.wiki` | Supported with fresh baseline/proposal gates | Automated adapter/CLI contracts on Linux and macOS plus bounded maintainer live checks; fields, workflows, and installed apps vary by deployment |
| Jira Development identities | Experimental private Development endpoint on the configured Jira Data Center deployment | Same host-scoped Jira PAT | Opt-in, bounded, fail-closed GitLab coordinates | No mirror format | Read-only | CLI `--include-development` or typed MCP `include_development:true`; unsupported on 404/405, no endpoint fallback, no GitLab request, and no compatibility claim for other plugin/version combinations. MCP omits Development-node URLs; downstream reads require exact owner-approved lowercase host equality and separate read-only authentication, never Jira credentials. |
| Jira Structure | Tempo Structure endpoints present on the configured Jira | Same Jira PAT | Read/export supported | No persistent graph/store | No Structure mutation surface | Capability depends on the installed Structure version and endpoint availability; qualify metadata before larger reads |
| Atlassian Cloud | Cloud REST APIs | Cloud OAuth or email/API-token models | Not supported | Not supported | Not supported | An HTTPS Cloud URL is not rejected at config time, but Cloud API/auth behavior is outside the contract; `atl` does not map Server/DC native formats to ADF |

Private deployment identity and content are not published. A protocol profile
does not imply support for adjacent vendor builds: its owner-only activation is
bound to exactly one observed identity and must be requalified explicitly after
an upgrade.

## Broker support and evidence

This table describes development `main`. Entries under
[`Unreleased`](../CHANGELOG.md#unreleased) do not describe an installed
release; use that release's documentation and confirm it with `atl version`.

`Available` means compiled contract and product-path support. It does not mean
that a host composed the service or that discovery granted the current caller.
Every execution authenticates and authorizes independently; Broker mode never
falls back to direct backend access or the ordinary PAT store.

| Operation or family | CLI and MCP exposure | Status and principal bounds | Primary synthetic oracle or owner |
|---|---|---|---|
| Exact Jira issue read, `jira.issue.read` v1 | CLI: `atl jira issue get KEY --fields summary,description,updated`, with any nonempty subset. MCP: no dedicated exact-issue tool; discovery only. | Available for one canonical key and the three named fields. No display/custom field, JQL, URL, or broader issue operation. | [Exact server/authority/backend chain](../internal/brokerserver/server_test.go) |
| Exact Confluence page read, `confluence.page.read` v1 | CLI: `atl conf page get --id N --format csf`, `page meta --id N`, and numeric-id `page outline N`, `section N`, or `sections N`. MCP: `confluence_page_meta`, `confluence_page_outline`, `confluence_page_section`, `confluence_page_sections`. | Available for one numeric id and exact metadata or native storage. No URL selector, rendered view, search, or attachment bytes. | [Production MCP Broker path](../internal/mcpserver/broker_client_test.go) |
| Discovery v2 and fixed-family discovery v3 | CLI: `atl broker discover --service jira` or `--service confluence`; add `--family atl.broker.execution.v2` for project pages. MCP: private zero-TTL service resources and the Jira family resource. | Available and advisory only: `allowed`, `access_request_required`, or `unavailable`. It selects no resource and grants no execution. | [Fresh MCP discovery process oracle](../internal/brokerserver/discovery_process_test.go) |
| Jira project page, `jira.project.issue_page.read` v1 | CLI: `atl jira issue project-page --project KEY --fields summary,description --limit N --cursor OFFSET`. MCP: `jira_project_issue_page`. | Available for one separately authorized page of at most 15 issues. No caller JQL or automatic continuation; `selection_complete` is always false. | [Selected CLI/MCP process oracle](../internal/brokerserver/project_page_process_test.go) |
| Guarded Jira comment preview, apply, and outcome | CLI: `jira issue comment preview KEY --from-file FILE`; `comment add KEY --from-file FILE --apply --expected-proposal-hash HASH --operation-ticket TICKET`; `comment outcome --operation-ticket TICKET`. MCP: none. | Available only with paired guarded-comment config and `broker serve --enable-jira-comments`. Apply binds unchanged native Jira-wiki bytes, proposal, ticket, and writer session; at most one comment POST. Outcome uses the separate observer session and journal only. | [Guarded-comment daemon/crash oracle](../internal/cli/broker_guarded_comment_process_unix_test.go) |
| Qualified Confluence corpus handoff | CLI: `atl corpus handoff-qualified --store DIR`. MCP: none. | Available only for one clean, complete, Confluence-only sealed cache generation. Qualification neither refreshes nor deletes data and is not a later execution grant. | [Selected CLI handoff oracle](../internal/brokerserver/cache_corpus_cli_process_test.go) |
| Local Broker host and journal initialization | CLI: `atl broker serve --config FILE [--enable-jira-comments]`; `atl broker journal initialize --config FILE`. MCP: not applicable. | Available as a bounded loopback TLS service. Initialization is local and create-only, with no authority/backend request. The flagless host remains read-only. | [Host lifecycle oracle](../internal/cli/broker_process_unix_test.go) |
| Jira attachment metadata/body streaming | CLI and MCP: none in Broker mode. | Unavailable. Direct attachment commands and existing corpus capture do not imply a Broker attachment operation. Add this row's positive status only when its contract, runtime, consumers, and selected-process oracle are enabled together. | [Current closed registry](broker-contract.md#versioned-operation-registry) |
| Further mutations | CLI and MCP: none in Broker mode. | Jira whole-issue update and all other adjacent Jira mutations are unavailable; comment append grants none of them. Confluence mutations are unavailable. Whole-issue update remains gated by #1490 and Confluence push dry-run by #1491. | [Current operation boundary](broker-contract.md#versioned-operation-registry) |

The linked tests prove synthetic bounds and process-restart behavior, not live
certification. They do not qualify an exact deployed backend version, actual
upstream credential privileges, external PDP or revocation latency, destination
egress, deployment backup/restore procedures, or a real mutation/recovery outcome.
Those facts need separate deployment evidence and, for live operations, an
explicitly authorized, version-recorded plan. Synthetic success remains labelled
synthetic.

## Operating systems and distribution

| Surface | Status | Evidence |
|---|---|---|
| Linux amd64 | Supported | Release artifact and hosted Linux test job |
| Linux arm64 | Supported release target | Release artifact is cross-compiled; no hosted arm64 runtime certification |
| macOS amd64/arm64 | Supported release targets | Release artifacts for both; hosted macOS tests exercise the runner architecture, not a guaranteed per-architecture matrix |
| Windows | Not currently supported | Source cross-compilation is CI-verified; Windows remains unsupported pending runtime, install, update, and recovery evidence |
| Homebrew | Supported | Release-owned formula and checksum |
| Release installer | Supported on Linux/macOS | SHA-256 verification; signed update trust documented separately |
| Source build | Supported with Go 1.26.6+ | Maintainer toolchain contract and CI |

The release binary is static and has no runtime Go dependency.

## Agent surfaces

| Surface | Status | Boundary |
|---|---|---|
| CLI | Supported | Complete product surface; JSON by default |
| Claude Code plugin | Supported | Generated skills plus typed read-only MCP launch |
| Codex plugin | Supported | Same generated skills and MCP surface |
| Standalone MCP | Supported, read-only | Closed full/Jira/Confluence/offline tool inventories; no mutation, shell, raw REST, or arbitrary filesystem |
| Context7 docs | Supported for published releases | `stable` follows the latest release; versioned ids preserve older docs |

## What “compatible” does not mean

- Marketplace apps and custom Jira fields can add deployment-specific schemas.
- A successful basic read does not prove every workflow or plugin endpoint.
- Advisory Confluence Cloud-compat findings do not predict a migration outcome.
- `ATL_ALLOW_INSECURE=1` is an explicit transport override, not a supported
  default for public networks.
- Private maintainer checks are not a public certification program.

## Report a compatibility result

Open a sanitized [compatibility issue](https://github.com/isukharev/atl/issues/new/choose)
with:

- `atl version`, OS, architecture;
- product and version family, without a private hostname;
- auth type (never the token);
- command shape and stable exit code/kind;
- a synthetic or redacted reproduction.

Do not publish object IDs, titles, content, usernames, company names, URLs,
local private paths, or credentials.

## Choosing a different tool

`atl` is specialized for lossless local Server/Data Center workflows,
reviewable native diffs, and explicit write gates. Other tools may be a better
fit:

| Need | Consider | Why |
|---|---|---|
| Official Jira Cloud command automation | [Atlassian CLI](https://developer.atlassian.com/cloud/acli/reference/commands/jira/) | Official Jira Cloud command surface |
| Hosted multi-product Cloud tools for agents | [Atlassian Rovo MCP](https://developer.atlassian.com/cloud/rovo-mcp/) | Atlassian-hosted Cloud MCP with OAuth/API-token options |
| Broad Jira/Confluence MCP across Cloud and Server/DC | [mcp-atlassian](https://github.com/sooperset/mcp-atlassian) | Community MCP with a wider direct tool inventory |
| Local native mirrors, offline diff, and review-bound writes | `atl` | This project's primary contract |

This comparison describes mechanism, not a quality ranking. Recheck each
linked project before making a long-term deployment decision.
