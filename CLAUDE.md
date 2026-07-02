# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`atl` is a single static Go binary: a Git-style CLI that mirrors Confluence pages and Jira
issues to disk in their **native storage formats** (Confluence Storage Format `.csf`; Jira
wiki), lets agents edit/search the bytes directly, and pushes under an optimistic version
gate. The `.csf` bytes are the substrate — there is no lossy Markdown round-trip, so the
write path must never convert bodies. The `.md` files in a mirror are read-only views and
are regenerated best-effort (a render failure is swallowed; it never fails a pull).

## Commands

```sh
make build    # CGO_ENABLED=0 build -> ./atl (version-stamped via -ldflags)
make test     # go test ./...
make race     # go test -race ./...
make lint     # golangci-lint run (v2 config in .golangci.yml)
make vet      # go vet ./...
go test ./internal/csf/ -run TestParse   # single package / single test
```

Live integration tests hit a real backend and are gated by env so they never run in CI.
Keep your DC URL/PATs out of the repo: copy the template to a gitignored `.env.integration`
and run them through the Makefile, which sources it and sets `ATL_INTEGRATION=1`:

```sh
cp .env.integration.example .env.integration   # fill in DC URL, PATs, throwaway test objects
make integration                               # runs only -run Integration, never in CI
```

Or pass the env inline for a one-off (no file): `ATL_INTEGRATION=1 CONFLUENCE_URL=… TEST_CONFLUENCE_PAT=…
ATL_TEST_PAGE_ID=<throwaway-page-id> go test ./... -run Integration`. Jira `field-options` coverage
also needs `ATL_TEST_JIRA_PROJECT` + `ATL_TEST_JIRA_FIELD` (e.g. `priority`).

Requires Go 1.26+. CI enforces `gofmt` and `goimports` (`local-prefixes = github.com/isukharev/atl`)
and pins `golangci-lint` (v2.12.2) and `govulncheck` (v1.4.0) — match these locally to avoid lint drift.

## Architecture

Hexagonal (ports & adapters). The dependency rule is strict — internalize it before adding code:

- **`internal/domain`** — the hub. Ports (`DocStore`, `Tracker`), the `Resource`/`Ref` model,
  registry ports (`AssetSink`, `AssetResolver`, `UserResolver`), and sentinel errors.
  Imports nothing from the rest of the tree; everything else implements or consumes it.
  Adapters and CLI never import each other.
- **`internal/adapter/{confluence,jira}`** — REST adapters implementing the ports. All HTTP
  goes through `internal/httpx`. Bodies are passed verbatim, never converted.
- **`internal/app`** — transport-agnostic use-cases (`ConfluenceService`, `JiraService`),
  assembled in `wire.go`. No cobra, no stdin, no filesystem beyond the mirror. A future
  server/MCP tier would call this layer directly. Note: a service method name here may differ
  from the `domain` port method it calls (e.g. `JiraService.Comment` → `Tracker.AddComment`),
  so grepping one name won't always reveal the full service→port→adapter chain.
- **`internal/cli`** — thin cobra layer: parse flags → call one use-case → `emit()` → return
  error. Command tree:
  - `conf` — search, space tree, page {get,meta,history,create,move,delete,list,open,copy},
    attachment {list,get,upload,delete}, pull, status, validate, push, comment {list,add}, me.
  - `jira` — issue {get,search,create,update,transition,comment {add,list,delete},
    link {add,list,delete},link-epic,images,check,delete,labels,history},
    board {list,get}, sprint {list,get,current,issues,add,remove}, pull, fields,
    field-options, transitions, link-types, me, user {search,get}.
  - `auth` (login,status,logout), `config` (show,set), `version`.
- **`internal/csf`** — read-only DOM parser + validator for Confluence Storage Format.
- **`internal/fragment`** — extracts/resolves opaque fragments (drawio, image, user,
  page-link, attachment) from a CSF DOM.
- **`internal/mirror`** — on-disk layout + sidecar (`.atl/state.json`, `.atl/base/`) +
  dirty/drift detection. Backend-agnostic; stores `Resource` bytes, knows nothing of HTTP/CSF.
- Shared infra: `internal/httpx` (bearer PAT auth, retries, status→sentinel mapping),
  `internal/auth` (PAT resolution: env → credentials.json), `internal/config`,
  `internal/safepath` (sanitize server-controlled path components + containment + safe writes),
  `internal/selfupdate`, `internal/version`.

