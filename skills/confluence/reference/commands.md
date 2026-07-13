<!-- Generated from skills-src/confluence/reference/commands.md — edit the source and run 'make gen-plugins'. -->
# Confluence command and read/reference inventory

Load this reference for discovery, output layout, rendering, status, bulk reads,
or an exact command/flag. Return to the main skill for any write sequence.

## Discovery and read modes

```bash
atl conf search --cql '<CQL>' --limit 25
atl conf search --space <KEY> --title '<substring>' --type page --limit 25
atl conf space tree --space <KEY> [--depth N]
atl conf page list --space <KEY> [--status current|archived|trashed]
atl conf page resolve <id-or-same-origin-url> -o id
atl conf page outline <id-or-same-origin-url>
atl conf page section <id-or-same-origin-url> --heading '<exact>' -o text
atl conf page view <id-or-same-origin-url> --jira-view default -o text
atl conf page view <id-or-same-origin-url> --jira-macros off -o text # untrusted/heavy page: placeholders only
```

Do not combine `--cql` with convenience filters. Search requires CQL or at
least one of `--space/--title/--label/--type`; paginate with `--cursor`.

Use transient `page view` only for one-off readonly work. For a mirror:

```bash
atl conf pull --id <id> --assets --comments --jira-view default --into <root>
# alternatives: --cql '<CQL>' or --space <KEY> [--depth N]
```

Read-only page selectors accept stable ids, same-origin canonical/viewpage/REST
URLs, exact display URLs, and one `/x/` redirect. Resolve once when several
commands share a reference. Never send foreign URLs or replace exact resolution
with fuzzy title search; ambiguity is exit 8.

For long pages, outline before reading. Section selection is exact after
case/whitespace normalization, includes descendant headings, and requires
`--occurrence N` for duplicates. Check `complete`; `--max-bytes` truncates only
at whole rendered blocks and a truncated section is not complete evidence.

`--cql` caps at 1000 pages; `--space` and tree cap at 2000. A capped result has
`truncated:true` and a stderr warning. Narrow the selection; never treat it as
complete.

## Mirror layout

```text
<root>/<SPACE>/<ancestors…>/<page-slug>/
    <page-slug>.csf           native write substrate
    <page-slug>.md            derived staging view
    <page-slug>.meta.json     auto-managed metadata/fragments
    <page-slug>.comments.json optional readonly sidecar
    <page-slug>.comments.md   optional derived comment view
    <page-slug>.jira-macros.json optional readonly Jira IssueList snapshots
    <page-slug>.assets/       optional diagram/image renders
<root>/.atl/                  baseline/view state; never edit or commit
```

Sibling slug collisions use an id-suffixed directory; nothing is overwritten.
Comment sidecars do not affect dirty/drift/push. A pull without `--comments`
leaves existing sidecars untouched.

## Render configuration

`minimal/default` render body plus generated boundaries. `full` adds readonly
typed metadata and comments when available. Per-run flags:

```bash
--render-profile full
--render-include page_fields,comments
--jira-macros auto|off # page view/pull only; off means no Jira credential read/search
--render-exclude <section>
```

Persist with `atl config set render.confluence.*`. Re-render offline:

```bash
atl conf render <mirror-or-page.csf> --render-profile full
```

`render.confluence.page_fields` is a JSON descriptor array. Closed ids:
`title`, `space`, `version`, `parent`, `ancestors`, `labels`, `restricted`,
`updated`; placements `metadata|section`; formats `scalar`, list for list
fields, and `date|datetime` for updated. Fields are readonly. `restricted` is
fetched only when selected; unknown offline is never shown as unrestricted.
View v2 removed legacy YAML `frontmatter`; use `page_fields`. A stale include is
ignored with the normal unknown-section warning, so render or pull again after
updating the config and before editing.

