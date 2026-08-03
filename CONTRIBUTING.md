# Contributing to atl

Thank you for your interest in contributing! This document explains how to get
started and what to expect when opening a pull request.

---

## Dev setup

**Requirements:** the exact Go patch declared by `go.mod`, `make`, `git`.

```bash
git clone https://github.com/isukharev/atl.git
cd atl
go build ./...          # build everything
make test               # run core product tests
make agent-eval-contract # run the complete deterministic evaluator contract
```

Or, using the Makefile targets:

```bash
make build   # builds ./atl binary
make test    # core product tests
make race    # core product tests with the race detector
make lint    # golangci-lint run
make check-maintainer-contract # verifies the local Go/tooling contract
make check-package-boundary # verifies the core/heavy dependency split
```

### Devcontainer

A `.devcontainer/devcontainer.json` is provided. Open the repo in VS Code and
choose **Reopen in Container** — the exact Go patch from `go.mod`, gopls, and
golangci-lint are pre-installed. The container uses `GOTOOLCHAIN=local`, so a
stale image fails the maintainer-contract check instead of downloading a
different Go toolchain automatically.

---

## Code style

- Format with **`gofmt`** (enforced by CI).
- Organize imports with **`goimports`** (`local-prefixes = github.com/isukharev/atl`).
- The codebase follows a **ports-and-adapters (hexagonal) architecture** — see
  [`docs/architecture.md`](docs/architecture.md) before adding new packages.
- Keep packages small and dependency-free at the core; put I/O and external
  calls in adapters.

---

## Testing

- **Unit tests** live alongside the code they test (`*_test.go` in the same
  package). All new logic must have unit tests.
- **Live integration tests** are opt-in and load their private selectors from
  `.env.integration`, so they never run in CI unintentionally:

  ```bash
  make integration
  make live-smoke
  ```

  Start with the read-only fixture checks. A live write requires a separately
  reviewed plan, explicit authority, an owned disposable target, and a cleanup
  plan. Never hard-code real object IDs or backend details in tracked files.

- **Agent-evaluation tests** have a separate deterministic gate. Product
  changes run the small compatibility contract in CI; evaluator/corpus changes
  run `make agent-eval-contract`. Release tags additionally run the evaluator
  race gate on Linux. Generic synthetic backend fixtures live outside the
  evaluator so product tests cannot acquire a hidden heavy dependency.

---

## Documentation

The focused files under `docs/reference/cli/` and `docs/reference/output/` own
command and output prose. The old `docs/usage.md` and
`docs/OUTPUT_CONTRACT.md` paths are generated compatibility indexes; do not edit
them directly. If a canonical section moves, update the split map and run:

```sh
go run ./scripts/check-reference-split -root . -write
make check-reference-split
```

---

## Commits and pull requests

- Keep PRs **small and focused** — one logical change per PR.
- Non-trivial work should have a GitHub issue before implementation. Link the
  issue from the PR and include the roadmap ID or parent initiative when the work
  is roadmap-driven. See [`docs/github-issue-workflow.md`](docs/github-issue-workflow.md).
- Commit subject line: `<type>: <short summary>` (conventional-ish, e.g.
  `fix: handle empty body in push`, `feat: add fragment resolution`). Keep it
  under 72 characters.
- Sign-off (`git commit -s`) is optional but appreciated.
- All PRs must pass **CI** (build + test + lint) and require **at least one
  review** — `main` is a protected branch.
- Do not commit secrets, PATs, or any credentials. See `.gitignore` for the
  key/token patterns that are explicitly excluded.

## Maintainer documentation

[`AGENTS.md`](AGENTS.md) is the binding cross-agent repository contract.
Provider-specific overlays do not replace it. The
[documentation index](docs/README.md#maintainers) routes to architecture,
issue/PR lifecycle, generated plugins, releases, documentation indexing, and
evaluation operations. The focused [maintainer workflows](docs/maintainers/README.md)
cover preflight, verification, landing, recovery, and live validation.
[`STANDARDS.md`](STANDARDS.md) explains which document owns each maintainer topic
so policy is updated in one place.

---

## Releases

Releases are cut by the maintainer. When a release is ready:

1. The maintainer tags `vX.Y.Z` on `main`.
2. CI builds cross-platform binaries, signs them, and publishes a GitHub
   Release with checksums.
3. `atl update` picks up the new release automatically via signature-verified
   self-update.

Contributors do not need to manage releases.

---

## Security

If you discover a security vulnerability, please **do not open a public issue**.
Follow the process described in [`SECURITY.md`](SECURITY.md).
