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

2. Store the private key as a repo secret, then delete the local copy:
   ```bash
   gh secret set ATL_RELEASE_PRIVATE_KEY < atl-release-key.b64
   rm atl-release-key.b64
   ```

> Keep a secure offline backup of the private key if you want to be able to keep
> signing after a laptop loss. Losing it just means generating a new pair and
> shipping a release that embeds the new public key (see SECURITY.md → rotation).

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
# bump VERSION + CHANGELOG in a normal reviewed PR first, then from main:
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow cross-compiles the four targets, generates `manifest.json`,
**signs it** with `ATL_RELEASE_PRIVATE_KEY`, generates the Homebrew formula
(`atl.rb`), attests SLSA build provenance, and publishes the GitHub Release with
the binaries, `.sha256` files, `manifest.json`, `manifest.json.sig`, `atl.rb`,
and `install.sh`.

### Homebrew tap (optional, owner-maintained)

`atl.rb` is emitted by `make homebrew` (`scripts/gen-homebrew-formula`) with each
platform's release-asset URL pinned to its SHA-256, and published as a release
asset. To offer `brew install isukharev/tap/atl`, create a tap repository named
`homebrew-tap` under the same owner and, on each release, copy the published
`atl.rb` into its `Formula/` directory (manually, or via a small bump workflow you
add to the tap repo with a token scoped to it). The release pipeline here does
**not** write to the tap; it only produces the formula.

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
