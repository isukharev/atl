# Broker semantic contract

This document defines ATL's Broker semantic contracts and authenticated HTTP
v1 and the bounded project-page execution-v2 family. Explicit Broker client
mode routes supported Jira and Confluence reads through the local host; direct
mode remains the default. Execution-scoped discovery and qualified corpus handoff use separate
authenticated routes. Guarded writes, durable outcomes, Broker capture/refresh,
and broader cache reuse remain unavailable until their owning slices compose.

The machine-readable shapes are the semantic
[`schemas/broker-v1.schema.json`](schemas/broker-v1.schema.json) and HTTP
[`schemas/broker-http-v1.schema.json`](schemas/broker-http-v1.schema.json).
Execution-scoped discovery has a separate
[`schemas/broker-discovery-v2.schema.json`](schemas/broker-discovery-v2.schema.json)
and [HTTP v2 schema](schemas/broker-discovery-http-v2.schema.json), so its
evolution does not redefine either v1 digest. Cache resolution similarly uses
the separate [semantic v2](schemas/broker-cache-v2.schema.json) and
[HTTP v2](schemas/broker-cache-http-v2.schema.json) schemas while embedding the
unchanged v1 qualification request/decision. The normative strict codecs and canonical vectors live in
`internal/brokercontract` and `internal/brokertransport`. JSON Schema alone
does not prove duplicate-key rejection, temporal ordering, authority provenance
or canonical bytes.

A bounded-page family has its own semantic
[`schemas/broker-execution-v2.schema.json`](schemas/broker-execution-v2.schema.json).
It defines one structured Jira project-page operation without changing the v1
schema, registry or digest namespace. Its fixed `/v2/execute` route, CLI/MCP
consumers and discovery-v3 sibling are described below; broader search,
streaming and write families are not inferred from its availability.

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

The server core has exact typed routes: `POST /v1/execute` for the two available
read operations, `POST /v2/cache/qualify` for the separate cache family, and
`POST /v2/execute` for one project page, alongside
authenticated `GET /v1/protocol` for schema, registry,
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
forwarded headers. Admission, qualification and final authorization use three
fixed authority routes and the existing exact decision codecs. Cache source
resolution uses the separate `POST /v2/authorize/cache` route with a 64 KiB
request and response cap. Every authority call is single-attempt,
redirect-free, bounded to five seconds and never positively cached; the other
four retain their 128 KiB caps.
Cache qualification establishes one five-second connection/context deadline
before request-body receipt and shares it across workload authentication,
authority resolution, failures, and response publication; it does not receive
a new lease per phase or proceed when connection deadlines are unavailable.

The authority adapter also implements `POST /v1/authorize/proposal` with the
existing strict v1 request and clearance codecs: one attempt, at most 2 MiB of
encoded request, at most 128 KiB for success or failure, and a caller-clipped
five-second deadline. The app binds the decoded clearance to the exact operation
decision, proposal, native candidate, version evidence, context and current lease
on receipt and again before dispatch. Qualification or operation permission is
not proposal clearance. This adapter capability does not enable a guarded
server, client or registry operation.

The handler caps parsed request headers at 16 KiB, the workload bearer at
8 KiB, and each execute or cache-qualification body at 64 KiB before strict
decode. A fixed global
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

Exact and project-page reads additionally anchor every decision's
`expires_at_ms - issued_at_ms` duration to a local monotonic observation before
the authorization call. The earlier local lease or signed expiry bounds the
next phase and final release; allowed authority clock skew does not extend it.
Signed decision bytes and their digest bindings are unchanged.

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

The internal operation-observation service implements the authorization and
metadata boundary without registering a runtime route. It strictly validates
one current `broker.operation.outcome` invocation, admits it, authorizes its
zero-request `operation_id` qualification, then authorizes the exact
`observe/operation` resource before its sole journal lookup. The operation
resource is derived from the input ticket and proves only that exact selector;
it does not assert that a record exists or reveal its owner or phase. Unknown,
foreign, malformed journal and internally issued-but-unbound identifiers all
produce the same content-free denial. The service has a lookup-only journal
port, so it cannot read a recovery artifact, enumerate operations, transition a
phase or contact Jira.

