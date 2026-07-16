---
name: search-knowledge
description: Answer questions from company knowledge by searching Confluence and Jira together with the atl CLI and synthesizing a cited answer. USE WHEN the user asks "what do we know about X", "find docs/tickets about X", "explain our <process/system/term>", "has this been discussed", or any question whose answer may live in Confluence pages or Jira issues rather than in the codebase.
---
<!-- Generated from skills-src/search-knowledge/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Search company knowledge with `atl`

Search Confluence and Jira in parallel, read the best hits, and answer with
citations. This recipe is read-only: never create, update, or comment.

Make `export ATL_READ_ONLY=1` the first statement of every Bash block so all
later atl calls and child processes inherit the guard. Never override it in
this workflow.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `$setup` and stop.

## Workflow

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
`next_cursor` / report the partial search instead of claiming absence.

### 3. Read bounded Confluence evidence

For the 2–4 best page hits, inspect the outline, then read only the relevant
section. A numeric id emitted by `conf search` is already a stable exact
reference; pass it directly to `outline`/`section`. Use `resolve` only for a URL,
short link, or user-supplied reference whose stable id is still unknown. Use a
full page only when no heading isolates the answer.

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
is absent. Use `jira epic digest` instead when an epic question spans children,
status narrative, comments, history, and refs.

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
