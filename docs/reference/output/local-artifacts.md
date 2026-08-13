# Local-artifact output contracts

Local manifest and sealed-corpus result shapes.

[Reference index](README.md) · [Documentation home](../../README.md)

## Manifest creation

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

## Corpus build

`atl corpus build` returns one content-free successful build summary. Service
names, independent UTC capture windows, counts, physical request/response-byte
usage, closed dimension states, readiness, digests, build provenance, and
generation totals are present. Cleartext selectors, backend origins,
principals, object identities, titles, bodies, local paths, and member paths
are absent:

Verbose HTTP tracing retains only methods, statuses, and `<redacted>` route
markers for this command; backend paths and query values remain absent from
stderr.

```json
{
  "schema_version": 1,
  "source": "new",
  "services": [
    {
      "service": "jira",
      "status": "complete",
      "count": 2,
      "started_at": "2026-08-12T12:00:00Z",
      "completed_at": "2026-08-12T12:00:03Z",
      "usage": {"attempts": 6, "response_bytes": 4096},
      "dimensions": [
        {"dimension": "attachments", "state": "complete"},
        {"dimension": "comments", "state": "complete"},
        {"dimension": "metadata", "state": "complete"},
        {"dimension": "native", "state": "complete"}
      ]
    }
  ],
  "usage": {"attempts": 7, "response_bytes": 4352},
  "elapsed_ms": 3000,
  "reused": false,
  "projection": {
    "schema_version": 2,
    "projection_schema": 2,
    "readiness": "ready",
    "qualifications": [
      {
        "service": "jira",
        "state": "ready",
        "basis": "receipt",
        "scope_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "source_receipt_digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "reasons": []
      }
    ],
    "counts": {
      "documents": 4,
      "edges": 4,
      "markdown_files": 3,
      "markdown_bytes": 84,
      "artifacts": 1,
      "artifact_bytes": 1024
    },
    "documents_digest": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "edges_digest": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "markdown_digest": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    "artifacts_digest": "abababababababababababababababababababababababababababababababab",
    "projection_digest": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
  },
  "generation": {
    "generation_digest": "1111111111111111111111111111111111111111111111111111111111111111",
    "manifest_schema": 1,
    "receipt_schema": 1,
    "projection_schema": 2,
    "generator_version": "0.0.0-dev",
    "build_state": "unknown",
    "services": ["jira"],
    "totals": {"members": 6, "bytes": 2048}
  }
}
```

`source` is `new`, `resumed`, or `restarted`. `reused:true` means exact
recovery found an equivalent already-selected generation; otherwise a
successful result identifies the newly selected generation. `usage` includes
principal checks and all selected services. A service's usage covers only its
qualified complete pull, so the sum of service usage can be lower than the
aggregate. Capture windows remain separate and do not imply a cross-service
remote transaction.

Failures expose only `corpus build failed: phase=<closed> reason=<closed>` in
the normal error envelope. Phases are `validate`, `workspace`, `recover`,
`principal`, `capture`, `snapshot`, and `publish`; reasons are `usage`,
`budget`, `deadline`, `backend`, `integrity`, `drift`, and `outcome_unknown`.
The wrapped stable sentinel still selects the normal exit class.
No partially captured service result is emitted as success.

Comments and attachments are `not_requested` unless their flags are present.
Requested exact empty inventories are `complete`. With strict policy, any
requested partial or forbidden outcome fails before generation publication.
With `--allow-partial-evidence`, service dimensions and projection
qualification both report `partial`; they are never promoted to `ready`.
Attachment bodies contribute to `artifact_bytes` only when captured.

On a normal resumable failure, rerunning the exact command continues the
retained attempt under its original deadline and cumulative budget. A
`recover/outcome_unknown` result caused by `remote_in_flight` requires explicit
`--restart`; never infer rollback or automatically replay it. Consumers keep
using whichever fully verified generation the current pointer selects.
`publish/outcome_unknown` can mean the new generation is already current but
the completed attempt record did not reach a confirmed durability barrier;
repeat exact options without `--restart` so ATL can verify the visible current
generation and active record before it resumes recovery or starts another
bounded capture.

