#!/usr/bin/env bash
#
# devcontainer postCreateCommand: one-time setup after the container is created.
# Kept as a script (instead of a long inline JSON string) so the steps are
# readable, diffable, and easy to extend.
#
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Make the mounted config volumes writable by the non-root user.
sudo chown -R vscode:vscode /home/vscode/.claude /home/vscode/.codex /home/vscode/.agents /home/vscode/.config/gh

# System tools used by the dev workflow.
sudo apt-get update -qq
sudo apt-get install -y --no-install-recommends ripgrep

# Pin golangci-lint to the version CI enforces (see CLAUDE.md).
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
    | sh -s -- -b "$(go env GOPATH)/bin" v2.12.2

# Claude Code native build (Linux-only port of claude.ai/install.sh).
bash "${here}/install-claude-code.sh"

# OpenAI Codex CLI (installed via npm; see script header for rationale).
bash "${here}/install-codex.sh"
