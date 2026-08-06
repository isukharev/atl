#!/usr/bin/env bash
#
# devcontainer postCreateCommand: one-time setup after the container is created.
# Kept as a script (instead of a long inline JSON string) so the steps are
# readable, diffable, and easy to extend.
#
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${here}/.." && pwd)"

# Fail before installing tools if the container and repository contract drift.
(cd "${repo_root}" && go run ./scripts/check-maintainer-contract)

# Make the mounted config volumes writable by the non-root user.
sudo chown -R vscode:vscode /home/vscode/.claude /home/vscode/.codex /home/vscode/.agents /home/vscode/.config/gh

# System tools used by the dev workflow.
sudo apt-get -o Acquire::Retries=3 -o APT::Update::Error-Mode=any update -qq
sudo apt-get -o Acquire::Retries=3 install -y --no-install-recommends gnupg python3 ripgrep

# Pin golangci-lint to the version CI enforces (see CLAUDE.md).
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
    | sh -s -- -b "$(go env GOPATH)/bin" v2.12.2

# Claude Code from Anthropic's signed APT repository.
bash "${here}/install-claude-code.sh"

# OpenAI Codex CLI (installed via npm; see script header for rationale).
bash "${here}/install-codex.sh"

# Optional structural code navigation (CLI only; graph extraction stays manual).
bash "${here}/install-graphify.sh"
