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

## Corpus export

`atl corpus export` returns only schema, qualification, digest, count, build,
and reuse fields. It never writes mirror or store paths, selectors, backend
origins, object identities, titles, bodies, or member paths to stdout:

```json
{
  "schema_version": 1,
  "reused": false,
  "projection": {
    "schema_version": 1,
    "projection_schema": 1,
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
    "counts": {"documents": 3, "edges": 2, "markdown_files": 2, "markdown_bytes": 42},
    "documents_digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "edges_digest": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "markdown_digest": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "projection_digest": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
  },
  "generation": {
    "generation_digest": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
    "manifest_schema": 1,
    "receipt_schema": 1,
    "projection_schema": 1,
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