After final authorization, the sole lookup receives a child context capped by
the remaining operation/context deadline and final decision lease. The service
cancels that child immediately after lookup and repeats cancellation, current
context, decision and release checks before returning metadata.

Stable journal ownership hashes Broker id, complete backend binding, principal,
workload and audience as separately domain-separated canonical values. Original
execution id/epoch and authority revision have separate writer hashes and are
not compared with a later observer. A later execution can inspect an old ticket
only with its own current credential, execution expectations, qualification and
final observe authorization and only while the record's fixed observation
deadline remains open. That read does not renew the old writer or any deadline.
The service recomputes the journal's exact immutable intent binding and rejects
a well-shaped but changed binding or intent. This is consistency checking, not
independent authenticity proof: the trusted lookup adapter still owns durable
history, phase and dispatch evidence. Journal sequence remains an opaque
nonzero compare token and is never interpreted as a lifecycle ordinal.
Returned `complete` and `reconciled` describe stored evidence: only durable
`applied` with its qualified result digest is reconciled; observation performs
no fresh backend reconciliation. Response release is capped by the current
decision/operation deadlines and the original observation deadline.

The current storage-backed observation subset is `admitted`, `dispatching`,
`applied`, `not_applied` and `outcome_unknown`. Although the general v1 codec
reserves `retired_non_replayable`, this slice has no retirement transition and
rejects that phase from an injected lookup port rather than advertising it.

`broker.operation.outcome` remains unavailable in the registry. Server, client,
CLI and MCP wiring wait for a supported guarded-comment ticket producer and an
authenticated synthetic integration proving the complete no-enumeration and
zero-backend-request boundary.

The storage-only foundation in `internal/adapter/brokerjournal` implements the
domain journal port without enabling comment, outcome, server, client or CLI
routes. Creation requires an absent root under an existing current-owner `0700`
parent; reopening requires the existing root, exact deployment identity and
unchanged limits. A caller must never fall back from reopening missing or
invalid state to creating fresh state. One held advisory writer lock lasts
until close. Parent/root identity, current ownership, exact private modes,
single-link regular files and no-symlink paths are checked through held Unix
directory descriptors. File sync and directory sync use those same handles.
Filesystem errors have closed diagnostics without paths or underlying text.

The adapter qualifies Linux ext-family, XFS, Btrfs and OverlayFS filesystems
with working allocation and sync primitives; OverlayFS backing storage must
also be local. On Darwin it qualifies only local, writable APFS mounts that
enforce ownership. Darwin allocation requests all-or-none persistent
`F_PREALLOCATE` but does not trust that advisory flag alone: it proves the
returned allocation, establishes the logical file size, then verifies the
allocated block count. Later opens revalidate physical allocation. Regular-file
commitments use `F_FULLFSYNC`; directory commitments retain descriptor
`fsync`. It does not support shared/network coordination, other Darwin
filesystems or volatile temporary filesystems. Allocation and sync cannot
guarantee against every storage, copy-on-write or hardware failure; any
ambiguous mutation poisons the open instance and returns no new dispatch right
or terminal success.

Issuance reserves a random 256-bit ID and durable storage before returning it
to the trusted application. A separate bind step fixes the existing v1 ticket
after the application can hash prospective apply arguments containing that
ID. The internal issued reservation has no public phase and grants no write
authority. Unknown IDs are rejected. Stable ownership contains Broker, backend,
principal, workload and audience hashes; original execution and authority
revision hashes are separate immutable fields. An independently authorized new
observation execution can therefore inspect its stable owner's expired writer
ticket without reviving the original write authority. Exact semantic arguments,
proposal, native candidate, target/effect and version evidence are immutable;
fresh transport correlation IDs are excluded. The v1 ticket codec and digest
remain unchanged.

