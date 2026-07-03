# CSF and fragments

This document explains how `atl` handles Confluence Storage Format (CSF): how
it parses CSF for reading, why edits are byte-stable on the write path, what
the `.md` read-view looks like, and how opaque fragments (diagrams, user
mentions, page links, images, attachments) are discovered and resolved.

See also: [../README.md](../README.md) · [architecture.md](architecture.md) ·
[usage.md](usage.md)

---

## What is Confluence Storage Format?

Confluence Storage Format is the native on-wire format used by the Confluence
REST API's `body.storage` representation. It is XHTML-like XML with two
non-standard namespace prefixes:

- `ac:` — Confluence-specific elements: `<ac:structured-macro>`,
  `<ac:parameter>`, `<ac:image>`, `<ac:link>`, `<ac:layout>`, etc.
- `ri:` — Resource Identifier elements: `<ri:page>`, `<ri:user>`,
  `<ri:attachment>`, etc.

Standard HTML block and inline elements (`<p>`, `<h1>`–`<h6>`, `<table>`,
`<ul>`, `<strong>`, `<a>`, …) co-exist with the `ac:`/`ri:` elements.
Documents can use HTML entities (`&nbsp;`, `&mdash;`, `&hellip;`) alongside
numeric character references.

A CSF body is an XML fragment (not a full document), so it may have multiple
top-level nodes. Macros like draw.io diagrams, info panels, code blocks, and
table-of-contents placeholders are all expressed as `<ac:structured-macro
ac:name="…">` elements with `<ac:parameter>` children and an optional
`<ac:rich-text-body>` or `<ac:plain-text-body>`.

---

## The write path: byte-stable round-trip

**`atl` never re-serializes a CSF body.** When `atl conf push` uploads a page,
it sends the exact bytes from the `.csf` file verbatim. This is the
foundational safety property of the tool: macros, panels, layouts, draw.io
diagrams, and any other Confluence-specific markup that the parser does not
understand are preserved with zero risk of lossy conversion.

The comment in `internal/csf/parse.go` makes this explicit:

> "The DOM here is read-only and lossy by design — it exists to understand a
> body, not to reproduce it."

The `.csf` file is the single source of truth. The `.md` read-view is derived
from it for human/agent orientation but is never used on the write path.

---

## The read path: parsing into a DOM

`csf.Parse(raw []byte) (*csf.Node, error)` builds a read-only DOM:

1. The raw bytes are wrapped in a synthetic `<root>…</root>` element so that
   body fragments with multiple top-level nodes are always a valid XML document
   for `encoding/xml`.
2. An `xml.Decoder` is configured with `xml.HTMLEntity` (resolves `&nbsp;`,
   `&mdash;`, and other HTML entities) and `Strict: true`.
3. Tokens are consumed and assembled into a `*csf.Node` tree. Namespace
   prefixes are kept as-is in `Node.Name.Space` (`"ac"`, `"ri"`, `""`), which
   avoids any dependency on namespace declarations in the document.
4. `xmlns:*` attributes are stripped from the DOM (they would appear as
   attributes in `encoding/xml` output but are not meaningful for reading).
5. CDATA sections and ordinary text are both stored as `Text` nodes (they are
   indistinguishable at the token layer and treated identically for text
   extraction).

DOM types:

| type | description |
|---|---|
| `csf.Node` | a single DOM node |
| `csf.NodeType` | `Element`, `Text`, or `CData` |
| `csf.Name` | `{Space, Local}` — e.g. `{"ac", "structured-macro"}` |
| `csf.Attr` | `{Name, Value}` |

Helper functions on `*csf.Node`:

- `Attrv(space, local)` — returns an attribute value by namespace + local name.
- `MacroName()` — returns the `ac:name` attribute of a structured-macro/macro
  element, or `""`.

`csf.Walk(n, fn)` performs depth-first traversal; returning `false` from `fn`
skips the subtree (used to avoid descending into draw.io macro internals).

