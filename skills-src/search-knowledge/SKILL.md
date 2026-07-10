---
name: search-knowledge
description: Answer questions from company knowledge by searching Confluence and Jira together with the atl CLI and synthesizing a cited answer. USE WHEN the user asks "what do we know about X", "find docs/tickets about X", "explain our <process/system/term>", "has this been discussed", or any question whose answer may live in Confluence pages or Jira issues rather than in the codebase.
---

# Search company knowledge with `atl`

Search Confluence and Jira **in parallel**, read the best hits, and answer with
citations. This recipe is read-only: never create, update, or comment on
anything from it. Command details live in the `confluence` and `jira` skills.

**Preflight:** `atl` must be installed and configured. If `command -v atl` fails
or a command exits `7` ("not configured"), run `{{atl.setup_cmd}}` and stop.

## Workflow

### 1. Extract search terms

Take the core nouns of the question, drop filler: "what do we know about the
billing retry queue" → `billing retry queue`. Prepare one narrower and one
broader variant up front so a miss doesn't stall the flow.

### 2. Search both backends in parallel

Run in the same message (independent commands):

```sh
atl conf search --cql 'siteSearch ~ "billing retry queue"' --limit 15
atl jira issue search --jql 'text ~ "billing retry queue" ORDER BY updated DESC' --limit 15
```

- CQL fallbacks when `siteSearch` misses: `text ~ "..."`, `title ~ "..."`.
- Scope when the user names a space/project:
  `space = KEY AND siteSearch ~ "..."` / `project = KEY AND text ~ "..."`.
- Both commands print JSON. Empty results → retry once with a prepared
  variant, then report honestly what was searched and found nothing.

### 3. Read the top sources

Fetch the 2–4 most relevant hits — weigh title match and recency over raw
rank:

```sh
atl conf page view <id> -o text                # configured Markdown, no mirror artifacts
atl jira issue get KEY-123                     # full issue incl. description
atl jira issue comment list KEY-123            # when the discussion matters
```

For deep reading across several pages, mirror once and read locally instead of
fetching page by page:

```sh
atl conf pull --cql 'space = KEY AND siteSearch ~ "..."' --into <dir>
grep -ril 'retry queue' <dir>                  # then read the matching .md files
```

The mirror is `atl`'s edge over live-API search: after one pull you can grep
the whole result set offline, with no rate limits and no per-request truncation.

### 4. Synthesize with citations

- Lead with the answer, then the evidence. Cite every claim with its source:
  page title + id, or issue key.
- When sources conflict, say so and prefer the most recently updated.
- Close with what was **not** found and the queries used, so the user can
  refine instead of assuming full coverage.

## Pitfalls

| Symptom | Cause / fix |
|---|---|
| exit 4 on `page get` | stale id from the search index — cite the search hit only |
| exit 3 | PAT missing/expired — `atl auth status`, then `{{atl.setup_cmd}}` |
| exit 2 with a CQL/JQL parse error | wrap the whole query in single quotes, values in double quotes |
| page body too large to quote | use `--format view` excerpts or the mirror's `.md`; never dump raw `.csf` at the user |
