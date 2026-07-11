# Self-update

`atl` can silently replace itself with a newer binary on every command run.
The mechanism is opt-out, throttled, and signature-verified. It never blocks a
command; every failure path is swallowed.

See also: [../README.md](../README.md) · [../SECURITY.md](../SECURITY.md) ·
[usage.md](usage.md) · [architecture.md](architecture.md)

---

## How it works, step by step

### 1. Triggered on every command

`PersistentPreRun` on the cobra root command calls `runSelfUpdate` before any
subcommand executes (`internal/cli/selfupdate.go`). The call is fire-and-forget;
it returns before the subcommand runs regardless of outcome.

### 2. Fail-closed early exits

`selfupdate.Run` returns immediately (doing nothing) if any of these are true:

- `version.Version` is `"dev"` or empty (local/unstamped build).
- The `baseURL` is empty (self-update source not configured).
- `ATL_NO_UPDATE` is set to any non-empty value.
- No ed25519 public key is compiled into `internal/selfupdate/pubkey.go`
  (`trustedPublicKeyB64` is the empty string).

This means a freshly cloned, locally-built binary **never** auto-updates.
Auto-update only activates on binaries published through the official release
pipeline that embedded the public key and stamped a real version.

### 3. HTTPS-only source enforcement

The base URL is checked before any network call:

- Any URL beginning with `https://` is accepted.
- HTTP is accepted only for genuine loopback addresses (`http://127.0.0.1` or
  `http://localhost`, with a port or path delimiter immediately following the
  host). This allows integration tests to run a local server without TLS.
- All other HTTP URLs are refused (`debugf` logs the refusal when
  `ATL_UPDATE_DEBUG` is set).

### 4. Throttling (6-hour cooldown)

A stamp file (`.update-check`) in the config directory records the last check
time. `Run` exits immediately if the stamp is less than 6 hours old. The stamp
is written at the start of a check (even before the network call), so a failed
network attempt still counts toward the cooldown — a broken release server
does not hammer the user's command latency.

### 5. Resolving the base URL

The distribution server URL is resolved in priority order:

1. `config.UpdateBaseURL` (set via `atl config set --update-url ...`).
2. `ATL_UPDATE_URL` environment variable (already merged into `Config` by
   `config.Load`).
3. `version.DefaultUpdateURL` baked into the binary at build time
   (`https://github.com/isukharev/atl/releases/latest/download`).

### 6. Downloading and verifying the signed manifest

Two files are fetched (4-second timeout each):

- `<baseURL>/manifest.json` — the release manifest.
- `<baseURL>/manifest.json.sig` — a base64-encoded ed25519 signature over the
  **exact bytes** of `manifest.json`.

The signature is verified against the compiled-in public key **before** the
manifest JSON is parsed. If verification fails — bad signature, corrupt
download, or a MITM-supplied substitute — `Run` returns with no update applied.

The manifest schema:

```json
{
  "version": "1.2.3",
  "builds": [
    { "os": "linux",  "arch": "amd64",  "sha256": "abc…", "path": "atl-linux-amd64" },
    { "os": "darwin", "arch": "arm64",  "sha256": "def…", "path": "atl-darwin-arm64" },
    { "os": "windows","arch": "amd64",  "sha256": "ghi…", "path": "atl-windows-amd64.exe" }
  ]
}
```

### 7. Version comparison

`semverLess(current, manifest.Version)` compares dotted numeric versions
(ignoring pre-release suffixes). If the current binary is already at or above
the manifest version, `Run` returns with no action.

### 8. Downloading and verifying the binary

The binary for the current `runtime.GOOS`/`runtime.GOARCH` is selected from
the manifest. It is downloaded with a 90-second timeout. Its sha256 is checked
against the value in the (already-verified) manifest. A mismatch causes `Run`
to return with no update.

### 9. Atomic replacement (applied on the next invocation)

