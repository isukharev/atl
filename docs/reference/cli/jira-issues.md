# Jira issues

Issue reads, fields, creation, guarded edits, transitions, relationships, attachments, history, watchers, worklogs, and deletion.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [`atl jira issue get`](#atl-jira-issue-get)
- [`atl jira issue fields`](#atl-jira-issue-fields)
- [`atl jira issue field get`](#atl-jira-issue-field-get)
- [`atl jira issue view`](#atl-jira-issue-view)
- [`atl jira issue search`](#atl-jira-issue-search)
- [`atl jira issue types` / `create-check`](#atl-jira-issue-types--create-check)
- [`atl jira issue children`](#atl-jira-issue-children)
- [`atl jira issue create`](#atl-jira-issue-create)
- [`atl jira issue update`](#atl-jira-issue-update)
- [`atl jira issue field preview` / `field set`](#atl-jira-issue-field-preview--field-set)
- [`atl jira issue edit`](#atl-jira-issue-edit)
- [`atl jira issue transition`](#atl-jira-issue-transition)
- [`atl jira issue assign`](#atl-jira-issue-assign)
- [`atl jira issue comment {preview,add,list,delete}`](#atl-jira-issue-comment-previewaddlistdelete)
- [`atl jira issue link {add,list,delete,suggest}`](#atl-jira-issue-link-addlistdeletesuggest)
- [`atl jira issue link-epic`](#atl-jira-issue-link-epic)
- [`atl jira issue plan apply`](#atl-jira-issue-plan-apply)
- [`atl jira issue attachment {list,get,upload}`](#atl-jira-issue-attachment-listgetupload)
- [`atl jira issue images`](#atl-jira-issue-images)
- [`atl jira issue history`](#atl-jira-issue-history)
- [`atl jira epic digest`](#atl-jira-epic-digest)
- [`atl jira issue labels`](#atl-jira-issue-labels)
- [`atl jira issue watchers list|add|remove`](#atl-jira-issue-watchers-listaddremove)
- [`atl jira issue worklog list|add`](#atl-jira-issue-worklog-listadd)
- [`atl jira issue check`](#atl-jira-issue-check)
- [`atl jira issue delete`](#atl-jira-issue-delete)
<!-- reference-navigation:end -->

## `atl jira issue get`

Fetch a Jira issue. Default fields: summary, description, status, type,
project, assignee, reporter, labels, links, comments, attachments.

```bash
atl jira issue get PROJ-1
atl jira issue get PROJ-1 --fields summary,status,issuetype,project,labels,description,attachment
atl jira issue get PROJ-1 --fields summary,"Delivery Notes"
atl jira issue get PROJ-1 -o text
```

`--fields` accepts exact technical ids or exact case-insensitive display names.
An ambiguous display name fails closed and lists candidate ids; use an id (or
`id:<id>`) to disambiguate.

## `atl jira issue fields`

Inspect the fields actually carrying evidence on one issue:

```bash
atl jira issue fields PROJ-1
atl jira issue fields PROJ-1 --metadata-only
atl jira issue fields PROJ-1 --field "Delivery Notes" --field Impact
atl jira issue fields PROJ-1 --include-empty
atl jira issue fields PROJ-1 --field assignee --raw
```

The default is deliberately **non-empty + compact**. Each record carries
`id`, human `name`, `custom`, schema type, and a normalized value. User objects
retain only stable username/key/display/active fields; options and named Jira
objects omit email, avatars, `self` URLs, and unrelated transport properties.
Unknown structured objects are represented by their non-empty key names rather
than recursively exposing arbitrary private data. Strings, arrays, and nesting
have explicit caps; a clipped record sets `truncated` and `original_bytes`.

`--metadata-only` is the lowest-token discovery projection. It preserves the
same non-empty/default or `--include-empty` selection, sets `mode:"metadata"`,
and emits only `id`, `name`, `custom`, optional schema, optional `empty`, and a
closed `value_type` (`string`, `number`, `boolean`, `list`, `object`, `null`, or
`unknown`). Its JSON `summary` reports custom, system, and unclassified counts,
non-empty versus missing id counts, uniqueness among non-empty ids, and
deterministic counts by `value_type`. The aggregates are derived from the
returned records without another Jira request; observed fields absent from the
field catalog remain unclassified instead of being guessed as system fields.
The `value` key is absent, not redacted or set to null, so no field content can
leak into JSON or the metadata Markdown table. Use the summary and inventory to
choose one or two exact `--field` selectors, then read those values in compact
mode. `--metadata-only --raw` is rejected before config or network access.

`--include-empty` adds missing/null/empty catalog fields while retaining every
field observed on the issue, including populated plugin/private fields omitted
from Jira's field catalog. `--raw` returns the
unprojected Jira values, may include private contact and transport data, and
writes a warning to stderr. Exact `--field` selectors accept ids or names;
duplicates after resolution are collapsed, while ambiguous names fail before
the issue read.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--field` | exact field id or display name to select (repeatable) |
| `--metadata-only` | emit field metadata and value type without field values |
| `--include-empty` | include empty catalog fields in addition to observed fields |
| `--raw` | emit unprojected values (may contain private contact/transport data) |

## `atl jira issue field get`

Expand exactly one required field without repeating a broad issue or epic
snapshot:

```bash
atl jira issue field get PROJ-1 --field "Delivery Notes"
atl jira issue field get PROJ-1 --field customfield_10002 --max-bytes 32768 -o text
```

`--field` accepts one exact technical id or unambiguous case-insensitive display
name. A technical id goes directly to the issue read; a display name first uses
the field catalog and fails closed on ambiguity. Atl then requests only that
field plus `updated` in one issue GET. JSON reports schema version, issue id/key/update
provenance, field id/name/schema/presence/type, `projection:"compact"`, the
reviewed byte limit, original/emitted encoded value sizes, and
`complete`/`truncated`. The default value cap is 16 KiB; accepted values are
256 bytes through 128 KiB.

The value uses the same closed compact projection as `jira issue fields`:
users omit email/avatar/self data, options and named Jira objects retain only
compact identity, and arbitrary transport objects never expand recursively.
The cap applies to the JSON-encoded compact value, so `emitted_value_bytes`
never exceeds `max_value_bytes`. Use this command when a compact digest names a
required field in `projection.clipped`; do not rerun the whole digest with the
full projection.

## `atl jira issue view`

Fetch one issue and render the same configured Markdown projection used by
`jira pull`/`jira render`, but write nothing to disk. This is the fast path for
one-off agent reading when no offline cache or editing baseline is needed.

```bash
atl jira issue view PROJ-1 -o text
atl jira issue view PROJ-1 --render-profile full
atl jira issue view PROJ-1 --render-root ~/.atl/workspace
```

Default JSON is one object: `{"key":"PROJ-1","markdown":"..."}`. With
`-o text`, stdout is raw Markdown. Render-resolution warnings go to stderr.
The command requests only fields required by the selected profile and typed
field config; configured `epic_children` may perform its bounded related query.
It never creates a mirror, snapshot, sidecar, asset, or writeback baseline.
Because transient reads do not download images, the local Image Attachments
section is omitted; use `jira pull --assets` or `jira issue images` when image
files are needed.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--render-profile` / `--render-include` / `--render-exclude` | override configured presentation for this read |
| `--render-root` | root whose presentation-only `.atl/config.json` is used; defaults to `ATL_MIRROR_ROOT`, the nearest `.atl`, or the current directory; never written |

Do not edit or save transient output as if it were a synchronized mirror. For
writeback, first run a fresh `jira pull`, edit its generated view, then use
`jira apply` and guarded `jira push`.

## `atl jira issue search`

Search issues by JQL.

```bash
atl jira issue search --jql "project=PROJ and status=Open" --limit 20
atl jira issue search --jql "assignee=currentUser()" --cursor 50
atl jira issue search --jql "project=PROJ" --columns key,summary,status,customfield_10001
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--view` | named configured list view (`default` when omitted) |
| `--columns` | ordered metadata, Jira-field, and source-context columns |
| `--limit` | page size from 1 to 1000 (default 50; explicit 0 is invalid) |
| `--cursor` | pagination cursor (startAt offset) |

JSON uses the common IssueList contract documented below under boards and
sprints. Read rows with `.rows[]`, selected fields with `.values.<field>`, and
resume from `.page.next_cursor`. Requested page sizes through 1000 are passed
to Jira unchanged; the backend may still return fewer rows and an explicit
continuation. `-o text` is a Markdown table in the exact `--columns` order;
`-o id` prints only keys.
JQL search exhaustion is qualified from Jira's paging coordinates. An empty
page with an advertised remainder is `complete:false` with
`partial_reason:"pagination_stalled"` and no cursor, so it never proves that
the query has no matches. `pagination_unqualified` reports inconsistent paging
coordinates. Partial reasons are closed static values and never backend text.

## `atl jira issue types` / `create-check`

Discover the issue types and content-free create-screen schema that Jira Data
Center exposes for one project before attempting a create:

```bash
atl jira issue types --project PROJ
atl jira issue types --project PROJ -o id
atl jira issue create-check --project PROJ --type Task
```

`types` accepts a project key or id, returns the exact backend type ids/names
and subtask flags, and prints type ids with `-o id`. `create-check` accepts an
exact type id or exact name and fails closed when a name is absent or ambiguous.
Its fields report only id, name, required, and whether allowed values exist;
option labels and values are deliberately omitted. Every returned field is on
Jira's create screen by definition of the endpoint, so the output does not emit
a redundant `on_screen` boolean. If an exact type is not found, refresh it with
`jira issue types` rather than guessing case or spelling. Both commands read
paginated metadata with a 1000-item hard bound and explicit completeness. They
are preflight reads, not proof that a later create will succeed after permissions
or workflow configuration change.

## `atl jira issue children`

Read one page of direct epic children without constructing project-wide JQL or
performing per-child requests:

```bash
atl jira issue children PROJ-100
atl jira issue children PROJ-100 --columns key,summary,status,issuetype,assignee
atl jira issue children PROJ-100 --cursor 50 -o text
```

The command resolves Jira's `Epic Link` field metadata once, then performs one
generated, key-ordered child query. Use `--epic-field parent`, a custom field
id, or its exact display name when auto-detection is not appropriate. JSON uses
the common IssueList contract with `source.kind:"epic"`, the parent and
resolved field under `selection`, and `epic.parent`/`epic.relation` under each
row's namespaced context. Defaults are
`key,summary,status,issuetype,assignee`; `--limit`, `--cursor`, `-o text`, and
`-o id` have the same meaning as `issue search`. `--view NAME` selects the
configured `epic_children` projection; explicit `--columns` wins. This is
read-only. The shared `--limit` range is `1..1000`; explicit 0, negatives, and
larger values fail before backend access.

## `atl jira issue create`

Create an issue. The description is either Jira wiki markup (`--from-file`) or
markdown converted to wiki (`--from-md`) — the two flags are mutually exclusive.

`--from-md` accepts the same markdown subset as the Confluence md surface
(headings, emphasis/links, bullet/numbered lists, GFM tables, fenced code,
blockquotes, `---`, `[KEY](jira:KEY)` issue links, `[~username]` mention
passthrough). Conversion is fail-closed: the first construct outside the
subset (task lists, images, emphasis without word boundaries, pipes inside
table cells, …) aborts with exit 8 naming the offending block, and nothing is
sent — write those bodies as wiki markup via `--from-file` instead.
Wiki-active characters in plain text (`{`, `[`, `!`, toggle chars in opening
position) are backslash-escaped automatically, so ordinary prose survives
verbatim. The same flag exists on `update` and `comment add`.

```bash
atl jira issue create \
  --project PROJ \
  --type Bug \
  --summary "Crash on empty input" \
  --from-file description.wiki

# or author the description in markdown:
atl jira issue create \
  --project PROJ --type Bug \
  --summary "Crash on empty input" \
  --from-md description.md

# Extra fields keep legacy string behavior unless an object/array is supplied.
# Use --field-json whenever Jira expects a typed scalar such as a number,
# boolean, or null.
atl jira issue create \
  --project PROJ --type Task --summary "Deploy docs" \
  --field 'priority={"name":"High"}' \
  --field 'labels=["docs","infra"]' \
  --field customfield_10001=foo \
  --field-json customfield_10002=5

# Opt in to immediate mirror registration from the authoritative readback:
atl jira issue create \
  --project PROJ --type Task --summary "Tracked task" \
  --register --into ./mirror-jira
```

Without `--register`, issue creation and output retain their legacy remote-only
behavior. `--register --into ROOT` performs one create and one authoritative
readback of the returned key, then writes the exact readback description,
pristine base, JSON snapshot, Markdown view, and sync/view state. Registration
never treats the submitted description as the remote baseline and never adopts
or overwrites an occupied target; sync state is committed last.

If Jira is known to have created the issue but readback or local registration
fails, stdout still identifies the issue and includes
`registration.status:"not_registered"`; the command exits 8. Never repeat
`issue create`. Preserve local files and recover only the returned key with
`atl jira pull --jql 'key = NEW-1' --into ROOT --limit 1`.

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (required) |
| `--summary` | issue summary (required) |
| `--from-file` | description body file (wiki markup) or `-` for stdin |
| `--from-md` | markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable); objects/arrays are decoded, other values remain strings |
| `--field-json key=JSON` | extra explicitly typed JSON field (repeatable), including number, boolean, or `null` |
| `--register` | explicitly register the created issue in a mirror; requires non-empty `--into` |
| `--into` | mirror root for registration; requires `--register` |

## `atl jira issue update`

Update summary, description, or arbitrary fields. This replaces the whole
description; for a small targeted change prefer `jira issue edit` below.

```bash
atl jira issue update PROJ-1 --summary "Crash on empty input (critical)"
atl jira issue update PROJ-1 --from-file updated-desc.wiki
atl jira issue update PROJ-1 --from-md updated-desc.md
atl jira issue update PROJ-1 --field 'priority={"name":"Highest"}'
atl jira issue update PROJ-1 --field-json customfield_10001=5
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--summary` | new summary |
| `--from-file` | new description file (wiki markup) or `-` for stdin |
| `--from-md` | new markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable); objects/arrays are decoded, other values remain strings |
| `--field-json key=JSON` | extra explicitly typed JSON field (repeatable), including number, boolean, or `null` |

## `atl jira issue field preview` / `field set`

Preview or atomically apply one or more large custom-field values from bounded
files. The value itself never appears in argv. The dedicated `field preview`
command is GET-only, works under `ATL_READ_ONLY=1`, and fresh-reads the selected
fields plus Jira `updated`; its result supplies the `expected_updated` and
aggregate `proposal_hash` required by a later `field set --apply`. `field set`
remains mutating in the command policy even when `--apply` is omitted, so agents
should use the dedicated preview surface until the user approves the write.

```bash
# Markdown is converted fail-closed to a Jira-wiki string
ATL_READ_ONLY=1 atl jira issue field preview PROJ-1 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001

# Re-run the same command with both reviewed gates to write
atl jira issue field set PROJ-1 \
  --from-md customfield_10001=progress.md \
  --allow-fields customfield_10001 \
  --expected-updated '2026-01-02T03:04:05.000+0000' \
  --expected-proposal-hash '<proposal_hash>' --apply

# Raw preview: valid JSON objects/arrays stay structured; everything else is an exact string
ATL_READ_ONLY=1 atl jira issue field preview PROJ-1 \
  --from-file customfield_10002=option.json \
  --from-file customfield_10003=plain.txt \
  --allow-fields customfield_10002,customfield_10003
```

Only Jira fields marked custom in field metadata are accepted. Each input must
also be named in the exact `--allow-fields` policy. Use the dedicated commands
for summary, Description, labels, assignee, links, comments, and transitions.
Multiple fields are sent in one PUT. The reviewed timestamp covers the remote
issue state, while one deterministic proposal hash covers every normalized
field value independent of CLI input order and bound to the issue key (proposal
hash schema v2). A changed input file, different issue key, or stale
timestamp emits a `blocked` result and exits 8 without writing.
Already-satisfied values are a no-op after both gates pass. Jira has no
server-side CAS, so a narrow read-to-write TOCTOU window remains.

Raw parsing is deliberately small: only valid JSON whose top level is an object
or array becomes structured. JSON-looking scalars (`true`, `7`, `null`) and
malformed/object-like text stay strings. `--from-md` always produces a string,
even when its rendered Jira wiki happens to look like JSON. Aggregate input and
normalized output are each capped at 64 MiB; stdin (`FIELD=-`) may be used once.

Default JSON includes the aggregate `proposal_hash` plus each normalized
proposed `value`, its `kind`, byte size, and SHA-256. That stdout is the review artifact and may contain private issue
content. `-o text` omits values and prints only field ids, kinds, sizes, and
hashes. Values are never written to verbose request logs.

Flags:

| flag | description |
|---|---|
| `--from-file FIELD=PATH` | raw value file or stdin `-` (repeatable) |
| `--from-md FIELD=PATH` | Markdown file or stdin `-`, converted to a Jira-wiki string (repeatable) |
| `--allow-fields IDS` | exact comma-separated custom field ids authorized for this operation (required) |
| `--expected-updated VALUE` | `field set` only: reviewed Jira `updated` value; required with `--apply` |
| `--expected-proposal-hash HASH` | `field set` only: reviewed aggregate proposal hash; required with `--apply` |
| `--apply` | `field set` only: perform the guarded write; without it `field set` also previews, but remains classified as mutating |

## `atl jira issue edit`

Targeted description edit in one command: fetch the current description,
replace `--old` with `--new` (the same whitespace/invisible-tolerant matcher
as `conf edit`), and write the result back. Small fixes and
insert-after-anchor edits skip the get → compose → update ceremony.

```bash
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'
# insert a section by replacing an anchor heading with new text + the anchor
atl jira issue edit PROJ-1 --old 'h2. Verify' \
  --new $'h2. Rollback\n\nRestore the previous snapshot.\n\nh2. Verify'
atl jira issue edit PROJ-1 --old 'obsolete paragraph' --new ''   # delete
atl jira issue edit PROJ-1 --old 'foo' --new 'bar' --dry-run     # preview only
```

The match must be unique unless `--all` is passed: ambiguous → exit 2, no
match → exit 4 with a quoted region dump showing the closest candidate (and
any hidden bytes that broke exact matching). An empty description is exit 4 —
set one with `issue update`. A whitespace-tolerant match that would cross a
line break is refused with exit 8: Jira wiki is line-sensitive (`h2.`, `*`,
`{code}` are line-start tokens), so a cross-line splice could silently merge
lines — copy `--old` exactly, newlines included. Replacement text is native wiki markup, spliced
verbatim; for a full markdown rewrite use `issue update --from-md` instead.

Jira DC updates are last-writer-wins (no version gate), so the `--old` match
doubles as the drift guard: if the description changed underneath, the needle
misses and the command refuses instead of overwriting.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--old` | text to find in the description (required; must be non-empty) |
| `--new` | replacement wiki text (required; pass `--new ''` to delete the match) |
| `--old-file` / `--new-file` | read either side from a file (`-` for stdin); one trailing newline is stripped |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match and regions without updating the issue |

## `atl jira issue transition`

Preview or apply one reviewed workflow transition. The dedicated `preview`
subcommand is GET-only and works under global read-only policy; the parent
transition command is classified as mutating even during its default dry-run.
Transition names are matched case-insensitively against the live transition
list before target-status matches. A match must be unique.

```bash
ATL_READ_ONLY=1 atl jira issue transition preview PROJ-1 --to "In Progress"
atl jira issue transition PROJ-1 --to Done --comment "Deployed to staging."
# Review proposal_hash, then repeat the exact request once:
atl jira issue transition PROJ-1 --to Done --comment "Deployed to staging." \
  --apply --expected-proposal-hash <hash>
```

The proposal binds the canonical issue identity, current status/update marker,
the uniquely selected exact transition, and exact current/proposed values for
every requested field. An optional comment also binds the exact Jira-wiki bytes,
authenticated actor, and complete comment-id baseline. Apply reconstructs all
relevant state immediately before at most one POST and then reads it back. A
definitive rejection is `not_applied`; unsafe committed or ambiguous outcomes
are `conflict` or `unverifiable` and must never be replayed automatically.
Already matching the target status is not idempotency because transitions may
have workflow side effects.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | target status or transition name (required) |
| `--comment` | optional non-empty Jira-wiki comment to post with the transition |
| `--field key=value` | field to set on the transition (repeatable); objects/arrays are decoded, e.g. `resolution={"name":"Fixed"}` |
| `--field-json key=JSON` | explicitly typed JSON field to set on the transition (repeatable), including number, boolean, or `null` |
| `--apply` | perform the exact reviewed transition (default: dry-run) |
| `--expected-proposal-hash` | exact hash emitted by the matching preview/dry-run (required with `--apply`) |

## `atl jira issue assign`

Set or clear the issue assignee via the dedicated assignee endpoint. Exactly one
of `--to`, `--me`, `--none` is required (else exit 2). `--me` resolves the
authenticated user's DC username first.

```bash
atl jira issue assign PROJ-1 --me            # take the ticket
atl jira issue assign PROJ-1 --to jdoe       # hand it to a DC username
atl jira issue assign PROJ-1 --none          # unassign
```

→ `{ "key": "PROJ-1", "status": "assigned", "assignee": "jdoe" }` (`"unassigned"`
and an empty `assignee` with `--none`).

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | DC username to assign the issue to |
| `--me` | assign to the authenticated user |
| `--none` | remove the assignee |

Find usernames with `atl jira user search '<name>'`; `--field assignee=<name>`
on `update` does **not** work (Jira DC expects an object there — use `assign`,
or `--field 'assignee={"name":"jdoe"}'`).

## `atl jira issue comment {preview,add,list,delete}`

Read or safely append Jira wiki comments. `preview` is a GET-only command that
works under the process-wide read-only policy. `add` uses the same proposal but
is dry-run by default; it writes only with both `--apply` and the exact reviewed
`--expected-proposal-hash`.

```bash
ATL_READ_ONLY=1 atl jira issue comment preview PROJ-1 --from-md note.md
atl jira issue comment add PROJ-1 --from-md note.md  # same dry-run, classified as mutating
atl jira issue comment add PROJ-1 --from-md note.md --apply \
  --expected-proposal-hash <hash-from-preview>
atl jira issue comment list PROJ-1                 # {key, comments:[{id,author,created,body}]}; -o id → ids
atl jira issue comment delete PROJ-1 <COMMENT-ID>  # see the id from `comment list`
```

Comment listing fails closed (exit 8) whenever a complete, stable listing
cannot be proven: for example, after the defensive page guard, a changed total,
an unexpected offset, inconsistent metadata, or a no-progress page. No partial
list is emitted or used for a proposal baseline. Preview validates unique,
non-empty comment ids and binds their sorted set, the exact normalized native
body, target, and authenticated Data Center identity into the proposal hash.
An existing identical body does not make append idempotent.

Apply reconstructs the proposal and re-reads the complete baseline immediately
before one non-retried POST. A changed body, identity, or baseline blocks before
the write. Successful or transport-ambiguous outcomes get one complete
readback. The closed statuses are `would_apply`, `applied`, `not_applied`,
`conflict`, and `unverifiable`; the latter two are non-zero where replay safety
is not proven. Never automatically replay an ambiguous comment POST. Default
JSON includes the reviewed body and may therefore be private; `-o text` emits
only hashes and byte counts.

Flags (`preview` and `add`):

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--from-file` | comment body file (wiki markup) or `-` for stdin (default stdin) |
| `--from-md` | markdown comment file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--apply` | `add` only: send one guarded POST (default is dry-run) |
| `--expected-proposal-hash` | `add` only: exact reviewed hash, required with `--apply` |

## `atl jira issue link {add,list,delete,suggest}`

Manage typed links between issues. `link` is a subcommand group.

```bash
atl jira issue link add PROJ-1 --to PROJ-2 --type blocks
atl jira issue link add PROJ-3 --to PROJ-1 --type "is cloned by"
atl jira issue link list PROJ-1                    # {key, links:[{id,direction,type,type_name,key}]}; -o id → link ids
atl jira issue link delete <LINK-ID>               # see the id from `link list`
atl jira issue link suggest --csv links.csv         # dry-run missing-link candidates only
```

Flags (`add`):

| flag | description |
|---|---|
| `PROJ-1` | source issue key (positional, required) |
| `--to` | target issue key (required) |
| `--type` | link type name (required; see `atl jira link-types`) |

`suggest` is read-only. It expects a reviewed CSV plan with `source`, `target`,
`type`, and optional `rationale` columns. Common aliases such as `from`, `to`,
`link_type`, and `reason` are accepted. For each source issue, it reads current
outward Jira links and emits only plan rows that are still missing:

```csv
source,target,type,rationale
PROJ-1,PROJ-2,Blocks,dependency found during review
```

## `atl jira issue link-epic`

Set the Epic Link custom field on an issue (classic Jira Data Center boards).

```bash
atl jira issue link-epic PROJ-42 --epic PROJ-1
```

The command uses `render.jira.epic_field` when configured (technical id or
exact display name); otherwise it resolves the conventional `Epic Link` field.
Resolution happens before the existing target authorization and single-attempt
PUT. This keeps writes aligned with instances whose Epic Link field was renamed.

Flags:

| flag | description |
|---|---|
| `PROJ-42` | child issue key (positional, required) |
| `--epic` | epic issue key (required) |

## `atl jira issue plan apply`

Preview or apply a guarded CSV operation plan. The default mode is **dry-run**:
the command re-reads current Jira state, reports `would_apply`,
`already_satisfied`, `blocked`, `failed`, or fail-fast `skipped` rows, and
performs no writes. A blocked/failed plan still emits its JSON audit result but
exits 8. Write mode requires both `--apply` and `--confirm APPLY`.

```bash
atl jira issue plan apply --csv plan.csv
atl jira issue plan apply --csv plan.csv --allow-ops link,label_add --apply --confirm APPLY
atl jira issue plan apply --csv plan.csv --continue-on-error # still exits 8 if any row fails
```

CSV columns:

| column | description |
|---|---|
| `version` | required plan schema version; currently `1` |
| `op` | `link`, `label_add`, `label_remove`, `comment`, or `field` |
| `source` | issue key to read/write |
| `target` | target issue key for `link` |
| `type` | Jira link type for `link` |
| `field` | field id/name for `field` |
| `value` | label(s), comment body, or field value |
| `rationale` | optional audit note |
| `expected_updated` | required Jira `updated` value captured during review; a mismatch blocks the row |

Flags:

| flag | description |
|---|---|
| `--csv` | operation plan CSV (required) |
| `--allow-ops` | comma-separated allowed operations (default `link`) |
| `--allow-fields` | comma-separated field ids/names allowed for `field` operations |
| `--allow-link-types` | explicit link-type exceptions when a type is absent from live Jira metadata |
| `--continue-on-error` | continue independent rows after failures; final exit remains 8 |
| `--apply` | perform writes instead of dry-run |
| `--confirm` | must be exactly `APPLY` when `--apply` is set |

The complete plan schema and live link-type metadata are validated before
writes. Execution is fail-fast by default; remaining rows are reported as
`skipped`. Every non-noop row re-reads the source issue and compares
`expected_updated` immediately before its write. Schema version 1 permits only
one mutating row per source issue; split dependent changes into separately
reviewed plans. Structured `field` values use semantic JSON comparison: object
key order and server-added object properties do not cause repeat updates, while
arrays retain reviewed order. Invalid JSON-looking text remains a string, as in
ordinary `--field` handling.

## `atl jira issue attachment {list,get,upload}`

List, download, or upload issue attachments. `get` accepts either the attachment
id or the filename in `--id`; server-provided filenames are reduced to a safe
basename before writing to the target directory.

```bash
atl jira issue attachment list PROJ-1                    # {key, attachments:[...]}; -o id → ids
atl jira issue attachment get PROJ-1 --id 42 --into ./attachments
atl jira issue attachment get PROJ-1 --id spec.xlsx
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--id` | attachment id or filename (`get`, required) |
| `--into` | output directory (`get`, default `.`) |
| `--file` | local file path (`upload`, required) |

An `upload` whose successful backend response is malformed JSON or carries no
attachment exits `8`.

## `atl jira issue images`

Download image attachments of an issue to files (useful for agent vision).

```bash
atl jira issue images PROJ-1
atl jira issue images PROJ-1 --into /tmp/proj1-images
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--into` | output directory (default `mirror-jira/<KEY>.assets/`) |

## `atl jira issue history`

Show an issue's changelog (who changed what, when) with explicit completeness
and source metadata. `atl` prefers the paginated Data Center changelog endpoint;
older instances fall back to `?expand=changelog`. An embedded result is marked
complete only when Jira returns paging metadata proving that every advertised
entry is present.

```bash
atl jira issue history PROJ-1
atl jira issue history PROJ-1 --field "Delivery Notes" --since 2026-04-01
atl jira issue history PROJ-1 --field status --until 2026-06-30T23:59:59Z
atl jira issue history PROJ-1 --summary-only
```

Repeatable `--field` accepts an exact id or case-insensitive display name and
fails closed on ambiguous names. `--since` and `--until` accept `YYYY-MM-DD`,
RFC3339, or Jira datetime values. Date boundaries are inclusive: an `--until`
date includes that entire calendar day in the observed Jira current-user IANA
timezone. Atl reads that preference once per top-level command, fails closed if
it is missing/invalid, and reports `boundary_time_zone`, its source, plus
canonical `since_instant` / `until_exclusive_instant`. Midnight gaps and folds
are resolved from the first and last real instants belonging to that civil
date, rather than normalizing a nonexistent `00:00`; this prevents an inclusive
end date from omitting evidence. A fully skipped requested date is exit 8. This
resolution is local and does not add another metadata or search request. An
explicit-offset RFC3339 boundary is already absolute, performs no timezone metadata GET, and
leaves the boundary-zone fields absent. Jira's compatible changelog APIs do not
provide these filters, so `atl` first reads the qualified snapshot and then
filters locally; `fetched` and `total` describe the pre-filter read, while
`count` describes matching history entries. Raw JQL is never rewritten.

JSON includes `complete`, `source`, optional `partial_reason`, field ids beside
display names, and `last_changes` for selected fields within the requested time
window. The additive `summary` object computes deterministic entry/item totals,
non-empty metadata counts, distinct and per-field buckets, status changes, id
uniqueness, separate missing-id and non-empty-id uniqueness facts, count/fetch
reconciliation, and chronological ordering from that
same filtered array without another request. If any timestamp is not
comparable, `chronological_comparable` is false and
`chronological_ascending` is `null`. `fetched_matches_total:true` does not
replace the top-level completeness qualification. Treat `complete:false` as
incomplete evidence rather than absence of a change. If a matching selected
change has an unsupported server timestamp, atl returns exit 8 because it
cannot order `last_changes` safely. `-o text` renders a completeness line
followed by an escaped Markdown table.

`--summary-only` performs the same qualified read and filtering but omits the
raw top-level `history` member. JSON keeps `key`, provenance/completeness,
source totals, `filters`, `summary`, and selected-field `last_changes`; it does
not add another backend request. Its text form contains bounded deterministic
facts, field buckets, and any selected-field last changes rather than raw
changelog rows. Without this flag, the JSON and text contracts are unchanged.
An explicit false form such as `--summary-only=false` is a usage error before
backend access. Omit the flag when raw history is intentionally required.

## `atl jira epic digest`

Aggregate the dated evidence commonly needed for an epic/quarter analysis
without generating subjective management prose:

For the decision flow around unfamiliar issues, several keys, and linked
Confluence evidence, see the evidence-first recipe in
[agent-recipes.md](../../agent-recipes.md#analyze-jira-evidence-without-manual-joins).

```bash
atl jira epic digest PROJ-1 --quarter 2026-Q2 \
  --status-field 'Delivery Notes' --dod-field 'Definition of Done' \
  --projection compact
atl jira epic digest PROJ-1 --since 2026-04-01 --until 2026-06-30 \
  --epic-field customfield_10001 --include identity,children,comments,history,refs
atl jira epic digest PROJ-1 --quarter 2026-Q2 --status-field customfield_10002 \
  --expand-confluence 2 --confluence-heading 'Metrics'
```

`--quarter YYYY-Q1..Q4` maps to inclusive calendar dates in the observed Jira
current-user timezone and conflicts with explicit `--since/--until`, which must
be supplied together. The resolved period includes the IANA zone/source and
canonical UTC instants. Digest reuses that single lookup for nested history;
it does not fetch the preference twice. Explicit-offset RFC3339 bounds need no
lookup. The default include
set is `identity,status-field,children,comments,links,history,refs`; repeat
`--include` or pass a comma list to narrow it. Identity is always present.
Status/DoD/Epic Link selectors accept exact ids or display names and never guess
a company-specific narrative field.

The schema-v1 JSON contains `period`, sorted `includes`, a `sources` map,
`epic`, optional `status_field`/`dod_field`, a common IssueList under `children`
plus `by_status` and dated updates, bounded newest comments/history, links and
blockers, artifact refs, optional Confluence sections, and `staleness`.
History and comments are filtered to the selected period; current child rows
remain visible while `updated_in_period` and staleness apply the period boundary.
Staleness is explainable evidence: the selected status-field change time,
latest newer evidence time, newer child/comment counts, and textual reasons —
not an opaque score or generated conclusion.

`--projection compact` keeps the same qualified evidence read and returns an
app-layer bounded projection for agent synthesis. It preserves `sources`,
counts, count/text truncation, warnings, staleness, status/DoD evidence and
small deterministic samples/summaries, while omitting raw child rows and raw
collections. `projection.omitted` names removed paths and
`projection.clipped` names values clipped by the tighter projection budget.
Expand a required clipped narrative with `jira issue field get`; do not repeat
the broad digest in `full`. Use another source-specific bounded command when a
different omitted collection detail is actually required. Compact JSON is bounded to 64 KiB by regression fixtures at
the command's existing source caps; it does not turn incomplete evidence into
complete evidence.

Every component declares `complete`, `count`, optional machine-readable
`count_truncated`/`text_truncated`, and a bounded warning.
Defaults/caps are 1000 children, 50 comments, 500 history entries, 128 KiB per
large text value, and 10 Confluence expansions. A source failure remains visible
and does not become proof of absence. `refs.complete` includes the completeness
of every contributing description, selected status/DoD field, and comment
source. Links have a canonical total order. Confluence expansion additionally
requires an exact heading; it scans at most 50 refs, accepts only the safe same-origin
references supported by `conf page resolve`, and reuses bounded `page section`.
`-o text` is a compact evidence overview, not a management summary.

`--child-limit`, `--comment-limit`, and `--history-limit` override the first
three collection bounds respectively. Zero keeps the documented default,
positive values are capped at 1000/50/500, and negative values are rejected.

## `atl jira issue labels`

Add and/or remove labels without clobbering labels set by others (uses the
field-update verb).

```bash
atl jira issue labels PROJ-1 --add bug,backend [--remove wontfix]
```

Flags: `--add` / `--remove` (comma-separated; at least one required, else exit 2).

## `atl jira issue watchers list|add|remove`

Read complete Jira Data Center watcher membership or preview one guarded
membership change:

```bash
atl jira issue watchers list PROJ-1
atl jira issue watchers add PROJ-1 --username alice
atl jira issue watchers remove PROJ-1 --me --apply \
  --expected-proposal-hash <hash-from-preview>
```

Choose exactly one of `--username` and `--me`. The latter resolves
`/rest/api/2/myself` and requires a non-empty Data Center `name`; it never
substitutes a Cloud account id. Usernames are trimmed, bounded to 255 bytes,
and rejected on control/invisible characters. The preview hash binds issue key,
operation, resolved username, and the complete sorted watcher membership. Apply
re-reads membership and requires the same hash even when already satisfied.

The Jira watcher endpoint is not paginated. `watchCount` must equal the number
of returned named identities; otherwise list emits `complete:false`,
`truncated:true`, and a stderr warning, while mutation fails closed. POST uses
Jira DC's raw JSON-string body and DELETE uses an encoded `username` query.
Each write is sent once and followed by a verification GET. Verified state is
`applied`; ambiguous/partial state is `unknown` with a non-zero exit and must
not be replayed automatically.

## `atl jira issue worklog list|add`

Read the complete Jira Data Center worklog history or add one reviewed time
entry without changing the remaining estimate:

```bash
atl jira issue worklog list PROJ-1 -o text
atl jira issue worklog add PROJ-1 --time 1h30m \
  --started 2026-07-13T09:00:00Z --from-file worklog.txt
atl jira issue worklog add PROJ-1 --time 1h30m \
  --started 2026-07-13T09:00:00Z --from-file worklog.txt --apply \
  --expected-proposal-hash <hash-from-preview>
```

`list` consumes every page advertised by Jira and fails with exit 8 on missing,
changing, or structurally inconsistent pagination; it never returns an
unmarked prefix. JSON authors are a compact projection (`name`, `key`, display
name, active) without email, avatar, self URL, or timezone. `-o text` is an
escaped Markdown table; `-o id` prints worklog ids.

`add` accepts positive integer `h`, `m`, and `s` segments (`1h30m`, `90m`,
`45s`). Days and weeks are rejected because their conversion depends on Jira
instance settings. `--started`, when present, must be RFC3339 with an explicit
timezone. The optional comment comes from either `--comment` or a bounded
`--from-file FILE|-`; prefer the file form because inline text is visible in
the process list.

The default is a read-only preview that normalizes the duration and start time,
shows the compact current identity and payload, and binds them with a proposal
hash. The result also exposes `baseline_sha256`, a value-free digest of the
complete sorted worklog-id set. Proposal schema v2 binds that digest, so any
intervening add (including a committed write behind an ambiguous response)
changes the reviewed hash and blocks before POST. `--apply` requires the exact
proposal hash, re-reads a complete baseline, and
sends one POST with `adjustEstimate=leave`. A timeout/transport/5xx result is
never retried: atl performs one complete reconciliation read and reports
`applied` only when an explicit `--started` value and exactly one new matching
entry prove the result. Without that timestamp, an ambiguous response remains
`unknown` even if a similar entry appears, because Jira chose its start time.
Every `unknown` exit is non-zero and agents must not replay it automatically.

## `atl jira issue check`

Audit that required/important fields are populated — a CI / pre-transition gate.
Exits **8** (`ErrCheckFailed`) when a `--require` field is empty (distinct from a
transport/auth error), after emitting the report on stdout.

```bash
atl jira issue check PROJ-1 --require assignee,fixVersions [--warn priority]
```

`--warn` defaults to `assignee,priority,components,fixVersions,description`; pass
`--warn ""` to opt out of warnings. A check that would audit nothing (no
`--require` and `--warn ""`) is a usage error (exit 2).

## `atl jira issue delete`

Preview or apply one permanent issue deletion. Jira Data Center has **no trash**
for issues, so preview is the default and apply requires the exact reviewed
freshness marker and proposal hash.

```bash
atl jira issue delete PROJ-1
atl jira issue delete PROJ-1 \
  --apply --confirm DELETE \
  --expected-updated '2026-08-02T20:00:00.000+0000' \
  --expected-proposal-hash '<hash from preview>'
```

The proposal binds the normalized backend identity, canonical requested/current
key, immutable numeric issue id, exact `updated` value, the complete
permission-relative direct-subtask id/key array, and `--delete-subtasks` intent.
If subtasks exist, a preview without `--delete-subtasks` is blocked; rerun the
preview with that flag only when permanent cascade deletion is intended. Apply
repeats the exact read immediately before one DELETE addressed by numeric id.

Jira has no delete-time CAS, tombstone, or positive readback resource. Therefore
only an acknowledged DELETE followed by exact numeric-id `404` is reported as
`applied`. Any ambiguous response remains `outcome_unknown` even if readback is
permission-relative absent. Never retry it automatically. The old `--force`
direct-write form is no longer supported.

The whole destructive `jira issue delete` leaf is mutation-classified, including
its GET-only preview, so `ATL_READ_ONLY=1` blocks both forms before credentials
or network. Enter an explicitly approved deletion workflow before removing that
policy for the preview; keep the reviewed output, run at most the one exact apply,
then restore the policy immediately.
