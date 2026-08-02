# Usage reference

`atl` is a non-interactive, agent-native CLI for Confluence and Jira. It
mirrors pages to local `.csf` files in their native storage format, validates
edits, and pushes back under an optimistic version gate — all without storing
credentials in the repository or the mirror.

See also: [../README.md](../README.md) · [architecture.md](architecture.md) ·
[csf-and-fragments.md](csf-and-fragments.md) · [self-update.md](self-update.md) ·
[network-egress.md](network-egress.md)

---

## Global conventions

### Output format

By default every command writes JSON to stdout. Pass `-o text` (or
`--output text`) for human-readable output on the same commands that support
it. Text support is an explicit per-command contract: an unsupported request
returns a usage error (exit 2) before config, stdin, or network access and never
falls back to JSON.

`-o id` is also an explicit per-command contract. It prints only primary
identifiers, one per line. Unsupported id output now fails at the same root
preflight, before config, stdin, self-update, or network access.

```
atl conf search --cql "space=DOCS" -o text
atl jira issue view PROJ-1 -o text
```

### Offline agent capability catalog

`atl capabilities` maps a closed exact task class to a small ordered command
route. It loads no config or credentials, makes no network request, and skips
self-update, so agents can use it before broad help/skill discovery:

```bash
atl capabilities --task jira/evidence
atl capabilities --task confluence/edit -o text
atl capabilities --task jira/portfolio -o id
atl capabilities --task jira/board-portfolio -o text
atl capabilities --task jira/batch-analysis -o text
atl capabilities --task jira/structure-planning -o text
atl capabilities --task jira/edit -o text
atl capabilities --task jira/mirror -o text
atl capabilities --task confluence/table-analytics -o text
atl capabilities --task confluence/comments -o text
atl capabilities --task confluence/mirror -o text
atl capabilities --task knowledge/search -o text
atl capabilities --id confluence.page.section
```

Supported task classes are `jira/evidence`, `jira/portfolio`,
`jira/board-portfolio`, `jira/batch-analysis`, `jira/structure-planning`,
`jira/edit`, `jira/mirror`, `confluence/evidence`,
`confluence/table-analytics`, `confluence/comments`, `confluence/edit`, `confluence/mirror`, and
`knowledge/search`. Exact `--service` and `--access
read-only|mutating` filters can narrow the result. An unknown task or capability
id exits 4; an invalid service/access value exits 2. No fuzzy classification is
performed.

For `jira/evidence`, the ordered route starts with `jira issue search` for
broad candidate discovery before exact per-issue field qualification and
bounded expansion.

`jira/structure-planning` returns separate routes for hierarchy rows, an
explicit per-row Structure value matrix, and transient issue export. The value
operation remains read-only even though the Structure API carries that query
payload over HTTP POST.

`jira/portfolio` includes `jira structure get` as the qualification step for an
exact Structure id before folder discovery or a bounded view. On the typed MCP
surface, `jira_structure_get` narrows that result to id, name, and read-only
state and omits owner, permission, saved-view, and forest transport payloads.

`jira/edit` includes complete worklog listing and the single-entry add as a
bounded pair. The add previews by default, applies only an exact reviewed
proposal hash, and reconciles an ambiguous POST through one read without
replaying the write.

`confluence/comments` is an additive four-step route: qualified list discovery,
one exact thread expansion, read-only guarded preview, and guarded add. The
catalog maps list/thread to the narrower read-only MCP tools described below;
preview/add are explicitly CLI-only and the route grants no write authority.

JSON uses schema version 1:

```json
{
  "schema_version": 1,
  "routing": {
    "match": "exact",
    "reference_load": "invoke capability.skill, then open capability.reference relative to that skill; do not search the filesystem",
    "stop": "stop expanding the route when sufficient complete evidence is available"
  },
  "selection": {"task": "jira/evidence", "count": 4},
  "capabilities": [{
    "id": "jira.epic.digest",
    "task_class": "jira/evidence",
    "service": "jira",
    "role": "primary",
    "priority": 20,
    "summary": "Collect bounded multi-source evidence for one epic and period",
    "command": "jira epic digest",
    "cli_command": "jira epic digest",
    "mcp_tool": "jira_epic_digest",
    "mcp_scope": "Bounded digest with explicit include sources; no Confluence expansion.",
    "cli_only": false,
    "access": "read-only",
    "output_modes": ["json", "text"],
    "evidence": "qualified",
    "completeness": "per-source",
    "skill": "jira",
    "reference": "reference/evidence-workflow.md"
  }]
}
```

The transport fields are additive within schema version 1. `command` remains a
compatibility alias for `cli_command`. `mcp_tool` names a reviewed bounded
typed route only inside `mcp_scope`; it does not promise full CLI output or
workflow equivalence. CLI-only entries omit `mcp_tool` and `mcp_scope` and
set `cli_only:true`.

`access` is derived from the CLI's reviewed process-wide policy inventory:
`mutating` commands are refused by `ATL_READ_ONLY=1`; `read-only` means no
remote mutation (some reads such as `pull` intentionally write local mirror
artifacts). `output_modes` is derived from the same command-tree preflight used
at execution. CI verifies that every catalog command exists and these facts do
not drift. The catalog describes safe routing only; it never grants approval to
execute a mutating entry.

### `atl mcp serve`

Run the typed read-only agent tool surface over MCP stdio:

```bash
atl mcp serve
atl mcp serve --service jira
atl mcp serve --service confluence
atl mcp serve --service offline
```

The default process registers twenty-three explicit Jira/Confluence evidence tools and no
mutation, shell, arbitrary-file, mirror-write, or raw-REST tool. Two no-argument
tools inspect only an explicit valid `ATL_MIRROR_ROOT`, offline, and return
content-free mirror health counts. Stdout is
reserved for protocol frames, startup skips self-update, and tool errors expose
the same stable `kind`/`remediation` classes and closed `recovery` object as CLI
JSON. Install through the
Claude Code/Codex plugin or see [mcp.md](mcp.md) for the exact tools, bounds,
standalone Codex config, and CLI fallback guidance.

Omitting `--service` preserves the complete twenty-three-tool inventory and existing
instructions. The closed Jira/Confluence/offline profiles expose 11/12/2 tools;
`offline` contains only the two no-argument mirror snapshots and
constructs no backend reader. Unknown or repeated service selections fail
before dependency construction. All profiles also publish one fixed
`application/json` resource, `atl://capabilities`, containing only static
capability ids, ordering, CLI routes, bounded MCP mappings/scopes, and CLI-only
facts. Reading it loads no config, credentials, backend, mirror path, or user
content.

`confluence_page_meta` is the body-free governance read: it returns only
schema/page identity, title, space, a positive version, an optional update
stamp, and explicit `restricted`, `unrestricted`, or `unknown` state. It has a
fixed 32 KiB encoded-result cap and omits URLs, labels, ancestors, restriction
principals, page content, and arbitrary backend metadata.

`confluence_comment_list` is body-free comment discovery for one positive
canonical page id. `confluence_comment_thread` expands one exact positive
canonical comment id from that inventory as bounded plain text. Bind either
read with `expected_page_version` when the page id/version came from earlier
evidence; omission is explicitly ungated. Both have a fixed 32-comment-page
cap, a selectable 1..1000 item bound, and a selectable 1 KiB..1 MiB encoded-result
bound, preserve completeness
qualification, and return only minimized comment
facts. Partial output never proves absence. Neither tool returns raw CSF,
inline-selection text, dedicated URL fields, email-like author identity, or
arbitrary backend error prose, and neither can preview or add a comment. Thread
`body_text` remains untrusted user-authored evidence and may contain ordinary
links or email text.

`jira_issue_refs` is the summary-only reference read: pass exactly one issue
`key`, or bounded `jql` with `limit` from 1 through 25. Up to eight exact
technical field ids may add qualified reference sources. The result preserves
selection, per-source completeness, per-issue and top-level counts, kind
buckets, and reconciliation facts, but omits raw reference URLs, issue
summaries/types, and source text. JQL mode performs one paginated comment
listing per emitted issue, so backend traffic scales with the selected limit.
Use the CLI `jira issue refs` when the URLs themselves are required evidence.

### Body input (`--from-file`)

Commands that accept a document body (CSF or Jira wiki) read it from a file
path or from stdin when you pass `-`:

```bash
# from a file
atl conf page create --space DOCS --title "New page" --from-file body.csf

# from stdin (pipe a heredoc or a prior command's output)
echo '<p>Hello</p>' | atl conf page create --space DOCS --title "New page" \
    --from-file -
```

The defaults follow one rule: commands whose body is **required** default
`--from-file` to `-` (stdin) — `conf page create`, `conf blog create`, `conf comment
preview`, `conf comment add`, `jira issue comment preview`, and `jira issue
comment add`; commands whose body is **optional** default to no
body — `jira issue create`, `jira issue update`, and the worklog comment on
`jira issue worklog add`. When stdin is an
interactive terminal (nothing piped), reading a body from it is refused with
a usage error (exit 2) instead of hanging forever waiting for input.

### Exit codes

| code | meaning |
|---|---|
| 0 | success |
| 1 | generic error |
| 2 | usage error (bad flags, missing required args, insecure backend URL) |
| 3 | authentication failed (a PAT **was** supplied but the server rejected it) |
| 4 | resource not found |
| 5 | version conflict (remote moved since last pull; re-pull and retry) |
| 6 | forbidden (per-space or per-issue permission) |
| 7 | not configured (backend URL or PAT **not set** yet; run `atl config set` / `atl auth login`) |
| 8 | safety/check failed (validation, lossy conversion, ambiguous write outcome, or app-layer Jira drift) |

A script can therefore tell three distinct "auth-ish" states apart: `7` = you
have not finished setup (no URL/token) → run setup; `3` = the token you supplied
was refused → replace it; `6` = the token is valid but lacks permission. Note the
split for a bad URL: a *missing* URL is `7`, but a *non-https* (insecure) URL is a
usage error (`2`) — fix the input rather than re-running setup.

---

## Environment variables

### Backend URLs

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_URL` | Confluence base URL (takes priority over `CONFLUENCE_URL`) |
| `CONFLUENCE_URL` | Confluence base URL (fallback) |
| `ATL_JIRA_URL` | Jira base URL (takes priority over `JIRA_URL`) |
| `JIRA_URL` | Jira base URL (fallback) |
| `ATL_ALLOW_INSECURE` | set to any non-empty value to permit a non-https backend URL for a non-loopback host (an internal http-only instance you trust). Loopback hosts are always allowed; otherwise a non-https URL is refused so the PAT is never sent in cleartext |

### Mirror location

| variable | effect |
|---|---|
| `ATL_MIRROR_ROOT` | default mirror root for `conf pull`, `conf status`, `conf diff`, and `jira pull`; required by the no-argument MCP mirror snapshot tools, which validate an existing `.atl` directory and never accept a model-supplied path (an explicit CLI `--into` still overrides it) |

Mirror writes are contained beneath the selected root even when a checkout
contains descendant symlinks. Mirror listings used by `status` and directory
`push` fail on unreadable/corrupt entries rather than reporting an incomplete
tree as success.

### Authentication

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_PAT` | Confluence Personal Access Token |
| `ATL_JIRA_PAT` | Jira Personal Access Token |

Env vars take priority over the stored credentials file. See `atl auth` below
for how to store PATs on disk.

### Config directory

| variable | effect |
|---|---|
| `ATL_CONFIG_DIR` | override config/credentials directory (default: `$XDG_CONFIG_HOME/atl` or `~/.config/atl`) |
| `XDG_CONFIG_HOME` | standard XDG base directory (used when `ATL_CONFIG_DIR` is not set) |

### Self-update

| variable | effect |
|---|---|
| `ATL_UPDATE_URL` | override the distribution server base URL |
| `ATL_NO_UPDATE` | set to any non-empty value to disable auto-update |
| `ATL_UPDATE_DEBUG` | set to any non-empty value to print self-update diagnostics to stderr |

`ATL_READ_ONLY=1` prevents writes but intentionally permits backend reads;
`ATL_NO_UPDATE=1` disables only the release check. For the complete destination
and trigger inventory, package-manager behavior, and air-gap recipe, see
[network-egress.md](network-egress.md).

---

## Scripting & CI

`atl` is built for non-interactive use: JSON to stdout, diagnostics to stderr,
stable exit codes, no prompts. A robust CI/script harness looks like this:

```bash
#!/usr/bin/env bash
set -euo pipefail

# 1. Configure entirely from the environment (URLs + PATs); no on-disk config.
export ATL_CONFLUENCE_URL="https://confluence.example.com"
export ATL_CONFLUENCE_PAT="$CONFLUENCE_TOKEN"   # from your CI secret store

# 2. Disable the best-effort self-update so a command never spends time probing
#    the release server (it is throttled, but a fresh runner has no throttle file).
export ATL_NO_UPDATE=1

# 3. Isolate credentials: point at a throwaway config dir so a leftover
#    ~/.config/atl/credentials.json from a previous job can't silently win.
export ATL_CONFIG_DIR="$(mktemp -d)"

# 4. Fail fast with a clear signal if setup/connectivity is wrong.
if atl conf search --cql 'type = page' --limit 1 >/dev/null; then
  : # connected
else
  code=$?
  case $code in
    7) echo "atl is not configured (URL/PAT missing)"   >&2 ;;
    3) echo "atl PAT was rejected by the server"          >&2 ;;
    *) echo "atl connectivity check failed (exit $code)"  >&2 ;;
  esac
  exit $code
fi

atl conf pull --cql 'label = runbook' --into "$PWD/mirror"
```

Notes for scripts:

- **Errors are JSON too.** On success `atl` prints a JSON result to stdout; on
  failure it prints `error`, the unchanged numeric `code`, stable `kind`, and
  deterministic `remediation` and schema-v1 `recovery` to **stderr** (use
  `-o text` for a plain `error: <msg>` line). Branch on `kind`/exit code;
  remediation/recovery are guidance for
  the agent to present, never permission to retry or mutate automatically.
  `rate_limited` / `wait_before_retry` means the bounded replay-safe read retry
  policy was exhausted; wait before a later read instead of immediately
  repeating it, and never retry a write automatically.
  `output_limit_exceeded` / `narrow_or_raise_bound` means the selected
  `max_bytes` rejected the complete encoded result; it is not partial evidence,
  so narrow the query/selection or deliberately choose a larger allowed bound.
- **Ordinary `--cql` pull caps at 1000 pages; `--space` at 2000.** When either cap is
  hit the result carries `"truncated": true` / `"truncated_at": N` and a
  `warning:` line is printed to stderr — the rest is not mirrored. Narrow the
  selection, or use explicit resumable `--complete` for a full historical
  selector.
- **`--from-file -` (stdin) is bounded at 64 MiB**; larger input is rejected
  with a usage error (exit 2) — pass a file path for bigger bodies.
- **Direct REST fallback:** when you must call an uncovered Server/Data Center
  endpoint yourself, keep PATs out of argv and shell history. Put the token in an
  env var, disable shell tracing, and feed curl's header through stdin:

  ```bash
  set +x
  {
    printf 'url = "%s/rest/api/2/myself"\n' "$ATL_JIRA_URL"
    printf 'header = "Authorization: Bearer %s"\n' "$ATL_JIRA_PAT"
  } | curl --fail --silent --show-error --config -
  ```

---

## `atl config`

Manage non-secret settings (backend URLs). PATs are managed separately via
`atl auth`.

### Global read-only policy

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

### `atl config show`

Print the resolved configuration (file + env overlay).

```
atl config show
atl config show -o text
```

JSON output:

```json
{
  "read_only": false,
  "confluence_url": "https://confluence.example.com",
  "jira_url": "https://jira.example.com",
  "update_base_url": "",
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

`mirror.active_root` is present only when `ATL_MIRROR_ROOT` is set. Explicit
`--into` flags still override the default for each pull/status command.

`render` is the **effective** (merged) render configuration; `render_provenance`
maps each dotted render key that is *not* a built-in default to its source
(`global` or `local`), so an all-default mirror emits no provenance at all.
`local_config_path` appears only when a per-mirror `.atl/config.json` is in scope
from the current directory. Any forbidden/unknown key in a local file is reported
to **stderr** as a `warning:` line and ignored — never applied.

### `atl config set`

Persist backend URLs, or a dotted `render.*` key, to the config file
(`~/.config/atl/config.json`).

```
atl config set --confluence-url https://confluence.example.com
atl config set --jira-url https://jira.example.com
atl config set --update-url https://releases.example.com/atl

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

### `atl doctor`

Run one privacy-safe setup diagnostic:

```bash
atl doctor
atl doctor --remote
atl doctor -o text
```

The default is fully offline and always skips self-update. It reports schema-v1
build provenance, OS/architecture, config source/parse/owner-only state, URL
policy without URL values, credential presence/coarse source, global read-only
policy, optional content-free mirror health, and the fact that plugin version
is not observable from the CLI. It emits no backend URL or hostname,
filesystem path, token, environment-variable name, identity, object id, mirror
content, or raw parser/backend error.

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

### `atl compatibility status|pin|clear`

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

### `atl environment inspect`

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

## `atl profile`

Store compact private workflow memory separately from credentials, mirrors, and
workspace guidance. The profile lives at `ATL_CONFIG_DIR/profile.json` (normally
`~/.config/atl/profile.json`), is written atomically with mode `0600`, and has five
deliberately separate sections:

- `schema`: Jira field and Confluence space facts with source + verification time;
- `preferences`: human-confirmed services and mirror choice;
- `team_policy`: explicit rules with declared provenance (never inferred);
- `render_defaults`: the agreed render shape (it does not silently rewrite config);
- `selectors`: named reusable JQL/CQL, without sampled issue/page content.

The profile may contain private field names and selectors even though it contains
no credentials. Never commit or publish it.
All `profile` commands are local/offline and skip the synchronous bounded
self-update check, so preview performs no network or unrelated filesystem
mutation.

### Preview and apply

Every write is a two-phase optimistic operation:

```sh
PRIVATE_TMP="$(mktemp -d)"       # verify mode 0700
CANDIDATE="$PRIVATE_TMP/profile.json"  # create/write with mode 0600
atl profile preview --from-file "$CANDIDATE"

atl profile apply --from-file "$CANDIDATE" \
  --candidate-hash <candidate_hash> \
  --expected-current-hash <current_hash>
```

Remove the private temporary directory on approval decline, error,
interruption, or success; never use a predictable shared `/tmp` filename.

`preview` strictly validates schema version 1, rejects unknown keys, normalizes
unordered lists, and returns the complete normalized candidate plus per-section
`added|removed|changed|unchanged` status. It never writes. `apply` requires both
exact hashes: a modified candidate fails with exit 8; a current profile changed by
another actor fails with exit 5. Concurrent cooperating applies are serialized by
an owner-only advisory lock. Apply also repairs a semantically identical profile
back to mode `0600` if it was restored with permissive permissions.

Candidates must use schema version `1`. An ordinary `show` rejects an unsupported
stored version, but `preview` may treat syntactically valid future-version bytes
as opaque state: it reports `migration_from_schema_version` and a raw current
hash. The same guarded apply can then replace those exact bytes with an approved
version-1 candidate without interpreting unknown fields.

### Context-efficient reads and guidance

```sh
atl profile show
atl profile show --section all
atl profile show --section preferences
atl profile show --section schema --service jira
atl profile show --section render_defaults --service confluence
atl profile show --section selectors --service confluence
atl profile guidance -o text
```

`show` returns metadata `{exists,path,hash}` by default. Use an explicit `--section`
and optional `--service` for `schema`, `render_defaults`, or `selectors` to load
only one backend's data; `--section all` is the deliberate full-profile escape
hatch. Service-scoped render reads return only the selected `jira` or
`confluence` object (`null` means no saved memory for that service, independent
of its sibling). They remain memory: neither `show` nor suggestion apply changes
active render config.
`guidance` emits only a short generic instruction pointing agents to those slices;
it never embeds fields, selectors, policy rules, or sampled content. The optional
`onboarding` client skill performs the consent-gated interview and preview/apply
flow. Saved `render_defaults` and `preferences.mirror_root` are memory, not active
runtime. The onboarding/learning flow compares them with `atl config show` and
requires separate approval for `atl config set render.* ...`, current-session
`ATL_MIRROR_ROOT`, explicit `--into`, or a shell-profile handoff. Declined sync is
reported as memory-only; conflicts between active and saved roots require a choice,
and shell/workspace files are never edited implicitly. Effective local render is
verified by running `atl config show` from the target mirror root; an explicit
`--into` is verified from the next approved command result's root/path, never by
causing a read/write solely for verification. Newly captured mirror paths are
canonical absolute values and are passed as one shell-quoted argument; a legacy
leading `~` is expanded without `eval`. Clearing a profile preference removes only
memory and never resets runtime implicitly. Generic workspace guidance retains
this approval protocol but never embeds the private root itself.

### Consent-gated suggestions

Later sessions can propose memory changes without silently mutating the profile.
The caller creates a version-1 observations file in a private directory. It must
name the exact current `base_profile_hash`; schema facts carry their own source
and verification time, while preference/render/selector proposals require an
`evidence` item. There is deliberately no `team_policy` key—strict decoding
rejects attempts to infer policy.

```json
{
  "schema_version": 1,
  "base_profile_hash": "<current-profile-hash>",
  "schema": {
    "jira_fields": [{
      "id": "customfield_10001",
      "name": "Risk Notes",
      "type": "string",
      "source": "approved field metadata read",
      "verified_at": "2026-07-10T12:00:00Z"
    }]
  },
  "preferences": {"services": ["jira"]},
  "evidence": [{
    "source": "approved workflow review",
    "observed_at": "2026-07-10T12:05:00Z",
    "reason": "user confirmed this recurring workflow"
  }]
}
```

```sh
atl profile suggest --from-file "$PRIVATE_TMP/observations.json" \
  --out "$PRIVATE_TMP/learning.atl-suggestion.json"

atl profile suggestion review --from-file "$PRIVATE_TMP/learning.atl-suggestion.json"

# approve the exact three hashes returned by review
atl profile suggestion apply --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash> \
  --candidate-hash <preview.candidate_hash> \
  --expected-current-hash <preview.current_hash>

# or reject that exact artifact
atl profile suggestion reject --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash>
```

`suggest` is deterministic for the same normalized observations + base profile.
It writes only the explicitly selected mode-0600 suggestion under a mode-0700
parent; the required `.atl-suggestion.json` suffix cannot collide with profile,
credential, or state filenames even if that private parent is the ATL config
directory. Parent mode validation and atomic rename use one held directory
handle. `profile.json` is untouched. `review` is read-only and returns evidence
plus the ordinary complete profile preview. `apply` is the confirmation that
turns proposed preferences into `confirmed:true`. `reject` stores only a bounded
recent window of suggestion hashes in owner-only decision state—never evidence,
selectors, or sampled content—so an identical proposal still in that window reports
`previously_rejected:true`. Delete the temporary observation/suggestion files
after either decision.

Observation objects are partial: omitted preference fields preserve their
current values, and a Jira-only/Confluence-only `render_defaults` proposal
preserves the other service. An explicit empty value clears only that named
preference/service value. Schema facts and selectors are upsert-only; removals
require the ordinary full profile preview flow. Suggestion apply updates only the
private profile; changed render/mirror preferences still require the same separate
runtime comparison, approval, and verification as initial onboarding.

### Explicit schema revalidation

Staleness uses a caller-selected absolute cutoff, never the wall clock hidden
inside the CLI:

```sh
atl profile revalidation status \
  --stale-before 2026-04-01T00:00:00Z \
  --service jira
```

The result classifies relevant facts as `fresh`, `stale`, `verified_pending`,
`missing`, or `failed`. After the user approves exact metadata reads, encode the
results (`verified|missing|failed`) in a version-1 revalidation batch carrying
the current profile hash and one explicit `checked_at`, then run:

