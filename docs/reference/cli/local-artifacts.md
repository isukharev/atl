# Local artifacts

Local manifest generation and backend-identity hashing.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl manifest create`

Write a backend-identity-hashed manifest for a local mirror or snapshot root. The manifest
counts files/bytes/extensions and records reproducibility metadata such as the
source command, selectors, fields, include flags, ATL version, and backend URL
hashes. Configured backend URLs are represented only by hashes and stored PATs
are not read. Caller-provided command text, selectors, JQL/CQL, field names,
include values, and paths are preserved verbatim without redaction: never put a
credential in those flags, and review the manifest before publishing it.

```bash
atl manifest create --root mirror-jira --service jira --selector 'jql=project=PROJ' --fields summary,status
atl manifest create --root mirror-conf --service confluence --out mirror-conf/manifest.json
```

Flags:

| flag | description |
|---|---|
| `--root` | local mirror/snapshot root directory (required) |
| `--out` | manifest output path (default `<root>/manifest.json`) |
| `--service` | optional `jira`, `confluence`, or `generic` |
| `--selector` | comma-separated selectors to record |
| `--fields` | comma-separated field names/ids to record |
| `--include` | comma-separated include flags to record |
| `--command` | command string to record (default `atl manifest create`) |

---