`csf.TextContent(n)` returns the concatenated, trimmed text of a subtree.

---

## Validation

`csf.Validate(raw []byte) []csf.Problem` runs two passes:

### Pass 1: well-formedness

Streams all XML tokens. On any error, records a single `Problem` with
`Severity: "error"` and an accurate line/col position mapped back to the
original bytes (the 6-byte `<root>` prefix offset is subtracted before the
line/col computation).

A well-formedness error **blocks a push** (`csf.HasErrors` returns true).
Sending malformed XML to the Confluence API would silently corrupt the page.

### Pass 2: sanity checks (warnings only)

Walks the successfully-parsed DOM and emits advisory `Problem` items
(`Severity: "warning"`) for common mistakes:

| rule | condition |
|---|---|
| `macro-name` | `<ac:structured-macro>` missing `ac:name` |
| `drawio-params` | `drawio` macro missing the `diagramName` parameter |
| `dangling-ref` | `<ri:attachment>` without `ri:filename` |

Warnings do not block a push; they are surfaced in the output of
`atl conf validate` and `atl conf push --dry-run` so a human or agent can
notice and fix them.

---

## Fragment extraction

`fragment.Extract(root *csf.Node) []domain.Ref` walks the DOM and identifies
_opaque fragments_ — CSF constructs that cannot be round-tripped through
Markdown without information loss and that may require separate asset fetches
for a legible read-view.

Extracted fragments, in document order, deduplicated by `(kind, key)`:

| CSF pattern | `Ref.Kind` | `Ref.Key` |
|---|---|---|
| `<ac:structured-macro ac:name="drawio">` with `<ac:parameter ac:name="diagramName">` | `drawio` | diagram name; `revision` captured in `Params` |
| `<ac:image>` containing `<ri:attachment ri:filename="…">` | `image` | attachment filename |
| `<ri:user ri:userkey="…">` or `<ri:user ri:account-id="…">` | `user` | userkey or account-id |
| `<ri:page ri:content-title="…">` | `page-link` | content-title |
| `<ri:attachment ri:filename="…">` (outside `<ac:image>`) | `attachment` | filename |

Draw.io macro internals are skipped with `return false` in the `Walk`
callback — the macro's body is fully opaque XML/JSON and descending into it
would produce spurious hits.

---

## Fragment resolution

`fragment.Resolve(ctx, page, refs, deps)` fills `Ref.Display` and `Ref.Asset`
for each extracted ref. All failures are swallowed; a ref that cannot be
resolved keeps its raw display and gains no asset path.

### draw.io and inline images

`deps.Resolver.Resolve(ctx, page, ref)` is called (the Confluence adapter
implements `domain.AssetResolver`):

- **draw.io**: downloads the pre-rendered PNG via
  `/download/attachments/<pageID>/<diagramName>.png?version=<revision>`.
  The revision number comes from the `<ac:parameter ac:name="revision">` value
  captured at extraction time, so the PNG matches the exact version of the
  diagram stored in the page — not the latest revision of the attachment.
- **inline image**: downloads via
  `/download/attachments/<pageID>/<filename>` (latest version).

The bytes are handed to `deps.Assets.Put(filename, data)` which writes them
into `<slug>.assets/<filename>` under the page directory and returns the
relative path. This relative path is stored in `Ref.Asset` and used in the
`.md` read-view.

Resolution only runs when `--assets` is passed to `atl conf pull` (the
`Resolver` field of `fragment.Deps` is left nil otherwise, and the code skips
it gracefully).

### User mentions