Jira JQL macros retain their placeholder in the editable body and render
resolved IssueList tables in readonly `# Jira Queries`. Macro-defined columns
win; otherwise `--jira-view <name>` chooses the configured
`jira_list_views.<name>.confluence_macro` projection. Pull records the result in
the typed sidecar for byte-stable offline render/apply. Missing Jira access or a
failed query warns and keeps the placeholder. Never edit a generated macro
table as page body. Treat the result as partial when the command warns: one page
is capped at 20 JQL macros and 2000 aggregate rows (1000 per macro).
For an untrusted or unexpectedly heavy page, use `--jira-macros off`; persist
the global-only preference with `config set render.confluence.jira_macros off`.
Mirror-local config cannot enable authenticated Jira reads. Do not pair
`--jira-view` with the opt-out. Push refresh preserves a recorded query suffix.
If a corrupt/stale sidecar blocks recovery, remove only the generated
`.jira-macros.json` file and pull again; intentional loss-approved macro removal
retires it automatically. A successful push that changes the native macro set
also retires the obsolete snapshot and warns the agent to re-pull before
relying on refreshed Jira query rows.

Pull/render record the exact resolved section and typed-field descriptors in
`.atl/state.json.views`. `conf apply` reproduces that recorded pristine view;
ambient config changes do not silently redefine generated/read-only regions.
If a pre-version mirror lacks view state, preserve edits outside `.md`, render
that exact page/root once, then reapply the reviewed patch.

## Status and outputs

```bash
atl conf status <root> --remote
atl conf validate <page.csf>
```

Remote status makes one request per page. Omit `--remote` for local-only state.
All commands default to JSON. `-o text` selects an explicitly registered human
projection; unsupported text requests fail before network access rather than
falling back to JSON. Commands with an ID projection accept `-o id` for one
identifier per line.

## Command inventory

| Command | Purpose | Key flags |
|---|---|---|
| `conf search` | Find pages | `--cql` or convenience filters, `--limit`, `--cursor` |
| `conf space tree` | Space hierarchy | `--space`, `--depth` |
| `conf page resolve|outline|section|list|get|view|meta|history|open` | Reference resolution and page reads | outline before long reads; section uses exact heading/occurrence/byte cap; view supports `--jira-view`, `--jira-macros` |
| `conf page labels list <ID>` | Complete page-label read | no write; inspect `complete` |
| `conf page labels add\|remove <ID> <LABEL>...` | Guarded label preview/apply | `--apply`, `--expected-proposal-hash` |
| `conf page title set <ID>` | Guarded title preview/apply | `--from-file`, `--apply`, expected gates |
| `conf page move <ID>` | Guarded move preview/apply | `--parent`, `--apply`, expected gates |
| `conf page create|copy|delete` | Page lifecycle | command-specific title/space/parent/file flags |
| `conf blog create` | Create one native blog post | `--space`, `--title`, one body source; `-o text/id` |
| `conf pull` | Mirror pages | selector, `--assets`, `--comments`, `--jira-view`, `--jira-macros`, `--into`, render flags |
| `conf render` | Regenerate Markdown offline | path, render flags, `--into` |
| `conf status` | Dirty/drift state | path, `--remote` |
| `conf apply` | Merge Markdown to CSF | page md, `--dry-run`, `--allow-fragment-loss`, `--into` |
| `conf edit` | Tolerant local byte splice | `--old/--new`, file variants, `--all`, `--dry-run` |
| `conf validate` | Validate CSF | file |
| `conf push` | Version-gated write | file/dir, `--dry-run`, `--force`, `--into` |
| `conf comment list|add` | Comment reads/writes | page id, CSF file |
| `conf attachment list|get|upload|delete` | Attachments | page/id/name/version/file/into |
| `conf table extract` | Table export | selector, `--format`, `--raw-csv` |
| `conf me` | Authenticated user | none |

CSV table export neutralizes spreadsheet-formula-leading cells by default.
Use `--raw-csv` only for a trusted non-spreadsheet consumer.

## Profile learning

Load only `schema`, `selectors`, or `render_defaults` with `--service
confluence`. Repeated space/selector/render discoveries go through the
onboarding skill's `profile suggest → suggestion review → apply|reject`
flow. Revalidate stale space facts. Applying memory never changes runtime;
runtime render/root sync needs separate approval and verification.
