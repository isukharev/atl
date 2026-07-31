# Jira work-artifact graph

- Root: `jira:issue:PROJ-1`
- Complete: `true`
- Depth: `0` (expanded `1`, followed `0`)
- Nodes: `4`; edges: `3`; evidence: `3`; sources: `8`

## Sources

| Source | Status | Complete | Count | Truncated | Stability | Reason |
| --- | --- | --- | --- | --- | --- | --- |
| issue_fields | complete | true | 6 | false | public_api |  |
| issue_links | empty | true | 0 | false | public_api |  |
| hierarchy | empty | true | 0 | false | public_api |  |
| attachments | complete | true | 1 | false | public_api |  |
| issue_properties | empty | true | 0 | false | public_api |  |
| comments | empty | true | 0 | false | public_api |  |
| worklogs | empty | true | 0 | false | public_api |  |
| remote_links | empty | true | 0 | false | public_api |  |

## Nodes

| ID | Kind | State | Depth | Expanded | Label |
| --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | jira_issue | resolved | 0 | true | Graph seed |
| jira:attachment:4 | attachment | resolved | 1 | false | design.txt |
| confluence:page:7 | confluence_page | stub | 1 | false |  |
| jira:issue:PROJ-2 | jira_issue | unresolved | 1 | false |  |

## Edges

| From | Kind | Type | Relation | To | Direction | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | attached |  |  | jira:attachment:4 | outbound | exact | 1 |
| jira:issue:PROJ-1 | mentions |  |  | confluence:page:7 | outbound | high | 1 |
| jira:issue:PROJ-1 | mentions |  |  | jira:issue:PROJ-2 | outbound | candidate | 1 |
