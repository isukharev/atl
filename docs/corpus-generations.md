# Sealed corpus generations

Sealed corpus generations are ATL's backend-neutral boundary between private
mirror evidence and one completely verified local corpus. The durable format is
owned by `internal/corpus`; it accepts already-produced member streams and does
not fetch from Jira or Confluence or interpret backend selectors. The
`atl corpus export` application path separately projects pristine local mirror
baselines into those member streams without configuration, credentials, or
network access. `atl corpus build` is the explicit remote-read orchestrator: it
captures nominated qualified selections into private attempts and invokes the
same projection and publication boundary only after every selected service
reconciles.

This format is separate from [`atl manifest create`](reference/cli/local-artifacts.md),
whose aggregate mirror manifest and behavior are unchanged. See the
[`atl corpus export` reference](reference/cli/local-artifacts.md#atl-corpus-export)
and package ownership in [Architecture](architecture.md).

## Qualified capture and publication

`atl corpus build` selects only the caller-nominated Jira project and/or
Confluence space. It does not enumerate containers, follow discovered targets,
infer deleted objects, or perform any backend write. The command requires an
invocation-wide `--read-only` or `ATL_READ_ONLY=1` policy before loading
configuration or credentials. Every selected adapter also receives a deny-all
write authorizer. Unselected service credentials are not loaded.

One command-scoped scheduler and one physical read budget cover principal
qualification, retries, redirects, both complete selections, and body reads.
The absolute deadline and cumulative request/response-byte usage are persisted
in the active attempt, so an ordinary resume cannot reset them. Jira and
Confluence capture sequentially into separate attempt roots and carry separate
start/completion times. A generation containing both services proves two
qualified captures; it does not claim a remote transaction or one shared
snapshot instant.

Each service receipt binds only content-free evidence: service, principal-scope
digest, selector/options/selection/snapshot digests, capture window, exact
total/completed count, pull usage, and closed native/metadata/comments/
attachments states. The scope hashes the qualified backend origin and stable
authenticated principal; neither value is persisted in cleartext. Ready
publication requires the complete-pull selection digest and total to match the
full provider-ID inventory and pristine snapshot fingerprint. Local mirror
health alone cannot upgrade structural evidence to ready.

The build fixes rendering to the minimal profile, UTC display semantics, and
disabled Confluence Jira-macro expansion, independently of global or local
presentation config. Native bodies are identity-checked and stored verbatim;
Markdown and JSONL are derived only after the pristine snapshots reconcile.
Comments and attachments default to `not_requested`. When selected, the
capture receipt records each dimension as `complete` or `partial`; an exact
empty inventory is complete rather than omitted. Jira comments use a dedicated
bounded endpoint, and both services bind evidence to the captured parent stable
identity and revision. Attachment inventory is independent of body capture.
Bodies require an exact MIME allowlist plus per-item and generation-wide count
and byte limits. ATL streams only adapter-qualified attachment references into
contained owner-private files, validates declared/actual size and SHA-256, and
never crawls narrative URLs, expands archives, performs OCR, or executes
content.

Strict mode refuses any requested incomplete inventory or body outcome. The
explicit `--allow-partial-evidence` policy may seal closed forbidden, failed,
truncated, or limit reasons, but the capture qualification and generation
readiness are `partial`. Excluded media types are a complete policy outcome;
they do not claim that bytes were captured.

Only after every requested receipt is durable does the build produce canonical
documents/edges, seal the exact generation inventory, atomically publish the
same-filesystem current pointer, and verify that selection. The previous
generation is never removed. A consumer opens only the sealed current
generation and has no reason to inspect active attempts.

Recovery is deliberately asymmetric:

- a returned remote error records known cumulative usage and leaves the exact
  attempt resumable under its original options and deadline;
- a process loss while `remote_in_flight` is durable is ambiguous and cannot be
  replayed automatically;
- explicit `--restart` first recovers the old service publication/journal,
  marks that attempt retained, then starts a fresh random attempt; and
- a completed active record remains as content-free recovery evidence.

If the generation is selected but the final completed active-record barrier
cannot be confirmed, the command reports `publish/outcome_unknown`. The current
pointer may already name that fully verified generation. Preserve the root and
repeat the exact command without `--restart`: ATL resolves the visible current
generation and active record under their normal verification rules. Depending
on which completed barrier survived, this may resume local reconciliation or
begin the next bounded capture.

No build path deletes attempts, stages, or generations. Retention and garbage
collection require a separate policy.

## Indexer-v2 projection

The projection preserves canonical indexer-v1 JSONL document and edge
inventories and clean Markdown members, then adds an
`artifacts.indexer-v2.jsonl` inventory and an indexer-v2 content-free receipt.
The v1 receipt remains present for legacy readers. Stable
document identities bind backend-origin digest, service, object kind, and
numeric provider id; mutable keys, titles, paths, and presentation fields do not
change identity. Edges either name an included stable identity or preserve one
bounded unresolved key, title, or numeric id. Raw backend URLs are rejected.

Jira issues, comments, attachments, hierarchy, typed issue links, and body
references are projected when their correlated pristine evidence exists.
Confluence pages, comments, attachments, hierarchy, page references, and Jira
macros are treated similarly. Qualified attachment filenames resolve page
references only when they select one stable attachment identity; ambiguous or
title/key-only targets remain explicit unresolved references. A two-source
export can therefore emit relative
Markdown links and typed cross-service edges without fetching discovered
targets. Render failure is explicit and does not substitute native bytes into
Markdown. A null issue-link field or malformed Jira issue-link row makes
relation evidence unavailable or partial; only an actual empty array proves an
exact empty relation set, and silently dropped transport rows never do.

Every attachment has one stable attachment document, one exact
`attachment_owner` edge, and one artifact record. The record carries body
status, exact media type, declared size, parent/inventory lineage, and—only for
`captured`—a contained relative asset path, byte size, and SHA-256. The v2
receipt binds the unchanged document/edge/Markdown digests plus artifact count,
bytes, and digest. A captured record without exactly one matching sealed
`asset` member, or an asset member without a matching record, fails validation.
Inline render assets remain a separate mirror/view class and cannot satisfy
this canonical attachment contract.

Every document carries per-category evidence and conservative visibility.
Absence of a Jira issue-security level is not evidence of unrestricted access.
The receipt derives `ready`, `partial`, or `unavailable` from the weakest source
qualification. Current mirrors have structural correlation rather than an
independent complete-pull receipt, so their projection is sealed as `partial`;
consumers must retain that qualification.

Export holds service snapshot locks while it inventories and streams bounded
pristine evidence, revalidates the snapshot, then releases those locks before
sealing. It preflights the actual generation member count, every complete
member's bytes, and their aggregate bytes before initializing a store or
beginning a stage. Working `.csf`, `.wiki`, and `.md` files are ignored. Staged
lineage is refused by default; `--allow-unreconciled` is diagnostic and can
never produce a ready projection.

## Trust root and platform boundary

The caller creates the trust root before initialization. It must be an existing,
empty, owner-only directory with mode `0700`; ATL does not create or claim the
durability of its parent. Every store directory is exactly `0700`, and every
store file is exactly `0600`. A mode mismatch fails closed.

The durable implementation is enabled on the package's exact POSIX build-tag
set: Darwin, DragonFly BSD, FreeBSD, Linux, NetBSD, OpenBSD, and Solaris. Other
platforms, including AIX and Windows, return the stable unsupported result
instead of approximating the locking, link-count, or durability contract
(`ErrUnsupported` in the internal library).

"Immutable" means that ATL creates members exclusively, never overwrites a
sealed generation, and detects later tampering before use. It does not protect
bytes from the filesystem owner or another process with equivalent authority.

## Store shape and sensitivity

This synthetic, content-free example shows the v1 namespace after publication:

```text
owner-only-root/
  .build.lock
  .publish.lock
  active.v1.json
  current.v1.json
  attempts/
    fedcba9876543210fedcba9876543210/
      confluence/
      jira/
      receipts/
        confluence.capture.v1.json
        jira.capture.v1.json
  generations/
    0123456789abcdef0123456789abcdef/
      artifacts/
      manifest.v1.json
      receipt.v1.json
```

Random attempt and generation identifiers carry no host, backend, selector, or
object identity. `attempts/` contains active and retained native mirrors and is
private. `active.v1.json` and capture receipts are content-free but remain
owner-only recovery records. `.build.lock` serializes build attempts and is
crash-scoped. `artifacts/` contains the private sealed member files.
`manifest.v1.json` is also private because it is the exact inventory: it
contains member service, stable identity, role, relative path, size, mode, and
digest.

`receipt.v1.json` is written only after the generation is complete and is the
content-free seal marker. `current.v1.json` is the content-free selection
pointer. `.publish.lock` scopes concurrent publication and is released by the
operating system if its holder exits. A store without a valid current pointer
has no selected generation, even if it contains preserved staging directories.

Member service namespaces are closed. `jira` and `confluence` identify
source-owned members. `aggregate` identifies a member that combines both
sources, such as one cross-service document or edge inventory, and is accepted
only when both Jira and Confluence qualifications are present. A single-source
inventory remains in that backend's namespace. Source qualifications never
accept `aggregate`, so a member cannot invent a synthetic backend qualification
or weaken the evidence required for either source.

## Creation, sealing, and publication

1. Beginning a generation creates a private directory with a random,
   content-free identifier. Callers stream every member through codec-owned
   `Add`; the codec exclusively creates the durable file and rejects duplicate
   member tuples or paths. Callers do not pre-populate or adopt an artifact
   tree.
2. Sealing performs an exact inventory pass, writes the private canonical
   manifest exclusively, and performs a second exact inventory pass. Any
   difference is concurrent drift and fails the seal.
3. Only after those inventories agree does sealing write the canonical receipt
   exclusively and last. File and directory `fsync` barriers order member,
   manifest, receipt, generation-directory, and store-directory durability.
   A receipt that may have reached durable storage when an error occurs is
   reported as an unknown outcome; it is never silently recreated or replaced.
4. Publication fully verifies the target, takes the crash-scoped store lock,
   verifies the current generation, and compares the target's predecessor
   digest with that current generation. A stale predecessor loses the
   compare-and-set.
5. Publication writes and syncs a temporary content-free pointer, atomically
   replaces `current.v1.json` on the same filesystem, syncs the store directory,
   and reads the pointer back. A failure after replacement is an unknown
   outcome, not permission to roll back or overwrite generation bytes.

Publication never deletes a predecessor. Staging, failed, and sealed-but-not-
published generations are preserved. Only a coherent pointer to a generation
with a valid receipt can become the current consumer selection; incomplete or
unpointed state is not selected.

## Exact verification and reads

Opening a selected generation pins its directory with `os.Root` and fully
verifies the receipt, manifest, and exact tree before returning it. Verification
fails closed on:

- an extra or missing directory, member, manifest, or receipt;
- a symlink, special file, or file with more than one hard link;
- any directory or file whose mode is not exactly private;
- an absolute, traversing, non-canonical, overlong, over-deep, or otherwise
  unsafe member path;
- a duplicate service/stable-id/role tuple or duplicate member path, including
  a case-folding alias;
- member-count, member-size, total-size, path, depth, or manifest-size overflow;
- a size, mode, digest, lineage, total, or schema mismatch; or
- metadata or byte drift observed during a bounded scan.

Selection uses one coherent pointer read. A publication racing immediately
after that read may leave the caller holding the prior generation; this is safe
because every returned generation was fully verified, remains pinned, and is
never removed by this format.

`CopyMember` looks up one private member tuple and revalidates the pinned file's
identity, regular-file type, link count, exact mode, size, and digest while
streaming it. The destination can contain a partial prefix if validation or I/O
fails, so callers must discard the entire sink on every error.

## Canonical JSON and digest binding

The manifest, receipt, and pointer use schema-v1 canonical compact JSON with a
single trailing newline. Decoding is strict: unversioned input, unknown or
duplicate fields, trailing values, non-canonical bytes, and future durable
schema versions all fail closed. V1 is the complete currently accepted durable
schema set.

Digest domains are distinct, and each hashed part is length-prefixed before it
enters SHA-256. The receipt binds the exact canonical manifest, the canonical
member inventory, qualification and predecessor lineage, generator/build state,
tombstone state, and aggregate totals. Its generation digest covers the
canonical receipt preimage after excluding only the generation-digest field
itself. Consequently, copying a receipt onto another inventory or lineage does
not produce a valid generation.

A future schema migration must construct, seal, verify, and publish a new
generation. It must never rewrite a prior generation's manifest, receipt,
members, or digest in place.

## Privacy and recovery contract

Manifests and members are private even when their surrounding store is
owner-only. Receipts, pointers, summaries, and errors are deliberately
content-free: they contain no host, cleartext selector, stable object identity,
title or body, member path, trust-root path, or raw corrupt bytes. They may
contain schema and generator state, service categories, digests, counts, and
byte totals. Content-free output reduces disclosure; it is not by itself
authorization to publish a private artifact.

A missing or invalid receipt leaves a generation unsealed. A missing, invalid,
or stale pointer leaves it unselected. Both forms are preserved for owner-side
inspection. A failed stage or publication that never committed a new pointer
leaves the last valid pointer and generation usable. Recovery reopens the store
and performs the same complete verification; it does not infer success from a
directory name or partial file set. The export path attempts that exact
verification after an ambiguous seal or pointer result. If it still cannot
reconcile, the content-free error retains the stable durable-outcome-unknown
classification even if reconciliation was cancelled or timed out; preserve the
store and do not infer rollback or success.

Cleanup and garbage collection, backend I/O, rendering, retention policy, and
backup are outside the sealed-store format. `corpus build` owns bounded backend
reads and derived rendering before it crosses that format boundary; retention
remains separate. None of those responsibilities may mutate sealed generations
in place.
