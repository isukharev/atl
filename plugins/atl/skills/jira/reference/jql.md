<!-- Generated from skills-src/jira/reference/jql.md — edit the source and run 'make gen-plugins'. -->
# Searching and pulling Jira issues (JQL)

## Search vs pull

- `atl jira issue search --jql '<JQL>'` returns a lightweight list to stdout — use it to find the
  right issues without writing anything to disk. Default `--limit 50`.
- `atl jira pull --jql '<JQL>' --into ~/.atl/<workspace>/` writes one `<KEY>.md` + `<KEY>.json` per
  issue — use it once you know which issues you'll work with. Default `--limit 100`; **`--limit 0`
  means all** (unbounded — unlike Confluence's `conf pull --cql`, which caps at 1000 and reports
  `"truncated": true` plus a stderr warning when the cap is hit).

## Common JQL patterns

```text
project = PROJ AND statusCategory != Done ORDER BY updated DESC
project = PROJ AND assignee = currentUser() AND status = "In Progress"
key in (PROJ-1, PROJ-2, PROJ-3)
project = PROJ AND issuetype = Epic
"Epic Link" = PROJ-100                 # issues under an epic
project = PROJ AND labels = backend AND priority in (High, Highest)
project = PROJ AND updated >= -7d
```

Quote multi-word values with double quotes inside the single-quoted shell argument.

## Slimming and paginating

- `--fields summary,status,assignee` limits the columns fetched (cheaper, less noise).
- Pagination: the response includes `next_cursor` (a `startAt` offset). Pass it back via
  `--cursor <next_cursor>` to get the next page.

## Picking a query

Prefer a narrow JQL that returns just the issues you need, then `pull` those. Don't pull a whole
project to grep it locally when a `statusCategory`/`assignee`/`updated` filter would return the
handful you actually care about — search-first keeps context small and the mirror tidy.
