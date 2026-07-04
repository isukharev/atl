# Confluence Storage Format (CSF) and fragments

## What the `.csf` is

`.csf` holds the page body in **Confluence Storage Format** — an XHTML-derived format with macros.
`atl` treats it as **byte-stable**: it parses CSF read-only and never re-serializes your bytes, so
editing the `.csf` directly is safe and lossless. This is why there is no Markdown round-trip — a
Markdown conversion would silently drop macros and structure.

Edit the XML/macro bytes directly. Keep tags balanced and entities well-formed; `atl conf validate`
checks well-formedness and reports problems as `{severity, line, col, rule, message}` (treat any
`severity: "error"` as a hard block).

## Editing existing CSF — avoid the exact-match trap

Real CSF bodies are usually **one huge line** and contain **invisible bytes**: non-breaking
spaces (`U+00A0` — pasted from the Confluence editor), entities, zero-width characters. An
exact-string edit can miss even when the text *looks* identical on screen. Agents lose the most
time here — not on writing CSF, but on retrying string matches that can never match. Work like
this:

1. **Match the shortest unique anchor** around the change — a few words plus the nearest tag,
   never a whole sentence or a whole table row.
2. **If an exact match fails once, stop retrying variants.** Dump the real bytes of the region
   and look at them:
   ```bash
   python3 -c "t=open('page.csf').read(); i=t.find('anchor text'); print(repr(t[max(0,i-40):i+120]))"
   ```
   A `\xa0` in the output is the usual culprit — include it in your match string as-is.
3. **For long single-line stretches** (table rows, macro bodies), a checked scripted replacement
   is more reliable than editor matching:
   ```bash
   python3 - <<'EOF'
   t = open('page.csf').read()
   old, new = '<td>exact old cell</td>', '<td>new cell</td>'
   assert t.count(old) == 1, f"matches: {t.count(old)}"
   open('page.csf', 'w').write(t.replace(old, new))
   EOF
   ```
   The `count == 1` assert is the safety: zero means your anchor has hidden bytes, two+ means it
   is not unique.
4. **Never reformat or pretty-print the whole file** — the bytes are the substrate; touch only
   the fragment you are changing, and re-run `atl conf validate` after every edit.

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
