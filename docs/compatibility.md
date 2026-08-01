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
surface. Inspect them independently:

```sh
atl compatibility status
atl compatibility status --remote
```

The provider settings live in owner-only `compatibility.json`, separate from
ordinary `config.json`. Enabling a provider explicitly binds a compiled protocol
profile to one exact version and build; every remote use must match that private
pin. Nearby patches are never inferred. The status probe accepts no custom
endpoint, header, payload template, or provider download.

## Atlassian products

| Product | Deployment/API | Authentication | Read | Mirror/diff | Reviewed write | Evidence and limits |
|---|---|---|---|---|---|---|
| Confluence | Server/Data Center REST API | Bearer PAT | Supported | Supported, native `.csf` | Supported with validation and page version gate | Automated adapter/CLI contracts on Linux and macOS plus bounded maintainer live checks; no public per-version certification yet |
| Confluence inline-comment Data Center profile 1 | One owner-pinned exact build | Same host-scoped PAT | Qualification only in the identity/pin phase | No additional mirror format | Mutation commands are delivered separately | Explicit owner-only activation; fail-closed on exact identity mismatch, with no range or arbitrary REST escape hatch |
| Jira | Server/Data Center REST API v2 plus Agile API where required | Bearer PAT | Supported | Supported, native `.wiki` | Supported with fresh baseline/proposal gates | Automated adapter/CLI contracts on Linux and macOS plus bounded maintainer live checks; fields, workflows, and installed apps vary by deployment |
| Jira Structure | Tempo Structure endpoints present on the configured Jira | Same Jira PAT | Read/export supported | No persistent graph/store | No Structure mutation surface | Capability depends on the installed Structure version and endpoint availability; qualify metadata before larger reads |
| Atlassian Cloud | Cloud REST APIs | Cloud OAuth or email/API-token models | Not supported | Not supported | Not supported | An HTTPS Cloud URL is not rejected at config time, but Cloud API/auth behavior is outside the contract; `atl` does not map Server/DC native formats to ADF |

Private deployment identity and content are not published. A protocol profile
does not imply support for adjacent vendor builds: its owner-only activation is
bound to exactly one observed identity and must be requalified explicitly after
an upgrade.

## Operating systems and distribution

| Surface | Status | Evidence |
|---|---|---|
| Linux amd64 | Supported | Release artifact and hosted Linux test job |
| Linux arm64 | Supported release target | Release artifact is cross-compiled; no hosted arm64 runtime certification |
| macOS amd64/arm64 | Supported release targets | Release artifacts for both; hosted macOS tests exercise the runner architecture, not a guaranteed per-architecture matrix |
| Windows | Not currently supported | No release artifact or hosted compatibility matrix |
| Homebrew | Supported | Release-owned formula and checksum |
| Release installer | Supported on Linux/macOS | SHA-256 verification; signed update trust documented separately |
| Source build | Supported with Go 1.26.5+ | Maintainer toolchain contract and CI |

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
