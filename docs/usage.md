# Usage reference

`atl` is a non-interactive, agent-native CLI for Confluence and Jira. It
mirrors pages to local `.csf` files in their native storage format, validates
edits, and pushes back under an optimistic version gate — all without storing
credentials in the repository or the mirror.

See also: [../README.md](../README.md) · [architecture.md](architecture.md) ·
[csf-and-fragments.md](csf-and-fragments.md) · [self-update.md](self-update.md)

---

## Global conventions

### Output format

By default every command writes JSON to stdout. Pass `-o text` (or
`--output text`) for human-readable output on the same commands that support
it.

```
atl conf search --cql "space=DOCS" -o text
atl jira issue get PROJ-1 -o text
```

### Body input (`--from-file`)

Commands that accept a document body (CSF or Jira wiki) read it from a file
path or from stdin when you pass `-`:

```bash
# from a file
atl conf page create --space DOCS --title "New page" --from-file body.csf

# from stdin (pipe a heredoc or a prior command's output)
echo '<p>Hello</p>' | atl conf page create --space DOCS --title "New page" \
    --from-file -
```

The defaults follow one rule: commands whose body is **required** default
`--from-file` to `-` (stdin) — `conf page create`, `conf comment add`,
`jira issue comment add`; commands whose body is **optional** default to no
body — `jira issue create`, `jira issue update`. When stdin is an
interactive terminal (nothing piped), reading a body from it is refused with
a usage error (exit 2) instead of hanging forever waiting for input.

### Exit codes

| code | meaning |
|---|---|
| 0 | success |
| 1 | generic error |
| 2 | usage error (bad flags, missing required args, insecure backend URL) |
| 3 | authentication failed (a PAT **was** supplied but the server rejected it) |
| 4 | resource not found |
| 5 | version conflict (remote moved since last pull; re-pull and retry) |
| 6 | forbidden (per-space or per-issue permission) |
| 7 | not configured (backend URL or PAT **not set** yet; run `atl config set` / `atl auth login`) |
| 8 | check failed (`jira issue check` found empty required fields) |

A script can therefore tell three distinct "auth-ish" states apart: `7` = you
have not finished setup (no URL/token) → run setup; `3` = the token you supplied
was refused → replace it; `6` = the token is valid but lacks permission. Note the
split for a bad URL: a *missing* URL is `7`, but a *non-https* (insecure) URL is a
usage error (`2`) — fix the input rather than re-running setup.

---

## Environment variables

### Backend URLs

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_URL` | Confluence base URL (takes priority over `CONFLUENCE_URL`) |
| `CONFLUENCE_URL` | Confluence base URL (fallback) |
| `ATL_JIRA_URL` | Jira base URL (takes priority over `JIRA_URL`) |
| `JIRA_URL` | Jira base URL (fallback) |
| `ATL_ALLOW_INSECURE` | set to any non-empty value to permit a non-https backend URL for a non-loopback host (an internal http-only instance you trust). Loopback hosts are always allowed; otherwise a non-https URL is refused so the PAT is never sent in cleartext |

### Mirror location

| variable | effect |
|---|---|
| `ATL_MIRROR_ROOT` | default mirror root for `conf pull`, `conf status`, and `jira pull` (so a workspace fixes one location without re-passing `--into`; an explicit `--into` still overrides it) |

### Authentication

| variable | effect |
|---|---|
| `ATL_CONFLUENCE_PAT` | Confluence Personal Access Token |
| `ATL_JIRA_PAT` | Jira Personal Access Token |

Env vars take priority over the stored credentials file. See `atl auth` below
for how to store PATs on disk.

### Config directory

| variable | effect |
|---|---|
| `ATL_CONFIG_DIR` | override config/credentials directory (default: `$XDG_CONFIG_HOME/atl` or `~/.config/atl`) |
| `XDG_CONFIG_HOME` | standard XDG base directory (used when `ATL_CONFIG_DIR` is not set) |

### Self-update

| variable | effect |
|---|---|
| `ATL_UPDATE_URL` | override the distribution server base URL |
| `ATL_NO_UPDATE` | set to any non-empty value to disable auto-update |
| `ATL_UPDATE_DEBUG` | set to any non-empty value to print self-update diagnostics to stderr |

---

## Scripting & CI

`atl` is built for non-interactive use: JSON to stdout, diagnostics to stderr,
stable exit codes, no prompts. A robust CI/script harness looks like this:

```bash
#!/usr/bin/env bash
set -euo pipefail

# 1. Configure entirely from the environment (URLs + PATs); no on-disk config.
export ATL_CONFLUENCE_URL="https://confluence.example.com"
export ATL_CONFLUENCE_PAT="$CONFLUENCE_TOKEN"   # from your CI secret store

# 2. Disable the best-effort self-update so a command never spends time probing
#    the release server (it is throttled, but a fresh runner has no throttle file).
export ATL_NO_UPDATE=1

# 3. Isolate credentials: point at a throwaway config dir so a leftover
#    ~/.config/atl/credentials.json from a previous job can't silently win.
export ATL_CONFIG_DIR="$(mktemp -d)"

# 4. Fail fast with a clear signal if setup/connectivity is wrong.
if ! atl conf search --cql 'type = page' --limit 1 >/dev/null; then
  code=$?
  case $code in
    7) echo "atl is not configured (URL/PAT missing)"   >&2 ;;
    3) echo "atl PAT was rejected by the server"          >&2 ;;
    *) echo "atl connectivity check failed (exit $code)"  >&2 ;;
  esac
  exit $code
fi

