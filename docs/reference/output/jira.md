# Jira output contracts

Jira mirrors, issue evidence and mutations, exports, graphs, references, and reports.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [Guarded targeted description edits](#guarded-targeted-description-edits)
- [Guarded Jira labels](#guarded-jira-labels)
- [Guarded Jira links](#guarded-jira-links)
- [Jira mirrors and derived views](#jira-mirrors-and-derived-views)
- [`jira attachment-bodies`](#jira-attachment-bodies)
- [Jira mirror status, apply, and push](#jira-mirror-status-apply-and-push)
- [Jira exports](#jira-exports)
- [Exports, tables, and reports](#exports-tables-and-reports)
- [Jira export comparison, reports, and field catalogs](#jira-export-comparison-reports-and-field-catalogs)
- [Jira graphs and references](#jira-graphs-and-references)
- [Attachments, guarded mutations, and worklogs](#attachments-guarded-mutations-and-worklogs)
- [Guarded Jira CSV plans](#guarded-jira-csv-plans)
- [Jira epic digest](#jira-epic-digest)
<!-- reference-navigation:end -->

## Guarded targeted description edits

`atl jira issue edit preview <KEY>` and the default preview of the
mutation-classified parent emit the same content-free schema-v1 result. It
contains `backend_sha256`, normalized `requested_key`, exact canonical `key`,
positive numeric `issue_id`, parsed `updated`, `mode`, closed `status`, exact
old/new and before/after SHA-256 plus byte lengths, `all`, matcher
`pass|count|offsets`, `proposal_hash`, `write_attempted`, `reconciled`, and
`complete`. Live description and replacement bytes are never emitted.

Statuses are `would_apply`, `already_satisfied`, `blocked`, `not_applied`,
`applied`, `recovered`, or `outcome_unknown`. Only `--apply` with the exact
reviewed hash can attempt one PUT. `recovered` means an ambiguous response was
followed by exact intended bytes and an advancing `updated`; every unavailable,
unchanged, or conflicting readback is `outcome_unknown` and exit 8.

## Guarded Jira labels

`jira issue labels` and its independent `preview` child emit content-minimized
schema-v1 JSON only. The result identifies the operation, backend digest,
requested/canonical key, numeric issue id, project and exact `updated`; includes
the sorted requested `add`/`remove` values; and represents current, desired and
effective-delta sets only as `{count,sha256}`. It also carries fixed bounds,
usage, `proposal_hash`, `mode`, closed `status`, `write_attempted`,
`reconciled`, and `complete`. Unrelated current labels are never emitted.
The bounds include a 64-byte requested-key cap alongside the label, request,
response-byte, and deadline limits bound into the proposal hash.

Statuses are `would_apply`, `already_satisfied`, `blocked`, `not_applied`,
`applied`, `recovered`, or `outcome_unknown`. A matching already-satisfied
proposal makes only the initial GET. Otherwise apply uses at most four Jira
attempts under one 60-second deadline and 16 MiB aggregate response/error-body
budget: initial GET, immediate numeric-id GET, one numeric-id PUT, and one
numeric-id readback. Preview permits one GET. Only exact desired labels plus a
strictly advancing `updated` prove `applied` or `recovered`; unavailable,
moved, malformed, non-advancing, or conflicting evidence is terminal
`outcome_unknown`, exit 8, and not replay-safe.

## Guarded Jira links

`jira issue link add|delete` and their independent `preview` children emit only
schema-v1 JSON. The result contains `operation`, content-free backend origin,
semantic `requested_from`/`requested_to`, requested selector/link id, exact
outward/inward endpoint `{id,key,project,role}`, selected type
`{id,name,inward,outward}`, `resolved_role`, complete type-catalog count/digest,
candidate-only reciprocal evidence, `proposal_hash`, `mode`, closed `status`,
`write_attempted`, `reconciled`, and `complete`. Native issue content, unrelated
link inventory, response bodies, and policy detail are never emitted.

Statuses are `would_apply`, add-only `already_satisfied`, `blocked`,
`not_applied`, `applied`, `recovered`, and `outcome_unknown`. `applied` requires
exact reciprocal intended state after add, or exact link-id absence on both
complete endpoints with no semantic replacement after delete. `recovered`
proves only the same bounded end state after an ambiguous response, not causal
attribution. Any missing, retained, duplicate, conflicting, moved, incomplete,
or deadline-exhausted readback is `outcome_unknown` and exit 8.

## Jira mirrors and derived views

`atl jira issue view <KEY>` is the non-persistent counterpart to a mirror view.
It writes no files and emits `{"key":<KEY>,"markdown":<configured-view>}` by
default; under `-o text`, stdout is the exact raw Markdown string with no
emitter-added newline (matching `conf page view`). Advisory render
warnings remain on stderr. The selected render root is read only for its local
presentation config and gains no snapshot, sidecar, assets, or writeback state.
Consequently transient output cannot be applied or pushed: pull the issue fresh
before editing it.

`atl jira pull` writes three files per issue: `<KEY>.wiki` (the native Jira wiki body, byte-for-byte —
the editable substrate), `<KEY>.md` (a derived Markdown staging view rendered from the wiki and
regenerated best-effort on pull/render), and `<KEY>.json` (the raw-fields snapshot). The pull
result's `path` points at the `.md`; `wiki_path` points at the sibling `.wiki` substrate. To use the
friendly surface, edit generated `# Description` and/or field sections explicitly configured as
editable, then run `jira apply`. Description changes merge into `.wiki`; field changes become an
explicit `.atl/pending/jira/<KEY>.json` write set. The raw issue snapshot is not changed until a
successful push refreshes it. `.md` is never sent directly and a later pull/render can replace it. Edit
`.wiki` directly for constructs the staging view cannot express. Generated issue fields appear in a
read-only `# Metadata` Markdown table; update them through dedicated commands, not by editing the
table. A typed field section is editable only with `editable:true`, `placement:"section"`, and
`format:"jira_wiki"`; transient `jira issue view` output remains read-only. Generated regions carry hidden stable `atl:section`
markers; Jira rich-text headings are nested below their generated owner. Human-facing
datetime values are compacted to minute precision, while the JSON snapshot keeps
the exact raw server value. The JSON snapshot is an object with
stable identity at the top level and raw Jira fields under `fields`:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "status_id": "11",
  "fields": {
    "summary": "Issue summary"
  }
}
```

`--fields` on `jira pull` adds requested fields to that `fields` map; the command still includes the
core fields needed to render the markdown view and choose the project/key path.

The `jira pull` stdout summary is `{ "into": <root>, "issues": [ { "key", "path", "wiki_path", "status", "assets", "epic_children" }, ... ] }`.
`status` is omitted on an ordinary successful pull; pull previews use
`would_pull`, while a preserved item uses `blocked`.
With `--assets`, each issue object gains an `assets` count of image attachments mirrored into
`<KEY>.assets/`, and the top-level result gains `assets_skipped` when some images could not be
downloaded. Both `assets` and `assets_skipped` are `omitempty`: a default (no `--assets`) pull, and a
`--assets` pull where nothing was skipped, produce the same shapes as before. The raw `<KEY>.json`
snapshot is never modified by `--assets` — it mirrors Jira's response and carries no local file paths.

With `--complete`, the ordinary top-level fields remain and `complete_pull`
binds the qualified project selection and durable prefix:

```json
{
  "into": "mirror-jira",
  "issues": [
    {"key":"PROJ-2","path":"PROJ/PROJ-2.md","wiki_path":"PROJ/PROJ-2.wiki"}
  ],
  "complete_pull": {
    "selector_sha256": "<sha256>",
    "selection_sha256": "<sha256>",
    "source": "resumed",
    "complete": true,
    "total": 2,
    "completed": 2,
    "remaining": 0,
    "checkpoint_active": false
  }
}
```

`source` is `new|resumed|restarted`. `issues[]` contains only payloads fetched
by this invocation, whereas `completed` includes an accepted prefix recovered
from an earlier run. Success has `complete:true`, `remaining:0`, and
`checkpoint_active:false`. A terminal incomplete search emits
`complete:false`, omits `selection_sha256`, includes one closed static
`partial_reason`, and exits with the normal check-failed class without writing
issue payloads or a new checkpoint. Other failures use the normal error
envelope and retain the private checkpoint. The hashes and all counts are content-free. The
checkpoint and bounded journal contain numeric issue identities and exact local
publication state, never credentials, backend URLs, titles, descriptions, or
raw fields. Stable numeric identity may move to a new key/path only through the
qualified current schema-6 relocation transaction. A non-empty legacy asset directory
without an ownership inventory blocks relocation and is preserved. Completion proves the selected
project membership, not absence/deletion and not the separate local-integrity
contract reported by `jira snapshot`.

`--complete --comments` and `--complete --attachments` do not add private
content to stdout: the `JiraPullResult` and `complete_pull` JSON shapes above
remain unchanged. They add only bounded mode-`0600` mirror artifacts beside an
accepted issue: `<KEY>.comments.json`, `<KEY>.attachments.json`, and, when
`--attachment-bodies` is selected, `<KEY>.attachments/<attachment-id>.body`.
The sidecars bind backend population, immutable issue ID, parent `updated`
revision, native hash, and raw-snapshot hash; a captured body also has exact
path, byte count, and digest evidence. Resume rechecks those receipts before
restoring aggregate body capacity. If the next complete invocation does not
select one of these optional kinds, its owned receipt is retired in the same
publication transaction. Ordinary pull refuses a primary refresh that would
leave a qualified receipt stale. See the exact flags, strict failure semantics,
and byte/item caps in [Jira mirrors](../cli/jira-mirrors.md#atl-jira-pull).

When the opt-in `epic_children` render section is enabled, epic issue objects
gain an `epic_children` count (omitted at zero) and the mirror gains
`<KEY>.epic-children.json`:

```json
{
  "epic": "PROJ-1",
  "epic_field": "customfield_10001",
  "epic_selector": "Epic Link",
  "children": [
    {"key": "PROJ-2", "summary": "Implement capability", "status": "Open", "type": "Story"}
  ],
  "truncated": true,
  "truncated_at": 1000
}
```

`children` is always an array. The truncation fields are `omitempty`; when any
related query hits the cap, the top-level pull result also carries
`epic_children_truncated: true` and `epic_children_truncated_at: 1000`, and the
CLI warns on stderr. The sidecar is derived/offline-render data and never enters
the `.wiki` content hash or remote drift gate. Offline render/apply accept it
only when its epic key, configured selector (when present), and resolved epic
field match the issue/view affinity; otherwise it is ignored and render warns
to re-pull. `epic_selector` is omitted for auto-detection and retained for any
explicit configured selector (display name or field id), so changing that
selector cannot reuse a stale sidecar resolved from a different field.

**Render profiles and typed field views do not otherwise change the `pull`
JSON.** Profiles and ordinary include/exclude sections only affect the derived
`.md`; `epic_children` is the explicit exception because it reports related-data
counts/truncation as described above. Unknown section names in an
include/exclude list produce a `warning:` line on **stderr** and are ignored —
never an error, never on stdout.

`atl jira render [DIR|FILE] [--render-*]` and `atl conf render [DIR|FILE]
[--render-*]` regenerate `.md` views offline (no network/PAT). `jira render` emits
`{ "root": <mirror-root>, "rendered": [ { "key", "path" }, ... ] }`; `conf render`
emits `{ "root", "rendered": [ { "id", "title", "path" }, ... ] }`, one entry per
rewritten `.md`. Both leave the `.csf`/`.wiki`/`.json` substrate and the sidecar
`pages` sync entries untouched (they record each view's render settings,
including the presentation-only display timezone, typed field descriptors,
and the resolved epic field, in the
sidecar `views` map only, so a later `apply` can reproduce it), so `status` is
unchanged before and after. Render-resolution warnings go to **stderr**, never
stdout.

## `jira attachment-bodies`

`atl jira attachment-bodies` emits one content-free local continuation result:

```json
{
  "schema_version": 1,
  "into": "mirror-jira",
  "inventories": 12,
  "pending": 31,
  "captured": 8,
  "remaining": 23,
  "complete": false
}
```

`inventories` is the number of qualified existing Jira attachment sidecars;
`pending` is their deterministic work queue before this invocation; `captured`
is the number of one-body local transactions committed now; and `remaining`
is recomputed from fully revalidated private evidence before the result is
emitted. No issue key, attachment id, filename, path, MIME type, byte count,
digest, native body, or backend address is included. `complete:true` means no
sidecar row remains pending under the supplied strict policy. An invocation
stopped by its caller-selected transaction cap is a normal `complete:false`
result; safety, local-integrity, selector, or backend-read failures use the
normal error envelope instead.

## Jira mirror status, apply, and push

`atl jira status [DIR | --into ROOT] [--remote]` emits `{ "entries": [ { "path", "key", "locally_edited",
"synced", "pending_fields"?, "local_error"?, "remote_drifted"?, "field_drifted"?, "remote_error"? }, ... ] }`.
`locally_edited` is true when the `.wiki` differs from the pulled base or a configured field is
pending; `synced` is false for a `.wiki` with no sidecar entry (never-synced — it also reads
`locally_edited`). `remote_drifted` covers description or pending-field drift; `field_drifted`
identifies the latter. They and `remote_error` appear only with `--remote` and are
`omitempty`. `local_error` is independent of `--remote` and reports a broken
pending-to-mirror binding such as a missing or moved `.wiki`.

`atl jira snapshot [DIR | --into ROOT] [--remote]` emits the content-free aggregate contract
`{schema_version:1,service:"jira",remote_requested,complete,reconciled,local,
native,snapshot,pending,render,remote}`. It intentionally omits root/target,
issue identity, path, hashes, field identity, diagnostic text, and native/raw/
derived content. The offline default requires no config or credentials and
performs no pending-transaction recovery, network, or filesystem writes. Local
inspection shares the persistent mutation lock when it exists. Contention
returns a content-free exit `8` before inspection. If a legacy mirror has no
lock yet, the command verifies that no current writer created it during the
read and discards/retries the first result if one did.

Jira status/snapshot use the same mutually exclusive explicit forms and
pre-network initialized-root check as Confluence, with `mirror-jira` as the
final fallback. Root-selection errors produce no result object.

`local` partitions every `.wiki` as clean/edited and canonical
tracked/untracked, with non-canonical copies counted inside untracked. `native`
partitions present and tracked-but-removed substrates by unchanged, modified,
removed, untracked, non-canonical,
missing baseline, baseline mismatch, or unreadable baseline, and independently
reconciles baseline present/missing/unreadable plus valid/invalid. `snapshot`
reconciles expected sibling raw snapshots through present/missing,
readable/unreadable, valid/invalid, and key-matched/mismatched buckets.
`pending` partitions stable records into valid/invalid/unreadable and
bound/unbound, and reports only aggregate field-edit and active-transaction
counts. `render` reconciles expected views through present/missing/unreadable,
current/legacy/missing-marker/unsupported format, and recorded/missing view
state. `renderer_compatible` describes marker readability/compatibility only;
it does not claim the view is unedited or safe to overwrite.

With `--remote`, local preflight runs before backend setup. Any qualified local
integrity failure emits the aggregate, returns exit `8`, and performs no request.
One eligible canonical issue with a valid baseline keeps its exact GET. Larger
selections use qualified batches of at most 100 keys and 16 KiB of escaped
selector input, with one single-attempt request per batch and generic
replay-safe retries disabled. A batch is credited only after terminal
pagination, exact case-insensitive key coverage, unique canonical positive
numeric ids, and an explicit Description projection; typed, partial, omitted, duplicate,
unexpected, or malformed evidence makes the whole batch unavailable without
per-issue fallback. Redirect responses are not followed and count as
unavailable. `attempted = checked + unavailable`, `checked = in_sync + drifted`,
and local `present = attempted + not_attempted`; unavailable never means in-sync
and makes `complete:false`. No form of this command mutates the mirror or backend.
If the aggregate cannot be written to stdout, the write failure is reported
together with the inspection failure and the exit code stays the inspection
classification. If inspection otherwise succeeds, the write failure is
returned on its own with generic exit `1`.

`atl jira push <file.wiki|DIR> [--apply] [--force] [--into ROOT]` emits `{ "items": [ ... ] }`, one
item per file: `{ "path", "key", "pushed", "dry_run"?, "skipped"?, "remote_drifted"?,
"drift_overridden"?, "diff"?, "fields"?: [{"id","diff"?}], "field_drifted"?, "failed"?,
"warning"? }`. It is **dry-run by default**: without
`--apply`, `dry_run` is `true`, `pushed` is `false`, `diff` carries the unified diff of what the
write changes on the server (current remote → local body; equal to base → local when there is no
drift), and no write occurs. Field-only pending issues are included in directory pushes. Description
drift without `--force` exits `8`; `--force` sets `drift_overridden`. Pending-field drift sets both
`remote_drifted` and `field_drifted` and always exits `8`, even with `--force`. When Description and
fields changed they are sent in one typed update. `--apply` sets `pushed:true`; a post-push
transport/local mirror-refresh failure surfaces as a `warning`, not an error.
A successful verification read that no longer matches the reviewed end state
retains pending, sets drift/failed details, and exits `8` even though
`pushed:true` records that the write request was sent. `skipped:"unchanged"`
marks a clean file.

`atl jira apply <FILE.md> [--dry-run] [--allow-loss] [--rebase-pending] [--into ROOT] [--render-*]` emits the same
shape as `conf apply` for Description, plus pending-field details:
`{ "path", "wiki_path", "pending_path"?, "dry_run", "rebased"?, "report": {...},
"fields"?: [{"id","pending","report"}], "wrote", "warning"? }`. It is **local only** (no network). Each
accepted view begins with `<!-- atl:document jira-issue v3 -->`; a v2, v1,
missing, or unversioned marker exits `8` before any write and requires an offline
`jira render` or fresh pull before editing. V1 identifies the former generated
bullet form of Subtasks/Epic Children; v2 predates the recorded display-timezone
contract. Neither legacy form is reconstructed as current during apply. A
future/unknown version requires a
newer binary and must not be rendered or downgraded by the current one. A
directory render preflights every existing view before rewriting any sibling,
so one future marker cannot produce a half-migrated batch. It repeats each
target check under the mirror mutation lock immediately before writing; `pull`
uses the same locked check before changing that issue's artifacts. A CRLF on
the marker line is recognized without normalizing the rest of the file.
Unreadable or malformed `.json` snapshots remain advisory skips, but each is
named in a stderr warning instead of disappearing silently. Since render
rewrites the derived `.md`, callers preserve any existing edits externally and
reapply them after migration.
`removed_constructs` entry is `{ "kind", "text" }` (`kind` ∈ `panel`, `color`, `mention`, `image`,
`monospace`, `link`, `macro`, …). The merge is fail-closed and exits `8` (`ErrCheckFailed`, nothing
written) on: an unconvertible edited block; a block set that exceeds the fixed alignment allocation
budget (edit the native `.wiki` directly); a wiki-only construct dropped without `--allow-loss`
(the report still carries `removed_constructs` so the caller can see what would go); an edit to any
section other than generated `# Description` or an explicitly editable rich-text field (the error
names the section and its dedicated command); or a
local `.wiki` matches neither the last-synced base nor exact ATL-produced
staged/pending lineage. Consecutive local applies retain the remote baseline;
id/path/native/base-hash mismatches fail closed. Exit `4` (`ErrNotFound`) when the issue was never
pulled (no base/snapshot). Editable field values are stored under `.atl/pending/jira/` and do not
mutate `<KEY>.json`; `pull`/`render` overlay them in the derived view. On a successful write
`wrote:true`; a failed `.md`-view refresh sets `warning` and is not an error.
`--rebase-pending` is the explicit conflict step after fresh pull/review: raw
snapshot values become the new bases while visible local proposals remain.
Pending commits bind the exact sidecar path and reviewed wiki hash; a hidden
transaction record makes combined Description+field apply crash-recoverable.
Jira mirror mutations use one persistent mirror-internal advisory lock inode;
dry-runs may initialize that coordination file but never change Jira or commit
wiki/pending/view content.

Both `conf apply` and `jira apply` also carry a `-o text` projection — a compact loss-review
(first line dry-run/applied, `blocks:` counts, `removed fragments:`/`removed constructs:` and
`problems:` sections, `validation:` for conf, an optional `warning:`, and a contextual `next:`
hint). The JSON above is unchanged; the text view is a read-only reprojection of the same result.

## Jira exports

## Exports, tables, and reports

`atl jira export --jql ... --out FILE --format jsonl|json|csv` writes one compact artifact and a
sidecar manifest at `FILE.manifest.json`. `--ids` and `--keys` can be used instead of `--jql` to
generate batched `id in (...)` / `key in (...)` queries. Explicit selectors are
de-duplicated by first occurrence and found issues are emitted in that order
across pages and batches. Missing/inaccessible identities are omitted without
disturbing the relative order of found rows. User JQL retains backend order.
Stdout remains the normal `emit()` JSON summary:

```json
{
  "path": "issues.jsonl",
  "manifest_path": "issues.jsonl.manifest.json",
  "format": "jsonl",
  "count": 1
}
```

JSONL emits one `JiraIssueSnapshot` object per line (`{key,id,fields}`); JSON emits
`{manifest,issues}`; CSV emits `key,id` followed by the deterministic field list.
JSONL/CSV are streamed atomically; aggregate JSON is limited to 10,000 issues
and 64 MiB of serialized issue data. The row-stream identity index is capped at
250,000 unique issues so exact deduplication remains memory-bounded.
CSV formula-leading cells are apostrophe-prefixed by default. `--raw-csv`
disables that protection and records `csv_raw: true` in the manifest. The manifest
stores query mode, generated queries when applicable, fields, format, count, CLI version, and a
backend URL hash only:

```json
{
  "command": "atl jira export",
  "format": "jsonl",
  "query_mode": "jql",
  "row_order": "backend",
  "jql": "project=PROJ",
  "count": 1,
  "backend": {
    "service": "jira",
    "url_hash": "sha256:..."
  }
}
```

For `query_mode: keys|ids`, the manifest instead carries `row_order:
"selector"` and `missing_identity_behavior: "omit"`. Ordering is identical in
JSONL, aggregate JSON, and CSV, for files and artifact-only stdout. Explicit
selection buffering is bounded to one generated batch and 64 MiB of encoded
issue data; the global 250,000 identity safety cap remains in force.

The export receipt and manifest prove local producer completion and describe
emitted rows; they do not contain backend-qualified `complete`, `truncated`, or
`partial_reason` fields. Reconcile explicit requested keys/ids against returned
identities, treating omissions as missing-or-inaccessible. A JQL export, even
with `limit: 0`, cannot by itself qualify exhaustive selection or absence.

The backend hostname and PAT are never written to the manifest.

## Jira export comparison, reports, and field catalogs

`atl jira issue create` and its read-only `create preview` child emit the
schema-v1 guarded-create result documented under
[registration and write guards](registration-and-write-guards.md#guarded-jira-issue-create).
JSON is the complete safety result. The parent is preview-by-default; apply
requires the exact proposal hash. `-o id` is apply-only and emits a key only for
terminal `applied`; text is unsupported.

`atl jira issue create-check` emits
`{schema_version,project,issue_type,count,complete,fields}`. Each field contains
only `{field_id,name,required,has_allowed_values}`. Jira's endpoint already
limits the result to create-screen fields, so there is no redundant `on_screen`
member; allowed-value labels and values are also omitted. This legacy JSON and
text shape is unchanged.

`atl jira issue create-metadata` emits schema version 1 with the same project
and exact resolved issue type, plus `qualification`, `bounds`, and a sorted
field inventory. A field has this content-free shape:

```json
{
  "field_id": "summary",
  "name": "Summary",
  "required": true,
  "schema": {"type": "string", "system": "summary"},
  "default_state": "absent",
  "allowed_values": {
    "mode": "not_advertised",
    "inline_count": 0,
    "exhaustive": false
  },
  "omittability": "must_supply",
  "omittability_basis": "required_without_default"
}
```

`required` and `schema` are `null` when Jira omitted them. `default_state` is
`present|absent|unknown`; it never contains the default. Allowed-value `mode`
is `inline|autocomplete|inline_and_autocomplete|not_advertised`, and
`exhaustive:true` means Jira supplied an inline list without also advertising
autocomplete. Labels, option values, and autocomplete URLs are never emitted.
`omittability` is `omittable|must_supply|unknown`, paired respectively with a
closed `omittability_basis` of `not_required|backend_default`,
`required_without_default`, or `metadata_unqualified`.

`complete:true` qualifies the bounded field inventory, while
`qualification.{schema_complete,default_complete,omittability_complete}` says
whether every field supplied enough facts for that projection. `bounds`
declares the 1000-type, 1000-field, 64-request, 16-MiB, and 60-second limits and
reports requests/response bytes used. The command fails instead of returning a
partial inventory or backend-controlled error text.

`atl jira export diff OLD NEW` reads JSONL/JSON/CSV compact exports and reports issue identifiers:

```json
{
  "old_count": 1,
  "new_count": 2,
  "added": ["PROJ-2"],
  "changed": ["PROJ-1"]
}
```

`atl jira planning report --jql ...` returns deterministic per-issue quality rows:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "score": 4,
      "max_score": 5,
      "level": "warn",
      "gaps": ["missing_artifact_ref"],
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ],
  "summary": {
    "good": 0,
    "warn": 1,
    "poor": 0
  }
}
```

When `--csv FILE` is passed, the same command writes a deterministic CSV sidecar
and includes `csv_path` in the JSON result. Formula-leading cells are
apostrophe-prefixed by default; `--raw-csv` requires `--csv` and disables that
protection for trusted non-spreadsheet consumers.

This legacy shape has no separate selection-completeness member. A positive
`--limit` is a bounded sample, so consumers must not use it for whole-scope
absence claims. `--limit 0` removes the caller cap and asks the collector to
exhaust pagination, but does not add source qualification or prove backend
completeness. The CSV sidecar changes storage, not selection qualification.

`atl jira fields` and typed MCP `jira_fields` share one value-free catalog
contract:

```json
{
  "schema_version": 1,
  "projection": "full",
  "source": "jira-field-catalog",
  "complete": true,
  "total": 2,
  "count": 1,
  "custom_count": 1,
  "system_count": 0,
  "fields": [
    {
      "id": "customfield_10001",
      "name": "Delivery Notes",
      "custom": true,
      "schema": "string"
    }
  ]
}
```

`total` describes the source snapshot before client-side filters; `count`
describes the filtered match set. `custom_count` and `system_count` partition
that same set and always sum to `count`. The default `projection:"full"` emits
the matching value-free definitions. CLI `--summary-only` and MCP
`summary_only:true` select `projection:"summary"` and return `fields:[]`,
preserving qualification, filters, and reconciled counts in a compact result.
Filtering and projection never upgrade or downgrade source completeness.
Jira's `/rest/api/2/field` response is atomic and non-paginated, so a
successfully decoded non-empty response is `complete:true`. An empty or
legacy/unqualified source is `complete:false` with `partial_reason`; malformed
ids, duplicates, and contradictory qualification fail with exit 8. Field
values are never part of this contract. The text projection begins with
the backward-compatible `complete`, `source`, `count`, and `total` line. The
summary text projection adds `projection=summary`, `custom`, and `system` on a
second line and no field records; the full projection keeps the existing
tab-separated field records.

Typed MCP `jira_issue_graph` returns the same full-v2 default or compact-v1
projection through a Jira-only read. Its schema requires one canonical issue
`key` and accepts optional `depth` from 0 through 2, `max_nodes`, `max_edges`,
`max_requests`, `include_development`, `projection`, `select`, and `max_bytes`.
It deliberately accepts neither
`resolve`/`resolve_confluence` nor `strict`: Confluence identities remain
qualified stubs, and callers inspect `complete`, sources, reconciliation, and
the frontier in the successful result.

The backend and result byte bounds are independent. MCP fixes evidence at 500
records and the aggregate Jira response budget at 16777216 bytes; reported
`bounds.max_response_bytes` and `response_bytes_used` expose that backend
budget, but `max_response_bytes` is not a v1 input. The separate `max_bytes`
input caps the final encoded MCP result (default 256 KiB, minimum 1 KiB,
maximum 1 MiB). `max_nodes` defaults to 50 and caps at 100, `max_edges`
defaults to 200 and caps at 500, and `max_requests` defaults to 50 and caps at
100. Exhausting an
application traversal bound can therefore return a valid full or compact result
with `complete:false` and static qualification. Exceeding the final `max_bytes`
instead returns an MCP output-limit error with no clipped result. Neither case
proves that an omitted relationship is absent. When `include_development` is
omitted or false, the MCP request and full output retain the stable profile and
no Development source is present; that absence must never be reported as zero
development activity.

## Jira graphs and references

`atl jira issue graph <KEY>` emits one transient deterministic work-artifact
graph. The omitted or explicit full projection is the existing schema-v2 byte
contract; compact is a qualified schema-v1 fact projection derived after full
bounded collection. Depth defaults to zero.

The CLI `--include-development` option and typed MCP
`include_development:true` input add
`bounds.include_development:true`, one `development` source per expanded Jira
node, and four closed GitLab node/edge kinds: project, commit, branch, and
merge request. Each GitLab node has an `scm` object containing `host` and
`project_path`, plus exactly one applicable artifact selector (`commit_sha`,
`branch_name`, or `merge_request_iid` with `merge_request_state`); project nodes
have no artifact selector. All such sources, nodes, edges, and evidence are
`experimental_api`; nodes are unexpanded stubs and are never traversed.
Development source `count` excludes project containers. Any failure or
reconciliation mismatch is fail-closed for that source: stable graph facts
remain, but no partial Development projection survives. Omitting the option or
supplying MCP false preserves the stable request sequence and full schema-v2
output bytes shown below. Explicit `--projection full` preserves those bytes as
well.

Compact projection uses `--projection compact` in the CLI or
`projection:"compact"` in `jira_issue_graph`. CLI `--select` is repeatable and
comma-separated; MCP `select` is an array. The closed selectors are `urls`,
`scm`, and qualification-only `none`. Omitted selection means `urls`, plus
`scm` only when Development collection is enabled. Explicit `scm` requires
that opt-in, and `none` cannot be combined with a fact selector. Full rejects a
selector. Compact is JSON-only in the CLI. These combinations are validated
before configuration or network access. Selected and omitted classes are
deduplicated and emitted in fixed `urls`, then `scm` order.

The compact result has `schema_version:1`, a normalized
`projection:{name,selected,omitted}`, the full graph's `root_id`, `complete`,
`truncated`, complete `bounds`, a compact reconciliation `summary`, selected
`facts`, retained `sources`, and the full bounded `frontier` and `warnings`.
Projection occurs only after schema-v2 collection and validation. It never
clips the graph, changes a request, or makes a lowered safety bound an output
selector.

Each fact has `class`, `node_id`, `kind`, `depth`, `state`, `stability`, and
sorted `source_node_ids`. A URL fact is copied only from one canonical
`kind:"url"` node and carries its already normalized `url` when safe. An opaque URL node is
still represented, but the blank URL identity is omitted; consumers must never
rebuild it from a node id, label, evidence pointer, or source content. An SCM
fact carries only the graph node's validated `scm` coordinates and never its
Development web URL. Compact evidence contains no pointer or source snippet.
Facts are ordered by class (`urls`, then `scm`), kind, and node id; each
`source_node_ids` array is sorted.

Every incomplete full-graph source remains in `sources`; when SCM is selected,
every requested Development source remains there with its status and count,
including complete-empty and incomplete sources that produced no fact. The
retained sources keep full-graph order.

```json
{
  "schema_version": 1,
  "projection": {
    "name": "compact",
    "selected": ["urls"],
    "omitted": ["scm"]
  },
  "root_id": "jira:issue:PROJ-1",
  "complete": true,
  "bounds": {
    "requested_depth": 0,
    "max_nodes": 100,
    "max_edges": 500,
    "max_evidence": 500,
    "max_source_bytes": 1048576,
    "expanded_node_count": 1,
    "followed_node_count": 0,
    "attempted_node_count": 1,
    "max_requests": 100,
    "requests_used": 4,
    "max_response_bytes": 16777216,
    "response_bytes_used": 4096,
    "max_sources": 801,
    "max_frontier": 100
  },
  "summary": {
    "collected": {
      "node_count": 2,
      "edge_count": 1,
      "evidence_count": 1,
      "source_count": 8,
      "incomplete_source_count": 0,
      "source_status_counts": {
        "complete": 2,
        "empty": 6,
        "forbidden": 0,
        "partial": 0,
        "skipped": 0,
        "unsupported": 0
      }
    },
    "projected": {
      "fact_count": 1,
      "source_count": 0,
      "url_count": 1,
      "scm_count": 0,
      "incomplete_source_count": 0,
      "source_status_counts": {
        "complete": 0,
        "empty": 0,
        "forbidden": 0,
        "partial": 0,
        "skipped": 0,
        "unsupported": 0
      }
    },
    "collected_counts_match_full": true,
    "projected_fact_count_matches_facts": true,
    "fact_class_counts_match_facts": true,
    "projected_source_count_matches_sources": true,
    "source_status_counts_match_sources": true,
    "incomplete_source_count_matches_sources": true
  },
  "facts": [
    {
      "class": "urls",
      "node_id": "url:<sha256>",
      "kind": "url",
      "url": "https://docs.example.com/spec",
      "state": "stub",
      "depth": 1,
      "stability": "heuristic",
      "source_node_ids": ["jira:issue:PROJ-1"]
    }
  ],
  "sources": []
}
```

`truncated`, `frontier`, and `warnings` are omitted when false or empty. When
present, their values are copied from the full graph. `summary.collected`
retains its validated full node/edge/evidence/source accounting, while
`summary.projected` counts only emitted facts and retained sources. The six
booleans reconcile those two scopes without asking a consumer to recount them.

The MCP full projection omits every GitLab node URL and exposes only the closed
SCM coordinates plus ordinary graph topology and experimental provenance. It does
not add narrative, people, email, avatars, files, diffs, timestamps, query
values, labels, or raw payloads. ATL itself never contacts GitLab or reuses Jira
credentials. A downstream GitLab read is a separate operation: require exact
equality between the returned lowercase host and an owner-approved host, then
use a separately authenticated read-only client for that exact host.

MCP applies its existing fail-closed full-graph validation/sanitization gate
before invoking the shared compact projector, which independently excludes
Development-node URLs. Its output schema is the closed union of full v2 and
compact v1. The existing `max_bytes` check measures the final encoded selected
result and fails the whole tool call on overflow; neither branch is clipped.

```json
{
  "schema_version": 2,
  "root_id": "jira:issue:PROJ-1",
  "complete": true,
  "bounds": {
    "requested_depth": 0,
    "max_nodes": 100,
    "max_edges": 500,
    "max_evidence": 500,
    "max_source_bytes": 1048576,
    "expanded_node_count": 1,
    "followed_node_count": 0,
    "attempted_node_count": 1,
    "max_requests": 100,
    "requests_used": 4,
    "max_response_bytes": 16777216,
    "response_bytes_used": 4096,
    "max_sources": 801,
    "max_frontier": 100
  },
  "summary": {
    "node_count": 2,
    "edge_count": 1,
    "evidence_count": 1,
    "source_count": 8,
    "incomplete_source_count": 0,
    "source_status_counts": {
      "complete": 2,
      "empty": 6,
      "forbidden": 0,
      "partial": 0,
      "skipped": 0,
      "unsupported": 0
    },
    "node_count_matches_nodes": true,
    "edge_count_matches_edges": true,
    "evidence_count_matches_edges": true,
    "source_count_matches_sources": true,
    "source_status_count_matches_sources": true,
    "incomplete_source_count_matches_sources": true,
    "expanded_count_matches_nodes": true,
    "complete_matches_sources": true
  },
  "nodes": [
    {
      "id": "jira:issue:PROJ-1",
      "kind": "jira_issue",
      "service": "jira",
      "external_id": "PROJ-1",
      "label": "Graph seed",
      "state": "resolved",
      "expanded": true,
      "depth": 0,
      "stability": "public_api"
    },
    {
      "id": "jira:issue:PROJ-2",
      "kind": "jira_issue",
      "service": "jira",
      "external_id": "PROJ-2",
      "state": "stub",
      "expanded": false,
      "depth": 1,
      "stability": "public_api"
    }
  ],
  "edges": [
    {
      "id": "edge:<sha256>",
      "from": "jira:issue:PROJ-1",
      "to": "jira:issue:PROJ-2",
      "kind": "jira_link",
      "relation_type": "Blocks",
      "relation": "blocks",
      "direction": "outward",
      "current": true,
      "confidence": "exact",
      "stability": "public_api",
      "evidence": [
        {
          "collector": "issue_links",
          "source_node_id": "jira:issue:PROJ-1",
          "source_kind": "field",
          "source_id": "7",
          "json_pointer": "/fields/issuelinks/0",
          "extraction": "structured"
        }
      ]
    }
  ],
  "sources": [
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_fields",
      "requested": true,
      "status": "complete",
      "complete": true,
      "count": 4,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_links",
      "requested": true,
      "status": "complete",
      "complete": true,
      "count": 1,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "hierarchy",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "attachments",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "issue_properties",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "experimental_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "comments",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "worklogs",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    },
    {
      "node_id": "jira:issue:PROJ-1",
      "node_depth": 0,
      "kind": "remote_links",
      "requested": true,
      "status": "empty",
      "complete": true,
      "count": 0,
      "stability": "public_api"
    }
  ]
}
```

The canonical node kinds in schema v2 are `jira_issue`, `confluence_page`,
`attachment`, and `url`. At depth zero, as shown above, the seed is the only
`expanded:true` node. All discovered targets have depth 1 and are not requested.
Candidate Jira keys found only in narrative use the canonical Jira node id with
`state:"unresolved"` until a structured fact supplies exact identity. Edge
kinds are `jira_link`, `parent_of`, `child_of`, `epic_of`, `attached`,
`remote_link`, and `mentions`; a typed relation and a mention to the same node
remain distinct edges. Every edge has at least one content-minimized evidence
record, and duplicate semantic edges merge sorted evidence.

The fixed source order is `issue_fields`, `issue_links`, `hierarchy`,
`attachments`, `issue_properties`, `comments`, `worklogs`, and `remote_links`.
Their closed statuses are `complete`, `empty`, `partial`, `forbidden`,
`unsupported`, and `skipped`.
Only `complete` and `empty` have `complete:true`. Optional
`partial_reason` is one of `inspection_limit`, `output_limit`,
`request_failed`, `malformed_response`, `request_limit`, `byte_limit`,
`dependency_unavailable`, or `policy`; it never contains a backend error.
Malformed or request-limited sources are `partial`; a source that cannot be
started by policy is `skipped`. Stability is fixed per source kind:
`issue_properties` is `experimental_api`; every other current kind is
`public_api`. `issue_properties` remains ordered: its count is the number of
returned properties inspected, and completeness means the returned property
set was processed under the fixed privacy exclusions and bounds, not that every
property produced graph evidence.
Top-level `complete` is derived from all requested sources. Auxiliary source
failure returns a reconciled graph with exit 0 and `complete:false`; seed,
schema, or reconciliation failure returns the corresponding non-zero sentinel.

The one root snapshot requests `fields=*all`, `properties=*all`, and
`expand=names,schema` together and is single-attempt. Comments and worklogs use
their complete paginated readers; remote links use Jira's supported direct
endpoint. Returned fields are reconciled against names/schema before recursive
inspection. A recursively eligible field with missing, blank, unknown, or
structurally invalid type/item metadata is skipped and qualifies `issue_fields`
as `partial` / `malformed_response`; structured and privacy-excluded fields do
not require walker metadata. A custom narrative field without necessary name
metadata disables bare Jira-key inference and receives the same qualification.
An unknown noncanonical field id is also partial and cannot enable bare
inference, though a valid non-identity schema may still permit URL-only
inspection. Jira's literal top-level `type:any` is accepted only for a canonical
custom field; it remains path-filtered and URL-only and never enables bare-key
inference. Nulls, scalar numbers/booleans, and empty strings or containers cannot
contain graph references and therefore require no walker metadata. Extra
metadata for fields that were not returned is ignored.
The walker accounts for container, key, scalar, pointer, depth, item,
and source-byte limits, excludes user/avatar/icon/transport/download subtrees,
and never dereferences discovered URLs. HTTP(S) URLs reject userinfo, remove
fragments and default ports, and never emit query values. Sensitive or
credential-like path segments make the URL an opaque identity without a raw
URL. Dynamic property and nested-object tokens in evidence pointers are
deterministic opaque tokens rather than source content. Text output
contains the same qualification plus escaped source/node/edge tables. For URL
nodes, the node table's `URL` cell is populated only from the canonical graph
node's public `url`; it remains blank for non-URL or opaque/sensitive identities
and is never rebuilt from evidence. `-o id` is rejected before configuration or
network access.

Every full graph projection uses schema v2. Omitting traversal and resolution
flags, explicit `--depth 0`, explicit `--resolve none`, or both explicit values
keeps the same direct depth-zero contract. `--depth 1..3` adds structured Jira
traversal and `--resolve confluence` adds the narrow metadata phase. Schema v2
uses the same top-level arrays and reconciliation summary at every depth, with
these transport and provenance fields:

- `bounds.attempted_node_count` counts Jira snapshot calls that were actually
  attempted; `followed_node_count` is the non-root subset and
  `expanded_node_count` counts successfully expanded Jira nodes.
- `bounds.max_requests` / `requests_used` count physical HTTP attempts across
  Jira and optional Confluence reads. `bounds.max_response_bytes` /
  `response_bytes_used` count aggregate buffered successful and error response
  bytes. Reads are single-attempt: no retry or followed redirect can bypass the
  shared bounds.
- `bounds.max_sources`, `max_frontier`, `frontier_count`, and optional
  `frontier_truncated` qualify the remaining inventories. The optional
  top-level `frontier` is sorted by depth, node id, and reason and contains only
  `{node_id, depth, reason}`.
- Every source has `node_depth` and remains keyed by `(node_id, kind)`. Every
  edge evidence record has `source_node_id`, which identifies the expanded Jira
  node whose collector observed that fact.

Traversal is deterministic breadth-first order across the entire current
depth. Only canonical `jira_issue` nodes in `state:"stub"` that came from exact
structured issue-link or hierarchy evidence are eligible. Narrative
`mentions`, URLs, attachments, and Confluence pages are never traversal inputs.
Cycles and diamonds are read once. A Jira response whose canonical key differs
from the requested moved key is reconciled into one node and one semantic edge
inventory before the summary is computed.

The schema-v2 defaults are 100 nodes, 500 edges, 500 evidence records, 100
physical requests, and 16777216 buffered response bytes. Hard maxima are 2048,
4096, 4096, 128, and 67108864 respectively; depth is capped at 3. Admission of
a new node, edge, and its evidence is atomic. Work refused by an output,
physical-request, or response-byte bound is statically classified as
`output_limit`, `request_limit`, or `byte_limit`; dynamic backend details and
live counters never enter a reason string. When the seed response itself
exceeds the response-byte bound, schema v2 still emits one
`state:"unresolved"` root with no edges, zero expanded nodes, a root frontier
item, and all requested sources qualified by the same budget reason. Optional
Confluence resolution, when requested, is represented by an equally qualified
`confluence_metadata` source rather than being silently omitted.

`--resolve confluence` adds one aggregate `confluence_metadata` source after
Jira traversal. It considers only already discovered canonical numeric page
ids and performs at most one same-origin, single-attempt, id/title-only GET for
each candidate. It does not request page body, ancestors, labels, restrictions,
principals, assets, or arbitrary URLs. Unavailable optional configuration is
`status:"skipped"` with `partial_reason:"dependency_unavailable"`. Missing
pages remain `state:"missing"` while a fully attempted inventory can still be
complete; forbidden or malformed responses remain explicitly incomplete.

Top-level `complete` continues to be derived from every requested source.
`--strict` does not alter either JSON projection: it emits the reconciled result
first, then returns `ErrCheckFailed` (exit 8) when `complete:false`. Only full
schema v2 supports text; it adds transport usage, per-node source columns, and
a frontier table when one exists.

### Jira inverse-reference search

`atl jira issue reference search` is a CLI-only, content-free search from one
exact GitLab project or Confluence page into a caller-qualified Jira scope.
There is no typed MCP result for this command. Its schema-v1 JSON shape is:

```json
{
  "schema_version": 1,
  "target": {
    "kind": "gitlab_project",
    "opaque_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  },
  "mode": "exhaustive",
  "sources": ["development"],
  "effective_field_ids": [],
  "target_resolution": {"complete": true},
  "selection": {"complete": true},
  "verification": {"complete": true},
  "counts": {
    "selected_issues": 1,
    "candidate_issues": 1,
    "scanned_issues": 2,
    "verified_issues": 1,
    "matched_issues": 1,
    "matches": 1
  },
  "source_counts": [
    {
      "source": "development",
      "complete": 1,
      "empty": 0,
      "partial": 0,
      "forbidden": 0,
      "unsupported": 0,
      "skipped": 0,
      "total": 1,
      "reconciled": true,
      "reasons": []
    }
  ],
  "matches": [
    {
      "issue_key": "DEMO-41",
      "relation": "development_association",
      "direction": "issue_to_target",
      "source": "development",
      "stability": "experimental_api",
      "confidence": "exact",
      "complete": true
    }
  ],
  "frontier": {"phase": "complete", "verified_issues": 1},
  "reconciliation": {
    "counts": true,
    "sources": true,
    "matches": true,
    "usage": true
  },
  "usage": {
    "max_issues": 10,
    "max_requests": 10,
    "requests": 4,
    "max_response_bytes": 65536,
    "response_bytes": 1024,
    "reconciled": true
  },
  "complete": true,
  "absence_proven": false
}
```

Wire names use underscores (`gitlab_project`, `confluence_page`, and
`remote_links`) even though the corresponding CLI values use hyphens. Sources
and `effective_field_ids` are normalized into deterministic order. The target
is only a one-way opaque id. The result never contains the original target,
scope JQL, Jira numeric ids, URLs, titles, source values, property keys,
application or user identities, or backend error prose.

The three phase objects qualify target resolution, selection, and verification
independently. A phase has `complete` and, when incomplete, one closed static
`reason`. `source_counts` reconciles every selected source across the selected
issues into `complete`, `empty`, `partial`, `forbidden`, `unsupported`, and
`skipped` buckets. Its reason counts use only `request_failed`,
`request_limit`, `byte_limit`, `malformed_response`, `field_missing`,
`not_permitted`, `not_supported`, and `mode_fast`. The fixed source order is the
order in top-level `sources`.

Matches are deduplicated by issue, source, relation, and optional technical
field id. Literal values use `literal_mention` / `heuristic` / `high`;
structured remote links use `structured_remote_link` / `public_api` / `exact`
(or a qualified high-confidence fallback); Jira Development uses
`development_association` / `experimental_api` / `exact`. Direction is always
`issue_to_target`. Match `complete` is derived from its named source rather
than asserting global completeness.

`frontier` identifies the bounded phase that completed or stopped.
`reconciliation` proves that emitted counts, sources, matches, and transport
usage agree; `usage.reconciled` is true only when all four reconciliation
classes hold. Top-level `complete:true` requires exhaustive mode, complete target
resolution, both complete selection passes with a stable identity set,
complete verification of every requested source, and every reconciliation.
Only `complete:true` with no matches sets `absence_proven:true`. Fast mode is
always `selection.complete:false`, so it never proves absence. Its otherwise
normally terminal narrowed pass uses `reason:"mode_fast"`; any concrete
selection failure retains its own closed reason instead.

Without `--strict`, a usable incomplete JSON result may exit zero. With
`--strict`, the same result is emitted before exit 8 and must be retained.
Text output is only an escaped match table (`KEY`, `RELATION`, `SOURCE`,
`CONFIDENCE`, `COMPLETE`) and cannot support an absence claim; id output is
unsupported.

The search never contacts GitLab or dereferences a URL discovered in Jira.
Only resolution of a caller-supplied Confluence display or short target may
contact the configured Confluence origin, under the same single-attempt request
and response-byte budget. Confluence values found in Jira match only direct,
same-origin, id-bearing links; the command reads neither page bodies nor
Confluence backlinks.

This contract change does not change `jira issue refs`; its exact JSON/text
compatibility goldens remain independent.

`atl jira issue refs <KEY>` and `atl jira issue refs --jql ...` return
deterministic, provenance-qualified artifact references per issue:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "complete": true,
  "selection": {
    "mode": "jql",
    "count": 1,
    "limit": 100,
    "complete": true
  },
  "summary": {
    "issue_count": 1,
    "complete_issue_count": 1,
    "incomplete_issue_count": 0,
    "reference_count": 1,
    "reference_kind_counts": {"doc": 1},
    "source_count": 2,
    "source_value_counts": {"comments": 2, "description": 1},
    "complete_source_count": 2,
    "incomplete_source_count": 0,
    "truncated_source_count": 0,
    "count_matches_issues": true,
    "selection_count_matches_issues": true,
    "reference_count_matches_kinds": true,
    "issue_summaries_reconciled": true,
    "complete_matches_inputs": true,
    "truncated_matches_inputs": true
  },
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "complete": true,
      "sources": {
        "comments": {"complete": true, "count": 2},
        "description": {"complete": true, "count": 1}
      },
      "reference_summary": {
        "reference_count": 1,
        "reference_kind_counts": {"doc": 1},
        "source_count": 2,
        "source_value_counts": {"comments": 2, "description": 1},
        "complete_source_count": 2,
        "incomplete_source_count": 0,
        "truncated_source_count": 0,
        "reference_count_matches_kinds": true,
        "complete_matches_sources": true,
        "truncated_matches_sources": true
      },
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ]
}
```

The top-level `complete` combines JQL/key selection completeness with every
issue's contributing sources. `selection.truncated:true` means `--limit`
stopped a JQL result while Jira advertised more rows. Each issue qualifies
`description`, `comments`, and every requested `field.<id>` with `complete`,
input-value `count`, optional `text_truncated`, and a bounded warning. Comments
come from the complete paginated comment endpoint; a recoverable comment-source
failure may retain embedded comments but marks that source and the issue
incomplete.

Each issue's additive `reference_summary` is derived from its final emitted
`sources` and deduplicated `refs`. `reference_count` therefore counts a URL once
per issue even if several narrative sources contained it, and always equals the
sum of `reference_kind_counts` when `reference_count_matches_kinds:true`.
`source_value_counts` preserves the existing source names and sums their input
value counts. The top-level `summary` combines those issue summaries, reports
complete/incomplete/truncated source and issue cardinalities, and exposes exact
reconciliation with top-level `count`, `selection`, `complete`, and `truncated`.
References repeated by different issues are counted once for each issue; atl
does not assert that cross-issue URLs represent one evidence use. Consumers
should use these deterministic aggregates instead of recounting nested arrays.

`--fields` selectors are resolved once through the shared Jira field catalog:
technical ids remain direct, while exact case-insensitive display names map to
technical ids before selection and extraction. Field source keys always contain
the resolved technical id. A JQL selection performs one complete paginated
comment listing per issue; callers should use a narrow query and explicit limit
when budgeting backend requests.
All narrative values use the same 128 KiB per-value evidence cap as `epic digest`.
Missing requested fields and clipped values remain incomplete. `-o text` starts
with completeness/selection status, then emits the shared escaped Markdown
table and bounded warnings. An empty `refs` array is evidence of absence only
when both result and issue completeness are true.

## Attachments, guarded mutations, and worklogs

`atl jira issue images <KEY>` returns `{ "key", "images": [ ... ] }` with
the actual written paths. Each basename includes the stable attachment ID:
`<id>-<safe-inventory-filename>` (at most 255 UTF-8 bytes). Different attachments
with equal names retain different paths. Invalid or duplicate selected image
identities fail with exit `8` before downloads or local writes. Existing
targets also fail with exit `8`; use a fresh output directory for another
download. Exclusive publication preserves files that appear during a download,
too. Existing unprefixed files are not removed or overwritten.

`atl jira issue attachment list <KEY>` returns the issue key plus the attachment
metadata Jira exposes. `-o id` prints attachment ids one per line:

```json
{
  "attachments": [
    {
      "id": "42",
      "title": "spec.xlsx",
      "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "fileSize": 12345,
      "version": 0
    }
  ],
  "key": "PROJ-1"
}
```

`atl jira issue attachment get <KEY> --id <ID-or-filename>` downloads one
attachment and returns the written local path. `id` echoes the selector the
caller passed; `name` is the filename Jira reported for the matched attachment:

```json
{
  "id": "42",
  "key": "PROJ-1",
  "name": "spec.xlsx",
  "path": "attachments/spec.xlsx"
}
```

`atl jira issue attachment upload <KEY> --file <PATH>` uploads one local file
and returns the uploaded attachment metadata:

```json
{
  "attachment": {
    "id": "44",
    "title": "spec.xlsx",
    "mediaType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "fileSize": 12345,
    "version": 0
  },
  "key": "PROJ-1"
}
```

For Confluence attachment `upload`, a caller-supplied negative size or a
multipart body that would overflow its length exits `2`. For both Confluence
and Jira, a successful backend response that is malformed JSON or carries no
attachment exits `8` — distinct from a transport failure (exit `1`).

`atl jira issue tree --jql ... --epic-field ...` returns a normalized
epic-to-child tree:

```json
{
  "jql": "project=PROJ",
  "epic_field": "customfield_10001",
  "count": 3,
  "epics": [
    {
      "key": "PROJ-1",
      "summary": "Parent",
      "type": "Epic",
      "children": [
        {
          "key": "PROJ-2",
          "summary": "Child",
          "type": "Story",
          "epic": "PROJ-1"
        }
      ]
    }
  ]
}
```

`external_epics` contains children whose epic key is not part of the selected
JQL result. `orphans` contains selected non-epic issues with no epic field. Both
fields are omitted when empty.

`atl jira issue link suggest --csv links.csv` is read-only and returns missing
link candidates from a reviewed CSV plan:

```json
{
  "path": "links.csv",
  "planned_count": 2,
  "count": 1,
  "candidates": [
    {
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "dependency found during review",
      "row": 2
    }
  ]
}
```

Rows whose outward link already exists on the source issue are omitted from
`candidates`. The command performs no Jira writes.

## Guarded Jira CSV plans

`jira issue plan preview` and `jira issue plan apply` emit schema-v2 results.
Top-level members have the fixed order shown here; optional backend,
authorization, and proposal fields appear only after their evidence exists:

```json
{
  "schema_version": 2,
  "operation": "jira_issue_plan",
  "mode": "preview",
  "status": "would_apply",
  "complete": true,
  "row_count": 1,
  "backend_sha256": "sha256:<digest>",
  "document": {"normalized_sha256": "<digest>"},
  "family_counts": {"link": 0, "label": 1, "comment": 0, "field": 0},
  "status_counts": {"would_apply": 1, "already_satisfied": 0, "applied": 0, "recovered": 0, "blocked": 0, "not_applied": 0, "skipped": 0, "outcome_unknown": 0},
  "bounds": {
    "max_document_bytes": 16777216,
    "max_rows": 100,
    "max_field_cell_bytes": 8388608,
    "formulas": {
      "preview_requests": "3L+1A+102C+2F",
      "apply_requests": "9L+4A+306C+6F",
      "preview_response_bytes": "16777216*(L+A+C)+83886080*F",
      "apply_response_bytes": "16777216*(L+A+C)+235929600*F"
    },
    "hard_caps": {"preview_requests": 1024, "apply_requests": 2048, "preview_response_bytes": 268435456, "apply_response_bytes": 536870912},
    "admitted": {"preview_requests": 1, "apply_requests": 4, "preview_response_bytes": 16777216, "apply_response_bytes": 16777216}
  },
  "parent_budget": {"max_requests": 1, "max_response_bytes": 16777216},
  "authorization": {"request_count": 1, "sha256": "<digest>"},
  "proposal_hash": "<digest>",
  "usage": {"requests": 1, "response_bytes": 512},
  "rows": [{
    "row": 2,
    "family": "label",
    "requested": {"source_key": "EXAMPLE-1"},
    "effect": {"action": "add", "count": 1, "bytes": 8, "sha256": "<digest>"},
    "qualified": {"source_id": "10001", "project": "EXAMPLE", "updated_sha256": "<digest>"},
    "authorization": {"verbs": ["update"], "targets": [{"service": "jira", "kind": "issue", "key": "EXAMPLE-1", "project": "EXAMPLE"}]},
    "proposal_hash": "<digest>",
    "status": "would_apply",
    "complete": true,
    "write_attempted": false,
    "reconciled": false,
    "usage": {"requests": 1, "response_bytes": 512}
  }]
}
```

The four row families are closed rather than a generic union. Requested shapes
are `source_key` plus `target_key` for links or `field_id` for fields. Link
effects are `action,selector_bytes,selector_sha256,resolved_type_id,resolved_role`;
label effects are `action,count,bytes,sha256`; comment effects are
`satisfaction_policy,body_bytes,body_sha256` with policy
`exact_body_present`; field effects are
`source,kind,bytes,sha256,prepared_bytes,prepared_sha256`. Link qualification
uses `source_id,target_id,source_project,target_project,source_updated_sha256`.
Other families use `source_id,project,updated_sha256`, followed by the
comment baseline/actor or field catalog members. `baseline_count` is emitted
even when it is zero. Authorization targets deliberately omit numeric IDs.

Arrays are never null. Rows always contain `row,family,requested,effect,status,
complete,write_attempted,reconciled,usage`; qualification, authorization, and
proposal members are omitted until established. Row statuses are
`would_apply`, `already_satisfied`, `applied`, `recovered`, `blocked`,
`not_applied`, `skipped`, or `outcome_unknown`. Reasons, when present, are
closed to `qualification_failed`, `policy_denied`, `proposal_changed`,
`deadline_expired`, `write_rejected`, `aggregate_barrier`,
`prior_row_failed`, and `ambiguous_outcome`.

Aggregate status precedence is unknown, preview blocked/would-apply/satisfied,
then apply applied/partially-applied/not-applied/satisfied/blocked. `complete`
is false for any skipped or unknown row. The path, raw values, timestamps,
actors, inventories, response bodies, and backend error text are never emitted.
Text mode is one content-free `plan` line followed by physical-row-order `row`
lines containing only status, completion, dispatch/reconciliation booleans,
hash, and aggregate usage.

`atl jira issue field preview <KEY>` and the dry-run form of
`atl jira issue field set <KEY>` share one deterministic single-issue proposal
result. The dedicated preview command is GET-only and available under the
process-wide read-only policy; `field set` is classified as mutating regardless
of flags. The result is:

```json
{
  "key": "PROJ-1",
  "mode": "dry-run",
  "status": "would_apply",
  "expected_updated": "2026-01-02T03:04:05.000+0000",
  "actual_updated": "2026-01-02T03:04:05.000+0000",
  "proposal_hash": "<hex>",
  "catalog": [{"id":"customfield_10001","custom":true}],
  "current": [{"field":"customfield_10001","present":true,"kind":"string","bytes":12,"sha256":"<hex>"}],
  "prepared": {"bytes": 71, "sha256": "<hex>"},
  "bounds": {"max_json_nesting_depth": 10000, "max_value_nesting_depth": 9997},
  "fields": [
    {
      "field": "customfield_10001",
      "source": "markdown",
      "kind": "string",
      "bytes": 42,
      "sha256": "<hex>",
      "value": "h2. Progress\n\nOn track."
    }
  ],
  "write_attempted": false,
  "reconciled": false,
  "complete": true
}
```

The aggregate proposal hash uses schema v3 and binds backend origin, immutable
numeric issue id, canonical key/project, exact updated marker, complete selected
catalog/current projections, normalized desired values, prepared request
digest, and fixed bounds. The normalized values are intentionally present in JSON stdout for review and
may be private. `-o text` omits them and prints hashes/sizes. Status is one of
`would_apply`, `already_satisfied`, `applied`, `blocked`, `failed`, or `unknown`.
Apply performs complete catalog/key qualification, repeats qualification by
immutable numeric id immediately before one raw single-attempt PUT, and always
performs exactly one numeric-id readback after an actual PUT. For a
definitive 4xx rejection, proposals already visible are `already_satisfied`
(another actor may have produced the end state); absent/unreadable proposals
are `failed`. An ambiguous transport/timeout/5xx outcome is `applied` when the
proposals are visible with a strictly advancing updated marker and remains `unknown` otherwise (an
immediate old read cannot prove an in-flight write will not commit). Successful
readbacks carry `"reconciled": true`; incomplete qualification/readback carries
`"complete": false`. `write_attempted:true` always forbids automatic replay. A stale apply still emits the
`blocked` result and exits 8. Only `field set --apply` can write, and it requires both
`--expected-updated` and `--expected-proposal-hash`. The latter binds sorted
field ids, sources, normalized types, values, the fixed 10,000-level strict
parser bound, and the 9,997-level structured-value bound that leaves three
containers for the released result envelope. A changed local input is detected after the initial catalog and
issue reads but before prewrite qualification or write. On a stale apply,
`expected_updated` retains the caller-reviewed value while `actual_updated`
reports the newly observed value; the hash binds actual state.

The result truth table uses `A=write_attempted`, `R=reconciled`, and
`C=complete`:

| mode | status | A | R | C |
|---|---|---:|---:|---:|
| dry-run | `would_apply` / `already_satisfied` | false | false | true |
| dry-run | `blocked` | false | false | false |
| apply | `already_satisfied` | false or true | same as A | true |
| apply | `blocked` | false | false | false or true |
| apply | `applied` | true | true | true |
| apply | `unknown` / `failed` | true | false or true | same as R |

A typed adapter refusal before dispatch is migrated from `failed` to `blocked`, `A=false`,
and exit 8 even when its retained cause is auth, configuration, forbidden, or
usage. `unknown` remains an ambiguity-marker-only exit 1. A definitive failure
retains its ordinary cause-derived exit, including exit 2 for HTTP 400. All
proposed fields are sent in one request.

`atl jira issue transition preview <KEY>` and the dry-run form of
`atl jira issue transition <KEY>` emit one state-bound proposal. The result
contains canonical issue identity, mode/status, reviewed transition identity,
current status/update evidence, sorted requested-field current/desired values,
optional reviewed comment evidence, completeness/reconciliation flags, and the
versioned proposal hash. Exact field and comment values are intentionally
present in JSON for review and may be private. `-o text` omits them and prints
only status, issue/transition identity, counts, byte/hash evidence, and the
proposal hash.

Preview is separately classified GET-only and available under the process-wide
read-only policy; the parent transition command is always mutating. Apply
requires `--apply --expected-proposal-hash`, reconstructs the issue, selected
transition, requested-field, and optional complete comment baseline immediately
before at most one exact-id POST, and disables transport retries for that POST.
Every successful or ambiguous attempt gets fresh issue readback and, when a
comment was requested, a complete comment readback. No POST is automatically
replayed, and matching the target status before execution is not treated as
idempotency because a transition is an event.

Status is closed to `would_apply`, `applied`, `not_applied`, `conflict`, and
`unverifiable`. A definitive rejection is `not_applied`. `applied` requires the
exact requested end state and unique optional comment attribution. Divergent or
partially attributable state is `conflict`; failed/incomplete readback is
`unverifiable`. Unsafe outcomes return non-zero after emitting the result and
carry `reconcile_write_outcome` recovery with `retry_safe:false`.

`atl jira issue delete <KEY>` is dry-run by default and emits one permanent
deletion proposal:

```json
{
  "schema_version": 1,
  "requested_key": "PROJ-1",
  "key": "PROJ-1",
  "issue_id": "10001",
  "issue_id_sha256": "<sha256>",
  "mode": "dry-run",
  "status": "would_apply",
  "operation": "delete",
  "observed_state": "present",
  "current_updated": "2026-08-02T20:00:00.000+0000",
  "expected_updated": "2026-08-02T20:00:00.000+0000",
  "subtask_count": 0,
  "subtasks": [],
  "subtasks_sha256": "<sha256>",
  "delete_subtasks": false,
  "backend_sha256": "sha256:<digest>",
  "proposal_hash": "<sha256>",
  "write_attempted": false,
  "permission_relative": true,
  "complete": true,
  "warning": "..."
}
```

Apply requires `--apply --confirm DELETE`, `--expected-updated`, and
`--expected-proposal-hash`. The proposal binds the backend, exact key and
immutable numeric id, `updated`, the canonical complete permission-relative
subtask inventory, and cascade intent. A non-empty inventory blocks unless
`--delete-subtasks` was included in both the reviewed preview and apply.
Immediately before one non-replayed DELETE by numeric id, ATL repeats the exact
snapshot and blocks any proposal drift.

Statuses are `would_apply`, `blocked`, `not_applied`, `applied`, and
`outcome_unknown`. Only an acknowledged DELETE followed by an exact numeric-id
not-found readback is `applied`. A definitive rejection is `not_applied`.
Transport, throttling, server, or response ambiguity remains `outcome_unknown`
even when the permission-relative readback is not found, because Jira exposes
neither a tombstone nor a physical-deletion receipt. `write_attempted:true`
always forbids automatic replay; stdout failure after that boundary is exit 8.

`atl jira issue comment preview <KEY>` and the dry-run form of
`atl jira issue comment add <KEY>` emit one baseline-bound append proposal:
`{schema_version,operation,satisfaction_policy,backend_sha256,requested_key,
issue_id,key,project,updated,readback_updated?,body_sha256,body_bytes,
actor_sha256,current_count,baseline_sha256,exact_body_count,bounds,usage,mode,
status,proposal_hash?,comment_id?,write_attempted,reconciled,complete}`. The
reviewed native body, actor values, individual baseline ids/metadata, backend
URL, and backend response detail are never emitted. `-o text` is the same
content-minimized contract in human form.

Preview is the independently GET-only command available under read-only policy;
the parent remains mutation-classified even in dry-run mode. The versioned hash
binds exact native body bytes and the full sorted qualified record baseline—not
only comment ids—plus immutable issue/project/revision, actor, backend digest,
exact-body predicate/count, and fixed bounds. Direct CLI satisfaction policy is
always `append_always`.

Status is closed to `would_apply`, `blocked`, `not_applied`, `applied`,
`recovered`, `already_satisfied`, and `outcome_unknown`. `already_satisfied` is
reserved for the app-only `exact_body_present` seam and is not produced by the
direct CLI. `applied` proves the acknowledged numeric id through exact advancing
readback; `recovered` proves exactly one matching new record after an ambiguous
or malformed acknowledgement. `outcome_unknown` is never replay-safe: complete
conflicting readback reports `complete:true`, while unavailable or incomplete
readback reports `complete:false`. An attempted POST is never replayed; a
stdout failure after `write_attempted:true` also exits 8 with an explicit
no-replay diagnostic.

`atl jira issue watchers list <KEY>` emits
`{key,watch_count,is_watching,watchers:[{name,key?,display_name?,active}],
complete,truncated?}`. Jira DC does not paginate this endpoint: completeness
requires every counted watcher to have a returned username. A count/identity
mismatch sets `complete:false`, `truncated:true`, and a stderr warning.

`atl jira issue watchers add|remove <KEY>` is dry-run by default and emits
`{key,operation,mode,status,username,identity_source,current,final?,
proposal_hash,complete,reconciled?}`. Exactly one of an explicit DC
`--username` or `/myself`-resolved `--me` is required. The proposal hash binds
issue, operation, resolved username, and complete current membership. Apply
requires the reviewed hash before `already_satisfied` or one non-retried write,
then verifies membership. Status is `would_apply`, `already_satisfied`,
`blocked`, `failed`, `applied`, or `unknown`; unknown is non-zero and must not
be automatically replayed. Incomplete membership refuses every mutation.

`atl jira issue worklog list <KEY>` emits
`{key,worklogs:[{id,issue_id?,author:{name?,key?,display_name?,active},comment?,
started,created?,updated?,time_spent?,time_spent_seconds}],total,complete}`.
The adapter consumes every advertised page and rejects missing/changing totals,
offset anomalies, empty incomplete pages, and missing/duplicate worklog ids.
Authors are a closed compact projection: email, avatars, self URL, and timezone
are never present. `-o text` is an escaped Markdown table and `-o id` emits one
worklog id per line.

`atl jira issue worklog add <KEY>` is dry-run by default and emits
`{key,mode,status,time_spent,time_spent_seconds,comment?,started?,author,
current_count,baseline_sha256,proposal_hash,created?,complete,reconciled?}`.
`baseline_sha256` is a deterministic digest of the complete sorted worklog-id
set; it exposes no comment or author value. The schema-v2 proposal hash binds
that baseline digest together with the issue key, normalized
seconds/comment/start time, and current compact author identity. Apply requires
the reviewed hash after a fresh complete baseline, sends exactly one non-retried POST with
`adjustEstimate=leave`, and returns `applied`, `blocked`, `failed`, or
`unknown`. An intervening worklog changes both hashes and blocks before POST.
After an ambiguous response, only one exact newly observed match can
prove `applied`, and that proof requires an explicit review-bound `--started`
timestamp. Every other outcome is non-zero `unknown` and must not be
automatically replayed.

`atl jira issue fields <KEY>` emits
`{key,mode,non_empty_only,count,omitted_empty?,summary?,fields:[{id,name,custom,
schema?,empty?,value_type?,value?,truncated?,original_bytes?}]}`. Default mode is `compact`
and omits empty fields. Exact repeatable `--field` selectors accept ids or
case-insensitive display names; ambiguous names fail before the issue read.
Compact user values omit email/avatar/self data, known options/named values use
closed projections, and unknown objects expose only bounded non-empty key names.
Explicit `--include-empty` returns the union of catalog fields and fields
actually observed on the issue, so a populated plugin/private field absent from
the catalog cannot disappear. Explicit `--raw` switches mode
to `raw`, preserves unprojected private values, and writes a privacy warning to
stderr. Explicit `--metadata-only` switches mode to `metadata`, omits `value`
entirely, and emits only the closed coarse `value_type` alongside field
identity/schema/emptiness. It preserves non-empty and `--include-empty`
semantics, including observed plugin fields absent from the catalog, and adds
`summary:{custom_count,system_count,unclassified_count,nonempty_id_count,
missing_id_count,nonempty_ids_unique,value_type_counts}`. Custom and system
counts cover catalog-classified fields; an observed field absent from the
catalog is counted separately as unclassified. Missing ids are kept separate
from uniqueness among non-empty ids. The summary is derived from the returned
array without another backend request. `--metadata-only` conflicts with
`--raw` before config/network access. Its `-o text` table has no value column;
compact/raw keep their existing escaped Markdown table.

`atl jira issue field get <KEY> --field <ID-or-name>` emits one qualified,
bounded expansion:

```json
{
  "schema_version": 1,
  "issue": {"id": "10001", "key": "PROJ-1", "updated": "2026-07-01T10:00:00.000+0000"},
  "field": {"id": "customfield_10002", "name": "Delivery Notes", "custom": true, "schema": "string", "present": true, "empty": false, "value_type": "string"},
  "projection": "compact",
  "max_value_bytes": 16384,
  "original_value_bytes": 24,
  "emitted_value_bytes": 24,
  "complete": true,
  "truncated": false,
  "value": "Current delivery status"
}
```

The command resolves exactly one field and reads it together with Jira
`updated`; a technical id does not require a catalog request and uses the id as
its fallback display name. Missing update provenance, ambiguous names, and malformed
values fail closed. `complete` qualifies the compact projection; properties
deliberately excluded by that projection (email, avatar, self URL, and other
transport noise) are outside the contract. The encoded compact `value` is at
most `max_value_bytes` (default 16 KiB, hard range 256 bytes..128 KiB).
`-o text` emits a one-row escaped Markdown table with issue/update/field/value.

`atl jira issue field batch --key <KEY> --field <ID-or-name>` emits a
qualified ordered schema-v1 matrix and supports JSON only:

```json
{
  "schema_version": 1,
  "operation": "jira_issue_field_batch",
  "projection": "compact",
  "complete": true,
  "reconciled": true,
  "selection": {"key_count": 2, "field_count": 1},
  "bounds": {
    "max_keys": 25,
    "max_key_bytes": 64,
    "max_fields": 8,
    "max_field_selector_bytes": 1024,
    "max_catalog_fields": 4096,
    "max_catalog_member_bytes": 1024,
    "max_cell_bytes": 16384,
    "max_search_pages": 25,
    "max_requests": 64,
    "max_response_bytes": 16777216,
    "max_output_bytes": 16777216,
    "deadline_millis": 60000
  },
  "usage": {"requests": 2, "response_bytes": 2048, "search_pages": 1, "found_count": 1, "missing_count": 1},
  "fields": [{"id": "customfield_10002", "name": "Delivery Notes", "custom": true, "schema": "string"}],
  "issues": [
    {"requested_key": "PROJ-2", "found": false, "reason": "missing_or_inaccessible"},
    {
      "requested_key": "PROJ-1",
      "found": true,
      "id": "10001",
      "key": "PROJ-1",
      "updated": "2026-07-01T10:00:00.000+0000",
      "cells": [{
        "field_id": "customfield_10002",
        "state": "value",
        "complete": true,
        "truncated": false,
        "original_value_bytes": 24,
        "emitted_value_bytes": 24,
        "value": "Current delivery status"
      }]
    }
  ]
}
```

Top-level `reconciled:true` proves the qualified catalog, stable search
exhaustion, identity checks, and requested-key reconciliation. Top-level
`complete` is true only when every emitted cell is also complete; any clipped
cell makes it false while reconciliation remains true. Neither signal turns
`missing_or_inaccessible` into an existence claim. Arrays are
always non-null. Found rows contain all selected cells in field order; missing
rows omit `id`, `key`, `updated`, and `cells`. Cell state is closed to
`absent|null|empty|value`; only `absent` omits `value`, while explicit null
encodes as `"value":null`. Clipping is orthogonal to state and is qualified by
the per-cell completeness and byte members plus top-level `complete:false`. The complete indented document,
including its trailing newline, must fit `bounds.max_output_bytes` before any
stdout byte is written.

Online Jira get/pull/view field selectors resolve exact names through the same
catalog. Render selectors are stored as resolved ids in view state, so offline
render/apply does not depend on a later metadata lookup. Existing technical ids
remain valid without an extra field-catalog request.

`atl jira issue history <KEY>` emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,history,
summary,last_changes?}`. Each history item preserves both `field` and `field_id`
when Jira supplies them. `summary` is derived from the final filtered `history`
array without another backend request. It contains entry/item totals, non-empty
identity/author/timestamp/field counts, explicit `history_id_missing_count` and
`history_nonempty_ids_unique` facts, emitted non-empty `from`/`to` member
counts, status-item count, multi-item-entry count, stable per-field buckets, and
the `count_matches_history` / `fetched_matches_total` consistency checks. Field
buckets use the case-insensitive technical id when available and otherwise the
trimmed case-insensitive display name, then sort by id/name. Thus
`distinct_item_field_count == len(summary.fields)`.

`history_ids_unique` retains its original compatibility semantics over every
emitted id value, including empty values. Use `history_id_missing_count` to
measure absent ids and `history_nonempty_ids_unique` to detect duplicate
non-empty ids without conflating the two conditions.

`summary.chronological_comparable` is false if any emitted timestamp cannot be
parsed. In that state `chronological_ascending` is JSON `null`, rather than a
misleading false; otherwise it is true for a non-decreasing sequence (including
an empty history) or false for an out-of-order sequence. A true
`fetched_matches_total` alone is not proof of completeness: only top-level
`complete:true` means every entry advertised by the chosen backend
representation was consumed. `complete:false` always carries a reason and must
not be interpreted as proof that an omitted change did not happen.
`source` is `paginated`, `embedded`, or `legacy`. Repeatable exact `--field`
selectors and inclusive `--since`/`--until` boundaries are applied locally
after the qualified read. A date-only boundary adds
`filters.boundary_time_zone`, `boundary_time_zone_source:"jira_current_user"`,
and canonical `since_instant` / `until_exclusive_instant`; atl performs one
current-user metadata GET and uses the observed IANA calendar (including DST).
For each requested civil date, the canonical interval spans from its first real
instant through one second after its last real instant. This includes midnight
gaps, folds, and historical repeated-date transitions without omitting
evidence; an entirely skipped requested date has no truthful boundary and fails
closed with exit 8. The local calculation adds no backend request.
Explicit-offset boundaries add only their canonical instant fields and perform
no timezone lookup. Missing/invalid required user timezone fails closed with
exit 8. `last_changes` reports the newest matching change per
selected resolved field within those boundaries. When a selected matching
change carries an unsupported server timestamp, latest-change ordering is
unknowable and the command fails closed with exit 8 instead of emitting
misleading metadata. `-o text` is a status line and a structurally escaped
Markdown table.

With `--summary-only`, the command performs the same qualified read and emits
`{key,complete,source,total,fetched,count,partial_reason?,filters,summary,
last_changes?}`. The raw top-level `history` member is absent by construction;
the projection neither repeats nor broadens the backend request. Its text
renderer contains deterministic facts and field buckets plus bounded
`last_changes` for explicitly selected fields, never the raw history rows.
Omitting the flag preserves the full JSON and text output byte contract.
An explicitly supplied false value, including a later duplicate override, is
rejected with exit 2 before backend access; callers must omit the flag to
request the full raw-history contract. Typed MCP `jira_issue_history` returns
this same summary projection unconditionally.

`jira epic digest` exposes the same fields under `period`. A quarter is resolved
once in the Jira current-user calendar and the resulting zone is passed into
the nested history filter, so a digest adds at most one current-user GET rather
than one per evidence source. Raw user JQL is not changed by either workflow.

`atl jira export ... --out -` is an artifact stdout mode, not a command-result
mode. JSONL emits one `JiraIssueSnapshot` per line, aggregate JSON emits a bare
snapshot array, and CSV emits its header and rows. It emits no manifest, export
result envelope, or trailing status bytes and creates no files. Diagnostics are
stderr-only. `--format`, not the global output flag, selects those artifact
bytes; `-o text` with `--out -` is rejected with exit 2. Aggregate JSON retains
the 10,000-issue/64 MiB caps; row formats
retain the identity cap and safe-CSV default. Because a late read/write failure
can leave a streamed prefix on stdout, consumers must accept the artifact only
when the process exits zero. File destinations retain the existing atomic
artifact plus `<out>.manifest.json` contract. Exact field display names are
resolved before search and exported under stable field ids.

## Jira epic digest

`atl jira epic digest <KEY>` emits schema v1 with
`{schema_version,period,includes,sources,epic,status_field?,dod_field?,children?,
comments?,links?,blockers?,history?,refs?,confluence?,staleness,warnings?}`.
`sources` qualifies each attempted component with `complete`, returned `count`,
optional `count_truncated`/`text_truncated`, and a bounded `warning`;
optional-source failure is never encoded as an empty complete result. Reference
completeness includes description, selected status/DoD fields, and comments
whenever those values contribute source text. `children.list` is the common
IssueList contract.
`staleness` contains `evaluated`, `stale`, selected status-field timestamp,
latest newer evidence timestamp, child/comment counts, and deterministic
reasons. It is evidence, not a score. Quarter/date boundaries are inclusive.
Component count/text/request caps and bounded Confluence `page section` results
remain explicit. Each `confluence[].section` uses the section shape above,
including `schema_version:1` and `page_version_gated:false`: the digest's
heading is fixed by its request rather than selected from an outline. Links use
a total `(key,type,type_name,direction,id)` order.
`-o text` renders source completeness, selected status text,
and child distribution without inventing narrative conclusions.

With `--projection compact`, the same schema additionally contains
`projection:{name:"compact",omitted:[],clipped:[]}` and summary objects for
comments, links, history, and refs. Raw collection members named in `omitted`
are absent; children retain aggregate counts but omit `children.list`.
`clipped` describes projection-level context reduction, independently of the
source-level `complete` and `*_truncated` signals. Consumers must inspect both:
projection clipping is not evidence-source truncation, and neither can be
interpreted as proof of absence. The default `full` JSON remains unchanged.

List-oriented Jira reads (`issue search`, `issue children`, `board
issues/backlog`, and `sprint issues`) share one app-layer contract:

```json
{
  "schema_version": 1,
  "source": {"kind": "board", "id": "5"},
  "selection": {"scope": "board", "jql": "status in (11,12)"},
  "projection": {
    "columns": ["position", "key", "summary", "status", "board.column"],
    "fields": ["summary", "status"],
    "ordering": "backend-rank",
    "view": "default"
  },
  "rows": [{
    "key": "PROJ-1",
    "id": "10001",
    "position": 0,
    "values": {"summary": "First", "status": "Open"},
    "context": {"board": {"column": "To Do", "in_board": true, "in_backlog": false}}
  }],
  "page": {"count": 1, "complete": true, "truncated": false, "next_cursor": null}
}
```

`rows` is always an array. Identity/order fields are fixed; selected Jira fields
live under `values`, and source semantics stay namespaced under `context`.
`projection.fields` exactly names `values`; `projection.columns` preserves the
requested human order. `--columns` derives backend fields and accepts common
identity, Jira field ids, and source-specific names such as `board.column` or
`sprint.id`. Unknown/foreign context columns fail with usage. `-o text` renders
the same rows as one safe Markdown table (or `_None._`); `-o id` prints keys.
The page cursor is `null` at exhaustion and resumable only when non-null.
Ordinary JQL search pages qualify exhaustion from Jira's paging coordinates.
An empty page is complete only when those coordinates prove that no remainder
exists. When Jira advertises more results but returns no rows, the page is
`complete:false`, `truncated:true`, has `next_cursor:null`, and carries
`partial_reason:"pagination_stalled"`; inconsistent paging coordinates use
`pagination_unqualified`. Compatibility tracker implementations that do not
expose qualification use `legacy_unqualified`. These are the only non-empty
search-page partial reasons, and they never contain backend text. A resumable
page with a non-null cursor is incomplete but omits `partial_reason` because the
continuation itself identifies the next safe action. Board, backlog, sprint,
and epic-child page qualification is unchanged.
For board pages, top-level `position` is the zero-based position within the
returned page; ordering is backend rank, but ATL does not expose that index as
a durable Jira rank value.

`projection.view` is `default`, `full`, a configured custom name, or
`explicit` when `--columns`/`--fields` supplied the projection. Applicable
commands accept `--view`; explicit projection flags win. Effective config
always exposes source-specific built-in `default` and `full` entries under
`jira_list_views`; custom entries inherit default arrays they omit. Unknown
views or context columns invalid for the selected source fail with usage before
network access.

`jira issue children <EPIC-KEY>` returns `source.kind:"epic"`, records the
parent key and resolved Epic Link field under `selection`, and namespaces
`parent` plus relation `epic-child` under `rows[].context.epic`. It resolves
field metadata once and executes one paginated generated JQL request; it does
not read every child individually. Its default columns are
`key,summary,status,issuetype,assignee`. The generated epic-children and
subtasks sections in transient/durable issue Markdown use the same table
renderer in embedded mode; an empty related list is `_None._`.
