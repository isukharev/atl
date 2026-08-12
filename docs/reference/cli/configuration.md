# Configuration and authentication

Backend binding, configuration, diagnostics, compatibility pins, environment inspection, and authentication commands.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [Help and shell completion](#help-and-shell-completion)
- [`atl mirror backend`](#atl-mirror-backend)
- [`atl config`](#atl-config)
- [Global read-only policy](#global-read-only-policy)
- [`atl config show`](#atl-config-show)
- [`atl config set`](#atl-config-set)
- [`atl doctor`](#atl-doctor)
- [`atl compatibility status|pin|clear`](#atl-compatibility-statuspinclear)
- [`atl environment inspect`](#atl-environment-inspect)
- [`atl auth`](#atl-auth)
- [`atl auth login`](#atl-auth-login)
- [`atl auth status`](#atl-auth-status)
- [`atl auth logout`](#atl-auth-logout)
<!-- reference-navigation:end -->

## Help and shell completion

`atl help` prints the command tree without contacting a configured backend.
Use `atl help <command>` or `<command> --help` to inspect one route before
building an automated invocation.

Generate completion scripts with `atl completion bash`, `atl completion fish`,
`atl completion powershell`, or `atl completion zsh`. These commands are
offline and read-only; redirect their output according to the installation
instructions of the selected shell. Every shell accepts `--no-descriptions` to
omit descriptive text from the generated completion candidates, for example
`atl completion zsh --no-descriptions`.

## `atl mirror backend`

Durable mirror state is bound separately for Jira and Confluence to a
content-minimized digest of the configured backend origin. The binding prevents
a mirror created from one backend from being read or written remotely through
another configuration. Raw URLs and hostnames are never persisted.

Inspect an initialized mirror without loading config or credentials or making a
network request:

```bash
atl mirror backend status [DIR]
# Equivalent explicit-root form:
atl mirror backend status --into DIR
```

Preview the configured service binding, then apply only the exact reviewed
`backend_sha256`:

```bash
atl mirror backend bind DIR --service jira
atl mirror backend bind DIR --service jira \
  --apply \
  --expected-backend-sha256 'sha256:<64 lowercase hex characters>' \
  --confirm BIND
```

Omit `DIR` to use `ATL_MIRROR_ROOT` or the `mirror` fallback; `--into DIR` is
also accepted. Do not pass both positional `DIR` and
`--into`. Bind preview is the default and writes nothing. It reads the selected
configured URL only long enough to derive the digest; neither preview nor apply
loads a PAT or contacts the backend. Apply requires both exact guards and is a
local compare-and-set: a matching binding is an idempotent `already_bound`, and
a different binding is never replaced. Preview reports `would_bind` or
`already_bound`; see [OUTPUT_CONTRACT.md](../output/configuration.md#mirror-backend-binding)
for the JSON shapes.

`mirror backend bind` is intentionally mutation-classified as one leaf.
`ATL_READ_ONLY=1`, global `--read-only`, or persistent read-only policy therefore
blocks even its write-free preview before configuration or network access.
`mirror backend status` remains available under read-only policy.

The first non-dry-run pull into a root with no evidence for that service binds
it automatically. `conf page create|copy --register --into ROOT` and
`jira issue create --register --into ROOT` do the same before registering the
new object. An unbound legacy root that already contains native or service state
must use the explicit reviewed bind workflow above. A Confluence pull that
persists expanded Jira macros requires a separate Jira binding as well as the
Confluence binding; `--jira-macros off` performs no Jira request or binding.

Remote mirror status/snapshot/push/reconcile and remote plan preflight/apply
fail closed on a missing or mismatched service binding before network access.
Local status, snapshot, diff, validate, render, apply, and plan creation
remain usable according to their ordinary local safety rules. Bindings are kept
in strict schema-v1 `.atl/backend-bindings.json`, a private regular file with
mode `0600`; malformed, unknown-version, empty, permissive, or symlinked state
is rejected.

---

## `atl config`

Manage non-secret settings (backend URLs). PATs are managed separately via
`atl auth`.

## Global read-only policy

Use `atl --read-only ...`, `ATL_READ_ONLY=1`, or persist
`atl config set safety.read_only true`. Enabling is monotonic: a true CLI flag,
environment value, or config value wins; `--read-only=false` cannot downgrade a
true environment/config guard. Mutating commands fail with exit 8 before
credentials, request-body files/stdin, self-update, or network access. Read-only
search/get/view/pull/render/status/export/validation commands remain available.
The JSON error adds `"policy":"read_only"` and the full `command` path.
`atl help`, nested help, generated `completion <shell>` scripts, and hidden
shell-completion requests are classified read-only and remain available.

Persistent read-only mode intentionally blocks `config set`, including its own
disable operation. After explicit human approval, edit `read_only` to `false`
in the owner-only global `config.json` (under `ATL_CONFIG_DIR`, normally
`~/.config/atl/`) or remove that key. A process environment guard must be
removed by the process launcher.

Guarded write commands intentionally do not share one confirmation spelling.
For example, `conf push --dry-run` opts into preview, `jira push` previews by
default and writes with `--apply`, field/title operations bind `--apply` to a
reviewed proposal hash, and batch plans additionally require `--confirm APPLY`.
Agents must follow each command's current `--help`/skill recipe and must not
infer write permission from another command's flags.

## `atl config show`

Print the resolved configuration (file + env overlay).

```
atl config show
atl config show -o text
```

JSON output:

```json
{
  "read_only": false,
  "configured_read_only": false,
  "effective_read_only": true,
  "read_only_source": "environment",
  "confluence_url": "https://confluence.example.com",
  "jira_url": "https://jira.example.com",
  "update_base_url": "",
  "transport": {
    "jira": { "ca_bundle_configured": false, "ca_bundle_source": "missing" },
    "confluence": { "ca_bundle_configured": false, "ca_bundle_source": "missing" }
  },
  "render": {
    "display_time_zone": "UTC",
    "jira": { "profile": "default" },
    "confluence": { "profile": "minimal" }
  },
  "jira_list_views": {
    "default": {
      "description": "Compact everyday agent view",
      "search": ["key", "summary", "status", "assignee"],
      "epic_children": ["key", "summary", "status", "issuetype", "assignee"],
      "board": ["position", "key", "summary", "status", "assignee"],
      "board_snapshot": ["position", "key", "summary", "status", "board.column", "assignee"],
      "sprint": ["position", "key", "summary", "status", "assignee"],
      "structure": ["key", "summary", "status", "assignee"],
      "confluence_macro": ["key", "summary", "status", "assignee"]
    },
    "full": {
      "description": "Broader planning and review context",
      "search": ["position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"],
      "epic_children": ["position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels", "epic.parent"],
      "board": ["position", "key", "summary", "status", "board.column", "issuetype", "priority", "assignee", "labels"],
      "board_snapshot": ["position", "key", "summary", "status", "board.column", "board.in_backlog", "issuetype", "priority", "assignee", "labels"],
      "sprint": ["position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"],
      "structure": ["key", "summary", "status", "issuetype", "priority", "assignee", "labels"],
      "confluence_macro": ["position", "key", "summary", "status", "issuetype", "priority", "assignee", "labels"]
    }
  },
  "render_provenance": {
    "render.confluence.profile": "local"
  },
  "local_config_path": "/home/user/.atl/work/mirror/.atl/config.json",
  "mirror": {
    "recommended_root": "~/.atl/<workspace>/",
    "active_root": "/home/user/.atl/work",
    "active_source": "ATL_MIRROR_ROOT"
  }
}
```

The legacy `read_only` field continues to mean the persisted configured value;
`configured_read_only` is its explicit alias. `effective_read_only` is the
monotonic process value after the CLI flag, environment, and configuration are
combined. `read_only_source` is the highest-precedence active source:
`flag|environment|configuration|none`. A false flag cannot mask a true
environment or configured guard. These fields describe the existing hard
preflight; they do not enforce a second policy path.

`mirror.active_root` is present only when `ATL_MIRROR_ROOT` is set. Explicit
`--into` flags still override the default for each pull or inspection command.

`render` is the **effective** (merged) render configuration; `render_provenance`
maps each dotted render key that is *not* a built-in default to its source
(`global` or `local`), so an all-default mirror emits no provenance at all.
`local_config_path` appears only when a per-mirror `.atl/config.json` is in scope
from the current directory. Any forbidden/unknown key in a local file is reported
to **stderr** as a `warning:` line and ignored — never applied.

## `atl config set`

Persist backend URLs, or a dotted `render.*` key, to the config file
(`~/.config/atl/config.json`).

```
atl config set --confluence-url https://confluence.example.com
atl config set --jira-url https://jira.example.com
atl config set --update-url https://releases.example.com/atl

# Optional private PKI trust, scoped independently to each backend:
atl config set transport.jira.ca_bundle /path/to/jira-ca.pem
atl config set transport.confluence.ca_bundle /path/to/confluence-ca.pem

# Render (presentation-only) keys — global or per-mirror (--local):
atl config set render.display_time_zone Europe/Berlin
atl config set render.jira.profile full
atl config set --local render.confluence.profile minimal
atl config set --local render.confluence.page_fields '[{"id":"title"},{"id":"updated","format":"date"}]'
atl config set render.confluence.jira_macros off # global-only: controls authenticated Jira reads
atl config set --local render.jira.include sprint,epic_children

# Reusable Jira list projection; omitted sources inherit "default":
atl config set jira.list_views.planning '{"description":"Quarter planning","board":["position","key","summary","status","board.column","priority","assignee"],"structure":["key","summary","status","priority","assignee"]}'
```

Flags:

| flag | description |
|---|---|
| `--confluence-url` | Confluence base URL |
| `--jira-url` | Jira base URL |
| `--update-url` | self-update distribution server base URL |
| `--local` | write the per-mirror `<root>/.atl/config.json` (render keys only) |
| `--into ROOT` | mirror root for `--local` (defaults to the nearest `.atl` walking up from cwd) |

**Render keys** (`render.display_time_zone`,
`render.{jira,confluence}.{profile,include,exclude}`, plus
`render.jira.custom_fields`, `render.jira.field_views`, and
`render.jira.epic_field`, plus `render.confluence.page_fields` and
`render.confluence.jira_macros`) tune the derived `.md` view. The macro policy
is global-only (or an explicit per-run flag); mirror-local config cannot enable
authenticated Jira reads. `profile` is one of
`minimal`, `default`, `full`; `include`/`exclude`/`custom_fields` take a
comma-separated list, while `field_views` and `page_fields` take JSON descriptor arrays.
`render.display_time_zone` is an IANA presentation zone shared by both
backends; it defaults to deterministic `UTC` and never changes JQL/CQL
interpretation or exact timestamps in JSON/native snapshots.

**Transport keys** `transport.jira.ca_bundle` and
`transport.confluence.ca_bundle` append PEM certificates to the operating
system trust roots for that backend only. The equivalent environment overrides
are `ATL_JIRA_CA_BUNDLE` and `ATL_CONFLUENCE_CA_BUNDLE`. A configured bundle is
accepted only for an HTTPS backend, must be a regular file no larger than
4 MiB, and must contain at least one certificate. Client certificates and
private keys are not supported. `config show`, `config set`, `doctor`, and
errors report only configured/source/status metadata; they never print the
local bundle path. These keys are global-only because they affect authenticated
transport and cannot be set in a mirror-local file. They do not change trust for
`update_base_url` or the self-update distribution channel, whose transport and
signed-manifest verification remain separate.

## `atl doctor`

Run one privacy-safe setup diagnostic:

```bash
atl doctor
atl doctor --remote
atl doctor --service jira --remote
atl doctor -o text
```

The default is fully offline and always skips self-update. It reports schema-v1
build provenance, OS/architecture, config source/parse/owner-only state, URL
policy without URL values, credential presence/coarse source, global read-only
policy, optional content-free mirror health, and the fact that plugin version
is not observable from the CLI. It emits no backend URL or hostname,
filesystem path, token, environment-variable name, identity, object id, mirror
content, or raw parser/backend error.

`--service all|jira|confluence` defaults to `all`. A single-service selection
scopes which service's URL, CA-bundle, mirror, compatibility, and optional
remote checks contribute to health; the sibling service remains explicit with
status `not_selected`. Its CA-bundle `configured` and `source` fields retain
the safe common-config facts, while validation status is `not_selected` and
the configured path is not opened. Common configuration parsing/file-permission and
credential-store safety checks always run, so selecting one service cannot
hide a shared unsafe file.

`safety.read_only` retains its legacy effective meaning. The adjacent
`configured_read_only`, `effective_read_only`, and `read_only_source` fields use
the same projection and closed source precedence as `config show`.

`--remote` requires a parseable global configuration, then evaluates each
service independently. A URL or credential sourced from a file whose
owner-only permissions fail is not used; an independently ready environment
override or sibling service may still be qualified. Mirror problems do not
suppress product metadata checks. Jira makes at most one single-attempt
`serverInfo` GET. Confluence first makes one single-attempt
`server-information` GET; only when that route returns `404` does it add one
bodyless HEAD to the content collection under the same five-second deadline.
That fallback proves REST reachability only: the remote service is available,
but compatibility remains `unverified` with `version_unavailable`. Redirects
and retries are disabled, verbose request identity is redacted, and no content
GET, current-user, search, page-body, or issue-body read is performed. Product
is adapter-owned; backend version and deployment strings cross a strict numeric
release-version grammar before output. A sibling service is still qualified
when the other local setup or metadata request fails.

Warnings such as an absent mirror or unobservable plugin version keep exit `0`.
Any error-severity `problems[]` entry makes `healthy:false`; the complete result
is emitted before the command returns check-failed exit `8`. `-o id` is not
supported.

## `atl compatibility status|pin|clear`

Manage the opt-in exact-build activation used by client-side Data Center
compatibility providers. This state is separate from `config.json`, so an older
binary or unrelated `config set` cannot erase or rewrite it:

```sh
atl compatibility status
atl compatibility pin confluence \
  --version "$ATL_CONFLUENCE_VERSION" \
  --build-number "$ATL_CONFLUENCE_BUILD_NUMBER"
atl compatibility status --remote
atl compatibility clear confluence
```

`status` is offline by default. With `--remote`, it makes one single-attempt
exact identity probe. A legacy Confluence whose modern metadata route returns a
typed 404 uses one bounded same-origin HTML-head read and projects only numeric
version/build metadata. Redirects and retries are disabled and verbose request
identity is redacted.

`pin` explicitly binds a compiled protocol profile to an exact three-component
version and decimal build. It writes owner-only `compatibility.json`; there is
no version range, arbitrary provider id, URL, endpoint, header, auth override,
payload template, or downloaded manifest. `clear` disables the provider.

Closed status values are `disabled`, `configured`, `unsupported`,
`unavailable`, `mismatch`, and `matched`. Only `matched` sets `qualified:true`.
This qualifies provider identity only; it does not alter ordinary product
compatibility and does not imply that a mutation command exists.

## `atl environment inspect`

Use one explicit diagnostic when a workflow depends on date boundaries or when
server, user, query, and display time appear inconsistent:

```bash
export ATL_READ_ONLY=1
atl environment inspect
atl environment inspect -o text
```

The command is allowed by the global read-only policy. When both backends are
configured it performs exactly three sequential metadata reads at most: Jira
server info, Jira current user, and Confluence current user. It never sends
JQL/CQL, searches issues/pages, reads content, mutates state, or runs a timezone
calibration probe. Missing credentials, unavailable endpoints, and absent
optional fields remain explicit per-backend statuses; one backend does not hide
the other's result.

Each time fact carries `evidence`:

- `observed` — returned directly by backend metadata;
- `configured` / `default` — selected by atl configuration;
- `assumed` — the Jira current-user timezone used as the JQL interpretation
  model; raw JQL is still sent unchanged;
- `unknown` — the backend did not prove a value. In particular, atl does not
  claim that a Confluence user preference controls CQL.

Only Jira's numeric server UTC offset is reported from `serverTime`; atl does
not invent an IANA name from an offset. Output deliberately excludes backend
URLs, user identity, email, and credentials. `complete` means all metadata
facts exposed by every configured backend were returned; an unavailable
optional Confluence user timezone therefore yields a useful but partial result.
This command is user-invoked only: `conf pull --incremental` does not call it.

Set a whole catalog with `jira.list_views` or one preset with
`jira.list_views.<name>`; pass JSON objects and use `null` to remove a custom
preset. Names match `[a-z][a-z0-9_-]{0,31}`. Built-in `default`/`full` cannot be
removed but may be overridden. List views are global-only; `--local` refuses
them.

`safety.read_only` accepts `true|false` and is global-only. Set it to `true` as
the last configuration step for an investigation-only agent or CI profile.
Independently of that policy, the shared transport refuses every redirect from
a mutating POST, PUT, PATCH, or DELETE request. An otherwise allowed
same-origin 3xx is returned as the original HTTP error; origin and scheme
violations remain transport-policy errors. The method and body are never sent
to any redirect target.

**Local config layer (security boundary).** `--local` writes a per-mirror
`.atl/config.json` that may carry **render keys only** — it is presentation-only.
A mirror directory can be shared or checked out, so a repo-local file must never
be able to redirect where a PAT is sent: backend/update URLs are global/env-only,
and `config set --local` refuses any URL flag (exit 2). At read time, any
credential-adjacent or unknown key found in a local file is warned about on stderr
and ignored. Precedence is **local > global > default**, merged per key.

`jira_list_views` is the effective global catalog of reusable Jira list
projections. Built-in `default` and `full` entries are always present and are
written into a newly saved config. Each view has source-specific arrays for
`search`, `epic_children`, `board`, `board_snapshot`, `sprint`, `structure`, and
`confluence_macro`; a custom view inherits the built-in default for omitted
sources. It is global-only because these transient reads are not bound to one
mirror root.

Runtime commands validate the complete catalog before any network request. An
invalid catalog fails with config exit 7 instead of silently falling back to an
unrelated projection. `atl config show` remains available for recovery: it
returns the raw catalog plus `jira_list_views_error`. Replace the catalog, set a
corrected preset, or remove the bad custom preset with
`atl config set jira.list_views.<name> null`. When several custom entries are
invalid, repeat that deletion for each one: each narrow repair is persisted,
but runtime commands stay at exit 7 until the whole catalog validates. Invalid JSON syntax in
`config.json` cannot be repaired safely as a dotted update; fix the file itself
and rerun `config show`. `atl version`, help/completion, classified read-only
auth/config/profile diagnostics, and local-only `conf status` / `jira status`
remain available because they are offline and skip self-update. `config show`
still returns exit 7 with the parse error; `status --remote`, all other online
reads, and all mutations remain blocked until valid.

---

## `atl auth`

Manage Personal Access Tokens. PATs are written to a mode-0600 credentials
file (`~/.config/atl/credentials.json`) or resolved from env vars. They are
never stored in the mirror or the repository.

## `atl auth login`

Run without flags for an interactive setup wizard (like `gh auth login`). For each
service it asks for the base URL and PAT, validates the PAT against the backend, and
stores both. Any service can be skipped. Requires a terminal.

```sh
atl auth login
# ? Configure Confluence? (Y/n) y
# ?     Confluence base URL [https://wiki.example.com]:
# ?     Enter PAT (input hidden): ****
# ?   ✓ Confluence: authenticated as Jane Doe
# ? Configure Jira? (Y/n) n
```

For non-interactive/scripted setup, configure one service at a time with `--service`
(below) plus `atl config set` for the URLs.

Store a PAT for a service.

The token is never accepted on the command line (which would leak it to the
process list and shell history). Provide it via `--from-file`, piped stdin, or
an interactive no-echo prompt:

```bash
# interactive: prompts without echo when run on a terminal
atl auth login --service confluence

# read from stdin without echo (bash; -s is not POSIX sh); avoids shell history
read -rs PAT && echo "$PAT" | atl auth login --service jira --from-file -

# from a file
atl auth login --service jira --from-file ./jira.pat
```

Flags:

| flag | description |
|---|---|
| `--service` | `confluence` or `jira` (required) |
| `--from-file` | file path, or `-` for stdin; omit to be prompted without echo |

## `atl auth status`

Show where each token is resolved from (env var name or file path). Never
prints the token value.

```
atl auth status
```

```json
{
  "confluence": "env:ATL_CONFLUENCE_PAT",
  "jira": "keychain-file:/home/user/.config/atl/credentials.json"
}
```

## `atl auth logout`

Remove a stored PAT from the credentials file.

```
atl auth logout --service confluence
```

---