atl conf pull --cql 'label = runbook' --into "$PWD/mirror"
```

Notes for scripts:

- **Errors are JSON too.** On success `atl` prints a JSON result to stdout; on
  failure it prints `{"error": "...", "code": N}` to **stderr** (use `-o text`
  for a plain `error: <msg>` line instead). Branch on the exit code; parse stdout
  for results and, if you capture stderr, parse it the same way.
- **`--cql` pull caps at 1000 pages; `--space` at 2000.** When either cap is
  hit the result carries `"truncated": true` / `"truncated_at": N` and a
  `warning:` line is printed to stderr — the rest is not mirrored. Narrow the
  selection to get everything.
- **`--from-file -` (stdin) is bounded at 64 MiB**; larger input is rejected
  with a usage error (exit 2) — pass a file path for bigger bodies.
- **Direct REST fallback:** when you must call an uncovered Server/Data Center
  endpoint yourself, keep PATs out of argv and shell history. Put the token in an
  env var, disable shell tracing, and feed curl's header through stdin:

  ```bash
  set +x
  {
    printf 'url = "%s/rest/api/2/myself"\n' "$ATL_JIRA_URL"
    printf 'header = "Authorization: Bearer %s"\n' "$ATL_JIRA_PAT"
  } | curl --fail --silent --show-error --config -
  ```

---

## `atl config`

Manage non-secret settings (backend URLs). PATs are managed separately via
`atl auth`.

### `atl config show`

Print the resolved configuration (file + env overlay).

```
atl config show
atl config show -o text
```

JSON output:

```json
{
  "confluence_url": "https://confluence.example.com",
  "jira_url": "https://jira.example.com",
  "update_base_url": "",
  "mirror": {
    "recommended_root": "~/.atl/<workspace>/",
    "active_root": "/home/user/.atl/work",
    "active_source": "ATL_MIRROR_ROOT"
  }
}
```

`mirror.active_root` is present only when `ATL_MIRROR_ROOT` is set. Explicit
`--into` flags still override the default for each pull/status command.

### `atl config set`

Persist one or more URLs to the config file
(`~/.config/atl/config.json`).

```
atl config set --confluence-url https://confluence.example.com
atl config set --jira-url https://jira.example.com
atl config set --update-url https://releases.example.com/atl
```

Flags:

| flag | description |
|---|---|
| `--confluence-url` | Confluence base URL |
| `--jira-url` | Jira base URL |
| `--update-url` | self-update distribution server base URL |

---

## `atl auth`

Manage Personal Access Tokens. PATs are written to a mode-0600 credentials
file (`~/.config/atl/credentials.json`) or resolved from env vars. They are
never stored in the mirror or the repository.

### `atl auth login`

Run without flags for an interactive setup wizard (like `gh auth login`). For each
service it asks for the base URL and PAT, validates the PAT against the backend, and
stores both. Any service can be skipped. Requires a terminal.

```sh
atl auth login
# ? Configure Confluence? (Y/n) y
# ?     Confluence base URL [https://wiki.example.com]:
# ?     Enter PAT (input hidden): ****
# ?   ✓ Confluence: authenticated as Jane Doe
# ? Configure Jira? (Y/n) n
```

For non-interactive/scripted setup, configure one service at a time with `--service`
(below) plus `atl config set` for the URLs.

Store a PAT for a service.

The token is never accepted on the command line (which would leak it to the
process list and shell history). Provide it via `--from-file`, piped stdin, or
an interactive no-echo prompt:

```bash
# interactive: prompts without echo when run on a terminal
atl auth login --service confluence

# read from stdin without echo (bash; -s is not POSIX sh); avoids shell history
read -rs PAT && echo "$PAT" | atl auth login --service jira --from-file -

# from a file
atl auth login --service jira --from-file ./jira.pat
```

Flags:

| flag | description |
|---|---|
| `--service` | `confluence` or `jira` (required) |
| `--from-file` | file path, or `-` for stdin; omit to be prompted without echo |

### `atl auth status`

Show where each token is resolved from (env var name or file path). Never
prints the token value.

```
atl auth status
```

```json
{
  "confluence": "env:ATL_CONFLUENCE_PAT",
  "jira": "keychain-file:/home/user/.config/atl/credentials.json"
}
```

### `atl auth logout`

Remove a stored PAT from the credentials file.

```
atl auth logout --service confluence
```

---

## `atl conf` — Confluence

### `atl conf search`

Search pages by CQL. Returns `id`, `title`, `space`, `version`, `excerpt`.

```
atl conf search --cql "space=DOCS and title~\"API\"" --limit 10
```

Flags:

| flag | description |
|---|---|
| `--cql` | Confluence CQL query (required) |
| `--limit` | max results (default 25) |
| `--cursor` | pagination cursor (start offset returned by the previous call) |

### `atl conf space tree`

Return the page hierarchy of a space. `depth 0` means unlimited.

```
atl conf space tree --space DOCS
atl conf space tree --space DOCS --depth 2
```

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--depth` | maximum depth (0 = unlimited) |

The listing stops at a 2000-page safety cap; when hit, the JSON result carries
`"truncated": true` and a `warning:` line goes to stderr.

### `atl conf pull`

Mirror pages to disk. Downloads `.csf` (native storage format), `.md`
(read-view), `.meta.json`, and optionally renders draw.io / image assets.

```bash
# single page
atl conf pull --id 12345678

# all pages in a space
atl conf pull --space DOCS --into my-mirror

# pages matching a CQL query
atl conf pull --cql "label=public and space=DOCS" --assets
```

Flags:

| flag | description |
|---|---|
| `--id` | single page id |
| `--cql` | CQL query selecting pages |
| `--space` | space key (mirrors the whole space) |
| `--depth` | depth limit when using `--space` (0 = unlimited) |
| `--assets` | download draw.io PNG renders and inline images |
| `--into` | mirror root directory (default `mirror`) |

At most one of `--id`, `--cql`, `--space` may be given.

**Mirror layout after pull**

```
mirror/
  DOCS/
    parent-page/
      child-page/
        child-page.csf         ← edit this
        child-page.md          ← read-only view
        child-page.meta.json   ← id, version, content_hash, fragments
        child-page.assets/
          diagram.png
  .atl/
    state.json                 ← last-synced version + hash
    base/
      12345678.csf             ← pristine copy for diff
```

### `atl conf table extract`

Extract tables from a page's native CSF body into structured data. This is useful
when the page has multiple tables or merged cells and a script needs something
more explicit than the markdown read-view.

```bash
# all tables as JSON, preserving per-cell metadata
atl conf table extract --id 12345678

# one table as rectangular CSV
atl conf table extract --id 12345678 --table 2 --format csv

# all tables as an XLSX workbook, one sheet per table
atl conf table extract --id 12345678 --format xlsx --out tables.xlsx
```

Flags:

