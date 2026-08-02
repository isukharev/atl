# `atl` Output Contract

This document is the authoritative reference for how `atl` communicates results and failures.
It is derived from `internal/cli/root.go` (`codeFor`, `emit`, `emitID`, `writeError`, exit constants).

---

## Output formats

`atl` accepts a global `-o` / `--output` flag (default `json`). The three modes:

| Mode | Flag | What is written to stdout |
|---|---|---|
| **json** | `-o json` (default) | Indented, HTML-unescaped JSON; one object per command |
| **text** | `-o text` | Human-readable text for commands with an explicit text projection; unsupported commands return exit 2 before config, stdin, or network access and never emit JSON |
| **id** | `-o id` | Primary identifier(s) one per line (issue keys, page IDs, attachment IDs) — for safe piping into `xargs`. Only commands that register an id projection support this; others return exit 2 before config, stdin, self-update, or network access |

Shell completion for the three values is registered on the root flag.

### `emit()` — JSON / text output

`emit(cmd, v, textFn)` is the standard result renderer:

- With `-o json`: writes `v` as indented JSON to stdout. HTML escaping is disabled (`&`, `<`, `>` pass through literally).
- With `-o text` and a non-nil `textFn`: calls `textFn()` and writes the result to stdout.
- With `-o text` and a nil `textFn`: returns exit 2 as a defensive backstop;
  the command-tree preflight normally rejects unsupported text before `RunE`.
- With `-o id`: returns exit 2 (usage error) — use `emitID` for commands that export identifiers.

### `emitID()` — JSON / text / id output

`emitID(cmd, v, textFn, idsFn)` extends `emit` with an id projection:

- With `-o id`: calls `idsFn()` and prints each returned string on its own line. No JSON envelope.
- With `-o json` or supported `-o text`: delegates to `emit` (same rules as above).
- Commands that have no meaningful identifier set `ids = nil`; `emitID` then returns exit 2 for `-o id`.

### Explicit created-object registration

`conf page create`, `conf page copy`, and `jira issue create` preserve their
legacy output and remote-only behavior when `--register` and `--into` are
omitted. The two flags must be supplied together. In default JSON mode, an
explicit registration adds this object to the ordinary created page/issue
result:

```json
{
  "registration": {
    "status": "registered",
    "root": "mirror",
    "path": "SPACE/page/page.csf",
    "version": 1,
    "sha256": "<sha256>",
    "readback_reconciled": true
  }
}
```

The envelope above shows only the additive member; the existing page fields
(`id`, `title`, `version`, `url`) or Jira issue fields remain alongside it.
`version` is present for Confluence and omitted for Jira. `path` is relative to
`root`. The digest, version, path, native file, pristine base, derived view, and
sync/view state are derived from one authoritative post-create readback, never
from the submitted body or the create response. Local artifacts and the base
are written and verified before sync state is saved.

After a known remote success followed by a readback, collision, or local commit
failure, stdout still identifies the created object. JSON uses
`registration.status:"not_registered"`, `readback_reconciled:false` until a
readback has qualified the object, a stable `reason`, and recovery text when an
identifier is available. The command then emits its normal structured error on
stderr and exits `8`. `-o id` for `conf page copy` and `jira issue create` still
prints the identifier before that non-zero exit; Jira `-o text` still prints
`created <KEY>`. This is not authorization to replay the non-idempotent create.
Preserve local files and use the reported narrow `conf pull --id ... --into ...`
or `jira pull --jql 'key = ...' --limit 1 --into ...` recovery.

### Mirror backend binding

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

### Maintainer-only private workspace migration

The repository's `agent-eval` maintainer tool is outside the shipped `atl`
command tree, but its migration output is also a stable privacy boundary.
Previewing `agent-eval private migrate` emits only this content-free JSON shape:

```json
{
  "schema_version": 1,
  "status": "ready",
  "from_schema_version": 3,
  "to_schema_version": 4,
  "source_sha256": "<hex>",
  "candidate_sha256": "<hex>",
  "migration_sha256": "<hex>",
  "preserved_run_sets": 2,
  "preserved_spec_references": 3,
  "preserved_run_records": 4
}
```

`status` is `ready` for an ordinary preview. The apply result uses the same
schema version, source/target versions, and migration digest with status
`migrated`; an exact interrupted dual-manifest, staged-source, or archived-source
transition returns `recovered`.
After flag parsing, migration-operation errors contain a closed reason code and
never include paths, run-set aliases, case identities, reviewer identities,
models, pricing, or source content.
Apply requires `--expected-migration-sha256` and `--confirm MIGRATE`.

### Qualified Confluence search page

`atl conf search` returns
`{schema_version:1,query,results,count,complete,truncated,partial_reason?,next_cursor}`.
`complete:true` requires a qualified terminal backend page: no continuation
cursor and no pagination anomaly. Legacy/unqualified stores remain
`complete:false`, even with an empty cursor. `-o text` carries the same signal
above a Markdown candidate table; `-o id` remains page ids only. Agents must
continue a cursor or disclose partial search before making an absence claim.

### Advisory Cloud-compatibility validation

`atl conf validate --cloud-compat` is opt-in and purely additive. Without the
flag the result object is unchanged: `{file, ok, problems}`. With it, the object
gains `cloud_compat:{rule_pack:"v1",source_date:"2026-07-25"}` and `problems[]`
gains `cloud-compat/*` entries appended in document order after the default
diagnostics. Every such entry has severity `"warning"`, so the flag can never
change `ok`, the push gate, or the command's exit status.

The v1 rule set is closed: `cloud-compat/macro-not-insertable`,
`cloud-compat/macro-view-only`, `cloud-compat/macro-removed`,
`cloud-compat/nested-bodied-macro`, and `cloud-compat/nested-table`. The macro
category is carried by `rule`, never by the message prose, so a client branches
on the rule name. `rule_pack` identifies the frozen taxonomy and `source_date`
the day it was reconciled against the official Atlassian Cloud editor and
macro-removal documentation; treat both as the version handle for any stored
finding. Only macro keys named explicitly on Atlassian's official compatibility
list are classified; an unlisted marketplace app, user, or unknown macro is
never guessed at. No finding asserts that a migration will or will not succeed.
A body that is not well-formed
short-circuits before the pack runs: the result keeps `cloud_compat` and the
well-formedness error but carries no `cloud-compat/*` entry, which is not
evidence of Cloud compatibility. The command converts nothing, calls no backend,
and writes no files.

Default validation can also return one blocking `max-depth` problem when CSF
nesting exceeds 1024 elements. This structural guard runs before recursive
consumers and reports only the observed depth and limit.

### Capability catalog

`atl capabilities` is an offline, deterministic routing contract. JSON is
`{schema_version:1,routing:{match,reference_load,stop},selection:{task?,service?,access?,id?,count},capabilities:[...]}`.
Each capability contains stable `id`, exact `task_class`, `service`, ordered
`role`/`priority`, `summary`, command path without the `atl` prefix, derived
`access`, derived `output_modes`, `evidence`, `completeness`, and a one-hop
`skill`/`reference` route. `-o text` is a Markdown table and `-o id` emits only
capability ids. The command reads neither config nor credentials and performs
no self-update or backend request. `routing.reference_load` tells an agent to
invoke the named skill first and resolve the reference relative to it; a
filesystem search is deliberately outside the route.
The additive schema-v1 transport fields preserve `command` as an alias of
`cli_command`, set `cli_only` as the inverse of a present `mcp_tool`, and
require a non-empty `mcp_scope` for every mapping. A mapping means the typed
read is sufficient only within that stated bounded projection; it is not full
CLI equivalence. Text and id output remain unchanged.
For `jira/structure-planning`, the ordered catalog exposes hierarchy rows,
explicit Structure values with `completeness:"per-row"`, and transient issue
export as separate capabilities.
For `jira/edit`, `jira.issue.worklog.list` exposes the complete baseline and
`jira.issue.worklog.add` routes to the guarded preview/apply command with
`evidence:"hash-bound"` and `completeness:"reconciled"`; catalog entries do
not themselves grant write authority.
For `confluence/comments`, the ordered additive route exposes qualified list,
exact thread expansion, guarded preview, and guarded add as separate
capabilities. Only list and thread map to the read-only MCP surface; preview
and add remain CLI-only, and catalog entries do not grant write authority.

### MCP tool results

`atl mcp serve` is a separate stdio protocol transport, so global CLI output
flags and process exit envelopes do not apply to individual tool calls. Each of
the twenty-three registered tools has inferred input/output JSON Schema and returns
typed `structuredContent`; compatible clients may also expose the SDK's text
projection. Tool failures set the MCP error result and contain a JSON text
object with stable `kind`, `remediation`, diagnostic `message`, and versioned
`recovery` fields.
Input rejected by a registered tool's JSON Schema is a tool failure with
`isError:true`, absent `structuredContent`, and one JSON text block containing
the static, value-free `usage_error` envelope. SDK validator prose and
caller-supplied property names or values are never returned, and validation
constructs no backend. Unknown tools and malformed outer requests remain
JSON-RPC protocol errors. Schema-valid application failures retain their
existing tool-specific envelopes.

`atl mcp serve --service jira|confluence|offline` selects one closed reviewed
inventory. Omission preserves the default twenty-three tools and instruction bytes.
Every profile exposes the fixed `application/json` resource
`atl://capabilities`; its static schema-v1 content contains capability
identity, task class/service/role/priority, CLI command, optional MCP tool/scope, and
CLI-only state. The resource accepts no parameters and performs no config,
credential, backend, mirror-path, or content read.
For transport/API failures, `message` is deliberately coarse and omits backend
paths, query values, and response bodies.

The recovery object is shared with CLI JSON and has
`{schema_version:1,action,retry_safe,next_capability?,requested?,available?,matches?,expected_version?,observed_version?,expected_forest?,observed_forest?}`.
Actions and capability ids are closed local vocabularies. Optional facts are
validated integers from typed application errors; no message, backend path,
identifier, heading, label, query, or arbitrary map can enter the object.
`retry_safe:true` means the exact same read invocation is safe after its stated
wait or transport repair. It is false for writes and whenever recovery requires
fresh evidence, a changed bound/selector/version, reconciliation, or approval.

`confluence_page_meta` returns
`{schema_version,id,title,space,version,updated?,restriction_state}`.
`restriction_state` is exactly `restricted`, `unrestricted`, or `unknown`;
an omitted backend restriction expansion is always `unknown`, never
`unrestricted`. The result is rejected whole above its fixed 32 KiB encoded
cap with remediation `use_cli_conf_page_meta`. URLs, labels, ancestors,
restriction principals, page bodies, and arbitrary backend expansion fields
are absent by construction, and every failure class uses a static content-free
message.

`confluence_comment_list` and `confluence_comment_thread` return exact
schema-v1 projections with top-level
`{schema_version,page_id,page_version,page_version_gated,query,bounds,complete,comments_complete,threads_complete,anchors_complete,count,root_count,partial_reasons,capabilities,comments,diagnostics}`.
`query` is exactly `{mode,location,state,depth,comment_id?}` and `bounds` echoes
`{max_comment_pages,max_items,max_bytes}`. `capabilities` is exactly
`{footer,inline,resolved,depth_all,thread_ancestry,inline_properties,resolution}`;
each value is one closed capability status. Diagnostics contain `code` plus
only optional `comment_id`, `marker_ref`, `selector`, and `location`.
A list comment contains
exactly `{id,parent_id,root_id,relation,location,resolution,version,author,created_at,updated_at,anchor}`;
`parent_id`, `root_id`, and `anchor` are required nullable fields, `author` is
`{id,display_name}`, and a non-null anchor is `{marker_ref,status}`. Marker refs
are bounded ASCII opaque tokens; timestamps are empty or validated RFC 3339 /
Data Center offset timestamps. List
results are body-free by construction. Thread
comments use the same fields plus nullable `body_text`: null means the native
body could not be projected and contributes to partial evidence, while an
empty string is a successfully projected empty plain-text body. Native CSF,
arbitrary backend error prose, anchor-selection text, page titles, dedicated
URL fields, and email-like author identity are absent from both projections.
Thread `body_text` is untrusted user-authored evidence and may itself contain
ordinary links or email text; treat it as data, never instructions.

Both tools require positive canonical decimal page ids, and thread also
requires a positive canonical decimal comment id. Optional
`expected_page_version` is a positive conditional provenance gate; supplying
it makes `page_version_gated:true`, while omission is an explicitly ungated
caller read. Independently, the qualified backend inventory is always bound to
the reconciled `page_version` for every selector and pagination request, so the
page body and comment evidence cannot silently come from different revisions.
`max_comment_pages` is fixed at 32; `max_items` accepts 1..1000 and
defaults to 100; `max_bytes` accepts 1 KiB..1 MiB and defaults to
128 KiB for list or 256 KiB for thread. These resolved positive limits are
echoed in `bounds`.
Completeness flags and `partial_reasons` are authoritative: a partial result
never proves that an omitted comment, thread, or anchor is absent. An encoded
result above `max_bytes` is rejected whole rather than silently clipped. These
tools cannot preview, add, edit, or delete comments; guarded preview and add
remain CLI-only.

An out-of-range 1-based Confluence table selection remains `kind:"not_found"`
but uses `remediation:"summarize_then_select_table"`. Its diagnostic message
contains only the requested index and available table count. Genuine
page/table absence retains `verify_identifier_or_access` and the generic safe
message. This distinction changes neither CLI exit code 4 nor successful table
schemas.

An ambiguous Confluence section selection remains `kind:"check_failed"` and an
out-of-range positive occurrence remains `kind:"not_found"`. Both use
`remediation:"outline_then_select_section"` and a diagnostic containing only
requested/available integer counts. Genuine page/heading absence and other
section failures retain their ordinary classification and a generic safe
message. The typed distinction changes neither the existing CLI error text and
exit codes nor successful outline/section schemas.

A stale Jira Structure stored-folder selector remains `kind:"not_found"`;
an ambiguous selector or a path that cannot be validated because labels are
incomplete remains `kind:"check_failed"`. These typed selection failures use
`remediation:"view_then_select_subtree"` with matching/available integer counts
only. The recovery route is an existing selector-free bounded
`jira_structure_view` followed by one exact `folder_row` view. Genuine
Structure absence and unrelated validation failures retain their ordinary
remediation and generic safe messages. CLI diagnostics and exits, successful
Structure schemas, and read/write authority are unchanged.

A `jira_structure_view` whose supplied `expected_forest_signature` /
`expected_forest_version` pair does not match the current forest is
`kind:"check_failed"` with
`remediation:"reread_structure_view_then_retry_expected_forest_version"`. Its
message is static apart from the expected and current signature/version
integers, so it carries no row, folder, issue, or backend content. Recover by
re-reading the view, re-selecting the subtree from that fresh result, and
requesting it once with the new pair. Other Structure `check_failed` results keep
their existing remediation.

`jira_fields`, `jira_issue_search`, `jira_issue_history`, `jira_issue_refs`,
`jira_epic_digest`, and `jira_board_view` reject a final encoded result larger than `max_bytes`
(default 256 KiB, minimum 1 KiB, maximum 1 MiB). Row/source limits and compact
projections remain independent semantic bounds; exceeding the byte bound is an
explicit `check_failed` result and never silently clips the typed output.

`jira_issue_history` requires `key` and returns exactly the summary projection
described for `atl jira issue history --summary-only`:
`{key,complete,source,total,fetched,count,partial_reason?,filters,summary,
last_changes?}`. The top-level `history` member is absent by construction, and
the tool exposes no raw-changelog selector or projection mode. Optional
repeated `fields` and inclusive `since`/`until` boundaries are forwarded
unchanged to the same application read, so the MCP result carries the identical
resolved `filters`, deterministic `summary`, and selected-field `last_changes`
as the CLI projection. Technical field ids are resolved locally, while any
display-name selector adds one Jira field-catalog request before the changelog
read. Explicit timestamp boundaries need no metadata lookup; one or more civil
dates add one current-user timezone request. These two metadata requests are
independent. The byte bound is applied to the projected result, so a rejected
oversize result never contains raw history rows. CLI flags, exits, and the full
raw-history contract are unchanged.

`jira_issue_refs` accepts exactly one issue `key`, or `jql` with a required
`limit` from 1 through 25, plus at most eight exact technical field ids. It
returns a closed schema-v1 projection:
`{schema_version,count,complete,truncated?,selection,summary,warnings?,issues}`.
Each issue contains only
`{key,complete,truncated?,sources,reference_summary}`. The input key/JQL echo,
issue summary/type, source text, and `refs` array with raw URLs are absent by
construction. The projection is made before validation and `max_bytes`
enforcement, so URLs cannot appear in a successful result or an oversize
diagnostic. All existing selection, source, per-issue, and top-level
reconciliation facts are preserved and checked without changing the full CLI
contract. JQL mode performs one paginated comment listing per emitted issue, so
backend traffic scales linearly with the selected limit.

`jira_issue_search` selects ordered returned fields with `columns` (preferred),
`fields`, or `projection`; the latter two are compatibility aliases. At most
one selector may be non-empty, and empty arrays are omitted. The returned
IssueList carries normalized `projection` metadata independently. Unknown input
names and conflicting aliases are rejected before backend access.

