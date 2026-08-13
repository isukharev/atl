# Read-only MCP server

`atl mcp serve` exposes a deliberately small typed evidence surface over MCP
stdio. It calls the same application services as the CLI; it does not run shell
commands, create mirror files, or register any mutating tool.

Use MCP for transient agent reads where a typed result is cheaper and safer
than teaching a model to construct shell commands. Two local-only tools also
summarize an existing durable mirror without exposing its paths or content.
Keep the CLI for mirror creation/content/status/diff, raw Structure
forest/values, exports, offline diff/plan workflows, and all guarded writes.

## Protocol eras and cache contract

The server uses MCP Go SDK v1.7 and deliberately supports both protocol eras.
Modern clients use stateless `2026-07-28` discovery and can call `tools/list`
without `initialize`. Legacy clients retain the `2025-11-25`
`initialize` → `notifications/initialized` handshake. A future unsupported
version receives the structured `UnsupportedProtocolVersion` error with the
requested version and ATL's supported versions; the server does not present
that response as a legacy peer.

The inventory is one closed, non-paginated page. Every `tools/list` result
includes `ttlMs:0` and `cacheScope:"public"`: clients should treat it as
immediately stale, and the inventory contains no user-specific state. The
legacy result has exactly `tools`, `ttlMs`, and `cacheScope`; the modern wire
also carries the modern completion and server metadata required by that era.
`resources/list` and `resources/read` use the same envelope: their legacy
payload member is respectively `resources` or `contents`, and modern results
add only `resultType:"complete"` plus server `_meta`.

## Closed service profiles and capability resource

The default command keeps the complete twenty-three-tool catalog and its existing
instructions:

```bash
atl mcp serve
```

When a session needs only one reviewed service boundary, select a closed
profile:

```bash
atl mcp serve --service jira
atl mcp serve --service confluence
atl mcp serve --service offline
```

## Plugin-to-binary startup contract

The committed Claude Code and Codex plugin definitions start the same command
with two hidden generated markers: an interface-contract version from the
binary's compiled contract owner and a product version derived directly from
the consuming plugin manifest. The interface contract, not product-version
equality, decides startup compatibility.

An incompatible marked invocation exits `2` as a content-free usage error
before config, credentials, dependency construction, or network access, with no
protocol bytes on stdout. A compatible interface continues when the plugin and
binary product versions differ. The startup gate does not emit the product
`match` or `mismatch` status at runtime; compare `atl version` with the
installed plugin or manifest version when diagnosing skew.
MCP `serverInfo` is only the running server's self-reported name/version and is
not treated as verified plugin, marker, or executable identity.

Both generated plugin definitions set exactly the public per-server
`CODEX_MCP_PROTOCOL_VERSION=2026-07-28` environment marker in addition to the
two startup arguments. For Codex 0.147, modern mode requires that marker and
the user-controlled, under-development global `mcp_2026_07_28` feature. Marker
only or feature only remains on the legacy handshake. The plugin supplies only
the per-server marker and cannot enable the user's global feature. This marker
selects Codex client behavior; it does not authenticate ATL or prove plugin,
binary, or package provenance.

A newly generated plugin used with an older binary fails through ordinary
unknown-flag parsing. Bare standalone invocation remains supported. Therefore
an older unmarked plugin used with a newer binary is indistinguishable from
standalone use and remains explicitly `unverified`; atl does not claim a
symmetric fail-closed guard. Update the older side and restart the agent session
when marked startup is refused or product versions are known to differ.

The closed profiles expose 11/12/2 tools for Jira/Confluence/offline
respectively. `offline` exposes only
`jira_mirror_snapshot` and `confluence_mirror_snapshot` and constructs neither
backend. The flag is not an arbitrary allowlist: unknown or repeated values
fail before dependency construction. Scoped instructions retain the common
read-only, untrusted-evidence, completeness, no-shell, and no-arbitrary-file
rules while mentioning only tools present in that profile.

Every profile advertises one fixed `application/json` resource at
`atl://capabilities`. It returns only static capability identity and ordering,
the CLI command, an optional bounded MCP route and its scope, and the explicit
CLI-only fact. It accepts no arguments and reads no config, credentials,
backend, mirror path, or user content. A mapping is not full CLI equivalence:
for example, Jira reference and history mappings are summary projections and
do not return raw URLs or changelog rows.

## Tools

The v1 surface is an explicit allowlist:

| Tool | Purpose | Important bound |
|---|---|---|
| `jira_fields` | Discover field ids or request a content-free catalog summary | explicit completeness/reconciled counts; `summary_only`; default 256 KiB, maximum 1 MiB encoded result |
| `jira_issue_search` | Read one compact IssueList page | default 50/maximum 1000 rows; default 256 KiB/maximum 1 MiB encoded result |
| `jira_issue_field_get` | Expand one exact compact field with issue/update provenance | default 16 KiB, maximum 128 KiB encoded value |
| `jira_issue_history` | Summarize one issue's changelog without raw history rows | summary projection only; default 256 KiB/maximum 1 MiB encoded result |
| `jira_issue_graph` | Build one full-v2 or compact-v1 qualified work-artifact graph from an exact issue | full default; Jira-only depth 0..2; fixed 16 MiB backend-response bound; default 256 KiB/maximum 1 MiB encoded result; no Confluence resolution; optional experimental Development SCM coordinates |
| `jira_issue_refs` | Summarize qualified issue references without raw URLs or narrative | one key or JQL limited to 25 issues; at most 8 technical field ids; default 256 KiB/maximum 1 MiB encoded result |
| `jira_epic_digest` | Aggregate selected qualified epic evidence | `projection:compact`; default 256 KiB/maximum 1 MiB encoded result |
| `jira_board_view` | Freeze one board/backlog membership snapshot | default 200/maximum 1000 rows per scope; default 256 KiB/maximum 1 MiB encoded result |
| `jira_structure_get` | Read compact metadata for one exact Structure id | accepts a positive integer or canonical decimal string id; 32 KiB result cap; omits owner, permissions, saved views, and raw forest data |
| `jira_structure_view` | Read a normalized full Structure or exact stored-folder subtree | optional paired `expected_forest_signature`/`expected_forest_version` binding; default 200/maximum 1000 emitted rows; maximum 1000 scanned forest rows; default 256 KiB/maximum 1 MiB encoded result |
| `jira_mirror_snapshot` | Summarize local Jira mirror health without content | no arguments; exact owner-configured root; offline fixed-shape counts |
| `confluence_search` | Search one qualified bounded CQL candidate page | default 25/maximum 100 rows; default 128 KiB/maximum 1 MiB encoded result |
| `confluence_page_resolve` | Resolve an id or same-origin URL/path | exact resolution only |
| `confluence_page_meta` | Read body-free page governance metadata | fixed 32 KiB result cap; explicit tri-state restriction state; no URL, labels, ancestors, principals, or body |
| `confluence_comment_list` | Discover qualified comments without returning bodies | positive canonical page id; optional provenance version gate; fixed 32-comment-page cap; default/maximum 100/1000 items and 128 KiB/1 MiB encoded result |
| `confluence_comment_thread` | Expand one exact qualified thread as plain text | positive canonical page/comment ids; optional provenance version gate; fixed 32-comment-page cap; default/maximum 100/1000 items and 256 KiB/1 MiB encoded result |
| `confluence_page_outline` | Inspect headings before reading content | one page |
| `confluence_page_section` | Read one exact Markdown section | optional `expected_page_version` binding; default 32 KiB, maximum 1 MiB |
| `confluence_page_sections` | Read 1..32 ordered Markdown sections from one page snapshot | optional `expected_page_version` binding; default 256 KiB aggregate content, maximum 1 MiB; independent encoded-result ceiling |
| `confluence_attachment_list` | Qualify one page's attachment inventory | requires a positive `expected_page_version`; metadata only; default 128 KiB, maximum 1 MiB encoded result |
| `confluence_table_summary` | Inspect content-free table structure | reports page version; default 128 KiB, maximum 1 MiB encoded result |
| `confluence_table_extract` | Read one exact expanded table | selected table required; summary-derived indexes require its version; default 256 KiB, maximum 1 MiB encoded result |
| `confluence_mirror_snapshot` | Summarize local Confluence mirror health without content | no arguments; exact owner-configured root; offline fixed-shape counts |

`jira_epic_digest` requires an explicit non-empty `include`; unlike the CLI it
never interprets omission as permission to fetch every default evidence source.
Set `projection:"compact"` for normal synthesis. The typed result preserves
source completeness and exposes every omitted/clipped path. When a required
narrative field is clipped, use `jira_issue_field_get`; do not repeat the whole
digest with `projection:"full"`.

`jira_fields` returns `schema_version`, `projection`, `source`, `complete`,
optional `partial_reason`, source `total`, filtered `count`, reconciled
`custom_count`/`system_count`, and value-free field definitions. Set
`summary_only:true` when only qualification and counts are needed; the result
uses `projection:"summary"` and an empty `fields` array. Filters apply before
the partition counts, so `custom_count + system_count == count`. Treat an empty
match as evidence of absence only when `complete:true`; a successful tool call
or non-empty match is not itself a completeness signal.

`jira_issue_history` answers changelog questions without shipping the changelog.
It requires `key` and returns only the deterministic summary projection: the
issue key, `complete`, `source`, `total`, `fetched`, `count`, an optional
`partial_reason`, the resolved `filters`, the `summary` cardinality and
consistency facts, and `last_changes`. The raw `history` array is absent by
construction, so the model reads counts and reconciliation facts instead of
redoing changelog arithmetic. `complete:false` always carries a reason and
never proves that an omitted change did not happen; a true
`summary.fetched_matches_total` alone is not a completeness signal. Pass
repeatable exact `fields` (technical ids after one `jira_fields` lookup, or
unambiguous display names) to also receive the newest matching change per
selected field in `last_changes`; without a field selection that member is
absent. Use a task-supplied technical field id directly; otherwise qualify an
unambiguous selector once with `jira_fields`. Technical ids are resolved
locally; a display-name selector makes one Jira field-catalog request inside
the history call before reading the changelog. Optional inclusive
`since`/`until` boundaries accept a date in the Jira user calendar or an
explicit timestamp and are applied locally. Civil dates may first make one
current-user request to resolve the Jira user's timezone; explicit timestamps
need no calendar lookup. A display-name selector and a civil date can therefore
add both metadata requests. There is no raw-history selector and no projection
mode: when individual changes are themselves the required evidence, use
`atl jira issue history` in the CLI.