```json
{
  "schema_version": 1,
  "base_profile_hash": "<current-profile-hash>",
  "checked_at": "2026-07-10T12:00:00Z",
  "jira_fields": [
    {
      "id": "customfield_10001",
      "status": "verified",
      "name": "Risk Notes",
      "type": "string",
      "source": "approved field metadata read"
    },
    {
      "id": "customfield_10002",
      "status": "failed",
      "source": "approved field metadata read",
      "error": "sanitized failure summary"
    }
  ]
}
```

```sh
atl profile revalidate --from-file "$PRIVATE_TMP/checks.json" \
  --out "$PRIVATE_TMP/verified.atl-observations.json"
```

Revalidation stores a bounded, newest-first-per-service set of check outcomes
in private owner-only state and
emits only successfully verified facts as a normal observations artifact.
The output name must end in `.atl-observations.json`, the corresponding reserved
non-state suffix.
Failure summaries reject control characters, redact URLs, hostnames, and IP
addresses, and are length-capped before persistence. Missing/failed checks never
delete or overwrite the last verified profile fact.
Feed the observations through `suggest → review → apply|reject`; until apply,
new facts appear as `verified_pending`. Backend reads are performed by the
calling agent only after consent—these profile commands are local/offline.

---

## Render profiles

The `.md` files in a mirror are derived staging views regenerated from the
native substrate (`.csf` / `.wiki`). A **profile** chooses what those views
contain. Supported body edits become real only after `conf apply` / `jira
apply`; generated metadata sections remain read-only, and pull/render may
replace the view. Profiles never affect substrate hashes or dirty/drift state.

Confluence views begin with `<!-- atl:document confluence-page v6 -->` and use
reserved metadata/body/comments/Jira-query boundaries. Pristine v5 and v4
views migrate only when the renderer for that exact marker reconstructs every
byte. Dirty v5/v4, older historical, unversioned, and unknown/future views are
preserved and refused; preserve any edits separately before rendering a
supported pristine legacy view.

| profile | Jira `.md` | Confluence `.md` |
|---|---|---|
| `minimal` | `key` + `summary` in `# Metadata`, `# Description` only | visible `# Content` boundary plus the native page body (same as `default`) |
| `default` | minimal **plus** `status`, `type`, `project`, `assignee`, `labels`, `priority`, `parent`, `# Image Attachments`, `# Links`, `# Comments` | visible `# Content` boundary plus the native page body |
| `full` | everything visible: default **plus** `reporter`, `created`/`updated`, `resolution`, `duedate`, `components`, `fix_versions`, configured `custom_fields`, `# Attachments` (non-image list), `# Subtasks`, `# Sprint` | read-only `# Metadata`, visible `# Content`, and readonly `# Comments` from the comments sidecar when present |

**Section names** (for `include`/`exclude`). Jira: `status`, `type`, `project`,
`assignee`, `labels`, `priority`, `parent`, `reporter`, `created`, `updated`,
`resolution`, `duedate`, `components`, `fix_versions`, `custom_fields`,
`attachments`, `attachments_all`, `links`, `comments`, `sprint`, `subtasks`,
`epic_children`. `epic_children` is intentionally in no profile base — including
it performs an additional bounded Jira query, so it must be enabled explicitly.
Confluence: `page_fields`, `comments`. The v2 format removed the legacy
`frontmatter` section; stale configs receive the normal unknown-section warning
and should migrate to typed `page_fields`. An unknown name is warned about on stderr
and ignored, never an error.

**Resolution order** (highest wins, merged per key): `--render-profile` /
`--render-include` / `--render-exclude` flags **>** local `.atl/config.json`
**>** global config **>** built-in `default`. `include` adds sections to the
profile base; `exclude` removes them.

`render.display_time_zone` follows local mirror config > global config > the
built-in `UTC` default. It affects only human `date`/`datetime` projections and
comment headings in derived Markdown. Date-only values stay calendar dates;
timestamp values are converted before formatting (for example
`2026-06-03 15:55 MSK`). The original API strings remain unchanged in
`.json`, `.meta.json`, and comment JSON sidecars. The process `TZ` environment
is never consulted, keeping offline render byte-stable across machines.

Confluence `page_fields` is a closed, read-only descriptor list. Configure it
globally or in a mirror-local render config:

```json
{
  "render": {
    "confluence": {
      "profile": "minimal",
      "include": ["page_fields"],
      "page_fields": [
        {"id": "title"},
        {"id": "ancestors", "placement": "section"},
        {"id": "updated", "format": "date"},
        {"id": "restricted", "show_empty": true}
      ]
    }
  }
}
```

IDs are `title`, `space`, `version`, `parent` (page id), `ancestors` (titles),
`labels`, `restricted`, and `updated`. Placement is `metadata` (default) or
`section`. Formats are `auto`, `scalar`, `list` (ancestors/labels), `date`, and
`datetime` (updated). Server-controlled labels and values are emitted as plain,
escaped Markdown text. Restriction state is an opt-in projection: it is fetched
only when a configured descriptor selects it, stored as known true/false in the
mirror, and cleared by a later pull that does not request it. Offline render
never guesses an unknown value; it warns and, with `show_empty`, prints a
re-pull-required value.

Two deliberate consequences of per-key merging:

- **List keys can be replaced, not emptied, by a higher layer.** An empty
  `include`/`exclude`/`custom_fields`/`field_views`/`page_fields` value means "not set here" and falls
  through to the lower layer — a local config or flag cannot clear a list the
  global config sets. To stop rendering a globally-configured custom field in
  one mirror, override the list with a different value, or counter it (e.g.
  `--render-exclude custom_fields`), or remove the key from the global config.
- **Profiles shape only the `.md` view.** The `<KEY>.json` snapshot keeps its
  standard field projection regardless of profile (`minimal` does not shrink
  it); `full` *widens* the pull's API request so every enabled section has its
  data, but nothing is removed for smaller profiles.

```sh
# per run
atl jira pull --jql "project=PROJ" --render-profile full
atl conf pull --id 123 --comments --render-profile full
atl jira pull --jql "project=PROJ" --render-include sprint --render-exclude comments

# persisted (see atl config set)
atl config set render.jira.profile full
atl config set --local render.jira.custom_fields customfield_10001,customfield_10002
atl config set --local render.jira.field_views '[{"id":"customfield_10003","label":"Risk Notes","placement":"section","format":"jira_wiki","editable":true}]'
atl config set --local render.jira.epic_field customfield_10004
atl config set --local render.jira.include custom_fields,epic_children
```

`custom_fields` (Jira only) lists custom field ids or exact display names to surface in the Markdown
metadata table under `full` (or when `custom_fields` is included); each renders
as a field/value row from the raw field (scalar verbatim; object via
`name`/`value`/`displayName`; array comma-joined; missing → omitted).

`field_views` is the typed alternative. Its `id` accepts a technical id or exact
display name during an online pull/view. Names resolve fail-closed and the
resolved id is recorded for byte-stable offline render/apply. Each descriptor is:

```json
{
  "id": "customfield_10003",
  "label": "Risk Notes",
  "placement": "section",
  "format": "jira_wiki",
  "show_empty": false,
  "editable": true
}
```

- `id` is the Jira API field id/key and is automatically added to pull's
  `fields=` projection.
- `label` is the metadata row label or section heading (defaults to `id`).
- `placement` is `metadata` (default) or `section`.
- `format` is `auto` (default), `scalar`, `list`, `jira_wiki`, `date`, or
  `datetime`; `jira_wiki` requires section placement and uses the same guarded
  wiki→Markdown renderer as Description. Valid `date` values normalize to
  `YYYY-MM-DD`; valid `datetime` values use a compact, minute-precision form
  such as `2026-06-03 12:55 UTC` or `2026-06-03 15:55 MSK`. Unexpected
  server values remain visible verbatim.
  A scalar with section `list` format becomes one bullet rather than an empty
  section.
- missing/empty values are omitted unless `show_empty` is true (`—` in
  metadata, `_Not set._` in a section).
- `editable` defaults to `false` and is valid only for
  `placement:"section"` + `format:"jira_wiki"`. In a pulled mirror it turns
  that field body into an apply surface; transient `jira issue view` output
  remains read-only. Missing editable fields render as an empty section so a
  value can be added.

Typed descriptors and legacy `custom_fields` render only while the
`custom_fields` section is enabled (`full` enables it; other profiles can
`include` it). A typed descriptor owns its id when both forms mention the same
field, preventing duplicate output. The raw value always remains unchanged in
`<KEY>.json`.

Editable field values are staged separately under
`.atl/pending/jira/<KEY>.json`; they never modify the raw `<KEY>.json` snapshot.
Offline render and pull overlay that explicit pending state in the derived
view. A successful guarded push refreshes the raw snapshot and removes the
pending record.

Generated Jira-owned boundaries are level-one headings (`# Metadata`,
`# Description`, and the configured/related-data sections) with stable hidden
`atl:section` markers. Headings from Jira rich text are nested one level below
their owner, so an original `h1.` becomes `##` while `h5.`/`h6.` keep their
exact level through a small hidden marker. `jira apply` uses those boundaries
and remains fail-closed if generated decorations are edited.

The beta metadata-table change removed the old descriptor `key`, renamed
`placement: frontmatter` to `placement: metadata`, and replaced old unmarked
level-two Jira boundaries. Update mirror-local configs and run `jira render`
(or pull again) before editing an existing view.

`epic_children` is an opt-in related-data section, not the built-in `subtasks`
field. On pull, atl resolves `render.jira.epic_field` lazily (or auto-detects the
field named `Epic Link` only after a page contains an epic candidate), groups
candidate keys from that main search page into one paginated JQL query, and
writes `<KEY>.epic-children.json` for known/inferred epics. With an explicitly
configured field, returned child rows identify localized/renamed epic types
without relying solely on the display name. The sidecar stores compact
key/summary/status/type/assignee rows and drives offline `jira render` through
the shared safe IssueList table renderer. The built-in `subtasks` section uses
the same embedded table shape. The
related query is capped at 1000 issues; a cap hit sets `truncated` /
`truncated_at` in sidecars, adds truncation fields to the pull result, and warns
on stderr. Re-pulling a non-epic with the section enabled removes a stale
sidecar. Browser-session-only provider panels are not queried.
Offline `jira render` warns when this section is enabled for an epic snapshot
that has no sidecar yet, or when sidecar issue/field identity no longer matches;
re-run `jira pull` to populate it.

