# Broker semantic contract

This document defines ATL's Broker semantic contracts and authenticated HTTP
v1. Explicit Broker client mode routes the supported exact Jira issue and
Confluence page reads through the local host; direct mode remains the default.
Guarded writes, durable outcomes, cache reuse and discovery v2 runtime routes
remain unavailable until their owning slices are composed.

The machine-readable shapes are the semantic
[`schemas/broker-v1.schema.json`](schemas/broker-v1.schema.json) and HTTP
[`schemas/broker-http-v1.schema.json`](schemas/broker-http-v1.schema.json).
Execution-scoped discovery has a separate, currently uncomposed
[`schemas/broker-discovery-v2.schema.json`](schemas/broker-discovery-v2.schema.json)
so its evolution does not redefine either v1 digest. The normative strict codecs and canonical vectors live in
`internal/brokercontract` and `internal/brokertransport`. JSON Schema alone
does not prove duplicate-key rejection, temporal ordering, authority provenance
or canonical bytes.

## Trust boundary

The client and every value in a `request` envelope are untrusted. A request can
state expected execution identity and authority revision, but those fields are
equality guards only. It cannot send a principal, role, grant, backend URL,
authorization decision, lease or proposal clearance.

`BrokerVerifiedContext` enters through the trusted authentication adapter.
It binds an opaque principal and workload, execution id and epoch, audience,
Broker id, authority revision, hard lifetimes and a content-minimized backend
identity. A structurally valid value does not prove authenticity. No codec in
this change turns a request into a verified context.

Existing contracts retain their roles:

- the capability/effect catalog is informational discovery metadata;
- local content policy remains defense in depth;
- `WritePreflightAuthorizer` remains deny-only;
- `WriteAuthorizer` remains the mutating adapter's last-hop clearance.

A Broker authorization never replaces the final adapter clearance.

## Authenticated HTTP server core

The server core has two exact routes: `POST /v1/execute` for the two available
read operations and authenticated `GET /v1/protocol` for schema, registry,
profile metadata, and the configured Broker id and workload audience. A client
checks that identity against its fixed configuration before execution. The
response is compatibility metadata, not
execution-scoped discovery or a grant. Paths, methods, queries, content type,
content encoding, authorization header cardinality and body shape are closed.
There is no raw URL, method, header, JQL, CQL or MCP forwarding surface.

Opaque workload credentials are introspected on every request through one
configured HTTPS authority. Authentication binds a fresh 192-bit server nonce,
a domain-separated credential digest, the configured issuer, audience and
Broker id, the complete verified context and a response lease of at most five
seconds. The credential cannot select the authority URL or provide identity in
forwarded headers. Admission, qualification and final authorization then use
three fixed authority routes and the existing exact decision codecs. All four
authority calls are single-attempt, redirect-free, independently capped at
128 KiB, bounded to five seconds and never positively cached. Proposal
authorization remains unsupported.

The handler caps parsed request headers at 16 KiB, the workload bearer at
8 KiB and the execute body at 64 KiB before strict decode. A fixed global
concurrency semaphore refuses overload before authentication. Successful
payloads retain the existing Jira or Confluence result bytes without another
wrapper. Failures use the closed HTTP schema, for example:

```json named-broker-http-failure-v1
{"schema_version":1,"status":"rejected","reason":"denied","recovery":"request_access","retry_safe":false,"complete":true}
```

The app result carries the final decision's monotonic release deadline. The
handler encodes and checks the complete response, refuses a canceled request,
and applies the earlier of the request deadline and final release deadline as
the response write deadline. It rechecks cancellation and expiry before
headers. A write that fails after headers is incomplete and never receives a
second JSON body. Before any
content response it also checks decoded selected fields, native storage and
encoded bytes for the workload and configured credentials known to the server.
A match refuses the whole response; native Jira-wiki or CSF bytes are never
redacted or rewritten. This bounded guard does not claim to detect unknown
secrets or arbitrary encodings.

