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
time here — not on writing CSF, but on retrying string matches that can never match.

**Use `atl conf edit` — it is built for exactly this:**

```bash
atl conf edit page.csf --old 'text as you see it' --new 'replacement'
atl conf edit page.csf --old-file old.txt --new-file new.txt [--dry-run] [--all]
atl conf edit page.csf --old ' obsolete sentence.' --new ''        # delete
```

- Matching is layered (exact → NBSP/zero-width/entity-tolerant → whitespace-run-tolerant), so
  you can type plain spaces where the file has `U+00A0` or `&nbsp;` — it still matches, and the
  splice preserves every surrounding byte verbatim.
- It refuses to guess: **exit 4** = not found, and the error dumps the closest region with
  hidden bytes made visible; **exit 2** = ambiguous — tighten `--old` or pass `--all`.
- Inserting: `--old '<anchor>' --new '<anchor + new content>'` (anchor on the side you extend).
- For `.csf` files the result is auto-validated (`csf_ok` in the JSON) — no separate
  `conf validate` call needed after each edit.
- `--old-file`/`--new-file` strip one trailing newline, so files written by your editor/tools
  work as-is against single-line CSF.

Rules that still apply:

1. **Match the shortest unique anchor** around the change — a few words plus the nearest tag,
   never a whole sentence or a whole table row.
2. **Never reformat or pretty-print the whole file** — the bytes are the substrate; touch only
   the fragment you are changing.
3. Fallback when `conf edit` is unavailable (old binary): dump the region bytes and splice with
   a checked script —
   ```bash
   python3 -c "t=open('page.csf').read(); i=t.find('anchor text'); print(repr(t[max(0,i-40):i+120]))"
   ```
   then `str.replace` guarded by `assert t.count(old) == 1`.

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