Each reservation preallocates eight 8 KiB metadata slots, eight independent
256-byte state-commitment slots and its declared recovery-artifact capacity,
between 1 byte and 16 MiB. The whole journal defaults
to at most 1,024 IDs and 256 MiB of reserved file bytes, including identity,
reservation index, state commitments, metadata and artifact storage; configured
limits can only reduce those ceilings. Creation preallocates the mandatory reservation index
with one checksummed 256-byte slot per configured ID slot. Each ordered entry
binds the issued ID and artifact capacity. Issuance syncs all three owned files,
then its index entry, then the initial reservation record and its independent
state commitment before returning an ID. A crash between these steps leaves
storage unavailable, never a fresh ID.
These byte ceilings exclude filesystem bookkeeping. Issuance refuses capacity
exhaustion while retaining all existing slots for closeout. No automatic
deletion, compaction, retirement or GC is implemented. The longest legal chain
occupies six slots: issued reservation, binding, admitted, dispatching,
outcome unknown, then qualified applied/not-applied. Other paths are shorter;
repeat lookup/binding/completion does not append or renew a deadline. The two
remaining slots provide fixed margin and authorize no additional transition.

Every phase transition syncs its record slot and then its independent state
commitment before returning. Each commitment binds the exact record-slot digest,
sequence and consumed-dispatch bit. A surviving record slot without its matching
commitment, or a commitment without its record slot, makes storage unavailable.
The issuance index has one immutable slot per ID; the separate fixed `.state`
file keeps phase evidence append-only and reserves terminal capacity without
rewriting the global reservation inventory for each transition.
This includes zeroed consumed slots after an otherwise valid admitted prefix;
such loss cannot be interpreted as proof that dispatch never occurred.

Metadata is strictly versioned canonical JSON inside length/checksum-delimited,
zero-padded slots. It contains hashes, fixed deadlines and durable state only,
never credentials, backend content, response prose or approval material. Native
recovery bytes live in a separate private, bounded artifact whose exact byte
digest and length are fixed at admission. The future operation-specific app
owner must restrict those bytes to the native candidate and required qualified
baseline; an opaque storage artifact is not permission to retain credentials or
unrelated content. Artifact durability precedes admission; the durable admitted
record establishes a target fence before the sole durable dispatch claim.
Current capability, proposal clearance and last-hop authorization still need
independent checks after slow durable I/O and immediately before the single
operation-specific send.

Reopening validates the complete bounded index, its exact owned-file inventory,
both matching slot chains and every transition before recovering any record.
Removing an operation's record, artifact and state-commitment files while
leaving its committed index entry therefore refuses
readiness instead of losing an unresolved target fence. Unindexed pairs and
missing, torn or inconsistent index entries also fail closed. A partial
publication, torn slot, missing or altered artifact, future format or
inconsistent binding makes storage unavailable; it is not silently repaired or
called a recovered outcome.
Surviving admitted records become never-dispatched `not_applied`; dispatching
records become `outcome_unknown`. Fences for admitted, dispatching and unknown
operations are restored before the adapter returns. The comment-dependent
target hash must bind the backend and immutable issue identity consistently
across principals. Unknown fences persist indefinitely; unrelated targets can
proceed under independent authority. Only qualified positive evidence may
resolve an unknown result; absence of readback is insufficient.

Acceptance is at most 60 seconds and bounded by the fixed execution, grant,
credential and operation deadlines. Observation has its own fixed deadline.
Neither lookup nor retry extends either lifetime. Every sampled time advances
an in-memory high-water mark, even on a refused expiry check. Writer and
observation deadlines compare against that observed maximum: backward movement
cannot revive a deadline already observed as expired in the open instance,
including movement within the existing one-second clock allowance. Larger
backward movement refuses new issuance/admission/dispatch until trustworthy time
is restored. Successful durable transitions retain the high-water timestamp;
closeout can still preserve evidence without dispatching. Observations that
never reached a durable transition are not recoverable after process loss, so a
cold restart still requires a trustworthy runtime clock. Local sync also cannot
detect coordinated historical rollback of records and their matching independent
commitments: trusted runtime execution/epoch invalidation is required before
resuming writes after that storage rollback. This foundation provides no authority
service, backend reconciliation or universal exactly-once delivery guarantee.

### Injectable guarded Jira comment core

The internal application core implements `jira.comment.preview` and
`jira.comment.apply` version 1 without registering either operation. Its sole
canonical `exact_jira_comment_v1` qualification profile is the
`identity_snapshot_v1` consistency described above: the exact operation and
version bind that profile through every authorization request. A synthetic or
future production authority must opt into those weaker immutable-resource
semantics. Credential possession, a successful late metadata read or an allowed
decision for another profile is not consent. An authority whose maximum requires
atomic current-project membership returns `unsupported_consistency` before
protected dispatch.