| flag | description |
|---|---|
| `--id` | page id |
| `--table` | 1-based table index to extract (0 = all tables) |
| `--format` | `json`, `csv`, or `xlsx` |
| `--out` | optional output file; required for `xlsx` |

JSON preserves the expanded cells, source coordinates for rowspan/colspan
repeats, ordinary links, and visible inline color markers. CSV without
`--table` emits a cell-level stream so pages with different table shapes can
share one file; CSV with `--table` emits a rectangular table.

### `atl conf status`

Show which mirrored pages have local edits and which have drifted on the
remote since the last pull.

```bash
atl conf status
atl conf status my-mirror
atl conf status --remote          # also checks remote version (one request per page)
```

Local edits are shown with `M`; remote drift with `M↯` in text mode.

Flags:

| flag | description |
|---|---|
| `[DIR]` | mirror root directory (default `mirror`) |
| `--remote` | also check remote for drift (one API call per page) |

### `atl conf validate`

Validate a `.csf` file for XML well-formedness and common sanity issues.
Well-formedness errors (severity `"error"`) block a push. Sanity problems
(severity `"warning"`) are advisory.

```bash
atl conf validate mirror/DOCS/guide/guide.csf
```

Output (JSON):

```json
{
  "file": "mirror/DOCS/guide/guide.csf",
  "ok": false,
  "problems": [
    {
      "severity": "error",
      "line": 14,
      "col": 5,
      "rule": "well-formedness",
      "message": "malformed CSF: XML syntax error on line 14: unexpected end element </p>"
    }
  ]
}
```

Exits 1 when any error-severity problem is found; 0 otherwise.

Advisory `invisible-chars` warnings flag characters that render invisibly but
defeat exact-string editing — non-breaking spaces (`U+00A0`), zero-width
characters, soft hyphens — one warning per class with the occurrence count and
first position. They never block a push; use `atl conf edit` (tolerant
matching) when they are present.

### `atl conf edit`

Replace text in a local file with tolerance for the invisible bytes that break
exact-match editing of real CSF (non-breaking spaces `U+00A0`, zero-width
characters, `&nbsp;`/`&#160;`/`&#xa0;` entities). Matching runs in layered
passes — exact bytes, then invisible-tolerant, then whitespace-run-tolerant —
and the replacement is spliced into exactly the matched byte range; every
surrounding byte is preserved verbatim. The replacement itself is inserted
literally.

```bash
atl conf edit page.csf --old 'Запрос предназначен для получения' --new 'Запрос возвращает'
atl conf edit page.csf --old-file old.txt --new-file new.txt --dry-run
atl conf edit page.csf --old '<td>ok</td>' --new '<td>done</td>' --all
atl conf edit page.csf --old ' obsolete sentence.' --new ''          # delete
```

Flags:

| flag | description |
|---|---|
| `<file>` | local file to edit (positional, required) |
| `--old` | text to find (tolerant matching) |
| `--old-file` | read the text to find from a file (`-` for stdin; one trailing newline stripped) |
| `--new` | replacement text, inserted verbatim (`--new ''` deletes) |
| `--new-file` | read the replacement from a file (one trailing newline stripped) |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match without writing |

Output includes which pass matched (`"pass"`), match count and byte offsets,
and quoted `region_before`/`region_after` context for review. For `.csf`
files the result is validated automatically: `"csf_ok"` plus `"problems"`
appear in the output, and a not-well-formed result prints a stderr warning
(the file is still written; `conf push` remains the gate).

Exit codes: `4` — the text was not found in any pass (the error carries a
quoted dump of the closest region, exposing hidden bytes); `2` — the match is
ambiguous (make `--old` more specific or pass `--all`).

Usage notes: keep `--old`/`--new` short and inline — match an anchor around
the change, not a whole sentence or table row; `--old-file`/`--new-file` are
for content that already lives in a file, not worth a write-then-clean-up
ceremony. The command is atomic (a miss leaves the file untouched), so
`--dry-run` is only needed for risky substitutions such as `--all`. For long
spans (deleting a section, replacing a table row) splice between two short
boundary anchors with a checked script instead of matching the full span —
see the confluence skill's CSF reference for the decision table.

### `atl conf apply`

Merge edits from a page's markdown view (`page.md`) into its `.csf`, block by
block. The markdown file becomes an editable surface: blocks you did not touch
keep their **exact base bytes**; changed or new blocks are converted from a
strict markdown subset (headings, paragraphs, lists, task lists, simple
tables, fenced code, blockquotes/admonitions, links, `[[Page Links]]`,
`[KEY](jira:KEY)`); opaque elements in edited blocks (macros, mentions,
links, images) keep their original bytes. Local only — `conf push` remains
the write path to the server.

Tables with editor styling (cell `style`/`class` attributes, wrapper divs,
spans) are merged **row/cell-wise** rather than converted: untouched rows keep
their exact bytes; an edited cell has its converted content spliced into the
existing cell wrapper (styles and classes survive); a deleted row drops its
byte range (the fragment-loss gate still applies to macros/mentions it held);
an inserted row clones the byte structure of a neighboring row, so numbering
columns and per-cell styling carry over. Mentions and links copied from an
untouched row into an edited cell are cloned byte-exactly; macros are never
cloned (a copy would duplicate the macro identity).

```bash
atl conf apply mirror/DOCS/guide/guide.md
atl conf apply guide.md --dry-run              # report without writing
atl conf apply guide.md --allow-fragment-loss  # intentional macro/mention removal
```

| flag | description |
|---|---|
| `<page.md>` | the page's markdown view (positional, required) |
| `--dry-run` | report the merge without writing files |
| `--allow-fragment-loss` | proceed when the edit drops opaque fragments |
| `--into` | mirror root (defaults to nearest `.atl`) |

Output: `{path, csf_path, dry_run, report: {unchanged, moved, converted,
removed, merged_tables?, removed_fragments?, problems?}, csf_ok, wrote,
warning?}`. After a successful apply the `.md` is regenerated from the merged
body so both surfaces agree; if that refresh cannot be written the apply
still succeeds and `warning` reports that the `.md` may be stale.

