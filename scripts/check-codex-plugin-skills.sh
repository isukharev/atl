#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

git -C "$repo_root" diff --no-index --exit-code -- skills plugins/atl/skills