`jira_issue_graph` builds the same provenance-qualified graph as the CLI's
direct Jira route. It requires one canonical `key`; optional `depth` from 0
through 2 follows only exact structured Jira relations. Omitted or explicit
`projection:"full"` returns the existing schema-v2 bytes and request sequence.
`projection:"compact"` returns schema v1 and accepts the same closed
`urls|scm|none` selector contract as the CLI through a `select` string array.
With no selector, compact selects URLs and also selects SCM only when
`include_development:true`; explicit SCM selection requires that opt-in. `none`
returns qualification without facts and cannot be combined with another
selector. Invalid combinations fail before Jira client construction or network
access.

```json
{
  "key": "PROJ-1",
  "projection": "compact",
  "select": ["urls"]
}
```

MCP v1 is Jira-only and intentionally has no `resolve` or
`resolve_confluence` input:
discovered Confluence page identities remain qualified stubs, and resolving
their id/title metadata requires the CLI. There is also no `strict` input;
inspect top-level `complete`, every requested source, the reconciliation
summary, transport usage, and the bounded `frontier` directly.

Compact selection runs only after the full graph is collected and passes the
existing fail-closed MCP full-graph validation/sanitization gate. The shared
compact projector then independently excludes Development-node URLs. It
preserves root/completeness/truncation, all collection and transport bounds,
incomplete sources, frontier, warnings,
`projection.selected`/`projection.omitted`, and reconciled source/fact counts.
URL facts come only from canonical URL nodes; opaque facts omit `url` rather
than reconstructing it. SCM facts contain coordinates only and never a GitLab
web URL. A requested Development source retains its status and count when empty
or incomplete.

Development remains absent, without implying zero activity, when
`include_development` is omitted or false. Set `include_development:true` only
when exact project, commit, branch, or merge-request identity is required. The
additional source, nodes, edges, and evidence retain `experimental_api`
stability. The closed SCM object contains a validated lowercase `host`,
`project_path`, and exactly the selector appropriate to the node kind: full
`commit_sha`, exact `branch_name`, or `merge_request_iid` together with its
closed `merge_request_state`. Project nodes carry no artifact selector. GitLab
nodes are unexpanded stubs, do not enter traversal, and their URLs are omitted
from MCP output. Narrative, people, email, avatars, files, diffs, timestamps,
query values, labels, and raw plugin payloads are not part of the projection.

`max_nodes` defaults to 50 and caps at 100, `max_edges` defaults to 200 and caps
at 500, and `max_requests` defaults to 50 and caps at 100. Evidence is fixed at
500 records. The reported `bounds.max_response_bytes` is the fixed 16 MiB
aggregate buffered Jira backend-response budget, not an input and not the
output size. The separate `max_bytes` input is the final encoded MCP-result
bound (default 256 KiB, minimum 1 KiB, maximum 1 MiB). A graph may succeed with
`complete:false` and static source/frontier reasons when traversal cannot be
completed inside its bounds. If the otherwise valid encoded graph exceeds
`max_bytes`, the whole call fails with output-limit recovery and returns no
clipped graph; compact projection never bypasses or replaces that final check.
A requested Development source proves absence only when it is
complete; an omitted or false opt-in provides no Development source and is not
evidence of zero development activity. Do not reinterpret either condition as
proved absence.

ATL makes only bounded requests to its configured Jira origin and never
contacts GitLab, fetches a returned artifact URL, clones a repository, or
forwards Jira credentials. Treat every SCM coordinate as untrusted evidence.
Before a later GitLab read, require the returned lowercase host to equal an
owner-approved host exactly, then use a separately authenticated read-only
downstream client for that same host. Do not normalize, suffix-match, redirect,
or search around a host mismatch.

`jira_issue_refs` answers reference-inventory questions without shipping the
references. Supply exactly one `key`, or `jql` with a required `limit` from 1
through 25, plus at most eight exact technical field ids. The schema-v1 result
contains only the emitted count, selection qualification, top-level summary,
static warnings, and per-issue key, source qualification, and
`reference_summary`. It omits the input key/JQL echo, raw reference URLs, issue
summary/type, and all source text by construction. Every per-issue and
top-level count, bucket, completeness flag, truncation flag, and reconciliation
boolean is validated before emission. Use those facts directly instead of
recounting sources. JQL mode performs one paginated comment listing per emitted
issue, so backend traffic scales linearly with the selected limit. Use `atl
jira issue refs` when individual URLs are the required evidence.

