#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -f .env.integration ]]; then
  echo "missing .env.integration - run: cp .env.integration.example .env.integration && edit it" >&2
  exit 1
fi

command -v jq >/dev/null 2>&1 || { echo "missing jq" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "missing python3" >&2; exit 1; }

set -a
. ./.env.integration
set +a

export ATL_INTEGRATION=1
export ATL_NO_UPDATE=1

ATL_BIN="${ATL_BIN:-$ROOT/atl}"
if [[ ! -x "$ATL_BIN" ]]; then
  echo "missing executable $ATL_BIN - run make build" >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ok() {
  printf 'OK %s\n' "$1"
}

skip() {
  printf 'SKIP %s\n' "$1"
}

jira_jql="${ATL_TEST_JIRA_JQL:-}"
if [[ -z "$jira_jql" && -n "${ATL_TEST_JIRA_PROJECT:-}" ]]; then
  jira_jql="project=${ATL_TEST_JIRA_PROJECT}"
fi

if [[ -n "${ATL_TEST_JIRA_FIELD:-}" ]]; then
  "$ATL_BIN" jira fields --id "$ATL_TEST_JIRA_FIELD" > "$tmp/jira-field.json"
  jq -e '.fields | length >= 1' "$tmp/jira-field.json" >/dev/null
  ok "jira fields"
else
  skip "jira fields (ATL_TEST_JIRA_FIELD unset)"
fi

if [[ -n "$jira_jql" ]]; then
  limit="${ATL_LIVE_SMOKE_LIMIT:-5}"
  export_args=(jira export --jql "$jira_jql" --limit "$limit" --format jsonl --out "$tmp/issues.jsonl")
  if [[ -n "${ATL_TEST_JIRA_EXPORT_FIELDS:-}" ]]; then
    export_args+=(--fields "$ATL_TEST_JIRA_EXPORT_FIELDS")
  elif [[ -n "${ATL_TEST_JIRA_FIELD:-}" ]]; then
    export_args+=(--fields "$ATL_TEST_JIRA_FIELD")
  fi
  "$ATL_BIN" "${export_args[@]}" >/dev/null
  test -s "$tmp/issues.jsonl"
  test -s "$tmp/issues.jsonl.manifest.json"
  python3 - "$tmp/issues.jsonl" "$tmp/issues.jsonl.manifest.json" <<'PY'
import json
import pathlib
import sys

issues = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines() if line.strip()]
if not issues:
    raise SystemExit("empty Jira export")
for issue in issues:
    if not issue.get("key") or not issue.get("id"):
        raise SystemExit("exported issue missing key/id")
manifest = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
manifest_text = json.dumps(manifest, ensure_ascii=False)
if "http://" in manifest_text or "https://" in manifest_text:
    raise SystemExit("manifest contains backend URL")
PY
  first_key="$(head -n 1 "$tmp/issues.jsonl" | jq -r '.key // empty')"
  if [[ -z "$first_key" ]]; then
    echo "Jira export returned no issue key" >&2
    exit 1
  fi
  "$ATL_BIN" jira export --keys "$first_key" --batch-size 1 --out "$tmp/by-key.jsonl" >/dev/null
  test -s "$tmp/by-key.jsonl"
  "$ATL_BIN" jira export diff "$tmp/issues.jsonl" "$tmp/by-key.jsonl" > "$tmp/export-diff.json"
  jq -e '.old_count >= .new_count and (.removed | type == "array" or .removed == null)' "$tmp/export-diff.json" >/dev/null
  ok "jira export"

  planning_args=(jira planning report --jql "$jira_jql" --limit "$limit" --csv "$tmp/planning.csv")
  [[ -n "${ATL_TEST_JIRA_EPIC_FIELD:-}" ]] && planning_args+=(--epic-field "$ATL_TEST_JIRA_EPIC_FIELD")
  [[ -n "${ATL_TEST_JIRA_ESTIMATE_FIELD:-}" ]] && planning_args+=(--estimate-field "$ATL_TEST_JIRA_ESTIMATE_FIELD")
  [[ -n "${ATL_TEST_JIRA_REQUIRED_FIELDS:-}" ]] && planning_args+=(--require "$ATL_TEST_JIRA_REQUIRED_FIELDS")
  "$ATL_BIN" "${planning_args[@]}" > "$tmp/planning.json"
  jq -e '.count >= 0 and (.issues | type == "array") and (.summary | type == "object")' "$tmp/planning.json" >/dev/null
  test -s "$tmp/planning.csv"
  ok "jira planning"

  pull_args=(jira pull --jql "$jira_jql" --limit 1 --into "$tmp/jira-mirror")
  if [[ -n "${ATL_TEST_JIRA_EXPORT_FIELDS:-}" ]]; then
    pull_args+=(--fields "$ATL_TEST_JIRA_EXPORT_FIELDS")
  elif [[ -n "${ATL_TEST_JIRA_FIELD:-}" ]]; then
    pull_args+=(--fields "$ATL_TEST_JIRA_FIELD")
  fi
  "$ATL_BIN" "${pull_args[@]}" >/dev/null
  find "$tmp/jira-mirror" -name '*.json' -print -quit | grep -q .
  ok "jira pull"