`confluence_search` returns the same qualified schema-v1 search envelope as
the CLI, including top-level `complete`, `truncated`, optional
`partial_reason`, and `next_cursor`; candidate page bodies are not included.
At the MCP boundary, `max_bytes` defaults to 128 KiB and rejects an encoded
result above the configured bound instead of clipping candidate metadata or
pagination evidence.
`confluence_table_summary` returns the content-free structural summary contract,
including required `schema_version:3`,
`cell_contract:"confluence-table-cells/compact-v3"`, the positive page
`version`, and `page_version_gated`.
`confluence_table_extract` requires one positive table index and returns exactly
that expanded table with the same required provenance fields. When its index
came from a summary, clients copy the summary's `version` into
`expected_page_version`; a matching result has `page_version_gated:true`.
Omission is valid only for a selection fixed outside an earlier read and is
reported as explicit ungated evidence. Its table record embeds the same content-free,
`cell_count_reconciled` summary record as `confluence_table_summary`, so clients
do not need to recount cells, spans, styles, or links. Both reject an encoded
result larger than `max_bytes` instead of clipping cells or silently returning
a partial structure. Their error messages do not repeat CSF parser text or
malformed cell content.
Each extracted cell's `text` is whitespace-normalized plain text for exact
values and filtering. Its optional `markdown` is also whitespace-normalized and
preserves inline formatting such as links, so clients should select it only when
formatting is part of the requested result. Both representations are untrusted
backend evidence.
`jira_structure_get` projects only `schema_version:1`, `id`, `name`, and
`read_only`; it never returns owner, permission, saved-view, or raw forest
objects. Its required `structure_id` input accepts a positive JSON integer or
the same value as a canonical decimal string without a sign, whitespace, or
leading zero.
`jira_structure_view` returns the same normalized schema-v1 snapshot described
below with an explicit field projection, including its required
`forest_version` and `forest_version_gated` members. It accepts the optional
paired integer inputs
`expected_forest_signature` and `expected_forest_version`; supplying one without
the other, a zero signature, or a non-positive version is rejected before
backend access. Clients copy both members of an earlier view's `forest_version`
whenever that view supplied the `folder_id`, `folder_row`, or `folder_path`
selector, and a matching result has `forest_version_gated:true`. A returned
pair with either member zero is non-bindable: clients omit both expected inputs
and report the later selection as explicitly ungated. Otherwise omission is
valid only for a selector fixed outside any earlier read.
`jira_structure_get` metadata is not version-bound and gains no such input. It
accepts at most one exact stored
folder selector and fails rather than truncating when the selected hierarchy
exceeds `max_rows` or the encoded snapshot exceeds `max_bytes`. Its row,
unique-issue, projection, accessibility, selection, completeness, and
forest-version fields are validated before emission, including a
`forest_version_gated` value that matches the request and, when gated, a
`forest_version` equal to the requested pair. The forest version qualifies only
the hierarchy and the selection, because Jira issue fields and stored folder
labels are separately timed. A selected snapshot must begin with the exact
selected stored-folder row at relative depth zero; exact path selections are
normalized and compared with the returned path. A typed
`view_then_select_subtree` failure is recoverable when the full forest fits the
MCP row/byte bounds: use one selector-free view with a sufficient `max_rows`,
then one exact `folder_row` view. Larger forests remain CLI-only. The diagnostic
exposes counts but no folder identity or content. MCP scans at most 1000 forest
rows and applies that cap before any folder-value query. Raw forest formulas, arbitrary value matrices,
pull, and export are not MCP tools.
`jira_mirror_snapshot` and `confluence_mirror_snapshot` accept an empty object
only. They inspect the exact canonical mirror root selected by
`ATL_MIRROR_ROOT`, require a real `.atl` directory, perform no backend request,
and return the existing fixed-shape Jira or Confluence snapshot contract without
paths, item identities, or content. Local integrity findings are represented by
the returned `complete` and reconciled bucket fields whenever a snapshot can be
formed; root/configuration failures remain classified tool errors. Both always
return `remote_requested:false`.
Unrestricted output properties use the JSON-Schema object form `{}` rather
than the equivalent boolean `true` for broad MCP-client compatibility.

The stable classes come from the same transport-neutral classifier used by CLI
JSON. Clients must branch on `kind`, not parse `message`. Stdout from the server
process is reserved for MCP protocol frames; operational failures are returned
through the protocol rather than mixed into successful tool content.

### Error output

On failure `atl` writes to **stderr**, never stdout, so a piped JSON result on stdout is never
contaminated. The format follows `-o`:

- **`-o json` (default):** `{"error":"<message>","code":N,"kind":"<stable-kind>","remediation":"<stable-action>","recovery":{...}}` (one JSON object, newline-terminated).
- **`-o text`:** `error: <message>`.

The existing `error` and `code` fields remain compatible. `kind` is always
present; `remediation` is deterministic guidance, not an instruction to execute
automatically. Both are derived from local sentinels/typed metadata, never by
parsing backend prose. Current exit classes map to `unexpected_error`,
`usage_error`, `authentication_failed`, `not_found`, `version_conflict`,
`forbidden`, `configuration_error`, and `check_failed`. Typed specializations
include `read_only_policy`, `transport_error`, `rate_limited`,
`output_limit_exceeded`, and `api_error` without changing their exit code.
`recovery` is an additive schema-v1 object shared with MCP. Its closed `action`
may be more precise than the compatibility `remediation`; `retry_safe` refers
only to replaying the exact same invocation, not to the safety of an entire
multi-step recovery workflow. Selection/version facts are emitted only after
their numeric invariants validate, otherwise recovery falls back without facts.
`rate_limited` uses
`remediation:"wait_before_retry"` after the bounded replay-safe read retry
policy is exhausted; it never authorizes an immediate repeated request or a
write retry. `output_limit_exceeded` uses
`remediation:"narrow_or_raise_bound"` when an otherwise valid encoded MCP
result exceeds the caller-selected `max_bytes`; the rejected result is not
partial evidence. A missing command registration invariant is
`internal_error`/`report_bug` (still exit 8), not a user check failure.

### Binary identity

`atl version` returns the stable object
`{version,commit,build_state}`. `commit` is a full source revision or
`"unknown"`; `build_state` is `"clean"`, `"dirty"`, or `"unknown"`.
Supported Makefile and release builds stamp both values, while an ordinary Go
build may use compiler VCS metadata. The object has no build timestamp and is
informational only: it is not an input to self-update or signature trust.
`atl version -o text` remains the bare version, and `atl --version` retains its
existing one-line Cobra form.

### Setup doctor

`atl doctor` returns a schema-v1, content-free aggregate with
`{schema_version,mode,complete,healthy,status,cli,runtime,config,credentials,
safety,services,mirror,plugin,problems}`. Closed status/reason/remediation
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

### Exact compatibility-provider status

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

## Sentinel → exit-code matrix

Adapters wrap domain conditions as `fmt.Errorf("%w: ...", domain.ErrXxx)`. The CLI's `codeFor`
maps them via `errors.Is`:

| Exit code | Constant | Sentinel | Meaning |
|---|---|---|---|
| `0` | `exitOK` | — | Success |
| `1` | `exitGeneric` | (default) | Unexpected error; read the message |
| `2` | `exitUsage` | `domain.ErrUsage` | Bad flags/args; flag-parse errors are also mapped here |
| `3` | `exitAuth` | `domain.ErrAuth` | Server **rejected** the token (expired/revoked/wrong instance) |
| `4` | `exitNotFound` | `domain.ErrNotFound` | Resource does not exist or is not visible |
| `5` | `exitVersionConfl` | `domain.ErrVersionConflict` | Confluence push: remote moved past synced version |
| `6` | `exitForbidden` | `domain.ErrForbidden` | Authenticated but lacks permission for this object |
| `7` | `exitConfig` | `domain.ErrConfig` | Invalid/incomplete configuration, including a missing backend URL/PAT or invalid named view |
| `8` | `exitCheckFailed` | `domain.ErrCheckFailed` | A check or safety precondition failed, including read-only policy refusal |

### Practical notes

When read-only policy blocks a mutation, the normal JSON error envelope keeps
`error` and `code:8` and adds stable `policy:"read_only"` plus the full
`command` path. The values come from typed local policy metadata, never backend
text. Its recovery action requires human approval and is never retry-safe. Text
output remains one concise `error:` line.

This additive stderr/MCP error schema does not change a successful result or
any Confluence/Jira mirror-derived document. No durable document-format marker
is bumped.

- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token configured);
  `3` = "the token you gave me was refused." React differently: `7` → run `/atl:setup`;
  `3` → replace the PAT via `auth login`.
- Codes `3` vs `6` are distinct: `3` = authentication failure (re-auth); `6` = authorization
  failure (the identity is known but lacks permission — surface to the user).
- **Only Confluence `push` uses the version gate** (`5`). Jira writes are last-writer-wins; `5` is
  never returned from Jira commands. `jira push` guards staleness with an app-layer
  compare-and-swap instead: a drift refusal is exit `8` (`ErrCheckFailed`), not `5`. A server-side
  HTTP 409 on a Jira write (locked issue, workflow veto) stays a generic conflict (exit `1`), also
  distinct from `5` (#66).
- Error-severity CSF validation failures are one gate contract across
  `conf validate`, `conf push`, `conf page create`, and `conf blog create`:
  `ErrCheckFailed` / exit `8`, `kind:"check_failed"`,
  `remediation:"review_failed_check"`, and the established closed recovery
  `{action:"inspect_failure",retry_safe:false}`. Existing command result
  objects and `problems[]` remain on stdout. An uncontended local push snapshot
  rejects invalid content before backend construction; an active mirror
  mutation retains the existing lock/config error precedence. Invalid content
  never reaches the network. This covers malformed XML and unsupported nesting
  depth. `--cloud-compat` adds only `"warning"`-severity findings, so it never
  changes this exit status.
- `jira issue check` exits `8` (`ErrCheckFailed`) when a field listed in `--require` is empty — a
  distinct code so a CI gate can tell "fields missing" from a transport/auth error. The full result
  (including `missing_required` and `missing_warn`) is still emitted to stdout before the exit.
- Flag-parse failures (unknown flag, bad value) are wrapped as `ErrUsage` → exit 2.
  This is enforced by a `SetFlagErrorFunc` on the root command, so it applies to every subcommand.
- Every public group, leaf, and intentional group/leaf hybrid is part of one
  exhaustive command registry. A pure group with no arguments prints help and
  exits 0; an unknown child or stray positional token is `ErrUsage` → exit 2
  before configuration, stdin, self-update, or network access. Every mutating
  leaf also declares its mutation profile and any profile-specific guard flags.

---

## `--verbose` / `ATL_VERBOSE=1`

When set, `httpx.SetTrace` attaches a request/response logger to stderr before any HTTP call.
The bearer token and query values are **never** written to the trace (query parameter names remain
visible with redacted values). stdout stays reserved for the result, so verbose output never
corrupts the JSON stream. HTTP API error strings use the same query-value redaction and omit URL
fragments, so a failed request does not reintroduce JQL/CQL/selectors through stderr.

---

## Stable Snapshot Notes

`atl jira issue view <KEY>` is the non-persistent counterpart to a mirror view.
It writes no files and emits `{"key":<KEY>,"markdown":<configured-view>}` by
default; under `-o text`, stdout is the exact raw Markdown string with no
emitter-added newline (matching `conf page view`). Advisory render
warnings remain on stderr. The selected render root is read only for its local
presentation config and gains no snapshot, sidecar, assets, or writeback state.
Consequently transient output cannot be applied or pushed: pull the issue fresh
before editing it.

`atl jira pull` writes three files per issue: `<KEY>.wiki` (the native Jira wiki body, byte-for-byte —
the editable substrate), `<KEY>.md` (a derived Markdown staging view rendered from the wiki and
regenerated best-effort on pull/render), and `<KEY>.json` (the raw-fields snapshot). The pull
result's `path` points at the `.md`; `wiki_path` points at the sibling `.wiki` substrate. To use the
friendly surface, edit generated `# Description` and/or field sections explicitly configured as
editable, then run `jira apply`. Description changes merge into `.wiki`; field changes become an
explicit `.atl/pending/jira/<KEY>.json` write set. The raw issue snapshot is not changed until a
successful push refreshes it. `.md` is never sent directly and a later pull/render can replace it. Edit
`.wiki` directly for constructs the staging view cannot express. Generated issue fields appear in a
read-only `# Metadata` Markdown table; update them through dedicated commands, not by editing the
table. A typed field section is editable only with `editable:true`, `placement:"section"`, and
`format:"jira_wiki"`; transient `jira issue view` output remains read-only. Generated regions carry hidden stable `atl:section`
markers; Jira rich-text headings are nested below their generated owner. Human-facing
datetime values are compacted to minute precision, while the JSON snapshot keeps
the exact raw server value. The JSON snapshot is an object with
stable identity at the top level and raw Jira fields under `fields`:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "status_id": "11",
  "fields": {
    "summary": "Issue summary"
  }
}
```

`--fields` on `jira pull` adds requested fields to that `fields` map; the command still includes the
core fields needed to render the markdown view and choose the project/key path.

The `jira pull` stdout summary is `{ "into": <root>, "issues": [ { "key", "path", "wiki_path", "status", "assets", "epic_children" }, ... ] }`.
`status` is omitted on an ordinary successful pull; pull previews use
`would_pull`, while a preserved item uses `blocked`.
With `--assets`, each issue object gains an `assets` count of image attachments mirrored into
`<KEY>.assets/`, and the top-level result gains `assets_skipped` when some images could not be
downloaded. Both `assets` and `assets_skipped` are `omitempty`: a default (no `--assets`) pull, and a
`--assets` pull where nothing was skipped, produce the same shapes as before. The raw `<KEY>.json`
snapshot is never modified by `--assets` — it mirrors Jira's response and carries no local file paths.

When the opt-in `epic_children` render section is enabled, epic issue objects
gain an `epic_children` count (omitted at zero) and the mirror gains
`<KEY>.epic-children.json`:

```json
{
  "epic": "PROJ-1",
  "epic_field": "customfield_10001",
  "epic_selector": "Epic Link",
  "children": [
    {"key": "PROJ-2", "summary": "Implement capability", "status": "Open", "type": "Story"}
  ],
  "truncated": true,
  "truncated_at": 1000
}
```

`children` is always an array. The truncation fields are `omitempty`; when any
related query hits the cap, the top-level pull result also carries
`epic_children_truncated: true` and `epic_children_truncated_at: 1000`, and the
CLI warns on stderr. The sidecar is derived/offline-render data and never enters
the `.wiki` content hash or remote drift gate. Offline render/apply accept it
only when its epic key, configured selector (when present), and resolved epic
field match the issue/view affinity; otherwise it is ignored and render warns
to re-pull. `epic_selector` is omitted for auto-detection and retained for any
explicit configured selector (display name or field id), so changing that
selector cannot reuse a stale sidecar resolved from a different field.

**Render profiles and typed field views do not otherwise change the `pull`
JSON.** Profiles and ordinary include/exclude sections only affect the derived
`.md`; `epic_children` is the explicit exception because it reports related-data
counts/truncation as described above. Unknown section names in an
include/exclude list produce a `warning:` line on **stderr** and are ignored —
never an error, never on stdout.

`atl jira render [DIR|FILE] [--render-*]` and `atl conf render [DIR|FILE]
[--render-*]` regenerate `.md` views offline (no network/PAT). `jira render` emits
`{ "root": <mirror-root>, "rendered": [ { "key", "path" }, ... ] }`; `conf render`
emits `{ "root", "rendered": [ { "id", "title", "path" }, ... ] }`, one entry per
rewritten `.md`. Both leave the `.csf`/`.wiki`/`.json` substrate and the sidecar
`pages` sync entries untouched (they record each view's render settings,
including the presentation-only display timezone, typed field descriptors,
and the resolved epic field, in the
sidecar `views` map only, so a later `apply` can reproduce it), so `status` is
unchanged before and after. Render-resolution warnings go to **stderr**, never
stdout.

Every Confluence derived page view begins with
`<!-- atl:document confluence-page v6 -->` and has reserved generated
metadata/body/comments/Jira-query boundaries. `conf apply` rejects missing, legacy, or
unknown versions and additions/removals/renames/reordering in the reserved marker sequence inside
the editable body before any substrate write. Marker-looking prose already
present in the native page remains valid when unchanged. Pristine v5 and v4
views migrate only when their complete bytes match the exact renderer for that
marker. Dirty v5/v4, older historical, unversioned, and unknown/future views
are preserved and refused; future versions require an updated binary and must
not be downgraded.
V6 edits also treat unrepresentable native element attributes, table-cell
wrappers, inline breaks, and code-macro metadata as protected structure. Their
removal is reported through the existing fragment-loss contract; raw values
are represented only by content-free hashes.
The marker line may end in LF or CRLF. Atl strips only the CR attached to that
first line for version classification; remaining Markdown bytes stay
significant for dirty/edit/relocation checks.

JQL-bearing Confluence Jira macros keep a readable query placeholder in the
editable body and, when Jira read access succeeds, append a generated readonly
`# Jira Queries` suffix rendered by the shared IssueList Markdown table. Macro
columns override the selected named list view; otherwise the view's
`confluence_macro` projection is used. Pull persists a page/macro-hash-bound
`<slug>.jira-macros.json` snapshot so offline render and apply remain
byte-stable without network access. Per-query failures are bounded warnings and
leave placeholders; invalid or stale recorded enrichment is never merged into
CSF and makes apply fail closed pending a fresh pull. One page resolves at most
20 JQL macros and 2000 aggregate rows, with a 1000-row per-macro cap.
`render.confluence.jira_macros` and the per-run `--jira-macros auto|off`
override control whether page-provided JQL may execute. `off` is resolved before
Jira credentials are loaded, performs no Jira search, keeps placeholders, and
emits no query sidecar. The config key is global-only; mirror-local config is
untrusted for authenticated-read policy and cannot enable it. Post-push refresh uses the same sidecar-aware view
constructor as render/apply/relocation, preserving generated suffix bytes.
Read-only refusal diagnostics distinguish `# Jira Queries` from `# Comments`.

When `page_fields` is enabled, the read-only prefix contains
`<!-- atl:section page-fields readonly -->` followed by a `# Metadata` table and
optional `<!-- atl:section page-field.<id> readonly -->` sections. Descriptors
are stored with the view state so apply/push reproduce the exact prefix. Values
are single-line escaped plain text, not executable Markdown. `restricted` is
absent/unknown unless explicitly projected; offline render never converts
unknown into `false`.