`deps.Users(ctx, userkey)` (the `domain.UserResolver` closure from the
Confluence adapter's `ResolveUser` method) maps an opaque userkey or account-id
to a display name. Results are cached within a single `Resolve` call to avoid
duplicate API round-trips when the same user is mentioned multiple times.

### Page links and attachments

These are already human-readable from the CSF itself (`content-title`,
`filename`). No network call is made; `Display` is set from the extracted key
during `Extract` and is not changed by `Resolve`.

---

## The `.md` read-view

`mirror.RenderMarkdown(root *csf.Node, refs []domain.Ref) []byte` produces the
human-readable and grep-friendly `.md` file. It is intentionally lossy: the
`.csf` file is always the authoritative source for edits and pushes.

### Block-level rendering

| CSF element | Markdown output |
|---|---|
| `<h1>`–`<h6>` | `#`–`######` heading |
| `<p>` | paragraph with trailing blank line |
| `<ul>` / `<ol>` | unordered / ordered list (nested) |
| `<table>` | pipe-table (first `<th>` row becomes the header row; `colspan` pads columns, `rowspan` repeats covered values) |
| `<hr>` | `---` |
| `<ac:layout>`, `<ac:layout-section>`, `<ac:layout-cell>` | contents rendered recursively (layout structure discarded) |
| `<ac:structured-macro ac:name="code">` | fenced code block with language hint |
| `<ac:structured-macro ac:name="info\|note\|warning\|tip\|panel">` | blockquote with label |
| `<ac:structured-macro ac:name="toc">` | `⟦table of contents⟧` placeholder |
| `<ac:structured-macro ac:name="status">` | `` `[TITLE]` `` inline badge |
| `<ac:structured-macro ac:name="drawio">` | `![diagram: name](slug.assets/name.png)` if asset downloaded; otherwise `⟦drawio diagram: name (open in Confluence)⟧` |
| any other macro | `⟦macro name⟧` (with rich body rendered if present) |

### Inline rendering

| CSF element | Markdown output |
|---|---|
| `<strong>` / `<b>` | `**…**` |
| `<em>` / `<i>` | `_…_` |
| `<code>` | `` `…` `` |
| `<br>` | space |
| `<a href="…">` | `[label](href)` |
| `<span style="color: …">` | `⟦color:…⟧text⟦/color⟧` marker |
| `<ac:link>` to a page | `[[title]]` |
| `<ac:link>` to an attachment | `[filename](attachment:filename)` |
| `<ri:user>` | resolved display name, or `@rawkey` |
| `<ac:image>` | `![filename](slug.assets/filename)` if downloaded; otherwise `![filename](attachment:filename)` |
| draw.io macro inline | asset link or `⟦drawio diagram: name⟧` |

The `⟦…⟧` placeholder syntax is used consistently for anything that cannot be
faithfully expressed in Markdown. An agent reading the `.md` file sees these
sentinels and knows to open Confluence for those elements, while still being
able to read, search, and reason over all the prose content.

---

## Example: a page with a draw.io diagram

Given a CSF body like:

```xml
<p>See the architecture below.</p>
<ac:structured-macro ac:name="drawio">
  <ac:parameter ac:name="diagramName">System overview</ac:parameter>
  <ac:parameter ac:name="revision">3</ac:parameter>
</ac:structured-macro>
<p>The boxes represent services.</p>
```

After `atl conf pull --id 12345678 --assets`:

- `child.csf` contains the exact bytes above.
- `child.assets/System overview.png` contains the PNG for revision 3 of the
  diagram attachment.
- `child.md` contains:

  ```markdown
  See the architecture below.

  ![diagram: System overview](child.assets/System overview.png)

  The boxes represent services.
  ```

- `child.meta.json` contains:

  ```json
  {
    "id": "12345678",
    "title": "Child page",
    "version": 7,
    "content_hash": "a3b4c5…",
    "fragments": [
      {
        "kind": "drawio",
        "key": "System overview",
        "display": "System overview",
        "asset": "child.assets/System overview.png",
        "params": {"diagramName": "System overview", "revision": "3"}
      }
    ]
  }
  ```

If the agent later edits `child.csf` and removes the draw.io macro, `atl conf
push --dry-run` will report a removed fragment in its output so the edit can be
reviewed before the push lands.
