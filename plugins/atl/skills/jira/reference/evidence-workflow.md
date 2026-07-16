<!-- Generated from skills-src/jira/reference/evidence-workflow.md — edit the source and run 'make gen-plugins'. -->
# Evidence-first Jira analysis

Use this workflow for status reports, quarter reviews, and questions whose
answer may live in custom fields, history, comments, child issues, or linked
Confluence pages. Stop as soon as the evidence is sufficient; do not run every
step mechanically.

## Choose the shortest path

| Situation | First command | Expand only when needed |
|---|---|---|
| One unfamiliar issue | `jira issue fields <KEY> --metadata-only` | exact bounded field get, selected history/refs, then a linked page section |
| One epic and known evidence-field names | `jira epic digest <KEY>` plus only a task-supplied period | bounded Confluence section expansion |
| One epic but unknown custom fields | `jira issue fields <KEY> --metadata-only` | exact compact fields, then one digest after choosing names/ids |
| Several known keys | `jira export --keys ... --out -` | per-key history/digest only for exceptions |
| Broad discovery | `jira issue search --columns ...` | batch export for selected keys |

## First-use epic flow

```sh
atl --read-only jira issue fields PROJ-1 --metadata-only

atl --read-only jira epic digest PROJ-1 \
  --status-field 'Delivery Notes' \
  --dod-field 'Definition of Done' \
  --projection compact
```

The first command omits values and empty fields. Choose an exact unambiguous
field name or stable id, then pass it directly to one digest; do not fetch the
same value separately first. The capability route and this reference are the
command contract, so do not probe `--help`. Add `--quarter`, `--since`, or
`--until` only when the task supplies that period (or a separately reviewed
workflow default); otherwise omit time flags rather than guessing the current
quarter. The digest joins identity, children, comments, history,
links/blockers, and refs; it does not write a management narrative. Inspect
every `sources.<name>.complete` and the dated `staleness.reasons` before drawing
a conclusion. In compact output also inspect `projection.omitted` and
`projection.clipped`. If a required `status_field.value` or `dod_field.value`
is clipped, expand only that exact field:

```sh
atl --read-only jira issue field get PROJ-1 \
  --field 'Delivery Notes' --max-bytes 16384
```

Do not rerun the whole digest with `--projection full`; use another bounded
source-specific command only when a different named omitted detail is required.
When the required sources are complete and sufficient, stop:
do not rerun the digest with `-o text`, a broader period, or alternate defaults.

If a non-epic issue needs a time-qualified field check, avoid a digest:

```sh
atl --read-only jira issue history PROJ-2 \
  --field 'Delivery Notes' --since 2026-04-01 --until 2026-06-30
atl --read-only jira issue refs PROJ-2 --fields 'Delivery Notes'
```

For `issue refs`, require top-level, selection, and per-issue `complete:true`
before treating an empty result as absence. Display names are resolved to
technical ids before the issue read. A JQL selection also performs one complete
paginated comment listing per issue, so keep the query narrow and set an
explicit `--limit`. Inspect the named description/comment/field source and
`text_truncated` when incomplete.
For history, `complete:false` means absence is unproven. Use `last_changes` for
the selected field; do not infer recency from array position. For date-only
periods, retain `boundary_time_zone` and the canonical instant fields as part of
the evidence. One current-user GET is expected per top-level history/digest
command; a digest reuses it. Midnight gaps/folds are represented by the complete
real civil-day interval, while a fully skipped date fails closed. Explicit-offset
RFC3339 bounds skip the lookup. Exit 8 on an
unsupported matching timestamp means recency is unknowable, not that no change
exists.

## Batch without shell loops

```sh
atl --read-only jira export \
  --keys PROJ-1,PROJ-2,PROJ-3 \
  --fields 'Delivery Notes,Impact' \
  --format json --out - |
  jq 'map({key, status: .fields.status.name, evidence: .fields.customfield_10001})'
```

The stdout artifact is valid only when atl exits zero. Discard a streamed
prefix after any failure. Use JSONL for larger sets and keep `--fields` narrow;
do not substitute `*all` or raw user objects.

## Expand linked Confluence evidence narrowly

Prefer outline then one exact section. A digest can do the same for a bounded
number of safe references when the heading is already known.

```sh
atl --read-only conf page resolve '<same-origin-page-or-short-url>'
atl --read-only conf page outline '<same-origin-page-or-short-url>'
atl --read-only conf page section '<same-origin-page-or-short-url>' \
  --heading 'Metrics' --max-bytes 65536 -o text

atl --read-only jira epic digest PROJ-1 --quarter 2026-Q2 \
  --status-field 'Delivery Notes' \
  --expand-confluence 1 --confluence-heading 'Metrics' --projection compact
```

The quarter in this expansion example is task-supplied. It is not a default to
infer for an undated request.

Honor section and source completeness. `refs.complete` also becomes false when
a contributing status/DoD/comment/description value was clipped; inspect
`count_truncated` and `text_truncated` before treating absence as evidence. Do
not fetch a whole long page merely to regex-slice Markdown, and never guess a
page title from an opaque short URL.

## Context and privacy discipline

- Keep JSON for reasoning and use `-o text` only for a documented human view.
- Select fields/columns and periods before increasing caps.
- Store private exports only in an owner-only ignored directory; do not paste
  issue bodies, queries, page URLs, or user records into a public repository.
- Do not enable verbose tracing for a report unless diagnostics are required;
  stderr can still describe private request structure.
- Read-only analysis never authorizes edits, comments, transitions, or mirror
  replacement. Switch to the guarded write workflow only on an explicit request.
