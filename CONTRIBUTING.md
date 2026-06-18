# Contributing to atl

Thank you for your interest in contributing! This document explains how to get
started and what to expect when opening a pull request.

---

## Dev setup

**Requirements:** Go 1.26 or later, `make`, `git`.

```bash
git clone https://github.com/isukharev/atl.git
cd atl
go build ./...          # build everything
go test ./...           # run unit tests
```

Or, using the Makefile targets:

```bash
make build   # builds ./atl binary
make test    # unit tests with -race
make lint    # golangci-lint run
```

### Devcontainer

A `.devcontainer/devcontainer.json` is provided. Open the repo in VS Code and
choose **Reopen in Container** — Go, gopls, and golangci-lint are pre-installed.

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
- **Live integration tests** are guarded by an env flag so they never run in CI
  unintentionally:

  ```bash
  ATL_INTEGRATION=1 ATL_TEST_PAGE_ID=<your-throwaway-page-id> go test ./... -run Integration
  ```

  Use a page you own and can safely overwrite; do not hard-code real page IDs in
  test files.

---

## Commits and pull requests

- Keep PRs **small and focused** — one logical change per PR.
- Commit subject line: `<type>: <short summary>` (conventional-ish, e.g.
  `fix: handle empty body in push`, `feat: add fragment resolution`). Keep it
  under 72 characters.
- Sign-off (`git commit -s`) is optional but appreciated.
- All PRs must pass **CI** (build + test + lint) and require **at least one
  review** — `main` is a protected branch.
- Do not commit secrets, PATs, or any credentials. See `.gitignore` for the
  key/token patterns that are explicitly excluded.

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
