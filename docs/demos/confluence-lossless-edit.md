# Demo: edit a complex page without losing native structure

Pull one page into a dedicated mirror and edit the derived Markdown view:

```sh
export ATL_MIRROR_ROOT="$HOME/.atl/demo-workspace"
atl conf pull --id 12345 --into "$ATL_MIRROR_ROOT"

# Change one supported table cell in the generated .md view.
atl conf apply "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.md"
atl conf validate "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf"
atl conf diff "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf" -o text
atl conf push "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf" --dry-run
```

What this demonstrates:

- pull stores the server's native `.csf` bytes as the write substrate;
- the `.md` file is a derived staging view;
- apply merges only the supported changed table cell;
- untouched code macros, table attributes, inline breaks, and opaque fragments
  remain byte-identical;
- dry-run reports the candidate without a remote write.

The credential-free repository rehearsal uses a synthetic page containing a
code macro whose body contains a Markdown fence, wrapper elements, cell
styling, an inline break, and one editable table cell. It changes only that
cell, then proves every other native byte remains exact and that no
PUT/POST/DELETE reached the loopback backend.

Run it with:

```sh
make check-onboarding-docs
```

For a real page, inspect the emitted paths rather than assuming the example
slug. If apply reports that a block cannot be represented faithfully, make the
edit in `.csf` or leave the block unchanged; do not bypass the loss gate.
