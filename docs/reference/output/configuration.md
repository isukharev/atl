# Configuration and environment output contracts

Mirror binding, doctor, compatibility, time qualification, configuration, and profile result shapes.

[Reference index](README.md) · [Documentation home](../../README.md)

## Mirror backend binding

`atl mirror backend status [DIR]` is a local, content-minimized inspection of an
initialized mirror. It does not load config or credentials and performs no
network access. Default JSON is deterministic by service:

```json
{
  "schema_version": 1,
  "root": "mirror",
  "bindings": [
    {
      "service": "confluence",
      "origin_sha256": "sha256:<64 lowercase hex characters>"
    }
  ]
}
```

An unbound mirror emits `"bindings":[]`. Text output is `no backend bindings`
when empty; otherwise it prints one `service origin_sha256` pair per line.

`atl mirror backend bind [DIR] --service confluence|jira` previews a binding by
default and writes nothing:

```json
{
  "schema_version": 1,
  "root": "mirror",
  "service": "confluence",
  "mode": "preview",
  "status": "would_bind",
  "backend_sha256": "sha256:<64 lowercase hex characters>"
}
```

A matching existing binding changes preview `status` to `already_bound`.
Applying the reviewed value requires the exact preview digest plus both guards:

```text
--apply --expected-backend-sha256 <exact backend_sha256> --confirm BIND
```

Apply emits the same shape with `mode:"apply"` and `status:"bound"`, or
`status:"already_bound"` for an idempotent match. A mismatched reviewed digest
or an existing different binding exits `8`; a binding is compare-and-set and is
never replaced. Bind text output is `service status (root)`.

The bind operation reads only the configured service URL in memory to derive
the digest. It loads no PAT and performs no backend request. The complete
`mirror backend bind` leaf is mutation-classified, so `ATL_READ_ONLY=1` and the
equivalent global or persisted policy reject even preview before configuration
or network access. `mirror backend status` remains read-only.

The durable file is strict schema-v1 `.atl/backend-bindings.json`:

```json
{
  "schema_version": 1,
  "services": {
    "confluence": "sha256:<64 lowercase hex characters>"
  }
}
```

It is an owner-only regular file with mode `0600`. Unknown or duplicate fields,
future versions, empty service maps, invalid tagged hashes, permissive modes,
and symlinks fail closed. Raw URLs and hostnames never enter this file or the
command output.

Fresh service-empty non-dry-run pulls and explicit created-object registration
establish a missing service binding automatically. Existing unbound roots with
service evidence require the explicit reviewed bind workflow. Persisted Jira
macro expansion on a Confluence pull establishes or requires a separate Jira
binding. Remote mirror status/snapshot/push/reconcile/plan phases require an
exact match before network access; offline mirror operations remain available.

The reviewed text/id inventories annotate the command tree before execution.
They are also the source of truth for `atl capabilities`; the catalog cannot
advertise an output mode that the root preflight would refuse.

## Setup doctor

`atl doctor` returns a schema-v1, content-free aggregate with
`{schema_version,mode,complete,healthy,status,cli,runtime,config,credentials,
safety,content_policy,services,mirror,plugin,problems}`. `content_policy`
reports only `active`, `enforcement`, and the closed `advisory_because` symbols;
it never exposes policy bytes, rules, paths, URLs, or digests. Closed status/reason/remediation
values are safe for automation; configured URLs/hostnames, local paths,
environment-variable names, credentials, identities, object ids, mirrored
content, and raw parser/backend errors are never fields or interpolated text.

Offline mode performs no network request and skips self-update. Explicit
`--remote` adds no more than one single-attempt metadata GET for Jira. For
Confluence it makes one version GET and, only when that route returns `404`, one
bodyless reachability HEAD under the same deadline. Fallback success projects
static product with an empty version: `remote.status` is `available`, while
compatibility is `unverified` / `metadata_only` / `version_unavailable`. The
projection contains only static product, sanitized version/deployment metadata,
and closed outcome values. Redirects/retries are disabled and verbose trace
omits request identity. No content GET or identity-bearing route is used.
Malformed global configuration blocks all remote probes; otherwise services
qualify independently. A file-sourced URL or credential with failed owner-only
evidence is not used, while an independently ready environment source or
sibling service may proceed. Mirror findings do not suppress the unrelated
product metadata probe.

