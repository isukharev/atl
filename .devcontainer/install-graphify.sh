#!/usr/bin/env bash
#
# Install the Graphify CLI inside the Linux devcontainer.
#
# Graphify is distributed as the Python package `graphifyy`. We bootstrap a
# reviewed uv release from its checksum-pinned standalone archive, then let uv
# keep Graphify in an isolated tool environment. Repository extraction and
# agent integration remain explicit: post-create installs the CLI only.
#
set -euo pipefail

readonly UV_VERSION="0.12.2"
readonly GRAPHIFY_VERSION="0.9.34"
readonly UV_RELEASE_BASE="https://github.com/astral-sh/uv/releases/download/${UV_VERSION}"
readonly GRAPHIFY_WHEEL_URL="https://files.pythonhosted.org/packages/c3/fe/eb0afeb410f29e2e534f2e46a2d3191a0e08c02a36176080548542371f83/graphifyy-0.9.34-py3-none-any.whl#sha256=2bb5fdc6aa96abbeb105f177040815f68253a56610af64771b5dcfa0464eb35b"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for dependency in curl sha256sum tar install mktemp; do
    if ! command -v "${dependency}" >/dev/null 2>&1; then
        echo "${dependency} is required but not installed" >&2
        exit 1
    fi
done

case "$(uname -s)" in
    Linux) ;;
    *) echo "This script only supports Linux containers (got $(uname -s))." >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|amd64)
        uv_target="x86_64-unknown-linux-gnu"
        uv_sha256="d66e96b5f1ca3b99806eee283a8125d33a0bd669e6e6d9bc4ab7ffda63c41bf4"
        ;;
    arm64|aarch64)
        uv_target="aarch64-unknown-linux-gnu"
        uv_sha256="19b7f1f66895261fbaa07f8ea91da0f86337ad4e47efa594e87641c1718ffc52"
        ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

bin_dir="${HOME}/.local/bin"
uv_bin="${bin_dir}/uv"
graphify_bin="${bin_dir}/graphify"
mkdir -p "${bin_dir}"

uv_version_output="$("${uv_bin}" --version 2>/dev/null || true)"
if [[ ! -x "${uv_bin}" ]] ||
   { [[ "${uv_version_output}" != "uv ${UV_VERSION}" ]] && [[ "${uv_version_output}" != "uv ${UV_VERSION} "* ]]; }; then
    archive_name="uv-${uv_target}.tar.gz"
    temp_dir="$(mktemp -d)"
    trap 'rm -rf "${temp_dir}"' EXIT

    echo "Installing uv ${UV_VERSION}..."
    curl -fsSL "${UV_RELEASE_BASE}/${archive_name}" -o "${temp_dir}/${archive_name}"
    printf '%s  %s\n' "${uv_sha256}" "${temp_dir}/${archive_name}" | sha256sum --check --status
    tar -xzf "${temp_dir}/${archive_name}" -C "${temp_dir}"
    install -m 0755 "${temp_dir}/uv-${uv_target}/uv" "${uv_bin}"
    install -m 0755 "${temp_dir}/uv-${uv_target}/uvx" "${bin_dir}/uvx"

    rm -rf "${temp_dir}"
    trap - EXIT
fi

export PATH="${bin_dir}:${PATH}"

if [[ ! -x "${graphify_bin}" ]] || [[ "$("${graphify_bin}" --version 2>/dev/null || true)" != "graphify ${GRAPHIFY_VERSION}" ]]; then
    echo "Installing Graphify ${GRAPHIFY_VERSION}..."
    UV_TOOL_BIN_DIR="${bin_dir}" "${uv_bin}" tool install \
        --force \
        --no-config \
        --python 3.11 \
        --no-python-downloads \
        --constraints "${here}/graphify-constraints.txt" \
        "${GRAPHIFY_WHEEL_URL}"
fi

installed_version="$("${graphify_bin}" --version)"
if [[ "${installed_version}" != "graphify ${GRAPHIFY_VERSION}" ]]; then
    echo "Graphify version check failed: got ${installed_version}" >&2
    exit 1
fi

echo "✅ Graphify CLI installed: ${graphify_bin} (${installed_version})"
