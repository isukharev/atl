#!/bin/sh
set -eu

umask 077

fail() {
	printf '%s\n' "atl-corpus-install: $1" >&2
	exit 1
}

version=${ATL_VERSION:-}
checksum=${ATL_ASSET_SHA256:-}
install_dir=${ATL_INSTALL_DIR:-"$HOME/.local/bin"}

printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
	fail "ATL_VERSION must be an exact vX.Y.Z release"
case "$checksum" in
	*[!0-9a-f]*|'') fail "ATL_ASSET_SHA256 must be 64 lowercase hexadecimal characters" ;;
esac
[ "${#checksum}" -eq 64 ] || fail "ATL_ASSET_SHA256 must be 64 lowercase hexadecimal characters"

os=$(uname -s)
arch=$(uname -m)
case "$os" in
	Linux) os=linux ;;
	*) fail "this runtime template supports Linux only" ;;
esac
case "$arch" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) fail "unsupported architecture" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v gh >/dev/null 2>&1 || fail "GitHub CLI is required for provenance verification"
command -v jq >/dev/null 2>&1 || fail "jq is required for version verification"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

scratch=$(mktemp -d /tmp/atl-release.XXXXXX) || fail "could not allocate release verification directory"
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
asset="atl-${os}-${arch}"
url="https://github.com/isukharev/atl/releases/download/${version}/${asset}"
curl -fsSL "$url" -o "$scratch/atl" || fail "release download failed"
actual=$(sha256sum "$scratch/atl" | awk '{print $1}')
[ "$actual" = "$checksum" ] || fail "release checksum mismatch"
gh attestation verify "$scratch/atl" \
	--repo isukharev/atl \
	--signer-workflow "isukharev/atl/.github/workflows/release.yml" \
	--source-ref "refs/tags/${version}" \
	>/dev/null || fail "release provenance verification failed"
reported_version=$(ATL_NO_UPDATE=1 "$scratch/atl" version 2>/dev/null) || fail "verified binary did not report its version"
printf '%s\n' "$reported_version" |
	jq -e --arg expected "${version#v}" 'type == "object" and .version == $expected' >/dev/null ||
	fail "verified binary version does not match ATL_VERSION"

install -d -m 0755 "$install_dir"
install -m 0755 "$scratch/atl" "$install_dir/atl"
printf '%s\n' "atl-corpus-install: verified pinned ATL release"