Advisories keep `healthy:true` and exit `0`. An error-severity problem sets
`healthy:false`; the aggregate is still written to stdout before the command
returns `ErrCheckFailed` / exit `8`. Consumers of doctor therefore must retain
and parse stdout even when the process exits non-zero. A stdout write failure
is joined with the check failure so neither cause is hidden. `-o text` preserves
the same facts; `-o id` is rejected in root preflight.

## Exact compatibility-provider status

`atl compatibility status` returns schema v1:

```json
{
  "schema_version": 1,
  "service": "confluence",
  "remote_requested": false,
  "status": "disabled",
  "reason": "not_configured",
  "qualified": false
}
```

`configured` is present when a syntactically valid pin exists. `observed` is
present only after a remote response passes the closed product/version/build
grammar. `provider_id` and `provider_family` are compile-time literals and are
present only when an owner-only exact activation names a compiled profile.
`status` is one of `disabled`,
`configured`, `unsupported`, `unavailable`, `mismatch`, or `matched`; `reason`
is a closed content-free classifier. Only exact configured/observed equality
sets `status:"matched"` and `qualified:true`.

The report never contains a configured URL/hostname, endpoint path, token,
response body, title, object identity, or raw transport error. Ordinary product
compatibility remains independent. `pin` and `clear` return the same offline
shape after owner-only local persistence; neither contacts a backend.

---

## Environment time diagnostics

`atl environment inspect` emits an identity- and URL-free
`EnvironmentInspectResult`:

```json
{
  "complete": true,
  "display_time_zone": {"value":"UTC","evidence":"default","source":"default"},
  "jira": {
    "configured": true,
    "status": "available",
    "server_utc_offset": {"value":"+00:00","evidence":"observed","source":"jira_server_time"},
    "user_time_zone": {"value":"Europe/Berlin","evidence":"observed","source":"jira_current_user"},
    "jql_time_zone": {"value":"Europe/Berlin","evidence":"assumed","source":"jira_current_user_time_zone"}
  },
  "confluence": {
    "configured": true,
    "status": "partial",
    "user_time_zone": {"evidence":"unknown","source":"confluence_current_user","reason":"field_not_returned"},
    "cql_time_zone": {"evidence":"unknown","source":"confluence_cql","reason":"not_exposed_by_backend_metadata"}
  },
  "confluence_incremental": {
    "query_literal_time_zone": {"value":"UTC","evidence":"configured","source":"incremental_protocol_v2"},
    "backend_query_time_zone": {"evidence":"unknown","source":"confluence_cql","reason":"not_exposed_by_backend_metadata"},
    "safety_overlap_hours": 48,
    "exact_timestamp_filter": true,
    "hidden_calibration_requests": false
  }
}
```

`evidence` is the closed set `observed|configured|default|assumed|unknown`.
Unknown facts omit `value` and use a closed privacy-safe `reason`; raw transport
or backend error text is never embedded. Backend `status` is
`available|partial|unavailable|not_configured|credentials_missing|credentials_unavailable|invalid_configuration`.
`complete` is false when a configured backend is not `available`; unconfigured
backends remain explicit but do not make another backend incomplete. With both
services available the command makes exactly three sequential GETs and no
search/content request. The command is read-only-policy compatible and has JSON
and text projections.

`atl config show` emits `{ "read_only", "confluence_url"?, "jira_url"?, "update_base_url"?, "render", "jira_list_views", "jira_list_views_error"?, "render_provenance"?, "local_config_path"?, "mirror" }`. `render` is the **effective** merged render configuration (always present; `display_time_zone` defaults to deterministic `UTC`, and both `jira` and `confluence` sections carry at least `profile`, defaulting to `default`). `render_provenance` maps each dotted render key whose value is *not* the built-in default to its source (`global` or `local`) and is `omitempty` — an all-default mirror emits none, keeping the shape backward-compatible. `local_config_path` appears only when a per-mirror `.atl/config.json` is in scope from the current directory. Warnings about forbidden/unknown keys in a local file go to **stderr** as `warning:` lines; stdout stays clean. `config set` accepts `safety.read_only`, Jira list views, or a positional dotted render key (`render.display_time_zone`, `render.{jira,confluence}.{profile,include,exclude}`, plus `render.jira.custom_fields`, `render.jira.field_views`, and `render.jira.epic_field`) alongside the existing URL flags; `field_views` is a JSON descriptor array. The display zone changes only human Markdown date/datetime projections; exact JSON/native timestamps and JQL/CQL semantics are unchanged. `--local` writes the per-mirror file (render keys only — a URL flag with `--local` is a usage error, exit 2).

