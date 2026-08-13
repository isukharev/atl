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
command -v env >/dev/null 2>&1 || fail "env is required"
command -v gh >/dev/null 2>&1 || fail "GitHub CLI is required for provenance verification"
command -v jq >/dev/null 2>&1 || fail "jq is required for version verification"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

scratch=$(mktemp -d /tmp/atl-release.XXXXXX) || fail "could not allocate release verification directory"
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
asset="atl-${os}-${arch}"
url="https://github.com/isukharev/atl/releases/download/${version}/${asset}"
curl --disable --fail --silent --show-error --location \
	--proto '=https' --proto-redir '=https' --max-redirs 5 \
	"$url" --output "$scratch/atl" || fail "release download failed"
actual=$(sha256sum "$scratch/atl" | awk '{print $1}')
[ "$actual" = "$checksum" ] || fail "release checksum mismatch"

attestation_response="$scratch/attestations.json"
attestation_bundle="$scratch/attestations.jsonl"
attestation_url="https://api.github.com/repos/isukharev/atl/attestations/sha256:${checksum}"
curl --disable --fail --silent --show-error --proto '=https' --max-filesize 8388608 \
	--header 'Accept: application/vnd.github+json' \
	--header 'X-GitHub-Api-Version: 2022-11-28' \
	"$attestation_url" --output "$attestation_response" || fail "release attestation download failed"
attestation_size=$(wc -c <"$attestation_response")
[ "$attestation_size" -gt 0 ] && [ "$attestation_size" -le 8388608 ] ||
	fail "release attestation response exceeded its bound"
jq -ce '
	if type == "object" and
		(.attestations | type == "array") and
		(.attestations | length > 0 and length <= 30) and
		all(.attestations[]; type == "object" and (.bundle | type == "object"))
	then .attestations[].bundle
	else error("invalid attestation response")
	end
' "$attestation_response" >"$attestation_bundle" 2>/dev/null || fail "release attestation response was invalid"
[ -s "$attestation_bundle" ] && [ "$(wc -c <"$attestation_bundle")" -le 8388608 ] ||
	fail "release attestation bundle exceeded its bound"

mkdir -m 0700 "$scratch/gh-config" || fail "could not isolate GitHub CLI configuration"
env -i \
	HOME="$scratch" \
	PATH="$PATH" \
	GH_CONFIG_DIR="$scratch/gh-config" \
	GH_PROMPT_DISABLED=1 \
	gh attestation verify "$scratch/atl" \
	--bundle "$attestation_bundle" \
	--hostname github.com \
	--repo isukharev/atl \
	--signer-workflow "isukharev/atl/.github/workflows/release.yml" \
	--source-ref "refs/tags/${version}" \
	>/dev/null || fail "release provenance verification failed"
chmod 0700 "$scratch/atl" || fail "could not make the verified binary executable"
reported_version=$(env -i HOME="$scratch" PATH="$PATH" ATL_NO_UPDATE=1 \
	"$scratch/atl" version 2>/dev/null) || fail "verified binary did not report its version"
printf '%s\n' "$reported_version" |
	jq -e --arg expected "${version#v}" 'type == "object" and .version == $expected' >/dev/null ||
	fail "verified binary version does not match ATL_VERSION"

install -d -m 0755 "$install_dir"
install -m 0755 "$scratch/atl" "$install_dir/atl"
printf '%s\n' "atl-corpus-install: verified pinned ATL release"
