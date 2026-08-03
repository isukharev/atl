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
| `5` | Remote version conflict | Preserve the candidate, reconcile with fresh remote state, and make a new preview |
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
product/version GET per ready service. If the Confluence version route returns
`404`, it adds one bodyless reachability HEAD; success is reported as available
with unverified compatibility. A blocking result is still written to stdout
before exit `8`; branch on `problems[].id` and `problems[].remediation`.

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

`status` and `snapshot` also accept `--into /path/to/mirror`; do not combine it
with positional `[DIR]`. With neither form they use `ATL_MIRROR_ROOT`, the
nearest initialized `.atl`, then the service fallback. Exit 4 means the selected
root is absent or not initialized; pull it first or select the intended mirror.

Add `--remote` only when you intentionally want one bounded remote drift check
per eligible object.

For a Confluence push exit `5`, keep the working `.csf` and its mirror baseline
unchanged. First qualify what a pull would do, then compare the exact base,
candidate, and current remote body without replacing any working artifact:

```sh
ATL_READ_ONLY=1 atl conf pull --id 123456 --into /path/to/mirror --dry-run
ATL_READ_ONLY=1 atl conf reconcile preview \
  /path/to/mirror/SPACE/page/page.csf --into /path/to/mirror -o text
```

The dry-run may exit `8` with `local_safety` because preserving the candidate is
the intended result. Reconcile performs one qualified remote read and does not
change the working `.csf`, `.md`, baseline, metadata, or sidecar. If exact review
artifacts are useful, run the separately mutation-classified stage:

```sh
env -u ATL_READ_ONLY atl conf reconcile stage \
  /path/to/mirror/SPACE/page/page.csf --into /path/to/mirror
```

It writes immutable base/theirs files under `.atl/reconcile/` and still does not
replace the working candidate.

After reviewing the three sides, explicitly merge or reapply the intended local
change onto current remote bytes. A qualified `pull --stash-local` can preserve
the exact edited native bytes in `.atl/stash/` before refreshing them; it cannot
bypass a dirty derived view, broken baseline, or other unqualified state. Then
validate, diff, and produce a fresh push preview. Never auto-force and never
replay the failed write.

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
