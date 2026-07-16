# Searching and pulling Jira issues (JQL)

## Search vs pull

- `atl jira issue search --jql '<JQL>'` returns a lightweight list to stdout — use it to find the
  right issues without writing anything to disk. Default `--limit 50`.
- `atl jira issue children <EPIC-KEY>` is the narrower path for one epic: it
  resolves the Epic Link field and returns a paginated IssueList without
  per-child reads or project-wide JQL.
- `atl jira pull --jql '<JQL>' --into ~/.atl/<workspace>/` writes one `<KEY>.wiki`,
  `<KEY>.md`, and `<KEY>.json` per issue — use it once you know which issues you'll work with.
  The `.wiki` is the native substrate; `.md` is a derived staging view whose supported body edits
  require `jira apply`. Default `--limit 100`; **`--limit 0`
  means all**. Ordinary Confluence `conf pull --cql` instead caps at 1000 and
  reports `"truncated": true` plus a stderr warning; its explicit
  `--complete` mode is the resumable historical-selector path.

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

- `--columns key,summary,status,assignee` controls both ordered output and the
  fields fetched (cheaper, less noise).
- Pagination lives under `page.next_cursor` (a `startAt` offset). Pass it back
  via `--cursor <page.next_cursor>` to get the next page.

## Picking a query

Prefer a narrow JQL that returns just the issues you need, then `pull` those. Don't pull a whole
project to grep it locally when a `statusCategory`/`assignee`/`updated` filter would return the
handful you actually care about — search-first keeps context small and the mirror tidy.