The editable body begins visibly at `# Content`; native page headings retain
their original levels beneath that delimiter so Markdown-to-CSF identity is not
changed. A full view ends with readonly `# Comments`. Each comment is headed at
level two, and its native storage-format body is rendered with headings nested
below the comment. The comments sidecar retains both a plain fallback `body`
and optional `body_storage` CSF.

Native page links render as readable synthetic Markdown links whose destination
is `confluence-page:` plus optional space and percent-encoded title; explicit
labels stay separate from targets. Native colored spans render as protected
HTML color spans only for a closed inert CSS-color grammar; other values use a
non-styling `data-atl-color` marker, and literal inner HTML is escaped. Both
remain opaque byte-preserving merge markers. Apply's
loss report counts full page-link identity (space, target, label) and color
spans, so same-label links cannot hide removal of a different target.

The sibling Confluence `.meta.json` persists `ancestors` and `updated` when the
backend supplied them. `restricted` is present as a JSON boolean only when the
pull explicitly selected that descriptor; a narrower later pull removes it.

`atl conf blog create` emits
`{id,type,title,space,version,body_present,url}`. Success requires the expanded
POST response to prove a non-empty identity, exact `blogpost` type/space/title,
positive version, and present storage body. `-o text` is one compact
tab-separated record; `-o id` emits only the content id. Invalid/empty CSF and
unsupported/empty Markdown fail before the network. A successful POST with an
incomplete or mismatched response is exit 8 and explicitly an unknown creation
outcome; transport, timeout, throttling, and server failures after dispatch are
unknown for the same reason. None may be automatically replayed. Definitive 4xx
sentinels retain their normal exit mapping.

`atl conf page title set <ID>` is dry-run by default and emits
`{id,mode,status,current_title,title,title_bytes,title_sha256,current_version,
expected_version,final_version?,proposal_hash,reconciled?}`. Apply requires the
reviewed version and aggregate hash, reuses the fresh native CSF bytes unchanged,
and verifies title, body hash, and exactly `current_version+1`. Status is
`would_apply`, `already_satisfied`, `blocked`, `failed`, `applied`, or `unknown`.
`already_satisfied` is returned only after the reviewed version/hash gates pass.
Unknown is non-zero and must never be automatically replayed.

`atl conf page labels list <ID>` emits
`{id,labels:[{id?,prefix?,name,label?}],count,complete,truncated?}`. It follows
offset pagination to exhaustion; hitting a safety cap keeps the collected
prefix but sets `complete:false`, `truncated:true`, and writes a warning to
stderr. Text output is one `prefix<TAB>name` record per line.

`atl conf page labels add|remove <ID> <LABEL>...` emits
`{id,operation,mode,status,requested,current:[label-records],final?:[label-records],proposal_hash,complete,
reconciled?}` and is dry-run by default. The hash binds the page, operation,
normalized request, and complete current prefix/name set. Apply requires that
exact reviewed hash before `already_satisfied` or a write. Writes are sent once;
only `global` labels are mutation targets, while other prefixes remain visible
in the records. The final collection is re-read. Status is `would_apply`, `already_satisfied`,
`blocked`, `failed`, `applied`, or `unknown`; unknown is non-zero and must not
be replayed automatically.

`atl conf page move <ID>` is also dry-run by default and emits
`{id,mode,status,current_parent,parent,current_version,expected_version,
expected_parent,target_version,final_version?,proposal_hash,reconciled?}`.
Apply requires the reviewed source version, exact current parent (including an
explicit empty value for a top-level page), and proposal hash. It validates the
fresh source/target hierarchy, writes the unchanged native body/title once,
and verifies parent, body, title, space, and exactly `current_version+1`.
Proposal-hash schema v2 also binds `target_version`; apply re-reads the target
identity, version, space, and ancestor ids immediately before PUT and blocks if
they changed. This narrows but cannot eliminate the backend's two-page TOCTOU.
`unknown` is non-zero and must never be automatically replayed.
An already-satisfied parent still requires the reviewed source version, current
parent, and proposal hash before it can return success.

`atl conf page delete --id <ID>` is dry-run by default and emits the guarded
page-trash schema:

```json
{
  "schema_version": 1,
  "id": "12345678",
  "mode": "dry-run",
  "status": "would_apply",
  "operation": "trash",
  "current_status": "current",
  "target_status": "trashed",
  "observed_state": "current",
  "current_version": 7,
  "expected_version": 7,
  "body_sha256": "<sha256>",
  "body_bytes": 42,
  "title_sha256": "<sha256>",
  "backend_sha256": "sha256:<digest>",
  "proposal_hash": "<sha256>",
  "complete": true,
  "write_attempted": false,
  "warning": "Confluence has no delete-time version CAS; apply revalidates immediately before one status=current DELETE and never replays it"
}
```

Apply requires `--apply --confirm TRASH`, `--expected-version N`, and
`--expected-proposal-hash SHA256`. The hash binds schema/operation, normalized
backend identity, page identity/type/status/version, native-body hash and byte
count, title hash, space, and parent. Before one DELETE, ATL repeats that exact
snapshot; the DELETE is explicitly limited to `status=current`. Readback must
match the reviewed version exactly. `status` is `would_apply`, `already_satisfied`, `blocked`,
`not_applied`, `applied`, `recovered`, or `outcome_unknown`. A write-attempted
result sets `write_attempted:true`; an exact post-attempt state read sets
`reconciled:true` and may add `final_version`. `complete` qualifies the state
evidence, not write success. `outcome_unknown` is exit 8 and must not be
replayed; failure to emit stdout after a write attempt is also exit 8 with the
same no-replay rule.

`atl conf page view <ID>` is the non-persistent counterpart. Its JSON is
`{"id","title","space","version","markdown"}`; text output is the exact
Markdown string. It uses the same versioned renderer, but marks the body
`readonly`, writes no mirror or view state, and cannot be used as an apply/push
surface. Optional comments are fetched only when selected by the effective
render settings; truncation is warned on stderr. A fresh pull is required before
editing.

`atl conf page history --id <ID>` emits the qualified version listing
`{schema_version:1,page_id,count,complete,partial_reason?,versions:[...]}`.
`page_id` is the resolved content id the versions belong to. A successful
listing always uses a JSON array; a page with no recorded versions emits
`"versions":[]`, never `null`. `complete:true` means the backend version
listing was exhausted, so an empty array is proven absence. `complete:false`
always carries a static `partial_reason` from the closed set `page_limit`,
`item_limit`, `pagination_stalled`, or `legacy_unqualified`, and never proves
that an omitted version does not exist. Version records preserve `number`,
`when`, `by`, and (when present) `message`, and are validated to be strictly
newest-first with positive version numbers before emission. Invalid or
duplicate version records fail as a check error (exit 8) instead of weakening
the completeness claim. `-o text` still emits
`number<TAB>when<TAB>by[<TAB>message]` per line and is unchanged.

`atl conf attachment list --id <ID>` emits the qualified inventory
`{schema_version:1,page_id,page_version,count,complete,partial_reason?,
attachments:[...]}`. `page_id` is the resolved content id and `page_version` is
the version observed immediately before listing, so the caller can reject a
page-body revision mismatch without assuming that Confluence provides an
atomic page/attachment snapshot. A successful listing always uses a JSON array;
a page with no attachments emits `"attachments":[]`, never `null`.
`complete:true` means the backend listing was exhausted, so an empty array is
proven absence. `complete:false` always carries a static `partial_reason` from
the closed set `page_limit`, `item_limit`, `pagination_stalled`, or
`legacy_unqualified`, and never proves that an omitted attachment does not
exist. Attachment records are unchanged and still include `comment`. `-o id`
emits one attachment id per line and produces empty output for the empty
collection; `-o text` still emits `id<TAB>title<TAB><size> bytes` per line.

`--expected-version <N>` is an optional consistency gate: a positive value
refuses the listing with exit `8` unless the page is currently at that version,
before any attachment request is issued, and reports only the expected and
current integers. `0` (the default) disables the gate; a negative value is a
usage error (exit `2`).

Confluence pull/render/apply/push and mirror-local `conf edit` acquire one persistent mirror-internal
advisory lock for their complete mutation/preview critical section. Contention
is exit `8` before page/state writes. The file persists so every process locks
the same inode; process exit releases ownership. Read-only status is lock-free.
Jira retains its own workflow lock, while both services additionally merge
sidecar patches under the shared `.atl/state.lock`; cross-service state
contention is retried for a brief fixed window, then fails closed and cannot
lose unrelated entries.

`atl conf snapshot [DIR | --into ROOT]` emits the content-free aggregate contract
`{schema_version:1,service:"confluence",remote_requested,complete,reconciled,
local,native,validation,render,remote}`. It intentionally omits root/target,
page identity, title, path, hashes, validation messages, and body/view bytes.
The offline default requires no config or credentials and performs no network
or filesystem writes. Local inspection shares the persistent mutation lock when
it exists. Contention returns a content-free exit `8` before inspection. If a
legacy mirror has no lock yet, the command verifies that no current writer
created it during the read and discards/retries the first result if one did.

`conf status` and `conf snapshot` accept either positional `[DIR]` or
`--into ROOT`, not both. Selection order is the explicit form,
`ATL_MIRROR_ROOT`, the nearest initialized `.atl` from the current directory,
then `mirror`. A missing or non-directory marker is `ErrNotFound`/exit 4 before
remote setup and produces no result object.

`local` partitions `present` into `clean|locally_edited` and
`tracked|untracked`, with `non_canonical` as an explicit untracked subset.
`native` repeats the closed `conf diff` state cardinalities and separately
partitions baselines into `baseline_present|baseline_missing|
baseline_unreadable`, then present baselines into
`baseline_valid|baseline_invalid`. `validation` partitions every native target
into present/absent candidates and every present candidate into valid/invalid;
`unreadable` qualifies inspection failures without exposing their text.

`render` partitions every present native page into present/missing/unreadable
views, then present views into `current|legacy|missing_marker|unsupported`.
Recorded/missing view-state counts form a second exact partition.
`renderer_compatible` is false for unsupported/future or unreadable views. It
is only a format-compatibility statement, not proof that rendering would
preserve edits, and never causes an automatic render. With `--remote`, `remote`
partitions all present local pages into attempted/not-attempted; attempted pages
must be an eligible tracked canonical subset. It then partitions attempts into
checked/unavailable and checked results into in-sync/drifted. One metadata probe
is started per attempted page with generic replay-safe transport retries
disabled. Redirect responses are not followed because a second hop would exceed
the one-attempt bound; they count as unavailable. Without `--remote`, all pages
remain not attempted.

Every nested `reconciled` proves its declared equations and top-level
`reconciled` requires all of them. `complete` is evidence availability, not a
health or publish decision: it becomes false for incomplete native comparison,
unreadable views, or requested unavailable remote evidence. Corrupt baseline
evidence preserves the qualified stdout contract and exit `8`. Any incomplete
local evidence stops before remote configuration, credential resolution, or the
first probe. If that qualified aggregate cannot be written to stdout, the write
failure is reported together with the inspection failure and the exit code
stays the inspection classification. If inspection otherwise succeeds, the
write failure is returned on its own with generic exit `1`.

`atl conf diff [file.csf|DIR]` is an offline, lock-free comparison with
`schema_version:1`. Its top-level contract is
`{schema_version,root,target,complete,summary,pages}`. Pages are sorted by path
and carry `{id?,title?,path,state,baseline,candidate,semantic_changed?,byte_only?,blocks?,features?,byte_evidence?}`.
`root` and `target` are canonical absolute path identities. The closed `state`
set is `unchanged|added|removed|modified|malformed|missing_baseline|
baseline_mismatch|unreadable`; the summary includes optional
`baseline_mismatch` when non-zero without changing valid v1 plan bytes.
The `-o text` projection keeps the same complete/summary qualification and a
path-ordered Markdown table with `State`, `Page`, mirror-root-relative `Path`,
`Review`, and `Deltas`. `Review` is `semantic` for understood content/feature
changes, `byte-only` for native-byte-only differences, `none` for unchanged
pages, and `n/a` for states that cannot be compared semantically. `Deltas` is
the number of block plus feature deltas; it is not a substitute for `Review`.
The two sides expose only presence, byte length, SHA-256, validity, and
validation diagnostics; block changes expose kind/index/fingerprints rather
than page text. Byte evidence identifies the exact common prefix/suffix and
hashes each changed window. `complete:false` means semantic comparison was not
fully available for at least one page. A scan never treats unreadable or corrupt
mirror state as an empty/clean subtree. `baseline_mismatch` distinguishes a
pristine base whose bytes disagree with its tracked sync hash from filesystem
unreadability.

`conf reconcile preview <page.csf|page.md>` and `conf reconcile stage ...`
emit schema-v1 content-free three-way evidence:
`{schema_version,service,mode,complete,reconciled,id,path,base_version,
remote_version,proposal_hash,base,ours,theirs,classification,block_summary,
blocks,local_changes?,remote_changes?,bounds,artifacts?}`.
`classification.state` is the closed set
`unchanged|local_only|remote_only|diverged`; exact equal concurrent changes use
`unchanged` with `converged:true`. `reconciled:false` means the exact whole-body
comparison diverged. Each side exposes only bytes/SHA-256/validity. Stage-only
artifact paths are mirror-relative and point beneath `.atl/reconcile`; stage
never changes the working substrate or pristine baseline. Both modes use one
single-attempt GET after local qualification. Bound or evidence failures emit
no success contract and return exit `8`.
The stage-only artifact object includes explicit manual cleanup guidance; ATL
never removes either file automatically.

Each block row classifies one deterministically aligned semantic region with
base/ours/theirs start, count, and hash evidence but no content. Region state
uses the same closed set; `block_summary` reconciles its cardinalities. The two
base-to-side change lists remain a compact pairwise projection. Aggregate LCS
allocation is capped before construction.

`jira reconcile preview <issue.wiki|issue.md>` and `jira reconcile stage ...`
use the same base/ours/theirs and classification contract, with
`{id,key,updated}` instead of page versions and an optional sorted `fields`
array for pending native-wiki fields. Every field repeats three content-free
sides plus its exact classification. The proposal hash binds Description,
fields, local path, and fresh remote identity/updated marker. Stage materializes
only Description base/theirs artifacts and never rewrites pending fields.
Jira `bounds` additionally declares the 64 MiB serialized pending-record and
256-field aggregate caps; these are distinct from the 16 MiB per-native-value
cap.

`conf plan create` writes a private `atl.confluence.plan/v1` artifact with
`{schema,root,target,summary,entries,proposal_hash}`. Entries are strictly
path-ordered `update` records bound to `{id,type,title,space,path,expected_version,
baseline_sha256,candidate_sha256,problems?,blocks?,features?,byte_evidence?}`.
Unknown fields/schemas, duplicate or non-canonical paths, invalid hashes,
inconsistent summaries, and trailing JSON are rejected. The proposal hash is
computed with its own field empty and covers every other byte-semantic field.
The file must also remain byte-identical to atl's canonical indented JSON plus
final newline; reformatting or line-ending conversion is a dirty-plan refusal.
The output path is exclusive: an existing or concurrently-created reviewed
artifact is never replaced.

`conf plan preview` and `conf plan apply` emit
`{schema,proposal_hash,root,target,mode,status,complete,entries}`. Each entry
repeats the review-critical identity, baseline/candidate hashes, and safe
block/feature/byte consequences from the plan before adding its outcome. Mode is `preview|apply`;
top-level status is `would_apply|already_satisfied|blocked|partial|applied`.
Per-entry status is `not_checked|would_apply|already_satisfied|stale|blocked|
not_attempted|applied|failed|unknown`, with expected/final version,
`reconciled`, warning, and coarse failure fields when applicable. Preview and
apply perform the same complete local and remote preflight. `blocked` before
execution means zero PUTs. `partial` is non-zero; `unknown` is non-replayable.
`conf plan preview` is read-only and remains available under the global
read-only policy. `conf plan apply` is execution-only and requires both
`--confirm APPLY` and an exact external
`--expected-proposal-hash`. Exact already-applied remote/local state is the only
resume path accepted in addition to the original baseline state.
Missing plan/root paths are not-found; unreadable or identity-unsafe local paths
are check failures. Lock/preflight failures return `blocked` with
`complete:false`. Drift failures distinguish remote identity, version, content,
and local-ahead-of-remote state.

When a Confluence re-pull computes a different path for an already tracked page
id, relocation is fail-closed. The old native body must match its synced hash,
the old Markdown must exactly match its recorded pristine view, metadata must
prove the same page id, and the destination must be unoccupied. Pull records the
new canonical path before removing only the old `.csf`, `.md`, and
`.meta.json`. Descendants, assets, comment caches, and unrelated files are never
recursively removed. A local relocation ownership marker reserves their old
directory for the same page id so a future slug collision cannot inherit them.
The `<slug>.relocated.json` marker is atl-managed reserved state: do not edit or
remove it. A pre-existing invalid/different-owner marker blocks relocation and
is never overwritten.
When all three old primary artifacts are absent, pull treats the old copy as
deliberately abandoned and replaces its stale sidecar path with the new
canonical path. Partial absence remains exit `8` because ownership and local
edits cannot be proven. A supported v5/v4 view produces migration-specific
guidance and migrates only after exact pristine reconstruction; older
historical, unversioned, and unknown/future views are preserved and refused.
If cleanup is interrupted, path-aware state lookup keeps an old copy
untracked/dirty rather than presenting it as current.
Such a copy is reported by status with `non_canonical:true` and
`canonical_path`; text output uses `S! <id> <old> (canonical: <new>)`. Remote
drift probing is skipped for this stale copy. Push/dry-run refuses it with exit
`8` even under `--force`.

A successful Confluence response that omits the requested body projection is
not equivalent to an empty page. Pull and native-CSF reads require
`body.storage.value`; `conf page get --format view` requires `body.view.value`.
Either omission fails with exit `8` before output/artifacts are treated as an
empty page. After a successful push, the
same partial refresh is advisory: local body/base/state bytes are preserved and
the item reports a re-pull warning. `BodyPresent=true` with zero body bytes is a
valid explicitly empty page.

