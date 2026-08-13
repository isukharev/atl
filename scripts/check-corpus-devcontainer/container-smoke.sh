#!/bin/sh
set -eu

umask 077

fail() {
	printf '%s\n' "corpus devcontainer runtime smoke failed: $1" >&2
	exit 1
}

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd -P)
before_status=$(git -C "$repository_root" status --porcelain=v1 --untracked-files=all)
temporary=$(mktemp -d /tmp/atl-devcontainer-smoke.XXXXXX) || fail "could not allocate temporary root"
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM
chmod 0700 "$temporary"

source_root=$repository_root
index_root=$temporary/index
context_parent=$temporary/contexts
mkdir -m 0700 "$index_root" "$context_parent"
secret=synthetic-container-secret-canary
printf '%s\n' 'https://backend.example.test' >"$temporary/jira-url"
printf '%s\n' "$secret" >"$temporary/jira-pat"
printf '%s\n' 'EXAMPLE' >"$temporary/jira-project"
chmod 0600 "$temporary/jira-url" "$temporary/jira-pat" "$temporary/jira-project"

stdout=$temporary/stdout
stderr=$temporary/stderr
ATL_SOURCE_ROOT=$source_root \
ATL_INDEX_ROOT=$index_root \
ATL_INDEXER=$repository_root/examples/corpus-devcontainer/local-indexer-stub.sh \
ATL_BIN=$repository_root/scripts/check-corpus-devcontainer/testdata/fake-atl.sh \
ATL_CONTEXT_PARENT=$context_parent \
ATL_JIRA_URL_FILE=$temporary/jira-url \
ATL_JIRA_PAT_FILE=$temporary/jira-pat \
ATL_JIRA_PROJECT_FILE=$temporary/jira-project \
ATL_MAX_JIRA_ISSUES=10 \
ATL_MAX_REQUESTS=100 \
ATL_MAX_RESPONSE_BYTES=1048576 \
ATL_MAX_MEMBERS=1000 \
ATL_MAX_GENERATION_BYTES=10485760 \
ATL_DEADLINE=5m \
ATL_MAX_IN_FLIGHT=2 \
ATL_REQUESTS_PER_SECOND=10 \
ATL_CAPTURE_COMMENTS=0 \
ATL_CAPTURE_ATTACHMENTS=0 \
ATL_CAPTURE_ATTACHMENT_BODIES=0 \
ATL_INITIALIZE_CACHE=not-a-toggle-without-cache \
ATL_CACHE_MAX_REQUESTS=not-a-number-without-cache \
ATL_CACHE_MAX_RESPONSE_BYTES=not-a-number-without-cache \
ATL_CACHE_DEADLINE= \
SSL_CERT_DIR=/synthetic/ambient-certificates \
HTTPS_PROXY=http://ambient-proxy.example.test:8080 \
HTTP_PROXY=http://ambient-proxy.example.test:8080 \
ALL_PROXY=socks5://ambient-proxy.example.test:1080 \
NO_PROXY=backend.example.test \
UNRELATED_SECRET=synthetic-container-unrelated-secret-canary \
	"$repository_root/examples/corpus-devcontainer/run-corpus.sh" >"$stdout" 2>"$stderr" ||
	fail "bootstrap returned an error"

[ "$(cat "$stdout")" = '{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}' ] ||
	fail "bootstrap output drifted"
[ ! -s "$stderr" ] || fail "bootstrap wrote diagnostics"
[ -f "$index_root/index-receipt.v1.json" ] || fail "stub index receipt is missing"
[ "$(stat -c '%a' "$index_root/index-receipt.v1.json")" = 600 ] || fail "index receipt is not private"
[ "$(find "$context_parent" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')" = 1 ] ||
	fail "runtime root count drifted"
find "$context_parent" -type d ! -perm 0700 -print -quit | grep -q . && fail "runtime directory is not private"
argv_log=$(find "$context_parent" -type f -name atl-argv.log -print -quit)
[ -n "$argv_log" ] && [ "$(wc -l <"$argv_log" | tr -d ' ')" = 2 ] || fail "ATL command boundary drifted"
runtime_root=${argv_log%/atl-argv.log}
expected_build="corpus build --root $runtime_root/corpus --initialize --max-requests 100 --max-response-bytes 1048576 --max-members 1000 --max-generation-bytes 10485760 --deadline 5m --max-in-flight 2 --requests-per-second 10 --jira-project EXAMPLE --max-jira-issues 10"
expected_handoff="corpus handoff --store $runtime_root/corpus --handoff-artifact $runtime_root/handoff/current.indexer-handoff.v1.json"
[ "$(sed -n '1p' "$argv_log")" = "$expected_build" ] || fail "no-cache build argv drifted"
[ "$(sed -n '2p' "$argv_log")" = "$expected_handoff" ] || fail "no-cache handoff argv drifted"
grep -R -F "$secret" "$context_parent" "$index_root" "$stdout" "$stderr" >/dev/null 2>&1 &&
	fail "runtime secret entered generated state"

after_status=$(git -C "$repository_root" status --porcelain=v1 --untracked-files=all)
[ "$before_status" = "$after_status" ] || fail "source checkout changed"
ATL_NO_UPDATE=1 atl version >/dev/null 2>&1 || fail "attested release binary is unavailable"
[ -x "$repository_root/atl" ] || fail "current ATL test binary is unavailable"
[ -x "$repository_root/tmp/corpus-devcontainer-check" ] || fail "compiled runtime checker is unavailable"
"$repository_root/tmp/corpus-devcontainer-check" -root "$repository_root" -atl "$repository_root/atl" >/dev/null ||
	fail "current ATL loopback build failed"
after_status=$(git -C "$repository_root" status --porcelain=v1 --untracked-files=all)
[ "$before_status" = "$after_status" ] || fail "current ATL smoke changed the source checkout"
printf '%s\n' 'corpus devcontainer runtime smoke: sealed handoff verified'
