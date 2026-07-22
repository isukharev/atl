<!-- Generated from skills-src/jira/reference/mirror.md — edit the source and run 'make gen-plugins'. -->
# Jira transient reads and durable mirrors

Load this reference for one-off rendered views, durable pulls, assets, render
profiles, custom rendered fields, and mirror identity. Keep `ATL_READ_ONLY=1`
exported for every read/pull/render/status shell.

## Choose transient or durable

For a one-off read that will not be edited or cached:

```bash
export ATL_READ_ONLY=1
atl jira issue view <KEY> -o text
```

The transient view writes nothing and has no synced baseline. `--render-root`
selects presentation-only local config without writing there. If the task turns
into an edit, discard the transient output and pull fresh.

Use a mirror for editing, repeatable offline reads, raw fields, or attachments:

```bash
export ATL_READ_ONLY=1
atl jira snapshot <existing-root> --remote
# expand identities only when repair or issue selection requires them
atl jira status <existing-root> --remote
atl jira pull --jql '<narrow JQL>' --into <absolute-root> --limit 0
# add --assets, --fields, or --render-profile full only when needed
```

Do not pull over local edits. Re-pull only a clean remote-drifted mirror. An
explicit root wins; otherwise use `ATL_MIRROR_ROOT`, nearest `.atl`, then the
`mirror-jira` fallback. Existing file identity always follows its nearest
`.atl` root.

## Durable layout

```text
<root>/<PROJECT>/<KEY>.wiki
<root>/<PROJECT>/<KEY>.md
<root>/<PROJECT>/<KEY>.json
<root>/<PROJECT>/<KEY>.assets/
<root>/<PROJECT>/<KEY>.epic-children.json
```

`.wiki` is the byte-preserving native body and write substrate. `.md` is a
derived staging view regenerated on pull/render. `.json` is a raw snapshot,
never an edit surface. Sidecars/bases under `.atl` establish dirty/drift
evidence. A pre-sidecar mirror is never-synced until re-pulled.

Use `jira snapshot [ROOT] [--remote]` for the first health decision. It emits
only exact reconciled counts for local/native baselines, raw snapshots, pending
records, render markers/view state, and optional drift. It never locks,
recovers, repairs, or writes. Offline mode needs no config or PAT. Remote mode
preflights locally, then performs at most one single-attempt GET per eligible
canonical issue. Stop on exit `8`, `complete:false`, `reconciled:false`, or any
unavailable probe; use `jira status` only to identify entries for repair.

`--assets` streams image attachments into the issue asset directory and links
them from `.md`; failures are counted/warned and do not expose local paths in
the raw snapshot. Use `jira issue images` for one issue.

## Render profiles and fields

Profiles affect only `.md` and requested fields:

- `minimal`: identity plus description;
- `default`: common metadata, attachments/links/comments;
- `full`: reporter/dates/resolution/components/versions/subtasks/sprint and
  configured custom fields.

Use `--render-profile`, `--render-include`, or `--render-exclude`; render an
existing clean mirror offline after a presentation change. The recorded
resolved view, not ambient config, is used later by apply.

Human dates use `render.display_time_zone` (IANA, default UTC). It changes only
presentation, never JQL or exact snapshot timestamps. For date-bound evidence,
`atl environment inspect` reports observed/assumed time semantics without a
search. High-level history/digest date periods use the observed Jira-user zone;
explicit-offset RFC3339 bounds skip that lookup.

Prefer typed `render.jira.field_views` to legacy custom field ids. A descriptor
defines field id, label, metadata/section placement, format, emptiness, and
optional editability. Only `section` + `jira_wiki` may be editable. Generated
metadata, transient views, and raw snapshots remain readonly. Pending edited
fields live under `.atl/pending/jira/` until push succeeds.

`epic_children` is opt-in because it performs a bounded related query for pages
that need it. Its identity-checked sidecar and generated table are readonly.
Use explicit epic-field configuration for localized/nonstandard Jira types.

## Profile memory

Profile preferences, selectors, field knowledge, render defaults, and mirror
root are consent-gated memory, not active config. Compare relevant saved values
with `atl config show` from the target root. Present conflicts; obtain separate
approval before using a saved root or changing runtime config. Never use `eval`
to expand a path or silently edit shell/workspace configuration.
