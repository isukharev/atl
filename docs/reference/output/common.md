# Common output and error contracts

Global output modes, binary identity, sentinel exit classes, practical recovery rules, and verbose diagnostics.

[Reference index](README.md) · [Documentation home](../../README.md)

The default success channel is JSON; build identity is one compact example:

```json
{"version":"0.6.0","commit":"<source revision>","build_state":"clean"}
```

## Output formats

`atl` accepts a global `-o` / `--output` flag (default `json`). The three modes:

| Mode | Flag | What is written to stdout |
|---|---|---|
| **json** | `-o json` (default) | Indented, HTML-unescaped JSON; one object per command |
| **text** | `-o text` | Human-readable text for commands with an explicit text projection; unsupported commands return exit 2 before config, stdin, or network access and never emit JSON |
| **id** | `-o id` | Primary identifier(s) one per line (issue keys, page IDs, attachment IDs) — for safe piping into `xargs`. Only commands that register an id projection support this; others return exit 2 before config, stdin, self-update, or network access |

Shell completion for the three values is registered on the root flag.

## `emit()` — JSON / text output

`emit(cmd, v, textFn)` is the standard result renderer:

- With `-o json`: writes `v` as indented JSON to stdout. HTML escaping is disabled (`&`, `<`, `>` pass through literally).
- With `-o text` and a non-nil `textFn`: calls `textFn()` and writes the result to stdout.
- With `-o text` and a nil `textFn`: returns exit 2 as a defensive backstop;
  the command-tree preflight normally rejects unsupported text before `RunE`.
- With `-o id`: returns exit 2 (usage error) — use `emitID` for commands that export identifiers.

## `emitID()` — JSON / text / id output

`emitID(cmd, v, textFn, idsFn)` extends `emit` with an id projection:

- With `-o id`: calls `idsFn()` and prints each returned string on its own line. No JSON envelope.
- With `-o json` or supported `-o text`: delegates to `emit` (same rules as above).
- Commands that have no meaningful identifier set `ids = nil`; `emitID` then returns exit 2 for `-o id`.

## Error output

On failure `atl` writes to **stderr**, never stdout, so a piped JSON result on stdout is never
contaminated. The format follows `-o`:

- **`-o json` (default):** `{"error":"<message>","code":N,"kind":"<stable-kind>","remediation":"<stable-action>","recovery":{...}}` (one JSON object, newline-terminated).
- **`-o text`:** `error: <message>`.

The existing `error` and `code` fields remain compatible. `kind` is always
present; `remediation` is deterministic guidance, not an instruction to execute
automatically. Both are derived from local sentinels/typed metadata, never by
parsing backend prose. Current exit classes map to `unexpected_error`,
`usage_error`, `authentication_failed`, `not_found`, `version_conflict`,
`forbidden`, `configuration_error`, and `check_failed`. Typed specializations
include `read_only_policy`, `transport_error`, `rate_limited`,
`output_limit_exceeded`, and `api_error` without changing their exit code.
`recovery` is an additive schema-v1 object shared with MCP. Its closed `action`
may be more precise than the compatibility `remediation`; `retry_safe` refers
only to replaying the exact same invocation, not to the safety of an entire
multi-step recovery workflow. Selection/version facts are emitted only after
their numeric invariants validate, otherwise recovery falls back without facts.
`rate_limited` uses
`remediation:"wait_before_retry"` after the bounded replay-safe read retry
policy is exhausted; it never authorizes an immediate repeated request or a
write retry. `output_limit_exceeded` uses
`remediation:"narrow_or_raise_bound"` when an otherwise valid encoded MCP
result exceeds the caller-selected `max_bytes`; the rejected result is not
partial evidence. A missing command registration invariant is
`internal_error`/`report_bug` (still exit 8), not a user check failure.

## Binary identity