Missing local page targets for Confluence render/apply/push use
`ErrNotFound`/exit `4`; syntactically invalid target types continue to use
`ErrUsage`/exit `2`. Transport failures expose a fixed coarse category
(`dns`, `tls`, `timeout`, `connection-refused`, `connection-lost`,
`unreachable`, `canceled`, or `network`) alongside a query-redacted URL. The
raw cause remains non-unwrappable and no category includes cause text.

`atl jira status [DIR | --into ROOT] [--remote]` emits `{ "entries": [ { "path", "key", "locally_edited",
"synced", "pending_fields"?, "local_error"?, "remote_drifted"?, "field_drifted"?, "remote_error"? }, ... ] }`.
`locally_edited` is true when the `.wiki` differs from the pulled base or a configured field is
pending; `synced` is false for a `.wiki` with no sidecar entry (never-synced — it also reads
`locally_edited`). `remote_drifted` covers description or pending-field drift; `field_drifted`
identifies the latter. They and `remote_error` appear only with `--remote` and are
`omitempty`. `local_error` is independent of `--remote` and reports a broken
pending-to-mirror binding such as a missing or moved `.wiki`.

`atl jira snapshot [DIR | --into ROOT] [--remote]` emits the content-free aggregate contract
`{schema_version:1,service:"jira",remote_requested,complete,reconciled,local,
native,snapshot,pending,render,remote}`. It intentionally omits root/target,
issue identity, path, hashes, field identity, diagnostic text, and native/raw/
derived content. The offline default requires no config or credentials and
performs no pending-transaction recovery, network, or filesystem writes. Local
inspection shares the persistent mutation lock when it exists. Contention
returns a content-free exit `8` before inspection. If a legacy mirror has no
lock yet, the command verifies that no current writer created it during the
read and discards/retries the first result if one did.

Jira status/snapshot use the same mutually exclusive explicit forms and
pre-network initialized-root check as Confluence, with `mirror-jira` as the
final fallback. Root-selection errors produce no result object.

`local` partitions every `.wiki` as clean/edited and canonical
tracked/untracked, with non-canonical copies counted inside untracked. `native`
partitions present and tracked-but-removed substrates by unchanged, modified,
removed, untracked, non-canonical,
missing baseline, baseline mismatch, or unreadable baseline, and independently
reconciles baseline present/missing/unreadable plus valid/invalid. `snapshot`
reconciles expected sibling raw snapshots through present/missing,
readable/unreadable, valid/invalid, and key-matched/mismatched buckets.
`pending` partitions stable records into valid/invalid/unreadable and
bound/unbound, and reports only aggregate field-edit and active-transaction
counts. `render` reconciles expected views through present/missing/unreadable,
current/legacy/missing-marker/unsupported format, and recorded/missing view
state. `renderer_compatible` describes marker readability/compatibility only;
it does not claim the view is unedited or safe to overwrite.

With `--remote`, local preflight runs before backend setup. Any qualified local
integrity failure emits the aggregate, returns exit `8`, and performs no request.
Eligible canonical issues with valid baselines then receive at most one
single-attempt GET each; redirect responses are not followed and count as
unavailable. `attempted = checked + unavailable`, `checked = in_sync + drifted`,
and local `present = attempted + not_attempted`; unavailable never means in-sync
and makes `complete:false`. No form of this command mutates the mirror or backend.
If the aggregate cannot be written to stdout, the write failure is reported
together with the inspection failure and the exit code stays the inspection
classification. If inspection otherwise succeeds, the write failure is
returned on its own with generic exit `1`.

`atl jira push <file.wiki|DIR> [--apply] [--force] [--into ROOT]` emits `{ "items": [ ... ] }`, one
item per file: `{ "path", "key", "pushed", "dry_run"?, "skipped"?, "remote_drifted"?,
"drift_overridden"?, "diff"?, "fields"?: [{"id","diff"?}], "field_drifted"?, "failed"?,
"warning"? }`. It is **dry-run by default**: without
`--apply`, `dry_run` is `true`, `pushed` is `false`, `diff` carries the unified diff of what the
write changes on the server (current remote → local body; equal to base → local when there is no
drift), and no write occurs. Field-only pending issues are included in directory pushes. Description
drift without `--force` exits `8`; `--force` sets `drift_overridden`. Pending-field drift sets both
`remote_drifted` and `field_drifted` and always exits `8`, even with `--force`. When Description and
fields changed they are sent in one typed update. `--apply` sets `pushed:true`; a post-push
transport/local mirror-refresh failure surfaces as a `warning`, not an error.
A successful verification read that no longer matches the reviewed end state
retains pending, sets drift/failed details, and exits `8` even though
`pushed:true` records that the write request was sent. `skipped:"unchanged"`
marks a clean file.

`atl jira apply <FILE.md> [--dry-run] [--allow-loss] [--rebase-pending] [--into ROOT] [--render-*]` emits the same
shape as `conf apply` for Description, plus pending-field details:
`{ "path", "wiki_path", "pending_path"?, "dry_run", "rebased"?, "report": {...},
"fields"?: [{"id","pending","report"}], "wrote", "warning"? }`. It is **local only** (no network). Each
accepted view begins with `<!-- atl:document jira-issue v3 -->`; a v2, v1,
missing, or unversioned marker exits `8` before any write and requires an offline
`jira render` or fresh pull before editing. V1 identifies the former generated
bullet form of Subtasks/Epic Children; v2 predates the recorded display-timezone
contract. Neither legacy form is reconstructed as current during apply. A
future/unknown version requires a
newer binary and must not be rendered or downgraded by the current one. A
directory render preflights every existing view before rewriting any sibling,
so one future marker cannot produce a half-migrated batch. It repeats each
target check under the mirror mutation lock immediately before writing; `pull`
uses the same locked check before changing that issue's artifacts. A CRLF on
the marker line is recognized without normalizing the rest of the file.
Unreadable or malformed `.json` snapshots remain advisory skips, but each is
named in a stderr warning instead of disappearing silently. Since render
rewrites the derived `.md`, callers preserve any existing edits externally and
reapply them after migration.
`removed_constructs` entry is `{ "kind", "text" }` (`kind` ∈ `panel`, `color`, `mention`, `image`,
`monospace`, `link`, `macro`, …). The merge is fail-closed and exits `8` (`ErrCheckFailed`, nothing
written) on: an unconvertible edited block; a wiki-only construct dropped without `--allow-loss`
(the report still carries `removed_constructs` so the caller can see what would go); an edit to any
section other than generated `# Description` or an explicitly editable rich-text field (the error
names the section and its dedicated command); or a
local `.wiki` matches neither the last-synced base nor exact ATL-produced
staged/pending lineage. Consecutive local applies retain the remote baseline;
id/path/native/base-hash mismatches fail closed. Exit `4` (`ErrNotFound`) when the issue was never
pulled (no base/snapshot). Editable field values are stored under `.atl/pending/jira/` and do not
mutate `<KEY>.json`; `pull`/`render` overlay them in the derived view. On a successful write
`wrote:true`; a failed `.md`-view refresh sets `warning` and is not an error.
`--rebase-pending` is the explicit conflict step after fresh pull/review: raw
snapshot values become the new bases while visible local proposals remain.
Pending commits bind the exact sidecar path and reviewed wiki hash; a hidden
transaction record makes combined Description+field apply crash-recoverable.
Jira mirror mutations use one persistent mirror-internal advisory lock inode;
dry-runs may initialize that coordination file but never change Jira or commit
wiki/pending/view content.

Both `conf apply` and `jira apply` also carry a `-o text` projection — a compact loss-review
(first line dry-run/applied, `blocks:` counts, `removed fragments:`/`removed constructs:` and
`problems:` sections, `validation:` for conf, an optional `warning:`, and a contextual `next:`
hint). The JSON above is unchanged; the text view is a read-only reprojection of the same result.

`atl conf pull` returns a `PullResult` whose `pages[]` entries are `PulledPage`
objects. Each carries `id`, `title`, `path`, `version`, `assets`, and — only when
`--comments` was passed — a `comments` count (omitted otherwise, so the shape is
unchanged without the flag; an explicit `"comments": 0` means the fetch ran and
found none, distinguishable from "not fetched"):

```json
{
  "root": "mirror",
  "pages": [
    { "id": "100", "title": "Alpha", "path": "DOCS/alpha/alpha.csf", "version": 3, "assets": 0, "comments": 2 }
  ]
}
```

Both pull families add `local_safety` only for `--dry-run`, an explicit native
recovery, or a refusal. Its stable shape is:

```json
{
  "dry_run": true,
  "complete": false,
  "blocked": 1,
  "action_count": 1,
  "actions": [{
    "id": "EXAMPLE-1",
    "path": "EXAMPLE/EXAMPLE-1.wiki",
    "status": "blocked",
    "reason": "local_native_modified",
    "current_sha256": "<sha256>",
    "baseline_sha256": "<sha256>"
  }]
}
```

Closed action statuses are `blocked`, `would_overwrite`, `would_stash`,
`overwritten`, and `stashed`. `stash_path` appears only after an exact native
copy was durably preserved. Hashes are content evidence; bodies are never
included. A blocked multi-object pull emits this qualified result and then
returns `ErrCheckFailed` (exit `8`). Safe ordinary/incremental siblings may have
been refreshed, but a blocked incremental watermark is unchanged and a
complete-pull checkpoint never advances beyond the blocked identity. Recovery
flags never qualify derived-view edits, missing/corrupt baselines, or tracked
path drift.

With `--incremental`, the same result additionally carries `incremental`:

```json
{
  "selector_sha256": "<sha256>",
  "watermark_source": "explicit",
  "watermark_instant": "2026-06-30T22:00:00Z",
  "query_literal": "2026-06-28 22:00",
  "query_literal_basis": "UTC",
  "backend_query_time_zone": "unknown",
  "safety_overlap_hours": 48,
  "complete": true,
  "matched": 3,
  "selected": 2,
  "overlap_skipped": 0,
  "boundary_skipped": 1,
  "view_migrations": 1,
  "next_instant": "2026-07-01T07:42:00Z",
  "boundary_count": 2,
  "watermark_advanced": true
}
```

Incremental and complete pulls also carry the exact command-scoped scheduling
policy (defaults shown):

```json
{
  "scheduling": {
    "page_prefetch": 1,
    "max_in_flight": 1,
    "requests_per_second": 0
  }
}
```

`page_prefetch` overlaps native body reads only. Every mirror/path/asset
side-effect and checkpoint stays in canonical serial order. `max_in_flight`
and `requests_per_second` cover every actual Confluence and optional Jira-macro
transport hop, including retries, redirects, comments, and streamed assets.
Server `Retry-After` extends one shared cooldown. Zero rate means no proactive
pacing, not zero requests.

`watermark_source` is `explicit|recorded|migrated`. Watermark instants are
canonical UTC RFC3339 minutes. `query_literal` is deliberately rendered from
UTC 48 hours before `watermark_instant`; `query_literal_basis` describes that
rendering, while `backend_query_time_zone:"unknown"` explicitly avoids claiming
how Confluence interprets the zone-less CQL literal. `overlap_skipped` counts older hits removed locally. This
over-fetch makes a timezone mismatch conservative rather than lossy. `matched`
is the unique complete search set; `selected` excludes overlap hits and exact
id/version pairs already recorded at the inclusive absolute lower minute.
`view_migrations` is omitted when zero and otherwise counts selected supported
legacy Markdown views whose complete bytes matched an exact pristine
reconstruction. Those views are rewritten in the current format only as their
page pull succeeds. Edited legacy views and unknown/future markers fail the
whole preflight before body GETs or local writes.
`complete:true` is emitted only after terminal
pagination evidence and two identical metadata passes. `watermark_advanced` describes whether the successful run
changed or first persisted the watermark. The private `0600`
`.atl/incremental.json` is versioned, service/selector-hash keyed, and written
atomically only after every selected local page commit succeeds. A cap,
pagination anomaly, local dirty/drift refusal, permission/network failure, or
requested-comment truncation leaves it unchanged. No missing result implies a
remote deletion.

With `--complete`, `pages[]` contains only pages fetched during this invocation,
while `complete_pull.completed` includes a durable prefix resumed from an
earlier invocation:

```json
{
  "root": "mirror",
  "pages": [
    {"id":"300","title":"Gamma","path":"DOCS/gamma/gamma.csf","version":2,"assets":0}
  ],
  "complete_pull": {
    "selector_sha256": "<sha256>",
    "selection_sha256": "<sha256>",
    "source": "resumed",
    "complete": true,
    "total": 3,
    "completed": 3,
    "remaining": 0,
    "checkpoint_active": false
  },
  "scheduling": {
    "page_prefetch": 1,
    "max_in_flight": 1,
    "requests_per_second": 0
  }
}
```

`source` is `new|resumed|restarted`. A successful result always has
`complete:true`, `remaining:0`, and `checkpoint_active:false`; failures are
reported through the normal error envelope and retain the private resume
checkpoint. Before the first body GET for a new/restarted snapshot, two
complete metadata passes must produce the same unique id set and the remaining
local artifacts must pass overwrite preflight. Under the mode-0600
`.atl/complete-pulls/` state, immutable `<selector-sha256>.json` stores only
schema/service hashes and canonical ids; a small
`<selector-sha256>.progress.json` stores the matching hashes and `next_index`.
Neither contains credentials, URL, title, or body, and progress writes do not
rewrite the large manifest. Pull-affecting options are hash-bound. Graceful
failures flush mirror state before advancing `next_index`; a hard crash may
replay the current 25-page batch but cannot skip an uncommitted page. Both are removed
only after every selected page and the final mirror sidecar are durable.
`view_migrations` is present only when supported pristine legacy views were
recognized during preflight. No missing page or retired checkpoint proves a
remote deletion.

`atl conf comment list` now emits a schema-v2 qualified inventory:

```json
{
  "schema_version": 2,
  "page_id": "123",
  "page_version": 7,
  "page_version_gated": false,
  "query": {"mode":"list","location":"all","state":"all","depth":"all"},
  "complete": true,
  "comments_complete": true,
  "threads_complete": true,
  "anchors_complete": true,
  "count": 0,
  "root_count": 0,
  "partial_reasons": [],
  "capabilities": {
    "footer": "documented",
    "inline": "documented",
    "resolved": "documented",
    "depth_all": "documented",
    "thread_ancestry": "documented",
    "inline_properties": "documented",
    "resolution": "documented"
  },
  "comments": [],
  "diagnostics": []
}
```

All arrays are non-null. Comment records carry nullable `parent_id`/`root_id`,
closed `relation` (`root|reply|unknown`), semantic `location`
(`footer|inline|unknown`), independent `resolution`
(`open|resolved|unknown`), exact native `body_storage`, plain `body`, author,
version/timestamps, and a nullable anchor. Anchor status is
`matched|missing|ambiguous|unavailable`; original and observed selections are
kept separately. Inline anchors belong to root discussions; proven replies have
a null anchor and remain qualified by their explicit ancestry. A backend
`resolved` location is represented as
`location:inline` plus `resolution:resolved`. The explicit backend wire state
`reopened` is normalized to semantic `resolution:open`; every other unknown
wire state remains `unknown` and makes the inventory partial.

Current schema-v2 projections never emit reply-level anchors. The sidecar
decoder and renderer still preserve historical schema-v2 reply anchors without
normalizing them, so the v5 reconstruction used by migration remains
byte-stable; a fresh v6 pull writes the root-owned shape. This compatibility exception does not
apply to transient result, list, or thread validators.

`complete` is the conjunction of the dimensions relevant to the selected
query. Closed `partial_reasons` and content-free diagnostics cover pagination,
duplicate/ancestry/metadata gaps, unavailable page/comment bodies and inline
expansions, and missing or ambiguous anchors. A successful partial result stays
on stdout. `comment thread` uses the same envelope with `query.mode:"thread"`
and exact `comment_id`; proven absence is exit 4, while unprovable absence is
exit 8. Its diagnostics, partial reasons, and completeness are scoped to the
selected root subtree: global enumeration/transport qualification remains,
but unrelated comment ids and orphan page markers are excluded. Explicit
`--legacy-flat` retains the prior list shape temporarily and
cannot be combined with schema-v2 filters or a page-version gate.

`atl conf comment preview` is the read-only proposal surface. `atl conf comment
add` emits the same dry-run by default but remains mutating-classified; only
`--apply --expected-proposal-hash <hash>` can send one POST. Both use this exact
top-level result shape (fields with `omitempty` are noted below):

```json
{
  "schema_version": 1,
  "page_id": "123",
  "mode": "dry-run",
  "status": "would_apply",
  "comment_type": "footer",
  "page_version": 7,
  "body_sha256": "<sha256>",
  "body_bytes": 18,
  "actor": {"id":"<stable-actor-id>","display_name":"Example User"},
  "capability": {
    "provider": "public_rest",
    "operation": "footer_root_create",
    "write": "documented",
    "readback": "documented",
    "depth": "root"
  },
  "current_count": 2,
  "baseline_sha256": "<sha256>",
  "backend_sha256": "<sha256>",
  "proposal_hash": "<sha256>",
  "complete": true,
  "warning": "non_idempotent_write_requires_single_attempt_and_reconciliation"
}
```

The exact fields are `schema_version`, `page_id`, `mode`, `status`,
`comment_type`, `page_version`, `body_sha256`, `body_bytes`, `actor`,
`capability`, `current_count`, `baseline_sha256`, `backend_sha256`,
`proposal_hash`, optional `created`, `complete`, optional `reconciled`, and
`warning`. `actor` is exactly `{id,display_name}`; `capability` is exactly
`{provider,operation,write,readback,depth}`. `created`, present only when a
record is proven, uses the qualified comment record fields `id`, `page_id`,
nullable `parent_id`/`root_id`, `relation`, `location`, `resolution`, `version`,
`author`, `created_at`, `updated_at`, `body`, `body_storage`, and nullable
`anchor`. `reconciled:true` is present only after complete readback succeeds. Text output is
exactly `status`, `page_id`, `proposal_hash`, `body_sha256`, and `body_bytes`,
one `key: value` line each.