The new binary is written to a temporary file in the same directory as the
running executable, then `os.Rename`-d over the current executable. The original
file's permission bits are preserved (a hardened `0700`/`0750` install is not
silently widened), with the executable bit forced on. On Linux and macOS this is
safe while the binary is running (the OS keeps the old inode open; only the
directory entry changes). The current process is **not** re-execed — the running
command finishes on the already-loaded image and the new version takes effect on
the next invocation, so a command is never transparently replaced mid-run.

If any step from 8 onwards fails (unwritable directory, rename error), `Run`
returns with no side effects visible to the user.

---

## Enabling auto-update for a release (maintainer guide)

### 1. Generate a keypair

```
make genkey
```

This prints a base64-encoded ed25519 public key to stdout and a private key
(also base64). Keep the private key — you will never see it again from this
command.

### 2. Embed the public key

Paste the public key into `internal/selfupdate/pubkey.go`:

```go
const trustedPublicKeyB64 = "<base64-encoded-public-key>"
```

Commit this change. The public key is not secret.

### 3. Store the private key as a CI secret

Add the private key as the `ATL_RELEASE_PRIVATE_KEY` secret in the protected
GitHub `release` environment (Settings → Environments → release). The release
job must reference that environment. This keeps the key unavailable to jobs
outside the approved release deployment and it is never written to logs.

### 4. Release workflow responsibilities

The CI release job must produce these assets alongside the compiled binaries:

- `manifest.json` — the manifest listing the version and each binary's sha256.
- `manifest.json.sig` — `base64(ed25519_sign(private_key, manifest.json_bytes))`.

Before building, CI must also derive the public key from its protected private
key and compare it with the key embedded in the latest published stable client.
This makes the old-key-signed bridge release an enforced part of rotation, not
only a runbook instruction. With no previous release, compare against the source
being released. A missing or mismatched signing key must stop publication.

An example signing step (using the `openssl` CLI or an equivalent Go tool):

```bash
# write the manifest
cat > manifest.json <<EOF
{
  "version": "$VERSION",
  "builds": [
    {"os":"linux","arch":"amd64","sha256":"$(sha256sum atl-linux-amd64 | cut -d' ' -f1)","path":"atl-linux-amd64"},
    {"os":"darwin","arch":"arm64","sha256":"$(sha256sum atl-darwin-arm64 | cut -d' ' -f1)","path":"atl-darwin-arm64"}
  ]
}
EOF

# sign it (using a Go helper that reads ATL_RELEASE_PRIVATE_KEY)
make sign-manifest MANIFEST=manifest.json
```

Upload `manifest.json`, `manifest.json.sig`, and all `atl-<os>-<arch>` files
as release assets. The `latest/download` GitHub Releases URL resolves them
automatically.

### 5. Verify end-to-end

After a release, install the previous version and run any `atl` command with
`ATL_UPDATE_DEBUG=1`. You should see the manifest fetch and signature check
logged to stderr; the swapped-in binary takes effect on the next invocation.

---

## Disabling auto-update

```bash
# for the current invocation only
ATL_NO_UPDATE=1 atl conf pull --id 12345678

# permanently (per machine)
atl config set --update-url ""   # empty URL disables auto-update
```

Alternatively, set `ATL_NO_UPDATE` in the environment where `atl` runs
(shell profile, CI environment variables, systemd service).

---

## Threat model (summary)

The threat `atl` is designed to resist is a compromised release binary landing
on a user's machine. Even if an attacker can:

- Replace the binary at the download URL, and
- Replace the sha256 in `manifest.json`,

they still cannot forge an update unless they also hold the ed25519 private
key. The signature check happens over the exact `manifest.json` bytes before
any of its content is trusted.

For the full supply-chain analysis see [../SECURITY.md](../SECURITY.md).

Auto-update is disabled when no public key is embedded, so a fork or a locally-
built binary cannot accidentally trust an attacker-controlled server.
