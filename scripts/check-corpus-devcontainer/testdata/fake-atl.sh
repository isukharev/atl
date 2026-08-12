#!/bin/sh
set -eu

[ "${ATL_READ_ONLY:-}" = 1 ]
[ "${ATL_NO_UPDATE:-}" = 1 ]
[ -z "${JIRA_URL:-}${JIRA_PAT:-}${CONFLUENCE_URL:-}${CONFLUENCE_PAT:-}" ]
[ -z "${TEST_JIRA_PAT:-}${TEST_CONFLUENCE_PAT:-}${ATL_INTEGRATION:-}" ]
[ -z "${ATL_JIRA_CA_BUNDLE:-}${ATL_CONFLUENCE_CA_BUNDLE:-}${ATL_ALLOW_INSECURE:-}${ATL_UPDATE_URL:-}" ]
[ -z "${ATL_POLICY:-}${ATL_POLICY_FILE:-}${ATL_POLICY_SHA256:-}${ATL_POLICY_REQUIRED:-}${ATL_MIRROR_ROOT:-}" ]
[ -z "${SSL_CERT_DIR:-}${HTTP_PROXY:-}${HTTPS_PROXY:-}${ALL_PROXY:-}${NO_PROXY:-}" ]
[ -z "${http_proxy:-}${https_proxy:-}${all_proxy:-}${no_proxy:-}${UNRELATED_SECRET:-}" ]
fake_log=${ATL_CONFIG_DIR%/*}/atl-argv.log
printf '%s\n' "$*" >>"$fake_log"
case "$1:$2" in
	corpus:build)
		[ ! -f "$0.fail-build" ] || exit 9
		shift 2
		store=
		while [ "$#" -gt 0 ]; do
			if [ "$1" = --root ]; then
				shift
				store=$1
			fi
			shift
		done
		[ -n "$store" ]
		generation=11111111111111111111111111111111
		document="$store/generations/$generation/artifacts/projection/jira/documents.indexer-v1.jsonl"
		mkdir -p "$(dirname "$document")"
		printf '{"synthetic":"document"}\n' >"$document"
		chmod 0600 "$document"
		printf '{}\n'
		;;
	corpus:handoff)
		shift 2
		store=
		artifact=
		while [ "$#" -gt 0 ]; do
			case "$1" in
				--store) shift; store=$1 ;;
				--handoff-artifact) shift; artifact=$1 ;;
			esac
			shift
		done
		generation=11111111111111111111111111111111
		document="$store/generations/$generation/artifacts/projection/jira/documents.indexer-v1.jsonl"
		digest=$(sha256sum "$document" | awk '{print $1}')
		size=$(wc -c <"$document" | tr -d ' ')
		printf '{"schema_version":1,"generation_id":"%s","generation_digest":"%s","projection_schema":2,"documents":{"service":"jira","stable_id":"indexer-v1-documents","role":"document","path":"projection/jira/documents.indexer-v1.jsonl","size":%s,"mode":384,"sha256":"%s"}}\n' \
			"$generation" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$size" "$digest" >"$artifact"
		chmod 0600 "$artifact"
		printf '{}\n'
		;;
	*) exit 8 ;;
esac
