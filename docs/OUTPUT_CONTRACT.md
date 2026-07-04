# `atl` Output Contract

This document is the authoritative reference for how `atl` communicates results and failures.
It is derived from `internal/cli/root.go` (`codeFor`, `emit`, `emitID`, `writeError`, exit constants).

---

## Output formats

`atl` accepts a global `-o` / `--output` flag (default `json`). The three modes:

| Mode | Flag | What is written to stdout |
|---|---|---|
| **json** | `-o json` (default) | Indented, HTML-unescaped JSON; one object per command |
| **text** | `-o text` | Human-readable plain text; only available for commands that supply a text renderer — commands without one fall back to JSON |
| **id** | `-o id` | Primary identifier(s) one per line (issue keys, page IDs, attachment IDs) — for safe piping into `xargs`. Only commands that register an id projection support this; others return exit 2 |

Shell completion for the three values is registered on the root flag.

### `emit()` — JSON / text output

`emit(cmd, v, textFn)` is the standard result renderer:

- With `-o json`: writes `v` as indented JSON to stdout. HTML escaping is disabled (`&`, `<`, `>` pass through literally).
- With `-o text` and a non-nil `textFn`: calls `textFn()` and writes the result to stdout.
- With `-o text` and a nil `textFn`: falls back to JSON (no text view defined for this command).
- With `-o id`: returns exit 2 (usage error) — use `emitID` for commands that export identifiers.

### `emitID()` — JSON / text / id output

`emitID(cmd, v, textFn, idsFn)` extends `emit` with an id projection:

- With `-o id`: calls `idsFn()` and prints each returned string on its own line. No JSON envelope.
- With `-o json` or `-o text`: delegates to `emit` (same rules as above).
- Commands that have no meaningful identifier set `ids = nil`; `emitID` then returns exit 2 for `-o id`.

### Error output

On failure `atl` writes to **stderr**, never stdout, so a piped JSON result on stdout is never
contaminated. The format follows `-o`:

- **`-o json` (default):** `{"error": "<message>", "code": <exit-code>}` (one JSON object, newline-terminated).
- **`-o text`:** `error: <message>`.

The `code` field in the JSON error object echoes the process exit code so a caller that captured only
stderr can still classify the failure without inspecting the exit code separately.

---

## Sentinel → exit-code matrix

Adapters wrap domain conditions as `fmt.Errorf("%w: ...", domain.ErrXxx)`. The CLI's `codeFor`
maps them via `errors.Is`:

| Exit code | Constant | Sentinel | Meaning |
|---|---|---|---|
| `0` | `exitOK` | — | Success |
| `1` | `exitGeneric` | (default) | Unexpected error; read the message |
| `2` | `exitUsage` | `domain.ErrUsage` | Bad flags/args; flag-parse errors are also mapped here |
| `3` | `exitAuth` | `domain.ErrAuth` | Server **rejected** the token (expired/revoked/wrong instance) |
| `4` | `exitNotFound` | `domain.ErrNotFound` | Resource does not exist or is not visible |
| `5` | `exitVersionConfl` | `domain.ErrVersionConflict` | Confluence push: remote moved past synced version |
| `6` | `exitForbidden` | `domain.ErrForbidden` | Authenticated but lacks permission for this object |
| `7` | `exitConfig` | `domain.ErrConfig` | Backend URL or PAT not set — setup incomplete |
| `8` | `exitCheckFailed` | `domain.ErrCheckFailed` | `jira issue check`: a `--require` field is empty (gate failed) |

### Practical notes

- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token configured);
  `3` = "the token you gave me was refused." React differently: `7` → run `/atl:setup`;
  `3` → replace the PAT via `auth login`.
- Codes `3` vs `6` are distinct: `3` = authentication failure (re-auth); `6` = authorization
  failure (the identity is known but lacks permission — surface to the user).
- **Only Confluence `push` uses the version gate** (`5`). Jira writes are last-writer-wins; `5` is
  never returned from Jira commands.
- `conf validate` exits non-zero (exit 1) when the CSF is not well-formed. Treat any `"error"`-
  severity problem in its `problems[]` array as a hard blocker before pushing.
- `jira issue check` exits `8` (`ErrCheckFailed`) when a field listed in `--require` is empty — a
  distinct code so a CI gate can tell "fields missing" from a transport/auth error. The full result
  (including `missing_required` and `missing_warn`) is still emitted to stdout before the exit.
- Flag-parse failures (unknown flag, bad value) are wrapped as `ErrUsage` → exit 2.
  This is enforced by a `SetFlagErrorFunc` on the root command, so it applies to every subcommand.

---

## `--verbose` / `ATL_VERBOSE=1`

When set, `httpx.SetTrace` attaches a request/response logger to stderr before any HTTP call.
The bearer token is **never** written to the trace. stdout stays reserved for the result, so
verbose output never corrupts the JSON stream.

---

## Stable Snapshot Notes

`atl jira pull` writes one read-only markdown view and one JSON snapshot per issue. The JSON snapshot
is an object with stable identity at the top level and raw Jira fields under `fields`:

