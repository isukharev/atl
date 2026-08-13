#!/bin/sh
set -eu

umask 077

[ -f "${ATL_CORPUS_DOCUMENT:-}" ] || exit 2
[ -d "${ATL_INDEX_ROOT:-}" ] || exit 2
[ -z "${ATL_JIRA_PAT:-}${ATL_CONFLUENCE_PAT:-}${ATL_JIRA_URL:-}${ATL_CONFLUENCE_URL:-}" ] || exit 2
[ -z "${JIRA_PAT:-}${CONFLUENCE_PAT:-}${JIRA_URL:-}${CONFLUENCE_URL:-}" ] || exit 2
[ -z "${TEST_JIRA_PAT:-}${TEST_CONFLUENCE_PAT:-}${ATL_INTEGRATION:-}" ] || exit 2
[ -z "${ATL_JIRA_PAT_FILE:-}${ATL_JIRA_URL_FILE:-}${ATL_JIRA_PROJECT_FILE:-}" ] || exit 2
[ -z "${ATL_CONFLUENCE_PAT_FILE:-}${ATL_CONFLUENCE_URL_FILE:-}${ATL_CONFLUENCE_SPACE_FILE:-}" ] || exit 2
[ -z "${ATL_CA_FILE:-}${ATL_ATTACHMENT_MEDIA_TYPES_FILE:-}${SSL_CERT_FILE:-}${ATL_CONFIG_DIR:-}${ATL_SOURCE_ROOT:-}" ] || exit 2
[ -z "${ATL_JIRA_CA_BUNDLE:-}${ATL_CONFLUENCE_CA_BUNDLE:-}${ATL_ALLOW_INSECURE:-}${ATL_UPDATE_URL:-}" ] || exit 2
[ "${ATL_CACHE_ROOT+x}" != x ] || exit 2
[ "${ATL_INITIALIZE_CACHE+x}" != x ] || exit 2
[ "${ATL_CACHE_MAX_REQUESTS+x}" != x ] || exit 2
[ "${ATL_CACHE_MAX_RESPONSE_BYTES+x}" != x ] || exit 2
[ "${ATL_CACHE_DEADLINE+x}" != x ] || exit 2
[ -z "${ATL_POLICY:-}${ATL_POLICY_FILE:-}${ATL_POLICY_SHA256:-}${ATL_POLICY_REQUIRED:-}${ATL_MIRROR_ROOT:-}" ] || exit 2
[ -z "${SSL_CERT_DIR:-}${HTTP_PROXY:-}${HTTPS_PROXY:-}${ALL_PROXY:-}${NO_PROXY:-}" ] || exit 2
[ -z "${http_proxy:-}${https_proxy:-}${all_proxy:-}${no_proxy:-}${UNRELATED_SECRET:-}" ] || exit 2
[ -d "${HOME:-}" ] && [ "$(stat -c '%a' "$HOME")" = 700 ] || exit 2
[ "$(stat -c '%a' "$ATL_CORPUS_DOCUMENT")" = 600 ] || exit 2
[ "$(sha256sum "$ATL_CORPUS_DOCUMENT" | awk '{print $1}')" = "${ATL_CORPUS_DOCUMENT_SHA256:-}" ] || exit 2
target="$ATL_INDEX_ROOT/index-receipt.v1.json"
[ ! -e "$target" ] || exit 2
printf '{"schema_version":1,"generation_digest":"%s","document_digest":"%s"}\n' \
	"$ATL_CORPUS_GENERATION_DIGEST" "$ATL_CORPUS_DOCUMENT_SHA256" >"$target"
chmod 0600 "$target"
