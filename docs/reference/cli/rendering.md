# Derived-view rendering

Profiles and offline regeneration for derived Jira and Confluence Markdown views.

[Reference index](README.md) · [Documentation home](../../README.md)

## Render profiles

The `.md` files in a mirror are derived staging views regenerated from the
native substrate (`.csf` / `.wiki`). A **profile** chooses what those views
contain. Supported body edits become real only after `conf apply` / `jira
apply`; generated metadata sections remain read-only, and pull/render may
replace the view. Profiles never affect substrate hashes or dirty/drift state.

Confluence views begin with `<!-- atl:document confluence-page v6 -->` and use
reserved metadata/body/comments/Jira-query boundaries. Pristine v5 and v4
views migrate only when the renderer for that exact marker reconstructs every
byte. Dirty v5/v4, older historical, unversioned, and unknown/future views are
preserved and refused; preserve any edits separately before rendering a
supported pristine legacy view.

| profile | Jira `.md` | Confluence `.md` |
|---|---|---|
| `minimal` | `key` + `summary` in `# Metadata`, `# Description` only | visible `# Content` boundary plus the native page body (same as `default`) |
| `default` | minimal **plus** `status`, `type`, `project`, `assignee`, `labels`, `priority`, `parent`, `# Image Attachments`, `# Links`, `# Comments` | visible `# Content` boundary plus the native page body |
| `full` | everything visible: default **plus** `reporter`, `created`/`updated`, `resolution`, `duedate`, `components`, `fix_versions`, configured `custom_fields`, `# Attachments` (non-image list), `# Subtasks`, `# Sprint` | read-only `# Metadata`, visible `# Content`, and readonly `# Comments` from the comments sidecar when present |

**Section names** (for `include`/`exclude`). Jira: `status`, `type`, `project`,
`assignee`, `labels`, `priority`, `parent`, `reporter`, `created`, `updated`,
`resolution`, `duedate`, `components`, `fix_versions`, `custom_fields`,
`attachments`, `attachments_all`, `links`, `comments`, `sprint`, `subtasks`,
`epic_children`. `epic_children` is intentionally in no profile base — including
it performs an additional bounded Jira query, so it must be enabled explicitly.
Confluence: `page_fields`, `comments`. The v2 format removed the legacy
`frontmatter` section; stale configs receive the normal unknown-section warning
and should migrate to typed `page_fields`. An unknown name is warned about on stderr
and ignored, never an error.

**Resolution order** (highest wins, merged per key): `--render-profile` /
`--render-include` / `--render-exclude` flags **>** local `.atl/config.json`
**>** global config **>** built-in `default`. `include` adds sections to the
profile base; `exclude` removes them.

`render.display_time_zone` follows local mirror config > global config > the
built-in `UTC` default. It affects only human `date`/`datetime` projections and
comment headings in derived Markdown. Date-only values stay calendar dates;
timestamp values are converted before formatting (for example
`2026-06-03 15:55 MSK`). The original API strings remain unchanged in
`.json`, `.meta.json`, and comment JSON sidecars. The process `TZ` environment
is never consulted, keeping offline render byte-stable across machines.

Confluence `page_fields` is a closed, read-only descriptor list. Configure it
globally or in a mirror-local render config:

```json
{
  "render": {
    "confluence": {
      "profile": "minimal",
      "include": ["page_fields"],
      "page_fields": [
        {"id": "title"},
        {"id": "ancestors", "placement": "section"},
        {"id": "updated", "format": "date"},
        {"id": "restricted", "show_empty": true}
      ]
    }
  }
}
```

IDs are `title`, `space`, `version`, `parent` (page id), `ancestors` (titles),
`labels`, `restricted`, and `updated`. Placement is `metadata` (default) or
`section`. Formats are `auto`, `scalar`, `list` (ancestors/labels), `date`, and
`datetime` (updated). Server-controlled labels and values are emitted as plain,
escaped Markdown text. Restriction state is an opt-in projection: it is fetched
only when a configured descriptor selects it, stored as known true/false in the
mirror, and cleared by a later pull that does not request it. Offline render
never guesses an unknown value; it warns and, with `show_empty`, prints a
re-pull-required value.

