# Testing & extending the CSF→Markdown renderer

The `.md` files in a mirror are a **derived, intentionally lossy staging** view of the
Confluence Storage Format (`.csf`) body, produced by `internal/mirror/markdown.go`
(`RenderMarkdown`). The `.csf` bytes are the source of truth; a render failure is
swallowed and never fails a pull. Because the view exists for humans and agents to
`grep`/read (and for supported edits merged by `conf apply`), "correct" means
**legible and faithful**, not byte-exact HTML↔Markdown. The view is never pushed
directly and may be regenerated.

This document describes how we verify the renderer and how to grow its coverage.

## Three layers of tests

1. **Per-construct unit tests — the contract.**
   `internal/mirror/render_test.go` and `internal/mirror/render_gaps_test.go` each
   pin one construct with a *minimal, synthetic* CSF snippet and assert the rendered
   substring(s). One behavior per test, written test-first. This is where every
   supported construct is locked; if you change a behavior, change the test here.

2. **Mixed-document smoke test.**
   `internal/mirror/render_smoke_test.go` renders `internal/csf/testdata/sample.csf`
   — a single synthetic page that exercises many constructs together — and checks
   the headline pieces survive. It catches interactions the unit tests miss.

3. **Corpus coverage sweep — optional, local.**
   `internal/mirror/corpus_sweep_test.go` renders a whole directory of *real*
   storage-format pages to find what the renderer still mishandles and to guard
   against panics on real input. It is **skipped unless a corpus is present**, so CI
   and the default `go test` are unaffected.

All three run with the standard tooling:

```sh
go test ./internal/mirror/        # layers 1 + 2 (3 skips with no corpus)
go test -race ./internal/mirror/  # the renderer must be panic- and race-free
```

## The corpus sweep

The sweep is a **coverage radar**: point it at real pages and it tells you what to
implement next. Real pages are the only reliable source of *what is actually
frequent*; guessing from the spec under-weights the common shapes and over-weights
the exotic ones.

### Build a corpus (never committed)

Pull real pages with the tool itself, into the conventional gitignored location:

```sh
atl conf pull --space <KEY> --into .csf-corpus     # or --cql / a page subtree
```

`.csf-corpus/` is in `.gitignore` and **must never be committed** — it contains real
page content. The committed tests use synthetic snippets only; the corpus is a
local, regenerable artifact. Use any Confluence instance you have read access to.

### Run it

```sh
# auto-detected at ./.csf-corpus, or point anywhere:
ATL_CSF_CORPUS=/path/to/corpus go test ./internal/mirror/ -run Sweep -v
```

The sweep:

- **fails** if any page panics the renderer (a hard correctness bug), and
- **reports** (with `-v`) a frequency-ranked list of every macro that still falls
  through to a generic `⟦macro NAME⟧` placeholder — i.e. your prioritized TODO for
  new coverage.

For table fidelity regressions, add synthetic unit tests first. Real corpus pages
can guide the case, but committed tests should use small snippets that cover the
CSF shape: `rowspan`, `colspan`, ordinary links inside cells, and semantic inline
styles such as colored spans.

### Frequency analysis by hand

To decide *what* to cover first, rank the constructs that actually occur:

```sh
# macros by ac:name, most frequent first
grep -rhoE 'ac:name="[a-z0-9-]+"' .csf-corpus --include='*.csf' | sort | uniq -c | sort -rn

# raw tag names (find unhandled elements like <pre>, <blockquote>, <time>, <s>)
grep -rhoE '<[a-z:]+' .csf-corpus --include='*.csf' | sort | uniq -c | sort -rn
```

> `.csf-corpus/` is hidden (leading dot) **and** gitignored, so tools that auto-skip
> those — ripgrep, `fd` — won't reach it from a *bare repo-wide* search. Naming the
> path explicitly is enough: `rg 'ac:name=' .csf-corpus` searches an explicitly-given
> root even when it's hidden/ignored (and conveniently skips the mirror's `.atl/`
> base snapshots, which you don't want to count). The `grep -r` recipes above are
> ignore-agnostic and need no flags.

Fix in frequency-×-damage order: a construct on most pages that *glues text* or
*drops a body* beats a rare one that only loses formatting.

## Workflow: adding coverage for a new construct

1. **Capture the shape.** Reduce the real element to the smallest *synthetic* CSF
   snippet that reproduces it (no real content, IDs, or hostnames). Inspect a real
   one with `grep -oE` over a corpus if needed.
2. **Write a failing test** in `render_gaps_test.go` (`mustContain` / `mustNotContain`).
   Run it; confirm it fails for the right reason.
3. **Implement in the correct path.** The renderer has three entry points and they
   must stay consistent:
   - `block()` — block-level elements and `macro()` for block macros (bodies, panels).
   - `inlineNode()` — inline elements and inline macros.
   - Note `soleBlockMacro`: Confluence routinely wraps a single block macro in `<p>`,
     which would otherwise route through the inline path and lose its body. New
     body-bearing macros usually belong in `isBlockMacroName` too.
   - A macro that can appear both inline and as a block (e.g. `jira`) needs the same
     decision in both `macro()` and the inline `structured-macro` switch.
4. **Re-run** the unit tests *and* the corpus sweep. The sweep confirms no
   regression and no new generic fallthrough.

## Invariants the view must keep

These are the properties the tests defend; preserve them when extending:

- **No word-gluing.** Whitespace between inline elements is preserved; block
  children flattened into a cell/list item are space-separated.
- **No silently dropped bodies.** Every macro renders *something* — its content if
  it has a body, otherwise a named `⟦…⟧` placeholder. Nothing vanishes.
- **Tables stay aligned.** Merged cells (`colspan`) are padded so header and body
  rows have the same column count.
- **Clean output.** Single trailing newline, never more than one consecutive blank
  line (`normalizeBlankLines`).
- **Never panics, never fails a pull.** Unknown constructs degrade gracefully
  (descend into children / placeholder), and `mirror.Write` swallows render errors.

## Optional: differential testing against an external oracle

For higher confidence on the parts that map cleanly to standard Markdown, diff our
output against an independent converter:

- **Pandoc** (`pandoc -f html -t gfm`) is an excellent oracle for the HTML *skeleton*
  — tables (including `colspan`/`rowspan`), nested lists, inline emphasis — after a
  small pre-pass that strips/normalizes the Confluence-specific `ac:`/`ri:` macros it
  does not understand.
- A **second storage-format→Markdown implementation** can be used *offline as a
  fixture generator*: run it over the corpus to emit "expected" `.md`, store those as
  goldens, and diff. Keep any such third-party converter **out of `go.mod`** — it is a
  test-time tool, not a shipped dependency.

Availability of these tools (Pandoc, a reachable Go module proxy) is environment-
specific; treat the oracle as an optional confidence boost layered on top of the
synthetic unit tests, not a replacement for them.