**`apply` reproduces the view it was rendered with.** Every `pull`/`render`
records the resolved render settings in `.atl/state.json` (a `views` map).
`conf apply` / `jira apply` rebuild the pristine view from those recorded
settings — so an untouched `full`-profile `.md` applies cleanly and its generated
metadata table and generated `# Comments` section stay **read-only**. Only
Description and field sections explicitly recorded with `editable:true` are
merged/staged; editing other sections is refused with a pointer to the matching
command. No `--render-*` flags are needed on apply. To
override the recorded view: `jira apply` accepts `--render-*` flags; `conf apply`
has no render flags — re-run `conf render` with the desired settings instead
(it re-records the view). A pre-upgrade mirror that has no recorded view falls
back to the ambient config (today's behavior) — re-run `render` once to record it.

### `atl jira render` / `atl conf render`

Regenerate the `.md` views of an existing mirror **offline** — no network, no
PAT — so changing a profile does not force a re-pull.

```sh
atl jira render                       # re-render the whole mirror (default root)
atl jira render mirror-jira/PROJ/PROJ-1.md --render-profile full
atl conf render mirror --render-profile full
atl conf render mirror/DOCS/page/page.csf --render-exclude comments
```

The target is a mirror directory, a `.md`, or the substrate file (`.wiki` for
Jira, `.csf` for Confluence); the mirror root is found by walking up to the
`.atl` marker. Only `.md` files are rewritten — the `.csf`/`.wiki`/`.json`
substrate and the `pages` sync entries are never touched, so `jira status` /
`conf status` stay clean across a re-render. Each rendered view's settings,
including `display_time_zone`, are recorded in `.atl/state.json` (the `views`
map) so a later `apply` can reproduce
it. A Confluence `.csf` that fails to parse yields the same markdown-unavailable
stub as `pull`.

Jira directory render checks every existing document marker before the first
rewrite, then repeats each target check under the mutation lock. `jira pull`
also refuses to overwrite an explicit future/unknown `.md` marker before it
changes that issue's artifacts. A CRLF marker line is recognized without
normalizing the rest of the view. Malformed or unreadable Jira `.json`
snapshots are skipped with one stderr warning per path; they are never silent.

| flag | description |
|---|---|
| `--render-profile` | `minimal` \| `default` \| `full` (overrides config) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |
| `--into ROOT` | mirror root when no target argument is given |

---

## `atl auth`

Manage Personal Access Tokens. PATs are written to a mode-0600 credentials
file (`~/.config/atl/credentials.json`) or resolved from env vars. They are
never stored in the mirror or the repository.

### `atl auth login`

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

### `atl auth status`

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

### `atl auth logout`

Remove a stored PAT from the credentials file.

```
atl auth logout --service confluence
```

---

## `atl conf` — Confluence

### `atl conf search`

Search pages by raw CQL or convenience filters. JSON is a versioned bounded-page
envelope with the exact `query`, `results`, `count`, `complete`, `truncated`,
optional `partial_reason`, and nullable `next_cursor`. Each result carries
`id`, `title`, `space`, `version`, and `excerpt`. Pass either `--cql` or at
least one convenience filter; the two modes cannot be combined.

`complete:true` is emitted only when qualified backend pagination proves the
page terminal. If `truncated:true`, continue with `--cursor` when present and
do not treat missing hits as evidence of absence. `-o text` renders the same
qualification followed by a Markdown candidate table; `-o id` emits only page
ids.

```json
{
  "schema_version": 1,
  "query": "space=DOCS and title~\"API\"",
  "results": [],
  "count": 0,
  "complete": true,
  "truncated": false,
  "next_cursor": null
}
```

```
atl conf search --cql "space=DOCS and title~\"API\"" --limit 10
atl conf search --space DOCS --title API --type page --limit 10
```

Flags:

| flag | description |
|---|---|
| `--cql` | Confluence CQL query (mutually exclusive with convenience filters) |
| `--space` | convenience filter by space key |
| `--title` | convenience substring filter by title |
| `--label` | convenience filter by label |
| `--type` | convenience filter by content type |
| `--limit` | max results (default 25) |
| `--cursor` | pagination cursor (start offset returned by the previous call) |

### `atl conf space tree`

Return the page hierarchy of a space. `depth 0` means unlimited.

```
atl conf space tree --space DOCS
atl conf space tree --space DOCS --depth 2
```

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--depth` | maximum depth (0 = unlimited) |

The listing stops at a 2000-page safety cap; when hit, the JSON result carries
`"truncated": true` and a `warning:` line goes to stderr.

### `atl conf pull`

Mirror pages to disk. Downloads `.csf` (native storage format), `.md`
(read-view), `.meta.json`, and optionally renders draw.io / image assets and
mirrors page comments.

```bash
# single page
atl conf pull --id 12345678

# all pages in a space
atl conf pull --space DOCS --into my-mirror

# pages matching a CQL query
atl conf pull --cql "label=public and space=DOCS" --assets

# complete historical bootstrap beyond the ordinary 1000/2000 caps; an
# interrupted run resumes its exact private selector snapshot automatically
atl conf pull --complete --cql 'space=DOCS and type=page' --into my-mirror

# Opt in only after reviewing backend capacity: overlap at most four native
# page-body reads while pacing every Confluence/Jira attempt to eight starts/s.
atl conf pull --complete --cql 'space=DOCS and type=page' \
  --page-prefetch 4 --requests-per-second 8 --into my-mirror

# also bring page comments into the mirror
atl conf pull --id 12345678 --comments

# complete changed-page delta; bootstrap with one reviewed absolute instant
atl conf pull --incremental --cql 'space=DOCS and type=page' \
  --since '2026-07-01T00:00:00+02:00' --into my-mirror

# later runs reuse the watermark bound to this exact selector
atl conf pull --incremental --cql 'space=DOCS and type=page' --into my-mirror

# inspect the selected refresh without changing files, state, or checkpoints
atl conf pull --space DOCS --into my-mirror --dry-run
```

Flags:

| flag | description |
|---|---|
| `--id` | single page id |
| `--cql` | CQL query selecting pages |
| `--space` | space key (mirrors the whole space) |
| `--depth` | depth limit when using `--space` (0 = unlimited) |
| `--assets` | download draw.io PNG renders and inline images |
| `--comments` | mirror schema-v2 comment evidence to `<slug>.comments.json`, render its qualified read-only tree in the main `.md`, and refresh the flat `.comments.md` compatibility view |
| `--dry-run` | perform selection and local qualification without writing mirror files, state, watermarks, checkpoints, or stashes |
| `--overwrite-local` | explicitly replace a qualified locally edited native `.csf`; never bypasses derived-view or baseline-integrity failures |
| `--stash-local` | before replacement, preserve a qualified locally edited `.csf` in the immutable content-addressed `.atl/stash/` store; mutually exclusive with `--overwrite-local` |
| `--complete` | build and consume an exact resumable two-pass selector snapshot; requires `--cql` or `--space` and does not support `--depth` |
| `--restart-complete` | explicitly replace an unfinished complete snapshot after a fresh stable selection and local preflight |
| `--incremental` | exhaustively select changes since a persisted selector watermark; requires `--cql` or `--space` |
| `--since` | first-run lower boundary as an exact RFC3339 minute with explicit `Z` or numeric offset |
| `--max-pages` | selection cap: incremental defaults to 10000; complete mode uses `0` as no configured cap (the local one-million-identity / 64 MiB checkpoint guards still apply) |
| `--page-prefetch` | ordered native-page-body read window for incremental/complete mode (`1` default, max `8`); mirror writes/checkpoints remain serial |
| `--requests-per-second` | shared request-start pace across Confluence plus optional Jira-macro traffic (`0` default means no proactive delay; max `1000`) |
| `--jira-view` | named `jira_list_views` projection for Jira JQL macros whose macro configuration does not specify columns |
| `--jira-macros` | `auto` (default) or `off`; `off` keeps placeholders and performs no Jira credential read/search |
| `--into` | mirror root directory (default `mirror`) |
| `--render-profile` | `.md` view profile: `minimal` \| `default` \| `full` (see [Render profiles](#render-profiles)) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |

At most one of `--id`, `--cql`, `--space` may be given.

Pull is non-destructive by default. Before each page-body GET, atl reconciles
the tracked path, sidecar hash, pristine base, native `.csf`, metadata, and
derived Markdown view. A local native edit or unqualified artifact is preserved,
reported under `local_safety`, and skipped while safe siblings continue. The
command then exits `8`, so automation cannot mistake a partial refresh for a
complete one. `--overwrite-local` and `--stash-local` apply only to a native
edit whose baseline is fully qualified; they do not discard unapplied Markdown,
future/unsupported views, missing artifacts, path drift, or corrupt state.
Stashes contain the exact previous native bytes and are named by their SHA-256.
Incremental watermarks and complete-pull checkpoints never advance past a
blocked page.

Complete mode is the explicit historical bootstrap for a selector larger than
the ordinary CQL/space caps. It exhausts qualified search pagination twice,
requires the same unique page-id set in both passes, canonicalizes that set
locally, and only then starts page-body GETs. Missing/duplicate identities,
repeated cursors, unreachable advertised results, selection drift, or an
explicit cap fail with exit `8` before any body request or new checkpoint.
User CQL containing `ORDER BY` is rejected; atl does not depend on an
undocumented id-order guarantee from the backend.

The exact identity snapshot and its durable prefix live in a private,
schema-versioned, mode-0600 pair under `.atl/complete-pulls/`: an immutable
`<selector-sha256>.json` id manifest plus a small
`<selector-sha256>.progress.json`. They contain ids, hashes, and the next index,
never credentials, backend URLs, titles, or page bodies. Progress updates do
not rewrite the large manifest. Repeating the same command resumes the
remaining prefix without repeating completed body GETs. Assets,
comments, effective render settings, and the resolved Jira-macro list view are
hash-bound; option drift fails closed. `--restart-complete` replaces an old
snapshot only after a fresh two-pass selection and local overwrite preflight
succeed, so a failed restart leaves the previous resume point intact.

Before body reads, native/Markdown local edits and partial/corrupt tracked
artifacts block the exact remaining set. Progress is committed after each
25-page batch and on a graceful failure. A hard process crash can therefore
re-fetch at most the uncheckpointed tail, but never skip it. Page downloads
are serial by default. `--page-prefetch N` may overlap up to `N` native body
GETs and can therefore read a bounded tail beyond the first page that later
fails; only the canonical sequential consumer claims paths, resolves/writes
assets and sidecars, performs relocation, or advances a checkpoint. This mode
intentionally costs two metadata search passes plus one body GET per selected
page; it runs only when explicitly requested and performs no background or
calibration queries. Requested comment truncation does not advance past that
page. Completion removes the checkpoint; neither a snapshot nor its absence is
evidence of remote deletion.

Incremental mode is deliberately inclusive at its lower minute. The first
`--since` is an absolute RFC3339 instant, so a DST fold or the timezone of the
machine running atl cannot change it. Atl canonicalizes the watermark to UTC.
Confluence CQL date literals have only minute precision and carry no offset;
the effective backend parser timezone is therefore reported as unknown rather
than inferred through hidden calibration searches. Every CQL read renders a
UTC-based literal 48 hours before the absolute boundary and locally discards
older hits by their exact REST timestamps. Across the IANA offset range, a
different backend CQL zone can only over-fetch rather than omit; the explicit
`--max-pages` cap remains fail-closed. Atl records
every page id/version at the completed absolute minute. A repeat skips only
those exact pairs: a new page or newer version in the same minute is still
fetched. Proven `absolute-overlap-v1` watermarks migrate to canonical UTC only
after a complete successful run; older state without a bound absolute instant
is rejected with guidance to preserve the old mirror. Results are paged until the
backend proves exhaustion, then the metadata pass is repeated and its
`(id,version,updated)` set must match before any body is fetched. Only
`type=page` hits are admitted. A repeated cursor, unreachable advertised total,
explicit cap, or malformed timestamp exits `8` and leaves the watermark
unchanged. `ORDER BY` in user CQL is rejected because atl appends
`lastmodified asc`; there is no dependency on an undocumented id tie-breaker.

Before the first page body fetch/write, the entire selected local set is
preflighted. Native CSF edits, unapplied Markdown edits, partial page artifacts,
or corrupt state block the batch. A supported v5/v4 legacy `.md` is accepted
only if its version-specific renderer reproduces every byte;
`view_migrations` counts those proven views, and each is rewritten
to the current format only when its page pull succeeds. A changed legacy view
gets a legacy-specific reconciliation error, while unversioned and
unknown/future views are preserved and never downgraded. A network/permission failure may leave pages
already mirrored through the ordinary atomic path, but never advances
`.atl/incremental.json`; rerunning replays the same inclusive range safely.
Empty deltas still commit a valid first watermark. Absence from a delta is
never interpreted as deletion or permission loss. Requests are serial in this
mode by default; the stability check intentionally doubles search-page GETs
but not body GETs. Opt-in prefetch has the same sequential write/watermark
boundary as complete mode. `--comments` truncation also prevents watermark
advancement.

Both large modes expose a `scheduling` result with `page_prefetch`,
`max_in_flight`, and `requests_per_second`. The command-scoped scheduler is
shared by Confluence and optional Jira-macro clients and wraps each actual
transport hop, including retries, redirects, comments, and streamed assets. It
holds an in-flight permit until the response body reaches EOF or closes, paces
request starts, and publishes a bounded server `Retry-After` cooldown to all
clients. Existing requests may finish, but no newly admitted attempt bypasses
that cooldown. Defaults are `1/1/0`, so installing this feature does not
increase backend load. The limits are proactive safety bounds, not an adaptive
throughput promise.

The `--render-*` flags override the configured profile for this run; the pull
result JSON is unchanged by the profile (they affect only the `.md` view).

Jira JQL macros are enriched on a best-effort read path when Jira credentials
are configured. Their original placeholder stays in `# Content`; resolved rows
use the shared Jira IssueList Markdown table under generated readonly `# Jira
Queries`. Explicit macro columns win, otherwise `--jira-view` selects the
`confluence_macro` projection (`default` when omitted). Pull records the typed
snapshot in `<slug>.jira-macros.json`, allowing offline render and apply to
reproduce the exact generated suffix without rerunning JQL. A missing Jira
configuration or one failed query retains the placeholder and emits a bounded
stderr warning; it never blocks the native page pull. Resolution is capped per
page at 20 JQL macros and 2000 total rows (1000 per macro); omitted macros stay
as placeholders and are reported.

Set `render.confluence.jira_macros` to `off`, or pass `--jira-macros off` to
`conf pull` / `conf page view`, when page-provided JQL should not execute with
the current user's Jira identity. The opt-out is resolved before Jira
credentials are loaded, retains readable placeholders, removes any generated
query sidecar on pull, and emits a bounded warning. `--jira-view` is invalid
while expansion is off.

`--comments` is opt-in: without it, no comment endpoint is contacted and no
comment files are written. Comments are auxiliary read-only data — they never
enter the page content hash or the version gate, so a page carrying comment
sidecars still reports Clean in `conf status`. Each comment retains a plain-text
`body` fallback and, when supplied, native `body_storage` CSF so the readonly
Markdown preserves paragraphs, lists, links, emphasis, and headings. It is not
part of the page write substrate. A re-pull **with**
`--comments` rewrites the sidecars; a re-pull **without** `--comments` leaves any
existing comment files untouched (they are never auto-deleted). If a page's
comment listing hits the fetch safety cap, the sidecar is incomplete, the meta
carries `comments_truncated: true`, and a stderr warning fires. Comment-enabled
complete/incremental pulls do not advance their checkpoint when comment or
thread completeness is false; anchor-only partiality remains durable but does
not block selection progress.

**Mirror layout after pull**

```
mirror/
  DOCS/
    parent-page/
      child-page/
        child-page.csf           ← edit this
        child-page.md            ← derived staging view; with schema-v2 comments, includes the qualified read-only tree
        child-page.meta.json     ← id, version, hierarchy, labels, updated, optional restricted, content_hash, fragments, comment state
        child-page.comments.json ← only with --comments: qualified schema-v2 envelope (legacy arrays remain readable)
        child-page.comments.md   ← only with --comments: best-effort flat compatibility view
        child-page.assets/
          diagram.png
  .atl/
    state.json                 ← remote sync, render, and staged-local lineage
    incremental.json           ← completed selector-bound lower boundaries (0600)
    base/
      12345678.csf             ← pristine copy for diff
```

Confluence pull/render/apply/push and mirror-local `conf edit` are serialized by one persistent advisory
lock under `.atl`; contention exits `8` before page/state writes. Wait for the
active operation—do not remove the lock file. Read-only status stays lock-free.
When Jira and Confluence share the root, their sidecar patches also use the
backend-neutral `.atl/state.lock`: a collision gets a brief bounded retry
window, then fails closed rather than losing the other service's `state.json`
entries.

Pull and direct page reads require the requested body projection from the
backend (`body.storage.value` for CSF, `body.view.value` for rendered view). A
successful partial response that omits it exits `8` before output/artifacts are
treated as an empty page; an explicitly present, zero-byte body is accepted.
If only the refresh after a successful push omits the body, atl preserves
the local mirror and reports a re-pull warning instead of replacing it with an
empty page.

Missing local targets for `conf render`, `conf apply`, and `conf push` all map
to exit `4` (`not found`). Malformed target kinds or incompatible flag
combinations remain exit `2` (`usage`). Offline render may migrate pristine
v5/v4 views after exact version-specific reconstruction, but refuses to
overwrite edited legacy, older historical, unversioned, or unknown/future
views. Directory renders inspect every selected
view first and make no view changes if any selected marker is unsupported.

### `atl conf table extract`

Extract tables from a page's native CSF body into structured data. This is useful
when the page has multiple tables or merged cells and a script needs something
more explicit than the markdown read-view.

```bash
# all tables as JSON, preserving per-cell metadata
atl conf table extract --id 12345678

# one table as rectangular CSV
atl conf table extract --id 12345678 --table 2 --format csv
atl conf table extract --id 12345678 --table 2 --expected-version 7 --format csv
atl conf table extract --id 12345678 --table 2 --format csv --raw-csv # unsafe spreadsheet interoperability

# all tables as an XLSX workbook, one sheet per table
atl conf table extract --id 12345678 --format xlsx --out tables.xlsx
```

Flags:

| flag | description |
|---|---|
| `--id` | page id |
| `--table` | 1-based table index to extract (0 = all tables) |
| `--expected-version` | optional positive page version already observed for this selection |
| `--format` | `json`, `csv`, or `xlsx` |
| `--out` | optional output file; required for `xlsx` |
| `--raw-csv` | preserve formula-leading cells verbatim; CSV only and unsafe in spreadsheets |

JSON preserves the expanded cells, durable compact provenance, ordinary links,
and visible inline color markers. In schema v3 a native cell is the unmarked
default with no source coordinates, a span repeat has `repeated:true` and names
its covering origin, and rectangular padding has `synthetic:true` with no
source coordinates. Every JSON table
also includes a required `summary` record with the same reconciled structural
metrics as `atl conf table summary`, so scripts can consume exact counts without
recounting cells. Top-level `returned_table_count` is the actual length of
`tables`, while `selection_reconciled` confirms that a selected index returned
exactly that table or that an unselected read returned the full page-wide
`table_count`. CSV without `--table` emits a cell-level stream so pages with
different table shapes can share one file; CSV with `--table` emits a
rectangular table.
JSON results also include `schema_version:3`, the exact
`cell_contract:"confluence-table-cells/compact-v3"` marker, the positive page
`version`, and `page_version_gated`. When a table index came from `conf table summary`, pass
that summary's exact version with `--expected-version`; omission is explicit
ungated evidence for a directly fixed index. A stale positive version exits
`8` before emitting table evidence; a negative value is a usage error and
exits `2`.
CSV prefixes cells beginning with `=`, `+`, `-`, `@`, tab, CR, or LF with an
apostrophe by default so opening untrusted page data in a spreadsheet does not
execute it as a formula. `--raw-csv` is an explicit unsafe escape hatch.

With `--out`, all three formats persist through one atomic writer (temp file
then rename), so a partial or truncated artifact never lands; missing parent
directories are created as needed. A persistence failure exits `8` and writes
nothing to stdout, leaving any prior file in place; a missing `--out` for
`xlsx` is still a usage error (exit `2`). The success acknowledgement JSON/text
is unchanged, and JSON and CSV without `--out` still stream to stdout.

### `atl conf table summary`

Inventory table structure without emitting page titles, cell text, URLs, style
values, raw attributes, or warning text. Use this bounded read before choosing
a table to extract, or when only shapes and structural features are needed.

```bash
atl conf table summary --id 12345678
atl conf table summary --id 12345678 --table 2
atl conf table summary --id 12345678 --expected-version 7
```

`--table` is 1-based; zero summarizes every table while preserving the page's
total `table_count`. `returned_table_count` and `selection_reconciled` qualify
that selection. Counts use the expanded rectangular representation and expose
native origins, span repeats, synthetic padding, direct rowspan/colspan
metadata, non-empty text/Markdown/raw cells, style entries, and distinct style
key/value markers without revealing their values. `rectangular` reports grid
shape. `cell_count_reconciled` additionally requires an independent
source-placement ledger: declared span rectangles may not overlap or leave the
source row domain, and every claim must agree with the expanded grid. Rowspan and
colspan source cells remain separate from coordinate-covered positions, avoiding
an ambiguous combined span count. Native `rowspan` and `colspan` values above
100 are refused with exit `8` before the rectangular grid is expanded; the
command never reports a silently clamped shape as reconciled.
The result reports `schema_version:3`, the exact
`cell_contract:"confluence-table-cells/compact-v3"` marker, the positive page
`version`, and `page_version_gated`. `--expected-version` optionally binds this read to an
already-observed revision without another request. A stale version fails before
any table evidence is returned with exit `8`; a negative value is a usage error
and exits `2`.

### `atl conf status`

Show which mirrored pages have local edits and which have drifted on the
remote since the last pull.

```bash
atl conf status
atl conf status my-mirror
atl conf status --remote          # also checks remote version (one request per page)
```

Local edits are shown with `M`; remote drift with `M↯` in text mode.
Missing, corrupt, or id-less sibling `.meta.json` is a mirror integrity failure:
the scan stops and returns exit `8` instead of silently omitting that page.

Flags:

| flag | description |
|---|---|
| `[DIR]` | mirror root directory (default `mirror`) |
| `--remote` | also check remote for drift (one API call per page) |

### `atl conf snapshot`

Return exact, content-free health cardinalities for a durable Confluence mirror.
The offline default needs no backend URL, PAT, or config and performs no writes.
It takes a shared advisory lock only when the persistent mutation lock already
exists. An active mutation fails closed with exit `8` before inspection; a
legacy mirror without that lock remains write-free and is re-inspected if a
current writer creates the lock during the first read.

```bash
ATL_READ_ONLY=1 atl conf snapshot
ATL_READ_ONLY=1 atl conf snapshot my-mirror
ATL_READ_ONLY=1 atl conf snapshot my-mirror --remote
```

The JSON partitions local clean/edited and tracked/untracked pages, every native
diff state, baseline presence/validity, candidate CSF validity, and derived-view
marker state. Current, known legacy, missing-marker, unsupported/future,
missing, and unreadable views remain distinct. `renderer_compatible` reports
only whether this binary understands the observed format; it does not prove a
view has no edits, and the command never rewrites that view.

Every section has a `reconciled` flag and the top-level flag requires all
partitions to agree. `complete:false` means some requested evidence is not
usable (for example malformed or corrupt native state, an unreadable derived
view, or a failed requested remote probe); it is separate from arithmetic
reconciliation. Any incomplete local evidence stops before remote setup or
probing. A corrupt baseline still emits the qualified snapshot and returns exit
`8`, even when `--remote` was requested. If writing that snapshot to stdout
fails, the write failure is reported together with the inspection failure and
the exit code stays the inspection code. If inspection otherwise succeeds, the
write failure is returned on its own with generic exit `1`.

`--remote` starts one metadata probe per eligible canonical tracked page and
disables the transport's automatic replay-safe retries for it. Redirect
responses are not followed and count as unavailable because a second hop would
exceed the one-attempt bound. Untracked/non-canonical pages remain `not_attempted`;
failures increment `unavailable` and never `in_sync`. The output never includes
page ids, titles, paths, hashes, validation text, or native/derived content. Use
`conf diff` only when page-level identity or exact change evidence is required.

### `atl conf diff`

Compare the exact last-synced native `.csf` baseline with one current page or
every tracked page in a subtree. The command is offline, makes no backend
requests, and never converts the write substrate through Markdown.

```bash
ATL_READ_ONLY=1 atl conf diff mirror/DOCS/guide/guide.csf
ATL_READ_ONLY=1 atl conf diff mirror/DOCS/ -o text
```

JSON uses `schema_version: 1` and reports a stable path-ordered `pages` array.
Each page state is one of `unchanged`, `added`, `removed`, `modified`,
`malformed`, `missing_baseline`, `baseline_mismatch`, or `unreadable`. Root and
target are canonical absolute identities, so relative roots and contained
symlink aliases cannot split one mirror into duplicate path namespaces.
Modified pages include:

- semantic block changes using canonical DOM fingerprints (attribute order and
  equivalent entity spelling do not create semantic changes);
- aggregate macro and opaque-fragment count deltas without copying page text;
- exact SHA-256/byte-count evidence for the changed byte window;
- candidate and baseline validation consequences.

`byte_only:true` means native bytes changed while the understood block and
feature projections remained equivalent. `complete:false` means at least one
comparison lacked a usable baseline or a body was malformed/unreadable. Missing
pre-upgrade baselines are never guessed: re-pull to establish one, preserving
any local edits first. `baseline_mismatch` specifically means the pristine base
bytes no longer match the tracked sync hash; preserve local candidates and
repair/re-pull the mirror rather than treating it as an unreadable page.
Directory scans fail closed on corrupt metadata,
descendant symlinks, or unreadable entries instead of silently omitting pages.
The Markdown projection is a compact review table. Its paths are relative to
the mirror root, `Review` is the closed `semantic|byte-only|none|n/a`
classification, and `Deltas` counts understood block plus feature changes. Use
`-o text` first for directory triage; use JSON when an agent needs canonical
absolute evidence paths, block hashes, feature deltas, byte windows, or
validation details.

Flags:

| flag | description |
|---|---|
| `[file.csf\|DIR]` | page or subtree; omitted uses the configured mirror root |
| `--into` | explicit mirror root (otherwise nearest `.atl`) |

### `atl conf plan create` / `atl conf plan preview` / `atl conf plan apply`

Use a durable plan when several native page updates must be reviewed as one
closed set. Plan creation is offline and accepts the same page/subtree target as
`conf diff`:

```bash
export ATL_READ_ONLY=1
atl conf plan create mirror/DOCS/ --out .atl-private/docs-plan.json
```

The output file has mode `0600`, schema `atl.confluence.plan/v1`, deterministic
bytes, and a proposal hash over the complete artifact. It includes only
`update` entries for modified, canonical, valid, baseline-backed pages. Every
entry declares page content type and binds the content id, title, space, mirror-relative path, expected
version, exact baseline/candidate SHA-256, validation warnings, semantic block
and feature consequences, and byte-window evidence. Native CSF bodies are not
copied into the plan. Added, removed, malformed, missing-baseline,
baseline-mismatch, unreadable, or relocated pages make creation fail before the
artifact is written.
Do not reformat or convert the line endings of a plan: apply requires the exact
canonical bytes as well as the embedded and externally reviewed hashes.
`--out` must name a new file: atl never replaces an existing reviewed artifact.

Plan files contain page titles and local workspace paths. Keep them private; do
not commit or publish them even though body prose is omitted.

Preview without leaving the intentionally exported global read-only environment:

```bash
atl conf plan preview .atl-private/docs-plan.json
```

Preview revalidates the complete local plan, then GETs every remote page before
any write. Each entry becomes `would_apply`, `already_satisfied`, `stale`,
`blocked`, or `not_checked`. If any local/remote binding changed, the batch is
blocked with zero PUTs.

After reviewing the exact proposal hash and obtaining approval:

```bash
env -u ATL_READ_ONLY atl conf plan apply .atl-private/docs-plan.json \
  --expected-proposal-hash <64-hex-hash> \
  --confirm APPLY
```

Apply repeats the complete preflight, then sends one version-gated PUT per
pending entry. Every response is reconciled with a native GET. Exact
`expected_version+1` candidate state is `applied` (and may be marked
`reconciled`); a prior exact success is `already_satisfied` and is never
replayed. A failed or unknown outcome stops the remaining writes, marks them
`not_attempted`, returns non-zero, and must not be automatically replayed.
Rerunning the same plan is safe after inspection: exact applied entries are
recognized and their mirror state is refreshed, while any other state is
blocked. There is no force mode, remote delete, create, move, or automatic
merge in v1.

`conf plan apply` is execution-only: omitting either the exact external hash or
`--confirm APPLY` exits 2 before config, plan loading, or network access.

Create flags:

| flag | description |
|---|---|
| `[file.csf\|DIR]` | one page or subtree; omitted uses the configured mirror |
| `--into` | explicit mirror root |
| `--out` | required new durable plan path; stdout and replacement are unsupported |

Apply flags:

| flag | description |
|---|---|
| `--confirm APPLY` | required exact confirmation for execution |
| `--expected-proposal-hash` | exact reviewed hash; required for execution |

### `atl conf validate`

Validate a `.csf` file for XML well-formedness, supported structural depth, and
common sanity issues. Well-formedness and `max-depth` errors (severity
`"error"`) block a push. Sanity problems (severity `"warning"`) are advisory.

```bash
atl conf validate mirror/DOCS/guide/guide.csf
```

`<file.csf>` is the required local file to validate.

Flags:

| flag | description |
|---|---|
| `--cloud-compat` | also report advisory Confluence Cloud compatibility findings (`cloud-compat/*` warnings; never blocks) |

Output (JSON):

```json
{
  "file": "mirror/DOCS/guide/guide.csf",
  "ok": false,
  "problems": [
    {
      "severity": "error",
      "line": 14,
      "col": 5,
      "rule": "well-formedness",
      "message": "malformed CSF: XML syntax error on line 14: unexpected end element </p>"
    }
  ]
}
```

Exits 8 (`check_failed`) when any error-severity problem is found; the result
and `problems[]` are still emitted. Exits 0 when there is no blocking problem.

`max-depth` rejects CSF nested beyond 1024 elements before recursive rendering
or inspection. The diagnostic contains only the observed depth and limit, not
document content.

Advisory `invisible-chars` warnings flag characters that render invisibly but
defeat exact-string editing — non-breaking spaces (`U+00A0`), zero-width
characters, soft hyphens — one warning per class with the occurrence count and
first position. They never block a push; use `atl conf edit` (tolerant
matching) when they are present.

#### `--cloud-compat` — advisory Cloud compatibility inventory

Opt-in. Without the flag nothing changes: the default diagnostics and the
default JSON object (`{file, ok, problems}`) are exactly as before. With the
flag, `conf validate` appends `cloud-compat/*` findings to `problems[]` in
document order after the default diagnostics, and adds one `cloud_compat`
object that identifies the rule pack:

```json
{
  "cloud_compat": {
    "rule_pack": "v1",
    "source_date": "2026-07-25"
  },
  "file": "mirror/DOCS/guide/guide.csf",
  "ok": true,
  "problems": [
    {
      "severity": "warning",
      "line": 1,
      "col": 4,
      "rule": "cloud-compat/macro-not-insertable",
      "message": "macro \"info\" cannot be inserted in the Confluence Cloud editor; Atlassian documents a Cloud editor migration or conversion path for existing content"
    },
    {
      "severity": "warning",
      "line": 2,
      "col": 23,
      "rule": "cloud-compat/nested-table",
      "message": "table nested inside another table; the Confluence Cloud editor does not support nested tables"
    }
  ]
}
```

Every `cloud-compat/*` finding has severity `"warning"`, so the flag can never
change `ok`, the push gate, or the exit status of an otherwise valid page.

The `v1` rule pack is closed — these five stable rule names and no others:

| rule | what it reports |
|---|---|
| `cloud-compat/macro-not-insertable` | a listed macro that cannot be inserted in the Cloud editor; Atlassian documents how existing content migrates or converts |
| `cloud-compat/macro-view-only` | a listed macro removed from both Cloud editors; existing instances stay visible but cannot be inserted or edited |
| `cloud-compat/macro-removed` | a listed macro removed from Confluence Cloud |
| `cloud-compat/nested-bodied-macro` | a macro carrying a body nested inside another macro, which the Cloud editor does not natively support |
| `cloud-compat/nested-table` | a table nested inside another table, which the Cloud editor does not support |

The macro category is carried by `rule`, never by the message prose, so tooling
can branch on the rule name without parsing text. Findings are anchored with
1-based `line`/`col` into the original bytes.

`rule_pack` and `source_date` are part of the output because Atlassian's
support pages change over time: `source_date` records when the pack was last
reconciled against the official documentation on
[what migrates with the Confluence Cloud Migration Assistant](https://support.atlassian.com/migration/docs/what-migrates-with-the-confluence-cloud-migration-assistant/),
[which macros are being removed](https://support.atlassian.com/confluence-cloud/docs/learn-which-macros-are-being-removed/),
and [legacy editor vs. Cloud editor differences](https://support.atlassian.com/confluence-cloud/docs/differences-using-confluence-legacy-editor-and-cloud-editor/).
Record both values alongside any finding you keep.

The pack is deliberately conservative. Read its output as an inventory, not a
verdict:

- It does not predict or guarantee migration success or failure. A clean run is
  not a promise that the page migrates intact, and a finding is not a claim that
  migration will fail.
- Only macro keys named explicitly on Atlassian's official compatibility list
  are classified. An unlisted marketplace app, user, or unknown macro is never
  guessed at, so an absent finding says nothing about it.
- It converts nothing. There is no CSF-to-ADF conversion, no Cloud or Data
  Center API call, no migration execution, and no file is written — validation
  stays offline and read-only.
- A body that is not well-formed short-circuits before the pack runs, because
  the rules need a parsed document. The result still carries `cloud_compat` and
  the well-formedness error, but no `cloud-compat/*` finding — and their absence
  is not a clean bill of health. Fix the XML error, then re-run with the flag.

### `atl conf edit`

Replace text in a local file with tolerance for the invisible bytes that break
exact-match editing of real CSF (non-breaking spaces `U+00A0`, zero-width
characters, `&nbsp;`/`&#160;`/`&#xa0;` entities). Matching runs in layered
passes — exact bytes, then invisible-tolerant, then whitespace-run-tolerant —
and the replacement is spliced into exactly the matched byte range; every
surrounding byte is preserved verbatim. The replacement itself is inserted
literally.

```bash
atl conf edit page.csf --old 'Запрос предназначен для получения' --new 'Запрос возвращает'
atl conf edit page.csf --old-file old.txt --new-file new.txt --dry-run
atl conf edit page.csf --old '<td>ok</td>' --new '<td>done</td>' --all
atl conf edit page.csf --old ' obsolete sentence.' --new ''          # delete
```

Flags:

| flag | description |
|---|---|
| `<file>` | local file to edit (positional, required) |
| `--old` | text to find (tolerant matching) |
| `--old-file` | read the text to find from a file (`-` for stdin; one trailing newline stripped) |
| `--new` | replacement text, inserted verbatim (`--new ''` deletes) |
| `--new-file` | read the replacement from a file (one trailing newline stripped) |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match without writing |

Output includes which pass matched (`"pass"`), match count and byte offsets,
and quoted `region_before`/`region_after` context for review. For `.csf`
files the result is validated automatically: `"csf_ok"` plus `"problems"`
appear in the output, and a not-well-formed result prints a stderr warning
(the file is still written; `conf push` remains the gate).

When the file belongs to an initialized mirror, dry-run and write both join the
same persistent Confluence mutation lock as pull/render/apply/push; contention
exits 8 before reading or changing the file. Symlink targets are resolved before
lock discovery; an alias outside the mirror joins the target mirror's lock, and
an alias visibly inside a mirror that resolves outside it is refused.

Exit codes: `4` — the target file is missing or the text was not found in any
pass (a no-match error carries a quoted dump of the closest region, exposing
hidden bytes); `2` — the match is ambiguous (make `--old` more specific or pass
`--all`).

Usage notes: keep `--old`/`--new` short and inline — match an anchor around
the change, not a whole sentence or table row; `--old-file`/`--new-file` are
for content that already lives in a file, not worth a write-then-clean-up
ceremony. The command is atomic (a miss leaves the file untouched), so
`--dry-run` is only needed for risky substitutions such as `--all`. For long
spans (deleting a section, replacing a table row) splice between two short
boundary anchors with a checked script instead of matching the full span —
see the confluence skill's CSF reference for the decision table.

### `atl conf apply`

Merge edits from a page's markdown view (`page.md`) into its `.csf`, block by
block. The markdown file becomes an editable surface: blocks you did not touch
keep their **exact base bytes**; changed or new blocks are converted from a
strict markdown subset (headings, paragraphs, lists, task lists, simple
tables, fenced code, blockquotes/admonitions, links, legacy `[[Page Links]]`
(canonicalized to identity-bearing `confluence-page:` links),
`[KEY](jira:KEY)`); opaque elements in edited blocks (macros, mentions,
links, images) keep their original bytes. Local only — `conf push` remains
the write path to the server.

Tables with editor styling or native structure (table/row/cell attributes,
caption/column metadata, header topology, wrapper divs, or spans) are merged
**row/cell-wise** rather than converted: untouched rows keep
their exact bytes; an edited cell has its converted content spliced into the
existing cell wrapper (styles and classes survive); a deleted row drops its
byte range (the fragment-loss gate still applies to macros/mentions it held);
an inserted row clones the byte structure of a neighboring row, so numbering
columns and per-cell styling carry over. Mentions and links copied from an
untouched row into an edited cell are cloned byte-exactly; macros are never
cloned (a copy would duplicate the macro identity).

```bash
atl conf apply guide.md --dry-run              # report without writing
atl conf apply mirror/DOCS/guide/guide.md
atl conf apply guide.md --allow-fragment-loss  # intentional macro/mention removal
```

| flag | description |
|---|---|
| `<page.md>` | the page's markdown view (positional, required) |
| `--dry-run` | report without committing `.csf` or regenerated `.md` view changes |
| `--allow-fragment-loss` | proceed when the edit drops opaque fragments |
| `--into` | mirror root (defaults to nearest `.atl`) |

The first line must be `<!-- atl:document confluence-page v6 -->`. V5 predates
the closed fence/break/table staging contract, while v4 predates
the qualified main-view comment tree, and v3 predates the recorded
display-timezone contract. Apply rejects missing/legacy/unknown
versions and additions, removals, renames, or reordering of reserved
`<!-- atl:... -->` marker text in the editable body before writing. Marker prose
that already came from native page content is allowed when left unchanged.
Pristine v5/v4 views migrate only after exact version-specific reconstruction.
Dirty v5/v4, older historical, and unversioned views are preserved and refused;
an unknown/future version requires a newer `atl` and is never downgraded.

The v6 body renderer chooses a code fence longer than any backtick run in the
native body, reversibly escapes paragraph text that would otherwise parse as a
fence or thematic break, and renders native `<br>` nodes as protected `<br>`
markers. Editing adjacent prose preserves the exact original break bytes.
Deleting protected break/structural markers participates in the explicit
fragment-loss gate; unrepresentable structural rewrites fail before `.csf`
changes. Code-macro metadata that cannot be expressed by a safe Markdown fence
(including unsafe language text, macro ids, or extra parameters) is likewise
loss-gated instead of being silently discarded by a body edit.

All views carry generated document/body boundaries. When the page was pulled
under every profile the body starts at visible `# Content`; `full` also carries
read-only `# Metadata` and `# Comments` sections. Native page headings keep
their original levels; comment headings are nested under their comment entry.
Generated regions are **read-only** in the view:
`apply` reproduces them from the recorded render settings (`.atl/state.json`) and
merges only the editable body between them, so an untouched `full`-profile page
applies to a byte-identical `.csf` — the decorations are never converted into
page content. Editing generated page fields or the `# Comments` section is
refused (exit `8`); use the relevant dedicated metadata/comment command where
available rather than editing the derived view.

Resolved Jira macro tables are generated/read-only too. Editing `# Jira
Queries` is refused; change the native macro in Confluence or select another
`jira_list_views` projection on the next pull. The
`.jira-macros.json` sidecar is bound to the page id and ordered macro
descriptors. Missing or stale enrichment never becomes editable page content:
apply fails closed and names the generated section that changed. A corrupt or
non-empty mismatched sidecar gives a non-looping recovery step: remove only the
generated `.jira-macros.json`, then run `conf pull`. When an explicitly
loss-approved body edit removes the native macro, apply retires the obsolete
sidecar automatically. Post-push refresh rebuilds the same suffix, so an
untouched subsequent apply remains byte-stable.

Output: `{path, csf_path, dry_run, report: {unchanged, moved, converted,
removed, merged_tables?, removed_fragments?, problems?}, csf_ok, wrote,
warning?}`. After a successful apply the `.md` is regenerated from the merged
body so both surfaces agree (keeping the `full` decorations when they were
present); if that refresh cannot be written the apply still succeeds and
`warning` reports that the `.md` may be stale.

Success also records an internal staged-local binding to the exact page id,
relative `.csf` path, native hash, and unchanged remote-base hash. You may edit
the refreshed `.md` and apply again before pushing: the next merge uses those
exact ATL-produced bytes while status/diff/push remain anchored to the original
remote baseline. A direct `.csf` edit, path mismatch, changed base, or corrupt
binding exits `8`; pull and successful post-push refresh clear the binding when
they establish a new remote baseline.

Pass `-o text` for a compact human loss-review — block counts, each removed
fragment, validation problems, and a contextual next-step hint:

```text
dry-run: no files written
blocks: 3 unchanged, 1 moved, 2 converted, 1 removed, 1 table merged
removed fragments:
  - drawio "diagram-1"
validation: ok
next: restore the marker(s) in the .md, or re-run with --allow-fragment-loss to accept the loss
```

The merge is **fail-closed** (exit `8`, nothing written) when: an edited block
cannot be converted faithfully (unsupported markdown, edits inside an
unrecognized wrapper element, an ambiguous mention whose display name collides
with prose); a table edit crosses what the row/cell mapping can carry
(changing a cell that spans rows/columns from a continuation slot, deleting a
row a rowspan passes through, adding/removing columns, editing inside a nested
table, copying a macro-bearing cell) — make that edit in the `.csf` directly
(`conf edit`); the edit drops opaque fragments and `--allow-fragment-loss`
was not given; or the local `.csf` matches neither the last-synced base nor the
exact staged output of the preceding `conf apply` (direct `.csf` edits win —
preserve and push them, or use pull's explicit stash/overwrite policy).
Exit `4`: the page was never pulled (no meta/base). The merged body is always
validated; `conf push --dry-run` remains the final gate before the server.

### `atl conf push`

Validate and push a `.csf` file (or all dirty files in a directory) back to
Confluence under an optimistic version gate.

```bash
# push one file
atl conf push mirror/DOCS/guide/guide.csf

# push all locally-edited files under a directory
atl conf push mirror/DOCS/

# dry run: show what would change without pushing
atl conf push --dry-run mirror/DOCS/guide/guide.csf

# override version conflict (last-write-wins — use with care)
atl conf push --force mirror/DOCS/guide/guide.csf
```

Push output lists each file's outcome and any removed/added fragments so you
can confirm that a macro or diagram was not accidentally deleted from the CSF.
An error-severity CSF problem returns the item and its `problems[]` with
`check_failed` / exit 8 before any write and, on an uncontended local mirror
snapshot, before backend configuration. An active mirror mutation keeps its
existing error precedence. Repair the file and re-run validation rather than
retrying the unchanged push.

Flags:

| flag | description |
|---|---|
| `--dry-run` | validate and diff fragments without pushing |
| `--force` | bypass the version gate (ignores remote drift) |
| `--into` | mirror root, when the file path is outside the default `mirror/` tree |

### `atl conf page resolve`

Resolve a Confluence content id or supported same-origin page URL to one stable
content id:

```bash
atl conf page resolve 12345678
atl conf page resolve 'https://confluence.example.test/spaces/ENG/pages/12345678/Page'
atl conf page resolve 'https://confluence.example.test/pages/viewpage.action?pageId=12345678'
atl conf page resolve '/x/AwAG'
```

Supported URL forms are modern `/spaces/<space>/pages/<id>/...`, exact
`/pages/viewpage.action?pageId=<id>`, REST self links, legacy
`/display/<space>/<title>`, and one `/x/<token>` short-link redirect. Absolute
URLs must exactly match the configured scheme, host/port, and context path;
userinfo, foreign hosts, HTTPS downgrades, duplicate page ids, nested short
links, and unsupported final redirects fail closed. Display links use one exact
CQL lookup and reject zero or ambiguous matches. Numeric/opaque ids and direct
id-bearing URLs need no backend request.

JSON is `{id,kind,via?,network_requests,space?,title?}`; `-o id`/`-o text`
prints the stable id. The same resolver is used by read-only page
get/view/meta/history/open, `pull --id`, `comment list`, `page labels list`,
attachment list/get, and `table extract`. Mutating page selectors remain
explicit ids.

### `atl conf page outline` / `atl conf page section` / `atl conf page sections`

Inspect a long page without placing its entire rendered body in agent context:

```bash
atl conf page outline 12345678
atl conf page outline '/x/AwAG' -o text
atl conf page section 12345678 --heading 'Delivery Notes' -o text
# bind an outline-selected heading to the exact version that outline returned
atl conf page section 12345678 --heading 'Delivery Notes' --occurrence 2 \
  --max-bytes 131072 --expected-version 7
atl conf page sections 12345678 \
  --heading 'Summary' --heading 'Delivery Notes' --heading 'Risks' \
  --occurrence 0 --occurrence 2 --occurrence 0 \
  --max-bytes 262144 --expected-version 7
```

`outline` parses native CSF and walks the same structural block traversal as the
Markdown renderer. Headings inside code/structured macros, tables, and other
opaque blocks are not promoted into page sections. All three commands emit
`schema_version:1` — they are one selection protocol, so no result should be
validated against another shape — and all three reconcile page identity
before parsing any body, failing with exit 8 rather than returning a result
that cannot be attributed to the resolved page and a positive version.
JSON includes ordered
`headings` with `index`, native `level`, normalized hierarchy `path`, and a
1-based occurrence among equal case/whitespace-normalized titles. `count` is
the emitted count, `total` is the parsed count, and `complete`/`truncated`
expose the 1000-heading/262144-byte safety caps; `original_bytes` and
`emitted_bytes` qualify the bounded heading records. A partial outline also
names its limiter in `partial_reason`: `heading_limit` when the 1000-heading cap
stopped emission first, `byte_limit` when the 262144-byte cap did. Both are
terminal — there is no larger outline bound to retry with. `-o text` emits an
indented Markdown list.

`section` selects an exact case/whitespace-normalized heading and renders it,
its body, and descendant headings through the existing link/color/table-aware
renderer, stopping before the next heading of the same or higher rank. Duplicate
titles fail with exit 8 until `--occurrence` is supplied. JSON reports the
selected `heading`, `level`, `path`, `occurrence`, `markdown`, `complete`,
`truncated`, `original_bytes`, `emitted_bytes`, and the always-present
`page_version_gated`. The default output cap is
262144 bytes; `--max-bytes` accepts 1..1048576 and truncates only before a whole
rendered block, adding a visible marker when it fits. A partial section names
its limiter in `partial_reason`: `max_bytes` when a whole rendered block did not
fit, `invalid_utf8` when the rendering was withheld entirely. `-o text` emits
only the selected Markdown. All three commands accept the safe page references
above, are read-only, and
create no mirror artifacts.

`sections` accepts 1..32 repeatable `--heading` selectors. Omit
`--occurrence` entirely when every heading is unique; when any occurrence is
given, supply one non-negative value per heading in the same order (`0` keeps
the unique-heading check). The command resolves every selector against one
fetched and parsed page snapshot before returning anything, then emits entries
in request order. JSON adds `requested_count`, `returned_count`, `reconciled`,
aggregate `complete`/`truncated`, `original_bytes`, `emitted_bytes`,
`max_bytes`, and ordered `sections` with the same per-section fields. The
aggregate bound defaults to 262144 and accepts 1..1048576 bytes. Remaining
capacity is divided among remaining selectors in request order; unused emitted
capacity carries forward, so output is deterministic and total
`emitted_bytes` never exceeds `max_bytes`. `-o text` concatenates only the
selected Markdown in request order. Treat a result as complete evidence only
when counts match, `reconciled:true`, and aggregate `complete:true`.

`--expected-version N` binds a section or multi-section read to a page revision
you already observed. A positive value refuses the read with exit 8 unless the
page is currently at exactly that version, reporting only the expected and current
version integers, and reports `page_version_gated:true` on a match. `0` (the
default) or an omitted flag leaves the read ungated and reports
`page_version_gated:false`; a negative value is a usage error (exit 2). The
check reuses the page response the read already fetched, so it issues no extra
backend request and grants no write capability.

Which section a heading resolves to depends on the revision it is resolved
against, because `occurrence` and `path` are positional. Pass the `version` from
the `page outline` result whenever the heading, path, or occurrence came from
that outline, and the `version` from the first section result when re-reading
the same selection at a wider bound. A selection fixed outside any earlier read
— a heading named in the task itself — has no earlier revision to reconcile, so
it may omit the flag. That ungated result is still exact evidence for the
revision reported in its own `version`; it simply reconciles no earlier
selection, which is what `page_version_gated:false` states.

For an outline, one section, or each section entry in a plural result,
`partial_reason` is a static identifier that carries no page content and is
present exactly when `complete` is `false`. Treat any partial outline or section
as incomplete evidence: do not infer that missing content is
absent and do not settle a decision from it. Only `max_bytes` is recoverable —
re-read the same reference, heading, and occurrence **once** with `--max-bytes`
at or above the reported `original_bytes`, which is the exact minimum bound for
the same valid rendering, and only when that value is within the 1048576-byte
cap. Pass `--expected-version` with the first result's `version` on that
re-read, so a page that moved is refused instead of silently answering from a
body the first result never described, and accept the recovery only when the
second result also has `complete:true`. Otherwise do not retry the same read;
use a narrower heading or the full page instead.
In a plural result the aggregate `original_bytes` is only the sum of full
section sizes; it is not an exact recovery bound for the request-order
allocator. Recover a required `max_bytes` entry once with singular
`page section`, that entry's `original_bytes`, and the plural result's exact
`version`. Do not retry the entire plural request until it happens to fit.

### `atl conf page get`

Fetch a page body directly (without mirroring to disk).

```bash
atl conf page get --id 12345678
atl conf page get --id 12345678 --format view   # rendered HTML view
atl conf page get --id 12345678 -o text         # raw body on stdout
```

Flags:

| flag | description |
|---|---|
| `--id` | page id (required) |
| `--format` | `csf` (default) or `view` (rendered HTML) |

Both formats require the backend to include the requested body projection
(`body.storage.value` or `body.view.value`). An omitted projection exits `8`
instead of appearing as an empty body; an explicitly present empty value is
valid.

### `atl conf page view`

Fetch native CSF and render one page through the same configured Markdown
pipeline as pull/render, without creating a mirror:

```bash
atl conf page view 12345678 -o text
atl conf page view 12345678 --render-profile full
atl conf page view 12345678 --render-root ~/.atl/workspace
atl conf page view 12345678 --jira-view full -o text
```

JSON output contains `id`, `title`, `space`, `version`, and `markdown`; `-o
text` emits only Markdown. The local `--render-root` is read for
presentation-only config and is never created or modified. Binary assets and
view state are not fetched or written. Comments are requested only when the
effective render profile includes `comments`; a capped result produces a
stderr warning.

Jira JQL macros use the same read-only IssueList enrichment as pull. Their
configured columns take precedence; otherwise `--jira-view` selects the named
`confluence_macro` projection. This may make bounded Jira search requests, but
never per-issue reads or Jira writes. Single-key Jira macros remain ordinary
readable Jira links.

The document and its body are explicitly marked read-only because transient
output has no synchronized CSF/baseline. Do not save it into a mirror or feed it
to apply/push. Pull the page fresh before any edit.

Flags:

| flag | description |
|---|---|
| `--render-root` | root whose local render config is used; never written |
| `--render-profile` | `minimal`, `default`, or `full` |
| `--render-include` | comma-separated Confluence sections to add |
| `--render-exclude` | comma-separated Confluence sections to remove |
| `--jira-view` | named Jira list projection for JQL macros (default `default`) |

### `atl conf page meta`

Fetch non-body page metadata (version, ancestors, labels, restrictions).
`restricted` is omitted when the backend omitted restriction state; absence
means unknown, never unrestricted. `-o text` prints identity/version first,
then only present metadata plus an explicit `restricted true|false|unknown`.

```
atl conf page meta --id 12345678
```

### `atl conf page labels list|add|remove`

List the complete content-label collection, or preview a guarded change:

```bash
atl conf page labels list 12345678
atl conf page labels add 12345678 release-ready needs-review
atl conf page labels remove 12345678 obsolete --apply \
  --expected-proposal-hash <hash-from-preview>
```

`list` follows Confluence pagination and emits `complete:false` plus a stderr
warning if the safety cap is reached. Mutation inputs are trimmed,
deduplicated byte-exactly, sorted for a stable review, bounded to 100 unique
names and 255 bytes per name, and rejected on control/invisible characters.
The preview hash binds page id, operation, normalized names, and the complete
current prefix/name set. Apply re-reads that set and requires the exact hash,
including when the goal is already satisfied.

Mutations deliberately manage only `global` labels; personal/team labels remain
visible in `list` and preview but never satisfy or become mutation targets. Add
uses one global-label POST; remove uses one query-parameter DELETE per reviewed
global name so `/` never becomes a path component. Because that endpoint
selects only by name, removal fails closed if the same name also has a
non-global prefix. Writes are never retried.
The command re-lists labels after any write: verified state reports `applied`,
a pre-existing goal reports `already_satisfied`, and an unverifiable or partial
outcome reports `unknown` with a non-zero exit and must not be replayed
automatically. Re-pull the page after a successful change if an existing mirror
must show the new metadata.

### `atl conf page title set`

Preview a page-title update from a bounded file or stdin; no write occurs by
default and the title never needs to appear in argv:

```bash
atl conf page title set 12345678 --from-file title.txt
# review title, hashes, and current_version, then:
atl conf page title set 12345678 --from-file title.txt --apply \
  --expected-version 7 \
  --expected-proposal-hash <hash-from-preview>
```

The preview normalizes surrounding whitespace, rejects empty/multiline/control
text and inputs over 4096 bytes, and returns `current_title`, `title`,
`title_bytes`, `title_sha256`, `current_version`, `expected_version`, and an
aggregate `proposal_hash` binding page id + version + normalized title. Apply
fresh-reads native CSF/title/version, requires both reviewed gates, and sends the
unchanged CSF with the new title in one version-gated PUT. There is no `--force`.

Every successful or ambiguous PUT is verified by another native page read. A
verified exact title/body/version reports `applied`; a pre-existing target title
reports `already_satisfied` only after the reviewed version and proposal-hash
gates pass. Ambiguous outcomes report `unknown`, exit non-zero,
and must be inspected rather than automatically replayed. The command itself
does not rewrite an existing mirror path or sidecar; after `applied`, re-pull
that page before further mirror edits. Re-pull relocates by stable page id only
when the old CSF and recorded Markdown are pristine and the new path is
unoccupied; otherwise it fails closed without deleting descendants.
Retained descendants/assets/comments stay protected by a local ownership marker,
so another page with the old slug is diverted instead of inheriting them.
If all old `.csf`, `.md`, and `.meta.json` primary files were deliberately
removed, re-pull repairs the stale sidecar path. A partial removal remains
ambiguous and exits `8`; restore the complete old page or remove all three
primary files, then re-pull. A supported v5/v4 view receives explicit
`conf render` migration guidance instead of the generic local-edit diagnostic;
only an exact pristine reconstruction can migrate.
If interrupted cleanup leaves an old copy, `conf status` marks it
`non_canonical` and names `canonical_path`; `conf push` refuses the old path
even with `--force`.

Flags:

| flag | description |
|---|---|
| `--from-file FILE|-` | required bounded title input |
| `--apply` | perform the guarded write; default is dry-run |
| `--expected-version` | reviewed current version; required with apply |
| `--expected-proposal-hash` | exact reviewed proposal hash; required with apply |

### `atl conf page history`

List a page's version records, newest first, with explicit completeness. The
JSON is the qualified result
`{schema_version:1,page_id,count,complete,partial_reason?,versions:[...]}`.
`versions` is always a JSON array; a page with no recorded versions emits
`"versions":[]`, never `null`. `complete:true` means the backend version
listing was exhausted, so an empty array is proven absence; `complete:false`
always carries a static `partial_reason` from the closed set `page_limit`,
`item_limit`, `pagination_stalled`, or `legacy_unqualified`, and never proves a
version is missing. Non-positive, duplicate, or non-descending version records
fail as a check error (exit 8) rather than being emitted as qualified evidence.
`-o text` is unchanged: one
`number<TAB>when<TAB>by[<TAB>message]` row per version.

```
atl conf page history --id 12345678
```

### `atl conf page create`

Create a new page. The body is either valid CSF (`--from-file`) or markdown
converted to CSF (`--from-md`) — the two flags are mutually exclusive.

```bash
echo '<p>Hello, <strong>world</strong>.</p>' \
  | atl conf page create --space DOCS --title "Hello" --from-file -

atl conf page create --space DOCS --parent 12345678 \
  --title "Child page" --from-file body.csf

# Author the body in markdown; atl converts it block-by-block:
atl conf page create --space DOCS --title "From markdown" --from-md body.md
```

`--from-md` accepts the same markdown subset as `conf apply` (headings,
paragraphs, emphasis/links, lists and task lists, GFM tables, fenced code,
blockquotes/admonitions, `---`, legacy `[[Page Title]]` page links,
identity-bearing `[label](confluence-page:SPACE/title)` links, `[KEY](jira:KEY)`
issue links). Conversion is fail-closed: the first construct outside the
subset aborts with exit 8 naming the offending block, and the page is **not**
created — write those bodies as CSF via `--from-file` instead. An empty
markdown document is refused the same way. The converted body still passes
the CSF validation gate before the API call. Malformed or over-depth raw CSF is
also a `check_failed` / exit-8 refusal with its `problems[]`; no backend request
is made.

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--title` | page title (required) |
| `--parent` | parent page id |
| `--from-file` | CSF body file or `-` for stdin (default stdin) |
| `--from-md` | markdown body file or `-` for stdin; converted to CSF, fail-closed (exit 8) |

### `atl conf blog create`

Create native Confluence `blogpost` content with a required space, title, and
non-empty body. This is a dedicated command: it cannot be confused with page
creation and does not accept a page parent.

```bash
atl conf blog create --space DOCS --title "Weekly update" --from-md update.md
atl conf blog create --space DOCS --title "Release notes" --from-file release.csf
```

Raw CSF is sent byte-for-byte after validation. `--from-md` uses the same strict
whole-document subset as `conf page create`; unsupported or empty Markdown is
exit 8 before credentials/network. Malformed or over-depth CSF is likewise
`check_failed` / exit 8 with its `problems[]`, while an empty raw CSF body
remains a usage error (exit 2); neither reaches the POST. The flags are mutually
exclusive and `--from-file -` remains the stdin default.

The adapter closes the request to `type:blogpost`, storage representation, and
the selected space; no `ancestors` field is sent. It requests an expanded write
response and requires a non-empty id, exact type/space/title, positive version,
and an explicitly present storage body. JSON output is
`{id,type,title,space,version,body_present,url}`; `-o text` is one compact record
and `-o id` prints the new content id. A successful but unverifiable response is
exit 8 and may mean the post already exists. Transport, timeout, throttling, and
server failures after dispatch are classified the same way; never retry any of
these ambiguous outcomes automatically.

The documented Data Center create-content response does not define a
case-folding, whitespace, or Unicode-normalization equivalence for `title` or
`space.key`. Atl therefore keeps exact comparison after trimming only the
caller input. A differently normalized success response remains `unknown`;
this conservative result is preferable to claiming the wrong created identity.

### `atl conf page move`

Preview a page reparenting operation by default:

```bash
atl conf page move 12345678 --parent 87654321
# review current_parent, current_version, and proposal_hash, then:
atl conf page move 12345678 --parent 87654321 --apply \
  --expected-version 7 \
  --expected-parent 11111111 \
  --expected-proposal-hash <hash-from-preview>
```

Apply fresh-reads both the source page and target parent, refuses self/descendant
cycles, incomplete hierarchy identities, cross-space parents, stale source
version/current-parent gates, and missing native source CSF. It sends the fresh
title/body unchanged in one version-gated PUT. For a source page that currently
has no parent, pass the explicit empty gate `--expected-parent=`.

Every successful or ambiguous PUT is verified by another native source-page
read. A verified exact parent/title/body/version reports `applied`; ambiguous
outcomes report `unknown`, exit non-zero, and must be inspected rather than
automatically replayed. The command itself does not relocate existing mirror
files; after `applied`, re-pull the page before further mirror edits. Re-pull
uses the same id-bound, local-edit-aware relocation path as a title change.
An already-satisfied parent is also a reviewed outcome: apply checks source
version, current parent, and proposal hash before returning it.
The proposal hash also binds the reviewed target version. Apply fetches the
target again immediately before PUT and refuses a changed version, space, or
ancestor identity. This narrows the two-page race; the source page remains the
only object protected by Confluence's write-version gate. The server's cycle
backstop was not write-tested as part of this change; do not treat it as a
client-side guarantee.

Flags:

| flag | description |
|---|---|
| `--parent ID` | proposed parent page id (required) |
| `--apply` | perform the guarded move; default is dry-run |
| `--expected-version` | reviewed current source version; required with apply |
| `--expected-parent` | reviewed current parent; required with apply, empty for top-level |
| `--expected-proposal-hash` | exact reviewed proposal hash; required with apply |

### `atl conf page delete`

Trash a page. May return exit 6 if per-space permissions forbid deletion. The
request uses one transport attempt and refuses redirects; after an ambiguous
result, inspect the exact target instead of retrying the command automatically.

```
atl conf page delete --id 12345678
```

### `atl conf page list`

Flat listing of pages in a space (no hierarchy), optionally by status.

```
atl conf page list --space ENG [--status current|archived|trashed] [--limit 100] [--cursor C]
```

`--space` is required. The output carries a `next_cursor` for pagination; `-o id`
prints the page ids.

### `atl conf page open`

Open a page in the system browser (uses `xdg-open`/`open`/`rundll32`, no shell).

```
atl conf page open --id 12345678
```

### `atl conf page copy`

Client-side copy that preserves the native CSF bytes verbatim (no Markdown
round-trip). Reads the source page and creates a new one with the same body.

```
atl conf page copy --id 12345678 --title 'Copy of Design Doc' [--space ENG] [--parent 999]
```

### `atl conf attachment {list,get,upload,delete}`

Manage page attachments. `delete` requires `--force`, uses one transport
attempt, and refuses redirects. After an ambiguous result, inspect the exact
attachment instead of retrying automatically.

```bash
atl conf attachment list --id 12345678                       # qualified inventory; -o id → ids
atl conf attachment list --id 12345678 --expected-version 7  # refuse unless the page is at v7
atl conf attachment get --id 12345678 --name diagram.png --into ./assets
atl conf attachment upload --id 12345678 --file ./diagram.png [--comment 'v2']
atl conf attachment delete --id <ATTACHMENT-ID> --force
```

`list` emits `{schema_version, page_id, page_version, count, complete,
partial_reason?, attachments:[...]}`. The JSON `attachments` member is always an
array after a successful listing. Pages without attachments emit
`"attachments":[]`, not `null`; `-o id` is empty in that case. Treat an empty
array as proven absence only when `complete:true`; a `complete:false` inventory
carries a static `partial_reason` (`page_limit`, `item_limit`,
`pagination_stalled`, or `legacy_unqualified`) and is a prefix, not the whole
set.

`--expected-version N` binds the listing to a page version you already
observed. A positive value refuses the read with exit `8` when the page has
moved, before any attachment request is made, and reports only the expected and
current version integers; `0` (the default) disables the gate.

Uploads stream the selected file without buffering it and send the exact multipart
`Content-Length`, preserving compatibility with intermediaries that reject chunked uploads.
A caller size fault (a negative size or a multipart body that would overflow the
length) exits `2`; a successful backend response that is malformed JSON or carries
no attachment exits `8`.

### `atl conf me`

Show the authenticated Confluence user.

```
atl conf me
```

### `atl conf comment list`

List a schema-v2, evidence-qualified page comment inventory. The default makes
separate bounded page-scoped reads for footer, inline, and resolved comments at
all thread depths, then joins inline metadata to exact markers in the page's
native CSF. Location, resolution, root/reply relation, and anchor status remain
independent; missing backend evidence becomes `unknown` or a partial reason,
never an inferred value. An explicit backend `reopened` state is the semantic
open state and is emitted as `resolution:"open"`; unrecognized states remain
fail-closed as `unknown`.

```sh
atl conf comment list --id 12345678
atl conf comment list --id 12345678 --location inline --state open --depth all
atl conf comment list --id 12345678 --expected-version 7
```

`--location` is `all|footer|inline|resolved`, `--state` is
`all|open|resolved|unknown`, and `--depth` is `root|all`. A positive
`--expected-version` refuses before comment reads when the page revision has
changed. Regardless of that optional caller gate, every qualified selector and
pagination request is internally bound to the reconciled `page_version`.
Inspect `comments_complete`, `threads_complete`, `anchors_complete`,
`partial_reasons`, and per-comment `relation`, `location`, `resolution`, and
`anchor.status`. Anchors are root-thread evidence; a proven reply has a null
anchor and is qualified by explicit ancestry instead. An empty list proves
absence only when `complete:true`.

`-o text` emits the same qualification header followed by a deterministic
indented thread view. Native `body_storage` remains available only in JSON.
During the mirror-schema migration window, explicit `--legacy-flat` preserves
the old `{comments:[...]}` response; it cannot be combined with v2 filters or
the version gate.

### `atl conf comment thread`

Read the proven root subtree containing one exact numeric comment id:

```sh
atl conf comment thread --id 12345678 --comment-id 87654321
atl conf comment thread --id 12345678 --comment-id 87654321 --expected-version 7
```

If a complete inventory proves the id absent, the command exits 4. If the
inventory is partial and cannot prove absence, it exits 8 instead. A selected
comment whose root is unavailable is returned alone with
`threads_complete:false`; the relationship is not guessed.
Thread diagnostics, partial reasons, and completeness are projected again for
the selected root subtree. Global enumeration/transport failures remain, while
diagnostics and orphan markers proven to belong elsewhere on the page are
excluded.

To persist comments alongside the mirrored page instead of printing them, use
`conf pull --comments`. The schema-v2 `.comments.json` is the source evidence,
including completeness and closed diagnostics. The main v6 `.md` renders a
deterministic read-only thread tree with author/time, location/state, qualified
anchors, and an explicit unattached section when ancestry is unavailable or
inconsistent. Only a matched observed selection is labelled current; other
anchor states may show the original selection only as reported. The separate
`.comments.md` remains a best-effort flat compatibility projection.

### `atl conf comment preview|add`

Safely create one root footer comment from exact native CSF. `preview` is a
read-only command and works under `ATL_READ_ONLY=1`. `add` reconstructs the same
proposal but remains classified as mutating even when its default dry-run sends
no POST. Apply requires both `--apply` and the exact reviewed
`--expected-proposal-hash`:

```bash
ATL_READ_ONLY=1 atl conf comment preview --id 12345678 --from-file comment.csf
atl conf comment add --id 12345678 --from-file comment.csf # dry-run; mutating-classified
atl conf comment add --id 12345678 --from-file comment.csf \
  --apply --expected-proposal-hash <hash-from-preview>
```

This replaces the former immediate-write behavior: existing automation must
review a preview and pass both apply gates; invoking `add` without `--apply`
never writes.

The body must be non-empty valid UTF-8 and valid Confluence Storage Format,
with a maximum size of exactly 1 MiB (1,048,576 bytes). Accepted bytes are
preserved exactly; there is no Markdown conversion. The proposal binds the
backend, resolved page id and version, stable current-user id, exact body and
its length/hash, public-REST capability evidence, and a complete sorted
root-only footer-comment baseline and count. It fails closed if current-user
identity, page metadata, capability, comment completeness, thread completeness,
or unique root identities cannot be proven.

Apply rebuilds the proposal, checks the reviewed hash, revalidates it immediately
before one non-retried POST, then performs complete readback reconciliation.
`applied` means the returned identity matches one exact new record; `recovered`
means one exact new actor/body match proves a commit despite an unusable write
response. `conflict` and `not_applied` prove no accepted write from this attempt.
`outcome_unknown` means the POST may have committed: retain the result, inspect
fresh state, and never replay automatically. An identical existing body never
makes append idempotent.

This public-REST create surface supports footer root comments only. The
separate version-qualified mutation surface below handles existing inline
threads.

| flag | description |
|---|---|
| `--id` | page id or supported same-origin URL (required) |
| `--from-file` | exact native-CSF body file or `-` for stdin (default stdin) |
| `--apply` | `add` only: send one guarded POST (default is dry-run) |
| `--expected-proposal-hash` | `add` only: exact reviewed hash, required with `--apply` |

### `atl conf comment mutation preview|apply`

Create a new inline anchor, reply to an existing open inline thread, resolve it,
or reopen it through an
explicitly activated Data Center compatibility profile. The read-only preview
binds the exact page version, stable actor, complete qualified comment inventory,
private exact product activation, and operation inputs. Existing-thread
operations bind the exact root and resolution. `inline-create` additionally
binds the native page bytes, current server-owned marker inventory, canonical
server-rendered `wiki-content` fingerprint, exact file-backed UTF-8 selection,
zero-based occurrence, derived browser highlight geometry, and native-CSF body:

```bash
atl conf comment mutation preview --id 12345678 --thread-id 87654321 \
  --operation reply --from-file reply.csf
atl conf comment mutation apply --id 12345678 --thread-id 87654321 \
  --operation reply --from-file reply.csf --apply \
  --expected-proposal-hash <hash-from-preview>

atl conf comment mutation preview --id 12345678 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0
atl conf comment mutation apply --id 12345678 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0 --apply \
  --expected-proposal-hash <hash-from-preview>
```

`resolve` and `reopen` omit `--from-file`; `inline-create` omits `--thread-id`.
ATL hashes and binds the selection file bytes exactly, then reproduces the
pinned browser client's search normalization: non-breaking spaces become ASCII
spaces and only that client's edge-whitespace set is trimmed. Line feeds are
removed from the provider's `originalSelection`, while raw DOM text and UTF-16
geometry remain unchanged. The occurrence is zero-based in the normalized
rendered root before native exclusion masks are applied; ATL rejects an
occurrence hidden by those masks or inside a native footer-fallback region,
then reports the resulting provider `match_index`. This fail-closed behavior
also rejects layout-dependent floating table headers and DOM shapes the pinned
highlighter cannot traverse.

ATL reads the same server-rendered page shape used by the pinned client plugin,
but excludes volatile request-time and page chrome from reviewed proposal
evidence. Apply revalidates the entire proposal,
then the provider requalifies the owner-pinned exact product identity immediately
before one fixed write. For create, an immediate second preparation must preserve
the stable DOM/geometry evidence; only its fresh server request-time enters the
single POST. ATL never writes an inline marker into page CSF: Confluence owns the
marker, and complete readback must prove that the native body changed only by one
matching marker wrapper plus one exact root comment. The backend may retain the
page's public content version or advance it by exactly one; ATL accepts only
those two transitions and still requires any successfully decoded provider
response to agree with the observed version. An unusable response can become
`recovered` only when the same strict readback proves the exact result. The
provider never follows a redirect, retries, or falls back to an arbitrary
endpoint. Complete readback must prove the exact new root, reply, or state
transition; retain and inspect any `outcome_unknown` without replay. Resolving an
already resolved thread and reopening an open thread are explicit no-op previews.
The commands are JSON-only and are intentionally absent from MCP.

| flag | operation | description |
|---|---|---|
| `--id` | all | page id or supported same-origin URL (required) |
| `--operation` | all | `inline-create`, `reply`, `resolve`, or `reopen` |
| `--thread-id` | reply/resolve/reopen | exact root inline-thread id |
| `--from-file` | inline-create/reply | exact native-CSF comment body file or `-` |
| `--selection-file` | inline-create | bounded UTF-8 browser selection input or `-`; raw bytes are proposal-bound, search text is pinned-client normalized |
| `--occurrence` | inline-create | zero-based normalized occurrence before native masks (default `0`) |
| `--apply` | apply command | permit its single guarded write attempt |
| `--expected-proposal-hash` | apply command | exact reviewed preview hash |

---

## `atl jira` — Jira

### `atl jira issue get`

Fetch a Jira issue. Default fields: summary, description, status, type,
project, assignee, reporter, labels, links, comments, attachments.

```bash
atl jira issue get PROJ-1
atl jira issue get PROJ-1 --fields summary,status,issuetype,project,labels,description,attachment
atl jira issue get PROJ-1 --fields summary,"Delivery Notes"
atl jira issue get PROJ-1 -o text
```

`--fields` accepts exact technical ids or exact case-insensitive display names.
An ambiguous display name fails closed and lists candidate ids; use an id (or
`id:<id>`) to disambiguate.

### `atl jira issue fields`

Inspect the fields actually carrying evidence on one issue:

```bash
atl jira issue fields PROJ-1
atl jira issue fields PROJ-1 --metadata-only
atl jira issue fields PROJ-1 --field "Delivery Notes" --field Impact
atl jira issue fields PROJ-1 --include-empty
atl jira issue fields PROJ-1 --field assignee --raw
```

The default is deliberately **non-empty + compact**. Each record carries
`id`, human `name`, `custom`, schema type, and a normalized value. User objects
retain only stable username/key/display/active fields; options and named Jira
objects omit email, avatars, `self` URLs, and unrelated transport properties.
Unknown structured objects are represented by their non-empty key names rather
than recursively exposing arbitrary private data. Strings, arrays, and nesting
have explicit caps; a clipped record sets `truncated` and `original_bytes`.

`--metadata-only` is the lowest-token discovery projection. It preserves the
same non-empty/default or `--include-empty` selection, sets `mode:"metadata"`,
and emits only `id`, `name`, `custom`, optional schema, optional `empty`, and a
closed `value_type` (`string`, `number`, `boolean`, `list`, `object`, `null`, or
`unknown`). Its JSON `summary` reports custom, system, and unclassified counts,
non-empty versus missing id counts, uniqueness among non-empty ids, and
deterministic counts by `value_type`. The aggregates are derived from the
returned records without another Jira request; observed fields absent from the
field catalog remain unclassified instead of being guessed as system fields.
The `value` key is absent, not redacted or set to null, so no field content can
leak into JSON or the metadata Markdown table. Use the summary and inventory to
choose one or two exact `--field` selectors, then read those values in compact
mode. `--metadata-only --raw` is rejected before config or network access.

`--include-empty` adds missing/null/empty catalog fields while retaining every
field observed on the issue, including populated plugin/private fields omitted
from Jira's field catalog. `--raw` returns the
unprojected Jira values, may include private contact and transport data, and
writes a warning to stderr. Exact `--field` selectors accept ids or names;
duplicates after resolution are collapsed, while ambiguous names fail before
the issue read.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--field` | exact field id or display name to select (repeatable) |
| `--metadata-only` | emit field metadata and value type without field values |
| `--include-empty` | include empty catalog fields in addition to observed fields |
| `--raw` | emit unprojected values (may contain private contact/transport data) |

### `atl jira issue field get`

Expand exactly one required field without repeating a broad issue or epic
snapshot:

```bash
atl jira issue field get PROJ-1 --field "Delivery Notes"
atl jira issue field get PROJ-1 --field customfield_10002 --max-bytes 32768 -o text
```

`--field` accepts one exact technical id or unambiguous case-insensitive display
name. A technical id goes directly to the issue read; a display name first uses
the field catalog and fails closed on ambiguity. Atl then requests only that
field plus `updated` in one issue GET. JSON reports schema version, issue id/key/update
provenance, field id/name/schema/presence/type, `projection:"compact"`, the
reviewed byte limit, original/emitted encoded value sizes, and
`complete`/`truncated`. The default value cap is 16 KiB; accepted values are
256 bytes through 128 KiB.

The value uses the same closed compact projection as `jira issue fields`:
users omit email/avatar/self data, options and named Jira objects retain only
compact identity, and arbitrary transport objects never expand recursively.
The cap applies to the JSON-encoded compact value, so `emitted_value_bytes`
never exceeds `max_value_bytes`. Use this command when a compact digest names a
required field in `projection.clipped`; do not rerun the whole digest with the
full projection.

### `atl jira issue view`

Fetch one issue and render the same configured Markdown projection used by
`jira pull`/`jira render`, but write nothing to disk. This is the fast path for
one-off agent reading when no offline cache or editing baseline is needed.

```bash
atl jira issue view PROJ-1 -o text
atl jira issue view PROJ-1 --render-profile full
atl jira issue view PROJ-1 --render-root ~/.atl/workspace
```

Default JSON is one object: `{"key":"PROJ-1","markdown":"..."}`. With
`-o text`, stdout is raw Markdown. Render-resolution warnings go to stderr.
The command requests only fields required by the selected profile and typed
field config; configured `epic_children` may perform its bounded related query.
It never creates a mirror, snapshot, sidecar, asset, or writeback baseline.
Because transient reads do not download images, the local Image Attachments
section is omitted; use `jira pull --assets` or `jira issue images` when image
files are needed.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--render-profile` / `--render-include` / `--render-exclude` | override configured presentation for this read |
| `--render-root` | root whose presentation-only `.atl/config.json` is used; defaults to `ATL_MIRROR_ROOT`, the nearest `.atl`, or the current directory; never written |

Do not edit or save transient output as if it were a synchronized mirror. For
writeback, first run a fresh `jira pull`, edit its generated view, then use
`jira apply` and guarded `jira push`.

### `atl jira issue search`

Search issues by JQL.

```bash
atl jira issue search --jql "project=PROJ and status=Open" --limit 20
atl jira issue search --jql "assignee=currentUser()" --cursor 50
atl jira issue search --jql "project=PROJ" --columns key,summary,status,customfield_10001
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--view` | named configured list view (`default` when omitted) |
| `--columns` | ordered metadata, Jira-field, and source-context columns |
| `--limit` | page size from 1 to 1000 (default 50) |
| `--cursor` | pagination cursor (startAt offset) |

JSON uses the common IssueList contract documented below under boards and
sprints. Read rows with `.rows[]`, selected fields with `.values.<field>`, and
resume from `.page.next_cursor`. Requested page sizes through 1000 are passed
to Jira unchanged; the backend may still return fewer rows and an explicit
continuation. `-o text` is a Markdown table in the exact `--columns` order;
`-o id` prints only keys.
JQL search exhaustion is qualified from Jira's paging coordinates. An empty
page with an advertised remainder is `complete:false` with
`partial_reason:"pagination_stalled"` and no cursor, so it never proves that
the query has no matches. `pagination_unqualified` reports inconsistent paging
coordinates. Partial reasons are closed static values and never backend text.

### `atl jira issue children`

Read one page of direct epic children without constructing project-wide JQL or
performing per-child requests:

```bash
atl jira issue children PROJ-100
atl jira issue children PROJ-100 --columns key,summary,status,issuetype,assignee
atl jira issue children PROJ-100 --cursor 50 -o text
```

The command resolves Jira's `Epic Link` field metadata once, then performs one
generated, key-ordered child query. Use `--epic-field parent`, a custom field
id, or its exact display name when auto-detection is not appropriate. JSON uses
the common IssueList contract with `source.kind:"epic"`, the parent and
resolved field under `selection`, and `epic.parent`/`epic.relation` under each
row's namespaced context. Defaults are
`key,summary,status,issuetype,assignee`; `--limit`, `--cursor`, `-o text`, and
`-o id` have the same meaning as `issue search`. `--view NAME` selects the
configured `epic_children` projection; explicit `--columns` wins. This is
read-only.

### `atl jira issue create`

Create an issue. The description is either Jira wiki markup (`--from-file`) or
markdown converted to wiki (`--from-md`) — the two flags are mutually exclusive.

`--from-md` accepts the same markdown subset as the Confluence md surface
(headings, emphasis/links, bullet/numbered lists, GFM tables, fenced code,
blockquotes, `---`, `[KEY](jira:KEY)` issue links, `[~username]` mention
passthrough). Conversion is fail-closed: the first construct outside the
subset (task lists, images, emphasis without word boundaries, pipes inside
table cells, …) aborts with exit 8 naming the offending block, and nothing is
sent — write those bodies as wiki markup via `--from-file` instead.
Wiki-active characters in plain text (`{`, `[`, `!`, toggle chars in opening
position) are backslash-escaped automatically, so ordinary prose survives
verbatim. The same flag exists on `update` and `comment add`.

```bash
atl jira issue create \
  --project PROJ \
  --type Bug \
  --summary "Crash on empty input" \
  --from-file description.wiki

# or author the description in markdown:
atl jira issue create \
  --project PROJ --type Bug \
  --summary "Crash on empty input" \
  --from-md description.md

# with extra fields. A --field value that parses as JSON is sent as that JSON
# type, so structured fields work; a plain string is sent as a string.
atl jira issue create \
  --project PROJ --type Task --summary "Deploy docs" \
  --field 'priority={"name":"High"}' \
  --field 'labels=["docs","infra"]' \
  --field customfield_10001=foo
```

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (required) |
| `--summary` | issue summary (required) |
| `--from-file` | description body file (wiki markup) or `-` for stdin |
| `--from-md` | markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable) |

### `atl jira issue update`

Update summary, description, or arbitrary fields. This replaces the whole
description; for a small targeted change prefer `jira issue edit` below.

```bash
atl jira issue update PROJ-1 --summary "Crash on empty input (critical)"
atl jira issue update PROJ-1 --from-file updated-desc.wiki
atl jira issue update PROJ-1 --from-md updated-desc.md
atl jira issue update PROJ-1 --field 'priority={"name":"Highest"}'
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--summary` | new summary |
| `--from-file` | new description file (wiki markup) or `-` for stdin |
| `--from-md` | new markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable) |

### `atl jira issue field preview` / `field set`

Preview or atomically apply one or more large custom-field values from bounded
files. The value itself never appears in argv. The dedicated `field preview`
command is GET-only, works under `ATL_READ_ONLY=1`, and fresh-reads the selected
fields plus Jira `updated`; its result supplies the `expected_updated` and
aggregate `proposal_hash` required by a later `field set --apply`. `field set`
remains mutating in the command policy even when `--apply` is omitted, so agents
should use the dedicated preview surface until the user approves the write.

```bash
# Markdown is converted fail-closed to a Jira-wiki string
ATL_READ_ONLY=1 atl jira issue field preview PROJ-1 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001

# Re-run the same command with both reviewed gates to write
atl jira issue field set PROJ-1 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001 \
  --expected-updated '2026-01-02T03:04:05.000+0000' \
  --expected-proposal-hash '<proposal_hash>' --apply

# Raw preview: valid JSON objects/arrays stay structured; everything else is an exact string
ATL_READ_ONLY=1 atl jira issue field preview PROJ-1 \
  --from-file customfield_10002=option.json \
  --from-file customfield_10003=plain.txt \
  --allow-fields customfield_10002,customfield_10003
```

Only Jira fields marked custom in field metadata are accepted. Each input must
also be named in the exact `--allow-fields` policy. Use the dedicated commands
for summary, Description, labels, assignee, links, comments, and transitions.
Multiple fields are sent in one PUT. The reviewed timestamp covers the remote
issue state, while one deterministic proposal hash covers every normalized
field value independent of CLI input order and bound to the issue key (proposal
hash schema v2). A changed input file, different issue key, or stale
timestamp emits a `blocked` result and exits 8 without writing.
Already-satisfied values are a no-op after both gates pass. Jira has no
server-side CAS, so a narrow read-to-write TOCTOU window remains.

Raw parsing is deliberately small: only valid JSON whose top level is an object
or array becomes structured. JSON-looking scalars (`true`, `7`, `null`) and
malformed/object-like text stay strings. `--from-md` always produces a string,
even when its rendered Jira wiki happens to look like JSON. Aggregate input and
normalized output are each capped at 64 MiB; stdin (`FIELD=-`) may be used once.

Default JSON includes the aggregate `proposal_hash` plus each normalized
proposed `value`, its `kind`, byte size, and SHA-256. That stdout is the review artifact and may contain private issue
content. `-o text` omits values and prints only field ids, kinds, sizes, and
hashes. Values are never written to verbose request logs.

Flags:

| flag | description |
|---|---|
| `--from-file FIELD=PATH` | raw value file or stdin `-` (repeatable) |
| `--from-md FIELD=PATH` | Markdown file or stdin `-`, converted to a Jira-wiki string (repeatable) |
| `--allow-fields IDS` | exact comma-separated custom field ids authorized for this operation (required) |
| `--expected-updated VALUE` | `field set` only: reviewed Jira `updated` value; required with `--apply` |
| `--expected-proposal-hash HASH` | `field set` only: reviewed aggregate proposal hash; required with `--apply` |
| `--apply` | `field set` only: perform the guarded write; without it `field set` also previews, but remains classified as mutating |

### `atl jira issue edit`

Targeted description edit in one command: fetch the current description,
replace `--old` with `--new` (the same whitespace/invisible-tolerant matcher
as `conf edit`), and write the result back. Small fixes and
insert-after-anchor edits skip the get → compose → update ceremony.

```bash
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'
# insert a section by replacing an anchor heading with new text + the anchor
atl jira issue edit PROJ-1 --old 'h2. Verify' \
  --new $'h2. Rollback\n\nRestore the previous snapshot.\n\nh2. Verify'
atl jira issue edit PROJ-1 --old 'obsolete paragraph' --new ''   # delete
atl jira issue edit PROJ-1 --old 'foo' --new 'bar' --dry-run     # preview only
```

The match must be unique unless `--all` is passed: ambiguous → exit 2, no
match → exit 4 with a quoted region dump showing the closest candidate (and
any hidden bytes that broke exact matching). An empty description is exit 4 —
set one with `issue update`. A whitespace-tolerant match that would cross a
line break is refused with exit 8: Jira wiki is line-sensitive (`h2.`, `*`,
`{code}` are line-start tokens), so a cross-line splice could silently merge
lines — copy `--old` exactly, newlines included. Replacement text is native wiki markup, spliced
verbatim; for a full markdown rewrite use `issue update --from-md` instead.

Jira DC updates are last-writer-wins (no version gate), so the `--old` match
doubles as the drift guard: if the description changed underneath, the needle
misses and the command refuses instead of overwriting.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--old` | text to find in the description (required; must be non-empty) |
| `--new` | replacement wiki text (required; pass `--new ''` to delete the match) |
| `--old-file` / `--new-file` | read either side from a file (`-` for stdin); one trailing newline is stripped |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match and regions without updating the issue |

### `atl jira issue transition`

Preview or apply one reviewed workflow transition. The dedicated `preview`
subcommand is GET-only and works under global read-only policy; the parent
transition command is classified as mutating even during its default dry-run.
Transition names are matched case-insensitively against the live transition
list before target-status matches. A match must be unique.

```bash
ATL_READ_ONLY=1 atl jira issue transition preview PROJ-1 --to "In Progress"
atl jira issue transition PROJ-1 --to Done --comment "Deployed to staging."
# Review proposal_hash, then repeat the exact request once:
atl jira issue transition PROJ-1 --to Done --comment "Deployed to staging." \
  --apply --expected-proposal-hash <hash>
```

The proposal binds the canonical issue identity, current status/update marker,
the uniquely selected exact transition, and exact current/proposed values for
every requested field. An optional comment also binds the exact Jira-wiki bytes,
authenticated actor, and complete comment-id baseline. Apply reconstructs all
relevant state immediately before at most one POST and then reads it back. A
definitive rejection is `not_applied`; unsafe committed or ambiguous outcomes
are `conflict` or `unverifiable` and must never be replayed automatically.
Already matching the target status is not idempotency because transitions may
have workflow side effects.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | target status or transition name (required) |
| `--comment` | optional non-empty Jira-wiki comment to post with the transition |
| `--field key=value` | field to set on the transition (repeatable), e.g. `resolution={"name":"Fixed"}` |
| `--apply` | perform the exact reviewed transition (default: dry-run) |
| `--expected-proposal-hash` | exact hash emitted by the matching preview/dry-run (required with `--apply`) |

### `atl jira issue assign`

Set or clear the issue assignee via the dedicated assignee endpoint. Exactly one
of `--to`, `--me`, `--none` is required (else exit 2). `--me` resolves the
authenticated user's DC username first.

```bash
atl jira issue assign PROJ-1 --me            # take the ticket
atl jira issue assign PROJ-1 --to jdoe       # hand it to a DC username
atl jira issue assign PROJ-1 --none          # unassign
```

→ `{ "key": "PROJ-1", "status": "assigned", "assignee": "jdoe" }` (`"unassigned"`
and an empty `assignee` with `--none`).

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | DC username to assign the issue to |
| `--me` | assign to the authenticated user |
| `--none` | remove the assignee |

Find usernames with `atl jira user search '<name>'`; `--field assignee=<name>`
on `update` does **not** work (Jira DC expects an object there — use `assign`,
or `--field 'assignee={"name":"jdoe"}'`).

### `atl jira issue comment {preview,add,list,delete}`

Read or safely append Jira wiki comments. `preview` is a GET-only command that
works under the process-wide read-only policy. `add` uses the same proposal but
is dry-run by default; it writes only with both `--apply` and the exact reviewed
`--expected-proposal-hash`.

```bash
ATL_READ_ONLY=1 atl jira issue comment preview PROJ-1 --from-md note.md
atl jira issue comment add PROJ-1 --from-md note.md  # same dry-run, classified as mutating
atl jira issue comment add PROJ-1 --from-md note.md --apply \
  --expected-proposal-hash <hash-from-preview>
atl jira issue comment list PROJ-1                 # {key, comments:[{id,author,created,body}]}; -o id → ids
atl jira issue comment delete PROJ-1 <COMMENT-ID>  # see the id from `comment list`
```

Comment listing fails closed (exit 8) whenever a complete, stable listing
cannot be proven: for example, after the defensive page guard, a changed total,
an unexpected offset, inconsistent metadata, or a no-progress page. No partial
list is emitted or used for a proposal baseline. Preview validates unique,
non-empty comment ids and binds their sorted set, the exact normalized native
body, target, and authenticated Data Center identity into the proposal hash.
An existing identical body does not make append idempotent.

Apply reconstructs the proposal and re-reads the complete baseline immediately
before one non-retried POST. A changed body, identity, or baseline blocks before
the write. Successful or transport-ambiguous outcomes get one complete
readback. The closed statuses are `would_apply`, `applied`, `not_applied`,
`conflict`, and `unverifiable`; the latter two are non-zero where replay safety
is not proven. Never automatically replay an ambiguous comment POST. Default
JSON includes the reviewed body and may therefore be private; `-o text` emits
only hashes and byte counts.

Flags (`preview` and `add`):

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--from-file` | comment body file (wiki markup) or `-` for stdin (default stdin) |
| `--from-md` | markdown comment file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--apply` | `add` only: send one guarded POST (default is dry-run) |
| `--expected-proposal-hash` | `add` only: exact reviewed hash, required with `--apply` |

### `atl jira issue link {add,list,delete,suggest}`

Manage typed links between issues. `link` is a subcommand group.

```bash
atl jira issue link add PROJ-1 --to PROJ-2 --type blocks
atl jira issue link add PROJ-3 --to PROJ-1 --type "is cloned by"
atl jira issue link list PROJ-1                    # {key, links:[{id,direction,type,type_name,key}]}; -o id → link ids
atl jira issue link delete <LINK-ID>               # see the id from `link list`
atl jira issue link suggest --csv links.csv         # dry-run missing-link candidates only
```

Flags (`add`):

| flag | description |
|---|---|
| `PROJ-1` | source issue key (positional, required) |
| `--to` | target issue key (required) |
| `--type` | link type name (required; see `atl jira link-types`) |

`suggest` is read-only. It expects a reviewed CSV plan with `source`, `target`,
`type`, and optional `rationale` columns. Common aliases such as `from`, `to`,
`link_type`, and `reason` are accepted. For each source issue, it reads current
outward Jira links and emits only plan rows that are still missing:

```csv
source,target,type,rationale
PROJ-1,PROJ-2,Blocks,dependency found during review
```

### `atl jira issue link-epic`

Set the Epic Link custom field on an issue (classic Jira Data Center boards).

```bash
atl jira issue link-epic PROJ-42 --epic PROJ-1
```

Flags:

| flag | description |
|---|---|
| `PROJ-42` | child issue key (positional, required) |
| `--epic` | epic issue key (required) |

### `atl jira issue plan apply`

Preview or apply a guarded CSV operation plan. The default mode is **dry-run**:
the command re-reads current Jira state, reports `would_apply`,
`already_satisfied`, `blocked`, `failed`, or fail-fast `skipped` rows, and
performs no writes. A blocked/failed plan still emits its JSON audit result but
exits 8. Write mode requires both `--apply` and `--confirm APPLY`.

```bash
atl jira issue plan apply --csv plan.csv
atl jira issue plan apply --csv plan.csv --allow-ops link,label_add --apply --confirm APPLY
atl jira issue plan apply --csv plan.csv --continue-on-error # still exits 8 if any row fails
```

CSV columns:

| column | description |
|---|---|
| `version` | required plan schema version; currently `1` |
| `op` | `link`, `label_add`, `label_remove`, `comment`, or `field` |
| `source` | issue key to read/write |
| `target` | target issue key for `link` |
| `type` | Jira link type for `link` |
| `field` | field id/name for `field` |
| `value` | label(s), comment body, or field value |
| `rationale` | optional audit note |
| `expected_updated` | required Jira `updated` value captured during review; a mismatch blocks the row |

Flags:

| flag | description |
|---|---|
| `--csv` | operation plan CSV (required) |
| `--allow-ops` | comma-separated allowed operations (default `link`) |
| `--allow-fields` | comma-separated field ids/names allowed for `field` operations |
| `--allow-link-types` | explicit link-type exceptions when a type is absent from live Jira metadata |
| `--continue-on-error` | continue independent rows after failures; final exit remains 8 |
| `--apply` | perform writes instead of dry-run |
| `--confirm` | must be exactly `APPLY` when `--apply` is set |

The complete plan schema and live link-type metadata are validated before
writes. Execution is fail-fast by default; remaining rows are reported as
`skipped`. Every non-noop row re-reads the source issue and compares
`expected_updated` immediately before its write. Schema version 1 permits only
one mutating row per source issue; split dependent changes into separately
reviewed plans. Structured `field` values use semantic JSON comparison: object
key order and server-added object properties do not cause repeat updates, while
arrays retain reviewed order. Invalid JSON-looking text remains a string, as in
ordinary `--field` handling.

### `atl jira issue attachment {list,get,upload}`

List, download, or upload issue attachments. `get` accepts either the attachment
id or the filename in `--id`; server-provided filenames are reduced to a safe
basename before writing to the target directory.

```bash
atl jira issue attachment list PROJ-1                    # {key, attachments:[...]}; -o id → ids
atl jira issue attachment get PROJ-1 --id 42 --into ./attachments
atl jira issue attachment get PROJ-1 --id spec.xlsx
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--id` | attachment id or filename (`get`, required) |
| `--into` | output directory (`get`, default `.`) |
| `--file` | local file path (`upload`, required) |

An `upload` whose successful backend response is malformed JSON or carries no
attachment exits `8`.

### `atl jira issue images`

Download image attachments of an issue to files (useful for agent vision).

```bash
atl jira issue images PROJ-1
atl jira issue images PROJ-1 --into /tmp/proj1-images
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--into` | output directory (default `mirror-jira/<KEY>.assets/`) |

### `atl jira issue history`

Show an issue's changelog (who changed what, when) with explicit completeness
and source metadata. `atl` prefers the paginated Data Center changelog endpoint;
older instances fall back to `?expand=changelog`. An embedded result is marked
complete only when Jira returns paging metadata proving that every advertised
entry is present.

```bash
atl jira issue history PROJ-1
atl jira issue history PROJ-1 --field "Delivery Notes" --since 2026-04-01
atl jira issue history PROJ-1 --field status --until 2026-06-30T23:59:59Z
atl jira issue history PROJ-1 --summary-only
```

Repeatable `--field` accepts an exact id or case-insensitive display name and
fails closed on ambiguous names. `--since` and `--until` accept `YYYY-MM-DD`,
RFC3339, or Jira datetime values. Date boundaries are inclusive: an `--until`
date includes that entire calendar day in the observed Jira current-user IANA
timezone. Atl reads that preference once per top-level command, fails closed if
it is missing/invalid, and reports `boundary_time_zone`, its source, plus
canonical `since_instant` / `until_exclusive_instant`. Midnight gaps and folds
are resolved from the first and last real instants belonging to that civil
date, rather than normalizing a nonexistent `00:00`; this prevents an inclusive
end date from omitting evidence. A fully skipped requested date is exit 8. This
resolution is local and does not add another metadata or search request. An
explicit-offset RFC3339 boundary is already absolute, performs no timezone metadata GET, and
leaves the boundary-zone fields absent. Jira's compatible changelog APIs do not
provide these filters, so `atl` first reads the qualified snapshot and then
filters locally; `fetched` and `total` describe the pre-filter read, while
`count` describes matching history entries. Raw JQL is never rewritten.

JSON includes `complete`, `source`, optional `partial_reason`, field ids beside
display names, and `last_changes` for selected fields within the requested time
window. The additive `summary` object computes deterministic entry/item totals,
non-empty metadata counts, distinct and per-field buckets, status changes, id
uniqueness, separate missing-id and non-empty-id uniqueness facts, count/fetch
reconciliation, and chronological ordering from that
same filtered array without another request. If any timestamp is not
comparable, `chronological_comparable` is false and
`chronological_ascending` is `null`. `fetched_matches_total:true` does not
replace the top-level completeness qualification. Treat `complete:false` as
incomplete evidence rather than absence of a change. If a matching selected
change has an unsupported server timestamp, atl returns exit 8 because it
cannot order `last_changes` safely. `-o text` renders a completeness line
followed by an escaped Markdown table.

`--summary-only` performs the same qualified read and filtering but omits the
raw top-level `history` member. JSON keeps `key`, provenance/completeness,
source totals, `filters`, `summary`, and selected-field `last_changes`; it does
not add another backend request. Its text form contains bounded deterministic
facts, field buckets, and any selected-field last changes rather than raw
changelog rows. Without this flag, the JSON and text contracts are unchanged.
An explicit false form such as `--summary-only=false` is a usage error before
backend access. Omit the flag when raw history is intentionally required.

### `atl jira epic digest`

Aggregate the dated evidence commonly needed for an epic/quarter analysis
without generating subjective management prose:

For the decision flow around unfamiliar issues, several keys, and linked
Confluence evidence, see the evidence-first recipe in
[agent-recipes.md](agent-recipes.md#analyze-jira-evidence-without-manual-joins).

```bash
atl jira epic digest PROJ-1 --quarter 2026-Q2 \
  --status-field 'Delivery Notes' --dod-field 'Definition of Done' \
  --projection compact
atl jira epic digest PROJ-1 --since 2026-04-01 --until 2026-06-30 \
  --epic-field customfield_10001 --include identity,children,comments,history,refs
atl jira epic digest PROJ-1 --quarter 2026-Q2 --status-field customfield_10002 \
  --expand-confluence 2 --confluence-heading 'Metrics'
```

`--quarter YYYY-Q1..Q4` maps to inclusive calendar dates in the observed Jira
current-user timezone and conflicts with explicit `--since/--until`, which must
be supplied together. The resolved period includes the IANA zone/source and
canonical UTC instants. Digest reuses that single lookup for nested history;
it does not fetch the preference twice. Explicit-offset RFC3339 bounds need no
lookup. The default include
set is `identity,status-field,children,comments,links,history,refs`; repeat
`--include` or pass a comma list to narrow it. Identity is always present.
Status/DoD/Epic Link selectors accept exact ids or display names and never guess
a company-specific narrative field.

The schema-v1 JSON contains `period`, sorted `includes`, a `sources` map,
`epic`, optional `status_field`/`dod_field`, a common IssueList under `children`
plus `by_status` and dated updates, bounded newest comments/history, links and
blockers, artifact refs, optional Confluence sections, and `staleness`.
History and comments are filtered to the selected period; current child rows
remain visible while `updated_in_period` and staleness apply the period boundary.
Staleness is explainable evidence: the selected status-field change time,
latest newer evidence time, newer child/comment counts, and textual reasons —
not an opaque score or generated conclusion.

`--projection compact` keeps the same qualified evidence read and returns an
app-layer bounded projection for agent synthesis. It preserves `sources`,
counts, count/text truncation, warnings, staleness, status/DoD evidence and
small deterministic samples/summaries, while omitting raw child rows and raw
collections. `projection.omitted` names removed paths and
`projection.clipped` names values clipped by the tighter projection budget.
Expand a required clipped narrative with `jira issue field get`; do not repeat
the broad digest in `full`. Use another source-specific bounded command when a
different omitted collection detail is actually required. Compact JSON is bounded to 64 KiB by regression fixtures at
the command's existing source caps; it does not turn incomplete evidence into
complete evidence.

Every component declares `complete`, `count`, optional machine-readable
`count_truncated`/`text_truncated`, and a bounded warning.
Defaults/caps are 1000 children, 50 comments, 500 history entries, 128 KiB per
large text value, and 10 Confluence expansions. A source failure remains visible
and does not become proof of absence. `refs.complete` includes the completeness
of every contributing description, selected status/DoD field, and comment
source. Links have a canonical total order. Confluence expansion additionally
requires an exact heading; it scans at most 50 refs, accepts only the safe same-origin
references supported by `conf page resolve`, and reuses bounded `page section`.
`-o text` is a compact evidence overview, not a management summary.

### `atl jira issue labels`

Add and/or remove labels without clobbering labels set by others (uses the
field-update verb).

```bash
atl jira issue labels PROJ-1 --add bug,backend [--remove wontfix]
```

Flags: `--add` / `--remove` (comma-separated; at least one required, else exit 2).

### `atl jira issue watchers list|add|remove`

Read complete Jira Data Center watcher membership or preview one guarded
membership change:

```bash
atl jira issue watchers list PROJ-1
atl jira issue watchers add PROJ-1 --username alice
atl jira issue watchers remove PROJ-1 --me --apply \
  --expected-proposal-hash <hash-from-preview>
```

Choose exactly one of `--username` and `--me`. The latter resolves
`/rest/api/2/myself` and requires a non-empty Data Center `name`; it never
substitutes a Cloud account id. Usernames are trimmed, bounded to 255 bytes,
and rejected on control/invisible characters. The preview hash binds issue key,
operation, resolved username, and the complete sorted watcher membership. Apply
re-reads membership and requires the same hash even when already satisfied.

The Jira watcher endpoint is not paginated. `watchCount` must equal the number
of returned named identities; otherwise list emits `complete:false`,
`truncated:true`, and a stderr warning, while mutation fails closed. POST uses
Jira DC's raw JSON-string body and DELETE uses an encoded `username` query.
Each write is sent once and followed by a verification GET. Verified state is
`applied`; ambiguous/partial state is `unknown` with a non-zero exit and must
not be replayed automatically.

### `atl jira issue worklog list|add`

Read the complete Jira Data Center worklog history or add one reviewed time
entry without changing the remaining estimate:

```bash
atl jira issue worklog list PROJ-1 -o text
atl jira issue worklog add PROJ-1 --time 1h30m \
  --started 2026-07-13T09:00:00Z --from-file worklog.txt
atl jira issue worklog add PROJ-1 --time 1h30m \
  --started 2026-07-13T09:00:00Z --from-file worklog.txt --apply \
  --expected-proposal-hash <hash-from-preview>
```

`list` consumes every page advertised by Jira and fails with exit 8 on missing,
changing, or structurally inconsistent pagination; it never returns an
unmarked prefix. JSON authors are a compact projection (`name`, `key`, display
name, active) without email, avatar, self URL, or timezone. `-o text` is an
escaped Markdown table; `-o id` prints worklog ids.

`add` accepts positive integer `h`, `m`, and `s` segments (`1h30m`, `90m`,
`45s`). Days and weeks are rejected because their conversion depends on Jira
instance settings. `--started`, when present, must be RFC3339 with an explicit
timezone. The optional comment comes from either `--comment` or a bounded
`--from-file FILE|-`; prefer the file form because inline text is visible in
the process list.

The default is a read-only preview that normalizes the duration and start time,
shows the compact current identity and payload, and binds them with a proposal
hash. The result also exposes `baseline_sha256`, a value-free digest of the
complete sorted worklog-id set. Proposal schema v2 binds that digest, so any
intervening add (including a committed write behind an ambiguous response)
changes the reviewed hash and blocks before POST. `--apply` requires the exact
proposal hash, re-reads a complete baseline, and
sends one POST with `adjustEstimate=leave`. A timeout/transport/5xx result is
never retried: atl performs one complete reconciliation read and reports
`applied` only when an explicit `--started` value and exactly one new matching
entry prove the result. Without that timestamp, an ambiguous response remains
`unknown` even if a similar entry appears, because Jira chose its start time.
Every `unknown` exit is non-zero and agents must not replay it automatically.

### `atl jira issue check`

Audit that required/important fields are populated — a CI / pre-transition gate.
Exits **8** (`ErrCheckFailed`) when a `--require` field is empty (distinct from a
transport/auth error), after emitting the report on stdout.

```bash
atl jira issue check PROJ-1 --require assignee,fixVersions [--warn priority]
```

`--warn` defaults to `assignee,priority,components,fixVersions,description`; pass
`--warn ""` to opt out of warnings. A check that would audit nothing (no
`--require` and `--warn ""`) is a usage error (exit 2).

### `atl jira issue graph`

Build one deterministic bounded work-artifact graph from an exact Jira issue:

```bash
export ATL_READ_ONLY=1
atl jira issue graph PROJ-1
atl jira issue graph PROJ-1 --depth 2 --strict
atl jira issue graph PROJ-1 --resolve confluence
atl jira issue graph PROJ-1 --include-development
atl jira issue graph PROJ-1 -o text
```

For a typed transient read, `jira_issue_graph` returns the same schema-v2 graph
without a shell command. MCP v1 is deliberately Jira-only: it has no
Confluence resolution input, always leaves discovered page identities as
qualified stubs, and has no `strict` option. Supply `key`, optional `depth` from
0 through 2, and optional `max_nodes`, `max_edges`, `max_requests`,
`include_development`, and `max_bytes`. Nodes default to 50 and cap at 100;
edges default to 200 and cap at 500; physical requests default to 50 and cap at
100. Evidence is fixed at 500 records and the aggregate buffered Jira response
budget is fixed at 16777216 bytes; both appear in `bounds`, including
`max_response_bytes`, but are not v1 inputs. The separate `max_bytes` input
limits the final encoded MCP result. A valid graph with `complete:false` remains
structured evidence to inspect, while an encoded-result overflow fails the
entire tool call rather than returning a clipped graph. Omitting
`include_development` or supplying false preserves the stable request and output
profile; the resulting absence of Development evidence is not proof of zero
development work.

CLI `--include-development` and typed MCP `include_development:true` explicitly
opt into Jira's experimental Development surface. For every successfully
expanded Jira issue, atl reads one summary and zero to 24 non-zero detail
selectors after the stable collectors.
It emits only normalized lowercase GitLab hosts and project paths, full
40/64-hex commit SHAs, exact bounded branch names, and positive merge-request
IIDs with normalized `open|merged|closed|unknown` state. Non-GitLab provider
selectors are rejected.
Messages, people,
emails, files, timestamps, plugin envelopes, and backend error text are never
projected. ATL does not contact GitLab, clone a repository, fetch an artifact
URL, or reuse Jira credentials for GitLab. GitLab nodes remain unexpanded
`experimental_api` stubs and never enter graph traversal.

MCP additionally omits Development-node URLs and returns only the validated
lowercase host, project path, applicable tagged selector/state, ordinary graph
topology, and experimental provenance. Treat these coordinates as untrusted evidence.
Before any later lookup, require exact equality with an owner-approved lowercase
host and use a separately authenticated read-only downstream client for that
exact host. A mismatch must stop the lookup; do not normalize, suffix-match, or
reuse the Jira PAT.

The requested `development` source is `complete` only after every non-zero
repository/branch/pullrequest selector and identity reconciles. Any malformed,
unsafe, conflicting, unsupported, forbidden, failed, or bounded response makes
that source incomplete and emits no Development nodes or edges for that Jira
issue. Its `count` is commits + branches + merge requests; supporting project
containers are excluded. With `--strict`, the graph is still emitted before
exit 8. Omitting the flag preserves the prior request sequence and output bytes.

The command always emits schema v2. With the default `--depth 0` and
`--resolve none`, it expands only the seed. Jira issues, Confluence page
identities, attachments, and safe URL targets discovered from the seed remain
depth-1 stubs; atl does not fetch a linked issue, resolve a page, download an
attachment, or dereference an external URL. Omitting both flags, supplying
`--depth 0`, supplying `--resolve none`, or supplying both explicitly produces
the same output contract.

`--depth 1..3` enables traversal, while `--resolve confluence` adds the narrow
metadata resolution phase. Traversal is a globally canonical breadth-first
walk and follows only exact `jira_issue` stubs discovered through structured
Jira links or hierarchy.
Heuristic narrative mentions are never followed. Cycles, diamonds, and moved
keys are reconciled deterministically. Schema v2 records each source against
its expanded node, adds `source_node_id` to evidence, and reports attempted,
followed, expanded, transport, response-byte, and bounded-frontier accounting.

Schema-v2 defaults and hard maxima are:

| Limit | Default | Maximum |
|---|---:|---:|
| `--max-nodes` | 100 | 2048 |
| `--max-edges` | 500 | 4096 |
| `--max-evidence` | 500 | 4096 |
| `--max-requests` | 100 | 128 |
| `--max-bytes` | 16777216 | 67108864 |

`--max-requests` counts actual physical HTTP attempts. Graph reads are
single-attempt, so retries and redirect following are disabled; a redirect
response still consumes its one attempt. `--max-bytes` is the aggregate number
of buffered backend response bytes, including buffered error responses, not
the size of graph output. Refused work is exposed through the closed
`request_limit`, `byte_limit`, or `output_limit` reasons and a bounded sorted
`frontier`; no bound is silently exceeded. If the seed response itself exceeds
the byte budget, schema v2 emits a minimal unresolved root, qualifies every
requested source with `byte_limit`, and records the root in the frontier
instead of discarding the budget evidence.

`--resolve confluence` runs after Jira traversal and only for already discovered
canonical numeric page ids. It uses the independently configured Confluence
origin and host-scoped PAT, performs at most one single-attempt GET per
candidate, requests no expansions, and retains only exact id/title metadata.
It never reads page bodies, ancestors, labels, restrictions, users, assets, or
foreign URLs. Missing configuration is a static
`dependency_unavailable` skipped source rather than a leaked setup detail.

`--strict` still emits the graph, then exits **8** (`ErrCheckFailed`) when any
requested source is incomplete. Without `--strict`, a usable reconciled graph
may exit 0 with `complete:false`; consumers must inspect qualification.
`-o id` is unsupported because a graph has no single primary identifier class,
and invalid flags are rejected before configuration, credentials, or network.

The root uses one single-attempt issue request with all returned applicable
fields, field names/schema, and issue properties. Before recursive inspection,
returned fields are reconciled against schema metadata. A recursively eligible
field with missing, blank, unknown, or structurally invalid type/item metadata
is not inspected and makes `issue_fields` partial with `malformed_response`.
Structured and privacy-excluded fields do not require walker metadata. A custom
narrative field missing its necessary name metadata is inspected conservatively
without bare Jira-key inference and receives the same qualification. An unknown
noncanonical field id is also partial and cannot enable bare inference, though a
valid non-identity schema may still permit URL-only inspection. Extra names or
schemas for fields not returned are ignored. Jira's literal top-level
`type:any` is accepted only for a canonical custom field and remains
path-filtered, URL-only, and ineligible for bare Jira-key inference. Nulls,
scalar numbers/booleans, and empty strings or containers cannot contain graph
references and require no walker metadata.
Collectors then add
typed issue links, parent/subtask/epic relations, attachment identities,
complete paginated comments and worklogs, and supported remote links. A bounded
path-aware walker extracts Jira keys, explicit Confluence page ids, and
absolute HTTP(S) URLs while excluding user, avatar/icon, transport, and
attachment-download subtrees. Graph output never emits URL query values,
userinfo, or fragments. Sensitive or credential-like path segments make the
URL an opaque identity without a raw URL. Dynamic property and nested-object
tokens in evidence pointers are deterministic opaque tokens, and no discovered
URL is requested.

`sources` is authoritative for absence claims. Each requested source is
`complete`, `empty`, `partial`, `forbidden`, `unsupported`, or `skipped`, with
static content-free reasons. Malformed, request-failed, inspection-limited, and
output-limited sources remain visibly incomplete. `empty` proves absence only
for that named source. Source stability is fixed per kind: `issue_properties`
is `experimental_api`; every other current source kind is `public_api`.
`issue_properties` remains ordered; its count is the returned property count,
while completeness means that set was processed under the fixed privacy
exclusions and bounds. Auxiliary failures keep the
usable stable graph and exit 0 with `complete:false`; an unusable seed snapshot,
invalid graph invariant, or failed reconciliation exits non-zero. The top-level
summary proves that node, edge, evidence, source, status-bucket,
incomplete-source, expanded-node, and completeness counts match the final
arrays.

Edges distinguish structured relations (`jira_link`, hierarchy,
`attached`, `remote_link`) from heuristic `mentions`. The same target may
therefore have both a strong typed edge and a separate mention edge. Every edge
has content-minimized evidence naming its collector and JSON pointer but never
copies a source snippet. Text output preserves per-node source qualification,
transport accounting, and any bounded frontier, then renders escaped node/edge
tables. JSON remains the canonical contract.

This command is additive. `jira issue refs` retains its existing URL-focused
schema, flags, output bytes, and JQL behavior.

### `atl jira issue refs`

Extract artifact references from one issue or from a JQL selection. This reuses
the same deterministic classifier as `jira planning report`: links are classified
as `doc`, `design`, `jira`, `chat`, or generic `link`. The result qualifies the
selection and every contributing description, custom field, and comment source.

```bash
atl jira issue refs PROJ-1
atl jira issue refs PROJ-1 --fields 'Delivery Notes,Design URL'
atl jira issue refs --jql "project=PROJ" --limit 100
atl jira issue refs --jql "project=PROJ" -o text
```

Pass exactly one of positional `KEY` or `--jql` (else exit 2). `--fields` accepts
technical ids or exact case-insensitive display names and adds those fields to
reference extraction; unknown names exit 4 and ambiguous names exit 8 before
the issue read. Output source identities use the resolved technical ids.
Description and comments are always included.

For JQL, `selection.complete:false` and `selection.truncated:true` mean
`--limit` stopped before backend exhaustion. Every issue exposes `complete`,
optional `truncated`, `sources`, and bounded warnings. Comments are fetched from
their complete paginated endpoint; a recoverable failure may retain embedded
partial comments but must mark the issue incomplete. Description, each selected
field, and each comment body are capped at 128 KiB per value and expose
`text_truncated` when clipped. Treat empty refs as evidence of absence only when
the top-level and per-issue `complete` values are true. `-o text` renders the
same qualification followed by an escaped Markdown table.

Use each issue's `reference_summary` and the top-level `summary` for
deterministic reference totals, per-kind counts, source-value cardinalities,
and complete/incomplete/truncated provenance counts. The explicit reconciliation
booleans prove that those aggregates match the emitted arrays, selection, and
top-level qualification. A duplicate URL found in several sources is counted
once within that issue; the same URL on different issues is counted once per
issue. Do not manually sum `refs` when an aggregate answers the question.

Complete comment qualification costs one paginated comment listing per selected
issue in addition to the issue selection. Keep JQL narrow and use an explicit
`--limit`; atl intentionally does not trade this completeness proof for hidden
parallelism or embedded-comment prefixes.

### `atl jira issue tree`

Build a read-only epic-to-child tree from a JQL selection using a configurable
epic field. Children whose parent epic is not included in the JQL result are
grouped under `external_epics`; selected non-epic issues without an epic are
listed under `orphans`.

```bash
atl jira issue tree --jql "project=PROJ" --epic-field customfield_10001
atl jira issue tree --jql "project=PROJ" --epic-field customfield_10001 -o text
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query selecting issues (required) |
| `--epic-field` | field id/name containing parent epic key (required) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to fetch |

### `atl jira issue delete`

Permanently delete an issue. Jira Data Center has **no trash** for issues, so this
is irreversible and requires `--force`.

```bash
atl jira issue delete PROJ-1 --force [--delete-subtasks]
```

### `atl jira pull`

Export issues matching a JQL query to disk. Each issue becomes three files:
`<KEY>.wiki` (the native Jira wiki body, stored byte-for-byte — the editable
source of truth, the Jira analog of a Confluence `.csf`), `<KEY>.md` (a
derived Markdown staging view rendered from the wiki, regenerated best-effort on every
pull), and `<KEY>.json` (identity plus raw Jira fields). Edit the generated `# Description`
and any explicitly editable rich-text field sections in the `.md` view, then merge/stage
them with `jira apply` (the recommended cycle, below), or
edit the `.wiki` directly for what the md view can't express — a bare `.md` edit
never reaches the server on its own.

```bash
atl jira pull --jql "project=PROJ and sprint in openSprints()" \
  --into my-jira-mirror --limit 200
atl jira pull --jql "project=PROJ" --fields customfield_10001,customfield_10002
# also mirror each issue's image attachments and link them from the .md
atl jira pull --jql "project=PROJ and status=Open" --assets
# inspect what a refresh would do without changing the mirror
atl jira pull --jql "project=PROJ" --into my-jira-mirror --dry-run
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--into` | output root directory (default `mirror-jira`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to include in JSON snapshots; core fields needed for rendering are always included |
| `--assets` | also download each issue's image attachments into a per-issue `<KEY>.assets/` directory and link them from the `.md` (opt-in; off by default) |
| `--dry-run` | select and qualify issues without writing mirror files, state, or stashes |
| `--overwrite-local` | explicitly replace a qualified locally edited native `.wiki`; never bypasses derived-view or baseline-integrity failures |
| `--stash-local` | preserve qualified local `.wiki` bytes in immutable `.atl/stash/` storage before replacement; mutually exclusive with `--overwrite-local` |
| `--render-profile` | `.md` view profile: `minimal` \| `default` \| `full` (see [Render profiles](#render-profiles)) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |

Under `full`, `pull` widens its API `fields=` projection to cover the active
profile's sections, so no extra per-issue fetch is needed. The pull result JSON is
unchanged by the profile.

With `--assets`, image attachments (media type `image/*`) are streamed into
`<KEY>.assets/<attachment-id>-<filename>` and referenced from a generated
`# Image Attachments` section in the `.md`, placed between the description and
the links. The attachment id prefix keeps duplicate filenames distinct.
Download is best-effort: a failed image is skipped (counted in `assets_skipped`
and reported via a single stderr warning), the issue is still written, and only
images that landed on disk are linked. Attachments with an empty or
`application/octet-stream` media type are skipped (same as `jira issue images`).
The raw `<KEY>.json` snapshot is unchanged — it never carries local paths.

Like Confluence pull, Jira pull is non-destructive by default. It preserves and
reports local `.wiki` edits and unapplied/unsupported `.md` views, continues
refreshing clean siblings, then exits `8` when anything was blocked. Explicit
overwrite/stash recovery is limited to a native edit with intact sidecar/base
evidence. Use `jira apply` or `jira push` for intentional work; use
`--stash-local` when discarding the working native bytes only after retaining an
exact content-addressed copy.

Output layout:

```
mirror-jira/
  PROJ/
    PROJ-1.wiki             # native Jira wiki body, verbatim — the editable source
    PROJ-1.md               # derived staging view; edit supported sections, then jira apply
    PROJ-1.json
    PROJ-1.assets/          # only with --assets, when the issue has images
      10001-screenshot.png
    PROJ-2.wiki
    PROJ-2.md
    PROJ-2.json
```

The `.md` is a lossy, best-effort read view (headings, emphasis, `{code}`/
`{quote}`/`{panel}`, lists, tables, links, `!image!` embeds, `{color}`,
`[~mentions]`); a render failure degrades that one section to a stub comment and
never fails the pull. To change supported content, edit generated `# Description` and/or
an opt-in editable rich-text field section in the `<KEY>.md` view and run `jira apply` (block-level,
non-lossy — the recommended loop just below), or edit `<KEY>.wiki` directly for what
the md view can't express (a bare `.md` edit never pushes on its own). Either way,
`jira push` is the only path to the server, and
`jira issue update --from-file <KEY>.wiki` remains the one-shot alternative.

The pull also records the `.wiki` body in the mirror sidecar (`.atl/state.json`)
plus a pristine base copy (`.atl/base/<KEY>.wiki`), which `jira status` and
`jira push` use to detect local edits and remote drift. Mirrors pulled by an
older `atl` have no sidecar entry: those issues read as never-synced (and are
not pushable) until re-pulled.

`<KEY>.json` shape:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "fields": {
    "summary": "Issue summary",
    "customfield_10001": "custom value"
  }
}
```

### `atl jira status`

Report which mirrored issues have local `.wiki` edits or pending editable-field
updates and, with `--remote`, which have drifted on the server since their bases
were captured. Content-hash based, the
Jira analog of `conf status`.

```bash
atl jira status                     # default root: mirror-jira (or $ATL_MIRROR_ROOT)
atl jira status my-jira-mirror
atl jira status --remote            # also check remote drift (one request per issue)
```

Each entry carries `locally_edited` (the `.wiki` differs from the pulled base or
at least one field is pending), optional `pending_fields`,
`synced` (`false` for a `.wiki` with no sidecar entry — never pulled through the
sidecar, so `locally_edited` + `synced:false` means "never-synced"), and, with
`--remote`, `remote_drifted` (the remote description or a pending field differs
from its stored base), optional `field_drifted`, or `remote_error` (the remote could not be checked — an uncheckable issue
is never reported in-sync). Drift needs a baseline: an issue with no base copy is
never reported drifted.

### `atl jira snapshot`

Return exact, content-free health cardinalities for a durable Jira mirror. The
offline default needs no backend URL, PAT, or config and performs no
transaction recovery, filesystem write, or network request. It takes a shared
advisory lock only when the persistent mutation lock already exists. An active
mutation fails closed with exit `8` before inspection; a legacy mirror without
that lock is re-inspected if a current writer creates it during the first read.

```bash
ATL_READ_ONLY=1 atl jira snapshot
ATL_READ_ONLY=1 atl jira snapshot my-jira-mirror
ATL_READ_ONLY=1 atl jira snapshot my-jira-mirror --remote
```

The JSON partitions local clean/edited and canonical tracked/untracked issue
substrates (including tracked-but-removed entries), wiki baseline presence and integrity, sibling raw-snapshot
presence/readability/key binding, pending-record validity/binding, and derived
view marker state. Current, known legacy, missing-marker, unsupported/future,
missing, and unreadable Markdown views remain distinct. The aggregate emits no
issue key/id, path, hash, field id, diagnostic text, wiki/raw-snapshot content,
or derived-view bytes.

`complete` means every inspected byte source needed for a trustworthy snapshot
was readable, internally valid, and stably bound; it does not mean the mirror
is clean. `reconciled` means every documented partition adds up exactly. A
baseline mismatch, malformed/misbound raw snapshot, invalid/unbound pending
record, active pending transaction, or unreadable source returns the qualified
snapshot with exit `8`. Missing optional/legacy evidence remains an explicit
count rather than silently reading as present. If writing that snapshot to
stdout fails, the write failure is reported together with the inspection
failure and the exit code stays the inspection code. If inspection otherwise
succeeds, the write failure is returned on its own with generic exit `1`.

`--remote` first completes that local preflight before loading backend config or
credentials. A failed preflight makes zero requests. Otherwise each eligible
canonical tracked issue with a valid baseline receives at most one GET attempt;
generic replay-safe retries are disabled and redirect responses are not
followed. Remote `attempted = checked + unavailable`, `checked = in_sync +
drifted`, and local `present = attempted + not_attempted`. A redirect or other
unavailable probe sets `complete:false` and never counts as in-sync. The command
never writes or repairs mirror state.

### `atl jira apply`

Merge supported edits made in an issue's markdown view (`<KEY>.md`) into its
`<KEY>.wiki` substrate and explicit pending-field set, block by block — the Jira analog of `conf apply`. This closes the
authoring loop **pull → edit the `.md` → apply → push**: you edit a familiar
markdown view instead of hand-writing Jira wiki markup, and `apply` folds the
change into the guarded local write set that `jira push` sends to the server.

The generated `# Description` section and field sections whose descriptor has
`editable:true`, `placement:"section"`, and `format:"jira_wiki"` are writable.
Blocks you did not touch keep their **exact base bytes**; changed or new blocks convert from the same strict
markdown subset as `jira issue create --from-md` (headings, paragraphs, lists,
simple tables, fenced code, blockquotes, links). Description is written to
`.wiki`; field values are stored under `.atl/pending/jira/` without modifying
the raw issue snapshot. Local only — `jira push` remains the write path to the server.

```bash
atl jira apply my-jira-mirror/PROJ/PROJ-1.md
atl jira apply PROJ-1.md --dry-run     # report the merge without writing
atl jira apply PROJ-1.md --allow-loss  # intentional {panel}/{color}/mention/embed removal
# after a fresh pull, compare raw remote vs visible local proposal first
atl jira apply PROJ-1.md --rebase-pending
```

| flag | description |
|---|---|
| `<FILE.md>` | the issue's markdown view (positional, required) |
| `--dry-run` | report the merge without writing files |
| `--allow-loss` | proceed when the edit drops wiki-only constructs |
| `--rebase-pending` | explicitly adopt freshly pulled raw field values as new pending drift bases after review |
| `--into` | mirror root (defaults to nearest `.atl`) |
| `--render-profile` / `--render-include` / `--render-exclude` | override the recorded view (normally unnecessary) |

`apply` reproduces the pristine view from the render settings the `.md` was last
written with (recorded on `pull`/`render` in `.atl/state.json`), diffed against
your edit — so no `--render-*` flags are needed. Pass them only to override that
recorded view; a mismatched profile will then flag the (unchanged) decorations as
edited. A pre-upgrade mirror with no recorded view falls back to the ambient
config — re-run `jira render` once to record it.

If you edit `.wiki` directly while fields are pending, its exact hash no longer
matches the reviewed combined write set and push refuses. Review both changes,
then `jira apply --rebase-pending` explicitly binds the proposals to that exact
wiki and regenerates `.md` without merging its stale Description.

Output: `{path, wiki_path, pending_path?, dry_run, rebased?, report: {...}, fields?:
[{id,pending,report}], wrote, warning?}`. After a successful apply the `.md` is regenerated from
the merged body so both surfaces agree; a failed refresh sets `warning` and the
apply still succeeds.

Pass `-o text` for a compact human loss-review — block counts, each removed
construct, and a contextual next-step hint (the Jira analog of `conf apply`'s):

```text
applied: PROJ/PROJ-1.wiki
blocks: 2 unchanged, 1 converted
removed constructs:
  - panel "{panel:title=Note}…"
next: run `jira push PROJ-1.wiki` to publish
```

The first line is the versioned format marker
`<!-- atl:document jira-issue v3 -->`; v2, v1, missing, or unversioned markers fail
closed and require `jira render` (or a fresh pull) before editing. V1 used the
former generated bullet form for Subtasks/Epic Children; v2 predates the
recorded display-timezone contract. Apply never guesses that an old generated
region was a user edit. A future or
unknown version requires updating `atl`; never render/downgrade it with the
older binary. Directory render checks all selected markers before rewriting the
first view. Because
render rewrites `.md`, save any existing edits as a reviewed external patch,
render the exact file/root, then reapply them. The
`<!-- atl:document ... -->` and `<!-- atl:section ... -->` prefixes are reserved
view boundaries. If either appears inside an editable Description or field
value, apply fails closed before changing `.wiki`, snapshot, or pending state;
remove it or edit the native `.wiki` substrate deliberately.

Each successful apply binds the exact issue key, relative `.wiki` path, native
hash, and unchanged remote-base hash in internal mirror state. Consecutive
Markdown edits can therefore be applied before push without moving the remote
baseline. A pull clears that generic binding when it advances remote state; if
it intentionally preserves a validated pending-field transaction, that exact
pending wiki hash remains the local lineage. Manual native edits still require
the explicit reviewed `--rebase-pending` path when fields are pending.

The merge is **fail-closed** (exit `8`, nothing written) when: an edited block
cannot be converted to wiki (a construct outside the subset) — make that edit in
the `.wiki` directly; a wiki-only construct present in the base is dropped by the
edit (`{panel}`, `{color}`, `[~mention]`, `!embed!`, a macro) and `--allow-loss`
was not given (the dropped constructs are listed in `removed_constructs`); an edit
touches any section other than generated `# Description` or an opt-in editable field (Metadata, Comments,
Links, Image Attachments) — the refusal names the section and the dedicated
command (`jira issue update`, `jira issue comment add`, `jira issue link add`,
`jira issue attachment upload`); or the local `.wiki` matches neither the
last-synced base nor exact ATL-staged/pending lineage (a direct `.wiki` edit
wins — preserve and push it, or explicitly rebase pending fields). Exit `4`:
the issue was never pulled (no base or snapshot).

### `atl jira push`

Push an edited `<KEY>.wiki` description and any pending opt-in rich-text fields
back to its issue. **Dry-run by
default** — without `--apply` it only previews the unified diff and any drift,
writing nothing. The diff shows what the write changes **on the server**
(current remote → local body), so under `--force` the remote-only changes about
to be overwritten are visible in the preview. No field outside the explicit
pending set is written. Description and fields are sent in one typed update when
both changed. This is the Jira analog of `conf push`, but deliberately stricter:

A `.wiki` whose issue key is tracked at another sidecar path is a stale copy:
preview and apply both fail closed with `skipped:"non-canonical-path"`, including
under `--force`, before any Jira read or write. Use the canonical mirror path.

```bash
# preview one file (dry-run: shows the diff, writes nothing)
atl jira push my-jira-mirror/PROJ/PROJ-1.wiki

# preview every locally-edited issue under a directory
atl jira push my-jira-mirror/

# actually write the change back
atl jira push --apply my-jira-mirror/PROJ/PROJ-1.wiki

# write over a drifted remote (re-base on current remote, then write)
atl jira push --apply --force my-jira-mirror/PROJ/PROJ-1.wiki
```

Flags:

| flag | description |
|---|---|
| `--apply` | actually write the change (default is a dry-run preview only) |
| `--force` | override description drift only; pending-field drift still refuses |
| `--into` | mirror root (defaults to the nearest `.atl`) |

A single-file target is pushed if changed (or with `--force` when clean); a
directory target pushes locally-edited files and field-only pending issues under it (`--force` does
not resurrect clean files). A file that was never pulled through the sidecar is
refused (exit 2 — pull it first).

**No server-side version gate.** Jira Data Center has no optimistic version gate,
so the staleness guard is an app-layer compare-and-swap: `jira push` re-reads the
remote description and every pending field. If the description changed since
pull, the push is **refused** with exit 8 ("remote
description changed since pull … re-pull or push `--force`") and nothing is
written unless explicitly forced. A pending field that no longer equals its
captured base is always refused, including with `--force`; re-pull and reconcile
it. A fresh pull keeps the local proposal visible in `.md` and puts the remote
value in raw `<KEY>.json`; when Description is also pending in `.wiki`, pull
preserves that reviewed local body while advancing its remote base. Compare the
versions, edit the proposal if needed, then run
`jira apply --rebase-pending` to adopt that snapshot as the new base. The next
push still fresh-reads it and refuses if it changed again. This CAS has an inherent time-of-check-to-time-of-use (TOCTOU) window —
the remote can still change between the check and the write — which `--force`
opts out of the refusal for rather than closing. A drift refusal is exit 8
(`ErrCheckFailed`), **never** exit 5 (`ErrVersionConflict`): exit 5 is reserved
for Confluence's real version gate. A server-side HTTP 409 (a locked issue, a
workflow veto) stays a generic conflict, distinct from local drift.

Typed writes are not replayed after an ambiguous response; atl performs one
fresh end-state read and accepts success only when all proposed values match.
If Jira already equals the proposal after a failed local refresh, the next push
repairs refresh/clear state without replaying the write.
On `--apply` success the mirror is refreshed (the `.wiki`, `.md`, raw `.json`,
base copy, and sidecar are rewritten, and pending state is cleared). A transport
or local-filesystem refresh failure is a warning — re-pull if you see one. If
the verification read succeeds but Jira no longer matches the full reviewed
proposal, pending state is retained and the command fails closed with exit 8.

### `atl jira export`

Write one compact issue export as a file plus backend-identity-hashed provenance
manifest, or as a transient stdout artifact. This is for scripts and analysis
that need JSONL/JSON/CSV instead of a directory mirror. For file destinations,
the manifest is written to `<out>.manifest.json` and stores query, fields,
format, count, CLI version, and a backend URL hash; it does not store the backend
hostname or token.

```bash
atl jira export --jql "project=PROJ" --format jsonl --out issues.jsonl
atl jira export --jql "project=PROJ" --format csv --fields customfield_10001 --out issues.csv
atl jira export --jql "project=PROJ" --format csv --out raw.csv --raw-csv # unsafe in spreadsheets
atl jira export --jql "project=PROJ" --format json --out issues.json --limit 10000
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 --out selected.jsonl
atl jira export --keys PROJ-1,PROJ-2 --fields "Delivery Notes" --out - | jq -s '.'
atl jira export --ids 10001,10002 --format json --out - | jq 'map(.key)'
atl jira export diff old.jsonl new.jsonl
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query; pass exactly one of `--jql`, `--ids`, or `--keys` |
| `--ids` | comma-separated numeric issue ids; generated batches emit found rows in de-duplicated first-occurrence order |
| `--keys` | comma-separated issue keys; generated batches emit found rows in case-insensitive first-occurrence order |
| `--batch-size` | max ids/keys per generated JQL batch (default 100) |
| `--out` | artifact path (writes `<out>.manifest.json`), or `-` for artifact-only stdout with no files/manifest |
| `--format` | `jsonl`, `json`, or `csv` (default `jsonl`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated exact field ids or unambiguous display names |
| `--raw-csv` | preserve formula-leading CSV cells verbatim (unsafe in spreadsheets) |

JSONL and CSV are written incrementally through an fsynced atomic temporary file, so
`--limit 0` does not accumulate issue payloads or the artifact in memory. Aggregate
JSON intentionally caps at 10,000 issues and 64 MiB of serialized issue data;
use JSONL/CSV or a smaller limit for larger selections. CSV neutralizes formula-leading cells by default using an
apostrophe prefix. The manifest records `csv_raw: true` when the unsafe raw mode
is explicitly selected. Exact cross-page deduplication uses a bounded identity
index and refuses a single export above 250,000 unique issues; split larger
selections into multiple exports.

For explicit `--keys` and `--ids`, every format and destination preserves the
caller's de-duplicated first-occurrence order across generated batches, even
when Jira returns pages in another order. Missing or inaccessible identities
produce no placeholder row; the surrounding found rows keep their requested
relative order, and `--limit` counts only emitted rows. File manifests declare
`row_order: selector` and `missing_identity_behavior: omit`. A user-authored
`--jql` is never reordered (`row_order: backend`). Explicit selections are
buffered only one configured batch at a time, with a 64 MiB reorder-buffer cap
that asks the caller to reduce `--batch-size`; JQL JSONL/CSV stays streaming.

With `--out -`, stdout contains **only** the artifact: one object per line for
JSONL, a JSON array for `--format json`, or CSV bytes. No manifest, command
result object, or trailing status line is emitted, and no filesystem artifact is
created. The artifact type is selected by `--format`; do not pass `-o text`
with `--out -` (that combination returns exit 2). Warnings/errors remain on
stderr. JSON keeps the same 10,000-issue and
64 MiB aggregate caps; JSONL/CSV retain the 250,000-identity safety cap and
formula neutralization. A streaming request can have written a prefix before a
later backend failure, so pipelines must check atl's exit status and discard
stdout on non-zero exit. Display-name fields resolve fail-closed before the
first search and the artifact uses their stable ids.

`jira export diff` compares compact JSONL/JSON/CSV exports by issue key (or id
when key is absent) and reports deterministic `added`, `removed`, and `changed`
identifier lists.

### `atl jira planning report`

Build a deterministic read-only planning quality report over a JQL query. The
rubric checks summary, description, assignee, optional estimate field, optional
required fields, artifact references, and optional epic decomposition.

```bash
atl jira planning report --jql "project=PROJ" \
  --estimate-field customfield_10001 \
  --epic-field customfield_10002 \
  --require fixVersions,components \
  --csv planning.csv
atl jira planning report --jql "project=PROJ" --csv raw.csv --raw-csv # unsafe in spreadsheets
atl jira quality-report --jql "project=PROJ"     # compatibility alias
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--require` | comma-separated fields that must be populated |
| `--estimate-field` | field id/name used as the estimate check |
| `--epic-field` | field id/name containing parent epic key; enables child lists and missing-epic gaps |
| `--limit` | max issues (0 = all; default 100) |
| `--csv` | optional CSV report path |
| `--raw-csv` | preserve formula-leading cells verbatim; requires `--csv` and is unsafe in spreadsheets |

Output includes per-issue `score`, `max_score`, `level` (`good|warn|poor`),
`gaps`, extracted `refs`, and `children` for epic rows when `--epic-field` is
set. Reference kinds are deterministic categories such as `doc`, `design`,
`jira`, `chat`, and `link`.

### `atl jira fields`

List all Jira fields (system and custom) with their IDs and schema types.

```
atl jira fields
atl jira fields --name-like Epic
atl jira fields --id customfield_10001
atl jira fields --custom true --schema string --id-like customfield
atl jira fields --summary-only
```

Filters are applied client-side to Jira's field list. Available filters are
`--id`, `--id-like`, `--name-like`, `--schema`, and `--custom true|false`.
`--summary-only` keeps the same filters and qualification but emits no field
definitions. It sets `projection:"summary"` and returns the filtered
`custom_count` and `system_count`; their sum equals `count`. The default full
projection keeps `fields` and reports the same reconciled counts. Use
`field-options` when you need values allowed for a specific project and issue
type. The JSON envelope is versioned and reports `source`, `complete`, optional
`partial_reason`, `projection`, the unfiltered `total`, and filtered `count`;
filters and projections never change source completeness. A successful
non-empty atomic Jira catalog is complete. An empty or unqualified source is
explicitly incomplete, so agents must not treat a non-empty match or successful
call alone as proof. The full `-o text` projection preserves its
`complete, source, count, total` first line and emits one tab-separated
`id, name, custom, schema` record per field. The summary text projection adds
one `projection=summary, custom, system` line and no field records. Field
options and link types emit one value per line, and transitions emit
`id, destination, name`.

### `atl jira field-options`

List allowed values for a field on a specific project/issue-type combination
(uses the `createmeta` API).

```
atl jira field-options --project PROJ --type Bug --field priority
atl jira field-options --project PROJ --field customfield_10020
```

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (optional) |
| `--field` | field id or display name (required) |

### `atl jira transitions`

List the workflow transitions currently available for an issue.

```
atl jira transitions --key PROJ-1
```

### `atl jira link-types`

List the configured issue link type names.

```
atl jira link-types
```

### `atl jira me` / `atl jira user {search,get}`

Identity lookups using the Data Center username/userkey model (not Cloud
accountId). `-o id` prints the username for piping.

```
atl jira me                      # the authenticated user
atl jira user search 'alice'     # {users:[{name,key,displayName,email,active}]}
atl jira user get alice          # one user by DC username
```

### `atl jira board {list,get,config,issues,backlog,view,export}` and `atl jira sprint {list,get,current,issues,add,remove}`

Agile boards & sprints, via the Data Center Agile API (`/rest/agile/1.0/`).
**Requires Jira Software** (GreenHopper); on a Core/Service-Management-only
instance the Agile endpoints 404 (exit 4). Boards and sprints are addressed by
**numeric id** — use `board list --project` to discover the id `--board` wants.

```bash
atl jira board list --project PROJ          # {boards:[{id,name,type,project_key}]}; -o id → board ids
atl jira board get 5
atl jira board config 5                     # filter, ordered columns/status ids, limits, estimation, rank field
atl jira board issues 5 --columns position,key,summary,status,assignee # one ranked page; -o id → keys
atl jira board issues 5 --view full                  # reusable configured projection
atl jira board backlog 5 --columns position,key,summary,status          # Scrum only; explicit pagination
atl jira board view 5 -o text               # normalized config + status-to-column mapping
atl jira board view 5 --jql 'statusCategory != Done' --limit 500
atl jira board view 5 --columns key,status,updated,customfield_10001 \
  --epic-field customfield_10001 --done-status Done
atl jira board export 5 --format jsonl --out board.jsonl
atl jira sprint list --board 5 [--state active|closed|future]   # {sprints:[...]}; -o id → sprint ids
atl jira sprint current --board 5           # the active sprint (exit 4 if none)
atl jira sprint issues 7 --columns position,key,summary,status  # issues in sprint 7; -o id → keys
atl jira sprint add 7 PROJ-1 PROJ-2         # move issues into sprint 7
atl jira sprint remove PROJ-1               # move issue(s) back to the backlog
```

`--board` and positional board/sprint ids must be positive (else exit 2).
JQL search, `board issues`/`board backlog`, and `sprint issues` return one
versioned issue-list shape: `source`, `selection`, ordered
`projection.columns/fields`, `rows[]` with identity plus `values` and namespaced
source `context`, and `page`. Their `-o text` form is a Markdown table; `-o id`
remains one key per line. Board pages expose one API page with `page.complete`
and `page.next_cursor`; page size is capped at 50. `board view` follows all pages by
default (`--limit 0`) and preserves backend rank order. A positive view/export
limit applies per requested scope and sets `complete:false`, `truncated:true`
when more rows exist.

The normalized view maps each issue's `status_id` to the first configured board
column and preserves unknown statuses as `column:"Unmapped"` with
`column_mapped:false`. `--scope all` reads board plus backlog membership on
Scrum. Jira Software's backlog issue endpoint is not available for Kanban, so a
Kanban `all` view reads board scope only, records `backlog_fetched:false`, and
never calls a sprint or backlog endpoint. Interpret its ordered configured
columns rather than pretending a separate backlog membership was observed.

For a deterministic epic aggregate, select the exact epic relation field and
`updated`, then pass that field to `--epic-field` and one or more repeatable
`--done-status` values. The optional `epic_rollup` groups the already-fetched
rows without another Jira request. It reports child/status/done counts, latest
child update, parent presence, timestamp coverage, and its own `complete`
signal. A missing parent row, missing child timestamp, or incomplete snapshot
makes the rollup incomplete. Relation and timestamp type errors fail closed.
The flags apply only to `board view`; `board export` formats are unchanged.

Use JSON for one complete object, JSONL for streaming `jq`, CSV for relational
tools/spreadsheets, and Markdown for review. CSV formula-leading cells are
neutralized by default; `--raw-csv` is the explicit unsafe opt-out. Board reads
never call rank/move/update endpoints. Sprint `add`/`remove` remain separate
mutating commands and require explicit user intent.

Use `--columns` as the single list projection control. It derives backend Jira
fields and preserves the requested order in Markdown. Namespaced source columns
include `board.column`, `board.in_backlog`, and `sprint.id`; unavailable context
columns fail with usage rather than silently rendering empty.
For repeated work, use `--view default|full|<custom>` instead. Precedence is
explicit `--columns` → named view → built-in `default`; output records the
resolved name as `projection.view`. Unknown views and source-invalid columns
fail before a backend request.

### `atl jira structure {get,view,forest,rows,folders,values,pull-issues,export}`

Read-only Tempo Structure access via the Structure REST API
(`/rest/structure/2.0/`). Structures are addressed by numeric id. If the
Structure plugin is not installed, the endpoint is disabled, or the object is not
visible to the token, Jira returns an API error (commonly exit 4 or 6).

```bash
atl jira structure get 123
atl jira structure view 123                         # normalized JSON; -o text -> Markdown table
atl jira structure view 123 --fields key,summary,status,assignee
atl jira structure view 123 --view full
atl jira structure forest 123
atl jira structure rows 123                         # parsed forest rows; -o id -> row ids
atl jira structure rows 123 --root "release train"  # first matching subtree
atl jira structure folders 123                     # stable folder ids, paths, subtree statistics
atl jira structure view 123 --folder-id 100        # exact stable folder selector
atl jira structure view 123 --folder-path 'Plans / Quarter' -o text
atl jira structure view 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7 # bind a selector to one forest version
atl jira structure rows 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure pull-issues 123 --folder-id 100 \
  --expected-forest-signature 55 --expected-forest-version 7 --out issues.json
atl jira structure export 123 --root "release train" --fields key,summary,status --format jsonl --out structure.jsonl
atl jira structure export 123 --folder-id 100 --format md --out structure.md \
  --expected-forest-signature 55 --expected-forest-version 7 # fails before the file is created if stale
atl jira structure export 123 --fields summary --format csv --out raw.csv --raw-csv # unsafe in spreadsheets
```

`view` is the recommended agent read path. It joins the hierarchy's stable item
identities with compact Jira issue fields; stored folders receive best-effort
labels, while calculated grouping/generator rows keep honest technical labels.
The nested Structure identity always includes `read_only:true|false`; `false`
is explicit rather than omitted, so agents can distinguish known mutability
from missing metadata.
Folder discovery likewise always emits string `name` and `parent_folder_id`
fields. An unavailable label remains `name:""` while `path` uses
`folder:<id>` as a stable technical fallback; root folders use
`parent_folder_id:""`.
JSON is the default, `-o text` is a Markdown table, and `-o id` emits row ids.
The default projection is `key,summary,status,assignee`;
`--fields` accepts Jira field ids and replaces that list. `--view NAME` selects
the preset's `structure` fields; explicit `--fields` wins. Structure presets
accept Jira field ids only because hierarchy columns remain fixed and honest.
The JSON projection records `source:"list-view"` for named/default presets and
`source:"explicit"` only when `--fields` supplied the projection.

Tempo's browser saved views and per-user column adjustments are a separate UI
configuration surface and are not reproduced by the documented integration
API. Every snapshot therefore includes an explicit `projection` object with
`browser_view_reproduced:false`. Ask which planning columns matter and pass
them through `--fields` instead of assuming the browser's currently selected
view.

Generated Structure rows may receive new ephemeral `row_id` values on a later
expansion even when the plan is unchanged. Atl therefore resolves issue data by
stable issue `item_id` only when `item_type` is `issue`, never by calculated row
id. Jira's advisory strict-query validation is disabled only for these generated
Structure identity joins so one deleted or permission-hidden id does not reject
the whole batch; user-authored JQL remains strict and inaccessible rows remain
explicit. Use `values.key`, `item_id`,
and the ordered hierarchy for durable analysis; use `row_id` only within one
snapshot.

`folders` is the fast discovery path: it reads metadata, one forest, and one
batched Structure Value projection for stored-folder labels, but never searches
Jira issues. JSON reports stable `folder_id`, snapshot-local `row_id`, exact
folder path, parent folder, depth, and subtree statistics for descendant rows,
issue occurrences, unique issues, descendant folders, and maximum relative
depth. `-o id` prints stable folder item ids; `-o text` is a compact Markdown
table. Label failures retain ids/statistics with `complete:false` and bounded
warnings.

`rows`, `view`, `pull-issues`, and `export` accept exactly one selector:
`--folder-id` (preferred stable id), `--folder-row` (one current-forest
occurrence), `--folder-path` (exact normalized slash-separated path), or legacy
fuzzy `--root`. Exact selectors verify a stored folder, never fall back to the
first substring match, and fail closed on absence/ambiguity. JSON preserves
absolute `depth`/`parent_row_id`, adds `relative_depth`, and returns a
`selection` object. Selected Markdown starts at depth zero. Path matching is
case-insensitive and collapses whitespace per segment; use folder id/row when a
folder name contains `/`. Completeness is scoped to emitted rows, so missing
labels in an unrelated branch do not mark a selected subtree partial.

`view`, `rows`, `pull-issues`, and `export` additionally accept the same paired
optional flags `--expected-forest-signature` and `--expected-forest-version`,
which bind one read to exactly one forest version. Omitting both is an
explicitly ungated read. If either is supplied both are required, the signature
must be non-zero, and the version must be positive; an unpaired, zero, or
non-positive pair is a usage error (exit `2`) decided before any backend
request. When a `--folder-id`, `--folder-row`, or `--folder-path` selector came
from an earlier `view`, `folders`, or `rows` result, copy both members of that
result's forest version into the later call. A selector fixed outside any
earlier read may omit both and must then be read as ungated evidence. A stale
pair fails with exit `8` after the initial forest read and before any
stored-folder label lookup, Structure Value request, Jira issue expansion,
export rendering, or local output file, so a caller that already lost the race
does no projection work and leaves no partial artifact; the diagnostic carries
only the expected and current signature/version integers. `get`, `forest`,
`folders`, and `values` accept no expected-version flags.

The gate is a single comparison against the forest the read is built from; no
additional forest request is made, and there is no final re-read after
projection. Every successful `view` snapshot therefore reports the
`forest_version` it was assembled from plus `forest_version_gated` — true only
when the caller supplied the exact pair. `rows` and `pull-issues` keep their
existing `version` member and add the same always-present
`forest_version_gated`; the `export` command result adds always-present
`forest_version` and `forest_version_gated`. A returned pair with either member
zero is non-bindable: do not pass it back through the expected-version flags,
and keep the later selection explicitly ungated. The forest version qualifies
the hierarchy and the selection only: Jira issue fields and stored-folder labels
are read at separate times and are not covered by it, so a gated result is not
one atomic versioned value snapshot.

`rows` parses Structure's forest formula into a stable row list. `--root`
matches the first row by row id, item id/type/semantic, or by selected Structure
attribute values (`--root-fields`, default `key,summary`) and emits only that
row plus its descendants:

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": false,
  "rows": [
    {
      "row_id": 100,
      "depth": 0,
      "item_type": "issue",
      "item_id": "10001",
      "position": 0
    }
  ]
}
```

`values` posts selected row ids and attribute ids to the Structure value
resource. The output preserves the raw response under `raw`, exposes
`responses`, and lifts any reported inaccessible row ids to
`inaccessible_rows` so scripts can detect permission gaps.

`pull-issues` collects numeric Jira issue ids from Structure issue rows and reads
the matching Jira issues via generated `id in (...)` JQL batches. Its default
field set comes from `jira.list_views.default.structure`; use `--view` for a
named projection or explicit `--fields` to override it. It emits:

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": false,
  "issue_ids": ["10001"],
  "issues": [
    {
      "key": "PROJ-1",
      "id": "10001",
      "fields": {
        "summary": "Example"
      }
    }
  ],
  "count": 1
}
```

Full Structure Markdown uses separate emitted `#`, numeric `Depth`, technical
`Type` and `Item`, Jira value columns, and `Access`; it no longer combines
indentation, key, and summary into a duplicated `Tree` cell.

`export` writes the same normalized projection as `view`. Supported formats are
`json`, `jsonl`, `csv`, and `md`; `--out` is required. JSONL emits one
self-contained hierarchy row for line-oriented tools, CSV includes row metadata
plus requested issue fields, and Markdown renders a compact hierarchy table.
The command result reports the exported artifact plus the forest it was built
from:

```json
{
  "path": "structure.jsonl",
  "format": "jsonl",
  "structure_id": 123,
  "forest_version": {
    "signature": 55,
    "version": 7
  },
  "forest_version_gated": true,
  "row_count": 1,
  "issue_count": 1
}
```

The JSON and Markdown artifacts carry the same normalized snapshot as `view`,
JSONL repeats `forest_version` and `forest_version_gated` on every record, and
the Markdown header note states the signature, version, and gate status. CSV
headers and cells are unchanged, so a CSV export carries that provenance only in
this command result — record it alongside the file if the CSV must stay
auditable. With `-o text`, the result line includes `forest_signature`,
`forest_version`, and `gated` alongside the format and counts.
The reported unique-issue count follows the emitted root/subtree. CSV formula-leading
cells are apostrophe-prefixed by default. `--raw-csv`
preserves them verbatim only with `--format csv` and produces an artifact that is
unsafe to open in a spreadsheet. These commands are read-only and do not write
Structure data back to Jira.

### `atl manifest create`

Write a backend-identity-hashed manifest for a local mirror or snapshot root. The manifest
counts files/bytes/extensions and records reproducibility metadata such as the
source command, selectors, fields, include flags, ATL version, and backend URL
hashes. Configured backend URLs are represented only by hashes and stored PATs
are not read. Caller-provided command text, selectors, JQL/CQL, field names,
include values, and paths are preserved verbatim without redaction: never put a
credential in those flags, and review the manifest before publishing it.

```bash
atl manifest create --root mirror-jira --service jira --selector 'jql=project=PROJ' --fields summary,status
atl manifest create --root mirror-conf --service confluence --out mirror-conf/manifest.json
```

Flags:

| flag | description |
|---|---|
| `--root` | local mirror/snapshot root directory (required) |
| `--out` | manifest output path (default `<root>/manifest.json`) |
| `--service` | optional `jira`, `confluence`, or `generic` |
| `--selector` | comma-separated selectors to record |
| `--fields` | comma-separated field names/ids to record |
| `--include` | comma-separated include flags to record |
| `--command` | command string to record (default `atl manifest create`) |

---

## `atl version`

Print the current binary version and informational build provenance. JSON is
the default:

```json
{
  "version": "0.5.1",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "build_state": "clean"
}
```

`commit` is the full source revision when known. `build_state` is one of
`clean`, `dirty`, or `unknown`; it describes tracked and non-ignored untracked
workspace changes for supported Makefile builds. Direct Go builds use compiler
VCS metadata when available. These fields are diagnostic only: self-update and
signature verification do not trust them. Builds intentionally contain no
timestamp.

```
atl version
atl version -o text
```

Text output remains the bare version for script compatibility. Root
`atl --version` also keeps its existing `atl version <version>` form.

---

## Workflow: pull → edit → validate → push

This is the canonical edit loop for Confluence pages:

```bash
# 1. Pull the page (and its draw.io/image assets if needed)
atl conf pull --id 12345678 --assets --into mirror

# 2. Inspect the on-disk layout
#    mirror/DOCS/parent/child/child.csf   ← your source of truth
#    mirror/DOCS/parent/child/child.md    ← human-readable view

# 3. Edit child.csf directly.
#    Tip: read child.md for orientation; edit child.csf for correctness.

# 4. Validate before pushing
atl conf validate mirror/DOCS/parent/child/child.csf

# 5. Dry-run to see what fragments change
atl conf push --dry-run mirror/DOCS/parent/child/child.csf

# 6. Push (version gate is automatic)
atl conf push mirror/DOCS/parent/child/child.csf

# If exit 5 (version conflict): someone else edited the page.
# Re-pull, re-apply your changes, then push again.
atl conf pull --id 12345678 --into mirror
```

For a whole space:

```bash
atl conf pull --space DOCS --into mirror
# ... edit files ...
atl conf status mirror                # see which files are dirty
atl conf push mirror/DOCS/           # push all dirty files under DOCS/
```

For Jira issues the workflow is read-heavy:

```bash
atl jira pull --jql "project=PROJ and status=Open" --into mirror-jira
# read mirror-jira/PROJ/PROJ-1.md  and  mirror-jira/PROJ/PROJ-1.json
# make changes via commands:
atl jira issue update PROJ-1 --summary "Revised title"
ATL_READ_ONLY=1 atl jira issue transition preview PROJ-1 --to "In Review"
# Repeat the exact target/fields/comment with transition --apply and the reviewed hash.
atl jira issue comment preview PROJ-1 --from-file - <<'EOF'
Updated as discussed in today's meeting.
EOF
# Repeat the exact body with `comment add --apply --expected-proposal-hash ...`
```