The merge is **fail-closed** (exit `8`, nothing written) when: an edited block
cannot be converted faithfully (unsupported markdown, edits inside an
unrecognized wrapper element, an ambiguous mention whose display name collides
with prose); a table edit crosses what the row/cell mapping can carry
(changing a cell that spans rows/columns from a continuation slot, deleting a
row a rowspan passes through, adding/removing columns, editing inside a nested
table, copying a macro-bearing cell) — make that edit in the `.csf` directly
(`conf edit`); the edit drops opaque fragments and `--allow-fragment-loss`
was not given; or the local `.csf` has diverged from the last-synced base
(direct `.csf` edits win — push or re-pull first).
Exit `4`: the page was never pulled (no meta/base). The merged body is always
validated; `conf push --dry-run` remains the final gate before the server.

### `atl conf push`

Validate and push a `.csf` file (or all dirty files in a directory) back to
Confluence under an optimistic version gate.

```bash
# push one file
atl conf push mirror/DOCS/guide/guide.csf

# push all locally-edited files under a directory
atl conf push mirror/DOCS/

# dry run: show what would change without pushing
atl conf push --dry-run mirror/DOCS/guide/guide.csf

# override version conflict (last-write-wins — use with care)
atl conf push --force mirror/DOCS/guide/guide.csf
```

Push output lists each file's outcome and any removed/added fragments so you
can confirm that a macro or diagram was not accidentally deleted from the CSF.

Flags:

| flag | description |
|---|---|
| `--dry-run` | validate and diff fragments without pushing |
| `--force` | bypass the version gate (ignores remote drift) |
| `--into` | mirror root, when the file path is outside the default `mirror/` tree |

### `atl conf page get`

Fetch a page body directly (without mirroring to disk).

```bash
atl conf page get --id 12345678
atl conf page get --id 12345678 --format view   # rendered HTML view
atl conf page get --id 12345678 -o text         # raw body on stdout
```

Flags:

| flag | description |
|---|---|
| `--id` | page id (required) |
| `--format` | `csf` (default) or `view` (rendered HTML) |

### `atl conf page meta`

Fetch non-body page metadata (version, ancestors, labels, restrictions).

```
atl conf page meta --id 12345678
```

### `atl conf page history`

List up to 50 version records for a page, newest first.

```
atl conf page history --id 12345678
```

### `atl conf page create`

Create a new page. The body is either valid CSF (`--from-file`) or markdown
converted to CSF (`--from-md`) — the two flags are mutually exclusive.

```bash
echo '<p>Hello, <strong>world</strong>.</p>' \
  | atl conf page create --space DOCS --title "Hello" --from-file -

atl conf page create --space DOCS --parent 12345678 \
  --title "Child page" --from-file body.csf

# Author the body in markdown; atl converts it block-by-block:
atl conf page create --space DOCS --title "From markdown" --from-md body.md
```

`--from-md` accepts the same markdown subset as `conf apply` (headings,
paragraphs, emphasis/links, lists and task lists, GFM tables, fenced code,
blockquotes/admonitions, `---`, `[[Page Title]]` page links, `[KEY](jira:KEY)`
issue links). Conversion is fail-closed: the first construct outside the
subset aborts with exit 8 naming the offending block, and the page is **not**
created — write those bodies as CSF via `--from-file` instead. An empty
markdown document is refused the same way. The converted body still passes
the CSF validation gate before the API call.

Flags:

| flag | description |
|---|---|
| `--space` | space key (required) |
| `--title` | page title (required) |
| `--parent` | parent page id |
| `--from-file` | CSF body file or `-` for stdin (default stdin) |
| `--from-md` | markdown body file or `-` for stdin; converted to CSF, fail-closed (exit 8) |

### `atl conf page move`

Reparent a page.

```
atl conf page move --id 12345678 --parent 87654321
```

### `atl conf page delete`

Trash a page. May return exit 6 if per-space permissions forbid deletion.

```
atl conf page delete --id 12345678
```

### `atl conf page list`

Flat listing of pages in a space (no hierarchy), optionally by status.

```
atl conf page list --space ENG [--status current|archived|trashed] [--limit 100] [--cursor C]
```

`--space` is required. The output carries a `next_cursor` for pagination; `-o id`
prints the page ids.

### `atl conf page open`

Open a page in the system browser (uses `xdg-open`/`open`/`rundll32`, no shell).

```
atl conf page open --id 12345678
```

### `atl conf page copy`

Client-side copy that preserves the native CSF bytes verbatim (no Markdown
round-trip). Reads the source page and creates a new one with the same body.

```
atl conf page copy --id 12345678 --title 'Copy of Design Doc' [--space ENG] [--parent 999]
```

### `atl conf attachment {list,get,upload,delete}`

Manage page attachments. `delete` requires `--force`.

```bash
atl conf attachment list --id 12345678                       # {attachments:[...]}; -o id → ids
atl conf attachment get --id 12345678 --name diagram.png --into ./assets
atl conf attachment upload --id 12345678 --file ./diagram.png [--comment 'v2']
atl conf attachment delete --id <ATTACHMENT-ID> --force
```

### `atl conf me`

Show the authenticated Confluence user.

```
atl conf me
```

### `atl conf comment list`

List page comments. Bodies are returned as plain text (CSF stripped).

```
atl conf comment list --id 12345678
```

### `atl conf comment add`

Add a comment. Body is CSF.

```bash
echo '<p>LGTM.</p>' | atl conf comment add --id 12345678 --from-file -
```

---

## `atl jira` — Jira

### `atl jira issue get`

Fetch a Jira issue. Default fields: summary, description, status, type,
project, assignee, reporter, labels, links, comments, attachments.

```bash
atl jira issue get PROJ-1
atl jira issue get PROJ-1 --fields summary,status,issuetype,project,labels,description,attachment
atl jira issue get PROJ-1 -o text
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--fields` | comma-separated field list to restrict the response |

### `atl jira issue search`

Search issues by JQL.

```bash
atl jira issue search --jql "project=PROJ and status=Open" --limit 20
atl jira issue search --jql "assignee=currentUser()" --cursor 50
atl jira issue search --jql "project=PROJ" --fields summary,status,customfield_10001
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--fields` | comma-separated field list |
| `--limit` | max results (default 50) |
| `--cursor` | pagination cursor (startAt offset) |

### `atl jira issue create`

Create an issue. The description is either Jira wiki markup (`--from-file`) or
markdown converted to wiki (`--from-md`) — the two flags are mutually exclusive.