## Corpus generation diff

`atl corpus diff --store DIR` returns only content-free transition evidence:

```json
{
  "schema_version": 1,
  "qualification": "qualified",
  "reason": "absent_from_qualified_generation",
  "counts": {
    "added": 3,
    "retained": 120,
    "changed": 8,
    "tombstoned": 2
  },
  "predecessor_generation_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "successor_generation_digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "tombstone_digest": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "identity_artifact_written": false
}
```

The output never includes stable identities, paths, keys, titles, selectors,
backend origins, principals, or body bytes. Counts classify the exact direct
transition. A surviving stable identity whose key, title, hierarchy, path, or
content changed is `changed`, not tombstoned. A predecessor-only identity uses
the sole closed reason `absent_from_qualified_generation`; that reason is
index-membership evidence, not a backend-deletion claim.

With `--identity-artifact PATH`, successful output changes only
`identity_artifact_written` to `true`. The exclusive `0600` artifact adds the
stable identity records, per-record predecessor/successor document digests,
qualified service bindings, and both final generation digests. ATL refuses an
existing target, a parent that is not owner-only, or any direct/symlink-aliased
destination inside the sealed store. Artifact failures expose
only a closed operation category and never echo the path. No artifact is
created by default.

The command fails closed when the current generation lacks exactly one valid
delta member, when its predecessor cannot be fully verified, or when capture,
projection, inventory, digest, or direct-lineage evidence differs. It emits no
partial identity result.

## Corpus export

`atl corpus export` returns only schema, qualification, digest, count, build,
and reuse fields. It never writes mirror or store paths, selectors, backend
origins, object identities, titles, bodies, or member paths to stdout:

```json
{
  "schema_version": 2,
  "reused": false,
  "projection": {
    "schema_version": 2,
    "projection_schema": 2,
    "readiness": "partial",
    "qualifications": [
      {
        "service": "jira",
        "state": "partial",
        "basis": "structural",
        "scope_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "reasons": ["legacy_mirror"]
      }
    ],
    "counts": {
      "documents": 3,
      "edges": 2,
      "markdown_files": 2,
      "markdown_bytes": 42,
      "artifacts": 0,
      "artifact_bytes": 0
    },
    "documents_digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "edges_digest": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "markdown_digest": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "artifacts_digest": "abababababababababababababababababababababababababababababababab",
    "projection_digest": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
  },
  "generation": {
    "generation_digest": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
    "manifest_schema": 1,
    "receipt_schema": 1,
    "projection_schema": 2,
    "generator_version": "0.0.0-dev",
    "build_state": "unknown",
    "services": ["jira"],
    "totals": {"members": 5, "bytes": 2048}
  }
}
```

`readiness` is the weakest source qualification. `partial` is valid sealed
output but is not proof of complete backend selection. `reused:true` means the
already-selected generation exactly matched the requested projection and build
identity. Errors remain content-free and use the normal stable CLI exit classes.
An unreconciled seal or pointer write additionally retains the stable
durable-outcome-unknown classification so callers do not mistake ambiguity for
a definite pre-write failure.

The private generation retains byte-compatible `documents.indexer-v1.jsonl`,
`edges.indexer-v1.jsonl`, and `receipt.indexer-v1.json` members for legacy
readers. Indexer-v2 adds `artifacts.indexer-v2.jsonl`,
`receipt.indexer-v2.json`, and one `asset` member for each record whose closed
body status is `captured`. Artifact records contain a stable attachment and
parent identity, exact source lineage, media type, declared size, closed body
status/reason, and, for captured bytes only, a contained path/size/SHA-256.
Unrequested, excluded, forbidden, and failed bodies never name a valid-looking
asset path.
