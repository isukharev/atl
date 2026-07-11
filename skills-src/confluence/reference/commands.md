# Confluence command and read/reference inventory

Load this reference for discovery, output layout, rendering, status, bulk reads,
or an exact command/flag. Return to the main skill for any write sequence.

## Discovery and read modes

```bash
atl conf search --cql '<CQL>' --limit 25
atl conf search --space <KEY> --title '<substring>' --type page --limit 25
atl conf space tree --space <KEY> [--depth N]
atl conf page list --space <KEY> [--status current|archived|trashed]
atl conf page view <id> -o text
```

Do not combine `--cql` with convenience filters. Search requires CQL or at
least one of `--space/--title/--label/--type`; paginate with `--cursor`.

Use transient `page view` only for one-off readonly work. For a mirror:

```bash
atl conf pull --id <id> --assets --comments --into <root>
# alternatives: --cql '<CQL>' or --space <KEY> [--depth N]
```

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
Legacy explicit `frontmatter` remains readable, but new configs use
`page_fields`.

## Status and outputs

```bash
atl conf status <root> --remote
atl conf validate <page.csf>
```

Remote status makes one request per page. Omit `--remote` for local-only state.
All commands default to JSON. `-o text` selects human output; commands with an
ID projection accept `-o id` for one identifier per line.

## Command inventory

| Command | Purpose | Key flags |
|---|---|---|
| `conf search` | Find pages | `--cql` or convenience filters, `--limit`, `--cursor` |
| `conf space tree` | Space hierarchy | `--space`, `--depth` |
| `conf page list|get|view|meta|history|open` | Page reads | command-specific id/format/render flags |
| `conf page title set <ID>` | Guarded title preview/apply | `--from-file`, `--apply`, expected gates |
| `conf page move <ID>` | Guarded move preview/apply | `--parent`, `--apply`, expected gates |
| `conf page create|copy|delete` | Page lifecycle | command-specific title/space/parent/file flags |
| `conf pull` | Mirror pages | selector, `--assets`, `--comments`, `--into`, render flags |
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