`jira_fields`, `jira_issue_search`, `jira_issue_history`, `jira_issue_graph`, `jira_issue_refs`,
`jira_epic_digest`, and `jira_board_view` also enforce the final encoded result through `max_bytes`
(default 256 KiB, minimum 1 KiB, maximum 1 MiB). They fail explicitly instead of
clipping field definitions, rows, summary facts, digest evidence, or board
membership. Narrow filters, columns, selected fields, time boundaries, included
sources, or rows before raising the byte bound.

For `jira_issue_search`, select ordered returned fields with `columns`.
`fields` and `projection` are compatibility aliases for the same selector
because agent clients and other Jira tools use those spellings. Supply at most
one non-empty selector; empty arrays are treated as omitted. The returned
IssueList still carries its normalized `projection` metadata independently.
Unknown input names and ambiguous requests fail before backend access.
The IssueList page qualifies Jira search exhaustion. In particular, an empty
page with an advertised remainder returns `complete:false`,
`partial_reason:"pagination_stalled"`, and a null cursor rather than presenting
the query as exhausted. `pagination_unqualified` marks inconsistent paging
coordinates; these reasons are static and contain no backend text.

Use `jira_structure_get` only when compact identity/read-only metadata is
enough. Its `structure_id` accepts either a positive JSON integer or the same
value as a canonical decimal string without a sign, whitespace, or leading
zero. Use `jira_structure_view` for normalized hierarchy evidence with an
explicit ordered `fields` projection. Omit all folder selectors for a bounded
full view, or pass exactly one of `folder_id`, `folder_row`, or `folder_path`
for an exact stored-folder subtree. The tool fails rather than truncating when
the selection exceeds `max_rows` or `max_bytes`. It also rejects forests above
1000 rows before querying folder values, even when the requested subtree would
be smaller; use the CLI for larger forests. Narrow the subtree before raising
an emitted-row or byte bound. `complete:false`, `inaccessible_rows`, and
`warnings` are evidence, not permission to probe raw forest/value endpoints.

Bind a selection to the forest it came from. Whenever an earlier
`jira_structure_view` supplied the `folder_id`, `folder_row`, or `folder_path`,
copy both `forest_version.signature` and `forest_version.version` from that
result into `expected_forest_signature` and `expected_forest_version`. Both
inputs are optional but paired: one without the other, a zero signature, or a
non-positive version is rejected before backend access. A matching result returns
`forest_version_gated:true`; omitting both is an explicitly ungated selection
that is appropriate only for a selector fixed outside any earlier read. The
comparison happens once, against the forest the view is built from, before any
folder-value or Jira issue expansion; there is no second forest request. Every
successful view reports the `forest_version` it was assembled from, which
qualifies the hierarchy and the selection only — Jira issue fields and stored
folder labels are separately timed and are not covered by that version, so do
not report them as one transactional snapshot. A returned pair with either
member zero is non-bindable: omit both expected inputs, keep the selection
explicitly ungated, and report that limitation. `jira_structure_get` metadata
is not version-bound and takes no such input, and no new tool is added.
If an exact selector fails with `view_then_select_subtree`, the Structure
itself was found but the stored-folder selection is stale, ambiguous, or cannot
be validated from the available labels. When the full forest fits the MCP
bounds, retry once without a folder selector, keep `fields` narrow, and set
`max_rows` high enough for the full forest (at most 1000). Choose a folder row
from that snapshot, then request the exact `folder_row` subtree. A forest above
1000 rows or a selector-free view that still exceeds its row/byte bound remains
CLI-only. The error carries only matching/available counts; it never repeats
folder identity or content. A `folder_row` that now identifies a non-folder is
instead a `usage_error`; refresh the bounded view rather than treating the
Structure as missing. Other Structure `not_found` and `check_failed` errors
remain generic.
Raw formulas, arbitrary value matrices, issue pull, file export, and mutations
remain unavailable through MCP.

The offline capability catalog exposes the additive `confluence/comments` task
as four dedicated entries in order: `confluence.comment.list`,
`confluence.comment.thread`, `confluence.comment.preview`, and
`confluence.comment.add`. Only list/thread map to MCP. Preview/add remain
guarded CLI-only commands; neither plugin installation nor a capability mapping
creates a mutation tool.

`confluence_search` requires explicit CQL and returns the same qualified
schema-v1 page as `conf search`: `query`, bounded candidate metadata, `count`,
`complete`, `truncated`, optional `partial_reason`, and `next_cursor`. Search
results omit page bodies. The MCP tool also rejects an encoded result larger
than `max_bytes` (default 128 KiB, minimum 1 KiB, maximum 1 MiB) rather than
clipping titles, excerpts, or pagination evidence. Narrow CQL or lower the row
limit before raising the byte bound. Reuse a returned numeric id directly with
`confluence_page_outline`, `confluence_page_section`, and
`confluence_page_sections`.
Pass `confluence_page_section.heading` as the exact `title` returned by the
outline, without a Markdown `#` prefix; use `occurrence` when that title
repeats.
For several headings on the same page, pass ordered
`confluence_page_sections.selectors` instead. The tool fetches and parses the
page once, preserves repeated selectors, resolves every selector before
returning anything, and emits one entry per selector in request order. Require
matching requested/returned counts, `reconciled:true`, and aggregate
`complete:true` before treating the set as complete evidence.
Each selector is `{heading,occurrence?}`; omission or zero keeps the
unique-heading rule. The 1..32 selector cap controls metadata amplification.
The aggregate Markdown budget is divided among remaining selectors in request
order and unused emitted capacity carries forward. The server additionally
caps the complete encoded result, so bounded Markdown cannot be amplified into
unbounded paths or envelope metadata. Any mismatch in selector order, paths,
counts, completeness, or aggregate/per-section byte accounting rejects the
whole result as `check_failed`.