Full detail: `docs/architecture.md`. Extension points (new backend, new fragment type) are
documented there.

## Conventions that affect correctness

- **Sentinel errors drive exit codes.** Adapters wrap every error as
  `fmt.Errorf("%w: ...", domain.ErrXxx)`; the CLI's `codeFor` uses `errors.Is` to map to an
  exit code: `ErrUsage`→2, `ErrAuth`→3, `ErrNotFound`→4, `ErrVersionConflict`→5,
  `ErrForbidden`→6, `ErrConfig`→7, `ErrCheckFailed`→8 (anything else→1). Do not return bare errors from layers below the CLI
  for these conditions, or the exit code degrades to 1.
- **Output is JSON by default.** `emit(cmd, v, textFn)` writes indented, HTML-unescaped JSON
  to stdout unless `-o text` AND a non-nil `textFn` are both present (pass `nil` for textFn
  when there is no human view). Logs/errors go to stderr; never interactive.
- **Optimistic version gate.** `UpdatePage` sends `expectVersion+1`; a 409 maps to
  `ErrVersionConflict` (exit 5). `--force` re-reads current and targets `current+1`. After a
  successful push, the code re-fetches to refresh the mirror; a failure there is a warning,
  not an error.
- **PAT is host-scoped.** `httpx` injects the bearer token only when the request host is empty
  or matches the configured backend host (case-insensitive) — server-supplied attachment URLs
  on other hosts get no token, and cross-host / scheme-downgrade redirects are refused. Retries:
  3 (4 attempts total); 429 for any method, but transport/5xx only for idempotent methods (POST
  is never retried, to avoid a double write); exp backoff 200ms→×2 capped at 5s with jitter,
  honoring `Retry-After` (itself capped at 30s).
- **Backend URLs must be https.** `config.CheckSecureURL` rejects a non-https backend URL for a
  non-loopback host (enforced at `config set` time and in `wire.go`); `ATL_ALLOW_INSECURE=1`
  overrides for a trusted internal http instance. `auth login` never accepts the PAT on argv —
  it reads a no-echo prompt, piped stdin, or `--from-file`.
- **CSF parsing is read-only and byte-stable.** `Parse` wraps raw bytes in a synthetic
  `<root>` (6-byte prefix; subtract when mapping error offsets) and never mutates input — the
  write path relies on this. `Validate` returns `[]Problem`; `HasErrors` (any `error`-severity
  problem) is the push gate. Warnings are advisory and do not block.
- **Fragment resolution never errors.** `fragment.Resolve` swallows all failures; an
  unresolved ref keeps its raw display/empty asset rather than failing the pull.

## Mirror & config gotchas

- **Silent CQL pull cap: 1000 pages.** `conf pull --cql` stops after 1000 IDs with no
  warning. Confluence has no "unbounded" escape; Jira `pull --limit 0` *does* mean unbounded.
- **Mirror root auto-detection.** Commands resolve the mirror root by walking up from the
  target ≤12 levels looking for an `.atl` marker dir; if none is found it defaults to
  `"mirror"`. Watch this in multi-workspace setups.
- **Drift needs a baseline.** A page is reported `Drifted` only when it has a prior synced
  version (`SyncedVersion > 0`); never-pushed pages read as clean even if the remote changed.
  Dirty detection is content-hash based, not timestamp.
- **Slugify is unicode-safe.** Page dir slugs lowercase, keep unicode letters/digits
  (Cyrillic preserved), hyphenate the rest, truncate at 80 runes, fall back to `"untitled"`.
  Space keys go through `safeSeg`, which neutralizes `.`/`..`/separators to block path
  traversal from hostile server input.
- **stdin bodies are capped at 64 MiB** (`--from-file -`); larger input is silently truncated.
- **PAT resolution order** (per service, first non-empty wins): `ATL_<SVC>_PAT` → `<SVC>_PAT`
  → `TEST_<SVC>_PAT` (only when `ATL_INTEGRATION` is set) → `~/.config/atl/credentials.json`
  (mode 0600, written atomically). Config dir:
  `ATL_CONFIG_DIR` → `$XDG_CONFIG_HOME/atl` → `~/.config/atl`. Env URLs always overlay the
  config file.
