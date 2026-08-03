# Network egress and air-gapped operation

`atl` has no telemetry, analytics, advertising, or background content sync.
Network access is limited to the signed release check and to operations the
caller explicitly starts against configured Jira or Confluence backends. This
document separates those two layers so a read-only policy is not mistaken for
a no-network policy.

See also: [CLI reference](reference/cli/README.md) · [self-update.md](self-update.md) ·
[mcp.md](mcp.md) · [../SECURITY.md](../SECURITY.md)

## Runtime egress inventory

| Path | Trigger | Destination and credentials | Disable or avoid |
|------|---------|-----------------------------|------------------|
| Signed self-update | Startup of most CLI commands, at most once per six hours | Configured update URL, GitHub Releases by default; no Jira/Confluence PAT | Set `ATL_NO_UPDATE=1`. Homebrew installs do this in their launcher and update only through `brew upgrade atl`. |
| Jira REST | An explicit `jira ...` read or guarded write | Configured Jira origin; Jira PAT is host-scoped | Do not invoke remote Jira commands. `ATL_READ_ONLY=1` blocks writes but still permits reads. |
| Confluence REST | An explicit `conf ...` read or guarded write | Configured Confluence origin; Confluence PAT is host-scoped | Do not invoke remote Confluence commands. `ATL_READ_ONLY=1` blocks writes but still permits reads. |
| Confluence comment inventory | `conf comment list|thread` | One native page GET plus up to three independently paginated page-child comment series at the configured Confluence origin | Narrow `--location`; use `--depth root` when replies are unnecessary. No global comment search or undocumented endpoint fallback is used. |
| Confluence MCP comment reads | `confluence_comment_list` or `confluence_comment_thread` | Configured Confluence origin; one exact native page read plus bounded page-scoped comment series; thread expands only one selected subtree | Keep selectors and comment-page/item/encoded-byte limits narrow. Partial output never proves absence; no MCP comment write exists. |
| Confluence footer-comment proposal | Read-only `conf comment preview`, or the default dry-run of mutating-classified `conf comment add` | Configured Confluence origin; resolves the page, stable current user, page metadata, and a complete root-only footer inventory; no POST | Use `preview` under `ATL_READ_ONLY=1`; keep the exact proposal hash and body only when a write may later be approved. |
| Confluence footer-comment apply | Explicit `conf comment add --apply --expected-proposal-hash ...` | Same configured origin; repeats/revalidates proposal reads, sends at most one public-REST POST, then performs complete root-only readback | Omit `--apply` to avoid the POST. An ambiguous `outcome_unknown` is never replay-safe. |
| Attachments and page assets | Explicit attachment download or a pull/view that resolves assets | Same configured origin and approved same-origin redirects | Avoid asset-bearing operations; use an existing local mirror. |
| Confluence Jira macros | Pull/view of a page containing a configured Jira macro | Configured Jira origin, after the Confluence page read | Set `render.confluence.jira_macros` to `off`. |
| Page-reference resolution | Explicit `conf page resolve` for a canonical or short URL | Configured Confluence origin; foreign origins and cross-origin redirects are rejected | Pass a known page ID, or resolve from existing local evidence. |
| Jira graph traversal | Explicit `jira issue graph` or typed `jira_issue_graph`; depth defaults to 0, while CLI depth 1..3 or MCP depth 1..2 follows structured Jira relations | Configured Jira origin under aggregate attempt/response-byte bounds | Keep depth 0 for a bounded direct read and lower the explicit bounds. Discovered external URLs are never fetched. The typed MCP route is Jira-only and cannot resolve Confluence. |
| Jira Development identities | Explicit CLI `jira issue graph --include-development` or typed MCP `jira_issue_graph` with `include_development:true`; one summary and zero to 24 detail GETs per expanded Jira issue | Configured Jira origin with the Jira PAT; returned GitLab coordinates receive no request | Omit the option or use false to preserve the stable request set. MCP omits Development-node URLs. ATL never contacts GitLab, follows artifact URLs, clones repositories, or forwards Jira credentials. Any downstream read requires exact owner-approved lowercase host equality and a separately authenticated read-only client. |
| Jira graph Confluence resolution | Explicit CLI `jira issue graph --resolve confluence` only | Optional configured Confluence origin receives one id/title-only GET per discovered canonical page id | Keep `--resolve none` when metadata resolution is unnecessary. MCP v1 exposes no Confluence-resolution input. |
| Setup doctor | Explicit `doctor --remote` | One Jira version GET; one Confluence version GET plus, only after `404`, one bodyless reachability HEAD to the same configured origin | Omit `--remote` for the fully offline diagnostic. |
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
process does not fetch that page. It accepts only a parsed HTTP(S) target under
the configured Confluence origin and context path; an absolute, network-path,
userinfo-bearing, cross-origin, or alternate-scheme backend value is never
passed to the browser. HTTPS is the default; HTTP remains possible only when
the configured backend has already passed ATL's loopback or explicit
insecure-transport policy. The browser may then make its own same-origin
network requests. Model providers, coding-agent hosts, shell commands, proxies,
package managers, and Context7 are also outside the `atl` runtime boundary.

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