`mode` is `dry-run|apply`. The closed statuses are `would_apply`, `conflict`,
`not_applied`, `applied`, `recovered`, and `outcome_unknown`. `applied` matches
the returned identity to one exact new root; `recovered` proves exactly one new
actor/body match after an unusable write response. `outcome_unknown` is an
ambiguous-write exit: the POST may have committed, so it is never replay-safe.
The `complete` and `reconciled` fields qualify the evidence available for that
classification.

The schema-v1 proposal hashes the configured backend identity, page id/version,
comment type, exact body bytes plus SHA-256 and length, stable actor id,
capability record, complete sorted footer-root baseline SHA-256, and current
count. Stdout exposes only `backend_sha256`, never the backend identity. Input
is non-empty valid UTF-8/native CSF and is accepted byte-exactly through 1 MiB
(1,048,576 bytes). Apply recomputes and immediately revalidates the proposal,
sends at most one single-attempt POST, then reconciles from a complete bounded
root-only footer read. It cannot create replies or inline comments or change
resolution; duplicate body text is not an idempotency key.

`atl conf comment mutation preview|apply` uses a separate content-free schema-v1
result with `page_id`, `thread_id`, `operation`, `mode`, `status`,
`page_version`, `thread_version`, `source_state`, optional `target_state`,
optional `body_sha256`/`body_bytes`, `actor`, `provider.id`, `current_count`,
`baseline_sha256`, `backend_sha256`, `proposal_hash`, optional `comment_id`,
`complete`, optional `reconciled`, and `warning`. Exact configured
version/build values, body bytes, selection text, DOM bytes, request-time, and
highlight paths are never emitted. Operations are exactly
`inline_create|reply|resolve|reopen`; statuses additionally include `no_op`.
`inline_create` also emits selection/body length and hashes, zero-based input
`occurrence`, derived provider `match_index`, surviving `num_matches`,
`highlight_count`, `geometry_sha256`, native
`page_body_sha256`, marker count/hash, and after proven apply optional
`marker_ref`/`result_page_version`. Its empty `thread_id`, zero
`thread_version`, and empty `source_state` retain the common schema shape until
readback supplies `comment_id`. Only
`--apply --expected-proposal-hash` may write. `outcome_unknown` is never
replay-safe. These commands are JSON-only and have no MCP route.

The inline-create proposal binds the exact native page revision, stable
canonical rendered-content fingerprint, raw selection/body hashes and lengths,
pinned-client-normalized search and wire selections, selected input occurrence,
surviving match count/provider index, derived raw-DOM UTF-16 geometry, complete
comment and native-marker baselines, actor, backend, and exact private provider
activation. Normalized or raw selection content is hash input only and is never
emitted. Native exclusion masks and footer-fallback regions are reproduced
fail-closed; ambiguous browser-layout constructs are rejected before POST.
Volatile server request-time is deliberately not part of the proposal or
output. Apply repeats preparation immediately before the sole POST, requires
all stable evidence to match, and uses only that fresh request-time. Success
requires a complete readback proving one exact new root and that the server
changed native page CSF only by inserting its one matching inline marker
wrapper. The pinned profile accepts only the two observed public-version
semantics: the page version may remain unchanged or advance by exactly one;
every other transition remains `outcome_unknown`. In either case the provider
response version, when the response is successfully decoded, must agree with
the reconciled readback. An unusable response can produce `recovered` only from
the same strict complete readback proof. ATL never synthesizes or applies
marker CSF.

With `--comments`, `<slug>.comments.json` is the authoritative versioned source
evidence, using the same qualified comment records, completeness dimensions, capabilities,
closed partial reasons, and diagnostics as the schema-v2 list contract. It also
binds `page_id` and `page_version`; `count` and `root_count` are validated
assertions. Arrays are never `null`, native `body_storage` values are preserved,
and the file is deterministic, indented JSON with one trailing newline. The
reader also accepts the historical flat `[{id,author,created,body,...}]` array,
but a successful `pull --comments` always writes v2. Malformed, future, or
page-version-mismatched v2 bytes never fall back to the legacy decoder.

The main v6 page `.md` renders schema-v2 comments as a deterministic read-only
tree: roots are level-two headings, replies nest through level six with a stable
deeper-depth indicator, and each entry shows author/time plus explicit
location/state. Matched anchors label only the observed selection as current;
missing, ambiguous, and unavailable anchors remain qualified and may show an
original selection only as reported. Incomplete or malformed ancestry never
drops a record: it appears deterministically under an unattached section.
Completeness and closed partial reasons are visible in the view. Safe generic
orphan/selection diagnostics may also be shown without record identifiers or
backend text; the structured evidence remains in `.comments.json`.
`<slug>.comments.md` remains a
best-effort flat compatibility projection. The
page's `.meta.json` gains `comments_pulled:true`, `comment_sidecar_version:2`,
counts, explicit comment/thread/anchor completeness booleans, and content-free
partial reason codes. `comments_truncated:true` remains limited to bounded
pagination/cap loss rather than all forms of partial anchor qualification.
These fields and all comment bytes stay outside `content_hash`, `.atl/base/`,
and dirty/drift/push gates. Complete and incremental pulls advance their durable
checkpoint only when both comment enumeration and thread geometry are complete;
anchor-only partiality remains recorded without blocking progress.

### Environment time diagnostics

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

`atl jira export --jql ... --out FILE --format jsonl|json|csv` writes one compact artifact and a
sidecar manifest at `FILE.manifest.json`. `--ids` and `--keys` can be used instead of `--jql` to
generate batched `id in (...)` / `key in (...)` queries. Explicit selectors are
de-duplicated by first occurrence and found issues are emitted in that order
across pages and batches. Missing/inaccessible identities are omitted without
disturbing the relative order of found rows. User JQL retains backend order.
Stdout remains the normal `emit()` JSON summary:

```json
{
  "path": "issues.jsonl",
  "manifest_path": "issues.jsonl.manifest.json",
  "format": "jsonl",
  "count": 1
}
```

JSONL emits one `JiraIssueSnapshot` object per line (`{key,id,fields}`); JSON emits
`{manifest,issues}`; CSV emits `key,id` followed by the deterministic field list.
JSONL/CSV are streamed atomically; aggregate JSON is limited to 10,000 issues
and 64 MiB of serialized issue data. The row-stream identity index is capped at
250,000 unique issues so exact deduplication remains memory-bounded.
CSV formula-leading cells are apostrophe-prefixed by default. `--raw-csv`
disables that protection and records `csv_raw: true` in the manifest. The manifest
stores query mode, generated queries when applicable, fields, format, count, CLI version, and a
backend URL hash only:

```json
{
  "command": "atl jira export",
  "format": "jsonl",
  "query_mode": "jql",
  "row_order": "backend",
  "jql": "project=PROJ",
  "count": 1,
  "backend": {
    "service": "jira",
    "url_hash": "sha256:..."
  }
}
```

For `query_mode: keys|ids`, the manifest instead carries `row_order:
"selector"` and `missing_identity_behavior: "omit"`. Ordering is identical in
JSONL, aggregate JSON, and CSV, for files and artifact-only stdout. Explicit
selection buffering is bounded to one generated batch and 64 MiB of encoded
issue data; the global 250,000 identity safety cap remains in force.

The backend hostname and PAT are never written to the manifest.

`atl conf table summary` returns a bounded content-free table inventory:

```json
{
  "schema_version": 3,
  "cell_contract": "confluence-table-cells/compact-v3",
  "page_id": "123456",
  "version": 7,
  "page_version_gated": false,
  "table_count": 1,
  "returned_table_count": 1,
  "selection_reconciled": true,
  "tables": [{
    "index": 1,
    "row_count": 3,
    "column_count": 2,
    "rectangular": true,
    "header_row_count": 1,
    "header_cell_count": 2,
    "expanded_cell_count": 6,
    "origin_cell_count": 5,
    "repeated_cell_count": 1,
    "synthetic_empty_cell_count": 0,
    "cell_count_reconciled": true,
    "nonempty_text_cell_count": 6,
    "nonempty_markdown_cell_count": 6,
    "nonempty_raw_cell_count": 2,
    "styled_cell_count": 0,
    "style_entry_count": 0,
    "distinct_style_marker_count": 0,
    "linked_cell_count": 1,
    "rowspan_metadata_cell_count": 2,
    "rowspan_source_cell_count": 1,
    "rowspan_covered_cell_count": 1,
    "colspan_metadata_cell_count": 0,
    "colspan_source_cell_count": 0,
    "colspan_covered_cell_count": 0,
    "warning_count": 0
  }]
}
```

Selecting `--table N` adds `selected_table:N`, limits `tables` to that one
entry, and keeps the page-wide `table_count`; `returned_table_count` and
`selection_reconciled` make that relationship explicit. Every cell count uses
the expanded representation. `origin_cell_count` counts native `th`/`td`
origins, `repeated_cell_count` counts span-covered copies, and
`synthetic_empty_cell_count` counts rectangular padding. A true
`cell_count_reconciled` proves more than those three counts equalling
`expanded_cell_count` and the reported row/column shape. ATL independently
reconstructs every source-cell placement and declared rowspan/colspan rectangle
from the source DOM, rejects overlapping claims or coverage outside the source
row domain, and requires that ledger to agree cell-for-cell with the expanded
grid. A syntactically valid native span above 100 returns a check failure before
expansion; no schema-v3 result can claim reconciled geometry for a clamped
table.

Direct `rowspan_metadata_cell_count` / `colspan_metadata_cell_count` count every
expanded cell carrying that span metadata, including covered copies; the
existing source and row/column-covered counts retain their coordinate-based
semantics. Non-empty text, Markdown, and raw-attribute counts are separate.
`style_entry_count` sums style-object entries, while
`distinct_style_marker_count` counts distinct key/value pairs. Only the counts
are emitted: the command never emits page titles, cell content, URLs, style
keys/values, raw attributes, or warning text.

`--expected-version N` binds either table command to that already-observed
positive page revision without adding a backend request. A match returns
`version:N` and `page_version_gated:true`; omission returns
`page_version_gated:false`. A stale version fails before table parsing or
evidence, using the typed expected/current integer mismatch. For JSON, CSV, or
XLSX written with `--out`, the extraction acknowledgement also includes
`returned_table_count`, `selection_reconciled`, `version`, and
`page_version_gated`. Its text form reports the returned count rather than the
page-wide count.

Every table record returned by `atl conf table extract --format json` also has
a required `summary` object with this exact record shape. ATL computes it from
the expanded table before JSON encoding. The embedded and standalone records
therefore use identical origin/repeat/padding and span semantics; clients that
need both content and counts should use the embedded record instead of
recounting cells. The field is additive to the extraction contract and does
not affect CSV or XLSX rendering.

Table schema v3 makes the compact cell kind durable and requires the exact
top-level `cell_contract:"confluence-table-cells/compact-v3"` marker on both
summary and extraction envelopes. A native `th`/`td` origin is the unmarked
default and has no source coordinates. Every span-covered copy has
`repeated:true` plus `source_row` and `source_column` naming its covering
origin. Rectangular padding has `synthetic:true`, no source coordinates, and no
content or span metadata. Any other combination is invalid. After
serialization ATL recomputes the span ledger from these fields and requires the
attached `summary` to match exactly, so legacy, schema-only relabelled, or
forged payloads cannot upgrade themselves to `cell_count_reconciled:true`.
All-table CSV keeps its existing source-coordinate columns and derives a native
origin's self coordinates from the compact cell kind; synthetic rows leave
them blank. CSV/XLSX exports are terminal exports rather than replayable mirror
views, so they have no separate migration marker.

Selected-table CSV neutralizes every cell whose first byte is `=`, `+`, `-`,
`@`, tab, carriage return, or newline by prefixing an apostrophe. This
spreadsheet-safe behavior is the default; it applies to headers and data cells
while leaving ordinary text, numbers, and already-apostrophe-prefixed values
unchanged. `--raw-csv` is an explicit unsafe escape hatch that preserves those
formula-leading values verbatim for trusted non-spreadsheet consumers. It does
not change table selection, parsing, or backend access, and it never authorizes
a remote write.

When `--out` is given, JSON, CSV, and XLSX all persist through one atomic
application writer (temp file then rename), so no partial artifact is ever
observable; missing parent directories are created as needed. The success
acknowledgement byte shape (`path`, `format`,
`table_count`, `returned_table_count`, `selection_reconciled`, `version`,
`page_version_gated`) is unchanged. A persistence failure is a check failure:
it exits `8`, emits nothing to stdout, and leaves any existing file untouched.
A missing XLSX `--out` remains a usage error (exit `2`).

The extraction's top-level `table_count` remains page-wide.
`returned_table_count` equals the actual `tables` array length, and
`selection_reconciled` is true only when an unselected extraction returned all
page tables or a selected extraction returned exactly the requested table.
These additive fields remove the need for clients to infer selected-result
cardinality from the page-wide count.

`atl jira export diff OLD NEW` reads JSONL/JSON/CSV compact exports and reports issue identifiers:

```json
{
  "old_count": 1,
  "new_count": 2,
  "added": ["PROJ-2"],
  "changed": ["PROJ-1"]
}
```

