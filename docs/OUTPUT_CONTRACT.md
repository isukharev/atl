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