Use `confluence_page_meta` when the question needs page governance facts but no
page content. It resolves the supplied reference through the same exact
application path as the CLI metadata command, then performs a non-body metadata
read. The schema-v1 result contains only `id`, `title`, `space`, a positive
`version`, optional `updated`, and `restriction_state`.
`restriction_state` is exactly `restricted`, `unrestricted`, or `unknown`;
when the backend omits restriction evidence the result says `unknown` and must
not be treated as unrestricted. The fixed 32 KiB cap rejects the entire result
rather than clipping it; `use_cli_conf_page_meta` is the only wider-surface
remediation. URLs, labels, ancestors, restriction principals, page bodies, and
arbitrary backend expansion fields never reach the MCP result. All
metadata-tool errors, including API and transport failures, use static
content-free messages.

The metadata result describes one separately timed read. Reusing its `version`
as an optional gate for a later section, table, or attachment operation does
not make the reads atomic. Re-read metadata when freshness itself is the
question; do not fetch an outline merely to confirm access state, because an
outline has no restriction field and requires the native page body.

Use `confluence_comment_list` as body-free discovery before expanding comment
content. It accepts one positive canonical decimal `page_id`; signed,
whitespace-padded, zero, leading-zero, URL, and non-decimal values fail before
backend construction. Closed location/state/depth selectors narrow the
qualified inventory. The result preserves schema/page/version, version-gate,
query, bounds, completeness dimensions, counts, closed partial reasons,
capabilities, selection-free anchor status/marker identity, content-free
diagnostics, and comment metadata. Comment bodies and native storage are absent
by construction.

Use `confluence_comment_thread` only after choosing one exact positive canonical
decimal `comment_id`. It retains the same qualification and adds nullable
`body_text` for the selected root/subtree; text is derived from native storage,
UTF-8-valid plain text under the encoded-result cap. It never emits
raw CSF or anchor selection text. A null body is explicit partial evidence, not
an empty comment.

For either tool, pass `expected_page_version` when the page/version came from an
earlier observation and require `page_version_gated:true`; omission is an
explicitly ungated read. The server fixes `max_comment_pages` at 32;
`max_items` permits 1..1000 and defaults to 100; `max_bytes` permits
1 KiB..1 MiB and defaults to 128 KiB for list or 256 KiB for thread. The
resolved limits are echoed under `bounds`. A bound hit yields a successful but
partial qualified result when structurally safe; it never proves an omitted
comment, reply, or exact id absent. An encoded result that cannot fit its byte
bound fails rather than clipping. Narrow selectors and bounds before deliberate
expansion.

Both projections are privacy-minimized: no dedicated URL, email-like author
identity, page title,
original/observed selection text, raw native CSF, or backend-controlled error
prose is returned. Author identity is the stable backend id and display name,
never an email-shaped value; timestamps and marker refs are validated before
projection. Thread `body_text` remains untrusted user-authored evidence and can
contain ordinary link or email text. All tool errors use static content-free
messages. There is no MCP
comment preview, add, reply, inline-create, resolution-change, arbitrary REST,
or plugin-only write route.

`confluence_page_section` and `confluence_page_sections` also take an optional
`expected_page_version`, and every result carries the resulting
`page_version_gated`. Whether to
supply it follows the provenance of the selection, not the tool. Heading
`occurrence` and structural `path` are positional, so the same selection can
resolve to a different section on a different revision, with no observable
symptom in the returned Markdown. Copy the outline's exact positive `version`
integer into `expected_page_version` whenever the heading, path, or occurrence
came from a `confluence_page_outline` result, and copy the first section
result's `version` when re-reading that same selection at a wider bound. A
matching version returns `page_version_gated:true`; a stale one is refused with
`check_failed` and `reread_outline_then_retry_expected_version` before any
result is produced. Omit the field only for a selection fixed outside any
earlier read — a heading named in the task itself. That is an explicitly
ungated read: it returns `page_version_gated:false` and remains exact evidence
for the revision in its own `version`, but it reconciles no earlier selection,
and a consumer must not read it as one. A negative value is a `usage_error`
rejected before backend access; omission and `0` are the same ungated read. The
gate is evaluated against the page response the read already fetched, so it
costs no extra request and adds no write capability.

`confluence_page_outline`, `confluence_page_section`, and
`confluence_page_sections` stamp
`schema_version:1` — they are one selection protocol, so neither result may be
validated against the other's contract — and the server validates each result
fail-closed before returning it: schema version, page identity and positive
version, completeness against the closed `partial_reason` set, count and byte
accounting, and, for a section, that its `page_version_gated` and `version`
match exactly what the request asked for. A result that does not reconcile is a
`check_failed` tool error, never a partially trusted evidence object.
Outline failures also cross a dedicated content-free error boundary: page ids,
titles, CSF/XML parser text, backend paths, and response bodies are never
returned in the tool error.