`atl version` returns the stable object
`{version,commit,build_state}`. `commit` is a full source revision or
`"unknown"`; `build_state` is `"clean"`, `"dirty"`, or `"unknown"`.
Supported Makefile and release builds stamp both values, while an ordinary Go
build may use compiler VCS metadata. The object has no build timestamp and is
informational only: it is not an input to self-update or signature trust.
`atl version -o text` remains the bare version, and `atl --version` retains its
existing one-line Cobra form.

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
| `7` | `exitConfig` | `domain.ErrConfig` | Invalid/incomplete configuration, including a missing backend URL/PAT or invalid named view |
| `8` | `exitCheckFailed` | `domain.ErrCheckFailed` | A check or safety precondition failed, including read-only policy refusal |

## Practical notes

When read-only policy blocks a mutation, the normal JSON error envelope keeps
`error` and `code:8` and adds stable `policy:"read_only"` plus the full
`command` path. The values come from typed local policy metadata, never backend
text. Its recovery action requires human approval and is never retry-safe. Text
output remains one concise `error:` line.

When scoped content policy blocks a write, exit `8` instead uses
`kind:"content_policy"`, `remediation:"request_human_approval"`, and
`policy:"content"`. The structured `denial` names the preflight or resolved
phase, closed reason, verbs, target, deciding layer/rule, allowed verbs,
non-path policy source, per-layer digest, advice, and retry safety. It never
reclassifies a backend `403` as policy denial. A resolving read that failed
transiently is retry-safe; policy scope, explicit deny, required-policy,
digest, and backend-binding failures are not.

This additive stderr/MCP error schema does not change a successful result or
any Confluence/Jira mirror-derived document. No durable document-format marker
is bumped.

- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token configured);
  `3` = "the token you gave me was refused." React differently: `7` → run `/atl:setup`;
  `3` → replace the PAT via `auth login`.
- Codes `3` vs `6` are distinct: `3` = authentication failure (re-auth); `6` = authorization
  failure (the identity is known but lacks permission — surface to the user).
- **Confluence version gates use exit `5`.** This includes `conf push` and
  qualified comment reads whose `--expected-version` no longer matches. Jira
  writes are last-writer-wins; `5` is never returned from Jira commands. `jira push` guards staleness with an app-layer
  compare-and-swap instead: a drift refusal is exit `8` (`ErrCheckFailed`), not `5`. A server-side
  HTTP 409 on a Jira write (locked issue, workflow veto) stays a generic conflict (exit `1`), also
  distinct from `5` (#66).
- Error-severity CSF validation failures are one gate contract across
  `conf validate`, `conf push`, `conf page create`, and `conf blog create`:
  `ErrCheckFailed` / exit `8`, `kind:"check_failed"`,
  `remediation:"review_failed_check"`, and the established closed recovery
  `{action:"inspect_failure",retry_safe:false}`. Existing command result
  objects and `problems[]` remain on stdout. An uncontended local push snapshot
  rejects invalid content before backend construction; an active mirror
  mutation retains the existing lock/config error precedence. Invalid content
  never reaches the network. This covers malformed XML and unsupported nesting
  depth. `--cloud-compat` adds only `"warning"`-severity findings, so it never
  changes this exit status.
- `jira issue check` exits `8` (`ErrCheckFailed`) when a field listed in `--require` is empty — a
  distinct code so a CI gate can tell "fields missing" from a transport/auth error. The full result
  (including `missing_required` and `missing_warn`) is still emitted to stdout before the exit.
- Flag-parse failures (unknown flag, bad value) are wrapped as `ErrUsage` → exit 2.
  This is enforced by a `SetFlagErrorFunc` on the root command, so it applies to every subcommand.
- Every public group, leaf, and intentional group/leaf hybrid is part of one
  exhaustive command registry. A pure group with no arguments prints help and
  exits 0; an unknown child or stray positional token is `ErrUsage` → exit 2
  before configuration, stdin, self-update, or network access. Every mutating
  leaf also declares its mutation profile and any profile-specific guard flags.

---

## `--verbose` / `ATL_VERBOSE=1`

When set, `httpx.SetTrace` attaches a request/response logger to stderr before any HTTP call.
The bearer token and query values are **never** written to the trace (query parameter names remain
visible with redacted values). stdout stays reserved for the result, so verbose output never
corrupts the JSON stream. HTTP API error strings use the same query-value redaction and omit URL
fragments, so a failed request does not reintroduce JQL/CQL/selectors through stderr.

---
