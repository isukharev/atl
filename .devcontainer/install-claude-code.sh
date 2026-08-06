#!/usr/bin/env bash
#
# Install Claude Code from Anthropic's signed APT repository inside the pinned
# Debian devcontainer. Package-manager downloads avoid the native bootstrap's
# large self-update path and remain authenticated by Anthropic's repository key.
#
# Usage: install-claude-code.sh [stable|latest]
#   Defaults to the "latest" channel. Override via the first arg or the
#   CLAUDE_CODE_CHANNEL env var (arg wins).
#
set -euo pipefail

readonly CHANNEL="${1:-${CLAUDE_CODE_CHANNEL:-latest}}"
readonly KEY_URL="https://downloads.claude.ai/keys/claude-code.asc"
readonly KEY_FINGERPRINT="31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
readonly KEYRING_PATH="/etc/apt/keyrings/claude-code.asc"
readonly REPOSITORY_PATH="/etc/apt/sources.list.d/claude-code.list"

if [[ ! "${CHANNEL}" =~ ^(stable|latest)$ ]]; then
    echo "Usage: $0 [stable|latest]" >&2
    exit 1
fi

for dependency in apt-get awk curl gpg install mktemp sudo; do
    if ! command -v "${dependency}" >/dev/null 2>&1; then
        echo "${dependency} is required but not installed" >&2
        exit 1
    fi
done

case "$(uname -s)" in
    Linux) ;;
    *) echo "This script only supports Linux containers (got $(uname -s))." >&2; exit 1 ;;
esac

temp_key="$(mktemp)"
trap 'rm -f "${temp_key}"' EXIT

curl -fsSL "${KEY_URL}" -o "${temp_key}"
primary_fingerprints="$(
    gpg --batch --quiet --show-keys --with-colons "${temp_key}" 2>/dev/null |
        awk -F: '
            $1 == "pub" { want_primary = 1; next }
            $1 == "sub" { want_primary = 0; next }
            $1 == "fpr" && want_primary { print $10; want_primary = 0 }
        '
)"
if [[ "${primary_fingerprints}" != "${KEY_FINGERPRINT}" ]]; then
    echo "Claude Code repository must contain exactly the reviewed primary signing key" >&2
    exit 1
fi

sudo install -d -m 0755 /etc/apt/keyrings
sudo install -m 0644 "${temp_key}" "${KEYRING_PATH}"
printf '%s\n' \
    "deb [signed-by=${KEYRING_PATH}] https://downloads.claude.ai/claude-code/apt/${CHANNEL} ${CHANNEL} main" |
    sudo tee "${REPOSITORY_PATH}" >/dev/null

echo "Installing Claude Code from the signed APT ${CHANNEL} channel..."
sudo apt-get -o Acquire::Retries=3 -o APT::Update::Error-Mode=any update -qq
sudo apt-get -o Acquire::Retries=3 install -y --no-install-recommends claude-code

if ! command -v claude >/dev/null 2>&1; then
    echo "Claude Code installation completed without a claude executable on PATH" >&2
    exit 1
fi
echo "✅ Claude Code installed: $(command -v claude) ($(claude --version))"