`--from-md` accepts the same markdown subset as the Confluence md surface
(headings, emphasis/links, bullet/numbered lists, GFM tables, fenced code,
blockquotes, `---`, `[KEY](jira:KEY)` issue links, `[~username]` mention
passthrough). Conversion is fail-closed: the first construct outside the
subset (task lists, images, emphasis without word boundaries, pipes inside
table cells, …) aborts with exit 8 naming the offending block, and nothing is
sent — write those bodies as wiki markup via `--from-file` instead.
Wiki-active characters in plain text (`{`, `[`, `!`, toggle chars in opening
position) are backslash-escaped automatically, so ordinary prose survives
verbatim. The same flag exists on `update` and `comment add`.

```bash
atl jira issue create \
  --project PROJ \
  --type Bug \
  --summary "Crash on empty input" \
  --from-file description.wiki

# or author the description in markdown:
atl jira issue create \
  --project PROJ --type Bug \
  --summary "Crash on empty input" \
  --from-md description.md

# with extra fields. A --field value that parses as JSON is sent as that JSON
# type, so structured fields work; a plain string is sent as a string.
atl jira issue create \
  --project PROJ --type Task --summary "Deploy docs" \
  --field 'priority={"name":"High"}' \
  --field 'labels=["docs","infra"]' \
  --field customfield_10001=foo
```

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (required) |
| `--summary` | issue summary (required) |
| `--from-file` | description body file (wiki markup) or `-` for stdin |
| `--from-md` | markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable) |

### `atl jira issue update`

Update summary, description, or arbitrary fields. This replaces the whole
description; for a small targeted change prefer `jira issue edit` below.

```bash
atl jira issue update PROJ-1 --summary "Crash on empty input (critical)"
atl jira issue update PROJ-1 --from-file updated-desc.wiki
atl jira issue update PROJ-1 --from-md updated-desc.md
atl jira issue update PROJ-1 --field 'priority={"name":"Highest"}'
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--summary` | new summary |
| `--from-file` | new description file (wiki markup) or `-` for stdin |
| `--from-md` | new markdown description file or `-` for stdin; converted to wiki, fail-closed (exit 8) |
| `--field key=value` | extra field (repeatable) |

### `atl jira issue edit`

Targeted description edit in one command: fetch the current description,
replace `--old` with `--new` (the same whitespace/invisible-tolerant matcher
as `conf edit`), and write the result back. Small fixes and
insert-after-anchor edits skip the get → compose → update ceremony.

```bash
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'
# insert a section by replacing an anchor heading with new text + the anchor
atl jira issue edit PROJ-1 --old 'h2. Verify' \
  --new $'h2. Rollback\n\nRestore the previous snapshot.\n\nh2. Verify'
atl jira issue edit PROJ-1 --old 'obsolete paragraph' --new ''   # delete
atl jira issue edit PROJ-1 --old 'foo' --new 'bar' --dry-run     # preview only
```

The match must be unique unless `--all` is passed: ambiguous → exit 2, no
match → exit 4 with a quoted region dump showing the closest candidate (and
any hidden bytes that broke exact matching). An empty description is exit 4 —
set one with `issue update`. A whitespace-tolerant match that would cross a
line break is refused with exit 8: Jira wiki is line-sensitive (`h2.`, `*`,
`{code}` are line-start tokens), so a cross-line splice could silently merge
lines — copy `--old` exactly, newlines included. Replacement text is native wiki markup, spliced
verbatim; for a full markdown rewrite use `issue update --from-md` instead.

Jira DC updates are last-writer-wins (no version gate), so the `--old` match
doubles as the drift guard: if the description changed underneath, the needle
misses and the command refuses instead of overwriting.

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--old` | text to find in the description (required; must be non-empty) |
| `--new` | replacement wiki text (required; pass `--new ''` to delete the match) |
| `--old-file` / `--new-file` | read either side from a file (`-` for stdin); one trailing newline is stripped |
| `--all` | replace every match instead of requiring a unique one |
| `--dry-run` | report the match and regions without updating the issue |

### `atl jira issue transition`

Move an issue through a workflow step. Transition names are matched
case-insensitively against the live transition list.

```bash
atl jira issue transition PROJ-1 --to "In Progress"
atl jira issue transition PROJ-1 --to Done --comment "Deployed to staging."
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | target status or transition name (required) |
| `--comment` | optional comment to post with the transition |
| `--field key=value` | field to set on the transition (repeatable), e.g. `resolution={"name":"Fixed"}` |

### `atl jira issue assign`

Set or clear the issue assignee via the dedicated assignee endpoint. Exactly one
of `--to`, `--me`, `--none` is required (else exit 2). `--me` resolves the
authenticated user's DC username first.

```bash
atl jira issue assign PROJ-1 --me            # take the ticket
atl jira issue assign PROJ-1 --to jdoe       # hand it to a DC username
atl jira issue assign PROJ-1 --none          # unassign
```

→ `{ "key": "PROJ-1", "status": "assigned", "assignee": "jdoe" }` (`"unassigned"`
and an empty `assignee` with `--none`).

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--to` | DC username to assign the issue to |
| `--me` | assign to the authenticated user |
| `--none` | remove the assignee |

Find usernames with `atl jira user search '<name>'`; `--field assignee=<name>`
on `update` does **not** work (Jira DC expects an object there — use `assign`,
or `--field 'assignee={"name":"jdoe"}'`).

### `atl jira issue comment {add,list,delete}`

Manage Jira wiki comments. `comment` is a subcommand group.

```bash
echo "Checked on staging — confirmed fixed." \
  | atl jira issue comment add PROJ-1 --from-file -
atl jira issue comment add PROJ-1 --from-md note.md  # markdown, converted to wiki
atl jira issue comment list PROJ-1                 # {key, comments:[{id,author,created,body}]}; -o id → ids
atl jira issue comment delete PROJ-1 <COMMENT-ID>  # see the id from `comment list`
```

Flags (`add`):

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--from-file` | comment body file (wiki markup) or `-` for stdin (default stdin) |
| `--from-md` | markdown comment file or `-` for stdin; converted to wiki, fail-closed (exit 8) |

