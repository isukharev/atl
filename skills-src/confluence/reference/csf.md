# Confluence Storage Format (CSF) and fragments

## What the `.csf` is

`.csf` holds the page body in **Confluence Storage Format** — an XHTML-derived format with macros.
`atl` treats it as **byte-stable**: it parses CSF read-only and never re-serializes your bytes, so
editing the `.csf` directly is safe and lossless. This is why there is no Markdown round-trip — a
Markdown conversion would silently drop macros and structure.

Edit the XML/macro bytes directly. Keep tags balanced and entities well-formed; `atl conf validate`
checks well-formedness and reports problems as `{severity, line, col, rule, message}` (treat any
`severity: "error"` as a hard block).

## Editing existing CSF — pick the technique by situation

**Prefer the md surface**: edit the page's `.md` and run `atl conf apply` (see the skill's
canonical loop). Styled tables are covered there too — apply merges them row/cell-wise. The
techniques below are for what apply refuses or md cannot express — rowspan/colspan
restructuring, column add/remove, nested tables, unrecognized wrappers, ambiguous mentions,
surgical byte-level fixes.

Real CSF bodies are usually **one huge line** and contain **invisible bytes**: non-breaking
spaces (`U+00A0` — pasted from the Confluence editor), entities, zero-width characters. An
exact-string edit can miss even when the text *looks* identical on screen. Agents lose the most
time here — not on writing CSF, but on retrying string matches that can never match. No single
technique is cheapest everywhere; pick by situation:

| Situation | Technique |
|---|---|
| Short, targeted replacement | Your editor's exact-match edit with the **shortest unique anchor** — one attempt only |
| Exact match missed once, or `conf validate` warned `invisible-chars` | `atl conf edit --old … --new …` (tolerant matching) — don't retry exact variants blindly |
| Inserting new content at a spot | `atl conf edit --old '<anchor>' --new '<anchor + new content>'` |
| Long span: delete/rewrite a whole section or table row | Scripted splice between two **short** boundary anchors (below) — don't pass the whole span as `--old` |

**`atl conf edit`** is the tolerant matcher:

```bash
atl conf edit page.csf --old 'text as you see it' --new 'replacement'
atl conf edit page.csf --old ' obsolete sentence.' --new ''        # delete
```

- Matching is layered (exact → NBSP/zero-width/entity-tolerant → whitespace-run-tolerant), so
  you can type plain spaces where the file has `U+00A0` or `&nbsp;` — it still matches, and the
  splice preserves every surrounding byte verbatim.
- It refuses to guess: **exit 4** = not found, and the error dumps the closest region with
  hidden bytes made visible; **exit 2** = ambiguous — tighten `--old` or pass `--all`.
- For `.csf` files the result is auto-validated (`csf_ok` in the JSON) — no separate
  `conf validate` call needed after a `conf edit`.
- Skip `--dry-run` for routine replacements: the command is atomic — on a miss (exit 4) the
  file is untouched. Reserve `--dry-run` for genuinely risky substitutions (e.g. `--all`).
- Keep `--old`/`--new` **inline and short**. Don't write helper files just to feed
  `--old-file`/`--new-file` — the file ceremony (create, feed, clean up) costs more than the
  edit; the flags exist for content that already lives in a file (they strip one trailing
  newline, so editor-written files work as-is against single-line CSF).

**Long spans** (dropping a section, replacing a table row): don't hunt for the exact bytes of
the whole span — splice between two short boundary anchors and validate:

```bash
python3 - <<'EOF'
t = open('page.csf').read()
a, b = '<h1>Section to drop</h1>', '<h1>Next section'
assert t.count(a) == 1 and t.count(b) == 1
i, j = t.index(a), t.index(b)          # keeps the end boundary
open('page.csf', 'w').write(t[:i] + t[j:])
EOF
atl conf validate page.csf
```

Rules that always apply:

1. **Match the shortest unique anchor** around the change — a few words plus the nearest tag,
   never a whole sentence or a whole table row.
2. **Never reformat or pretty-print the whole file** — the bytes are the substrate; touch only
   the fragment you are changing.
3. To see what's really in a region (before composing an anchor), dump the raw bytes:
   ```bash
   python3 -c "t=open('page.csf').read(); i=t.find('anchor text'); print(repr(t[max(0,i-40):i+120]))"
   ```
   The same `str.replace` guarded by `assert t.count(old) == 1` is the fallback when
   `conf edit` is unavailable (old binary).

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

View v2 renders page links as
`[label](confluence-page:SPACE/percent-encoded-title)` so label, target, and
cross-space identity stay visible. Colored text uses readable protected HTML
spans for a closed safe-color grammar and inert `data-atl-color` otherwise.
Leave either representation intact for byte-preserving edits; changing
or removing one is included in the apply loss gate, including when two links
share a label but point to different pages.
