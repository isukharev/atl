# Confluence Storage Format (CSF) and fragments

## What the `.csf` is

`.csf` holds the page body in **Confluence Storage Format** — an XHTML-derived format with macros.
`atl` treats it as **byte-stable**: it parses CSF read-only and never re-serializes your bytes, so
editing the `.csf` directly is safe and lossless. This is why there is no Markdown round-trip — a
Markdown conversion would silently drop macros and structure.

Edit the XML/macro bytes directly. Keep tags balanced and entities well-formed; `atl conf validate`
checks well-formedness and reports problems as `{severity, line, col, rule, message}` (treat any
`severity: "error"` as a hard block).

## Fragments (`.meta.json`)

Opaque elements in the body are surfaced in the page's `.meta.json` under `fragments[]`, each:

```json
{ "kind": "...", "key": "...", "display": "...", "asset": "...", "params": { } }
```

- `kind`: `drawio` | `user` | `attachment` | `page-link` | `image`
- `key`: raw backend key (diagram name, user account-id, filename, page title)
- `display`: resolved human-readable name (or the raw key if resolution failed)
- `asset`: relative path to a fetched render (for `drawio` / `image`, only with `--assets`)
- `params`: handler-specific (e.g. drawio `{ "diagramName": "...", "revision": "2" }`)

`.meta.json` is auto-managed — read it for context, don't hand-edit it.

## Assets and vision

`atl conf pull --assets` downloads draw.io diagrams (rendered to PNG at the exact revision) and
inline images into `<page-slug>.assets/`. Open those images when you need to understand a diagram
or screenshot before editing the surrounding `.csf`.

## Fragment safety on edit

When you change the body, `atl` diffs fragments against the pristine baseline and reports
`added_fragments` / `removed_fragments` in the push (and dry-run) output. If a dry-run shows a
`removed_fragments` entry you didn't intend (e.g. you accidentally deleted a macro or diagram),
stop and fix the `.csf` before pushing.
