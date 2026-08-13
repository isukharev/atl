#!/bin/sh
set -eu

[ "${ATL_READ_ONLY:-}" = 1 ]
[ "${ATL_NO_UPDATE:-}" = 1 ]
[ -z "${JIRA_URL:-}${JIRA_PAT:-}${CONFLUENCE_URL:-}${CONFLUENCE_PAT:-}" ]
[ -z "${TEST_JIRA_PAT:-}${TEST_CONFLUENCE_PAT:-}${ATL_INTEGRATION:-}" ]
[ -z "${ATL_ALLOW_INSECURE:-}${ATL_UPDATE_URL:-}" ]
[ -z "${ATL_POLICY:-}${ATL_POLICY_FILE:-}${ATL_POLICY_SHA256:-}${ATL_POLICY_REQUIRED:-}${ATL_MIRROR_ROOT:-}" ]
[ -z "${SSL_CERT_DIR:-}${HTTP_PROXY:-}${HTTPS_PROXY:-}${ALL_PROXY:-}${NO_PROXY:-}" ]
[ -z "${http_proxy:-}${https_proxy:-}${all_proxy:-}${no_proxy:-}${UNRELATED_SECRET:-}" ]
[ "${ATL_CACHE_ROOT+x}" != x ]
[ "${ATL_INITIALIZE_CACHE+x}" != x ]
[ "${ATL_CACHE_MAX_REQUESTS+x}" != x ]
[ "${ATL_CACHE_MAX_RESPONSE_BYTES+x}" != x ]
[ "${ATL_CACHE_DEADLINE+x}" != x ]
fake_log=${ATL_CONFIG_DIR%/*}/atl-argv.log
printf '%s\n' "$*" >>"$fake_log"
case "$1:$2" in
	corpus:build)
		[ ! -f "$0.fail-build" ] || exit 9
		shift 2
		store=
		cache_store=
		initialize_cache=0
		cache_max_requests=
		cache_max_response_bytes=
		cache_deadline=
		while [ "$#" -gt 0 ]; do
			case "$1" in
				--root) shift; store=$1 ;;
				--cache-root) shift; cache_store=$1 ;;
				--initialize-cache) initialize_cache=1 ;;
				--cache-max-requests) shift; cache_max_requests=$1 ;;
				--cache-max-response-bytes) shift; cache_max_response_bytes=$1 ;;
				--cache-deadline) shift; cache_deadline=$1 ;;
			esac
			shift
		done
		[ -n "$store" ]
		if [ -n "$cache_store" ]; then
			[ -n "$cache_max_requests" ]
			[ -n "$cache_max_response_bytes" ]
			[ -n "$cache_deadline" ]
			if [ -z "${SSL_CERT_FILE:-}" ]; then
				[ "${ATL_JIRA_CA_BUNDLE+x}" != x ]
				[ "${ATL_CONFLUENCE_CA_BUNDLE+x}" != x ]
			else
				if [ -n "${ATL_JIRA_URL:-}" ]; then
					[ "${ATL_JIRA_CA_BUNDLE+x}" = x ]
					[ "$ATL_JIRA_CA_BUNDLE" = "$SSL_CERT_FILE" ]
				else
					[ "${ATL_JIRA_CA_BUNDLE+x}" != x ]
				fi
				if [ -n "${ATL_CONFLUENCE_URL:-}" ]; then
					[ "${ATL_CONFLUENCE_CA_BUNDLE+x}" = x ]
					[ "$ATL_CONFLUENCE_CA_BUNDLE" = "$SSL_CERT_FILE" ]
				else
					[ "${ATL_CONFLUENCE_CA_BUNDLE+x}" != x ]
				fi
			fi
			if [ "$initialize_cache" = 1 ]; then
				install -d -m 0700 "$cache_store/generations"
			else
				[ -d "$cache_store/generations" ]
			fi
			store=$cache_store
		else
			[ "$initialize_cache" = 0 ]
			[ -z "$cache_max_requests$cache_max_response_bytes$cache_deadline" ]
			[ "${ATL_JIRA_CA_BUNDLE+x}" != x ]
			[ "${ATL_CONFLUENCE_CA_BUNDLE+x}" != x ]
		fi
		generation=11111111111111111111111111111111
		if [ -n "${ATL_JIRA_URL:-}" ] && [ -n "${ATL_CONFLUENCE_URL:-}" ]; then
			service=aggregate
		elif [ -n "${ATL_CONFLUENCE_URL:-}" ]; then
			service=confluence
		else
			service=jira
		fi
		document="$store/generations/$generation/artifacts/projection/$service/documents.indexer-v1.jsonl"
		mkdir -p "$(dirname "$document")"
		printf '{"synthetic":"document"}\n' >"$document"
		chmod 0600 "$document"
		printf '{}\n'
		;;
	corpus:handoff)
		[ "${ATL_JIRA_CA_BUNDLE+x}" != x ]
		[ "${ATL_CONFLUENCE_CA_BUNDLE+x}" != x ]
		[ "${SSL_CERT_FILE+x}" != x ]
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
		service=
		for candidate in aggregate jira confluence; do
			candidate_document="$store/generations/$generation/artifacts/projection/$candidate/documents.indexer-v1.jsonl"
			if [ -f "$candidate_document" ]; then
				[ -z "$service" ]
				service=$candidate
				document=$candidate_document
			fi
		done
		[ -n "$service" ]
		digest=$(sha256sum "$document" | awk '{print $1}')
		size=$(wc -c <"$document" | tr -d ' ')
		printf '{"schema_version":1,"generation_id":"%s","generation_digest":"%s","projection_schema":2,"documents":{"service":"%s","stable_id":"indexer-v1-documents","role":"document","path":"projection/%s/documents.indexer-v1.jsonl","size":%s,"mode":384,"sha256":"%s"}}\n' \
			"$generation" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$service" "$service" "$size" "$digest" >"$artifact"
		chmod 0600 "$artifact"
		printf '{}\n'
		;;
	*) exit 8 ;;
esac