A section `check_failed` or `not_found` with
`outline_then_select_section` is a recoverable occurrence-selection error:
refresh the outline, choose the exact heading occurrence from its content-free
metadata, and then read that section once. Other section `not_found` failures
remain generic and do not disclose the heading or page reference.

Unlike the fail-closed encoded-result caps on `confluence_search`, the table
tools, and `jira_structure_view`, these two page reads can satisfy the call with
structurally bounded partial output, so both qualify that result explicitly.
`confluence_page_outline` and `confluence_page_section` return `complete`,
optional `truncated`, optional `partial_reason`, `original_bytes`, and
`emitted_bytes`. The plural tool returns aggregate completeness and byte totals
plus the same fields, including optional `partial_reason`, on every section
entry. Per structural result or section entry, `partial_reason` is absent
exactly when `complete:true` and present exactly when `complete:false`.
Its values are a closed static set that never contains a heading, page id,
title, space, URL, body, or caller value:

| tool | `partial_reason` | meaning | recoverable |
|---|---|---|---|
| `confluence_page_outline` | `heading_limit` | the 1000-heading cap stopped emission first | no |
| `confluence_page_outline` | `byte_limit` | the 262144-byte heading cap stopped emission first | no |
| `confluence_page_section` | `max_bytes` | a whole rendered block did not fit the requested bound | yes, once |
| `confluence_page_section` | `invalid_utf8` | the rendering was withheld entirely | no |
| `confluence_page_sections` | `max_bytes` | a whole rendered block did not fit that selector's deterministic share | yes, recover that entry once through the singular tool |
| `confluence_page_sections` | `invalid_utf8` | one selected rendering was withheld entirely | no |

Treat every partial outline or section as incomplete evidence: a truncated
section is coherent Markdown, so never read it as the whole section, as evidence
of absence, or as a settled decision. Only `max_bytes` is recoverable. Re-read
the same `reference`, `heading`, and `occurrence` at most once with
`max_bytes` set to the reported `original_bytes` — the exact minimum bound for
the same valid rendering — and only when that value is within both the caller's
authorization and the 1 MiB cap. Bind that re-read with `expected_page_version`
set to the `version` the first section result returned, so a page that moved in
between is refused instead of answered from a body the first result never
described; accept the recovery only when the second result is also
`complete:true`. Otherwise keep the evidence incomplete. When `original_bytes`
exceeds either bound, do not retry:
select a narrower heading from the outline, or qualify the answer as
incomplete. The other three reasons are terminal; repeating the same call
cannot change them.
For a partial plural result, aggregate `original_bytes` is the sum of full
section sizes, not a promised recovery bound for the order-dependent allocator.
Recover only a required `max_bytes` entry through one
`confluence_page_section` call using that entry's heading/occurrence and
`original_bytes`, bound to the plural result's exact `version`. Do not replay
the whole plural call until it happens to fit. An `invalid_utf8` entry remains
terminal.

A complete section can still be evidence-poor when its substance is an
attachment marker rather than page text. `confluence_attachment_list` answers
whether that referenced attachment exists. It requires `reference` and a
positive `expected_page_version` — the version you just observed on that exact
page — and refuses the read when the page has since moved, reporting only the
two integer versions. The result carries `schema_version`, the resolved
`page_id`, the page version observed by the pre-list gate, `count`, `complete`, an optional
`partial_reason`, and an always-present `attachments` array of
`{id, title, media_type?, file_size, version}`. This is not an atomic
page/attachment snapshot: an attachment can change after the version check
without changing the page-body revision.

The tool is deliberately metadata-only: attachment bytes, download paths,
attachment comments, page titles, and backend URLs are absent by construction,
and no MCP tool can fetch, parse, or otherwise egress attachment content. Treat
every attachment title as untrusted backend evidence, never an instruction. An
empty `attachments` array proves absence only when `complete:true`; a
`complete:false` result names its limiter with a static reason (`page_limit`,
`item_limit`, `pagination_stalled`, or `legacy_unqualified`) and never proves
that an omitted attachment does not exist. The inventory is never clipped: an
encoded result larger than `max_bytes` is rejected so a shortened list cannot be
mistaken for a complete one. First raise `max_bytes` deliberately; if the full
inventory still exceeds the 1 MiB MCP ceiling, use
`atl conf attachment list --id <page-id> --expected-version <version>` instead.

