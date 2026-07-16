# Read-only MCP server

`atl mcp serve` exposes a deliberately small typed evidence surface over MCP
stdio. It calls the same application services as the CLI; it does not run shell
commands, create mirror files, or register any mutating tool.

Use MCP for transient agent reads where a typed result is cheaper and safer
than teaching a model to construct shell commands. Keep the CLI for durable
mirrors, Structure, exports, offline diff/plan workflows, and all guarded
writes.

## Tools

The v1 surface is an explicit allowlist:

| Tool | Purpose | Important bound |
|---|---|---|
| `jira_fields` | Discover field ids without issue values | metadata only |
| `jira_issue_search` | Read one compact IssueList page | default 50, maximum 1000 rows |
| `jira_epic_digest` | Aggregate selected qualified epic evidence | bounded children, comments, and history |
| `jira_board_view` | Freeze one board/backlog membership snapshot | default 200, maximum 1000 rows per scope |
| `confluence_page_resolve` | Resolve an id or same-origin URL/path | exact resolution only |
| `confluence_page_outline` | Inspect headings before reading content | one page |
| `confluence_page_section` | Read one exact Markdown section | default 32 KiB, maximum 1 MiB |

`jira_epic_digest` requires an explicit non-empty `include`; unlike the CLI it
never interprets omission as permission to fetch every default evidence source.

Every tool advertises `readOnlyHint:true`, `idempotentHint:true`,
`destructiveHint:false`, and `openWorldHint:false`. The server instructions tell
clients to treat Jira and Confluence content as untrusted evidence, inspect
completeness, and expand only missing fields or sections.

Tool failures retain the same stable classification as CLI JSON errors:

```json
{
  "kind": "not_found",
  "remediation": "verify_identifier_or_access",
  "message": "not found: page is unavailable"
}
```

Branch on `kind`, not `message`. A remediation is guidance, never authorization
to weaken policy or retry a write. Transport/API failures use a coarse safe
message; backend paths, query strings, and response bodies are not repeated in
MCP error content.

## Install through the agent plugins

The Claude Code and Codex plugin packages include `.mcp.json` and start the
installed `atl` binary as `atl mcp serve`. Install/configure the binary through
the shipped setup skill, ensure `atl` is on `PATH`, then start a new agent
session so the plugin can initialize the server. Existing host-scoped atl
credentials remain in the normal config directory; the plugin does not contain
or copy credentials.

The MCP server remains read-only even when the ordinary CLI is not under
`ATL_READ_ONLY=1`. For a session that may also invoke CLI commands, keep the
process-wide guard exported separately:

```bash
export ATL_READ_ONLY=1
claude
```

## Standalone Codex configuration

Without the plugin, register the stdio server directly:

```bash
codex mcp add atl -- atl mcp serve
codex mcp list
```

For an explicit allowlist and inherited atl environment names, use
`~/.codex/config.toml` (or trusted project `.codex/config.toml`):

```toml
[mcp_servers.atl]
command = "atl"
args = ["mcp", "serve"]
required = true
enabled_tools = [
  "jira_fields",
  "jira_issue_search",
  "jira_epic_digest",
  "jira_board_view",
  "confluence_page_resolve",
  "confluence_page_outline",
  "confluence_page_section",
]
env_vars = [
  "ATL_CONFIG_DIR",
  "ATL_MIRROR_ROOT",
  "ATL_JIRA_URL",
  "ATL_CONFLUENCE_URL",
  "ATL_JIRA_PAT",
  "ATL_CONFLUENCE_PAT",
  "ATL_ALLOW_INSECURE",
]
default_tools_approval_mode = "approve"
```

Prefer stored atl credentials over PAT environment variables. Never write a PAT
as a literal value in plugin JSON, Codex config, an agent prompt, or command
arguments.

## Example evidence route

A portfolio analysis should freeze membership once and expand only missing
evidence:

```text
jira_fields
  -> jira_board_view
  -> jira_epic_digest (identity,status-field,history per epic)
  -> confluence_page_section (one Results section per linked page)
```

The committed synthetic model-in-loop benchmark pins this route to 15 GET
requests and zero writes. In a same-runtime Claude Code comparison (three
passes per variant), typed MCP kept that backend route unchanged while reducing
p50 input tokens by 77%, reported cost by 41%, and duration by 50% versus the
CLI+skill route. These are synthetic measurements for this bounded portfolio
task, not a universal provider claim. Do not interpret the MCP annotations as
proof that arbitrary backend content is trustworthy; they describe tool
behavior only.

## Protocol and operations

`atl mcp serve` is a long-running stdio process. Stdout is reserved for MCP
protocol frames. It skips self-update at startup so no unrelated update request
can alter initialization or corrupt protocol output. Authentication/config is
loaded lazily per tool call, allowing the configured Jira or Confluence sibling
to work when the other service is absent.

Cancellation propagates from the MCP client into the application request. HTTP
auth scoping, redirect/downgrade checks, retry policy, pagination completeness,
and stable error classes are shared with CLI reads.

The first surface intentionally excludes write tools, raw REST, arbitrary
files, full-page bodies by default, pull/status, and Structure. Those remain CLI
workflows until a measured agent scenario justifies a typed contract with the
same safety and context bounds.