The explicit `atl broker serve --config ...` host composes this server without
consulting ordinary ATL client configuration or credentials. It reads one
bounded owner-private file and same-directory credential/TLS references before
opening two numeric loopback TLS listeners. Data and admin use distinct
audiences; authenticated health/readiness return only local lifecycle state and
make no backend probe. The fixed TLS 1.3/HTTP 1.1 host bounds connections,
admission rate, reads, request lifetime and shutdown, cancels admitted work on
SIGINT/SIGTERM, and emits only closed content-minimized audit events to stderr.
One five-second shutdown deadline covers server drain, complete hosted-handler
lifetime and audit closure. A handler that misses it causes a closed failure;
its credential state is retained until the handler exits or process teardown.
Ambient proxies, public binds, inline secrets, system trust fallback, hot
reload, HTTP/2, upgrades and response streams are refused. See the
[CLI contract](reference/cli/agent-interfaces.md#atl-broker-serve) and
[credential-free deployment example](broker-local-deployment.md).

Remote client ports and ordinary CLI/MCP Broker composition remain disabled
until #1487. Discovery, comments, journals, streams and caches remain separate
later increments.

## Versioned operation registry

Every request carries `schema_version:1`, an operation id and an independent
`operation_version:1`. Unknown versions, operations and fields fail closed.
The registry binds resolvable argument/result schema references, schema digest,
backend service, required features, qualification and execution profiles,
exact effects and fixed request, response, resource, field, native-body,
stream, operation-deadline and decision-lease limits.

| Operation | Contract availability | Selector and effect |
|---|---|---|
| `jira.issue.read` | available in `exact_reads_v1` | one canonical issue key; a unique explicit subset of `description`, `summary`, `updated` |
| `confluence.page.read` | available in `exact_reads_v1` | one canonical numeric page id; exactly `metadata` or native `storage` |
| `jira.comment.preview` | gated | one issue and exact native Jira-wiki body; qualification reads only |
| `jira.comment.apply` | gated | same body plus reviewed proposal hash and operation ticket; one comment effect |
| `broker.operation.outcome` | gated | one operation ticket; observation only, never list/search |

The last three definitions let independent later implementations build against
stable fixtures. Discovery must report them as `unsupported` or `unavailable`
until their semantic enforcement and durable journal dependencies land.
Requests must carry the registry's exact sorted feature set. The two initial
reads use an explicit empty set; unknown, missing or partial feature sets fail
closed instead of selecting a compatibility fallback.

Streaming is disabled in this profile: chunk count and chunk size are both
zero. Raw HTTP, URLs, JQL, CQL, wildcard/default-all fields, arbitrary maps,
search, attachments, nested expansion and shell execution are absent rather
than forwarded through a generic escape hatch.

## Request example

The request contains no authority-bearing identity:

```json named-broker-request-v1
{"schema_version":1,"operation":"jira.issue.read","operation_version":1,"request_id":"request-1","features":[],"expect":{"execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"},"arguments":{"issue_key":"EXAMPLE-1","fields":["description","summary"]}}
```

Field selection is a semantic set. The codec rejects duplicates and emits it in
sorted order before computing the arguments digest. Native comment bodies are
base64 on the wire and hashed from their exact decoded bytes; changing one
newline changes the digest.

## Exact read results

Both available read results repeat the server-recomputed `arguments_sha256` so
an independent client can bind a result to the semantic selector it sent. A
Jira result returns the immutable numeric issue id, canonical key/project,
update evidence, and only the explicitly projected fields. Its field set is
sorted, unique, presence-explicit and complete; a null value is distinct from
an omitted field. A Confluence result returns the numeric page id, exact space,
version, title and update evidence. The `storage` projection carries exact
UTF-8 CSF bytes as canonical base64, while `metadata` proves storage absence.

Each read permits at most 64 MiB of selected native value bytes. The closed JSON
result envelope has a separate 129 MiB transport cap to cover canonical base64
or worst-case accepted JSON escaping plus bounded metadata. Results reject
unknown members, incomplete projections, mismatched key/project identity,
ambiguous numeric ids, malformed base64 and invalid native UTF-8 without
echoing rejected content.

## Exact read enforcement

The transport-neutral `BrokerReadService` implements the first service-owned
semantic path for `jira.issue.read` and `confluence.page.read`. Each injected
reader is paired with a server-owned workload-backend id and reports a digest
derived from its immutable configured adapter base. Before backend I/O the
service compares both values with the authenticated backend binding, validates
the closed request, version and features, then obtains preliminary admission
and explicit authorization for one metadata qualification read. A qualification
denial therefore performs zero backend requests. Final denial performs exactly
the one authorized metadata request and no business read.

After qualification, the service binds the immutable resource, identity and
version evidence, requested projection and effect to a final authorization
request. The business GET uses the qualified numeric id, not the original
selector. Jira requests only project/update evidence and the selected subset of
`summary`, `description` and `updated`. Confluence requests only
space/version/ancestors plus `body.storage` for the storage projection. It does
not expand labels, restrictions, links, attachments, rendered views or nested
resources.

Evidence digests use the contract's domain-separated canonical JSON. Jira
version evidence covers `id` and `updated`; its identity projection also covers
`key` and `project`. Confluence version evidence covers `id`, `version` and
`updated`; its identity projection also covers `type`, `status`, `space`, the
ordered ancestor ids and explicit ancestor presence. These fields are compared
again against the business response before content can be returned.

Qualification and business reads use single-attempt contexts and child budgets
under one parent: one 64 KiB qualification response, one 64 MiB business
response, two physical requests and 64 MiB plus 64 KiB total. Each phase
context expires with the decision that authorized its request, so a queued GET
cannot be dispatched after that lease. The service buffers the complete result
and checks identity, scope, version, projection, request binding and the current
final decision again before returning it to a future transport. Expiry or
observed drift discards the result without publishing resource content.

Version 1 offers `identity_snapshot_v1` consistency. Project, space, ancestors
and revision are qualified snapshot facts for one immutable resource. Matching
two reads does not prove atomic current membership or exclude an unobserved ABA
move. An authority that requires a stronger current-project or current-space
guarantee must deny this operation with `unsupported_consistency`; the service
does not silently downgrade that requirement. No broad upstream credential is
recast as a membership-scoped credential.

The gated Jira comment result schema is also closed. Preview returns
`proposed`, an operation ticket and proposal/evidence digests with
`write_attempted:false`. Apply returns only `applied`, `recovered`,
`not_applied` or `outcome_unknown`. Applied and recovered results
require a complete reconciled readback. An unknown result can record whether a
complete but conflicting readback was observed; it remains incomplete and
never licenses a retry. These shapes are compatibility contracts for the later
journal slice, not an executable Broker route in this change.

## Authorization phases

The injected domain port has four independent methods: preliminary admission,
qualification authorization, final operation authorization and exact proposal
authorization. A success from an earlier method is never a success from a later
method. The only admitted order is:

1. strict bounded decode, supported operation/version/feature and verified
   context checks, with zero upstream I/O;
2. preliminary admission over the recomputed arguments digest, also with zero
   upstream I/O;
3. authorization of one bounded metadata qualification plan;
4. service-owned qualification in the later canonical-scope slice;
5. final authorization over qualified resources and effect-to-resource pairs;
6. independent proposal clearance for a guarded apply;
7. the business operation;
8. bounded outcome observation when the operation requires it.

The pure phase validator rejects a business transition that skips final
authorization or, for an apply, proposal clearance. The contract performs no
business I/O itself.

Every phase decision contains the digest of its exact authorization request and
verified context. Qualification decisions also bind the admission request,
admission decision and metadata plan; operation decisions bind the
qualification decision, operation, arguments, resources and effects; proposal
clearance binds the full proposal request, operation decision, native candidate
and version evidence. The validation API requires the expected request and
current context, so a decision copied across a principal, execution, backend,
selector, resource, effect or proposal does not validate.

Qualification authorization names exact metadata fields and limits. It is not
a broad permission to inspect the backend. Final resources distinguish missing
hierarchy evidence from a proved empty ancestor list. Jira issue identity is
immutable numeric id plus exact key/project. Confluence page identity is
numeric id plus space and explicit ancestor presence. Each semantic effect is
bound to its resource; there is no verbs-by-target Cartesian product that could
silently broaden a compound operation.

Registry limits separate the client request and result envelopes from total
upstream budgets and from qualification/business phase ceilings. Each initial
read permits one bounded qualification request and one bounded business read,
with an explicit two-request total and aggregate response-byte cap. A phase
ceiling never increases the total operation budget.

The guarded comment profile keeps qualification to one identity/metadata
request. Its business phase owns the authorized identity/update, actor and
comment-inventory reads, the single mutation for apply, and exact readback.
Preview reserves 101 business requests after qualification; apply reserves
305. Both remain inside the existing 102/306 total request and 16 MiB aggregate
response ceilings.

## Time, freshness and revocation

Execution expiry, grant expiry, credential expiry, authorization lease,
decision freshness and the whole operation deadline are separate values. The
v1 baseline has:

- no positive decision cache between operations;
- authorization before each new operation and protected step;
- a maximum positive decision lease of 5 seconds;
- a 1-second comparison allowance that never extends a hard expiry;
- a maximum operation duration of 60 seconds.

A decision is unusable at or after the earliest operation, execution, grant,
credential or decision expiry. Late responses and wall-clock rollback cannot
extend it. Credential refresh does not change execution epoch, authority
revision, grant expiry or operation deadline.

A later authorizer adapter may advertise this profile only when it can refuse
new operations or protected steps no later than five seconds after revocation
is committed by its authoritative source. If the backend or authorizer cannot
provide the required current-scope consistency, the result is
`unsupported_consistency`. Snapshot qualification is not atomic membership
enforcement and must not be described as such.

## Proposal clearance

Capability permission and approval of exact content are independent. Proposal
authorization binds the current operation decision, qualified resources and
effects, proposal schema/version/hash, exact native candidate digest and
version-evidence digest. A preview receipt or client-supplied expected hash is
not a grant. A changed proposal needs a new clearance.

The gated Jira comment contract keeps native Jira-wiki bytes,
`append_always`, the existing guarded proposal and the current limits: 1 MiB
body, 102 preview requests, 306 apply requests, 16 MiB aggregate response and a
60-second deadline. It does not create a universal callback or retry engine.

## Discovery, outcomes and cache qualification

Discovery is an execution-scoped, expiring projection of the static registry.
Its availability vocabulary is `available`, `unsupported`, `unavailable`.
Facts are not grants; every invocation still runs current authorization.
Discovery cannot enumerate policy rules, roles, sibling resources, backend URLs
or other principals.

Operation tickets bind principal, execution, audience, backend, operation,
arguments, optional proposal and an acceptance window. Exact duplicate
id/scope/digest is a lookup of existing state. Reusing an id with changed scope
or digest is `operation_id_conflict`. The durable phases reserved for the
journal slice are:

`admitted`, `dispatching`, `applied`, `not_applied`, `outcome_unknown`, and
`retired_non_replayable`.

After uncertain dispatch, missing evidence never makes the id fresh. Outcome
lookup needs independent current observation authority, cannot enumerate
another principal's operations and may remain unknown. Retained tombstones must
cover the full acceptance window plus clock allowance; unresolved writes stay
fenced or are explicitly retired as non-replayable.

Cache qualification binds issuer/backend, source principal and read-scope
digests, target execution, authority revision, operation/selector/projection,
evidence schema, immutable generation and exact content digest. Equal account
or backend identity is insufficient. Missing, legacy, altered, expired or
future qualification is unqualified. This disables automatic cross-context
reuse; it does not claim to delete already downloaded user data.

## Strict codec and digests

Every closed envelope is bounded before decode, limited to 64 nesting levels,
valid UTF-8, exactly one JSON value, known members and exact JSON types.
Duplicate keys, trailing values, unpaired surrogates, numeric/string coercion,
unknown fields and inconsistent status/completeness fail with a content-free
error. Opaque ids are 1–128 printable ASCII bytes and are never trimmed or
normalized. Digests are exactly 64 lowercase hexadecimal characters.

Canonical JSON sorts object keys, sorts declared semantic sets, preserves
ordered arrays, distinguishes absent/null, uses standard JSON string escaping
and accepts bounded base-10 integers without fractions, exponents, plus signs
or negative zero. It does not claim RFC 8785 conformance. Each digest is:

```text named-broker-digest-v1
SHA256("atl.broker.v1/<kind>\x00" || canonical_json_bytes)
```

Arguments, verified context, registry, each authorization request and decision,
qualified resources, effects, proposal clearance, operation ticket and cache
qualification use distinct `<kind>` domains. The
existing guarded application proposal hash remains unchanged and is bound as
an input rather than recalculated.

## Closed failures and compatibility

Broker reasons map to existing CLI/MCP error and recovery classes. No new exit
code or generic diagnostic kind is introduced.

| Reason | Existing identity and recovery |
|---|---|
| malformed, unsupported version/operation/feature | usage; adjust request |
| denied, revoked, grant expired | forbidden; request access |
| credential expired | authentication; reauthenticate |
| stale execution/authority, expired decision, authorization unavailable | terminal check; inspect failure |
| proposal clearance required | terminal check; request human approval |
| operation id conflict | terminal check; no replay |
| outcome unknown | terminal ambiguous check; reconcile write outcome |

Errors never include the rejected value, policy rule, backend/PDP text, URL,
path, credential or native body. A future server may attach only the closed
reason and content-minimized correlation facts allowed by the caller's
audience.

## Execution-scoped discovery v2

Discovery v2 is a separate strict semantic contract. Its client request carries
one service plus Broker id, audience, authenticated-context digest, a current
session lifetime ceiling, and execution id, epoch and authority-revision
equality guards. An authority request binds those bytes to a complete
authenticated context and a canonical request digest. Neither request can
select a resource, role, policy, backend destination or grant.

The response contains exactly the Broker's available operation definitions for
the authenticated backend service. Structural support is explicit and separate
from the current advisory access state: `allowed`,
`access_request_required`, or `unavailable`. Only the access-request state may
carry a printable opaque correlation reference, capped at 64 bytes. Effects,
features and limits must exactly match the canonical registry; no project,
space, issue, page, principal, policy rule or possible-role inventory exists in
the shape.

Request id and digest, authenticated-context digest, execution id and epoch,
audience, Broker id, authority revision, service, registry and both schema
digests are mandatory bindings. The lease is at most five seconds and cannot
outlive execution, grant or credential expiry. Client validation rejects a
late response and any mismatch with its current session. Server validation
also binds the response to the authenticated context. A valid discovery result
is advisory only and never enters admission or authorizes a later invocation.
The request digest uses its own versioned domain:

```text named-broker-discovery-v2-request-digest
SHA256("atl.broker.discovery.v2/request\x00" || canonical_request_json)
```

This increment publishes the pure v2 codec and schema only. It intentionally
does not add an HTTP route, CLI command, MCP resource, cache, polling loop or
authority call; those require the subsequent runtime composition review.

## Dependent implementation slices

Contracts are prerequisites rather than evidence that a runtime capability has
shipped. Each dependent slice must still provide authentication provenance,
current authorization, backend-supported consistency, durable ticket
verification, revocation evidence and runtime isolation before advertising its
operations.