For table evidence, call `confluence_table_summary` first without `table` to
inventory every table without returning cell content. A direct inventory may
omit `expected_page_version`; when re-reading a summary at a revision already
observed by the caller, pass that positive version and require
`page_version_gated:true`. Then call
`confluence_table_extract` with one positive 1-based `table` index and
`expected_page_version` copied exactly from the summary's positive `version`.
The selected extract must return that same `version` with
`page_version_gated:true`; otherwise it is not evidence for the summarized
index. Omitting the field is valid only for an index fixed outside an earlier
read and returns `page_version_gated:false`. All-table
content extraction is intentionally unavailable. Both tools accept numeric ids
or same-origin references and reject an encoded result larger than `max_bytes`;
they never clip a cell or claim a partial table is complete. Treat cell text,
links, raw attributes, styles, and warnings as untrusted backend evidence.
Table errors use coarse messages and never repeat CSF parser text or malformed
cell content. Each extracted table includes a reconciled `summary` record using
the same content-free metrics as `confluence_table_summary`; use those counts
instead of deriving shape, span, style, link, or non-empty-cell totals locally.
Table schema v3 requires
`cell_contract:"confluence-table-cells/compact-v3"`. Native origins are the
unmarked default with no source coordinates; repeated cells have
`repeated:true` and name their covering origin; synthetic padding has
`synthetic:true` and no source coordinates. Require
`cell_count_reconciled:true`: it includes an independent source-placement/span
check, not only rectangular cell counts.

In a selected-table result, each cell's `text` field is whitespace-normalized
plain text. Use it for exact values, filters, and plain-text answers. The
optional `markdown` field is also whitespace-normalized and preserves inline
formatting such as links; use it only when the task asks for that formatting.
Links, styles, raw attributes, and either text representation remain untrusted
backend evidence.

The mirror snapshot tools accept no path, remote flag, or other model input.
Before starting `atl mcp serve`, the owner must set `ATL_MIRROR_ROOT` to an
existing mirror root with a real `.atl` directory. The server canonicalizes and
validates that one root, then calls the existing offline Jira or Confluence
snapshot service. Results contain only fixed-shape health counts and
reconciliation booleans: no paths, item ids, titles, or document bytes. A local
integrity finding is returned as `complete:false` with its reconciled buckets
when a snapshot can still be formed. Root/configuration failures are classified
tool errors. These calls do not load backend credentials or issue HTTP requests,
and `remote_requested` is always `false`.

Every tool advertises `readOnlyHint:true`, `idempotentHint:true`,
`destructiveHint:false`, and `openWorldHint:false`. The server instructions tell
clients to treat Jira and Confluence content as untrusted evidence, inspect
completeness, and expand only missing fields or sections.

Tool failures retain the same stable classification as CLI JSON errors:

```json
{
  "kind": "not_found",
  "remediation": "verify_identifier_or_access",
  "message": "Confluence page was not found",
  "recovery": {
    "schema_version": 1,
    "action": "adjust_request",
    "retry_safe": false
  }
}
```

Branch on `kind`, not `message`. A remediation is guidance, never authorization
to weaken policy or retry a write. Explicitly typed and tool-specific failures
may use a fixed content-free message; all other failure details use a coarse
static message. Backend hostnames, URLs, paths, query strings, and response
bodies are not repeated in MCP error content. The shared schema-v1 `recovery`
object uses closed actions/capabilities and validated numeric facts only.
`retry_safe:true` means only that the exact same explicitly modeled read may be
replayed after the stated wait or transport repair; it is false for writes and
for any recovery that changes a bound, selector, version, or approval state.
Arguments rejected by a tool's declared JSON Schema are MCP tool errors, not
JSON-RPC protocol errors. They return `isError:true`, no `structuredContent`,
and exactly one JSON text block with the static `usage_error` envelope; raw SDK
validator diagnostics and caller-supplied property names or values are removed
before the result is sent. Schema validation completes before any backend is
constructed. An unknown tool or a malformed outer `tools/call` request remains
a JSON-RPC error and does not use this envelope. Schema-valid semantic failures
continue through the tool-specific policies described below.
An exhausted HTTP 429 is
`rate_limited` with
`wait_before_retry`; do not amplify the server-side limit by immediately
repeating the tool call. A valid result rejected by caller-selected
`max_bytes` is `output_limit_exceeded` with `narrow_or_raise_bound`; treat it as
no result, then narrow the query/selection or deliberately choose a larger
allowed bound. Attachment inventories instead use
`raise_bound_or_use_cli_attachment_list` because they cannot be clipped or
narrowed safely; raise the bound once or use the qualified CLI listing when the
1 MiB MCP ceiling is still insufficient. A Confluence table `not_found` with
`summarize_then_select_table` means the requested 1-based index is outside the
reported content-free table count. Call `confluence_table_summary` without a
table selection, choose from that inventory, and then extract once with the
summary's exact version; do not
report the page as missing. Other Confluence table `not_found` failures retain
`verify_identifier_or_access` and do not expose structural counts.
A table `check_failed` with
`reread_table_summary_then_retry_expected_version` means the supplied version
no longer matches the page. Its message contains only expected/current
integers. Re-read the summary, re-select the positional table index, and
extract once with the new version; never retry the old index against a new
revision.
A Confluence section `check_failed` or `not_found` with
`outline_then_select_section` means an omitted occurrence was ambiguous or the
requested positive occurrence exceeded the available count. Its message
contains only requested/available integer counts. Refresh the page outline,
select an occurrence from that inventory, and read the section once; do not
report the page or heading as missing.
A section `check_failed` with `reread_outline_then_retry_expected_version`
means the supplied `expected_page_version` no longer matches the page; its
message contains only the expected and current integers. Recover by re-reading
`confluence_page_outline`, re-selecting the heading occurrence from that fresh
outline, and requesting the section once with the new version — not by
retrying the previous selection against the new revision, which is the drift
rather than the fix. Other section failures use coarse safe
messages and retain their ordinary remediation.
A Jira Structure `not_found` or `check_failed` with
`view_then_select_subtree` means the Structure exists but its stored-folder
selector did not resolve exactly. Its message contains only
matching/available integer counts. Read one selector-free bounded view with a
narrow field projection and `max_rows` sufficient for the full forest, choose
the exact folder `row_id`, and request that `folder_row` subtree once. If the
full forest does not fit the MCP row/byte caps, use the CLI; do not report the
Structure as missing.
A Jira Structure `check_failed` with
`reread_structure_view_then_retry_expected_forest_version` means the supplied
forest-version pair no longer matches the current forest. Its message is static
apart from the expected and current
signature/version integers. Re-read the view, re-select the subtree from that
fresh result, and request it once with the new pair; never retry the old selector
against a new forest version. Other Structure failures use coarse safe messages
and retain their ordinary remediation.

