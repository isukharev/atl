# Jira boards and sprints

Board and sprint discovery, views, issue sets, and guarded membership changes.

[Reference index](README.md) · [Documentation home](../../README.md)

## `atl jira board {list,get,config,issues,backlog,view,export}` and `atl jira sprint {list,get,current,issues,add,remove}`

Agile boards & sprints, via the Data Center Agile API (`/rest/agile/1.0/`).
**Requires Jira Software** (GreenHopper); on a Core/Service-Management-only
instance the Agile endpoints 404 (exit 4). Boards and sprints are addressed by
**numeric id** — use `board list --project` to discover the id `--board` wants.

```bash
atl jira board list --project PROJ          # {boards:[{id,name,type,project_key}]}; -o id → board ids
atl jira board get 5
atl jira board config 5                     # filter, ordered columns/status ids, limits, estimation, rank field
atl jira board issues 5 --columns position,key,summary,status,assignee # one ranked page; -o id → keys
atl jira board issues 5 --view full                  # reusable configured projection
atl jira board backlog 5 --columns position,key,summary,status          # Scrum only; explicit pagination
atl jira board view 5 -o text               # normalized config + status-to-column mapping
atl jira board view 5 --jql 'statusCategory != Done' --limit 500
atl jira board view 5 --columns key,status,updated,customfield_10001 \
  --epic-field customfield_10001 --done-status Done
atl jira board export 5 --format jsonl --out board.jsonl
atl jira sprint list --board 5 [--state active|closed|future]   # {sprints:[...]}; -o id → sprint ids
atl jira sprint get 7                       # one sprint by numeric id; -o text/id supported
atl jira sprint current --board 5           # the active sprint (exit 4 if none)
atl jira sprint issues 7 --columns position,key,summary,status  # issues in sprint 7; -o id → keys
atl jira sprint add 7 PROJ-1 PROJ-2         # move issues into sprint 7
atl jira sprint remove PROJ-1               # move issue(s) back to the backlog
```

`--board` and positional board/sprint ids must be positive (else exit 2).
JQL search, `board issues`/`board backlog`, and `sprint issues` return one
versioned issue-list shape: `source`, `selection`, ordered
`projection.columns/fields`, `rows[]` with identity plus `values` and namespaced
source `context`, and `page`. Their `-o text` form is a Markdown table; `-o id`
remains one key per line. Board pages expose one API page with `page.complete`
and `page.next_cursor`; page size is capped at 50. `board view` follows all pages by
default (`--limit 0`) and preserves backend rank order. A positive view/export
limit applies per requested scope and sets `complete:false`, `truncated:true`
when more rows exist.

The normalized view maps each issue's `status_id` to the first configured board
column and preserves unknown statuses as `column:"Unmapped"` with
`column_mapped:false`. `--scope all` reads board plus backlog membership on
Scrum. Jira Software's backlog issue endpoint is not available for Kanban, so a
Kanban `all` view reads board scope only, records `backlog_fetched:false`, and
never calls a sprint or backlog endpoint. Interpret its ordered configured
columns rather than pretending a separate backlog membership was observed.

For a deterministic epic aggregate, select the exact epic relation field and
`updated`, then pass that field to `--epic-field` and one or more repeatable
`--done-status` values. The optional `epic_rollup` groups the already-fetched
rows without another Jira request. It reports child/status/done counts, latest
child update, parent presence, timestamp coverage, and its own `complete`
signal. A missing parent row, missing child timestamp, or incomplete snapshot
makes the rollup incomplete. Relation and timestamp type errors fail closed.
The flags apply only to `board view`; `board export` formats are unchanged.

Use JSON for one complete object, JSONL for streaming `jq`, CSV for relational
tools/spreadsheets, and Markdown for review. CSV formula-leading cells are
neutralized by default; `--raw-csv` is the explicit unsafe opt-out. Board reads
never call rank/move/update endpoints. Sprint `add`/`remove` remain separate
mutating commands and require explicit user intent.

Use `--columns` as the single list projection control. It derives backend Jira
fields and preserves the requested order in Markdown. Namespaced source columns
include `board.column`, `board.in_backlog`, and `sprint.id`; unavailable context
columns fail with usage rather than silently rendering empty.
For repeated work, use `--view default|full|<custom>` instead. Precedence is
explicit `--columns` → named view → built-in `default`; output records the
resolved name as `projection.view`. Unknown views and source-invalid columns
fail before a backend request.
