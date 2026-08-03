# Getting started with `atl`

This guide gets a Jira or Confluence Server/Data Center user from a clean
machine to a first read and a local mirror. It does not require reading the
full command reference.

## What `atl` is for

`atl` keeps Jira and Confluence work inside your environment:

- use bounded API reads for discovery;
- mirror selected content to ordinary local files;
- review local diffs with normal developer tools;
- apply remote changes only through explicit safety gates.

Confluence `.csf` and Jira `.wiki` files are the native write substrates.
Generated `.md` files are readable staging views, not a lossy replacement for
the source bytes.

## Requirements

- Linux or macOS on amd64 or arm64;
- Jira or Confluence Server/Data Center reachable from the machine;
- an HTTPS base URL and a least-privilege bearer Personal Access Token (PAT).

Atlassian Cloud email/API-token authentication is not supported. Check the
[compatibility matrix](compatibility.md) before setup.

## 1. Install

The release installer verifies the downloaded binary checksum:

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
atl version
```

Homebrew and source-install alternatives are in the
[installation reference](../README.md#install).

## 2. Configure one backend

Start with the service you need. You can add the other later.

```sh
atl config set --confluence-url https://confluence.example.com
# or:
atl config set --jira-url https://jira.example.com
```

Only use `ATL_ALLOW_INSECURE=1` for a loopback or internal HTTP instance whose
transport you trust. HTTPS is the normal policy.

## 3. Store a PAT safely

`auth login` reads the token from a no-echo prompt, stdin, or a file. Never put
the PAT in command-line arguments, committed config, examples, or logs.

```sh
atl auth login --service confluence
# or:
atl auth login --service jira

atl auth status
atl doctor
```

`auth status` reports only where a credential resolves from; it never prints
the value. CI may instead use `ATL_CONFLUENCE_PAT` or `ATL_JIRA_PAT`. `doctor`
is the share-safe setup report: it is offline by default and omits URLs,
hostnames, paths, identities, tokens, and mirror content.

## 4. Make the first read

Export the read-only policy for the whole shell before an investigation:

```sh
export ATL_READ_ONLY=1

atl doctor --remote
atl conf search --cql 'type = page' --limit 1
atl jira issue search --jql 'order by updated DESC' --limit 1
```

Run only the search command for the configured service. `doctor --remote`
makes one single-attempt product/version GET for each ready service. If the
Confluence version route is absent, it may add one bodyless reachability HEAD;
it reads no page/issue body, search result, or user identity. A versionless
success proves REST reachability but reports compatibility as unverified. The
bounded search remains the first useful permission-and-data read.

Common setup exits:

- `8` from `doctor`: inspect its emitted `problems[]`; a local or requested
  remote preflight is unhealthy;
- `7`: URL or PAT is missing or config is invalid;
- `3`: the backend rejected the PAT;
- `6`: authentication succeeded but the account lacks permission.

See [troubleshooting](troubleshooting.md) for the full recovery map.

## 5. Create a durable mirror

Choose a directory outside a source repository so Atlassian content cannot be
committed accidentally:

```sh
export ATL_READ_ONLY=1
export ATL_MIRROR_ROOT="$HOME/.atl/example-workspace"

atl conf pull --id 123456
# or:
atl jira pull --jql 'project = EXAMPLE order by key' --limit 20
```

Pulling writes local mirror files but does not mutate Jira or Confluence.
The mirror includes an `.atl` marker and sync evidence. Inspect the generated
`.md` view first; use the native `.csf` or `.wiki` file when the view cannot
represent a construct.

Local health and diffs do not need credentials:

```sh
ATL_NO_UPDATE=1 atl conf status "$ATL_MIRROR_ROOT"
ATL_NO_UPDATE=1 atl conf snapshot --into "$ATL_MIRROR_ROOT"
ATL_NO_UPDATE=1 atl conf diff "$ATL_MIRROR_ROOT" -o text

ATL_NO_UPDATE=1 atl jira status "$ATL_MIRROR_ROOT"
```

Status/snapshot require an initialized `.atl` root. Positional `[DIR]` and
`--into` are equivalent explicit forms and cannot be combined.

## What to do next

- Coding agent setup: [agent-setup.md](agent-setup.md)
- Refreshing, reconciling, or adopting a mirror: [mirrors-and-recovery.md](mirrors-and-recovery.md)
- Editing and reviewed writes: [safe-writes.md](safe-writes.md)
- Copy-paste task recipes: [agent-recipes.md](agent-recipes.md)
- Exhaustive commands and flags: [CLI reference](reference/cli/README.md)
- Stable JSON and exit contracts: [output-contract reference](reference/output/README.md)

Questions and sanitized bug reports belong in
[GitHub Issues](https://github.com/isukharev/atl/issues/new/choose).
Never include tokens, private hosts, object identifiers, titles, content, or
company data in a public report.
