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
# This script verifies the SHA-256 checksum published alongside each binary. If
# the GitHub CLI (gh) is installed, it additionally verifies build provenance.
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

# --- optional provenance verification ---------------------------------------
if command -v gh >/dev/null 2>&1; then
	if gh attestation verify "$tmp/atl" --repo "$REPO" >/dev/null 2>&1; then
		echo "atl-install: build provenance verified" >&2
	else
		echo "atl-install: note: could not verify provenance (continuing; sha256 already checked)" >&2
	fi
fi

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