`atl jira planning report --jql ...` returns deterministic per-issue quality rows:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "score": 4,
      "max_score": 5,
      "level": "warn",
      "gaps": ["missing_artifact_ref"],
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ],
  "summary": {
    "good": 0,
    "warn": 1,
    "poor": 0
  }
}
```

When `--csv FILE` is passed, the same command writes a deterministic CSV sidecar
and includes `csv_path` in the JSON result. Formula-leading cells are
apostrophe-prefixed by default; `--raw-csv` requires `--csv` and disables that
protection for trusted non-spreadsheet consumers.

`atl jira fields` and typed MCP `jira_fields` share one value-free catalog
contract:

```json
{
  "schema_version": 1,
  "projection": "full",
  "source": "jira-field-catalog",
  "complete": true,
  "total": 2,
  "count": 1,
  "custom_count": 1,
  "system_count": 0,
  "fields": [
    {
      "id": "customfield_10001",
      "name": "Delivery Notes",
      "custom": true,
      "schema": "string"
    }
  ]
}
```

`total` describes the source snapshot before client-side filters; `count`
describes the filtered match set. `custom_count` and `system_count` partition
that same set and always sum to `count`. The default `projection:"full"` emits
the matching value-free definitions. CLI `--summary-only` and MCP
`summary_only:true` select `projection:"summary"` and return `fields:[]`,
preserving qualification, filters, and reconciled counts in a compact result.
Filtering and projection never upgrade or downgrade source completeness.
Jira's `/rest/api/2/field` response is atomic and non-paginated, so a
successfully decoded non-empty response is `complete:true`. An empty or
legacy/unqualified source is `complete:false` with `partial_reason`; malformed
ids, duplicates, and contradictory qualification fail with exit 8. Field
values are never part of this contract. The text projection begins with
the backward-compatible `complete`, `source`, `count`, and `total` line. The
summary text projection adds `projection=summary`, `custom`, and `system` on a
second line and no field records; the full projection keeps the existing
tab-separated field records.

Typed MCP `jira_issue_graph` returns the same schema-v2 graph through a
Jira-only read. Its schema requires one canonical issue `key` and accepts
optional `depth` from 0 through 2, `max_nodes`, `max_edges`, `max_requests`,
and `max_bytes`. It deliberately accepts neither
`resolve`/`resolve_confluence` nor `strict`: Confluence identities remain
qualified stubs, and callers inspect `complete`, sources, reconciliation, and
the frontier in the successful result.

The backend and result byte bounds are independent. MCP fixes evidence at 500
records and the aggregate Jira response budget at 16777216 bytes; reported
`bounds.max_response_bytes` and `response_bytes_used` expose that backend
budget, but `max_response_bytes` is not a v1 input. The separate `max_bytes`
input caps the final encoded MCP result (default 256 KiB, minimum 1 KiB,
maximum 1 MiB). `max_nodes` defaults to 50 and caps at 100, `max_edges`
defaults to 200 and caps at 500, and `max_requests` defaults to 50 and caps at
100. Exhausting an
application traversal bound can therefore return a valid schema-v2 graph with
`complete:false` and static qualification. Exceeding the final `max_bytes`
instead returns an MCP output-limit error with no clipped graph. Neither case
proves that an omitted relationship is absent. When `include_development` is
omitted or false, the MCP request and output retain the stable profile and no
Development source is present; that absence must never be reported as zero
development activity.

`atl jira issue graph <KEY>` emits one transient, deterministic schema-v2
work-artifact graph. Depth defaults to zero:

The CLI `--include-development` option and typed MCP
`include_development:true` input add
`bounds.include_development:true`, one `development` source per expanded Jira
node, and four closed GitLab node/edge kinds: project, commit, branch, and
merge request. Each GitLab node has an `scm` object containing `host` and
`project_path`, plus exactly one applicable artifact selector (`commit_sha`,
`branch_name`, or `merge_request_iid` with `merge_request_state`); project nodes
have no artifact selector. All such sources, nodes, edges, and evidence are
`experimental_api`; nodes are unexpanded stubs and are never traversed.
Development source `count` excludes project containers. Any failure or
reconciliation mismatch is fail-closed for that source: stable graph facts
remain, but no partial Development projection survives. Omitting the option or
supplying MCP false preserves the stable request sequence and schema-v2 output
bytes shown below.

The MCP projection omits every GitLab node URL and exposes only the closed SCM
coordinates plus ordinary graph topology and experimental provenance. It does
not add narrative, people, email, avatars, files, diffs, timestamps, query
values, labels, or raw payloads. ATL itself never contacts GitLab or reuses Jira
credentials. A downstream GitLab read is a separate operation: require exact
equality between the returned lowercase host and an owner-approved host, then
use a separately authenticated read-only client for that exact host.

```json
{
  "schema_version": 2,
  "root_id": "jira:issue:PROJ-1",
  "complete": true,
  "bounds": {
    "requested_depth": 0,
    "max_nodes": 100,
    "max_edges": 500,
    "max_evidence": 500,
    "max_source_bytes": 1048576,
    "expanded_node_count": 1,
    "followed_node_count": 0,
    "attempted_node_count": 1,
    "max_requests": 100,
    "requests_used": 4,
    "max_response_bytes": 16777216,
    "response_bytes_used": 4096,
    "max_sources": 801,
    "max_frontier": 100
  },
  "summary": {
    "node_count": 2,
    "edge_count": 1,
    "evidence_count": 1,
    "source_count": 8,
    "incomplete_source_count": 0,
    "source_status_counts": {
      "complete": 2,
      "empty": 6,
      "forbidden": 0,
      "partial": 0,
      "skipped": 0,
      "unsupported": 0
    },
    "node_count_matches_nodes": true,
    "edge_count_matches_edges": true,
    "evidence_count_matches_edges": true,
    "source_count_matches_sources": true,
    "source_status_count_matches_sources": true,
    "incomplete_source_count_matches_sources": true,
    "expanded_count_matches_nodes": true,
    "complete_matches_sources": true
  },
  "nodes": [
    {
      "id": "jira:issue:PROJ-1",
      "kind": "jira_issue",
      "service": "jira",
      "external_id": "PROJ-1",
      "label": "Graph seed",
      "state": "resolved",
      "expanded": true,
      "depth": 0,
      "stability": "public_api"
    },
    {
      "id": "jira:issue:PROJ-2",
      "kind": "jira_issue",
      "service": "jira",
      "external_id": "PROJ-2",
      "state": "stub",
      "expanded": false,
      "depth": 1,
      "stability": "public_api"
    }
  ],
  "edges": [
    {
      "id": "edge:<sha256>",
      "from": "jira:issue:PROJ-1",
      "to": "jira:issue:PROJ-2",
      "kind": "jira_link",
      "relation_type": "Blocks",
      "relation": "blocks",
      "direction": "outward",
      "current": true,
      "confidence": "exact",
      "stability": "public_api",
      "evidence": [
        {
          "collector": "issue_links",
          "source_node_id": "jira:issue:PROJ-1",
          "source_kind": "field",
          "source_id": "7",
          "json_pointer": "/fields/issuelinks/0",
          "extraction": "structured"
        }
      ]
    }
  ],
  "sources": [
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_fields",
      "requested": true,
      "status": "complete",
      "complete": true,
      "count": 4,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_links",
      "requested": true,
      "status": "complete",
      "complete": true,
      "count": 1,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "hierarchy",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "attachments",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_properties",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "experimental_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "comments",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "worklogs",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "remote_links",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    }
  ]
}
```

The canonical node kinds in schema v2 are `jira_issue`, `confluence_page`,
`attachment`, and `url`. At depth zero, as shown above, the seed is the only
`expanded:true` node. All discovered targets have depth 1 and are not requested.
Candidate Jira keys found only in narrative use the canonical Jira node id with
`state:"unresolved"` until a structured fact supplies exact identity. Edge
kinds are `jira_link`, `parent_of`, `child_of`, `epic_of`, `attached`,
`remote_link`, and `mentions`; a typed relation and a mention to the same node
remain distinct edges. Every edge has at least one content-minimized evidence
record, and duplicate semantic edges merge sorted evidence.

The fixed source order is `issue_fields`, `issue_links`, `hierarchy`,
`attachments`, `issue_properties`, `comments`, `worklogs`, and `remote_links`.
Their closed statuses are `complete`, `empty`, `partial`, `forbidden`,
`unsupported`, and `skipped`.
Only `complete` and `empty` have `complete:true`. Optional
`partial_reason` is one of `inspection_limit`, `output_limit`,
`request_failed`, `malformed_response`, `request_limit`, `byte_limit`,
`dependency_unavailable`, or `policy`; it never contains a backend error.
Malformed or request-limited sources are `partial`; a source that cannot be
started by policy is `skipped`. Stability is fixed per source kind:
`issue_properties` is `experimental_api`; every other current kind is
`public_api`. `issue_properties` remains ordered: its count is the number of
returned properties inspected, and completeness means the returned property
set was processed under the fixed privacy exclusions and bounds, not that every
property produced graph evidence.
Top-level `complete` is derived from all requested sources. Auxiliary source
failure returns a reconciled graph with exit 0 and `complete:false`; seed,
schema, or reconciliation failure returns the corresponding non-zero sentinel.

The one root snapshot requests `fields=*all`, `properties=*all`, and
`expand=names,schema` together and is single-attempt. Comments and worklogs use
their complete paginated readers; remote links use Jira's supported direct
endpoint. Returned fields are reconciled against names/schema before recursive
inspection. A recursively eligible field with missing, blank, unknown, or
structurally invalid type/item metadata is skipped and qualifies `issue_fields`
as `partial` / `malformed_response`; structured and privacy-excluded fields do
not require walker metadata. A custom narrative field without necessary name
metadata disables bare Jira-key inference and receives the same qualification.
An unknown noncanonical field id is also partial and cannot enable bare
inference, though a valid non-identity schema may still permit URL-only
inspection. Jira's literal top-level `type:any` is accepted only for a canonical
custom field; it remains path-filtered and URL-only and never enables bare-key
inference. Nulls, scalar numbers/booleans, and empty strings or containers cannot
contain graph references and therefore require no walker metadata. Extra
metadata for fields that were not returned is ignored.
The walker accounts for container, key, scalar, pointer, depth, item,
and source-byte limits, excludes user/avatar/icon/transport/download subtrees,
and never dereferences discovered URLs. HTTP(S) URLs reject userinfo, remove
fragments and default ports, and never emit query values. Sensitive or
credential-like path segments make the URL an opaque identity without a raw
URL. Dynamic property and nested-object tokens in evidence pointers are
deterministic opaque tokens rather than source content. Text output
contains the same qualification plus escaped source/node/edge tables. `-o id`
is rejected before configuration or network access.

Every graph invocation uses schema v2. Omitting traversal and resolution flags,
explicit `--depth 0`, explicit `--resolve none`, or both explicit values keeps
the same direct depth-zero contract. `--depth 1..3` adds structured Jira
traversal and `--resolve confluence` adds the narrow metadata phase. Schema v2
uses the same top-level arrays and reconciliation summary at every depth, with
these transport and provenance fields:

- `bounds.attempted_node_count` counts Jira snapshot calls that were actually
  attempted; `followed_node_count` is the non-root subset and
  `expanded_node_count` counts successfully expanded Jira nodes.
- `bounds.max_requests` / `requests_used` count physical HTTP attempts across
  Jira and optional Confluence reads. `bounds.max_response_bytes` /
  `response_bytes_used` count aggregate buffered successful and error response
  bytes. Reads are single-attempt: no retry or followed redirect can bypass the
  shared bounds.
- `bounds.max_sources`, `max_frontier`, `frontier_count`, and optional
  `frontier_truncated` qualify the remaining inventories. The optional
  top-level `frontier` is sorted by depth, node id, and reason and contains only
  `{node_id, depth, reason}`.
- Every source has `node_depth` and remains keyed by `(node_id, kind)`. Every
  edge evidence record has `source_node_id`, which identifies the expanded Jira
  node whose collector observed that fact.

Traversal is deterministic breadth-first order across the entire current
depth. Only canonical `jira_issue` nodes in `state:"stub"` that came from exact
structured issue-link or hierarchy evidence are eligible. Narrative
`mentions`, URLs, attachments, and Confluence pages are never traversal inputs.
Cycles and diamonds are read once. A Jira response whose canonical key differs
from the requested moved key is reconciled into one node and one semantic edge
inventory before the summary is computed.

The schema-v2 defaults are 100 nodes, 500 edges, 500 evidence records, 100
physical requests, and 16777216 buffered response bytes. Hard maxima are 2048,
4096, 4096, 128, and 67108864 respectively; depth is capped at 3. Admission of
a new node, edge, and its evidence is atomic. Work refused by an output,
physical-request, or response-byte bound is statically classified as
`output_limit`, `request_limit`, or `byte_limit`; dynamic backend details and
live counters never enter a reason string. When the seed response itself
exceeds the response-byte bound, schema v2 still emits one
`state:"unresolved"` root with no edges, zero expanded nodes, a root frontier
item, and all requested sources qualified by the same budget reason. Optional
Confluence resolution, when requested, is represented by an equally qualified
`confluence_metadata` source rather than being silently omitted.

`--resolve confluence` adds one aggregate `confluence_metadata` source after
Jira traversal. It considers only already discovered canonical numeric page
ids and performs at most one same-origin, single-attempt, id/title-only GET for
each candidate. It does not request page body, ancestors, labels, restrictions,
principals, assets, or arbitrary URLs. Unavailable optional configuration is
`status:"skipped"` with `partial_reason:"dependency_unavailable"`. Missing
pages remain `state:"missing"` while a fully attempted inventory can still be
complete; forbidden or malformed responses remain explicitly incomplete.

Top-level `complete` continues to be derived from every requested source.
`--strict` does not alter the document: it emits the reconciled JSON or text
first, then returns `ErrCheckFailed` (exit 8) when `complete:false`. Schema-v2
text adds transport usage, per-node source columns, and a frontier table when
one exists.

This contract change does not change `jira issue refs`; its exact JSON/text
compatibility goldens remain independent.

`atl jira issue refs <KEY>` and `atl jira issue refs --jql ...` return
deterministic, provenance-qualified artifact references per issue:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "complete": true,
  "selection": {
    "mode": "jql",
    "count": 1,
    "limit": 100,
    "complete": true
  },
  "summary": {
    "issue_count": 1,
    "complete_issue_count": 1,
    "incomplete_issue_count": 0,
    "reference_count": 1,
    "reference_kind_counts": {"doc": 1},
    "source_count": 2,
    "source_value_counts": {"comments": 2, "description": 1},
    "complete_source_count": 2,
    "incomplete_source_count": 0,
    "truncated_source_count": 0,
    "count_matches_issues": true,
    "selection_count_matches_issues": true,
    "reference_count_matches_kinds": true,
    "issue_summaries_reconciled": true,
    "complete_matches_inputs": true,
    "truncated_matches_inputs": true
  },
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "complete": true,
      "sources": {
        "comments": {"complete": true, "count": 2},
        "description": {"complete": true, "count": 1}
      },
      "reference_summary": {
        "reference_count": 1,
        "reference_kind_counts": {"doc": 1},
        "source_count": 2,
        "source_value_counts": {"comments": 2, "description": 1},
        "complete_source_count": 2,
        "incomplete_source_count": 0,
        "truncated_source_count": 0,
        "reference_count_matches_kinds": true,
        "complete_matches_sources": true,
        "truncated_matches_sources": true
      },
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ]
}
```

The top-level `complete` combines JQL/key selection completeness with every
issue's contributing sources. `selection.truncated:true` means `--limit`
stopped a JQL result while Jira advertised more rows. Each issue qualifies
`description`, `comments`, and every requested `field.<id>` with `complete`,
input-value `count`, optional `text_truncated`, and a bounded warning. Comments
come from the complete paginated comment endpoint; a recoverable comment-source
failure may retain embedded comments but marks that source and the issue
incomplete.

Each issue's additive `reference_summary` is derived from its final emitted
`sources` and deduplicated `refs`. `reference_count` therefore counts a URL once
per issue even if several narrative sources contained it, and always equals the
sum of `reference_kind_counts` when `reference_count_matches_kinds:true`.
`source_value_counts` preserves the existing source names and sums their input
value counts. The top-level `summary` combines those issue summaries, reports
complete/incomplete/truncated source and issue cardinalities, and exposes exact
reconciliation with top-level `count`, `selection`, `complete`, and `truncated`.
References repeated by different issues are counted once for each issue; atl
does not assert that cross-issue URLs represent one evidence use. Consumers
should use these deterministic aggregates instead of recounting nested arrays.

`--fields` selectors are resolved once through the shared Jira field catalog:
technical ids remain direct, while exact case-insensitive display names map to
technical ids before selection and extraction. Field source keys always contain
the resolved technical id. A JQL selection performs one complete paginated
comment listing per issue; callers should use a narrow query and explicit limit
when budgeting backend requests.
All narrative values use the same 128 KiB per-value evidence cap as `epic digest`.
Missing requested fields and clipped values remain incomplete. `-o text` starts
with completeness/selection status, then emits the shared escaped Markdown
table and bounded warnings. An empty `refs` array is evidence of absence only
when both result and issue completeness are true.

`atl jira issue attachment list <KEY>` returns the issue key plus the attachment
metadata Jira exposes. `-o id` prints attachment ids one per line:

```json
{
  "attachments": [
    {
      "id": "42",
      "title": "spec.xlsx",
      "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "fileSize": 12345,
      "version": 0
    }
  ],
  "key": "PROJ-1"
}
```

`atl jira issue attachment get <KEY> --id <ID-or-filename>` downloads one
attachment and returns the written local path. `id` echoes the selector the
caller passed; `name` is the filename Jira reported for the matched attachment:

```json
{
  "id": "42",
  "key": "PROJ-1",
  "name": "spec.xlsx",
  "path": "attachments/spec.xlsx"
}
```

`atl jira issue attachment upload <KEY> --file <PATH>` uploads one local file
and returns the uploaded attachment metadata:

```json
{
  "attachment": {
    "id": "44",
    "title": "spec.xlsx",
    "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "fileSize": 12345,
    "version": 0
  },
  "key": "PROJ-1"
}
```

For Confluence attachment `upload`, a caller-supplied negative size or a
multipart body that would overflow its length exits `2`. For both Confluence
and Jira, a successful backend response that is malformed JSON or carries no
attachment exits `8` — distinct from a transport failure (exit `1`).

`atl jira issue tree --jql ... --epic-field ...` returns a normalized
epic-to-child tree:

```json
{
  "jql": "project=PROJ",
  "epic_field": "customfield_10001",
  "count": 3,
  "epics": [
    {
      "key": "PROJ-1",
      "summary": "Parent",
      "type": "Epic",
      "children": [
        {
          "key": "PROJ-2",
          "summary": "Child",
          "type": "Story",
          "epic": "PROJ-1"
        }
      ]
    }
  ]
}
```

`external_epics` contains children whose epic key is not part of the selected
JQL result. `orphans` contains selected non-epic issues with no epic field. Both
fields are omitted when empty.

`atl jira issue link suggest --csv links.csv` is read-only and returns missing
link candidates from a reviewed CSV plan:

```json
{
  "path": "links.csv",
  "planned_count": 2,
  "count": 1,
  "candidates": [
    {
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "dependency found during review",
      "row": 2
    }
  ]
}
```

Rows whose outward link already exists on the source issue are omitted from
`candidates`. The command performs no Jira writes.

`atl jira issue plan apply --csv plan.csv` returns a guarded dry-run/apply
report:

```json
{
  "version": 1,
  "path": "plan.csv",
  "mode": "dry-run",
  "count": 1,
  "results": [
    {
      "row": 2,
      "op": "link",
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "reviewed dependency",
      "expected_updated": "2026-01-02T03:04:05.000+0000",
      "status": "would_apply"
    }
  ]
}
```

Status values are `would_apply`, `already_satisfied`, `applied`, `blocked`,
`failed`, and fail-fast `skipped`. The command defaults to dry-run. Write mode
requires `--apply --confirm APPLY`; `field` operations also require the field
to be included in `--allow-fields`. Every CSV row carries `version=1` and a
review-time `expected_updated` value. Blocked/failed runs still emit the full
audit result on stdout and return exit 8. Default execution stops after the
first runtime failure; `--continue-on-error` processes independent rows but
does not turn the final exit into success. Schema version 1 rejects multiple
rows for the same source issue, preventing one successful write from making a
later row self-stale. Failed-row messages use safe reason categories rather than
raw transport errors, so backend URLs are not copied into the stdout audit.

`atl jira issue field preview <KEY>` and the dry-run form of
`atl jira issue field set <KEY>` share one deterministic single-issue proposal
result. The dedicated preview command is GET-only and available under the
process-wide read-only policy; `field set` is classified as mutating regardless
of flags. The result is:

```json
{
  "key": "PROJ-1",
  "mode": "dry-run",
  "status": "would_apply",
  "expected_updated": "2026-01-02T03:04:05.000+0000",
  "actual_updated": "2026-01-02T03:04:05.000+0000",
  "proposal_hash": "<hex>",
  "fields": [
    {
      "field": "customfield_10001",
      "source": "markdown",
      "kind": "string",
      "bytes": 42,
      "sha256": "<hex>",
      "value": "h2. Progress\n\nOn track."
    }
  ]
}
```

The aggregate proposal hash uses schema v2 and binds the issue key plus the
complete normalized field set, so a review for one issue cannot authorize the
same values on another. The normalized values are intentionally present in JSON stdout for review and
may be private. `-o text` omits them and prints hashes/sizes. Status is one of
`would_apply`, `already_satisfied`, `applied`, `blocked`, `failed`, or `unknown`.
After any PUT error atl performs one fresh reconciliation read. For a
definitive 4xx rejection, proposals already visible are `already_satisfied`
(another actor may have produced the end state); absent/unreadable proposals
are `failed`. An ambiguous transport/timeout/5xx outcome is `applied` when the
proposals are visible and remains `unknown` otherwise (an
immediate old read cannot prove an in-flight write will not commit). Successful
reconciliation reads carry `"reconciled": true`. A stale apply still emits the
`blocked` result and exits 8. Only `field set --apply` can write, and it requires both
`--expected-updated` and `--expected-proposal-hash`. The latter binds sorted
field ids, sources, normalized types, and values; a changed local input fails
before backend metadata/read/write calls. All proposed fields are sent in one
request.

