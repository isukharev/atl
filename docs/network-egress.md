# Network egress and air-gapped operation

`atl` has no telemetry, analytics, advertising, or background content sync.
Network access is limited to the signed release check and to operations the
caller explicitly starts against configured Jira or Confluence backends. This
document separates those two layers so a read-only policy is not mistaken for
a no-network policy.

See also: [usage.md](usage.md) · [self-update.md](self-update.md) ·
[mcp.md](mcp.md) · [../SECURITY.md](../SECURITY.md)

## Runtime egress inventory

| Path | Trigger | Destination and credentials | Disable or avoid |
|------|---------|-----------------------------|------------------|
| Signed self-update | Startup of most CLI commands, at most once per six hours | Configured update URL, GitHub Releases by default; no Jira/Confluence PAT | Set `ATL_NO_UPDATE=1`. Homebrew installs do this in their launcher and update only through `brew upgrade atl`. |
| Jira REST | An explicit `jira ...` read or guarded write | Configured Jira origin; Jira PAT is host-scoped | Do not invoke remote Jira commands. `ATL_READ_ONLY=1` blocks writes but still permits reads. |
| Confluence REST | An explicit `conf ...` read or guarded write | Configured Confluence origin; Confluence PAT is host-scoped | Do not invoke remote Confluence commands. `ATL_READ_ONLY=1` blocks writes but still permits reads. |
| Attachments and page assets | Explicit attachment download or a pull/view that resolves assets | Same configured origin and approved same-origin redirects | Avoid asset-bearing operations; use an existing local mirror. |
| Confluence Jira macros | Pull/view of a page containing a configured Jira macro | Configured Jira origin, after the Confluence page read | Set `render.confluence.jira_macros` to `off`. |
| Page-reference resolution | Explicit `conf page resolve` for a canonical or short URL | Configured Confluence origin; foreign origins and cross-origin redirects are rejected | Pass a known page ID, or resolve from existing local evidence. |
| Environment inspection | Explicit `environment inspect` | At most three metadata GETs across configured Jira/Confluence services | Do not run it offline; reuse previously reviewed environment evidence. |
| MCP evidence tools | An agent explicitly calls one of the registered tools | Same configured Jira/Confluence origins and host-scoped PATs | Do not call a remote tool in a no-backend session. Merely starting the MCP server makes no request and skips self-update. |

HTTP tracing is opt-in through `ATL_VERBOSE=1`. It does not add requests, but
it writes redacted request metadata to stderr. Query values and credentials are
not emitted. Transport errors expose only a safe reason category and a
query-redacted URL.

Large `conf pull --incremental|--complete` runs remain serial by default. Their
opt-in `--page-prefetch` and `--requests-per-second` controls add no destination:
one command-scoped scheduler bounds every Confluence and optional Jira-macro
transport hop and shares a server `Retry-After` cooldown.

`conf page open` asks the operating system to open a browser URL. The `atl`
process does not fetch that page, but the browser may make its own network
requests. Model providers, coding-agent hosts, shell commands, proxies, package
managers, and Context7 are also outside the `atl` runtime boundary.

## The two independent safety controls

Use both controls when an agent may read live evidence but must never write:

```bash
export ATL_READ_ONLY=1
export ATL_NO_UPDATE=1
atl jira issue view PROJ-1 -o text
```

- `ATL_READ_ONLY=1` rejects every classified mutating command before
  credentials, request bodies, self-update, or backend access. It deliberately
  permits Jira/Confluence reads.
- `ATL_NO_UPDATE=1` disables only the signed release check. It does not block
  Jira or Confluence requests.

Neither variable is a host firewall. Enforce a true network boundary outside
the process when policy requires one.

## Air-gapped use

Set `ATL_NO_UPDATE` for the entire shell, avoid remote commands, and work from
an existing mirror or other local artifacts:

```bash
export ATL_NO_UPDATE=1

atl capabilities --task confluence/evidence
atl config show
atl conf validate mirror/page.csf
atl conf render mirror
atl conf diff mirror/page.csf
atl jira render mirror-jira
```

`version`, `capabilities`, help/completion, `auth`, `config`, and `profile`
commands skip self-update by construction. The render, validate, diff, status,
manifest, and plan-building families may be locally implemented but should
still be launched with `ATL_NO_UPDATE=1` in an air-gapped workflow so their
startup cannot probe the release service. Consult command documentation before
assuming a preview is offline: some previews deliberately re-read remote state.

For a mechanically enforced air gap, deny outbound traffic at the container,
host, or network policy layer as well. A missing network route is handled as a
normal transport failure; `atl` never tries a different backend or forwards a
PAT to a foreign host.

## Package-managed updates

The Homebrew formula installs the release binary under `libexec` and exposes an
environment wrapper that always sets `ATL_NO_UPDATE=1`. Consequently the normal
Homebrew command cannot replace itself and receives upgrades only when the user
or package automation runs:

```bash
brew update
brew upgrade atl
```

Calling the Cellar's private `libexec/atl` path directly bypasses the wrapper
and is unsupported as a normal launch path. Binaries installed through
`install.sh`, a direct release asset, or `go install` retain the signed
self-update behavior unless the caller sets `ATL_NO_UPDATE`.

Context7 refreshes and Homebrew tap publication run in release automation; they
are distribution infrastructure, not runtime calls made by `atl`.