Two deliberate consequences of per-key merging:

- **List keys can be replaced, not emptied, by a higher layer.** An empty
  `include`/`exclude`/`custom_fields`/`field_views`/`page_fields` value means "not set here" and falls
  through to the lower layer — a local config or flag cannot clear a list the
  global config sets. To stop rendering a globally-configured custom field in
  one mirror, override the list with a different value, or counter it (e.g.
  `--render-exclude custom_fields`), or remove the key from the global config.
- **Profiles shape only the `.md` view.** The `<KEY>.json` snapshot keeps its
  standard field projection regardless of profile (`minimal` does not shrink
  it); `full` *widens* the pull's API request so every enabled section has its
  data, but nothing is removed for smaller profiles.

```sh
# per run
atl jira pull --jql "project=PROJ" --render-profile full
atl conf pull --id 123 --comments --render-profile full
atl jira pull --jql "project=PROJ" --render-include sprint --render-exclude comments

# persisted (see atl config set)
atl config set render.jira.profile full
atl config set --local render.jira.custom_fields customfield_10001,customfield_10002
atl config set --local render.jira.field_views '[{"id":"customfield_10003","label":"Risk Notes","placement":"section","format":"jira_wiki","editable":true}]'
atl config set --local render.jira.epic_field customfield_10004
atl config set --local render.jira.include custom_fields,epic_children
```

`custom_fields` (Jira only) lists custom field ids or exact display names to surface in the Markdown
metadata table under `full` (or when `custom_fields` is included); each renders
as a field/value row from the raw field (scalar verbatim; object via
`name`/`value`/`displayName`; array comma-joined; missing → omitted).

`field_views` is the typed alternative. Its `id` accepts a technical id or exact
display name during an online pull/view. Names resolve fail-closed and the
resolved id is recorded for byte-stable offline render/apply. Each descriptor is:

```json
{
  "id": "customfield_10003",
  "label": "Risk Notes",
  "placement": "section",
  "format": "jira_wiki",
  "show_empty": false,
  "editable": true
}
```

- `id` is the Jira API field id/key and is automatically added to pull's
  `fields=` projection.
- `label` is the metadata row label or section heading (defaults to `id`).
- `placement` is `metadata` (default) or `section`.
- `format` is `auto` (default), `scalar`, `list`, `jira_wiki`, `date`, or
  `datetime`; `jira_wiki` requires section placement and uses the same guarded
  wiki→Markdown renderer as Description. Valid `date` values normalize to
  `YYYY-MM-DD`; valid `datetime` values use a compact, minute-precision form
  such as `2026-06-03 12:55 UTC` or `2026-06-03 15:55 MSK`. Unexpected
  server values remain visible verbatim.
  A scalar with section `list` format becomes one bullet rather than an empty
  section.
- missing/empty values are omitted unless `show_empty` is true (`—` in
  metadata, `_Not set._` in a section).
- `editable` defaults to `false` and is valid only for
  `placement:"section"` + `format:"jira_wiki"`. In a pulled mirror it turns
  that field body into an apply surface; transient `jira issue view` output
  remains read-only. Missing editable fields render as an empty section so a
  value can be added.

Typed descriptors and legacy `custom_fields` render only while the
`custom_fields` section is enabled (`full` enables it; other profiles can
`include` it). A typed descriptor owns its id when both forms mention the same
field, preventing duplicate output. The raw value always remains unchanged in
`<KEY>.json`.

Editable field values are staged separately under
`.atl/pending/jira/<KEY>.json`; they never modify the raw `<KEY>.json` snapshot.
Offline render and pull overlay that explicit pending state in the derived
view. A successful guarded push refreshes the raw snapshot and removes the
pending record.