`atl jira issue transition preview <KEY>` and the dry-run form of
`atl jira issue transition <KEY>` emit one state-bound proposal. The result
contains canonical issue identity, mode/status, reviewed transition identity,
current status/update evidence, sorted requested-field current/desired values,
optional reviewed comment evidence, completeness/reconciliation flags, and the
versioned proposal hash. Exact field and comment values are intentionally
present in JSON for review and may be private. `-o text` omits them and prints
only status, issue/transition identity, counts, byte/hash evidence, and the
proposal hash.

Preview is separately classified GET-only and available under the process-wide
read-only policy; the parent transition command is always mutating. Apply
requires `--apply --expected-proposal-hash`, reconstructs the issue, selected
transition, requested-field, and optional complete comment baseline immediately
before at most one exact-id POST, and disables transport retries for that POST.
Every successful or ambiguous attempt gets fresh issue readback and, when a
comment was requested, a complete comment readback. No POST is automatically
replayed, and matching the target status before execution is not treated as
idempotency because a transition is an event.

Status is closed to `would_apply`, `applied`, `not_applied`, `conflict`, and
`unverifiable`. A definitive rejection is `not_applied`. `applied` requires the
exact requested end state and unique optional comment attribution. Divergent or
partially attributable state is `conflict`; failed/incomplete readback is
`unverifiable`. Unsafe outcomes return non-zero after emitting the result and
carry `reconcile_write_outcome` recovery with `retry_safe:false`.

`atl jira issue comment preview <KEY>` and the dry-run form of
`atl jira issue comment add <KEY>` emit one baseline-bound append proposal:
`{key,mode,status,body,body_bytes,body_sha256,actor,current_count,
baseline_sha256,proposal_hash,created?,complete,reconciled?}`. Preview is a
separately classified GET-only command available under the process-wide
read-only policy; `add` remains classified as mutating even in dry-run mode.
The reviewed native Jira-wiki body is present in JSON and may be private.
`-o text` omits body and actor values and emits only status, key, byte count,
and hashes.

The versioned proposal hash binds the target issue, exact validated native
body, stable authenticated Data Center identity, and the complete sorted set of
unique non-empty comment ids. Identical comment text already present is not an
idempotency condition: append remains a new event. Apply requires
`--apply --expected-proposal-hash`, reconstructs the reviewed proposal, and
re-reads the complete baseline immediately before at most one POST. Any local
body or remote baseline drift blocks before POST. A successful or ambiguous
POST gets one complete readback; no POST is automatically replayed.

Status is closed to `would_apply`, `applied`, `not_applied`, `conflict`, and
`unverifiable`. `not_applied` requires a definitive rejection. `applied`
requires a stable newly observed comment identity matching the reviewed body
and actor. Concurrent or duplicate-body evidence that prevents unique
attribution is `conflict`; unavailable/incomplete readback is `unverifiable`.
Unsafe outcomes return non-zero after emitting the result and carry structured
`reconcile_write_outcome` recovery with `retry_safe:false`.

`atl jira issue watchers list <KEY>` emits
`{key,watch_count,is_watching,watchers:[{name,key?,display_name?,active}],
complete,truncated?}`. Jira DC does not paginate this endpoint: completeness
requires every counted watcher to have a returned username. A count/identity
mismatch sets `complete:false`, `truncated:true`, and a stderr warning.

`atl jira issue watchers add|remove <KEY>` is dry-run by default and emits
`{key,operation,mode,status,username,identity_source,current,final?,
proposal_hash,complete,reconciled?}`. Exactly one of an explicit DC
`--username` or `/myself`-resolved `--me` is required. The proposal hash binds
issue, operation, resolved username, and complete current membership. Apply
requires the reviewed hash before `already_satisfied` or one non-retried write,
then verifies membership. Status is `would_apply`, `already_satisfied`,
`blocked`, `failed`, `applied`, or `unknown`; unknown is non-zero and must not
be automatically replayed. Incomplete membership refuses every mutation.

`atl jira issue worklog list <KEY>` emits
`{key,worklogs:[{id,issue_id?,author:{name?,key?,display_name?,active},comment?,
started,created?,updated?,time_spent?,time_spent_seconds}],total,complete}`.
The adapter consumes every advertised page and rejects missing/changing totals,
offset anomalies, empty incomplete pages, and missing/duplicate worklog ids.
Authors are a closed compact projection: email, avatars, self URL, and timezone
are never present. `-o text` is an escaped Markdown table and `-o id` emits one
worklog id per line.

`atl jira issue worklog add <KEY>` is dry-run by default and emits
`{key,mode,status,time_spent,time_spent_seconds,comment?,started?,author,
current_count,baseline_sha256,proposal_hash,created?,complete,reconciled?}`.
`baseline_sha256` is a deterministic digest of the complete sorted worklog-id
set; it exposes no comment or author value. The schema-v2 proposal hash binds
that baseline digest together with the issue key, normalized
seconds/comment/start time, and current compact author identity. Apply requires
the reviewed hash after a fresh complete baseline, sends exactly one non-retried POST with
`adjustEstimate=leave`, and returns `applied`, `blocked`, `failed`, or
`unknown`. An intervening worklog changes both hashes and blocks before POST.
After an ambiguous response, only one exact newly observed match can
prove `applied`, and that proof requires an explicit review-bound `--started`
timestamp. Every other outcome is non-zero `unknown` and must not be
automatically replayed.

`atl jira issue fields <KEY>` emits
`{key,mode,non_empty_only,count,omitted_empty?,summary?,fields:[{id,name,custom,
schema?,empty?,value_type?,value?,truncated?,original_bytes?}]}`. Default mode is `compact`
and omits empty fields. Exact repeatable `--field` selectors accept ids or
case-insensitive display names; ambiguous names fail before the issue read.
Compact user values omit email/avatar/self data, known options/named values use
closed projections, and unknown objects expose only bounded non-empty key names.
Explicit `--include-empty` returns the union of catalog fields and fields
actually observed on the issue, so a populated plugin/private field absent from
the catalog cannot disappear. Explicit `--raw` switches mode
to `raw`, preserves unprojected private values, and writes a privacy warning to
stderr. Explicit `--metadata-only` switches mode to `metadata`, omits `value`
entirely, and emits only the closed coarse `value_type` alongside field
identity/schema/emptiness. It preserves non-empty and `--include-empty`
semantics, including observed plugin fields absent from the catalog, and adds
`summary:{custom_count,system_count,unclassified_count,nonempty_id_count,
missing_id_count,nonempty_ids_unique,value_type_counts}`. Custom and system
counts cover catalog-classified fields; an observed field absent from the
catalog is counted separately as unclassified. Missing ids are kept separate
from uniqueness among non-empty ids. The summary is derived from the returned
array without another backend request. `--metadata-only` conflicts with
`--raw` before config/network access. Its `-o text` table has no value column;
compact/raw keep their existing escaped Markdown table.

`atl jira issue field get <KEY> --field <ID-or-name>` emits one qualified,
bounded expansion:

```json
{
  "schema_version": 1,
  "issue": {"id": "10001", "key": "PROJ-1", "updated": "2026-07-01T10:00:00.000+0000"},
  "field": {"id": "customfield_10002", "name": "Delivery Notes", "custom": true, "schema": "string", "present": true, "empty": false, "value_type": "string"},
  "projection": "compact",
  "max_value_bytes": 16384,
  "original_value_bytes": 24,
  "emitted_value_bytes": 24,
  "complete": true,
  "truncated": false,
  "value": "Current delivery status"
}
```

The command resolves exactly one field and reads it together with Jira
`updated`; a technical id does not require a catalog request and uses the id as
its fallback display name. Missing update provenance, ambiguous names, and malformed
values fail closed. `complete` qualifies the compact projection; properties
deliberately excluded by that projection (email, avatar, self URL, and other
transport noise) are outside the contract. The encoded compact `value` is at
most `max_value_bytes` (default 16 KiB, hard range 256 bytes..128 KiB).
`-o text` emits a one-row escaped Markdown table with issue/update/field/value.

Online Jira get/pull/view field selectors resolve exact names through the same
catalog. Render selectors are stored as resolved ids in view state, so offline
render/apply does not depend on a later metadata lookup. Existing technical ids
remain valid without an extra field-catalog request.

`atl jira issue history <KEY>` emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,history,
summary,last_changes?}`. Each history item preserves both `field` and `field_id`
when Jira supplies them. `summary` is derived from the final filtered `history`
array without another backend request. It contains entry/item totals, non-empty
identity/author/timestamp/field counts, explicit `history_id_missing_count` and
`history_nonempty_ids_unique` facts, emitted non-empty `from`/`to` member
counts, status-item count, multi-item-entry count, stable per-field buckets, and
the `count_matches_history` / `fetched_matches_total` consistency checks. Field
buckets use the case-insensitive technical id when available and otherwise the
trimmed case-insensitive display name, then sort by id/name. Thus
`distinct_item_field_count == len(summary.fields)`.

`history_ids_unique` retains its original compatibility semantics over every
emitted id value, including empty values. Use `history_id_missing_count` to
measure absent ids and `history_nonempty_ids_unique` to detect duplicate
non-empty ids without conflating the two conditions.

`summary.chronological_comparable` is false if any emitted timestamp cannot be
parsed. In that state `chronological_ascending` is JSON `null`, rather than a
misleading false; otherwise it is true for a non-decreasing sequence (including
an empty history) or false for an out-of-order sequence. A true
`fetched_matches_total` alone is not proof of completeness: only top-level
`complete:true` means every entry advertised by the chosen backend
representation was consumed. `complete:false` always carries a reason and must
not be interpreted as proof that an omitted change did not happen.
`source` is `paginated`, `embedded`, or `legacy`. Repeatable exact `--field`
selectors and inclusive `--since`/`--until` boundaries are applied locally
after the qualified read. A date-only boundary adds
`filters.boundary_time_zone`, `boundary_time_zone_source:"jira_current_user"`,
and canonical `since_instant` / `until_exclusive_instant`; atl performs one
current-user metadata GET and uses the observed IANA calendar (including DST).
For each requested civil date, the canonical interval spans from its first real
instant through one second after its last real instant. This includes midnight
gaps, folds, and historical repeated-date transitions without omitting
evidence; an entirely skipped requested date has no truthful boundary and fails
closed with exit 8. The local calculation adds no backend request.
Explicit-offset boundaries add only their canonical instant fields and perform
no timezone lookup. Missing/invalid required user timezone fails closed with
exit 8. `last_changes` reports the newest matching change per
selected resolved field within those boundaries. When a selected matching
change carries an unsupported server timestamp, latest-change ordering is
unknowable and the command fails closed with exit 8 instead of emitting
misleading metadata. `-o text` is a status line and a structurally escaped
Markdown table.

With `--summary-only`, the command performs the same qualified read and emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,summary,
last_changes?}`. The raw top-level `history` member is absent by construction;
the projection neither repeats nor broadens the backend request. Its text
renderer contains deterministic facts and field buckets plus bounded
`last_changes` for explicitly selected fields, never the raw history rows.
Omitting the flag preserves the full JSON and text output byte contract.
An explicitly supplied false value, including a later duplicate override, is
rejected with exit 2 before backend access; callers must omit the flag to
request the full raw-history contract. Typed MCP `jira_issue_history` returns
this same summary projection unconditionally.

`jira epic digest` exposes the same fields under `period`. A quarter is resolved
once in the Jira current-user calendar and the resulting zone is passed into
the nested history filter, so a digest adds at most one current-user GET rather
than one per evidence source. Raw user JQL is not changed by either workflow.

`atl jira export ... --out -` is an artifact stdout mode, not a command-result
mode. JSONL emits one `JiraIssueSnapshot` per line, aggregate JSON emits a bare
snapshot array, and CSV emits its header and rows. It emits no manifest, export
result envelope, or trailing status bytes and creates no files. Diagnostics are
stderr-only. `--format`, not the global output flag, selects those artifact
bytes; `-o text` with `--out -` is rejected with exit 2. Aggregate JSON retains
the 10,000-issue/64 MiB caps; row formats
retain the identity cap and safe-CSV default. Because a late read/write failure
can leave a streamed prefix on stdout, consumers must accept the artifact only
when the process exits zero. File destinations retain the existing atomic
artifact plus `<out>.manifest.json` contract. Exact field display names are
resolved before search and exported under stable field ids.

`atl conf page resolve <ID-OR-URL>` emits
`{id,kind,via?,network_requests,space?,title?}`. `kind` is `id`, `canonical`,
`viewpage`, `rest`, `display`, or `short`; a short link records the final parsed
form in `via`. `network_requests` is zero for direct identity-bearing forms,
one for exact display search or an id-bearing short-link target, and at most two
when a short link ends at an exact display URL. `-o id` and `-o text` emit only
the resolved id. Same-origin/context validation happens
before a request; ambiguous display matches and unsupported/malformed redirect
targets fail closed. Read-only page consumers accept the same references but
continue to emit the backend's stable page id in their existing result shapes.

`atl conf page outline <REF>` emits
`{schema_version:1,id,title,space,version,count,total,complete,truncated?,
partial_reason?,original_bytes,emitted_bytes,
headings:[{index,level,title,path,occurrence}]}`.
The 1000-heading and 262144-byte structural caps are explicit:
`count`/`emitted_bytes` describe emitted records and `total`/`original_bytes`
describe parsed records. `partial_reason` is `heading_limit` when the
1000-heading cap stopped emission first and `byte_limit` when the 262144-byte
cap did. `-o text` is an indented Markdown list. Macro/code/table-contained
headings are not entries.

`atl conf page section <REF> --heading ...` emits
`{schema_version:1,id,page_title,space,version,page_version_gated,heading,level,
path,occurrence,markdown,complete,truncated?,partial_reason?,original_bytes,
emitted_bytes}`. Duplicate normalized titles require an explicit 1-based
`--occurrence`. The section includes
descendant headings and ends before the next same/higher-level heading. The byte
cap is applied at rendered block boundaries; `complete:false,truncated:true` is
never a complete section. `partial_reason` is `max_bytes` when a whole rendered
block did not fit the requested bound and `invalid_utf8` when the rendering was
withheld entirely. `-o text` emits only `markdown`. No mirror artifact or
writeback base is created.

`atl conf page sections <REF> --heading ... [--heading ...]` emits
`{schema_version:1,id,page_title,space,version,page_version_gated,
requested_count,returned_count,reconciled,complete,truncated?,original_bytes,
emitted_bytes,max_bytes,sections:[{heading,level,path,occurrence,markdown,
complete,truncated?,partial_reason?,original_bytes,emitted_bytes}]}`. It accepts
1..32 ordered selectors and resolves all of them against one fetched and parsed
page snapshot before returning a result. When any repeatable `--occurrence` is
present, exactly one non-negative value must accompany each heading; zero keeps
the unique-heading rule. The aggregate bound is allocated deterministically in
request order by dividing remaining bytes among remaining selectors and
carrying unused emitted capacity forward. Therefore
`sum(sections[].emitted_bytes) == emitted_bytes <= max_bytes`; aggregate byte
totals equal the per-section sums. `reconciled` is true only when requested and returned counts match, and
aggregate `complete` is true only when the counts reconcile and every section
is complete. `-o text` concatenates section Markdown in request order without
transport-added separators.

All three structural commands stamp the same `schema_version:1`: outline,
section, and sections are one selection protocol, so a consumer must not
validate one shape against another's contract. All three also reconcile page
identity before parsing or rendering any body — a response whose content id does not match the resolved reference, or
whose version is not a positive integer, fails closed with exit `8` instead of
producing an unattributable result.

`--expected-version <N>` on `page section` and `page sections` is an optional
provenance binding,
and `page_version_gated` reports the outcome as a member that is always present.
A positive value refuses the read with exit `8` unless the page is currently at
exactly that version, reporting only the expected and current integers, and
returns `page_version_gated:true` on a match. `0` (the default) or an omitted
flag leaves the read ungated and returns `page_version_gated:false`; a negative
value is a usage error (exit `2`). The check reuses the page response the read
already fetched, so it costs no additional backend request and adds no write
capability.

Heading `occurrence` and `path` are positional, so which section a selection
resolves to depends on the revision it is resolved against. Pass the exact
`version` from the `page outline` result whenever the heading, path, or
occurrence came from that outline, and the exact `version` from the first
section result when re-reading the same selection at a wider `--max-bytes`
bound. A selection fixed outside any earlier read has no earlier revision to
reconcile, so it may omit the flag: an ungated result is still exact evidence
for the revision named in its own `version`, but it reconciles no earlier
selection, and `page_version_gated:false` is the signal that it does not.

The partial reasons are a closed set of static identifiers that never
interpolate a heading, page id, title, space, URL, body, or caller value. For
an outline, a single section, or each entry in a plural result,
`partial_reason` is absent exactly when `complete` is `true` and present exactly
when it is `false`, so a client can branch on the limiter
without parsing `markdown`. Only `max_bytes` permits a recovery attempt:
re-read the same reference, heading, and occurrence once with `--max-bytes` at
or above the reported `original_bytes` (and within the 1048576-byte cap).
`original_bytes` is the exact minimum bound for the same valid rendering. Bind
that second read with `--expected-version` set to the `version` the first
section result returned, so a page that moved in between is refused with exit
`8` rather than answered from a body the first result never described, and
accept the recovery only when it also reports `complete:true`.
For `page sections`, aggregate `original_bytes` is a sum, not the exact bound
that makes the order-dependent allocator complete. Recover a required partial
entry once with singular `page section`, that entry's exact `original_bytes`,
and the plural result's `version`; do not retry the plural request until it
happens to fit.
`heading_limit`, `byte_limit`, and
`invalid_utf8` are terminal for these commands. A partial result is never
evidence of absence and never establishes a decision.

