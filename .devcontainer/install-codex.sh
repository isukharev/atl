#!/usr/bin/env bash
#
# Install the OpenAI Codex CLI inside a Linux dev container.
#
# We install via npm (`@openai/codex`) rather than piping the official
# `curl -fsSL https://chatgpt.com/codex/install.sh | sh` installer into a shell.
# Same reasoning as install-claude-code.sh: we don't fetch+execute an opaque
# remote script at container-create time. npm pins an exact version and verifies
# the package integrity hash from the registry, and the Node toolchain is already
# present (devcontainers `node` feature), so this adds no new dependency.
#
# The Codex binary lives in the (non-persisted) npm global prefix and is
# reinstalled on every container create; only ~/.codex (config + credentials) is
# persisted, via the volume mount declared in devcontainer.json.
#
# Usage: install-codex.sh [latest|alpha|beta|VERSION]
#   Defaults to the "latest" channel. Override via the first arg or the
#   CODEX_VERSION env var (arg wins).
set -euo pipefail

VERSION="${1:-${CODEX_VERSION:-latest}}"

# Validate channel/version (latest | alpha | beta | X.Y.Z[-suffix]) before it
# ever reaches npm.
if [[ ! "$VERSION" =~ ^(latest|alpha|beta|[0-9]+\.[0-9]+\.[0-9]+(-[^[:space:]]+)?)$ ]]; then
    echo "Usage: $0 [latest|alpha|beta|VERSION]" >&2
    exit 1
fi

if ! command -v npm >/dev/null 2>&1; then
    echo "npm is required but not installed (expected from the devcontainer 'node' feature)" >&2
    exit 1
fi

echo "Installing OpenAI Codex CLI (@openai/codex@${VERSION})..."
npm install -g "@openai/codex@${VERSION}"

echo "✅ Codex CLI installed: $(command -v codex) ($(codex --version 2>/dev/null || echo 'version unknown'))"