Preview uses one authorized issue-identity request and reuses that evidence for
the actor and complete comment-inventory proposal, so qualification plus
business work retains the 102-request total. It durably reserves the random
operation ID before constructing the prospective apply arguments that contain
it, then binds the apply ticket, proposal, operation-specific native digest,
complete backend, immutable issue target, effects and version evidence before
releasing `proposed`. Preview and apply native-body digests remain distinct.

Apply reauthorizes the exact issue/effects and proposal around the ordinary
prewrite snapshot. It durably publishes a private immutable recovery artifact
and admitted target fence, repeats operation/proposal authorization and a
deny-only full-identity local preflight, then claims dispatch durably. Only a
successful new claim reaches the operation-specific guarded write port. That
port remains responsible for authoritative last-hop local clearance immediately
before its single POST. The apply path retains 306 total upstream requests and
one shared 16 MiB response cap, including its one qualification request.

The recovery artifact is strict canonical versioned JSON capped and reserved at
4 MiB. It contains the exact native Jira-wiki candidate, immutable issue
identity/revision, actor digest and sorted baseline comment-ID/record-digest
pairs, bound to the operation ticket and journal intent. It contains no
credential, actor value, approval prose, raw response or unrelated comment
body, and is never rewritten. The maximum accepted 1 MiB candidate plus 10,000
longest accepted numeric IDs is exercised as a real encoded fixture below both
the application cap and the journal's 16 MiB artifact ceiling.

No-attempt proof may terminalize a consumed claim as `not_applied`; possible
send, unproved readback or failed terminal persistence remains non-replayable
and fenced. Applied or recovered success is released only after the existing
qualified readback and durable result completion. The separately authorized
metadata observer reports stored state only; this core adds no backend
reconciliation, replay, retry, policy store or generic dispatch hook.

The current Jira adapter's last-hop comment clearance does not yet include the
immutable numeric issue ID, and a project metadata check cannot make a later
numeric-ID POST atomically project-scoped. Adapter/runtime composition,
authenticated strong-scope evidence and client/server/CLI availability remain
required separate work. Registry availability therefore remains false.

Cache qualification binds issuer/backend, source principal and read-scope
digests, target execution, authority revision, operation/selector/projection,
evidence schema, immutable generation and exact content digest. Equal account
or backend identity is insufficient. Missing, legacy, altered, expired or
future qualification is unqualified. This disables automatic cross-context
reuse; it does not claim to delete already downloaded user data.

The implemented positive cache slice is only a clean, fully verified,
Confluence-only sealed generation with manifest/receipt/capture/cache-binding
v1, indexer projection v2, native and metadata complete, and comments and
attachments not requested. The client supplies selector, projection,
evidence-schema, generation, and native snapshot digests derived from that
generation. It cannot supply source principal or read scope. The configured
external authority resolves those two values by the exact trusted
backend/generation/content tuple, constructs the unchanged semantic-v1 request,
and returns that request with its decision. The Broker pins the configured
issuer and verifies every returned request field, digest and hard expiry before
releasing a response that omits the source bindings.
The client-visible target-execution digest covers only Broker id, audience,
execution id and epoch. It is not complete authenticated identity: the
server-validated request digest binds the full principal, workload, backend
binding and execution/grant/credential lifetimes without disclosing them.

This is a real external verifier dependency, not an ATL hash-to-grant rule.
Unknown tuples deny or remain unavailable, and merely configuring an authority
does not advertise cache availability through discovery v2. ATL stores no
capture registry or policy. Qualification makes zero Jira/Confluence requests;
`confluence.page.read` identifies the native content family but cannot reuse a
single-page execution grant for the aggregate. The authority-owned source scope
must cover every member and excluded dimension, while the distinct cache digest
domains bind the aggregate selector, native snapshot, derived projection and
whole immutable generation.

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

`atl broker discover --service jira|confluence` and the selected read-only MCP
resources `atl://broker/discovery/jira` and
`atl://broker/discovery/confluence` obtain a fresh projection on every read.
They require explicit Broker mode, use only the selected workload session and
never resolve a direct backend PAT or fall back to a direct adapter.