### `atl jira issue link {add,list,delete,suggest}`

Manage typed links between issues. `link` is a subcommand group.

```bash
atl jira issue link add PROJ-1 --to PROJ-2 --type blocks
atl jira issue link add PROJ-3 --to PROJ-1 --type "is cloned by"
atl jira issue link list PROJ-1                    # {key, links:[{id,direction,type,type_name,key}]}; -o id → link ids
atl jira issue link delete <LINK-ID>               # see the id from `link list`
atl jira issue link suggest --csv links.csv         # dry-run missing-link candidates only
```

Flags (`add`):

| flag | description |
|---|---|
| `PROJ-1` | source issue key (positional, required) |
| `--to` | target issue key (required) |
| `--type` | link type name (required; see `atl jira link-types`) |

`suggest` is read-only. It expects a reviewed CSV plan with `source`, `target`,
`type`, and optional `rationale` columns. Common aliases such as `from`, `to`,
`link_type`, and `reason` are accepted. For each source issue, it reads current
outward Jira links and emits only plan rows that are still missing:

```csv
source,target,type,rationale
PROJ-1,PROJ-2,Blocks,dependency found during review
```

### `atl jira issue link-epic`

Set the Epic Link custom field on an issue (classic Jira Data Center boards).

```bash
atl jira issue link-epic PROJ-42 --epic PROJ-1
```

Flags:

| flag | description |
|---|---|
| `PROJ-42` | child issue key (positional, required) |
| `--epic` | epic issue key (required) |

### `atl jira issue plan apply`

Preview or apply a guarded CSV operation plan. The default mode is **dry-run**:
the command re-reads current Jira state, reports `would_apply`,
`already_satisfied`, `blocked`, or `failed`, and performs no writes. Write mode
requires both `--apply` and `--confirm APPLY`.

```bash
atl jira issue plan apply --csv plan.csv
atl jira issue plan apply --csv plan.csv --allow-ops link,label_add --apply --confirm APPLY
```

CSV columns:

| column | description |
|---|---|
| `op` | `link`, `label_add`, `label_remove`, `comment`, or `field` |
| `source` | issue key to read/write |
| `target` | target issue key for `link` |
| `type` | Jira link type for `link` |
| `field` | field id/name for `field` |
| `value` | label(s), comment body, or field value |
| `rationale` | optional audit note |

Flags:

| flag | description |
|---|---|
| `--csv` | operation plan CSV (required) |
| `--allow-ops` | comma-separated allowed operations (default `link`) |
| `--allow-fields` | comma-separated field ids/names allowed for `field` operations |
| `--apply` | perform writes instead of dry-run |
| `--confirm` | must be exactly `APPLY` when `--apply` is set |

### `atl jira issue attachment {list,get,upload}`

List, download, or upload issue attachments. `get` accepts either the attachment
id or the filename in `--id`; server-provided filenames are reduced to a safe
basename before writing to the target directory.

```bash
atl jira issue attachment list PROJ-1                    # {key, attachments:[...]}; -o id → ids
atl jira issue attachment get PROJ-1 --id 42 --into ./attachments
atl jira issue attachment get PROJ-1 --id spec.xlsx
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--id` | attachment id or filename (`get`, required) |
| `--into` | output directory (`get`, default `.`) |
| `--file` | local file path (`upload`, required) |

### `atl jira issue images`

Download image attachments of an issue to files (useful for agent vision).

```bash
atl jira issue images PROJ-1
atl jira issue images PROJ-1 --into /tmp/proj1-images
```

Flags:

| flag | description |
|---|---|
| `PROJ-1` | issue key (positional, required) |
| `--into` | output directory (default `mirror-jira/<KEY>.assets/`) |

### `atl jira issue history`

Show an issue's changelog (who changed what, when), via the DC-universal
`?expand=changelog` form.

```bash
atl jira issue history PROJ-1   # {key, history:[{id,author,created,items:[{field,from,to}]}]}
```

### `atl jira issue labels`

Add and/or remove labels without clobbering labels set by others (uses the
field-update verb).

```bash
atl jira issue labels PROJ-1 --add bug,backend [--remove wontfix]
```

Flags: `--add` / `--remove` (comma-separated; at least one required, else exit 2).

### `atl jira issue check`

Audit that required/important fields are populated — a CI / pre-transition gate.
Exits **8** (`ErrCheckFailed`) when a `--require` field is empty (distinct from a
transport/auth error), after emitting the report on stdout.

```bash
atl jira issue check PROJ-1 --require assignee,fixVersions [--warn priority]
```

`--warn` defaults to `assignee,priority,components,fixVersions,description`; pass
`--warn ""` to opt out of warnings. A check that would audit nothing (no
`--require` and `--warn ""`) is a usage error (exit 2).

### `atl jira issue refs`

Extract artifact references from one issue or from a JQL selection. This reuses
the same deterministic classifier as `jira planning report`: links are classified
as `doc`, `design`, `jira`, `chat`, or generic `link`.

```bash
atl jira issue refs PROJ-1
atl jira issue refs --jql "project=PROJ" --limit 100
atl jira issue refs --jql "project=PROJ" -o text
```

Pass exactly one of positional `KEY` or `--jql` (else exit 2). `--fields` can add
extra fields to fetch before extraction; description and comments are always
included.

### `atl jira issue tree`

Build a read-only epic-to-child tree from a JQL selection using a configurable
epic field. Children whose parent epic is not included in the JQL result are
grouped under `external_epics`; selected non-epic issues without an epic are
listed under `orphans`.

```bash
atl jira issue tree --jql "project=PROJ" --epic-field customfield_10001
atl jira issue tree --jql "project=PROJ" --epic-field customfield_10001 -o text
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query selecting issues (required) |
| `--epic-field` | field id/name containing parent epic key (required) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to fetch |

### `atl jira issue delete`

Permanently delete an issue. Jira Data Center has **no trash** for issues, so this
is irreversible and requires `--force`.

```bash
atl jira issue delete PROJ-1 --force [--delete-subtasks]
```

### `atl jira pull`

Export issues matching a JQL query to disk. Each issue becomes three files:
`<KEY>.wiki` (the native Jira wiki body, stored byte-for-byte — the editable
source of truth, the Jira analog of a Confluence `.csf`), `<KEY>.md` (a
read-only Markdown view rendered from the wiki, regenerated best-effort on every
pull), and `<KEY>.json` (identity plus raw Jira fields). Edit the `.wiki` file,
not the `.md` view.

```bash
atl jira pull --jql "project=PROJ and sprint in openSprints()" \
  --into my-jira-mirror --limit 200
