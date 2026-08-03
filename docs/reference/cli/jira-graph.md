# Jira graph and references

Bounded typed graph traversal, reference extraction, and hierarchy projection.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl jira issue graph`

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
