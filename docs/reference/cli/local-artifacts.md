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

## `atl corpus build`

Capture one nominated Jira project, one nominated Confluence space, or both
into separate private attempt mirrors, reconcile each qualified complete pull
with its pristine provider-ID inventory, project canonical `indexer-v1`
members, and atomically select one sealed `ready` generation. The command never
discovers projects or spaces and never sends a mutating backend request.

The caller creates the owner-only trust root. Initialization is explicit and
works only for an empty directory with exact mode `0700`:

```bash
install -d -m 0700 /private/indexer-corpus
export ATL_READ_ONLY=1
atl corpus build \
  --root /private/indexer-corpus --initialize \
  --jira-project EXAMPLE --max-jira-issues 5000 \
  --confluence-space DOCS --max-confluence-pages 5000 \
  --max-requests 20000 --max-response-bytes 4294967296 \
  --max-members 100000 --max-generation-bytes 8589934592 \
  --deadline 2h --max-in-flight 4 --requests-per-second 20
```

Later captures omit `--initialize`. `--read-only` or `ATL_READ_ONLY=1` is a
mandatory invocation input, not merely a compatible global configuration
value. ATL checks that requirement before configuration, credentials,
self-update, or network access. Only selected services load a PAT and construct
a client for their configured URL. Local attempt and generation writes still
occur under the named root.

Every bound is finite and required. Reaching a request, response-byte,
deadline, selection, snapshot, member, or generation-byte limit prevents
publication. Physical retries and redirect hops count as attempts, and both
services share the same attempt, response-byte, concurrency, and start-rate
guards. Captures run sequentially and record independent windows; a two-service
generation never claims one remote atomic instant. The derived view is fixed to
the minimal profile with Confluence Jira-macro expansion off. Comments and
attachments are recorded as `not_requested` in this command version. Even with
`--verbose`, request URLs and response paths are rendered as `<redacted>` so a
selector or object identity cannot enter the trace.

| flag | description |
|---|---|
| `--root` | existing owner-only corpus root (required) |
| `--initialize` | initialize an existing empty exact-`0700` root; mutually exclusive with `--restart` |
| `--restart` | recover the interrupted attempt's local journal, retain it, and begin a fresh attempt |
| `--jira-project` | canonical uppercase Jira project key; optional when Confluence is selected |
| `--max-jira-issues` | required Jira selection cap, `1..100000`, only with `--jira-project` |
| `--confluence-space` | canonical Confluence space key; optional when Jira is selected |
| `--max-confluence-pages` | required Confluence selection cap, `1..100000`, only with `--confluence-space` |
| `--max-requests` | aggregate physical HTTP-attempt cap, `1..10000000` |
| `--max-response-bytes` | aggregate buffered response-byte cap, `1..68719476736` |
| `--max-members` | sealed member and per-snapshot item guard, `1..100000` |
| `--max-generation-bytes` | sealed generation and per-snapshot byte guard, `1..68719476736` |
| `--deadline` | original attempt duration, greater than zero and at most `24h` |
| `--max-in-flight` | shared concurrent physical-attempt cap, `1..8` |
| `--requests-per-second` | shared request-start cap, `1..1000` |

An ordinary returned read error durably records consumed budget and permits an
exact resume with unchanged options and the original deadline. A hard process
loss while `remote_in_flight` is set has an ambiguous remote read outcome and
is never replayed automatically: rerun with `--restart`. Restart first recovers
service-owned local publication/journal state, marks the old attempt retained,
then creates a random fresh attempt. It never deletes attempts, generations, or
the previous current pointer. A completed active record is also retained.

If publication selects and verifies a generation but the final completed
active-record barrier cannot be confirmed, the command returns
`phase=publish reason=outcome_unknown`. The current pointer may already name
that generation. Preserve the root and repeat the exact command without
`--restart`; ATL verifies the visible current generation and active record
before resuming recovery or beginning the next bounded capture.

Consumers open only `current.v1.json` through the sealed-generation reader.
They never inspect `attempts/`, active records, or working mirror files. Keep
the entire root outside source repositories and any index that could publish
private data. See [Sealed corpus generations](../../corpus-generations.md) for
the exact durability and privacy model.

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
