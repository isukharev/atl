#!/bin/sh
set -eu

umask 077

# Resolve only trusted system helpers. ATL and the downstream indexer are
# absolute executables and receive explicit allowlisted environments below.
PATH=/usr/local/bin:/usr/bin:/bin
export PATH

fail() {
	printf '%s\n' "atl-corpus-bootstrap: $1" >&2
	exit 1
}

require_owner_directory() {
	path=$1
	[ -d "$path" ] && [ ! -L "$path" ] || fail "$2 must be an existing directory"
	[ "$(stat -c '%a' "$path")" = 700 ] || fail "$2 must have exact mode 0700"
	[ "$(stat -c '%u' "$path")" = "$(id -u)" ] || fail "$2 must be owned by the current user"
}

absolute_directory() {
	case "$1" in
		/*) ;;
		*) fail "$2 must be absolute" ;;
	esac
	realpath -e "$1" || fail "$2 could not be resolved"
}

reject_overlap() {
	candidate=$1
	boundary=$2
	case "$candidate/" in
		"$boundary/"|"$boundary/"*) fail "$3 overlaps a protected boundary" ;;
	esac
	case "$boundary/" in
		"$candidate/"|"$candidate/"*) fail "$3 overlaps a protected boundary" ;;
	esac
}

require_private_file() {
	path=$1
	[ -f "$path" ] && [ ! -L "$path" ] || fail "$2 must be a regular mounted file"
	[ "$(stat -c '%u' "$path")" = "$(id -u)" ] || fail "$2 must be owned by the current user"
	case "$(stat -c '%a' "$path")" in
		400|600) ;;
		*) fail "$2 must have mode 0400 or 0600" ;;
	esac
	[ "$(wc -c <"$path")" -le "$3" ] || fail "$2 exceeds its byte limit"
}

resolve_private_file() {
	path=$1
	case "$path" in
		/*) ;;
		*) fail "$2 must be absolute" ;;
	esac
	resolved=$(realpath -e "$path") || fail "$2 could not be resolved"
	require_private_file "$resolved" "$2" "$3"
	for boundary in "$source_root" "$index_root" "$context_root"; do
		case "$resolved" in
			"$boundary"|"$boundary/"*) fail "$2 overlaps a protected boundary" ;;
		esac
	done
	printf '%s' "$resolved"
}

read_private_line() {
	path=$1
	require_private_file "$path" "$2" 8192
	[ "$(awk 'END { print NR }' "$path")" -eq 1 ] || fail "$2 must contain exactly one line"
	IFS= read -r private_value <"$path" || [ -n "${private_value:-}" ]
	[ -n "${private_value:-}" ] || fail "$2 is empty"
	printf '%s' "$private_value"
}

require_positive_number() {
	value=$1
	case "$value" in
		''|*[!0-9]*|0) fail "$2 must be a positive integer" ;;
	esac
}

require_toggle() {
	case "$1" in
		0|1) ;;
		*) fail "$2 must be 0 or 1" ;;
	esac
}

for required_command in awk find grep id install jq mktemp realpath sha256sum stat tr wc; do
	command -v "$required_command" >/dev/null 2>&1 || fail "$required_command is required"
done

source_root=$(absolute_directory "${ATL_SOURCE_ROOT:-}" "ATL_SOURCE_ROOT")
index_root=$(absolute_directory "${ATL_INDEX_ROOT:-}" "ATL_INDEX_ROOT")
require_owner_directory "$index_root" "ATL_INDEX_ROOT"
[ -z "$(find "$index_root" -mindepth 1 -maxdepth 1 -print -quit)" ] || fail "ATL_INDEX_ROOT must be empty"

indexer=${ATL_INDEXER:-}
case "$indexer" in
	/*) ;;
	*) fail "ATL_INDEXER must be an absolute executable path" ;;
esac
[ -f "$indexer" ] && [ ! -L "$indexer" ] && [ -x "$indexer" ] || fail "ATL_INDEXER must be a regular executable"

atl_bin=${ATL_BIN:-}
if [ -z "$atl_bin" ]; then
	atl_bin=${HOME:-}/.local/bin/atl
fi
case "$atl_bin" in
	/*) ;;
	*) fail "ATL_BIN must resolve to an absolute executable path" ;;
esac
[ -f "$atl_bin" ] && [ ! -L "$atl_bin" ] && [ -x "$atl_bin" ] || fail "ATL_BIN must be a regular executable"

context_parent=${ATL_CONTEXT_PARENT:-/tmp}
context_parent=$(absolute_directory "$context_parent" "ATL_CONTEXT_PARENT")
if [ "$context_parent" != /tmp ]; then
	require_owner_directory "$context_parent" "ATL_CONTEXT_PARENT"
fi
reject_overlap "$context_parent" "$source_root" "runtime parent"
reject_overlap "$context_parent" "$index_root" "runtime parent"
context_root=$(mktemp -d "$context_parent/atl-context.XXXXXX") || fail "could not allocate private runtime root"
require_owner_directory "$context_root" "runtime root"
reject_overlap "$context_root" "$source_root" "runtime root"
reject_overlap "$index_root" "$source_root" "index root"
reject_overlap "$index_root" "$context_root" "index root"

config_root="$context_root/config"
home_root="$context_root/home"
corpus_root="$context_root/corpus"
handoff_root="$context_root/handoff"
install -d -m 0700 "$config_root" "$home_root" "$corpus_root" "$handoff_root"

selected=0
jira_project=
confluence_space=
jira_url=
jira_pat=
confluence_url=
confluence_pat=
if [ -n "${ATL_JIRA_URL_FILE:-}${ATL_JIRA_PAT_FILE:-}${ATL_JIRA_PROJECT_FILE:-}" ]; then
	[ -n "${ATL_JIRA_URL_FILE:-}" ] && [ -n "${ATL_JIRA_PAT_FILE:-}" ] && [ -n "${ATL_JIRA_PROJECT_FILE:-}" ] || fail "Jira requires URL, PAT, and project files together"
	ATL_JIRA_URL_FILE=$(resolve_private_file "$ATL_JIRA_URL_FILE" "Jira URL file" 8192)
	ATL_JIRA_PAT_FILE=$(resolve_private_file "$ATL_JIRA_PAT_FILE" "Jira PAT file" 8192)
	ATL_JIRA_PROJECT_FILE=$(resolve_private_file "$ATL_JIRA_PROJECT_FILE" "Jira project file" 8192)
	jira_url=$(read_private_line "$ATL_JIRA_URL_FILE" "Jira URL file")
	jira_pat=$(read_private_line "$ATL_JIRA_PAT_FILE" "Jira PAT file")
	jira_project=$(read_private_line "$ATL_JIRA_PROJECT_FILE" "Jira project file")
	require_positive_number "${ATL_MAX_JIRA_ISSUES:-}" "ATL_MAX_JIRA_ISSUES"
	selected=$((selected + 1))
fi
if [ -n "${ATL_CONFLUENCE_URL_FILE:-}${ATL_CONFLUENCE_PAT_FILE:-}${ATL_CONFLUENCE_SPACE_FILE:-}" ]; then
	[ -n "${ATL_CONFLUENCE_URL_FILE:-}" ] && [ -n "${ATL_CONFLUENCE_PAT_FILE:-}" ] && [ -n "${ATL_CONFLUENCE_SPACE_FILE:-}" ] || fail "Confluence requires URL, PAT, and space files together"
	ATL_CONFLUENCE_URL_FILE=$(resolve_private_file "$ATL_CONFLUENCE_URL_FILE" "Confluence URL file" 8192)
	ATL_CONFLUENCE_PAT_FILE=$(resolve_private_file "$ATL_CONFLUENCE_PAT_FILE" "Confluence PAT file" 8192)
	ATL_CONFLUENCE_SPACE_FILE=$(resolve_private_file "$ATL_CONFLUENCE_SPACE_FILE" "Confluence space file" 8192)
	confluence_url=$(read_private_line "$ATL_CONFLUENCE_URL_FILE" "Confluence URL file")
	confluence_pat=$(read_private_line "$ATL_CONFLUENCE_PAT_FILE" "Confluence PAT file")
	confluence_space=$(read_private_line "$ATL_CONFLUENCE_SPACE_FILE" "Confluence space file")
	require_positive_number "${ATL_MAX_CONFLUENCE_PAGES:-}" "ATL_MAX_CONFLUENCE_PAGES"
	selected=$((selected + 1))
fi
[ "$selected" -gt 0 ] || fail "select Jira, Confluence, or both through mounted files"

ca_file=
if [ -n "${ATL_CA_FILE:-}" ]; then
	ATL_CA_FILE=$(resolve_private_file "$ATL_CA_FILE" "ATL_CA_FILE" 1048576)
	ca_file=$ATL_CA_FILE
fi

run_atl() {
	env -i \
		HOME="$home_root" \
		PATH=/usr/local/bin:/usr/bin:/bin \
		TMPDIR="$context_root" \
		ATL_CONFIG_DIR="$config_root" \
		ATL_READ_ONLY=1 \
		ATL_NO_UPDATE=1 \
		ATL_JIRA_URL="$jira_url" \
		ATL_JIRA_PAT="$jira_pat" \
		ATL_CONFLUENCE_URL="$confluence_url" \
		ATL_CONFLUENCE_PAT="$confluence_pat" \
		SSL_CERT_FILE="$ca_file" \
		"$atl_bin" "$@"
}

run_local_atl() {
	env -i \
		HOME="$home_root" \
		PATH=/usr/local/bin:/usr/bin:/bin \
		TMPDIR="$context_root" \
		ATL_CONFIG_DIR="$config_root" \
		ATL_READ_ONLY=1 \
		ATL_NO_UPDATE=1 \
		"$atl_bin" "$@"
}

require_positive_number "${ATL_MAX_REQUESTS:-}" "ATL_MAX_REQUESTS"
require_positive_number "${ATL_MAX_RESPONSE_BYTES:-}" "ATL_MAX_RESPONSE_BYTES"
require_positive_number "${ATL_MAX_MEMBERS:-}" "ATL_MAX_MEMBERS"
require_positive_number "${ATL_MAX_GENERATION_BYTES:-}" "ATL_MAX_GENERATION_BYTES"
require_positive_number "${ATL_MAX_IN_FLIGHT:-}" "ATL_MAX_IN_FLIGHT"
require_positive_number "${ATL_REQUESTS_PER_SECOND:-}" "ATL_REQUESTS_PER_SECOND"
[ -n "${ATL_DEADLINE:-}" ] || fail "ATL_DEADLINE is required"

set -- corpus build \
	--root "$corpus_root" --initialize \
	--max-requests "$ATL_MAX_REQUESTS" \
	--max-response-bytes "$ATL_MAX_RESPONSE_BYTES" \
	--max-members "$ATL_MAX_MEMBERS" \
	--max-generation-bytes "$ATL_MAX_GENERATION_BYTES" \
	--deadline "$ATL_DEADLINE" \
	--max-in-flight "$ATL_MAX_IN_FLIGHT" \
	--requests-per-second "$ATL_REQUESTS_PER_SECOND"
if [ -n "$jira_project" ]; then
	set -- "$@" --jira-project "$jira_project" --max-jira-issues "$ATL_MAX_JIRA_ISSUES"
fi
if [ -n "$confluence_space" ]; then
	set -- "$@" --confluence-space "$confluence_space" --max-confluence-pages "$ATL_MAX_CONFLUENCE_PAGES"
fi

comments=${ATL_CAPTURE_COMMENTS:-0}
attachments=${ATL_CAPTURE_ATTACHMENTS:-0}
attachment_bodies=${ATL_CAPTURE_ATTACHMENT_BODIES:-0}
require_toggle "$comments" "ATL_CAPTURE_COMMENTS"
require_toggle "$attachments" "ATL_CAPTURE_ATTACHMENTS"
require_toggle "$attachment_bodies" "ATL_CAPTURE_ATTACHMENT_BODIES"
if [ "$comments" = 1 ]; then
	require_positive_number "${ATL_MAX_COMMENT_PAGES_PER_ITEM:-}" "ATL_MAX_COMMENT_PAGES_PER_ITEM"
	require_positive_number "${ATL_MAX_COMMENTS_PER_ITEM:-}" "ATL_MAX_COMMENTS_PER_ITEM"
	set -- "$@" --comments --max-comment-pages-per-item "$ATL_MAX_COMMENT_PAGES_PER_ITEM" --max-comments-per-item "$ATL_MAX_COMMENTS_PER_ITEM"
fi
if [ "$attachments" = 1 ]; then
	require_positive_number "${ATL_MAX_ATTACHMENT_PAGES_PER_ITEM:-}" "ATL_MAX_ATTACHMENT_PAGES_PER_ITEM"
	require_positive_number "${ATL_MAX_ATTACHMENTS_PER_ITEM:-}" "ATL_MAX_ATTACHMENTS_PER_ITEM"
	set -- "$@" --attachments --max-attachment-pages-per-item "$ATL_MAX_ATTACHMENT_PAGES_PER_ITEM" --max-attachments-per-item "$ATL_MAX_ATTACHMENTS_PER_ITEM"
fi
if [ "$attachment_bodies" = 1 ]; then
	[ "$attachments" = 1 ] || fail "attachment bodies require attachment inventory"
	require_positive_number "${ATL_MAX_ATTACHMENT_BYTES:-}" "ATL_MAX_ATTACHMENT_BYTES"
	require_positive_number "${ATL_MAX_TOTAL_ATTACHMENT_BYTES:-}" "ATL_MAX_TOTAL_ATTACHMENT_BYTES"
	[ -n "${ATL_ATTACHMENT_MEDIA_TYPES_FILE:-}" ] || fail "attachment bodies require a media-type file"
	ATL_ATTACHMENT_MEDIA_TYPES_FILE=$(resolve_private_file "$ATL_ATTACHMENT_MEDIA_TYPES_FILE" "attachment media-type file" 8192)
	set -- "$@" --attachment-bodies --max-attachment-bytes "$ATL_MAX_ATTACHMENT_BYTES" --max-total-attachment-bytes "$ATL_MAX_TOTAL_ATTACHMENT_BYTES"
	while IFS= read -r media_type || [ -n "$media_type" ]; do
		[ -n "$media_type" ] || fail "attachment media-type file contains an empty line"
		printf '%s\n' "$media_type" | grep -Eq '^[A-Za-z0-9!#$&^_.+-]+/[A-Za-z0-9!#$&^_.+-]+$' || fail "attachment media-type file contains an invalid value"
		set -- "$@" --attachment-media-type "$media_type"
	done <"$ATL_ATTACHMENT_MEDIA_TYPES_FILE"
fi

run_atl "$@" >"$context_root/build-result.json" || fail "corpus build failed; indexer was not invoked"
# The remaining phases are local and use clean-room child environments. The
# shell retains mounted values only in its own private process until exit.
handoff_artifact="$handoff_root/current.indexer-handoff.v1.json"
run_local_atl corpus handoff --store "$corpus_root" --handoff-artifact "$handoff_artifact" >"$context_root/handoff-result.json" || fail "sealed handoff verification failed"

generation_id=$(jq -er '
	select(
		.schema_version == 1 and
		.projection_schema == 2 and
		(.generation_id | test("^[0-9a-f]{32}$")) and
		(.generation_digest | test("^[0-9a-f]{64}$")) and
		(.documents.service == "jira" or .documents.service == "confluence" or .documents.service == "aggregate") and
		.documents.stable_id == "indexer-v1-documents" and
		.documents.role == "document" and
		.documents.path == ("projection/" + .documents.service + "/documents.indexer-v1.jsonl") and
		(.documents.size | type == "number") and .documents.size >= 0 and
		.documents.mode == 384 and
		(.documents.sha256 | test("^[0-9a-f]{64}$"))
	) | .generation_id
' "$handoff_artifact") || fail "handoff artifact is invalid"
document_rel=$(jq -er '.documents.path' "$handoff_artifact") || fail "handoff document route is missing"
document_digest=$(jq -er '.documents.sha256 | select(test("^[0-9a-f]{64}$"))' "$handoff_artifact") || fail "handoff document digest is invalid"
document_size=$(jq -er '.documents.size' "$handoff_artifact") || fail "handoff document size is missing"
generation_digest=$(jq -er '.generation_digest' "$handoff_artifact") || fail "handoff generation digest is missing"
generation_root="$corpus_root/generations/$generation_id"
document_candidate="$generation_root/artifacts/$document_rel"
[ ! -L "$document_candidate" ] || fail "sealed document member must not be a symlink"
document_path=$(realpath -e "$document_candidate") || fail "sealed document member is missing"
case "$document_path" in
	"$generation_root/artifacts/"*) ;;
	*) fail "sealed document member escaped its generation" ;;
esac
[ -f "$document_path" ] && [ ! -L "$document_path" ] || fail "sealed document member must be regular"
[ "$(stat -c '%a' "$document_path")" = 600 ] || fail "sealed document member must have mode 0600"
[ "$(wc -c <"$document_path" | tr -d ' ')" = "$document_size" ] || fail "sealed document member size changed"
[ "$(sha256sum "$document_path" | awk '{print $1}')" = "$document_digest" ] || fail "sealed document member digest changed"

indexer_input_root="$context_root/indexer-input"
indexer_home="$context_root/indexer-home"
install -d -m 0700 "$indexer_input_root" "$indexer_home"
# The sealed source remains canonical JSONL. The isolated byte-identical copy
# uses a document extension so document-oriented indexers do not classify the
# handoff as source code or silently ignore the JSONL suffix.
indexer_document="$indexer_input_root/documents.indexer-v1.txt"
install -m 0600 "$document_path" "$indexer_document" || fail "could not stage the sealed document member"
[ "$(wc -c <"$indexer_document" | tr -d ' ')" = "$document_size" ] || fail "staged document member size changed"
[ "$(sha256sum "$indexer_document" | awk '{print $1}')" = "$document_digest" ] || fail "staged document member digest changed"

cd "$indexer_home"
if env -i \
	HOME="$indexer_home" \
	PATH=/usr/local/bin:/usr/bin:/bin \
	TMPDIR="$indexer_home" \
	ATL_CORPUS_DOCUMENT="$indexer_document" \
	ATL_CORPUS_DOCUMENT_SHA256="$document_digest" \
	ATL_CORPUS_GENERATION_DIGEST="$generation_digest" \
	ATL_INDEX_ROOT="$index_root" \
	GRAPHIFY_BIN="${GRAPHIFY_BIN:-}" \
	GRAPHIFY_BACKEND="${GRAPHIFY_BACKEND:-}" \
	OLLAMA_HOST="${OLLAMA_HOST:-}" \
	ATL_APPROVE_SEMANTIC_EGRESS="${ATL_APPROVE_SEMANTIC_EGRESS:-}" \
	"$indexer" >"$context_root/indexer.stdout" 2>"$context_root/indexer.stderr"; then
	printf '%s\n' '{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}'
else
	fail "indexer failed; private diagnostics remain in the runtime root"
fi