The existing v1 workload session intentionally contains only its credential,
execution and authority equality guards. It cannot supply an authenticated
context digest or lifetime ceiling. Discovery therefore uses two stateless,
single-attempt POSTs, sharing one five-second deadline and two-response budget:

1. `/v2/discovery/negotiate` authenticates a bounded request containing the
   service, Broker/audience, request id, session equality guards and client
   deadline. It returns only a strict v2 discovery request, now bound to the
   authenticated context digest and the earliest execution/grant/credential
   expiry, together with the discovery HTTP schema digest.
2. `/v2/discovery` authenticates again and requires that exact context and
   session binding. The server invokes the fixed authority `/v2/discovery`
   endpoint with the context-bound semantic authorization request. Only a
   complete, canonical, current projection for the configured service is
   published; no qualification or business read occurs.

The negotiation request and response are capped at 16 KiB; authority and final
projection responses are capped at 256 KiB. Failures use the independent v2
closed failure envelope, capped at 4 KiB. Redirects, duplicate/unknown members,
unsupported schema identities, context changes, oversized and late responses
fail closed without retry. The client reloads the session before accepting the
final projection and rejects replacements during the call. HTTP responses are
`no-store`; MCP discovery reads are private with a zero TTL. Resource listing
returns static descriptors only, and tool registration is unchanged.

Closed recovery distinguishes access requests (`denied`, `revoked`,
`grant_expired`), refreshed credentials/execution (`credential_expired`,
`stale_execution`, `stale_authority`), rereading discovery (`decision_expired`),
unsupported request adjustment, and authority outages requiring inspection.
Proposal clearance retains human-approval precedence, and `outcome_unknown`
requires outcome reconciliation. Every Broker recovery has `retry_safe:false`;
an enclosing runtime must obtain the required fresh state or access and issue
a separate explicit operation. HTTP status and backend prose never select a
recovery action. Existing v1 transport failure mappings remain unchanged.

## Gated Jira project-page execution v2 foundation

The separate execution-v2 semantic family defines the available
`jira.project.issue_page.read` operation version 1. Its formerly gated
foundation now has fixed authority/server/client composition and explicit
`jira issue project-page` / `jira_project_issue_page` consumers. Availability
means compiled contract support, not permission or a configured Jira backend;
an uncomposed service returns authenticated `unsupported`. Discovery v2 remains
bound to the unchanged v1 registry and schema.

The fixed-family discovery-v3 negotiation and projection routes are
`/v3/discovery/negotiate` and `/v3/discovery`. They accept only Jira and
`contract_family:"atl.broker.execution.v2"`, bind the current workload session,
exact registry/schema/operation/effect/limit facts and a five-second lease,
and never grant execution permission. Unknown families do not fall back to v2
or v1. CLI `broker discover --family atl.broker.execution.v2` and the matching
private zero-TTL MCP resource expose this advisory projection. Existing v1
and discovery-v2 wire bytes and digest namespaces remain unchanged.

Early overload or drain failures keep the request route's failure-envelope
version. They can omit correlation before authentication has assigned one;
successful execution and discovery replies always require valid correlation.

The authority adapter posts exact strict v2 requests to
`/v2/authorize/project-page/admission`, `/v2/authorize/project-page/qualification`
and `/v2/authorize/project-page/operation`: each is single-attempt, redirect-free,
bounded to five seconds, at most 2 MiB of request and 128 KiB of response/error
bytes. The app validates each decision against its exact phase and current lease.
Missing final authorization can follow two authorized metadata reads, but never
permits the business read.

The execution route installs one monotonic, parent-clipped 60-second deadline
before body receipt or authentication. Discovery routes use a five-second
ceiling. Unfinished bodies retain a connection read bound, and the clipped
write deadline remains through explicit response flush and handler return.
After body EOF, Go may start its next-request background read with a cleared
read deadline; that is not an unbounded unfinished body. Completed requests
retain normal keepalive reuse.