Generated Jira-owned boundaries are level-one headings (`# Metadata`,
`# Description`, and the configured/related-data sections) with stable hidden
`atl:section` markers. Headings from Jira rich text are nested one level below
their owner, so an original `h1.` becomes `##` while `h5.`/`h6.` keep their
exact level through a small hidden marker. `jira apply` uses those boundaries
and remains fail-closed if generated decorations are edited.

The beta metadata-table change removed the old descriptor `key`, renamed
`placement: frontmatter` to `placement: metadata`, and replaced old unmarked
level-two Jira boundaries. Update mirror-local configs and run `jira render`
(or pull again) before editing an existing view.

`epic_children` is an opt-in related-data section, not the built-in `subtasks`
field. On pull, atl resolves `render.jira.epic_field` lazily (or auto-detects the
field named `Epic Link` only after a page contains an epic candidate), groups
candidate keys from that main search page into one paginated JQL query, and
writes `<KEY>.epic-children.json` for known/inferred epics. With an explicitly
configured field, returned child rows identify localized/renamed epic types
without relying solely on the display name. The sidecar stores compact
key/summary/status/type/assignee rows and drives offline `jira render` through
the shared safe IssueList table renderer. The built-in `subtasks` section uses
the same embedded table shape. The
related query is capped at 1000 issues; a cap hit sets `truncated` /
`truncated_at` in sidecars, adds truncation fields to the pull result, and warns
on stderr. Re-pulling a non-epic with the section enabled removes a stale
sidecar. Browser-session-only provider panels are not queried.
Offline `jira render` warns when this section is enabled for an epic snapshot
that has no sidecar yet, or when sidecar issue/field identity no longer matches;
re-run `jira pull` to populate it.

**`apply` reproduces the view it was rendered with.** Every `pull`/`render`
records the resolved render settings in `.atl/state.json` (a `views` map).
`conf apply` / `jira apply` rebuild the pristine view from those recorded
settings — so an untouched `full`-profile `.md` applies cleanly and its generated
metadata table and generated `# Comments` section stay **read-only**. Only
Description and field sections explicitly recorded with `editable:true` are
merged/staged; editing other sections is refused with a pointer to the matching
command. No `--render-*` flags are needed on apply. To
override the recorded view: `jira apply` accepts `--render-*` flags; `conf apply`
has no render flags — re-run `conf render` with the desired settings instead
(it re-records the view). A pre-upgrade mirror that has no recorded view falls
back to the ambient config (today's behavior) — re-run `render` once to record it.

## `atl jira render` / `atl conf render`

Regenerate the `.md` views of an existing mirror **offline** — no network, no
PAT — so changing a profile does not force a re-pull.

```sh
atl jira render                       # re-render the whole mirror (default root)
atl jira render mirror-jira/PROJ/PROJ-1.md --render-profile full
atl conf render mirror --render-profile full
atl conf render mirror/DOCS/page/page.csf --render-exclude comments
```

The target is a mirror directory, a `.md`, or the substrate file (`.wiki` for
Jira, `.csf` for Confluence); the mirror root is found by walking up to the
`.atl` marker. Only `.md` files are rewritten — the `.csf`/`.wiki`/`.json`
substrate and the `pages` sync entries are never touched, so `jira status` /
`conf status` stay clean across a re-render. Each rendered view's settings,
including `display_time_zone`, are recorded in `.atl/state.json` (the `views`
map) so a later `apply` can reproduce
it. A Confluence `.csf` that fails to parse yields the same markdown-unavailable
stub as `pull`.

Jira directory render checks every existing document marker before the first
rewrite, then repeats each target check under the mutation lock. `jira pull`
also refuses to overwrite an explicit future/unknown `.md` marker before it
changes that issue's artifacts. A CRLF marker line is recognized without
normalizing the rest of the view. Malformed or unreadable Jira `.json`
snapshots are skipped with one stderr warning per path; they are never silent.

| flag | description |
|---|---|
| `--render-profile` | `minimal` \| `default` \| `full` (overrides config) |
| `--render-include` | comma-separated sections to add to the profile |
| `--render-exclude` | comma-separated sections to remove from the profile |
| `--into ROOT` | mirror root when no target argument is given |

---
