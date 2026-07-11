# Releasing & repository hardening

This is the maintainer runbook for publishing the public repo, locking it down so
nobody can slip malicious code or binaries into the supply chain, and cutting a
signed release. Run the commands yourself — they touch GitHub settings and the
signing key, which must never pass through automation you don't control.

Prerequisites: `gh auth status` shows you logged in as `isukharev`, and your
shell is in the root of the prepared repo (`cd` into your clone).

---

## 1. Generate the release signing key (do this once, off-CI)

The whole auto-update trust chain hangs on this key. Generate it on your own
machine; the private half must only ever live as a GitHub Actions secret.

```bash
make genkey
```

This prints a **public key** and writes the **private key** to
`atl-release-key.b64` (gitignored). Then:

1. Paste the public key into `internal/selfupdate/pubkey.go`:
   ```go
   const trustedPublicKeyB64 = "<the printed public key>"
   ```
   Commit that change. (Until this is set, clients fail-closed and never
   auto-update — which is the safe default.)

2. Store the private key in the protected `release` environment, then delete the
   local copy after placing an offline backup in a trusted vault:
   ```bash
   gh secret set ATL_RELEASE_PRIVATE_KEY --env release < atl-release-key.b64
   rm atl-release-key.b64
   ```

> Keep a secure offline backup of the private key. Rotation requires a bridge
> release signed by the old key; clients that miss that bridge may otherwise
> need a manual reinstall (see SECURITY.md → rotation).

For a staged rotation, do **not** install the new environment secret immediately.
First embed the new public key and publish one bridge release while the old key
still signs in CI. Verify that release, allow an adoption window, then set the
new environment secret and remove any repository-scoped copy of the old key.

---

## 2. Create and push the public repository

The repo already has an initial commit on `main` (with the signing-key change
from step 1 committed on top). Sanity-check what is tracked, then publish:

```bash
git status                 # clean
git ls-files | grep -E '\.(b64|key|pem)$|^dist/|\.env' && echo "STOP: secret/build artifact tracked" || echo "clean"

gh repo create isukharev/atl \
  --public \
  --description "Agent-native CLI for Confluence/Jira: mirror docs to disk, edit native storage format, push under a version gate" \
  --source=. --remote=origin --push
```

---

## 3. Harden the repository

Run this block once after the repo exists. It enforces review, blocks force
pushes, requires CI to pass, enables secret scanning + push protection, and makes
the default Actions token read-only.

```bash
REPO=isukharev/atl

# Default GITHUB_TOKEN is read-only; workflows that need more ask explicitly.
gh api -X PUT "repos/$REPO/actions/permissions/workflow" \
  -f default_workflow_permissions=read \
  -F can_approve_pull_request_reviews=false

# Secret scanning + push protection (free on public repos).
gh api -X PATCH "repos/$REPO" \
  -f 'security_and_analysis[secret_scanning][status]=enabled' \
  -f 'security_and_analysis[secret_scanning_push_protection][status]=enabled'

# Branch protection on main.
cat > /tmp/atl-protection.json <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["test", "lint", "govulncheck"]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "required_approving_review_count": 1,
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": true
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "block_creations": false,
  "required_conversation_resolution": true
}
JSON
gh api -X PUT "repos/$REPO/branches/main/protection" \
  -H "Accept: application/vnd.github+json" \
  --input /tmp/atl-protection.json
rm /tmp/atl-protection.json

# Require signed commits on main (optional but recommended).
gh api -X POST "repos/$REPO/branches/main/protection/required_signatures" \
  -H "Accept: application/vnd.github+json"
```

Then in the GitHub UI, double-check:
- **Settings → Actions → General:** "Allow GitHub Actions to create and approve
  pull requests" is **off**; fork-PR workflows require approval.
- **Settings → Code security:** CodeQL/Dependabot alerts enabled (the workflows
  and `dependabot.yml` are already in the repo).
- Enable **2FA** on your account if not already.

> Tag pushes trigger releases. Because `main` is protected and releases are built
> only by the workflow from the tagged commit, an attacker would need both a
> merged-and-reviewed PR *and* the signing secret to ship a malicious update.

---

## 4. Cut a release

```bash
# In a normal reviewed PR first: bump VERSION + CHANGELOG, and set the SAME
# version in BOTH plugin manifests:
#   .claude-plugin/plugin.json          ("version": "X.Y.Z")
#   plugins/atl/.codex-plugin/plugin.json
# Then, from main:
git tag v0.3.0
git push origin v0.3.0
```

The plugin-manifest bump is not cosmetic: the manifest `version` is the update
trigger for installed plugins — while it is unchanged, `/plugin update` reports
"already at the latest version" and clients keep their install-time skills
forever, even as the binary self-updates. The release workflow fail-fast
asserts both manifests equal the tag, so a forgotten bump cannot ship.

The `release` workflow cross-compiles the four targets, generates `manifest.json`,
**signs it** with `ATL_RELEASE_PRIVATE_KEY`, generates the Homebrew formula
(`atl.rb`), attests SLSA build provenance, and publishes the GitHub Release with
the binaries, `.sha256` files, `manifest.json`, `manifest.json.sig`, `atl.rb`,
and `install.sh`.

### Homebrew tap

`atl.rb` is emitted by `make homebrew` (`scripts/gen-homebrew-formula`) with each
platform's release-asset URL pinned to its SHA-256, published as a release asset,
and consumed by the tap repository `isukharev/homebrew-tap` (`Formula/atl.rb`),
which backs `brew install isukharev/tap/atl`.

**Auto-bump (recommended).** When the `HOMEBREW_TAP_TOKEN` secret is set, the
release workflow's *Bump Homebrew tap* step pushes the new `atl.rb` into the tap
automatically, so `brew upgrade atl` works with no manual step. The step is gated
on the secret and runs *after* publish, so a tap failure never blocks the
release. To enable it once:

1. Create a **fine-grained PAT** (GitHub → Settings → Developer settings →
   Fine-grained tokens) limited to **only** the `isukharev/homebrew-tap`
   repository, with **Repository permissions → Contents: Read and write** (the
   default `GITHUB_TOKEN` can't write to another repo, hence a dedicated token).
2. Add it as an Actions secret on **this** repo:
   `gh secret set HOMEBREW_TAP_TOKEN -R isukharev/atl` (paste the token).

Rotate/expire the PAT on your usual cadence; a deploy key scoped to the tap repo
is an equivalent, more locked-down alternative.

**Manual fallback.** If the secret is not set, the workflow logs a notice and
skips the bump; copy the formula by hand:

```bash
gh release download "$TAG" -R isukharev/atl -p atl.rb -D Formula --clobber
git -C <homebrew-tap> commit -am "atl ${TAG#v}" && git -C <homebrew-tap> push
```

Verify the published release:

```bash
gh attestation verify <(curl -fsSL https://github.com/isukharev/atl/releases/download/v0.1.0/atl-linux-amd64) \
  --repo isukharev/atl \
  --signer-workflow isukharev/atl/.github/workflows/release.yml
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
atl version
```

Clients on a prior signed version will pick up the update automatically within
the 6h check window (unless `ATL_NO_UPDATE=1`).