The workload client starts its overall 60-second bound before loading the
selected session. Fresh v3 discovery and execution share a three-request
response budget of 16 KiB + 256 KiB + 129 MiB; failures are individually capped
at 4 KiB. It retains the same credential and execution guards, reloads before
execution and after buffering, and rejects even a same-guard credential
replacement. Discovery checks both wall expiry and elapsed lease length.
Broker clients never consult proxy settings, including with system TLS trust;
ordinary Jira/Confluence adapters keep their existing transport defaults.

The request is semantic rather than JQL. It contains one already canonical
uppercase project key, a sorted unique subset of `summary` and `description`,
a `start_at` offset from 0 through 1,000,000, and `max_results` from 1 through
15. There is no URL, JQL, provider, credential, principal, role, policy,
default-all field, expansion or automatic continuation. The Jira adapter alone
constructs `project = <qualified numeric project id> ORDER BY id ASC` and fixed
field/paging parameters. Jira documents `id` as an issue-key alias; ATL does
not reinterpret that ordering as numeric immutable-id order or as a backend
snapshot guarantee.

One invocation has an absolute 60-second ceiling and exactly three
single-attempt, redirect-free Jira reads under one parent budget:

1. an exact project endpoint response capped at 256 KiB, retaining only the
   numeric id and canonical key;
2. an identity-only search page capped at 1 MiB, retaining the paging
   coordinates and at most 15 ordered issue id/key/project/update identities;
3. after final authorization, one business search capped at 64 MiB, returning
   only the selected fields plus mandatory identity evidence.

The exact project endpoint has no field selector and may return documented
supporting members in addition to id/key. The adapter therefore admits only a
closed top-level vocabulary, recursively bounds those supporting JSON values,
discards them, and never follows returned URLs. The qualification plan names
the retained id/key projection while honestly accounting for the whole
256-KiB physical response. It never calls the global project inventory.

Qualification authorization binds two ordered steps: project identity at one
request/256 KiB and page identity at one request/1 MiB. Their aggregate is two
requests/1.25 MiB. The complete Jira contour is three requests/65.25 MiB
(68,419,584 bytes). The result permits at most 64 MiB of selected decoded
values and 129 MiB of JSON wire bytes. Streams remain disabled.

Final authorization contains the qualified project plus every issue and one
effect for each resource. The project effect covers `id`, `key` and pagination;
each issue effect covers mandatory `id`, `key`, `project`, and `updated` plus
exactly the requested optional fields. Fifteen issues plus the project fit the
fixed 16-resource/16-effect ceiling. Resources and effects use an internal
numeric-id sort only to produce one canonical authorization set. Separately
bound page evidence preserves Jira's exact returned row order. A denial of any
resource denies the whole page before the business read; rows are never
filtered because that would falsify offsets, totals and absence claims.

The qualification decision is checked before and after both metadata reads.
The final decision is checked before the business read and again after the
fully buffered result. These checks implement the existing bounded positive
lease model, with a maximum five-second lease; they do not claim to observe an
external revocation committed after the last authority call without another
mechanism. Project identity, paging coordinates, reported total, exact ordered
issue identities, project membership facts and update markers must match
between qualification and business responses or the whole buffer is discarded.

The result keeps three truths distinct:

- top-level `complete:true` means the one authorized envelope is complete;
- `coordinate_exhausted:true` means only that `start_at + count` equaled the
  matching responses' reported total;
- `selection_complete` is always false because separate offset pages cannot
  prove a stable project set or stable global absence.

A positive row count below the reported total yields only the next decimal
offset while that offset is at most 1,000,000. A larger next offset is
`offset_limit` with no cursor; coordinate exhaustion still wins when the last
row reaches the reported total. An empty page with an advertised remainder is
`pagination_stalled` and has no cursor. Every later page is a fresh operation
with fresh authentication and all authorization phases; a cursor is not a
grant. `identity_snapshot_v1` does not prove atomic current membership, exclude
an unobserved move or ABA change, stabilize totals/order across calls, or turn
broad Jira credentials into project-scoped native credentials. An authority
that requires atomic membership or a stable complete selection must return
`unsupported_consistency`.

## Dependent implementation slices

Contracts are prerequisites rather than evidence that a runtime capability has
shipped. Each dependent slice must still provide authentication provenance,
current authorization, backend-supported consistency, durable ticket
verification, revocation evidence and runtime isolation before advertising its
operations.
