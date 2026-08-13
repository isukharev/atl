#!/bin/sh
set -eu

umask 077

PATH=/usr/local/bin:/usr/bin:/bin
export PATH

[ -f "${ATL_CORPUS_DOCUMENT:-}" ] || { printf '%s\n' "graphify handoff document is missing" >&2; exit 2; }
[ ! -L "$ATL_CORPUS_DOCUMENT" ] || { printf '%s\n' "graphify handoff document must not be a symlink" >&2; exit 2; }
[ -d "${ATL_INDEX_ROOT:-}" ] || { printf '%s\n' "graphify output root is missing" >&2; exit 2; }
case "$ATL_CORPUS_DOCUMENT" in
	*/documents.indexer-v1.txt) ;;
	*) printf '%s\n' "graphify handoff document route is unsupported" >&2; exit 2 ;;
esac
document_root=${ATL_CORPUS_DOCUMENT%/*}
[ -d "$document_root" ] && [ ! -L "$document_root" ] || { printf '%s\n' "graphify input root is unsafe" >&2; exit 2; }
[ "$(find "$document_root" -mindepth 1 -maxdepth 1 -printf x | wc -c)" = 1 ] || {
	printf '%s\n' "graphify input root must contain exactly one document" >&2
	exit 2
}
backend=${GRAPHIFY_BACKEND:-}
[ -n "$backend" ] || { printf '%s\n' "GRAPHIFY_BACKEND must be explicit" >&2; exit 2; }
graphify_bin=${GRAPHIFY_BIN:-/usr/local/bin/graphify}
case "$graphify_bin" in
	/*) ;;
	*) printf '%s\n' "GRAPHIFY_BIN must be an absolute executable path" >&2; exit 2 ;;
esac
[ -f "$graphify_bin" ] && [ ! -L "$graphify_bin" ] && [ -x "$graphify_bin" ] || {
	printf '%s\n' "GRAPHIFY_BIN must be a regular executable" >&2
	exit 2
}

is_loopback_ollama_endpoint() {
	endpoint=$1
	case "$endpoint" in
		http://127.0.0.1:*) port=${endpoint#http://127.0.0.1:} ;;
		http://localhost:*) port=${endpoint#http://localhost:} ;;
		http://\[::1\]:*) port=${endpoint#http://\[::1\]:} ;;
		*) return 1 ;;
	esac
	case "$port" in
		''|*[!0-9]*) return 1 ;;
	esac
	[ "${#port}" -le 5 ] && [ "$port" -gt 0 ] && [ "$port" -le 65535 ]
}

case "$backend" in
	ollama)
		ollama_endpoint=${OLLAMA_HOST:-http://127.0.0.1:11434}
		is_loopback_ollama_endpoint "$ollama_endpoint" ||
			[ "${ATL_APPROVE_SEMANTIC_EGRESS:-}" = 1 ] || {
				printf '%s\n' "non-loopback semantic egress is not approved" >&2
				exit 2
			}
		;;
	*)
		[ "${ATL_APPROVE_SEMANTIC_EGRESS:-}" = 1 ] || { printf '%s\n' "semantic provider egress is not approved" >&2; exit 2; }
		;;
esac

exec "$graphify_bin" extract "$document_root" \
	--out "$ATL_INDEX_ROOT" \
	--backend="$backend" \
	--no-cluster
