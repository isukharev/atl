# Local-artifact output contracts

Local manifest result shapes.

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
