<!-- Generated from skills-src/jira/reference/editing.md — edit the source and run 'make gen-plugins'. -->
# Guarded Jira write workflows

Load this reference only after the user requests a Jira mutation. An inherited
`ATL_READ_ONLY=1` remains a human-decision boundary: remove it only for the exact
reviewed command after approval.

## Choose one write surface

- Small unique description replacement: `jira issue edit` with `--dry-run`;
  the exact `--old` match is its drift guard.
- Summary or whole body: fresh narrow get, then `jira issue update` using
  `--from-md` or native `--from-file`.
- One large custom field: GET-only `jira issue field preview` with files and an
  exact allowlist; after review, guarded `jira issue field set` with the exact
  updated/proposal-hash gates and `--apply`.
- Structural body or opted-in editable rich field: durable `.md` → apply →
  push.
- Wiki-only constructs or unsupported Markdown: direct `.wiki` edit → push.
- Labels, links, transitions, watchers, worklogs, comments, attachments, and
  plans: use their dedicated guarded commands, not generic field mutation.

Jira has no general server-side version gate. Read the narrow current state
immediately before a last-writer-wins update unless the selected command has a
stronger match/CAS/proposal-hash guard.

## One-shot body edits

Preview a targeted `issue edit` before applying. The unique old text doubles as
the drift check; ambiguous/no-match failures require more context, never force.
Use Markdown `--from-md` for supported headings/lists/tables/code and raw Jira
wiki `--from-file` only when intentionally authoring native markup.

For field shapes, valid options, large file-bound values, and exact
`expected_updated`/proposal hashes, use the direct `fields.md` route from the
main skill. Resolve transitions, options, link types, and DC usernames before
writing. A changed reviewed file always requires a new preview/hash.

## Markdown mirror cycle

Require first line `<!-- atl:document jira-issue v3 -->`. Preserve real edits
outside a legacy/future view before render; update atl for future markers.
Keep the existing file's nearest `.atl` root for the entire cycle.

After explicit approval to begin the write workflow:

```bash
env -u ATL_READ_ONLY atl jira apply <KEY>.md --dry-run
env -u ATL_READ_ONLY atl jira apply <KEY>.md
atl jira status <root> --remote
env -u ATL_READ_ONLY atl jira push <KEY>.wiki
env -u ATL_READ_ONLY atl jira push --apply <KEY>.wiki
```

Push is dry-run by default. Review description/field diffs, removed constructs,
candidate hashes, expected timestamps, drift, and every per-field outcome.
`jira apply` changes/stages local state only; the raw snapshot stays unchanged.
Description plus fields are sent as one typed write.

Untouched blocks preserve exact base bytes. Dropped wiki-only constructs or an
unconvertible block fail closed unless the user explicitly accepts the named
loss. Generated metadata, comments, links, image attachments, readonly fields,
sidecars, and `.atl` state are never edited through Markdown.

Description drift blocks unless a human explicitly chooses `--force` after
review. Pending-field drift blocks even with force: pull fresh, compare remote
and local proposals, then use the explicit pending rebase flow. An ambiguous
write is reconciled through a fresh end-state read and never replayed.

## Direct wiki fallback

Use `.wiki` only for unrepresentable native constructs or deliberate byte
surgery. Do not retain unapplied `.md` edits in the same cycle. A direct wiki
change makes ordinary apply refuse until push/re-pull; if fields were already
pending, explicitly review and rebind them to that exact wiki candidate before
push.

## Other guarded writes

- Comments: require a complete existing listing before POST; on ambiguity list
  again and reconcile content/author/time. Never retry from a partial listing.
- Watchers: preview resolved DC username and complete membership, then apply the
  exact proposal hash. `complete:false` blocks absence claims and writes.
- Worklogs: list completely, prefer explicit `--started` and file comments,
  review normalized duration/start/author plus the complete worklog-id baseline
  hash, then apply once. Unknown is possibly committed and never reusable.
- Links/plans: freeze exact scope, expected update times, link type metadata,
  and proposal hash before any row writes; stop/reconcile unknown outcomes.
- Attachments/create operations are non-idempotent: do not automatically retry
  a transport-ambiguous POST.

Mirror mutations share a persistent lock and may share `.atl/state.json` with
Confluence. Wait on contention. Never delete locks or infer clean state from a
partial scan, missing native body, corrupt sidecar, or failed refresh.
