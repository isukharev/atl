# Sealed corpus generations

Sealed corpus generations are ATL's internal, backend-neutral boundary between
private staging bytes and one completely verified local corpus. The format is
owned by `internal/corpus`; it accepts already-produced member streams and does
not fetch from Jira or Confluence, interpret selectors, or render documents.

This format is separate from [`atl manifest create`](reference/cli/local-artifacts.md),
whose aggregate mirror manifest and behavior are unchanged. It is a library
contract rather than a new CLI surface. See also the package ownership in
[Architecture](architecture.md).

## Trust root and platform boundary

The caller creates the trust root before initialization. It must be an existing,
empty, owner-only directory with mode `0700`; ATL does not create or claim the
durability of its parent. Every store directory is exactly `0700`, and every
store file is exactly `0600`. A mode mismatch fails closed.

The durable implementation is enabled on the package's exact POSIX build-tag
set: AIX, Darwin, DragonFly BSD, FreeBSD, Linux, NetBSD, OpenBSD, and Solaris.
Other platforms return the stable unsupported result instead of approximating
the locking, link-count, or durability contract (`ErrUnsupported` in the
internal library).

"Immutable" means that ATL creates members exclusively, never overwrites a
sealed generation, and detects later tampering before use. It does not protect
bytes from the filesystem owner or another process with equivalent authority.

## Store shape and sensitivity

This synthetic, content-free example shows the v1 namespace after publication:

```text
owner-only-root/
  .publish.lock
  current.v1.json
  generations/
    0123456789abcdef0123456789abcdef/
      artifacts/
      manifest.v1.json
      receipt.v1.json
```

The random generation identifier carries no host, backend, selector, or object
identity. `artifacts/` contains the private member files. `manifest.v1.json` is
also private because it is the exact inventory: it contains member service,
stable identity, role, relative path, size, mode, and digest.

`receipt.v1.json` is written only after the generation is complete and is the
content-free seal marker. `current.v1.json` is the content-free selection
pointer. `.publish.lock` scopes concurrent publication and is released by the
operating system if its holder exits. A store without a valid current pointer
has no selected generation, even if it contains preserved staging directories.

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
directory name or partial file set.

Cleanup and garbage collection, backend I/O, rendering, retention policy, and
backup are outside this format. Those responsibilities require separate owners
and must not mutate sealed generations in place.
