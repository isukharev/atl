# Jira graph and references

Bounded typed graph traversal, reference extraction, and hierarchy projection.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [`atl jira issue graph`](#atl-jira-issue-graph)
- [`atl jira issue reference search`](#atl-jira-issue-reference-search)
- [`atl jira issue refs`](#atl-jira-issue-refs)
- [`atl jira issue tree`](#atl-jira-issue-tree)
<!-- reference-navigation:end -->

## `atl jira issue graph`

Build one deterministic bounded work-artifact graph from an exact Jira issue:

```bash
export ATL_READ_ONLY=1
atl jira issue graph PROJ-1
atl jira issue graph PROJ-1 --depth 2 --strict
atl jira issue graph PROJ-1 --resolve confluence
atl jira issue graph PROJ-1 --include-development
atl jira issue graph PROJ-1 --projection compact
atl jira issue graph PROJ-1 --include-development --projection compact --select urls,scm
atl jira issue graph PROJ-1 -o text
```

For a typed transient read, `jira_issue_graph` returns the same full or compact
projection without a shell command; full schema v2 remains its default. MCP v1
is deliberately Jira-only: it has no
Confluence resolution input, always leaves discovered page identities as
qualified stubs, and has no `strict` option. Supply `key`, optional `depth` from
0 through 2, and optional `max_nodes`, `max_edges`, `max_requests`,
`include_development`, `projection`, `select`, and `max_bytes`. Nodes default to
50 and cap at 100;
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

`--projection` accepts `full` or `compact`. Omitting it or explicitly selecting
`full` emits the existing schema-v2 bytes and makes the existing request
sequence. `--select` is a repeatable, comma-separated closed selector with
`urls`, `scm`, and qualification-only `none`; it is valid only with compact
JSON. With no selector, compact selects `urls` and also selects `scm` only when
`--include-development` is present. Explicit `scm` therefore requires
`--include-development`; `none` cannot be combined with a fact selector.
Repeated values are normalized and deduplicated into fixed `urls`, then `scm`
order.
Incompatible projection, selector, and output-format combinations fail before
configuration, credentials, or network access.

Compact schema v1 is derived only after the bounded full schema-v2 graph has
been collected and validated. It preserves root identity, top-level
completeness and truncation, every collection and transport bound, incomplete
source qualification, the bounded frontier, warnings,
`projection.selected`/`projection.omitted`, and reconciled counts. Selecting
fewer facts or `none` changes no collector, request, traversal, or bound; lower
graph bounds are not an output
reduction mechanism. URL facts are copied only from canonical `url` nodes,
including opaque nodes whose safe `url` is omitted, and are never rebuilt from
evidence or labels. SCM facts contain only validated coordinates and
never synthesize GitLab web URLs. A requested Development source retains its
status and count even when it is complete-empty or incomplete and no SCM fact
is emitted. `summary.collected` preserves the full graph counts;
`summary.projected` and its reconciliation booleans qualify only the compact
`facts` and retained `sources` inventories.

The full projection emits schema v2; compact emits schema v1 and is JSON-only.
With the default `--depth 0` and `--resolve none`, collection expands only the
seed. Jira issues, Confluence page
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
copies a source snippet. Full text output preserves per-node source qualification,
transport accounting, and any bounded frontier, then renders escaped node/edge
tables. For URL nodes, its node table includes the already-normalized public
`url` value; non-URL, opaque, or sensitive identities keep that cell blank,
and the renderer never reconstructs a URL from evidence or source content.
JSON remains the canonical contract; compact has no text or id rendering.

This command is additive. `jira issue refs` retains its existing URL-focused
schema, flags, output bytes, and JQL behavior.

## `atl jira issue reference search`

Start from one exact GitLab project or Confluence page and search a
caller-qualified Jira scope for issues that refer to it. This is a read-only,
CLI-only capability: there is no typed MCP counterpart.

```bash
export ATL_READ_ONLY=1
atl jira issue reference search \
  --target 'https://gitlab.example.test/platform/widget' \
  --target-kind gitlab-project \
  --scope-jql 'project = DEMO' \
  --mode exhaustive \
  --sources description,comments,remote-links,development \
  --max-issues 100 \
  --max-requests 1000 \
  --max-response-bytes 16777216 \
  --strict

atl jira issue reference search \
  --target 12345678 \
  --target-kind confluence-page \
  --scope-jql 'project = DEMO' \
  --mode exhaustive \
  --sources description,fields,comments,remote-links \
  --fields customfield_10001 \
  --max-issues 100 \
  --max-requests 500 \
  --max-response-bytes 16777216
```

The command has no defaults that can turn a missing policy decision into a
broad scan. It accepts no positional arguments and requires all of these
inputs:

| Flag | Contract |
|---|---|
| `--target` | exact target identity; at most 2048 bytes |
| `--target-kind` | `gitlab-project` or `confluence-page` |
| `--scope-jql` | caller-qualified predicate without `ORDER BY`; at most 16384 bytes |
| `--mode` | `exhaustive` or `fast` |
| `--sources` | repeatable/comma-separated `description`, `fields`, `comments`, `remote-links`, `worklogs`, `development`, or `properties` |
| `--fields` | repeatable/comma-separated exact technical Jira field ids; required exactly when `fields` is selected, maximum 128 |
| `--max-issues` | positive selected-issue bound, maximum 5000 |
| `--max-requests` | positive shared physical-request bound, maximum 25000 |
| `--max-response-bytes` | positive aggregate buffered-response bound, maximum 268435456 |
| `--strict` | emit the qualified result, then exit 8 when it is incomplete |

The scope is permission-relative and must contain the project, time, or other
boundary appropriate to the caller. `atl` appends its own `ORDER BY key ASC`;
supplying an ordering is rejected before configuration or network access.
Sources and fields are normalized into deterministic order, and duplicate or
unknown selectors fail at the same pre-configuration boundary.

A GitLab target must be an exact HTTPS project URL, not a commit, branch, or
merge-request URL. Host case, the default HTTPS port, and a trailing `.git` are
canonicalized; the project path remains case-sensitive. A Confluence target
may be an opaque content id or one of the same supported same-origin references
as `conf page resolve`. A configured secure Confluence origin is required even
for an id because it qualifies later URL comparisons. Id-bearing URLs and ids
resolve offline. A caller-supplied display or short URL may use the configured
Confluence identity resolver under the same single-attempt request/byte budget.

`exhaustive` selects the full caller scope twice with the same ascending key
order. Both passes must reach terminal pagination and return the same issue
identity set before selection is complete. This detects candidate-set drift;
it is not atomic snapshot isolation. `atl` then verifies every requested source
for every selected issue. `fast` instead makes one target-derived narrowed Jira
selection and is always returned as `selection.complete:false` with
`reason:"mode_fast"`, even when every selected issue verifies successfully.
It is useful for qualified discovery but can never prove absence.

Each selected source is explicit and locally matched:

| Source | Bounded evidence read |
|---|---|
| `description` | the exact Jira `description` field |
| `fields` | only the exact technical ids supplied with `--fields` |
| `comments` | the complete paginated comment-body inventory |
| `remote-links` | Jira's supported remote-link inventory and coherent structured Confluence metadata |
| `worklogs` | the complete worklog-comment inventory |
| `development` | fail-closed Jira Development project, commit, branch, and merge-request coordinates; meaningful only for GitLab targets |
| `properties` | the opt-in returned issue-property values; property keys never enter output |

Description, selected fields, and properties inspect every bounded JSON string
leaf. This differs deliberately from the broad graph walk: its privacy-excluded
key list does not hide a value the caller selected exactly. Values remain
local and are never copied into the result. Individual field/property values
are capped at 65536 bytes, field/property walkers at 1048576 bytes, properties
at 128 entries, and comment/worklog/remote-link inventories at 10000 entries.
A malformed or clipped inventory remains visibly incomplete.

Literal matching accepts only validated absolute links. GitLab project and
modern `/-/` artifact URLs map to the canonical project. Confluence literals
must be same-origin, direct id-bearing URLs for the resolved page. `atl` never
resolves a display or short URL discovered in Jira content; select structured
remote links where available or use a direct page-id URL. Duplicate
observations collapse by issue, source, relation, and technical field id.

The default schema-v1 JSON is content-free. It exposes a one-way opaque target
id, normalized source/field selectors, independently qualified target,
selection, and verification phases, candidate/scan/verification/match counts,
per-source status and static reason counts, matches, a bounded frontier,
reconciliation, physical request/byte usage, top-level `complete`, and
`absence_proven`. A match contains only the Jira key, relation, fixed
`issue_to_target` direction, source, optional technical field id, stability,
confidence, and its source-derived completeness. It never contains the target
coordinate, scope JQL, Jira numeric id, URL, title, source text, property key,
application/user identity, or backend error.

Source outcomes are `complete`, `empty`, `partial`, `forbidden`, `unsupported`,
or `skipped`; only the first two are decisive. Source reason counts use the
closed `request_failed`, `request_limit`, `byte_limit`, `malformed_response`,
`field_missing`, `not_permitted`, `not_supported`, and `mode_fast` vocabulary.
Phase reasons are the closed `mode_fast`, `request_limit`, `byte_limit`,
`issue_limit`, `request_failed`, `malformed_response`, `selection_drift`, and
`source_incomplete` vocabulary.
Top-level `complete:true` requires exhaustive mode, complete selection and
verification, and every count/source/match/usage reconciliation. Only
`complete:true` with zero matches sets `absence_proven:true`.

Without `--strict`, a usable incomplete result exits zero for inspection. With
`--strict`, atl emits the same JSON or text and then exits **8**
(`ErrCheckFailed`); retain that stdout and do not retry it as a missing result.
`-o text` is an escaped match-only table with `KEY`, `RELATION`, `SOURCE`,
`CONFIDENCE`, and `COMPLETE`; it omits the qualification needed for absence
claims, so agents should use JSON. `-o id` is unsupported.

`atl` never contacts GitLab, clones a repository, follows an artifact URL,
forwards Jira credentials, or dereferences any URL found in Jira. Optional
Confluence traffic is limited to resolving the caller-supplied display/short
target identity; the command does not read page bodies or Confluence backlinks.
Every Jira and optional Confluence read shares the emitted single-attempt
request and buffered-response budget.

## `atl jira issue refs`

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

## `atl jira issue tree`

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
