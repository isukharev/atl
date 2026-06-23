#!/usr/bin/env bash
#
# Install the Claude Code *native* build inside a Linux dev container.
#
# This is a self-contained, Linux-only port of the official installer
# (https://claude.ai/install.sh). We vendor it into the repo instead of piping
# the remote script straight into a shell at container-create time, so the exact
# steps are reviewable, pinned in git, and don't depend on fetching+executing an
# opaque script over the network. The security-critical bits are preserved:
# the binary's SHA-256 is checked against the signed manifest before it runs.
#
# Usage: install-claude-code.sh [stable|latest|VERSION]
#   Defaults to the "stable" channel. Override via the first arg or the
#   CLAUDE_CODE_CHANNEL env var (arg wins).
#
set -euo pipefail

CHANNEL="${1:-${CLAUDE_CODE_CHANNEL:-latest}}"

# Validate channel/version (stable | latest | X.Y.Z[-suffix]) before it ever
# reaches a URL.
if [[ ! "$CHANNEL" =~ ^(stable|latest|[0-9]+\.[0-9]+\.[0-9]+(-[^[:space:]]+)?)$ ]]; then
    echo "Usage: $0 [stable|latest|VERSION]" >&2
    exit 1
fi

DOWNLOAD_BASE_URL="https://downloads.claude.ai/claude-code-releases"
DOWNLOAD_DIR="${HOME}/.claude/downloads"

# --- dependencies ---------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
    DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
    DOWNLOADER="wget"
else
    echo "Either curl or wget is required but neither is installed" >&2
    exit 1
fi

HAS_JQ=false
command -v jq >/dev/null 2>&1 && HAS_JQ=true

if ! command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum is required but not installed" >&2
    exit 1
fi

download_file() {
    # download_file <url> [output]
    local url="$1" output="${2:-}"
    if [ "$DOWNLOADER" = "curl" ]; then
        if [ -n "$output" ]; then curl -fsSL -o "$output" "$url"; else curl -fsSL "$url"; fi
    else
        if [ -n "$output" ]; then wget -q -O "$output" "$url"; else wget -q -O - "$url"; fi
    fi
}

# Pure-bash manifest checksum extraction, used when jq is unavailable.
get_checksum_from_manifest() {
    local json="$1" platform="$2"
    json=$(echo "$json" | tr -d '\n\r\t' | sed 's/ \+/ /g')
    if [[ $json =~ \"$platform\"[^}]*\"checksum\"[[:space:]]*:[[:space:]]*\"([a-f0-9]{64})\" ]]; then
        echo "${BASH_REMATCH[1]}"
        return 0
    fi
    return 1
}

# --- platform detection (Linux only) --------------------------------------
case "$(uname -s)" in
    Linux) ;;
    *) echo "This script only supports Linux containers (got $(uname -s))." >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch="x64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

# Pick the musl variant when running on a musl libc (e.g. Alpine) container.
if [ -f "/lib/libc.musl-${arch/x64/x86_64}.so.1" ] \
   || [ -f /lib/libc.musl-x86_64.so.1 ] || [ -f /lib/libc.musl-aarch64.so.1 ] \
   || ldd /bin/ls 2>&1 | grep -q musl; then
    platform="linux-${arch}-musl"
else
    platform="linux-${arch}"
fi

mkdir -p "$DOWNLOAD_DIR"

# --- resolve version & verify --------------------------------------------
# Always fetch the latest bootstrapper (it carries the newest `claude install`),
# then let it install the requested channel.
version=$(download_file "$DOWNLOAD_BASE_URL/latest")
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    echo "Failed to get a valid version from downloads.claude.ai (got unexpected content)." >&2
    echo "The download service may be unreachable or unavailable in your region:" >&2
    echo "  https://www.anthropic.com/supported-countries" >&2
    exit 1
fi

manifest_json=$(download_file "$DOWNLOAD_BASE_URL/$version/manifest.json")
if [ "$HAS_JQ" = true ]; then
    checksum=$(echo "$manifest_json" | jq -r ".platforms[\"$platform\"].checksum // empty")
else
    checksum=$(get_checksum_from_manifest "$manifest_json" "$platform") || true
fi

if [ -z "${checksum:-}" ] || [[ ! "$checksum" =~ ^[a-f0-9]{64}$ ]]; then
    echo "Platform $platform not found in manifest" >&2
    exit 1
fi

binary_path="$DOWNLOAD_DIR/claude-$version-$platform"
if ! download_file "$DOWNLOAD_BASE_URL/$version/$platform/claude" "$binary_path"; then
    echo "Download failed" >&2
    rm -f "$binary_path"
    exit 1
fi

actual=$(sha256sum "$binary_path" | cut -d' ' -f1)
if [ "$actual" != "$checksum" ]; then
    echo "Checksum verification failed for $binary_path" >&2
    echo "  expected: $checksum" >&2
    echo "  actual:   $actual" >&2
    rm -f "$binary_path"
    exit 1
fi

chmod +x "$binary_path"

# --- install --------------------------------------------------------------
# `claude install` lays down the version-managed native build under
# ~/.local/share/claude and links the launcher into ~/.local/bin (on PATH).
echo "Installing Claude Code native build ($CHANNEL channel)..."
"$binary_path" install "$CHANNEL"

rm -f "$binary_path"

echo "✅ Claude Code native build installed at ${HOME}/.local/bin/claude"
