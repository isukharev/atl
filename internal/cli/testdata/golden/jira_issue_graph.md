# Jira work-artifact graph

- Root: `jira:issue:PROJ-1`
- Complete: `true`
- Depth: `0` (expanded `1`, followed `0`)
- Transport: `4/100` attempts; `575/16777216` buffered response bytes
- Nodes: `4`; edges: `3`; evidence: `3`; sources: `8`

## Sources

| Node | Depth | Source | Status | Complete | Count | Truncated | Stability | Reason |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | 0 | issue_fields | complete | true | 6 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | issue_links | empty | true | 0 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | hierarchy | empty | true | 0 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | attachments | complete | true | 1 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | issue_properties | empty | true | 0 | false | experimental_api |  |
| jira:issue:PROJ-1 | 0 | comments | empty | true | 0 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | worklogs | empty | true | 0 | false | public_api |  |
| jira:issue:PROJ-1 | 0 | remote_links | empty | true | 0 | false | public_api |  |

## Nodes

| ID | Kind | State | Depth | Expanded | Label | URL |
| --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | jira_issue | resolved | 0 | true | Graph seed |  |
| jira:attachment:4 | attachment | resolved | 1 | false | design.txt |  |
| confluence:page:7 | confluence_page | stub | 1 | false |  |  |
| jira:issue:PROJ-2 | jira_issue | unresolved | 1 | false |  |  |

## Edges

| From | Kind | Type | Relation | To | Direction | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | attached |  |  | jira:attachment:4 | outbound | exact | 1 |
| jira:issue:PROJ-1 | mentions |  |  | confluence:page:7 | outbound | high | 1 |
| jira:issue:PROJ-1 | mentions |  |  | jira:issue:PROJ-2 | outbound | candidate | 1 |