Runtime commands validate all `jira_list_views` before network access and map
an invalid catalog to config exit 7. Recovery is deliberately narrower:
`config show` returns the raw entries and `jira_list_views_error`, and
`config set jira.list_views...` may replace/delete invalid entries one at a
time. A repair deletion can persist while another entry remains invalid; other
commands never consume a partially valid catalog. Malformed `config.json` JSON
also maps to exit 7 and must be repaired as a file rather than overwritten from
an uncertain partial decode. Offline, skip-self-update diagnostic reads may run
without decoding the policy so version/help/profile evidence remains available;
this exception never applies to a mutating command or online read.

`atl profile show` emits `{exists,path,hash,data?}`. A missing profile is a
successful read with `exists:false`, the future profile path, and a stable
64-hex missing-state hash. An existing profile also omits `data` by default.
`--section all|schema|preferences|team_policy|render_defaults|selectors` adds
the requested `data`; `--service jira|confluence` is valid for `schema`,
`render_defaults`, and `selectors`. A service-scoped render read returns only
`data.{jira|confluence}` and never changes runtime configuration. The selected
value is `null` when that service has no saved render memory, independent of
whether the sibling service is configured.

`atl profile preview --from-file FILE` emits
`{path,current_exists,current_hash,candidate_hash,changed,migration_from_schema_version?,sections,normalized_candidate}`.
It is read-only. Each `sections[]` item is `{section,status}` where status is
`added|removed|changed|unchanged`. The normalized candidate uses profile schema
version 1 and keeps schema facts, confirmed preferences, declared team policy,
render defaults, and named selectors separate. When a syntactically valid
future-version profile is present, preview never interprets it: it hashes the
exact bytes, sets `migration_from_schema_version`, and reports every replacement
section as changed.

`atl profile apply --from-file FILE --candidate-hash HASH
--expected-current-hash HASH` emits
`{path,previous_hash,profile_hash,changed}`. Candidate mismatch is exit 8;
current-profile mismatch is exit 5. A successful change atomically writes the
owner-only private profile; an already-current candidate succeeds with
`changed:false`. `atl profile guidance` emits
`{configured,schema_version?,instructions}` and is guaranteed not to project
profile values into `instructions`. Its generic instructions explicitly state
that saved render/mirror preferences are memory until separately compared with
and synchronized to runtime; it never emits the saved values themselves.

`atl profile suggest --from-file OBSERVATIONS --out SUGGESTION` emits
`{path,suggestion_hash,base_profile_hash,previously_rejected}` and writes the
canonical version-1 suggestion mode 0600 under an already-private parent. It
never writes `profile.json`. Observations are strict and versioned; non-schema
proposals require `{source,observed_at,reason}` evidence and cannot contain team
policy. Preference fields and Jira/Confluence render services merge
independently, so omitted siblings are preserved. Generated artifacts and
private state are bounded to the same 4 MiB read limit before write. Rejection
memory retains the most recent 4096 distinct hashes.
Suggestion output names require `.atl-suggestion.json`; revalidation observation
outputs require `.atl-observations.json`. These reserved non-state suffixes plus
one held parent-directory handle prevent collisions and check/write redirection;
the parent itself must be mode 0700 or stricter.

`atl profile suggestion review --from-file SUGGESTION` emits
`{suggestion_hash,previously_rejected,evidence?,preview}` where `preview` is the
same exact profile-preview contract above. `suggestion apply` requires
`--suggestion-hash`, `--candidate-hash`, and `--expected-current-hash`, returning
`{suggestion_hash,profile}` with the normal apply result nested under `profile`.
`suggestion reject` returns `{suggestion_hash,status:"rejected",changed,path}`;
its owner-only decision file retains hashes only. Content/hash mismatch is exit
8 and base/current profile mismatch is exit 5.

`atl profile revalidation status --stale-before RFC3339 [--service ...]` emits
`{profile_hash,stale_before,entries}`. Entries contain
`{service,id,name?,status,verified_at?,last_checked_at?,source?,error?}` and status
is `fresh|stale|verified_pending|missing|failed`. `atl profile revalidate
--from-file CHECKS --out OBSERVATIONS` emits
`{path,observations_hash,base_profile_hash,entries}`; immediate check-result
entries use `verified|missing|failed`. It records at most the 1000 newest checks
per service in private state, writes verified facts to a version-1 observations
artifact, and never changes or deletes a profile fact. Persisted failure
summaries reject controls, redact network locations, and are length-capped.