else
  skip "jira export/planning/pull (ATL_TEST_JIRA_JQL or ATL_TEST_JIRA_PROJECT unset)"
fi

if [[ -n "${ATL_TEST_JIRA_STRUCTURE_ID:-}" ]]; then
  "$ATL_BIN" jira structure get "$ATL_TEST_JIRA_STRUCTURE_ID" > "$tmp/structure.json"
  jq -e '.id != null and .name != null' "$tmp/structure.json" >/dev/null
  "$ATL_BIN" jira structure forest "$ATL_TEST_JIRA_STRUCTURE_ID" > "$tmp/structure-forest.json"
  jq -e '.formula != null and (.formula | length > 0)' "$tmp/structure-forest.json" >/dev/null
  "$ATL_BIN" jira structure rows "$ATL_TEST_JIRA_STRUCTURE_ID" > "$tmp/structure-rows.json"
  jq -e '.rows | length > 0' "$tmp/structure-rows.json" >/dev/null
  row_ids="$(jq -r '[.rows[0:10][].row_id] | join(",")' "$tmp/structure-rows.json")"
  if [[ -z "$row_ids" ]]; then
    echo "Structure row ids missing" >&2
    exit 1
  fi
  "$ATL_BIN" jira structure values "$ATL_TEST_JIRA_STRUCTURE_ID" --rows "$row_ids" --fields key,summary,status > "$tmp/structure-values.json"
  jq -e '.responses != null or .raw != null' "$tmp/structure-values.json" >/dev/null
  ok "jira structure"
else
  skip "jira structure (ATL_TEST_JIRA_STRUCTURE_ID unset)"
fi

conf_table_page_id="${ATL_TEST_CONFLUENCE_TABLE_PAGE_ID:-}"
if [[ -n "$conf_table_page_id" ]]; then
  "$ATL_BIN" conf pull --id "$conf_table_page_id" --into "$tmp/conf" >/dev/null
  md_file="$(find "$tmp/conf" -name '*.md' -print -quit)"
  csf_file="$(find "$tmp/conf" -name '*.csf' -print -quit)"
  if [[ -z "$md_file" || -z "$csf_file" ]]; then
    echo "Confluence pull did not write markdown and CSF files" >&2
    exit 1
  fi
  python3 - "$md_file" "$csf_file" <<'PY'
import html
import pathlib
import re
import sys

md = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
csf = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
table_lines = [line for line in md.splitlines() if "|" in line]
if len(table_lines) < 2 or not any("|" in line and "---" in line for line in table_lines):
    raise SystemExit("markdown table missing")

plain_md = re.sub(r"\u27e6/?color[^\u27e7]*\u27e7", "", md)
plain_md = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", plain_md)
plain_md = re.sub(r"\s+", " ", plain_md)

for match in re.finditer(r"<t[dh]\b[^>]*\browspan=[\"']([2-9][0-9]*)[\"'][^>]*>(.*?)</t[dh]>", csf, re.I | re.S):
    span = int(match.group(1))
    cell_text = html.unescape(re.sub(r"<[^>]+>", " ", match.group(2)))
    cell_text = re.sub(r"\s+", " ", cell_text).strip()
    if cell_text and plain_md.count(cell_text) < span:
        raise SystemExit("rowspan cell was not repeated in markdown")

hrefs = re.findall(r"<a\b[^>]*\bhref=[\"']([^\"']+)[\"']", csf, re.I)
if hrefs and not any(href in md for href in hrefs):
    raise SystemExit("table link URL missing from markdown")

if re.search(r"<span\b[^>]*\bstyle=[\"'][^\"']*color\s*:", csf, re.I) and "\u27e6color:" not in md:
    raise SystemExit("color marker missing from markdown")
PY
  ok "confluence table"
else
  skip "confluence table (ATL_TEST_CONFLUENCE_TABLE_PAGE_ID unset)"
fi

ok "live smoke"
