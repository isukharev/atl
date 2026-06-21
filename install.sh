#!/bin/sh
# atl installer — downloads a release binary from GitHub, verifies its SHA-256,
# and installs it to ~/.local/bin (override with ATL_INSTALL_DIR).
#
#   curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
#
# Options (env):
#   ATL_VERSION       install a specific tag (e.g. v0.1.0); default: latest
#   ATL_INSTALL_DIR   install directory; default: $HOME/.local/bin
#
# This script verifies the SHA-256 checksum published alongside each binary. The
# checksum lives in the same release as the binary, so it detects corruption in
# transit. For a stronger authenticity guarantee (SLSA build provenance), verify
# the binary out-of-band with the GitHub CLI — see docs/RELEASING.md / SECURITY.md.
# The installer itself does not require gh.
set -eu

REPO="isukharev/atl"
INSTALL_DIR="${ATL_INSTALL_DIR:-$HOME/.local/bin}"

err() { echo "atl-install: $*" >&2; exit 1; }

# --- detect platform ---------------------------------------------------------
os=$(uname -s 2>/dev/null || echo unknown)
arch=$(uname -m 2>/dev/null || echo unknown)
case "$os" in
	Linux)  os=linux ;;
	Darwin) os=darwin ;;
	*) err "unsupported OS: $os (linux and macOS only)" ;;
esac
case "$arch" in
	x86_64|amd64)  arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) err "unsupported architecture: $arch" ;;
esac
asset="atl-${os}-${arch}"

# --- resolve download base ---------------------------------------------------
if [ -n "${ATL_VERSION:-}" ]; then
	base="https://github.com/$REPO/releases/download/${ATL_VERSION}"
else
	base="https://github.com/$REPO/releases/latest/download"
fi

# --- fetch helper ------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
else
	err "need curl or wget"
fi

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t atl)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "atl-install: downloading $asset ..." >&2
fetch "$base/$asset" "$tmp/atl" || err "download failed: $base/$asset"
fetch "$base/$asset.sha256" "$tmp/atl.sha256" || err "checksum download failed"

# --- verify sha256 -----------------------------------------------------------
want=$(awk '{print $1}' "$tmp/atl.sha256")
[ -n "$want" ] || err "empty checksum"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/atl" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/atl" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$got" = "$want" ] || err "checksum mismatch (expected $want, got $got)"
echo "atl-install: sha256 verified" >&2

# Note: the sha256 above detects corruption in transit, not a tampered release.
# SLSA build provenance is published with every release; a security-conscious
# user can verify it out-of-band with the GitHub CLI (see docs/RELEASING.md).
# This installer deliberately does not require or invoke gh — it never runs extra
# tooling on your machine.

# --- install -----------------------------------------------------------------
mkdir -p "$INSTALL_DIR"
chmod 0755 "$tmp/atl"
mv "$tmp/atl" "$INSTALL_DIR/atl"
echo "atl-install: installed to $INSTALL_DIR/atl" >&2

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) echo "atl-install: add $INSTALL_DIR to your PATH:  export PATH=\"$INSTALL_DIR:\$PATH\"" >&2 ;;
esac

"$INSTALL_DIR/atl" version 2>/dev/null || true

# --- next steps --------------------------------------------------------------
# A bare binary is not yet usable: atl needs a backend URL and a PAT. Print the
# exact commands so the first run does not fail with an unexplained config error.
cat >&2 <<'EOF'

atl-install: next steps (Confluence/Jira are Server/Data Center)
  1. Point atl at your instance(s):
       atl config set --confluence-url https://confluence.example.com \
                      --jira-url       https://jira.example.com
  2. Add a Personal Access Token (no-echo prompt; never pass it on argv):
       atl auth login --service confluence
       atl auth login --service jira
  3. Verify, then run a cheap read:
       atl auth status
       atl conf search --cql 'type = page' --limit 1

Quick start & scripting/CI guide: https://github.com/isukharev/atl#quick-start
EOF
