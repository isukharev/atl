# Security Policy

## Reporting a vulnerability

Please report security issues **privately**. Do not open a public issue for a
vulnerability.

- Preferred: open a [GitHub Security Advisory](https://github.com/isukharev/atl/security/advisories/new)
  (Security → Report a vulnerability).
- Alternatively, email the maintainer at **ivan7654@gmail.com** with `[atl
  security]` in the subject.

Please include a description, affected version (`atl version`), and a reproduction
if possible. You will get an acknowledgement as soon as possible, and a fix or
mitigation timeline once the report is triaged. Coordinated disclosure is
appreciated.

## Supported versions

Only the latest released version is supported. Fixes ship in a new release.

---

## Trust model

The runtime destination inventory and air-gapped operation contract live in
[docs/network-egress.md](docs/network-egress.md). `ATL_READ_ONLY=1` prevents
mutations but not backend reads; `ATL_NO_UPDATE=1` disables the release check
but not Jira or Confluence calls.

`atl` is a single static binary that **replaces itself on disk** during
auto-update. That makes the update channel the highest-value target: whoever can
make a malicious binary look like a legitimate update gets code execution on
every user's machine. The design treats that channel as untrusted transport and
anchors trust in a key that never leaves CI.

### Signed releases (the core control)

1. Releases are produced **only** by the tagged GitHub Actions release workflow
   (`.github/workflows/release.yml`) from a commit on a protected branch — never
   by manual upload.
2. The workflow generates `manifest.json` (version + per-binary SHA-256) and
   signs its exact bytes with an **ed25519 private key** that exists only as the
   `ATL_RELEASE_PRIVATE_KEY` GitHub Actions secret. The detached signature is
   published as `manifest.json.sig`.
3. The matching **public key is compiled into every `atl` binary**
   (`internal/selfupdate/pubkey.go`).
4. On update, the CLI downloads `manifest.json` + `manifest.json.sig` and
   **verifies the signature against the embedded public key before trusting
   anything in the manifest**, including the SHA-256 of the binary it is about
   to install. Only then does it download `atl-<os>-<arch>`, check its hash
   against the signed manifest, atomically replace the running binary, and
   re-exec.

**Consequence:** an attacker who fully compromises a GitHub Release — swapping
both the binary *and* its published hash — still cannot push an update, because
they cannot forge a valid signature without the private key. A stolen hash is
worthless; only a signature minted by the CI secret is accepted.

### Fail-closed by default

Auto-update does nothing — rather than trusting unsigned data — when any of the
following hold:

- the build is a development build (`Version == "dev"`);
- no signing public key is embedded (`internal/selfupdate/pubkey.go` empty);
- the update source is not HTTPS (plain HTTP is permitted only against loopback,
  for tests);
- the signature does not verify, the download fails, or the hash mismatches;
- the user sets `ATL_NO_UPDATE=1`.

In every one of these cases the running command proceeds normally and no binary
is replaced. Manual installation via the published `install.sh` always remains
available.

The generated Homebrew launcher sets `ATL_NO_UPDATE=1` so Homebrew remains the
sole owner of package upgrades. Direct release and installer deployments retain
the signed self-update path by default.

### Defense in depth

- **Build provenance (SLSA):** each release binary carries a signed
  [build provenance attestation](https://docs.github.com/actions/security-guides/using-artifact-attestations)
  tying it to the exact source commit and workflow run. Verify with:
  ```bash
  gh attestation verify atl-linux-amd64 --repo isukharev/atl
  ```
- **SHA-256 checksums** are published per binary and inside the signed manifest.
- **PAT handling:** the per-user Personal Access Token is sent only to the
  configured Confluence/Jira host; it is never written to the mirror and never
  forwarded to a server-supplied foreign URL. Credentials live in a `0600` file
  under `~/.config/atl` (or in env).
- **Minimal dependencies:** one direct dependency (`spf13/cobra`); module
  integrity is enforced by `go.sum` and the Go checksum database.
- **CI hardening:** workflows pin actions, request least-privilege `permissions`,
  and run `govulncheck` and CodeQL.

---

## Verifying a release manually

```bash
# 1) download a binary + its checksum from the release
ver=v0.1.0; os=linux; arch=amd64
base="https://github.com/isukharev/atl/releases/download/$ver"
curl -fsSLO "$base/atl-$os-$arch"
curl -fsSL  "$base/atl-$os-$arch.sha256" | sha256sum -c -

# 2) (optional) verify build provenance
gh attestation verify "atl-$os-$arch" --repo isukharev/atl
```

### A note on `curl … install.sh | sh`

The convenience installer is fetched over TLS and run directly, so — like any
`curl | sh` — it cannot verify *itself* before executing. The signed-manifest
model protects the **auto-update** path (a release compromise cannot push a
forged update), but not this **bootstrap** step. If you want the strongest
guarantee on first install, prefer one of:

- the manual download + `sha256sum -c` (+ `gh attestation verify`) shown above, or
- `go install github.com/isukharev/atl/cmd/atl@v0.1.0` (builds from a tagged,
  checksum-pinned source tree), or
- pin the installer to a tagged commit instead of `latest`:
  `curl -fsSL https://raw.githubusercontent.com/isukharev/atl/v0.1.0/install.sh | sh`.

## For maintainers: managing the signing key

The signing key is generated **off CI** and the private half is never committed:

```bash
make genkey         # prints the public key to embed + writes a gitignored private key
```

1. Paste the printed public key into `internal/selfupdate/pubkey.go`
   (`trustedPublicKeyB64`) and commit it.
2. Add the private key as the `ATL_RELEASE_PRIVATE_KEY` secret in the protected
   `release` environment. Then delete the local copy after placing an offline
   backup in a trusted vault.
3. Rotation is staged. Generate a new pair and embed its public key, but keep the
   old CI secret active for one bridge release. Existing clients trust the old
   signature on that release, then install a binary that trusts the new key.
   After an adoption window, replace the environment secret with the new private
   key and remove any repository-scoped copy. Clients that miss the bridge
   release may need to reinstall via `install.sh`.

The release workflow verifies this continuity mechanically before building. It
derives the public key from the protected private-key secret and compares it to
the trust key in the latest published stable release's source. Missing,
malformed, non-canonical, or prematurely rotated keys fail the release without
printing private material. The first release bootstraps against its own source.

If the old private key is no longer retrievable but is still present as a CI
secret, it can still sign the bridge release; do not shadow it with the new
environment secret until that release has been published and verified.

When the old key is intentionally abandoned or unavailable even as a CI secret,
the protected release environment supports a one-release emergency reset via
`ATL_RELEASE_TRUST_RESET_TAG=<exact-tag>`. Reset mode requires the configured
private key to match the public key in the source being released and requires
that key to differ from the latest stable client's key. A stale or differently
tagged reset is refused. This deliberately prevents older binaries from using
built-in self-update across the boundary; package-manager, installer, and manual
updates remain available and the next release resumes normal continuity from
the reset release. Remove the variable immediately after verification.

Never commit the private key. `.gitignore` blocks common key filenames as a
backstop, but treat that as a safety net, not a control.
