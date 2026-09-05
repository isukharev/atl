# Confluence output contracts

Qualified Confluence reads, mirrors, comments, tables, page operations, and guarded mutations.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [Qualified Confluence search page](#qualified-confluence-search-page)
- [Qualified Confluence space tree](#qualified-confluence-space-tree)
- [Bounded Confluence attachment discovery](#bounded-confluence-attachment-discovery)
- [Advisory Cloud-compatibility validation](#advisory-cloud-compatibility-validation)
- [Confluence mirrors and page operations](#confluence-mirrors-and-page-operations)
- [Mirror status, diff, reconciliation, and plans](#mirror-status-diff-reconciliation-and-plans)
- [Confluence pull and comments](#confluence-pull-and-comments)
- [Confluence tables](#confluence-tables)
- [Page and section reads](#page-and-section-reads)
<!-- reference-navigation:end -->

## Qualified Confluence search page

`atl conf search` returns
`{schema_version:1,query,results,count,complete,truncated,partial_reason?,next_cursor}`.
`complete:true` requires a qualified terminal backend page: no continuation
cursor and no pagination anomaly. A page exactly at the requested limit needs
a supported exact total to prove exhaustion when no continuation is present;
missing or contradictory terminal evidence remains `complete:false` with a
static reason. Legacy/unqualified stores also remain `complete:false`, even
with an empty cursor. `-o text` carries the same signal above a Markdown
candidate table; `-o id` remains page ids only. Agents must continue a cursor,
narrow or investigate an unresumable partial page, or disclose partial search
before making an absence claim.

## Qualified Confluence space tree

`atl conf space tree` emits
`{schema_version:1,space,depth,count,complete,truncated?,partial_reason?,consistency,bounds,pages}`.
`pages` is always an array. `bounds` records both the selected item, scanned-row,
physical-request, aggregate response-byte, and deadline ceilings and the
observed `scanned_items`, `requests_used`, and `response_bytes_used`.

`truncated:true` remains the compatibility alias for `complete:false` and is
omitted on complete results. `complete:true` requires terminal offset-pagination evidence before any bound
or pagination anomaly intervenes. Every page must contain non-null
`results`, `start`, `limit`, `size`, total, and links fields with consistent
coordinates; the total must remain stable and the terminal remainder must be
exact. Equality with an item or scanned-row cap does not make an otherwise
terminal page partial. It does not claim a snapshot:
`consistency` is always `live_unproven`. A false value carries exactly one
static `partial_reason`: `item_limit`, `scan_limit`, `request_limit`,
`response_byte_limit`, `deadline`, `pagination_stalled`,
`pagination_unqualified`, or `legacy_unqualified`. Physical attempts and
buffered response bytes are charged below the application loop through the
command-scoped read budget. A partial page prefix never proves absence.

## Bounded Confluence attachment discovery

`atl conf attachment search` emits
`{schema_version:1,qualification,complete,reason?,consistency,scope_sha256,
start_offset,next_cursor?,count,total_size?,bounds,attachments}`. The
`attachments` member is always an array and each row is the metadata-only shape
`{id,title,type,version,container_id,container_type,container_version,space,
media_type,file_size}`. No URL, download path, comment, body, or binary data is
projected. Attachment and container ids use the bounded opaque
`[A-Za-z0-9_-]{1,256}` read grammar.

`qualification` is `complete`, `partial`, or `failed`.
`complete:true` requires strict stable-total terminal search coordinates but
also requires a present non-null `total_size` equal to the exact terminal end.
It still reports `consistency:"live_unproven"`: Confluence supplies an offset,
not a snapshot token. Partial results use a closed reason (`item_limit`,
`request_limit`, `response_byte_limit`, `deadline`, `pagination_stalled`, or
`pagination_unqualified`) and a query/backend/space-bound opaque next cursor.
That cursor is a checked live offset, not stable snapshot identity. Failed
results use `read_failed` or `validation_failed`, require `count:0` and an empty
`attachments` array, omit both `total_size` and `next_cursor`, and are emitted
before the CLI returns its mapped non-zero error. The MCP projection
uses this same validated DTO and marks such a structured result as a tool error;
it never turns a backend failure into success.

Every invocation requires explicit `max_items`, `max_requests`,
`max_response_bytes`, and deadline bounds. `bounds` reports those selections
and physical `requests_used`/buffered `response_bytes_used`. Request admission
is enforced below orchestration, generic retries and redirects are disabled,
and server-provided continuation/attachment URLs are never fetched. The wire
shape is qualified against the official Server/Data Center
[REST specification](https://developer.atlassian.com/server/confluence/10.2.14.swagger.v3.json);
`-o id` contains only attachment ids.

## Advisory Cloud-compatibility validation

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

## Confluence mirrors and page operations

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

`atl conf attachment get --id <PAGE-ID> --name <FILENAME> [--version N]
[--max-bytes N]`
emits the deliberately non-exact schema-v1 download acknowledgement after its
atomic contained write:

```json
{
  "schema_version": 1,
  "page_id": "12345",
  "name": "diagram.png",
  "output_name": "diagram.png",
  "requested_attachment_version": 2,
  "observed_attachment_id": "67890",
  "observed_attachment_version": 2,
  "observed_file_size": 1048576,
  "max_bytes": 67108864,
  "selector": "page_filename_attachment_version",
  "attachment_id_bound": false,
  "identity_revalidated": true,
  "page_version_gated": false,
  "path": "assets/diagram.png"
}
```

`name` preserves the exact caller selector; `output_name` is the safe contained
basename written beneath `--into`. A positive request uses
`page_filename_attachment_version`. For requested version `0`, the selector is
`page_filename_latest`, but metadata revalidation must observe one unambiguous
current positive version and the binary GET uses that positive value; requested
and observed versions remain separate fields.

`--max-bytes` defaults to 67108864 (64 MiB) and accepts `1..1073741824`
(1 GiB). The exact `--name` must be nonblank valid UTF-8 of at most 255 bytes;
`--id` accepts a bounded opaque `[A-Za-z0-9_-]{1,256}` page id, an absolute
HTTP(S) URL, or a root-relative path. All three inputs are validated before
config, backend, and path access.
`observed_file_size` is the required non-negative `fileSize` for the selected
attachment version, with historical metadata taking precedence, and
`max_bytes` echoes the selected ceiling. A reported size above that ceiling
performs no binary request and does not create the destination directory.

The metadata/reference phase is bounded at five physical attempts, 2 MiB of
aggregate response bytes, and 15 seconds. Normal reference resolution may use
bounded read retry and safe same-origin redirects; only the immediate
revalidator calls are single-attempt. Its context is canceled before the
binary phase. An absent/duplicate exact filename, incomplete inventory,
mismatched historical version, or exhausted bound performs no binary request
and no output write.

Binary transfer starts from the original caller context and has a separate
five-physical-attempt budget. It disables generic replay retry while permitting
only finite same-origin, scheme-safe redirects, and it is outside the metadata
15-second/2-MiB budget. Exactly `observed_file_size` bytes must be read, with an
additional one-byte overrun probe; short, long, canceled, or close-failed
transfers preserve any existing destination and leave no temporary file.
`identity_revalidated:true` claims only the immediately checked selector tuple
`resolved page id + exact caller filename + positive attachment version`.
The binary route itself remains page+filename+version, not attachment-id bound,
and no page version gate, transaction, or snapshot exists; therefore
`attachment_id_bound:false` and `page_version_gated:false`. Text output remains
the written path.

`atl conf attachment delete --page-id <PAGE-ID> --id <ATTACHMENT-ID>` emits the
guarded schema-v1 proposal:

```json
{
  "schema_version": 1,
  "page_id": "12345",
  "attachment_id": "67890",
  "mode": "dry-run",
  "status": "would_apply",
  "operation": "delete",
  "observed_state": "present",
  "current_page_version": 7,
  "expected_page_version": 7,
  "page_body_sha256": "<sha256>",
  "page_body_bytes": 128,
  "page_title_sha256": "<sha256>",
  "page_hierarchy_sha256": "<sha256>",
  "attachment_title_sha256": "<sha256>",
  "media_type_sha256": "<sha256>",
  "attachment_file_size": 4096,
  "attachment_version": 2,
  "inventory_count": 3,
  "inventory_sha256": "<sha256>",
  "expected_final_count": 2,
  "expected_final_sha256": "<sha256>",
  "backend_sha256": "<sha256>",
  "proposal_hash": "<sha256>",
  "write_attempted": false,
  "complete": true,
  "warning": "..."
}
```

Apply adds `final_page_version`, `final_count`, and `final_sha256` when exact
readback is available. `status` is `would_apply`, `blocked`, `not_applied`,
`applied`, `recovered`, or `outcome_unknown`; `write_attempted` records whether
the single DELETE began and `reconciled:true` means two independently complete
final inventories agreed. Success/recovery requires unchanged exact page
evidence and that reconciled inventory to equal the entire reviewed inventory
minus the selected attachment. Absence alone is insufficient when the page or
a sibling changed. Attachment comments participate only through the aggregate
inventory/proposal hashes and are never emitted by this result.

Confluence pull/render/apply/push and mirror-local `conf edit` acquire one persistent mirror-internal
advisory lock for their complete mutation/preview critical section. Contention
is exit `8` before page/state writes. The file persists so every process locks
the same inode; process exit releases ownership. Read-only status is lock-free.
Jira retains its own workflow lock, while both services additionally merge
sidecar patches under the shared `.atl/state.lock`; cross-service state
contention is retried for a brief fixed window, then fails closed and cannot
lose unrelated entries.

## Mirror status, diff, reconciliation, and plans

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
checked/unavailable and checked results into in-sync/drifted. One eligible page
uses its exact metadata endpoint. Larger selections use qualified batches of at
most 100 page ids and 16 KiB of escaped selector input, with one transport
attempt per batch and generic replay-safe retries disabled. A batch is credited
only after exact id/version reconciliation and terminal pagination; any typed,
partial, omitted, duplicate, unexpected, or malformed evidence makes the whole
batch unavailable without per-page fallback. Redirect responses are not
followed. Without `--remote`, all pages remain not attempted.

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

Page reads reject duplicate JSON members, lossy Unicode, and case aliases of
known content members before projecting evidence. Unknown additional members
remain forward-compatible; contradictory evidence cannot confirm an update.

Post-push refresh also requires the exact confirmed page ID, version, and
native body. Any mismatch, including a newer concurrent version, preserves
local candidate/base/state bytes and sets `warning` while keeping the
confirmed `pushed:true` result. An invalid update acknowledgement never emits
`pushed:true` with `new_version:0`: one bounded exact readback may prove success;
otherwise the command exits `8`, preserves local files, and reports
`reconcile_write_outcome` with `retry_safe:false` in structured recovery.

Missing local page targets for Confluence render/apply/push use
`ErrNotFound`/exit `4`; syntactically invalid target types continue to use
`ErrUsage`/exit `2`. Transport failures expose a fixed coarse category
(`dns`, `tls`, `timeout`, `connection-refused`, `connection-lost`,
`unreachable`, `canceled`, or `network`) alongside a query-redacted URL. The
raw cause remains non-unwrappable and no category includes cause text.

## Confluence pull and comments

`atl conf pull` returns a `PullResult` whose `pages[]` entries are `PulledPage`
objects. Each carries `id`, `title`, `path`, `version`, `assets`, and — only when
`--comments` was passed — a `comments` count (omitted otherwise, so the shape is
unchanged without the flag; an explicit `"comments": 0` means the fetch ran and
found none, distinguishable from "not fetched"). A complete pull with requested
`--attachments` likewise adds an `attachments` count: on a partial inventory it
is only the retained observed prefix, not a total. The top-level `includes`
array is always present in stable `assets`, `comments` order; requested
attachments append a third row:

```json
{
  "root": "mirror",
  "pages": [
    { "id": "100", "title": "Alpha", "path": "DOCS/alpha/alpha.csf", "version": 3, "assets": 0, "comments": 2 }
  ],
  "includes": [
    {"dimension":"assets","requested":false,"qualification":"not_requested"},
    {"dimension":"comments","requested":true,"qualification":"qualified","complete":true}
  ]
}
```

`qualification` is from the closed set `not_requested`, `deferred`,
`qualified`, `partial`, or `failed`. `complete` is omitted until actual work
proves coverage or proves it incomplete. Preview leaves requested
assets/comments/attachments `deferred`, omits `complete`, sets
`reason:preview_deferred`, and makes no comment-list, asset-download,
attachment-inventory, or attachment-body GET.
Actual pulls aggregate each requested dimension across all selected pages:
`qualified,complete:true` proves coverage; `partial,complete:false` uses
`resolution_incomplete`, `inventory_incomplete`, or `not_attempted`; and
`failed,complete:false` uses `read_failed` or `staging_failed`. No backend text
or request estimate enters `reason`. When a failed include aborts the pull, the
qualified result is emitted before the original mapped non-zero error. Text
output carries the same fields on stable `include:` lines. A clean actual result
continues to omit `local_safety`; this array does not manufacture a safety
refusal.

`attachments` exists only for an explicitly bounded complete-pull attachment
policy. Its inventory is recorded in the versioned `<slug>.attachments.json`
sidecar and binds the parent id/version plus native and metadata hashes. With
`--attachment-bodies`, only exact allowlisted media types can be streamed into
the contained, hash-bound `<slug>.attachments/` tree; result JSON exposes neither filenames
nor body paths. Inventory/body incompleteness is `partial,complete:false`; body
selection or budget incompleteness uses the attachment-only
`reason:"body_incomplete"`. The default complete-pull policy stops before that
page's durable acceptance for incomplete comment/thread or attachment evidence.
The CLI rejects inventory caps above 100 list pages or 10,000 records per page,
and body caps above 64 MiB per body or 64 MiB in aggregate; the aggregate cap
must be at least the per-body cap. Each atomic page publication captures at
most 512 eligible bodies. Before any body GET, atl reserves the exact core
page, staged-asset, Jira-macro, and relocation tombstone bytes; the preflighted attachment-sidecar
upper bound; and ownership-proven retirement entries in that page's
transaction. Strict mode refuses an over-count or over-byte page before any
body GET; partial mode records the canonical retained prefix and
`count_limit` or `aggregate_limit` exclusions. Before each body GET, the filename/version selector
is revalidated against the exact inventory ID; an ambiguous or changed selector
stops the default strict page and becomes qualified partial failure only with
`--allow-partial-artifacts`, never a body claimed for another ID.
`--allow-partial-artifacts` is the only opt-in that retains such sidecars and
allows the page snapshot to complete; it never upgrades the include or sidecar
to `complete:true`. The pre-existing anchor-only comment qualification remains a
partial detail rather than an implicit completeness claim.

For attachment bodies, the private accepted-prefix aggregate is restored before
the remaining pages are read. Thus `max_total_attachment_bytes` is a cap for one
complete clone, including a resumed prefix, rather than a fresh allowance on
every invocation.

On a requested attachment recapture, replacement sidecar/body publication also
retires only ownership-proven prior body files that it no longer retains. A
complete pull without `--attachments` retires an ownership-proven prior capture
when the replacement page would invalidate it. A non-complete refresh,
including one that relocates the page, instead fails before any write and
directs the caller to `--complete`; it never leaves a new page identity beside
stale capture evidence.

Evidence becomes `qualified` or `partial` only after the page and every staged
artifact for that dimension are durably published. A comment-sidecar, asset,
attachment-sidecar/body, page, or shared batch publication failure demotes the dimensions staged for the
affected page to `failed/staging_failed` before the non-zero result is emitted.
Complete-pull progress and journals persist a versioned content-free aggregate
for the durable prefix. Attachment-free current state keeps the existing
progress/journal encoding; attachment evidence uses its next version. Current
state restores it across resume; legacy state with an accepted prefix but no
include evidence is explicitly
`partial/not_attempted`, never fabricated as complete.

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

Incremental and complete pulls, plus ordinary CQL/space pulls with an effective
`--page-prefetch 2..8` or positive `--requests-per-second`, carry the exact
command-scoped scheduling policy (incremental/complete defaults shown):

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
pacing, not zero requests. Ordinary pulls with effective defaults
`page_prefetch=1` and `requests_per_second=0` retain the unscheduled transport
and omit this object, including when those defaults were supplied explicitly.

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
  "includes": [
    {"dimension":"assets","requested":false,"qualification":"not_requested"},
    {"dimension":"comments","requested":false,"qualification":"not_requested"}
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
schema/service hashes and canonical ids; a small progress file stores the
matching hashes and `next_index`; a bounded journal records accepted pages; and
one private publication directory may retain an exact page payload until all
of its canonical artifacts are durable. Control files contain no credentials,
backend URL, title, or body. Pull-affecting options are hash-bound. A surviving
publication intent or journal owns every exact destination-side atomic temp
name before that temp can exist. Recovery removes only the exact declared,
bounded regular residue, accepts only exact pre/post images, and preserves
unexpected evidence with exit `8`. Accepted journal entries are reconciled to
the sidecar and progress before journal retirement, so a hard crash neither
repeats their body GETs nor skips an uncommitted page. All state is removed only
after every selected page and the final mirror sidecar are durable.
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

## Confluence tables

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

## Page and section reads

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
