# Local artifacts

Local manifest generation, backend-identity hashing, and sealed indexer corpora.

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

## `atl corpus export`

Project the pristine baselines of one or two initialized mirrors into a private,
sealed `indexer-v1` generation. This command is deliberately zero-egress: it
does not load global configuration or credentials, contact a backend, or check
for updates. It reads `.atl` baselines and correlated metadata, not ambient
working `.csf`, `.wiki`, or `.md` edits.

Create the store root yourself with owner-only mode, then initialize it once:

```bash
install -d -m 0700 /private/indexer-corpus
atl corpus export --jira /private/jira-mirror \
  --confluence /private/confluence-mirror \
  --store /private/indexer-corpus --initialize-store
```

Later exports omit `--initialize-store`. An exact existing projection is reused;
otherwise ATL seals a new immutable generation and atomically selects it.
The actual member count and complete member/aggregate bytes are checked before
store initialization or staging. Neither publication nor recovery deletes older
or incomplete generations. If exact verification cannot reconcile an ambiguous
seal or pointer result, preserve the store; the error reports a content-free
durable-outcome-unknown classification rather than claiming failure or success.

Flags:

| flag | description |
|---|---|
| `--jira` | initialized Jira mirror root; optional when `--confluence` is set |
| `--confluence` | initialized Confluence mirror root; optional when `--jira` is set |
| `--store` | existing owner-only sealed-generation store root (required) |
| `--initialize-store` | initialize an existing empty `0700` store root |
| `--allow-unreconciled` | diagnostic export from pristine bases despite staged lineage; output remains non-ready |

The store contains private titles, text, paths, references, and Markdown. Keep
the whole root out of source repositories and indexes that may publish data.
Current structural mirror evidence is explicitly `partial`; consumers must not
promote it to `ready`. Export refuses staged/unreconciled lineage by default.
Windows and AIX return the stable unsupported result because ATL cannot provide
the required filesystem contract there.

---