atl jira pull --jql "project=PROJ" --fields customfield_10001,customfield_10002
# also mirror each issue's image attachments and link them from the .md
atl jira pull --jql "project=PROJ and status=Open" --assets
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--into` | output root directory (default `mirror-jira`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to include in JSON snapshots; core fields needed for rendering are always included |
| `--assets` | also download each issue's image attachments into a per-issue `<KEY>.assets/` directory and link them from the `.md` (opt-in; off by default) |

With `--assets`, image attachments (media type `image/*`) are streamed into
`<KEY>.assets/<attachment-id>-<filename>` and referenced from a generated
`## Image Attachments` section in the `.md`, placed between the description and
the links. The attachment id prefix keeps duplicate filenames distinct.
Download is best-effort: a failed image is skipped (counted in `assets_skipped`
and reported via a single stderr warning), the issue is still written, and only
images that landed on disk are linked. Attachments with an empty or
`application/octet-stream` media type are skipped (same as `jira issue images`).
The raw `<KEY>.json` snapshot is unchanged — it never carries local paths.

Output layout:

```
mirror-jira/
  PROJ/
    PROJ-1.wiki             # native Jira wiki body, verbatim — the editable source
    PROJ-1.md               # read-only rendered Markdown view (regenerated on pull)
    PROJ-1.json
    PROJ-1.assets/          # only with --assets, when the issue has images
      10001-screenshot.png
    PROJ-2.wiki
    PROJ-2.md
    PROJ-2.json
```

The `.md` is a lossy, best-effort read view (headings, emphasis, `{code}`/
`{quote}`/`{panel}`, lists, tables, links, `!image!` embeds, `{color}`,
`[~mentions]`); a render failure degrades that one section to a stub comment and
never fails the pull. To change an issue body, edit `<KEY>.wiki` (or a copy of
it) and apply it with `jira issue update --from-file <KEY>.wiki` — never hand-edit
the `.md` view.

`<KEY>.json` shape:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "fields": {
    "summary": "Issue summary",
    "customfield_10001": "custom value"
  }
}
```

### `atl jira export`

Write one compact issue export artifact plus a sanitized provenance manifest.
This is for scripts and analysis that need JSONL/JSON/CSV instead of a directory
mirror. The manifest is written to `<out>.manifest.json` and stores query,
fields, format, count, CLI version, and a backend URL hash; it does not store
the backend hostname or token.

```bash
atl jira export --jql "project=PROJ" --format jsonl --out issues.jsonl
atl jira export --jql "project=PROJ" --format csv --fields customfield_10001 --out issues.csv
atl jira export --jql "project=PROJ" --format json --out issues.json --limit 0
atl jira export --keys PROJ-1,PROJ-2 --batch-size 100 --out selected.jsonl
atl jira export diff old.jsonl new.jsonl
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query; pass exactly one of `--jql`, `--ids`, or `--keys` |
| `--ids` | comma-separated numeric issue ids; generates batched `id in (...)` JQL |
| `--keys` | comma-separated issue keys; generates batched `key in (...)` JQL |
| `--batch-size` | max ids/keys per generated JQL batch (default 100) |
| `--out` | output artifact path (required; manifest path is `<out>.manifest.json`) |
| `--format` | `jsonl`, `json`, or `csv` (default `jsonl`) |
| `--limit` | max issues (0 = all; default 100) |
| `--fields` | extra comma-separated fields to include |

`jira export diff` compares compact JSONL/JSON/CSV exports by issue key (or id
when key is absent) and reports deterministic `added`, `removed`, and `changed`
identifier lists.

### `atl jira planning report`

Build a deterministic read-only planning quality report over a JQL query. The
rubric checks summary, description, assignee, optional estimate field, optional
required fields, artifact references, and optional epic decomposition.

```bash
atl jira planning report --jql "project=PROJ" \
  --estimate-field customfield_10001 \
  --epic-field customfield_10002 \
  --require fixVersions,components \
  --csv planning.csv
atl jira quality-report --jql "project=PROJ"     # compatibility alias
```

Flags:

| flag | description |
|---|---|
| `--jql` | JQL query (required) |
| `--require` | comma-separated fields that must be populated |
| `--estimate-field` | field id/name used as the estimate check |
| `--epic-field` | field id/name containing parent epic key; enables child lists and missing-epic gaps |
| `--limit` | max issues (0 = all; default 100) |
| `--csv` | optional CSV report path |

Output includes per-issue `score`, `max_score`, `level` (`good|warn|poor`),
`gaps`, extracted `refs`, and `children` for epic rows when `--epic-field` is
set. Reference kinds are deterministic categories such as `doc`, `design`,
`jira`, `chat`, and `link`.

### `atl jira fields`

List all Jira fields (system and custom) with their IDs and schema types.

```
atl jira fields
atl jira fields --name-like Epic
atl jira fields --id customfield_10001
atl jira fields --custom true --schema string --id-like customfield
```

Filters are applied client-side to Jira's field list. Available filters are
`--id`, `--id-like`, `--name-like`, `--schema`, and `--custom true|false`. Use
`field-options` when you need values allowed for a specific project and issue
type.

### `atl jira field-options`

List allowed values for a field on a specific project/issue-type combination
(uses the `createmeta` API).

```
atl jira field-options --project PROJ --type Bug --field priority
atl jira field-options --project PROJ --field customfield_10020
```

Flags:

| flag | description |
|---|---|
| `--project` | project key (required) |
| `--type` | issue type name (optional) |
| `--field` | field id or display name (required) |

### `atl jira transitions`

List the workflow transitions currently available for an issue.

```
atl jira transitions --key PROJ-1
```

### `atl jira link-types`

List the configured issue link type names.

```
atl jira link-types
```

### `atl jira me` / `atl jira user {search,get}`

Identity lookups using the Data Center username/userkey model (not Cloud
accountId). `-o id` prints the username for piping.

```
atl jira me                      # the authenticated user
atl jira user search 'alice'     # {users:[{name,key,displayName,email,active}]}
atl jira user get alice          # one user by DC username
```

### `atl jira board {list,get}` and `atl jira sprint {list,get,current,issues,add,remove}`

Agile boards & sprints, via the Data Center Agile API (`/rest/agile/1.0/`).
**Requires Jira Software** (GreenHopper); on a Core/Service-Management-only
instance the Agile endpoints 404 (exit 4). Boards and sprints are addressed by
**numeric id** — use `board list --project` to discover the id `--board` wants.

```bash
atl jira board list --project PROJ          # {boards:[{id,name,type,project_key}]}; -o id → board ids
atl jira board get 5
atl jira sprint list --board 5 [--state active|closed|future]   # {sprints:[...]}; -o id → sprint ids
atl jira sprint current --board 5           # the active sprint (exit 4 if none)
atl jira sprint issues 7 [--fields summary,status]              # issues in sprint 7; -o id → keys
atl jira sprint add 7 PROJ-1 PROJ-2         # move issues into sprint 7
atl jira sprint remove PROJ-1               # move issue(s) back to the backlog
```

`--board` must be a positive id (else exit 2). List commands expose
`next_cursor`; `--limit` is capped at 50 by the Agile API.

### `atl jira structure {get,forest,rows,values,pull-issues,export}`

Read-only Tempo Structure access via the Structure REST API
(`/rest/structure/2.0/`). Structures are addressed by numeric id. If the
Structure plugin is not installed, the endpoint is disabled, or the object is not
visible to the token, Jira returns an API error (commonly exit 4 or 6).

```bash
atl jira structure get 123
atl jira structure forest 123
atl jira structure rows 123                         # parsed forest rows; -o id -> row ids
atl jira structure rows 123 --root "release train"  # first matching subtree
atl jira structure values 123 --rows 100,101 --fields key,summary,status
atl jira structure pull-issues 123 --fields summary,status
atl jira structure export 123 --root "release train" --fields summary,status --format json --out structure.json
```

`rows` parses Structure's forest formula into a stable row list. `--root`
matches the first row by row id, item id/type/semantic, or by selected Structure
attribute values (`--root-fields`, default `key,summary`) and emits only that
row plus its descendants:

```json
{
  "structure_id": 123,
  "version": {
    "signature": 55,
    "version": 7
  },
  "rows": [
    {
      "row_id": 100,
      "depth": 0,
      "item_type": "issue",
      "item_id": "10001",
      "position": 0
    }
  ]
}
```

`values` posts selected row ids and attribute ids to the Structure value
resource. The output preserves the raw response under `raw`, exposes
`responses`, and lifts any reported inaccessible row ids to
`inaccessible_rows` so scripts can detect permission gaps.

`pull-issues` collects numeric Jira issue ids from Structure issue rows and reads
the matching Jira issues via generated `id in (...)` JQL batches. It emits:

```json
{
  "structure_id": 123,
  "issue_ids": ["10001"],
  "issues": [
    {
      "key": "PROJ-1",
      "id": "10001",
      "fields": {
        "summary": "Example"
      }
    }
  ],
  "count": 1
}
```

`export` writes a single offline artifact that combines the Structure row tree
with issue snapshots. Supported formats are `json`, `csv`, and `md`; `--out` is
required. `json` contains `rows`, `issue_ids`, and `issues`; `csv` includes row
metadata plus requested issue fields; `md` renders an indented tree for quick
review. These commands are read-only and do not write Structure data back to
Jira.

### `atl manifest create`

Write a sanitized manifest for a local mirror or snapshot root. The manifest
counts files/bytes/extensions and records reproducibility metadata such as the
source command, selectors, fields, include flags, ATL version, and backend URL
hashes. It never writes backend hostnames or tokens.

```bash
atl manifest create --root mirror-jira --service jira --selector 'jql=project=PROJ' --fields summary,status
atl manifest create --root mirror-conf --service confluence --out mirror-conf/manifest.json
```

Flags:

| flag | description |
|---|---|
| `--root` | local mirror/snapshot root directory (required) |
| `--out` | manifest output path (default `<root>/manifest.json`) |
| `--service` | optional `jira`, `confluence`, or `generic` |
| `--selector` | comma-separated selectors to record |
| `--fields` | comma-separated field names/ids to record |
| `--include` | comma-separated include flags to record |
| `--command` | command string to record (default `atl manifest create`) |

---

## `atl version`

Print the current binary version.

```
atl version
atl version -o text
```

---

## Workflow: pull → edit → validate → push

This is the canonical edit loop for Confluence pages:

```bash
# 1. Pull the page (and its draw.io/image assets if needed)
atl conf pull --id 12345678 --assets --into mirror

# 2. Inspect the on-disk layout
#    mirror/DOCS/parent/child/child.csf   ← your source of truth
#    mirror/DOCS/parent/child/child.md    ← human-readable view

# 3. Edit child.csf directly.
#    Tip: read child.md for orientation; edit child.csf for correctness.

# 4. Validate before pushing
atl conf validate mirror/DOCS/parent/child/child.csf

# 5. Dry-run to see what fragments change
atl conf push --dry-run mirror/DOCS/parent/child/child.csf

# 6. Push (version gate is automatic)
atl conf push mirror/DOCS/parent/child/child.csf

# If exit 5 (version conflict): someone else edited the page.
# Re-pull, re-apply your changes, then push again.
atl conf pull --id 12345678 --into mirror
```

For a whole space:

```bash
atl conf pull --space DOCS --into mirror
# ... edit files ...
atl conf status mirror                # see which files are dirty
atl conf push mirror/DOCS/           # push all dirty files under DOCS/
```

For Jira issues the workflow is read-heavy:

```bash
atl jira pull --jql "project=PROJ and status=Open" --into mirror-jira
# read mirror-jira/PROJ/PROJ-1.md  and  mirror-jira/PROJ/PROJ-1.json
# make changes via commands:
atl jira issue update PROJ-1 --summary "Revised title"
atl jira issue transition PROJ-1 --to "In Review"
atl jira issue comment add PROJ-1 --from-file - <<'EOF'
Updated as discussed in today's meeting.
EOF
```