- **Self-update** runs in `PersistentPreRun` before every subcommand, best-effort (never
  blocks/errors). Throttled to 6h; skipped for dev/empty version builds or when
  `ATL_NO_UPDATE` is set; the source URL must be https. Verifies the ed25519 signature over the
  exact manifest bytes *before* parsing, then SHA-256 of the binary (public key embedded in
  `internal/selfupdate/pubkey.go`), enforces a persisted version high-water-mark (anti-rollback),
  and fails closed. It does NOT re-exec — the swapped binary takes effect on the next invocation.

## GitHub issue workflow (agent tracking)

Non-trivial work is **issue-first**: find or open a GitHub issue before changing code, so the
chain `roadmap → issue/sub-issue → agent plan → branch → PR → verification → done` stays
visible. Non-trivial = changes user-facing behaviour, public docs, CLI output, release process,
architecture, or security posture; trivial typos/formatting/local experiments may skip it. The
process is deliberately **issue-only** (labels + comments + sub-issues), not GitHub Projects.
Canonical guides live outside this file: `AGENTS.md` (cross-agent handoff rules) and
`docs/github-issue-workflow.md` (full process); issue forms are in `.github/ISSUE_TEMPLATE/`.

- **Comment an `## Agent plan` before coding** (Problem / Approach / Files / Acceptance criteria /
  Verification / Risks-non-goals); re-comment when scope changes instead of drifting silently.
- **Labels carry state + routing:** `agent-ready` → `agent-working` → `needs-human`;
  `area/{confluence,jira,sync,mcp,safety,packaging,cloud,docs}`;
  `kind/{feature,bug,research,docs,infra}`; `roadmap/{now,next,later}`.
- **Branch** with `gh issue develop <n> --checkout`; the **PR** references the issue
  (`Refs #…` / `Fixes #…`), parent initiative, and roadmap ID/section, and lists verification
  (`make test`, `make lint`). Close issues through a merged PR, not a local patch.
- **Never** put PATs, private hostnames, private page IDs / issue keys, customer names, or
  proprietary page/issue bodies in issues, PRs, comments, commits, or logs.
- `ROADMAP.md` is the **public** roadmap. Internal product/brand strategy is kept in
  local-only, gitignored files (declared "not part of public documentation") — never commit
  them to this public repo, and never point public docs/issues at them.

## House rules

- Tests live alongside code in the same package; new logic needs unit tests. Tests use
  `httptest` servers, `t.Setenv`, `t.TempDir`, and `internal/csf/testdata/sample.csf` — no
  build tags. (Never pair `t.Parallel` with `t.Setenv` — it panics.)
- **Fuzz the code that ingests server-controlled bytes.** The CSF parser and the path
  sanitizers (`internal/csf`, `internal/safepath`, `internal/mirror`) have `fuzz_test.go`
  targets whose `f.Add` seeds double as deterministic regression tests under plain `go test`;
  add seeds (and keep any `testdata/fuzz/` crash corpus) when you touch that code. Containment
  fuzzers join the sanitized segment as its own path component, not a `+".csf"` suffix (a
  suffix masks a bare-`..` regression into a harmless filename).
- **CLI output is a contract.** `internal/cli` golden tests (`testdata/golden/` + `assertGolden`,
  regenerate with `go test ./internal/cli/ -run … -update`) pin `emit()`'s JSON; keep canned
  responses free of volatile data (httptest ports, timestamps). The sentinel→exit-code matrix
  is locked in `cli_contract_test.go` — extend it when you add a sentinel.
- **Keep the shipped plugin in sync with the CLI.** `skills/` is the Claude Code plugin clients
  install to drive `atl`; it — and `docs/` — enumerate commands, flags, exit codes, and output.
  When a change alters user-facing CLI behaviour, update the matching `skills/*/SKILL.md`
  (Quick-Reference tables, examples, `USE WHEN` frontmatter, Common-Errors / exit-code blocks) plus
  `docs/usage.md` / `docs/OUTPUT_CONTRACT.md` / `CHANGELOG.md` in the **same PR**, and confirm it
  **before merging**.
- **Security-boundary tests assert the guarantee fails when the control is removed** (O_NOFOLLOW,
  atomic symlink-replace, ed25519 verify-before-parse). Tamper *inside a valid payload* so the
  control under test — not an incidental parse failure — is what rejects it.
- Never commit secrets/PATs (see `.gitignore`). The ed25519 release signing private key is
  never committed.
- Keep PRs small; commit subjects `<type>: <summary>` (e.g. `fix: handle empty body in push`).
