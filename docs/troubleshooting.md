# Troubleshooting `atl`

Start with the stable exit class. JSON failures include a numeric `code`, a
closed `kind`, and safe recovery guidance. Scripts and agents should not match
free-form backend error text.

## Exit codes

| Exit | Meaning | First action |
|---:|---|---|
| `1` | Unexpected or unclassified failure | Preserve stderr and report a sanitized issue |
| `2` | Invalid command, flag, or input | Run the exact parent command with `--help` |
| `3` | Backend rejected the credential | Replace or re-enter the PAT |
| `4` | Object not found | Recheck the exact selector and permissions |
| `5` | Remote version conflict | Pull fresh state and reapply |
| `6` | Authenticated but forbidden | Request the minimum missing permission |
| `7` | URL, credential, or config missing/invalid | Complete or repair setup |
| `8` | Safety/check gate refused the operation | Follow the structured recovery; do not bypass |

## Installation

If `atl` is not found after the release installer, add `~/.local/bin` to
`PATH` and start a new shell:

```sh
export PATH="$HOME/.local/bin:$PATH"
atl version
```

For a Homebrew install, update only through `brew upgrade atl`. Do not mix
Homebrew ownership with binary self-update.

## Configuration and authentication

Inspect non-secret configuration and credential sources:

```sh
atl doctor
atl doctor --remote
```

`doctor` is offline by default, survives malformed setup, and never prints
configured URLs/hostnames, paths, identities, token values, mirrored content,
or raw backend errors. `--remote` is explicit and adds one single-attempt
product/version metadata GET per ready service. A blocking result is still
written to stdout before exit `8`; branch on `problems[].id` and
`problems[].remediation`.

For local interactive repair, `atl config show` and `atl auth status` expose
more detail: the former includes configured URLs and local paths, while the
latter reports credential sources but never token values. Do not paste
`config show` into a public issue.

Common cases:

- exit `7`: configure the service URL and PAT;
- exit `3`: the PAT expired, was revoked, or belongs to another instance;
- exit `6`: the PAT is valid but the user cannot access the requested object;
- insecure URL refusal: use HTTPS, except for an explicitly trusted internal or
  loopback test with `ATL_ALLOW_INSECURE=1`.

Atlassian Cloud (`*.atlassian.net`) is not supported. Cloud uses different API
and authentication contracts; see [compatibility.md](compatibility.md).

## Network and self-update

Disable only the signed release check when diagnosing an offline environment:

```sh
ATL_NO_UPDATE=1 atl version
ATL_NO_UPDATE=1 atl conf status /path/to/mirror
```

`ATL_NO_UPDATE=1` does not make an online Jira/Confluence command offline. Use
an external network policy and local-only commands for a true air gap. The
complete destination inventory is in [network-egress.md](network-egress.md).

Verbose HTTP diagnostics go to stderr and redact credentials and query values:

```sh
ATL_VERBOSE=1 atl conf search --cql 'type = page' --limit 1
```

Still review logs before sharing them; object content and identifiers may be
sensitive even when credentials are redacted.

## Mirror problems

Local status does not need backend config:

```sh
ATL_NO_UPDATE=1 atl conf status /path/to/mirror
ATL_NO_UPDATE=1 atl jira status /path/to/mirror
```

Add `--remote` only when you intentionally want one bounded remote drift check
per eligible object.

For a Confluence push exit `5`, preserve the local candidate, pull current
remote state, reapply the edit, and review a new diff. Never auto-force.

An ordinary Confluence `--cql` pull caps selection and reports
`truncated:true`. Narrow the query or use the explicit resumable `--complete`
workflow when a full historical mirror is required.

## Agent plugin or MCP mismatch

If a skill documents a command that the binary does not recognize:

1. run `atl version`;
2. compare the installed plugin version;
3. update the older side;
4. start a new agent session.

Use the CLI fallback if MCP did not register. Do not improvise raw REST calls
with a PAT in arguments or logs.

## Ask for help

Use [GitHub Issues](https://github.com/isukharev/atl/issues/new/choose) for
questions, feature requests, and reproducible defects. Search existing issues
first.

Public reports must not contain:

- backend hostnames, URLs, or IPs;
- page, space, issue, board, sprint, Structure, comment, or attachment IDs;
- titles, bodies, table cells, attachment names, user identity, or company
  names;
- local paths tied to an organization or user;
- PATs or other credentials.

Use generic placeholders and include `atl version`, OS/architecture, stable
exit code/kind, the command shape, and a minimal synthetic reproduction.
Security vulnerabilities follow [SECURITY.md](../SECURITY.md), not public
issues.
