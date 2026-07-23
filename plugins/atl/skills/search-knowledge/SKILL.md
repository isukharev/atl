---
name: search-knowledge
description: Answer from qualified Jira and Confluence evidence with atl. USE WHEN organizational knowledge is unknown or split across both services. DO NOT USE WHEN one exact resource is known, the task is a report/dashboard, or the answer belongs in the codebase.
---
<!-- Generated from skills-src/search-knowledge/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Search company knowledge with `atl`

Search Confluence and Jira in parallel, read the best hits, and answer with
citations. This recipe is read-only: never create, update, or comment.

Make `export ATL_READ_ONLY=1` the first statement of every Bash block so all
later atl calls and child processes inherit the guard. Never override it in
this workflow. Keep headless blocks to that export plus documented `atl`
commands only; do not add output separators, pipes, help probes, or unrelated
shell/file inspection commands.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `$setup` and stop.
For an unfamiliar mixed-backend question, inspect the bounded offline route
once before loading any broader reference:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl capabilities --task knowledge/search
```

## Workflow

When the plugin exposes typed `atl` MCP tools, use the equivalent transient
route without shell: call `confluence_search` and `jira_issue_search` once,
freeze only complete candidate pages, then use `jira_issue_field_get` plus
`confluence_page_outline` and `confluence_page_section` for the selected
evidence. When the selected evidence is tabular, use
`confluence_table_summary` and then `confluence_table_extract` for one exact
table instead of reading a broader section. Reuse a numeric Confluence result
id directly. Do not mix MCP and CLI
reads merely to repeat already complete evidence; fall back to the CLI workflow
below when MCP is unavailable or the task needs an operation outside its
read-only surface.

### 1. Extract search terms

Keep the core nouns, prepare one narrow and one broad variant, and carry any
known space/project scope into the query.

### 2. Search both backends

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf search --cql 'siteSearch ~ "billing retry queue"' --limit 15
atl jira issue search --jql 'text ~ "billing retry queue" ORDER BY updated DESC' --limit 15
```

CQL fallbacks: `text ~ "..."`, then `title ~ "..."`. Scope with `space = KEY`
or `project = KEY` when known. Retry one prepared variant after an empty result,
then report the exact searches rather than implying full coverage. Freeze the
first useful result pages before expanding anything: require Jira
`page.complete:true` and Confluence top-level `complete:true`, or continue from
`next_cursor` / report the partial search instead of claiming absence. To
continue, repeat the exact query and page limit and pass only the returned
cursor:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf search --cql 'siteSearch ~ "billing retry queue"' --limit 15 --cursor '<next-cursor>'
atl jira issue search --jql 'text ~ "billing retry queue" ORDER BY updated DESC' --limit 15 --cursor '<next-cursor>'
```

### 3. Read bounded Confluence evidence

For the 2–4 best page hits, inspect the outline, then read only the relevant
section. A numeric id emitted by `conf search` is already a stable exact
reference; pass it directly to `outline`/`section`. Use `resolve` only for a URL,
short link, or user-supplied reference whose stable id is still unknown. Use a
full page only when no heading isolates the answer. For typed tools, pass the
section `heading` as the exact outline `title`, without Markdown `#` prefixes.

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf page outline '<search-result-id>' -o text
atl conf page section '<search-result-id>' --heading 'Retries' --max-bytes 32768 -o text
```

Require `complete:true`. A duplicate heading needs explicit `--occurrence`;
`truncated:true` means the omitted tail is not evidence of absence.

### 4. Read targeted Jira evidence

Discover non-empty field metadata before reading custom values. Request only
selected fields and qualified references; fetch history only when chronology
matters.

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl jira issue fields KEY-123 --metadata-only
atl jira issue field get KEY-123 --field 'Delivery Notes'
atl jira issue refs KEY-123 --fields 'Delivery Notes'
atl jira issue history KEY-123 --field 'Delivery Notes' --since 2026-07-01
```

Require top-level and named-source completeness before claiming a link/change
is absent. For refs, use top-level `summary` and per-issue
`reference_summary` for total/per-kind references and source-value counts; do
not manually add the nested arrays. The reconciliation booleans make count and
completeness disagreements explicit. A repeated URL is deduplicated within one
issue but counted once per issue across a multi-issue selection. Use
`jira epic digest` instead when an epic question spans children, status
narrative, comments, history, and refs.

### 5. Escalate only when bounded reads are insufficient

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf page view '<id-or-url>' -o text
atl jira issue get KEY-123
atl jira issue comment list KEY-123 -o text
```

For repeated deep reading, mirror once and search locally:

<!-- atl:read-only-shell -->
```sh
export ATL_READ_ONLY=1
atl conf pull --cql 'space = KEY AND siteSearch ~ "..."' --into <dir>
rg -il 'retry queue' <dir>
```

The mirror amortizes later reads, but a pull cap or comment warning still
limits what absence claims are justified.

### 6. Synthesize with citations

- Lead with the answer, then evidence; cite page title + id or issue key.
- Prefer the most recently updated source when sources conflict, and say so.
- Close with what was not found, queries used, and every incomplete/truncated
  source.

## Pitfalls

| Symptom | Cause / fix |
|---|---|
| exit 4 on a page read | stale id or unresolved URL — retry `page resolve`; cite only evidence actually read |
| exit 3 | PAT missing/expired — `atl auth status`, then `$setup` |
| exit 2 with CQL/JQL | wrap the query in single quotes and values in double quotes |
| body too large | prefer `outline` → bounded `section`; then full `view` or mirror `.md`, never raw `.csf` |
