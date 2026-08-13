# Confluence pages and attachments

Read and mutate page lifecycle, metadata, labels, hierarchy, blogs, attachments, and current-user evidence.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [`atl conf page resolve`](#atl-conf-page-resolve)
- [`atl conf page outline` / `atl conf page section` / `atl conf page sections`](#atl-conf-page-outline--atl-conf-page-section--atl-conf-page-sections)
- [`atl conf page get`](#atl-conf-page-get)
- [`atl conf page view`](#atl-conf-page-view)
- [`atl conf page meta`](#atl-conf-page-meta)
- [`atl conf page labels list|add|remove`](#atl-conf-page-labels-listaddremove)
- [`atl conf page title set`](#atl-conf-page-title-set)
- [`atl conf page history`](#atl-conf-page-history)
- [`atl conf page create`](#atl-conf-page-create)
- [`atl conf blog create`](#atl-conf-blog-create)
- [`atl conf page move`](#atl-conf-page-move)
- [`atl conf page delete`](#atl-conf-page-delete)
- [`atl conf page list`](#atl-conf-page-list)
- [`atl conf page open`](#atl-conf-page-open)
- [`atl conf page copy`](#atl-conf-page-copy)
- [`atl conf attachment search`](#atl-conf-attachment-search)
- [`atl conf attachment {list,get,upload,delete}`](#atl-conf-attachment-listgetuploaddelete)
- [`atl conf me`](#atl-conf-me)
<!-- reference-navigation:end -->

## `atl conf page resolve`

Resolve a Confluence content id or supported same-origin page URL to one stable
content id:

```bash
atl conf page resolve 12345678
atl conf page resolve 'https://confluence.example.test/spaces/ENG/pages/12345678/Page'
atl conf page resolve 'https://confluence.example.test/pages/viewpage.action?pageId=12345678'
atl conf page resolve '/x/AwAG'
```

Supported URL forms are modern `/spaces/<space>/pages/<id>/...`, exact
`/pages/viewpage.action?pageId=<id>`, REST self links, legacy
`/display/<space>/<title>`, and one `/x/<token>` short-link redirect. Absolute
URLs must exactly match the configured scheme, host/port, and context path;
userinfo, foreign hosts, HTTPS downgrades, duplicate page ids, nested short
links, and unsupported final redirects fail closed. Display links use one exact
CQL lookup and reject zero or ambiguous matches. Numeric/opaque ids and direct
id-bearing URLs need no backend request.

JSON is `{id,kind,via?,network_requests,space?,title?}`; `-o id`/`-o text`
prints the stable id. The same resolver is used by read-only page
get/view/meta/history/open, `pull --id`, `comment list`, `page labels list`,
attachment list/get, and `table extract`. Mutating page selectors remain
explicit ids.

## `atl conf page outline` / `atl conf page section` / `atl conf page sections`

Inspect a long page without placing its entire rendered body in agent context:

```bash
atl conf page outline 12345678
atl conf page outline '/x/AwAG' -o text
atl conf page section 12345678 --heading 'Delivery Notes' -o text
# bind an outline-selected heading to the exact version that outline returned
atl conf page section 12345678 --heading 'Delivery Notes' --occurrence 2 \
  --max-bytes 131072 --expected-version 7
atl conf page sections 12345678 \
  --heading 'Summary' --heading 'Delivery Notes' --heading 'Risks' \
  --occurrence 0 --occurrence 2 --occurrence 0 \
  --max-bytes 262144 --expected-version 7
```

`outline` parses native CSF and walks the same structural block traversal as the
Markdown renderer. Headings inside code/structured macros, tables, and other
opaque blocks are not promoted into page sections. All three commands emit
`schema_version:1` — they are one selection protocol, so no result should be
validated against another shape — and all three reconcile page identity
before parsing any body, failing with exit 8 rather than returning a result
that cannot be attributed to the resolved page and a positive version.
JSON includes ordered
`headings` with `index`, native `level`, normalized hierarchy `path`, and a
1-based occurrence among equal case/whitespace-normalized titles. `count` is
the emitted count, `total` is the parsed count, and `complete`/`truncated`
expose the 1000-heading/262144-byte safety caps; `original_bytes` and
`emitted_bytes` qualify the bounded heading records. A partial outline also
names its limiter in `partial_reason`: `heading_limit` when the 1000-heading cap
stopped emission first, `byte_limit` when the 262144-byte cap did. Both are
terminal — there is no larger outline bound to retry with. `-o text` emits an
indented Markdown list.

`section` selects an exact case/whitespace-normalized heading and renders it,
its body, and descendant headings through the existing link/color/table-aware
renderer, stopping before the next heading of the same or higher rank. Duplicate
titles fail with exit 8 until `--occurrence` is supplied. JSON reports the
selected `heading`, `level`, `path`, `occurrence`, `markdown`, `complete`,
`truncated`, `original_bytes`, `emitted_bytes`, and the always-present
`page_version_gated`. The default output cap is
262144 bytes; `--max-bytes` accepts 1..1048576 and truncates only before a whole
rendered block, adding a visible marker when it fits. A partial section names
its limiter in `partial_reason`: `max_bytes` when a whole rendered block did not
fit, `invalid_utf8` when the rendering was withheld entirely. `-o text` emits
only the selected Markdown. All three commands accept the safe page references
above, are read-only, and
create no mirror artifacts.

`sections` accepts 1..32 repeatable `--heading` selectors. Omit
`--occurrence` entirely when every heading is unique; when any occurrence is
given, supply one non-negative value per heading in the same order (`0` keeps
the unique-heading check). The command resolves every selector against one
fetched and parsed page snapshot before returning anything, then emits entries
in request order. JSON adds `requested_count`, `returned_count`, `reconciled`,
aggregate `complete`/`truncated`, `original_bytes`, `emitted_bytes`,
`max_bytes`, and ordered `sections` with the same per-section fields. The
aggregate bound defaults to 262144 and accepts 1..1048576 bytes. Remaining
capacity is divided among remaining selectors in request order; unused emitted
capacity carries forward, so output is deterministic and total
`emitted_bytes` never exceeds `max_bytes`. `-o text` concatenates only the
selected Markdown in request order. Treat a result as complete evidence only
when counts match, `reconciled:true`, and aggregate `complete:true`.

`--expected-version N` binds a section or multi-section read to a page revision
you already observed. A positive value refuses the read with exit 8 unless the
page is currently at exactly that version, reporting only the expected and current
version integers, and reports `page_version_gated:true` on a match. `0` (the
default) or an omitted flag leaves the read ungated and reports
`page_version_gated:false`; a negative value is a usage error (exit 2). The
check reuses the page response the read already fetched, so it issues no extra
backend request and grants no write capability.

Which section a heading resolves to depends on the revision it is resolved
against, because `occurrence` and `path` are positional. Pass the `version` from
the `page outline` result whenever the heading, path, or occurrence came from
that outline, and the `version` from the first section result when re-reading
the same selection at a wider bound. A selection fixed outside any earlier read
— a heading named in the task itself — has no earlier revision to reconcile, so
it may omit the flag. That ungated result is still exact evidence for the
revision reported in its own `version`; it simply reconciles no earlier
selection, which is what `page_version_gated:false` states.

For an outline, one section, or each section entry in a plural result,
`partial_reason` is a static identifier that carries no page content and is
present exactly when `complete` is `false`. Treat any partial outline or section
as incomplete evidence: do not infer that missing content is
absent and do not settle a decision from it. Only `max_bytes` is recoverable —
re-read the same reference, heading, and occurrence **once** with `--max-bytes`
at or above the reported `original_bytes`, which is the exact minimum bound for
the same valid rendering, and only when that value is within the 1048576-byte
cap. Pass `--expected-version` with the first result's `version` on that
re-read, so a page that moved is refused instead of silently answering from a
body the first result never described, and accept the recovery only when the
second result also has `complete:true`. Otherwise do not retry the same read;
use a narrower heading or the full page instead.
In a plural result the aggregate `original_bytes` is only the sum of full
section sizes; it is not an exact recovery bound for the request-order
allocator. Recover a required `max_bytes` entry once with singular
`page section`, that entry's `original_bytes`, and the plural result's exact
`version`. Do not retry the entire plural request until it happens to fit.

## `atl conf page get`

Fetch a page body directly (without mirroring to disk).

```bash
atl conf page get --id 12345678
atl conf page get --id 12345678 --format view   # rendered HTML view
atl conf page get --id 12345678 -o text         # raw body on stdout
```

Flags:

| flag | description |
|---|---|
| `--id` | page id (required) |
| `--format` | `csf` (default) or `view` (rendered HTML) |

Both formats require the backend to include the requested body projection
(`body.storage.value` or `body.view.value`). An omitted projection exits `8`
instead of appearing as an empty body; an explicitly present empty value is
valid.

## `atl conf page view`

Fetch native CSF and render one page through the same configured Markdown
pipeline as pull/render, without creating a mirror:

```bash
atl conf page view 12345678 -o text
atl conf page view 12345678 --render-profile full
atl conf page view 12345678 --render-root ~/.atl/workspace
atl conf page view 12345678 --jira-view full -o text
atl conf page view 12345678 --jira-macros off -o text
```

JSON output contains `id`, `title`, `space`, `version`, and `markdown`; `-o
text` emits only Markdown. The local `--render-root` is read for
presentation-only config and is never created or modified. Binary assets and
view state are not fetched or written. Comments are requested only when the
effective render profile includes `comments`; a capped result produces a
stderr warning.

Jira JQL macros use the same read-only IssueList enrichment as pull. Their
configured columns take precedence; otherwise `--jira-view` selects the named
`confluence_macro` projection. This may make bounded Jira search requests, but
never per-issue reads or Jira writes. Single-key Jira macros remain ordinary
readable Jira links.

The document and its body are explicitly marked read-only because transient
output has no synchronized CSF/baseline. Do not save it into a mirror or feed it
to apply/push. Pull the page fresh before any edit.

Flags:

| flag | description |
|---|---|
| `--render-root` | root whose local render config is used; never written |
| `--render-profile` | `minimal`, `default`, or `full` |
| `--render-include` | comma-separated Confluence sections to add |
| `--render-exclude` | comma-separated Confluence sections to remove |
| `--jira-view` | named Jira list projection for JQL macros (default `default`) |
| `--jira-macros` | JQL macro expansion mode: `auto` or `off`; overrides configured render policy |

## `atl conf page meta`

Fetch non-body page metadata (version, ancestors, labels, restrictions).
`restricted` is omitted when the backend omitted restriction state; absence
means unknown, never unrestricted. `-o text` prints identity/version first,
then only present metadata plus an explicit `restricted true|false|unknown`.

```
atl conf page meta --id 12345678
```

## `atl conf page labels list|add|remove`

List the complete content-label collection, or preview a guarded change:

```bash
atl conf page labels list 12345678
atl conf page labels add 12345678 release-ready needs-review
atl conf page labels remove 12345678 obsolete --apply \
  --expected-proposal-hash <hash-from-preview>
```

`list` follows Confluence pagination and emits `complete:false` plus a stderr
warning if the safety cap is reached. Mutation inputs are trimmed,
deduplicated byte-exactly, sorted for a stable review, bounded to 100 unique
names and 255 bytes per name, and rejected on control/invisible characters.
The preview hash binds page id, operation, normalized names, and the complete
current prefix/name set. Apply re-reads that set and requires the exact hash,
including when the goal is already satisfied.

Mutations deliberately manage only `global` labels; personal/team labels remain
visible in `list` and preview but never satisfy or become mutation targets. Add
uses one global-label POST; remove uses one query-parameter DELETE per reviewed
global name so `/` never becomes a path component. Because that endpoint
selects only by name, removal fails closed if the same name also has a
non-global prefix. Writes are never retried.
The command re-lists labels after any write: verified state reports `applied`,
a pre-existing goal reports `already_satisfied`, and an unverifiable or partial
outcome reports `unknown` with a non-zero exit and must not be replayed
automatically. Re-pull the page after a successful change if an existing mirror
must show the new metadata.

## `atl conf page title set`

Preview a page-title update from a bounded file or stdin; no write occurs by
default and the title never needs to appear in argv:

```bash
atl conf page title set 12345678 --from-file title.txt
# review title, hashes, and current_version, then:
atl conf page title set 12345678 --from-file title.txt --apply \
  --expected-version 7 \
  --expected-proposal-hash <hash-from-preview>
```

The preview normalizes surrounding whitespace, rejects empty/multiline/control
text and inputs over 4096 bytes, and returns `current_title`, `title`,
`title_bytes`, `title_sha256`, `current_version`, `expected_version`, and an
aggregate `proposal_hash` binding page id + version + normalized title. Apply
fresh-reads native CSF/title/version, requires both reviewed gates, and sends the
unchanged CSF with the new title in one version-gated PUT. There is no `--force`.

Every successful or ambiguous PUT is verified by another native page read. A
verified exact title/body/version reports `applied`; a pre-existing target title
reports `already_satisfied` only after the reviewed version and proposal-hash
gates pass. Ambiguous outcomes report `unknown`, exit non-zero,
and must be inspected rather than automatically replayed. The command itself
does not rewrite an existing mirror path or sidecar; after `applied`, re-pull
that page before further mirror edits. Re-pull relocates by stable page id only
when the old CSF and recorded Markdown are pristine and the new path is
unoccupied; otherwise it fails closed without deleting descendants.
Retained descendants/assets/comments stay protected by a local ownership marker,
so another page with the old slug is diverted instead of inheriting them.
If all old `.csf`, `.md`, and `.meta.json` primary files were deliberately
removed, re-pull repairs the stale sidecar path. A partial removal remains
ambiguous and exits `8`; restore the complete old page or remove all three
primary files, then re-pull. A supported v5/v4 view receives explicit
`conf render` migration guidance instead of the generic local-edit diagnostic;
only an exact pristine reconstruction can migrate.
If interrupted cleanup leaves an old copy, `conf status` marks it
`non_canonical` and names `canonical_path`; `conf push` refuses the old path
even with `--force`.

Flags:

| flag | description |
|---|---|
| `--from-file FILE|-` | required bounded title input |
| `--apply` | perform the guarded write; default is dry-run |
| `--expected-version` | reviewed current version; required with apply |
| `--expected-proposal-hash` | exact reviewed proposal hash; required with apply |

## `atl conf page history`

List a page's version records, newest first, with explicit completeness. The
JSON is the qualified result
`{schema_version:1,page_id,count,complete,partial_reason?,versions:[...]}`.
`versions` is always a JSON array; a page with no recorded versions emits
`"versions":[]`, never `null`. `complete:true` means the backend version
listing was exhausted, so an empty array is proven absence; `complete:false`
always carries a static `partial_reason` from the closed set `page_limit`,
`item_limit`, `pagination_stalled`, or `legacy_unqualified`, and never proves a
version is missing. Non-positive, duplicate, or non-descending version records
fail as a check error (exit 8) rather than being emitted as qualified evidence.
`-o text` is unchanged: one
`number<TAB>when<TAB>by[<TAB>message]` row per version.

```
atl conf page history --id 12345678
```

## `atl conf page create`

Create a new page. The body is either valid CSF (`--from-file`) or markdown
converted to CSF (`--from-md`) — the two flags are mutually exclusive.

```bash
echo '<p>Hello, <strong>world</strong>.</p>' \
  | atl conf page create --space DOCS --title "Hello" --from-file -

atl conf page create --space DOCS --parent 12345678 \
  --title "Child page" --from-file body.csf

# Author the body in markdown; atl converts it block-by-block:
atl conf page create --space DOCS --title "From markdown" --from-md body.md

# Opt in to immediate mirror registration from the authoritative readback:
atl conf page create --space DOCS --title "Tracked page" --from-md body.md \
  --register --into ./mirror
```

`--from-md` accepts the same markdown subset as `conf apply` (headings,
paragraphs, emphasis/links, lists and task lists, GFM tables, fenced code,
blockquotes/admonitions, `---`, legacy `[[Page Title]]` page links,
identity-bearing `[label](confluence-page:SPACE/title)` links, `[KEY](jira:KEY)`
issue links). Conversion is fail-closed: the first construct outside the
subset aborts with exit 8 naming the offending block, and the page is **not**
created — write those bodies as CSF via `--from-file` instead. An empty
markdown document is refused the same way. The converted body still passes
the CSF validation gate before the API call. Malformed or over-depth raw CSF is
also a `check_failed` / exit-8 refusal with its `problems[]`; no backend request
is made.

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--title` | page title (required) |
| `--parent` | parent page id |
| `--from-file` | CSF body file or `-` for stdin (default stdin) |
| `--from-md` | markdown body file or `-` for stdin; converted to CSF, fail-closed (exit 8) |
| `--register` | explicitly register the created page in a mirror; requires non-empty `--into` |
| `--into` | mirror root for registration; requires `--register` |

Without `--register`, creation and output retain their legacy remote-only
behavior. With `--register --into ROOT`, atl creates the page once, performs one
authoritative page readback, and derives the canonical `.csf`, pristine base,
`.md`, metadata, and sync/view state from that readback rather than the input
file or create response. Existing target artifacts are never adopted or
overwritten; sync state is committed only after the local artifacts and base
have been written and verified.

If the page is known to have been created but readback or local registration
fails, stdout still identifies the page and includes
`registration.status:"not_registered"`; the command exits 8. Do not repeat
`page create`. Preserve local files, resolve the reported obstruction, and
recover only that page with `atl conf pull --id <new-id> --into ROOT`.

## `atl conf blog create`

Create native Confluence `blogpost` content with a required space, title, and
non-empty body. This is a dedicated command: it cannot be confused with page
creation and does not accept a page parent.

```bash
atl conf blog create --space DOCS --title "Weekly update" --from-md update.md
atl conf blog create --space DOCS --title "Release notes" --from-file release.csf
```

Raw CSF is sent byte-for-byte after validation. `--from-md` uses the same strict
whole-document subset as `conf page create`; unsupported or empty Markdown is
exit 8 before credentials/network. Malformed or over-depth CSF is likewise
`check_failed` / exit 8 with its `problems[]`, while an empty raw CSF body
remains a usage error (exit 2); neither reaches the POST. The flags are mutually
exclusive and `--from-file -` remains the stdin default.

The adapter closes the request to `type:blogpost`, storage representation, and
the selected space; no `ancestors` field is sent. It requests an expanded write
response and requires a non-empty id, exact type/space/title, positive version,
and an explicitly present storage body. JSON output is
`{id,type,title,space,version,body_present,url}`; `-o text` is one compact record
and `-o id` prints the new content id. A successful but unverifiable response is
exit 8 and may mean the post already exists. Transport, timeout, throttling, and
server failures after dispatch are classified the same way; never retry any of
these ambiguous outcomes automatically.

The documented Data Center create-content response does not define a
case-folding, whitespace, or Unicode-normalization equivalence for `title` or
`space.key`. Atl therefore keeps exact comparison after trimming only the
caller input. A differently normalized success response remains `unknown`;
this conservative result is preferable to claiming the wrong created identity.

## `atl conf page move`

Preview a page reparenting operation by default:

```bash
atl conf page move 12345678 --parent 87654321
# review current_parent, current_version, and proposal_hash, then:
atl conf page move 12345678 --parent 87654321 --apply \
  --expected-version 7 \
  --expected-parent 11111111 \
  --expected-proposal-hash <hash-from-preview>
```

Apply fresh-reads both the source page and target parent, refuses self/descendant
cycles, incomplete hierarchy identities, cross-space parents, stale source
version/current-parent gates, and missing native source CSF. It sends the fresh
title/body unchanged in one version-gated PUT. For a source page that currently
has no parent, pass the explicit empty gate `--expected-parent=`.

Every successful or ambiguous PUT is verified by another native source-page
read. A verified exact parent/title/body/version reports `applied`; ambiguous
outcomes report `unknown`, exit non-zero, and must be inspected rather than
automatically replayed. The command itself does not relocate existing mirror
files; after `applied`, re-pull the page before further mirror edits. Re-pull
uses the same id-bound, local-edit-aware relocation path as a title change.
An already-satisfied parent is also a reviewed outcome: apply checks source
version, current parent, and proposal hash before returning it.
The proposal hash also binds the reviewed target version. Apply fetches the
target again immediately before PUT and refuses a changed version, space, or
ancestor identity. This narrows the two-page race; the source page remains the
only object protected by Confluence's write-version gate. The server's cycle
backstop was not write-tested as part of this change; do not treat it as a
client-side guarantee.

Flags:

| flag | description |
|---|---|
| `--parent ID` | proposed parent page id (required) |
| `--apply` | perform the guarded move; default is dry-run |
| `--expected-version` | reviewed current source version; required with apply |
| `--expected-parent` | reviewed current parent; required with apply, empty for top-level |
| `--expected-proposal-hash` | exact reviewed proposal hash; required with apply |

## `atl conf page delete`

Preview or apply one reviewed current-to-trashed transition. The command is
dry-run by default: preview performs exact `current`/`trashed` reads and emits a
content-minimized proposal, but sends no DELETE.
`--id` must be a canonical positive numeric content id. Aliases, URLs, signs,
leading zeroes, and surrounding whitespace are usage errors before
configuration or backend access.

```bash
atl conf page delete --id 12345678
atl conf page delete --id 12345678 \
  --apply \
  --confirm TRASH \
  --expected-version 7 \
  --expected-proposal-hash '<hash-from-preview>'
```

Apply requires all three review gates. The proposal binds the normalized
backend identity, exact page id/type/status/version, native-body hash and byte
count, title hash, space, parent, operation, and schema. ATL then repeats the
exact read immediately before one non-replayed DELETE. Confluence has no
delete-time version compare-and-set, so the second read narrows but cannot
eliminate the final race. The DELETE explicitly carries `status=current`; this
command never requests permanent purge of an already trashed page.

After a success or any possibly committed failure, ATL reads the explicit
`current` and `trashed` status namespaces. Only an exact trashed page whose
identity and native bytes match the reviewed state is `applied` or `recovered`.
A definitive permission rejection is `not_applied` (normally exit 6). Missing,
unavailable, current, or mismatched readback is `outcome_unknown` and exit 8;
never replay it automatically. A backend that cannot provide exact status
reads fails closed before DELETE. `already_satisfied` sends no DELETE.

## `atl conf page list`

Flat listing of pages in a space (no hierarchy), optionally by status.

```
atl conf page list --space ENG [--status current|archived|trashed] [--limit 100] [--cursor C]
```

`--space` is required. The output carries a `next_cursor` for pagination; `-o id`
prints the page ids. `--limit` accepts `1..100`; explicit 0, negatives, and
values above 100 are usage errors before backend access.

## `atl conf page open`

Open a page in the system browser (uses `xdg-open`/`open`/`rundll32`, no shell).

```
atl conf page open --id 12345678
```

## `atl conf page copy`

Client-side copy that preserves the native CSF bytes verbatim (no Markdown
round-trip). It is dry-run by default.

```
atl conf page copy --id 12345678 --title 'Copy of Design Doc' [--space ENG] [--parent 999]
atl conf page copy --id 12345678 --title 'Tracked copy' \
  --register --into ./mirror
atl conf page copy --id 12345678 --title 'Tracked copy' \
  --register --into ./mirror --apply \
  --expected-version 7 --expected-proposal-hash '<preview hash>'
```

`--register` and a non-empty `--into ROOT` must be supplied together. Omit both
for a remote-only copy. Preview performs exact `status=current` reads and binds
the backend, complete source state, resolved destination, optional parent state,
and canonical registration-root digest into `proposal_hash`; it performs no POST
or local write. Apply requires the previewed source version and hash, repeats the
exact source/parent reads immediately before one POST, and never retries or
searches by title. Only an exact version-1 current readback with the reviewed
native bytes/title/space/parent proves `applied` or `recovered`.

Registration consumes that same authoritative readback and commits mirror state
last. A known-created but unregistered copy emits the page id and exits 8. Any
`outcome_unknown` must not be replayed; if an id was emitted, preserve it and use
a narrow `atl conf pull --id <new-id> --into ROOT`. `-o id` is apply-only because
preview has no created identifier.

The entire copy leaf is mutating-classified, so `ATL_READ_ONLY=1` blocks its
GET-only preview as well as apply. Remove the policy only within an explicitly
reviewed copy workflow; do not weaken it globally.

## `atl conf attachment search`

Search typed attachment metadata across Confluence Server/Data Center without
first knowing a page. Every execution bound is mandatory:

```bash
atl conf attachment search --space DOCS \
  --max-items 100 --max-requests 5 \
  --max-response-bytes 8388608 --deadline 20s

atl conf attachment search --cql 'creator = currentUser()' \
  --max-items 100 --max-requests 5 \
  --max-response-bytes 8388608 --deadline 20s
```

Flags:

| flag | description |
|---|---|
| `--space` | optional exact space-key scope |
| `--cql` | optional additional CQL predicate; `ORDER BY` is refused |
| `--cursor` | opaque backend/query/space-bound live offset returned by a partial result |
| `--max-items` | required returned-item bound (`1..10000`) |
| `--max-requests` | required physical HTTP-attempt bound (`1..100`) |
| `--max-response-bytes` | required aggregate buffered-response bound (`1..268435456`) |
| `--deadline` | required wall-clock bound (positive, at most `10m`) |

The schema-v1 result carries `qualification`, `complete`, optional closed
`reason`, `consistency:"live_unproven"`, a content-free `scope_sha256`,
`start_offset`, optional `next_cursor`, `count`, optional qualified
`total_size`, exact selected/consumed `bounds`, and an `attachments` array.
Each row contains only attachment id/title/type/version, parent container
id/type/version, space key, media type, and file size. It contains no body,
comment, URL, download path, or binary bytes. Attachment and container ids are
bounded opaque `[A-Za-z0-9_-]{1,256}` identifiers, not numbers to parse or
increment. `-o id` emits attachment ids;
`-o text` begins with the same qualification and continuation evidence.

`complete` means this one bounded live traversal reached terminal,
coordinate-consistent search evidence and always includes a stable non-null
`total_size` whose exact terminal end matches the returned prefix. It is not a
snapshot claim. A partial result has one of `item_limit`, `request_limit`,
`response_byte_limit`, `deadline`, `pagination_stalled`, or
`pagination_unqualified` and an opaque continuation for the next checked
offset. The cursor is bound to the configured backend plus exact space/CQL
scope, but pagination may drift between calls.
`failed` uses only `read_failed` or `validation_failed`, never advertises a
continuation or `total_size`, and has `count:0` with an empty `attachments`
array. It is emitted before the mapped non-zero CLI error and is not safe to
resume as a prefix. Server-provided next links and attachment URLs are never
followed. The adapter uses the Server/Data Center
[CQL content-search resource](https://developer.atlassian.com/server/confluence/rest/v10214/api-group-search/)
with `type=attachment`; this endpoint is not for Confluence Cloud.

## `atl conf attachment {list,get,upload,delete}`

Manage page attachments. Permanent `delete` is preview-first, binds one exact
page revision and two independently complete qualified inventories that agree,
uses one transport attempt on apply, and refuses redirects.

```bash
atl conf attachment list --id 12345678                       # qualified inventory; -o id → ids
atl conf attachment list --id 12345678 --expected-version 7  # refuse unless the page is at v7
atl conf attachment get --id 12345678 --name diagram.png --into ./assets --max-bytes 67108864
atl conf attachment get --id 12345678 --name diagram.png --version 2 --into ./assets --max-bytes 1073741824
atl conf attachment upload --id 12345678 --file ./diagram.png [--comment 'v2']
atl conf attachment delete --page-id 12345678 --id <ATTACHMENT-ID>
atl conf attachment delete --page-id 12345678 --id <ATTACHMENT-ID> \
  --apply --confirm DELETE --expected-version 7 \
  --expected-proposal-hash '<preview hash>'
```

`list` emits `{schema_version, page_id, page_version, count, complete,
partial_reason?, attachments:[...]}`. The JSON `attachments` member is always an
array after a successful listing. Pages without attachments emit
`"attachments":[]`, not `null`; `-o id` is empty in that case. Treat an empty
array as proven absence only when `complete:true`; a `complete:false` inventory
carries a static `partial_reason` (`page_limit`, `item_limit`,
`pagination_stalled`, or `legacy_unqualified`) and is a prefix, not the whole
set.

`--expected-version N` binds the listing to a page version you already
observed. A positive value refuses the read with exit `8` when the page has
moved, before any attachment request is made, and reports only the expected and
current version integers; `0` (the default) disables the gate.

For attachment `get`, `--name` must be nonblank valid UTF-8 and at most 255
bytes. `--id` accepts a bounded opaque `[A-Za-z0-9_-]{1,256}` page id, an
absolute HTTP(S) URL, or a root-relative path. These selectors and
`--max-bytes` are validated before config, backend, or path access.

Attachment `get --version N` immediately revalidates one unambiguous exact
filename match through the Server/Data Center
[page attachment collection](https://developer.atlassian.com/server/confluence/rest/v10214/api-group-attachments/)
under the resolved page id. A positive `N` additionally
revalidates that attachment version. With `0` (the default), ATL observes the
current positive attachment version and uses that positive value in the actual
download request; the byte GET is never left floating latest. The metadata
resolution and revalidation phase has a caller-derived 15-second deadline,
2 MiB aggregate response cap, and at most five physical attempts. Ordinary
reference resolution may use its bounded read retry and safe same-origin
redirect handling; the immediate metadata revalidator calls are
single-attempt. That phase is canceled before binary transfer begins.

`--max-bytes` defaults to 67108864 (64 MiB) and accepts `1..1073741824`
(1 GiB). The CLI validates it before config, backend, or path access. Metadata
must report a present non-negative version-specific `fileSize`; a historical
version's size takes precedence over any current record. A size above the
selected ceiling, an absent/ambiguous filename, incomplete metadata page,
version mismatch, or exhausted metadata bound fails before the binary request
and before creating the output directory.

The binary phase starts from the original caller context with a separate limit
of five physical attempts. Generic replay retries are disabled, while only
finite same-origin, scheme-safe redirects are permitted inside that budget.
Binary transfer is outside the metadata 15-second/2-MiB budget and must contain
exactly the admitted number of bytes, including an `N+1` overrun probe. A
short, long, canceled, or close-failed transfer preserves any existing
destination and leaves no temporary file.

JSON emits `{schema_version:1,page_id,name,output_name,
requested_attachment_version,observed_attachment_id,
observed_attachment_version,observed_file_size,max_bytes,selector,attachment_id_bound,
identity_revalidated,page_version_gated,path}`. `name` preserves the exact
caller selector and `output_name` is its safe contained basename.
`identity_revalidated:true` means only the tuple `resolved page id + exact
caller filename + positive observed attachment version` was checked immediately
before the version-addressed GET. The binary route still addresses
page+filename+version, so `attachment_id_bound:false`; no page version was
caller-gated, so `page_version_gated:false`. Metadata and bytes are not one
transaction or snapshot. Text output remains the written path.

Uploads stream the selected file without buffering it and send the exact multipart
`Content-Length`, preserving compatibility with intermediaries that reject chunked uploads.
A caller size fault (a negative size or a multipart body that would overflow the
length) exits `2`; a successful backend response that is malformed JSON or carries
no attachment exits `8`.

Attachment deletion is permanent and preview-first. Preview brackets two
consecutive complete qualified inventories with exact current-page reads,
requires their canonical agreement, and binds the backend,
page version/native identity, selected attachment metadata, and the complete
canonical inventory into `proposal_hash`, and performs no DELETE. Partial or
legacy inventory, an absent target, malformed metadata, or a page that changes
during inventory pagination fails closed.

Apply requires `--confirm DELETE`, the previewed positive page version, and the
exact proposal hash. It re-reads the independently reconciled proposal state
before one non-replayed DELETE. Only unchanged exact page evidence and two
agreeing complete final inventories equal to the reviewed inventory with that
attachment removed prove `applied` or `recovered`; a retained target, page or
inventory drift, or
unavailable/partial readback is `outcome_unknown` (exit `8`) and must not be
replayed. The entire delete leaf is mutating-classified, so `ATL_READ_ONLY=1`
blocks its GET-only preview as well as apply.

## `atl conf me`

Show the authenticated Confluence user.

```
atl conf me
```