## Install through the agent plugins

The Claude Code and Codex plugin packages include `.mcp.json` and start the
installed `atl` binary as `atl mcp serve`. Install/configure the binary through
the shipped setup skill, ensure `atl` is on `PATH`, then start a new agent
session so the plugin can initialize the server. Existing host-scoped atl
credentials remain in the normal config directory; the plugin does not contain
or copy credentials. The generated definition supplies the per-server modern
protocol marker, but only the user can opt into Codex's global
`mcp_2026_07_28` feature:

```sh
codex features enable mcp_2026_07_28
```

Restart Codex after changing the feature. Without both gates, Codex continues
to use ATL's supported legacy handshake.

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
codex mcp add --env CODEX_MCP_PROTOCOL_VERSION=2026-07-28 atl -- atl mcp serve
codex mcp list
```

That registration supplies the per-server marker. To opt into modern mode,
also run `codex features enable mcp_2026_07_28` and restart Codex. The feature
is under development; marker only or feature only remains legacy, and ATL
continues to support both eras.

For an explicit allowlist and inherited atl environment names, use
`~/.codex/config.toml` (or trusted project `.codex/config.toml`):

```toml
[mcp_servers.atl]
command = "atl"
args = ["mcp", "serve"]
env = { CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }
required = true
enabled_tools = [
  "jira_fields",
  "jira_issue_search",
  "jira_issue_field_get",
  "jira_issue_history",
  "jira_issue_refs",
  "jira_epic_digest",
  "jira_board_view",
  "jira_structure_get",
  "jira_structure_view",
  "jira_mirror_snapshot",
  "confluence_search",
  "confluence_page_resolve",
  "confluence_page_meta",
  "confluence_comment_list",
  "confluence_comment_thread",
  "confluence_page_outline",
  "confluence_page_section",
  "confluence_page_sections",
  "confluence_attachment_list",
  "confluence_table_summary",
  "confluence_table_extract",
  "confluence_mirror_snapshot",
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

A second committed cell starts from an unknown topic and compares the primary
CLI + `search-knowledge` route with typed MCP. The first reviewed MCP baseline
passed the same 18 correctness/safety checks with five typed calls, five GETs,
one expected duplicate page target, zero writes, and a 10,000-bps qualitative
review. It is evidence for the bounded `confluence_search` addition, not a
claim that every search workflow should use MCP.

A bounded Structure route starts with metadata only when identity must be
confirmed, then requests one normalized selection:

```text
jira_structure_get
  -> jira_structure_view (explicit fields, selector-free bounded inventory)
  -> jira_structure_view (one exact folder selector + that inventory's forest-version pair)
```

## Protocol and operations

`atl mcp serve` is a long-running stdio process. Stdout is reserved for MCP
protocol frames. It skips self-update at startup so no unrelated update request
can alter initialization or corrupt protocol output. Authentication/config is
loaded lazily per tool call, allowing the configured Jira or Confluence sibling
to work when the other service is absent.

Raw stdio compatibility covers stateless `server/discover` followed by
`tools/list`, `resources/list`, and `resources/read`; the complete legacy
initialize/initialized sequence with the same three calls; structured
future-version rejection; one response per request; clean stderr; and closed
tool/resource inventories in both eras.

Cancellation propagates from the MCP client into the application request. HTTP
auth scoping, redirect/downgrade checks, retry policy, pagination completeness,
and stable error classes are shared with CLI reads.

Tool output schemas retain their inferred contracts while spelling an
unrestricted property as the object schema `{}` instead of boolean `true`.
The forms are JSON-Schema-equivalent, but the object form keeps the complete
tool catalog usable in clients that reject boolean property schemas.

The surface intentionally excludes write tools, raw REST, arbitrary files,
full-page bodies by default, raw changelog rows, pull, identity-bearing mirror
status/diff, raw Structure forest/values, and Structure pull/export. Those
remain CLI workflows.