`atl jira epic digest <KEY>` emits schema v1 with
`{schema_version,period,includes,sources,epic,status_field?,dod_field?,children?,
comments?,links?,blockers?,history?,refs?,confluence?,staleness,warnings?}`.
`sources` qualifies each attempted component with `complete`, returned `count`,
optional `count_truncated`/`text_truncated`, and a bounded `warning`;
optional-source failure is never encoded as an empty complete result. Reference
completeness includes description, selected status/DoD fields, and comments
whenever those values contribute source text. `children.list` is the common
IssueList contract.
`staleness` contains `evaluated`, `stale`, selected status-field timestamp,
latest newer evidence timestamp, child/comment counts, and deterministic
reasons. It is evidence, not a score. Quarter/date boundaries are inclusive.
Component count/text/request caps and bounded Confluence `page section` results
remain explicit. Each `confluence[].section` uses the section shape above,
including `schema_version:1` and `page_version_gated:false`: the digest's
heading is fixed by its request rather than selected from an outline. Links use
a total `(key,type,type_name,direction,id)` order.
`-o text` renders source completeness, selected status text,
and child distribution without inventing narrative conclusions.

With `--projection compact`, the same schema additionally contains
`projection:{name:"compact",omitted:[],clipped:[]}` and summary objects for
comments, links, history, and refs. Raw collection members named in `omitted`
are absent; children retain aggregate counts but omit `children.list`.
`clipped` describes projection-level context reduction, independently of the
source-level `complete` and `*_truncated` signals. Consumers must inspect both:
projection clipping is not evidence-source truncation, and neither can be
interpreted as proof of absence. The default `full` JSON remains unchanged.

List-oriented Jira reads (`issue search`, `issue children`, `board
issues/backlog`, and `sprint issues`) share one app-layer contract:

```json
{
  "schema_version": 1,
  "source": {"kind": "board", "id": "5"},
  "selection": {"scope": "board", "jql": "status in (11,12)"},
  "projection": {
    "columns": ["position", "key", "summary", "status", "board.column"],
    "fields": ["summary", "status"],
    "ordering": "backend-rank",
    "view": "default"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "values": {"summary": "First", "status": "Open"},
    "context": {"board": {"column": "To Do", "in_board": true, "in_backlog": false}}
  }],
  "page": {"count": 1, "complete": true, "truncated": false, "next_cursor": null}
}
```

`rows` is always an array. Identity/order fields are fixed; selected Jira fields
live under `values`, and source semantics stay namespaced under `context`.
`projection.fields` exactly names `values`; `projection.columns` preserves the
requested human order. `--columns` derives backend fields and accepts common
identity, Jira field ids, and source-specific names such as `board.column` or
`sprint.id`. Unknown/foreign context columns fail with usage. `-o text` renders
the same rows as one safe Markdown table (or `_None._`); `-o id` prints keys.
The page cursor is `null` at exhaustion and resumable only when non-null.
Ordinary JQL search pages qualify exhaustion from Jira's paging coordinates.
An empty page is complete only when those coordinates prove that no remainder
exists. When Jira advertises more results but returns no rows, the page is
`complete:false`, `truncated:true`, has `next_cursor:null`, and carries
`partial_reason:"pagination_stalled"`; inconsistent paging coordinates use
`pagination_unqualified`. Compatibility tracker implementations that do not
expose qualification use `legacy_unqualified`. These are the only non-empty
search-page partial reasons, and they never contain backend text. A resumable
page with a non-null cursor is incomplete but omits `partial_reason` because the
continuation itself identifies the next safe action. Board, backlog, sprint,
and epic-child page qualification is unchanged.
For board pages, top-level `position` is the zero-based position within the
returned page; ordering is backend rank, but ATL does not expose that index as
a durable Jira rank value.

`projection.view` is `default`, `full`, a configured custom name, or
`explicit` when `--columns`/`--fields` supplied the projection. Applicable
commands accept `--view`; explicit projection flags win. Effective config
always exposes source-specific built-in `default` and `full` entries under
`jira_list_views`; custom entries inherit default arrays they omit. Unknown
views or context columns invalid for the selected source fail with usage before
network access.

`jira issue children <EPIC-KEY>` returns `source.kind:"epic"`, records the
parent key and resolved Epic Link field under `selection`, and namespaces
`parent` plus relation `epic-child` under `rows[].context.epic`. It resolves
field metadata once and executes one paginated generated JQL request; it does
not read every child individually. Its default columns are
`key,summary,status,issuetype,assignee`. The generated epic-children and
subtasks sections in transient/durable issue Markdown use the same table
renderer in embedded mode; an empty related list is `_None._`.

`atl jira board config <ID>` returns the workflow projection used to interpret
board issues:

```json
{
  "id": 5,
  "name": "Quarter plan",
  "type": "kanban",
  "filter_id": "42",
  "kanban_subquery": "fixVersion is EMPTY",
  "constraint_type": "issueCount",
  "columns": [
    {"name": "To Do", "status_ids": ["11", "12"], "max": 7},
    {"name": "Done", "status_ids": ["13"]}
  ],
  "rank_field_id": "10019"
}
```

`board issues` and `board backlog` return one explicit common IssueList page.
The backend request may include `status` when board column context needs its id,
without adding an unrequested value to `projection.fields`. The backlog issue
endpoint is Scrum-only; `board backlog` refuses a Kanban board after reading its
configuration and before calling the incompatible endpoint.

`atl jira board view <ID>` returns a normalized multi-page snapshot:

```json
{
  "schema_version": 1,
  "board": {"id": 5, "name": "Quarter plan", "type": "kanban", "columns": []},
  "scope": "all",
  "projection": {
    "kind": "jira-fields-v1",
    "columns": ["position", "key", "summary", "status", "board.column", "assignee"],
    "fields": ["summary", "status", "assignee"],
    "ordering": "backend-rank"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "board_position": 0,
    "in_board": true,
    "in_backlog": false,
    "status_id": "11",
    "status": "Open",
    "column": "To Do",
    "column_index": 0,
    "column_mapped": true,
    "values": {"summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "complete": true,
  "truncated": false,
  "backlog_fetched": false
}
```

When `board view` receives `--epic-field <exact-field>` and at least one
`--done-status`, it adds an optional deterministic aggregate:

```json
{
  "epic_rollup": {
    "epic_field": "customfield_10001",
    "done_statuses": ["Done"],
    "complete": true,
    "epics": [{
      "key": "PROJ-10",
      "parent_present": true,
      "child_count": 2,
      "done_child_count": 1,
      "status_counts": [
        {"status": "Done", "count": 1},
        {"status": "In Progress", "count": 1}
      ],
      "latest_child_updated": "2026-06-20T10:00:00.000+0000",
      "timestamped_children": 2,
      "missing_updated_children": 0,
      "timestamp_coverage_complete": true
    }]
  }
}
```

The exact epic field and `updated` must both occur in the selected projection.
Done statuses are matched case-insensitively, case-insensitive duplicates are
rejected, and accepted values are emitted in deterministic order. Epic keys and
status records are sorted lexically. The aggregate uses only rows in this
snapshot and does not fetch children separately. Its `complete` is false when
the snapshot is incomplete, a referenced parent is absent, or a child lacks
`updated`. A backend relation must be an exact non-empty string or an object
with an exact non-empty string `key`; arrays, scalar non-strings, and objects
without that key fail check validation. Malformed timestamps fail the same
way. With no rollup options the field is omitted, preserving the existing JSON
shape.

Rows from board scope retain backend rank order. For Scrum `scope:all`, backlog
membership and backlog position are joined by issue key; backlog-only issues
are appended in backlog order. For Kanban, `scope:all` reads board scope only,
sets `backlog_fetched:false`, and never calls backlog or sprint endpoints.
Unknown status ids use `column:"Unmapped"`, `column_index:-1`, and
`column_mapped:false` rather than disappearing.

`--limit 0` follows pagination to exhaustion. A positive limit applies per
requested scope; when more rows exist the output sets `complete:false` and
`truncated:true`. Negative aggregate limits are usage exit 2 before any request
or output-file creation. Repeated issues across pages, a non-advancing cursor, or the
pagination safety cap return check-failed (exit 8). There is no board snapshot
version in Jira's API, so `complete` means all reported pages were consumed,
not that concurrent board changes were transactionally excluded.

`board export --format json|jsonl|csv|md` writes the existing row projection
and does not accept or emit the optional view-only epic rollup. JSONL
repeats compact board identity, projection, row count, and completeness with each row. CSV contains rank,
scope membership, status/column mapping, and selected fields; formula-leading
cells are neutralized unless `--raw-csv` is explicitly approved. Markdown is a
compact review table rendered by the same primitive as other issue lists. None
of these read paths call rank, sprint, move, or issue
write endpoints.

`atl jira structure folders <ID>` is the fast stored-folder index. It fetches
metadata, one forest, and one batched folder-label value projection; it never
searches Jira issues:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Planning", "read_only": false},
  "forest_version": {"signature": 10, "version": 2},
  "folders": [{
    "folder_id": "100",
    "row_id": 500,
    "name": "Quarter",
    "path": ["Plans", "Quarter"],
    "depth": 1,
    "parent_folder_id": "99",
    "stats": {"descendant_rows": 86, "issue_rows": 72, "unique_issues": 70, "subfolders": 2, "max_relative_depth": 4}
  }],
  "complete": true,
  "warnings": []
}
```

`structure.read_only` is always present, including when it is `false`, so a
known mutable Structure is not confused with missing metadata. Folder `name`
and `parent_folder_id` are also always present strings: a missing label is
`name:""` while `path` uses the stable `folder:<id>` fallback, and a root folder
has `parent_folder_id:""`. Consumers must not substitute the fallback path into
the empty semantic name. `-o id` emits stable folder item ids, not row ids.
Missing/partial labels keep technical ids and statistics, set `complete:false`,
and add bounded warnings.

`atl jira structure rows <ID>` returns a parsed read-only view of a Tempo Structure forest:

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

For non-root rows, `parent_row_id` is present. `-o id` prints Structure row ids
one per line. `--root` emits the first matching row plus descendants; matching is
by row metadata first and then by Structure values fetched through
`--root-fields` (default `key,summary`).

`forest_version_gated` is always present and is `true` only when the caller
supplied the exact expected forest pair described under `structure view`; the
existing `version` member still reports the forest the rows were parsed from.

Rows/view/pull-issues/export also accept one mutually exclusive exact selector:
`--folder-id`, `--folder-row`, or `--folder-path`. Exact selectors verify a
stored folder in the same forest snapshot, never fall back to fuzzy matching,
and return not-found or check-failed on absence/ambiguity. Results include
`selection`; selected rows retain absolute `depth` and `parent_row_id` and add
`relative_depth` beginning at zero. `--folder-id` is the durable agent path;
`--folder-row` is snapshot-local and path selection requires complete labels.
Path comparison is case-insensitive and collapses whitespace in every segment;
folder names containing a literal `/` require id/row selection. `complete`
describes the emitted subtree: unrelated missing labels elsewhere in the forest
do not make an id/row/root-selected view partial.

`atl jira structure values <ID> --rows ... --fields ...` preserves the backend
value matrix under `responses` and `raw`; if the backend reports permission
gaps, normalized row ids are also exposed as `inaccessible_rows`. The field is
always present; when there are no reported gaps it is `[]`.

`atl jira structure view <ID>` returns a normalized snapshot:

```json
{
  "schema_version": 1,
  "structure": {"id": 123, "name": "Quarter plan", "read_only": true},
  "forest_version": {"signature": 55, "version": 7},
  "forest_version_gated": false,
  "projection": {
    "kind": "jira-fields-v1",
    "source": "list-view",
    "attributes": ["key", "summary", "status", "assignee"],
    "browser_view_reproduced": false
  },
  "rows": [{
    "row_id": 100,
    "depth": 0,
    "item_type": "issue",
    "item_id": "10001",
    "position": 0,
    "accessible": true,
    "values": {"key": "PROJ-1", "summary": "First", "status": "Open"}
  }],
  "row_count": 1,
  "issue_count": 1,
  "complete": true,
  "inaccessible_rows": [],
  "warnings": []
}
```

`projection.source` is `list-view` for the built-in default, `full`, and custom
named views; it is `explicit` when `--fields` wins. The selected preset name is
reported separately as `projection.view`.

Every successful snapshot keeps `schema_version:1` and always carries
`forest_version` — the forest the snapshot was assembled from — and
`forest_version_gated`. `forest_version_gated:true` means the caller supplied
that exact pair through the paired `--expected-forest-signature` and
`--expected-forest-version` flags, and the snapshot's `forest_version` then
equals it. `view`, `rows`, `pull-issues`, and `export` accept the same paired
flags; `get`, `forest`, `folders`, and `values` accept none. Omitting both flags
is an explicitly ungated read. If either is supplied both are required, the
signature must be non-zero, and the version must be positive; an unpaired,
zero, or non-positive pair is a usage error and exits `2` before any backend
request. A supplied pair that does not match exits `8`: the comparison runs on
the initial forest read, before stored-folder labels, Structure Value or Jira
issue expansion, export rendering, and before any `--out` file is created, so a
stale gate leaves no partial local artifact. The diagnostic carries only the
expected and current signature/version integers. There is no second forest
request and no final re-read. A returned pair with either member zero is
non-bindable: do not pass it as an expected pair, and treat a later selection as
explicitly ungated. Copy
both non-zero members of a returned `forest_version` (`version` on `rows` and
`pull-issues`) into a later call
whenever its `--folder-id`, `--folder-row`, or `--folder-path` selector came
from an earlier `view`, `folders`, or `rows` result; a selector fixed outside
any earlier read may omit them and is then explicitly ungated evidence. The
forest version qualifies the hierarchy and the selection only — Jira issue
fields and stored folder labels are separately timed and are not covered by it,
so a gated result is not one atomic versioned value snapshot. The `-o text`
header states the signature, version, and gated facts alongside the projection
and row count.

`-o text` renders emitted `#`, numeric Depth (relative when selected), technical
Type/Item, separate Jira value columns, and Access. It does not duplicate key
and summary in a combined Tree cell or dump raw Jira objects/transport URLs.
Known Jira objects use their human label/name; an unknown non-empty object is
shown as `[object]` so it cannot be mistaken for a missing value without leaking
transport internals.
`-o id` emits row ids. The default attributes are shown above; explicit
`--fields` selects Jira fields and changes both `projection.attributes` and row values. Browser saved
views are deliberately not claimed as the source because Structure's supported
integration API does not expose a stable saved-view column projection.

Issue values are joined only for rows whose type is `issue`, using the forest's
stable numeric issue `item_id` through Jira search, not by Structure row id.
Structure's generated identity join disables Jira's advisory strict-query
validation so one deleted or hidden id cannot reject an otherwise readable
batch; ordinary user-authored JQL remains strict, and Jira parsing and
permission filtering still apply. Issues unavailable to the current
token/read remain usable but visible as gaps: `complete` is false, affected rows have
`accessible:false`, and their ids are listed in `inaccessible_rows`. Stored
folder summaries are best effort; calculated grouping/generator rows retain
their technical identity instead of risking a misleading label.

`issue_count` describes unique issue identities in the final emitted
root/subtree scope rather than the unfiltered forest. Structure may regenerate
row ids for calculated rows without changing the
expanded plan. Treat `row_id` and `parent_row_id` as snapshot-local identities;
issue keys and item ids remain the durable correlation keys.

`atl jira structure pull-issues <ID>` returns:

```json
{
  "structure_id": 123,
  "version": {"signature": 55, "version": 7},
  "forest_version_gated": false,
  "rows": [],
  "issue_ids": ["10001"],
  "issues": [{"key": "PROJ-1", "id": "10001", "fields": {}}],
  "count": 1
}
```

`forest_version_gated` is always present. When it is `true`, the stale-pair
check already ran on the initial forest read, so the Jira issue search and any
`--out` snapshot file happen only after the hierarchy matched the expected
forest. The pair still says nothing about the separately timed Jira fields
collected afterwards.

`atl jira structure export <ID> --out FILE --format json|jsonl|csv|md` writes the
artifact and returns a small result object:

```json
{
  "path": "structure.json",
  "format": "json",
  "structure_id": 123,
  "forest_version": {"signature": 55, "version": 7},
  "forest_version_gated": true,
  "row_count": 1,
  "issue_count": 1
}
```

`forest_version` and `forest_version_gated` are always present in the command
result, so an export is auditable without reopening the artifact. JSON and
Markdown contain the same normalized snapshot as `structure view`; Markdown
states that signature, version, and gated value in its header note. JSONL has
one self-contained record per row, including schema, structure id,
`forest_version`, `forest_version_gated`, projection, and
row, which makes line-oriented filtering safe. CSV
contains row metadata (`row_id`, `depth`, `relative_depth`, `parent_row_id`, `position`,
`item_type`, `item_id`, `accessible`) plus selected Structure attributes. CSV headers and
cells are unchanged by the gate, so a CSV export carries this provenance only in
the command result above. CSV cells use the
same default formula neutralization as `jira export`; `--raw-csv` disables it
only for CSV and is unsafe for spreadsheet use. Use `pull-issues` separately
when raw Jira issue snapshots are required.

With `-o text`, the command-result line reports `format`, `forest_signature`,
`forest_version`, `gated`, `rows`, and `issues` after the output path, so CSV
provenance remains visible in either output mode.

`atl manifest create --root DIR` writes a backend-identity-hashed local manifest and returns
the written path plus the manifest body:

```json
{
  "path": "mirror/manifest.json",
  "manifest": {
    "created_at": "2026-01-01T00:00:00Z",
    "command": "atl manifest create",
    "root": "mirror",
    "service": "jira",
    "selectors": ["jql=project=PROJ"],
    "fields": ["summary", "status"],
    "counts": {
      "files": 2,
      "bytes": 42,
      "extensions": {
        ".json": 1,
        ".md": 1
      }
    },
    "backend": [
      {
        "service": "jira",
        "url_hash": "sha256:..."
      }
    ],
    "atl_version": "0.2.0",
    "elapsed_ms": 1
  }
}
```

Configured backend entries contain URL hashes only; `atl` does not read or add
stored PATs to this artifact. Caller-provided `command`, selectors, JQL/CQL,
fields, include values, and paths are preserved verbatim and are **not
redacted**. Never pass credentials in that metadata, and review the manifest
before publishing it.