```json
{
  "key": "PROJ-1",
  "id": "10001",
  "fields": {
    "summary": "Issue summary"
  }
}
```

`--fields` on `jira pull` adds requested fields to that `fields` map; the command still includes the
core fields needed to render the markdown view and choose the project/key path.

`atl jira export --jql ... --out FILE --format jsonl|json|csv` writes one compact artifact and a
sidecar manifest at `FILE.manifest.json`. `--ids` and `--keys` can be used instead of `--jql` to
generate batched `id in (...)` / `key in (...)` queries. Stdout remains the normal `emit()` JSON
summary:

```json
{
  "path": "issues.jsonl",
  "manifest_path": "issues.jsonl.manifest.json",
  "format": "jsonl",
  "count": 1
}
```

JSONL emits one `JiraIssueSnapshot` object per line (`{key,id,fields}`); JSON emits
`{manifest,issues}`; CSV emits `key,id` followed by the deterministic field list. The manifest
stores query mode, generated queries when applicable, fields, format, count, CLI version, and a
backend URL hash only:

```json
{
  "command": "atl jira export",
  "format": "jsonl",
  "query_mode": "jql",
  "jql": "project=PROJ",
  "count": 1,
  "backend": {
    "service": "jira",
    "url_hash": "sha256:..."
  }
}
```

The backend hostname and PAT are never written to the manifest.

`atl jira export diff OLD NEW` reads JSONL/JSON/CSV compact exports and reports issue identifiers:

```json
{
  "old_count": 1,
  "new_count": 2,
  "added": ["PROJ-2"],
  "changed": ["PROJ-1"]
}
```

`atl jira planning report --jql ...` returns deterministic per-issue quality rows:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "score": 4,
      "max_score": 5,
      "level": "warn",
      "gaps": ["missing_artifact_ref"],
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ],
  "summary": {
    "good": 0,
    "warn": 1,
    "poor": 0
  }
}
```

When `--csv FILE` is passed, the same command writes a deterministic CSV sidecar
and includes `csv_path` in the JSON result.

`atl jira issue refs <KEY>` and `atl jira issue refs --jql ...` return
deterministic artifact references per issue:

```json
{
  "jql": "project=PROJ",
  "count": 1,
  "issues": [
    {
      "key": "PROJ-1",
      "summary": "Implement capability",
      "type": "Story",
      "refs": [
        {
          "url": "https://docs.example.com/spec",
          "kind": "doc"
        }
      ]
    }
  ]
}
```

`atl jira issue tree --jql ... --epic-field ...` returns a normalized
epic-to-child tree:

```json
{
  "jql": "project=PROJ",
  "epic_field": "customfield_10001",
  "count": 3,
  "epics": [
    {
      "key": "PROJ-1",
      "summary": "Parent",
      "type": "Epic",
      "children": [
        {
          "key": "PROJ-2",
          "summary": "Child",
          "type": "Story",
          "epic": "PROJ-1"
        }
      ]
    }
  ]
}
```

`external_epics` contains children whose epic key is not part of the selected
JQL result. `orphans` contains selected non-epic issues with no epic field. Both
fields are omitted when empty.

`atl jira issue link suggest --csv links.csv` is read-only and returns missing
link candidates from a reviewed CSV plan:

```json
{
  "path": "links.csv",
  "planned_count": 2,
  "count": 1,
  "candidates": [
    {
      "source": "PROJ-1",
      "target": "PROJ-2",
      "type": "Blocks",
      "rationale": "dependency found during review",
      "row": 2
    }
  ]
}
```

Rows whose outward link already exists on the source issue are omitted from
`candidates`. The command performs no Jira writes.

`atl jira structure rows <ID>` returns a parsed read-only view of a Tempo Structure forest:

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

For non-root rows, `parent_row_id` is present. `-o id` prints Structure row ids
one per line. `--root` emits the first matching row plus descendants; matching is
by row metadata first and then by Structure values fetched through
`--root-fields` (default `key,summary`).

`atl jira structure values <ID> --rows ... --fields ...` preserves the backend
value matrix under `responses` and `raw`; if the backend reports permission
gaps, normalized row ids are also exposed as `inaccessible_rows`.

`atl jira structure pull-issues <ID>` returns:

```json
{
  "structure_id": 123,
  "version": {"signature": 55, "version": 7},
  "rows": [],
  "issue_ids": ["10001"],
  "issues": [{"key": "PROJ-1", "id": "10001", "fields": {}}],
  "count": 1
}
```

`atl jira structure export <ID> --out FILE --format json|csv|md` writes the
artifact and returns a small result object:

```json
{
  "path": "structure.json",
  "format": "json",
  "structure_id": 123,
  "row_count": 1,
  "issue_count": 1
}
```

JSON export artifacts contain `{structure_id,version,rows,issue_ids,issues}`.
CSV export artifacts contain row metadata (`row_id`, `depth`, `parent_row_id`,
`item_type`, `item_id`, `issue_key`, `issue_id`) plus requested issue fields.
Markdown export artifacts render an indented tree for review.
