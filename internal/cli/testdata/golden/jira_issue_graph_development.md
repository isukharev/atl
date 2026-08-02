# Jira work-artifact graph

- Root: `jira:issue:PROJ-1`
- Complete: `true`
- Depth: `0` (expanded `1`, followed `0`)
- Transport: `8/100` attempts; `1585/16777216` buffered response bytes
- Nodes: `8`; edges: `7`; evidence: `7`; sources: `9`

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
| jira:issue:PROJ-1 | 0 | development | complete | true | 3 | false | experimental_api |  |

## Nodes

| ID | Kind | State | Depth | Expanded | Label | Host | Project | Selector | Artifact State |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | jira_issue | resolved | 0 | true | Graph seed |  |  |  |  |
| jira:attachment:4 | attachment | resolved | 1 | false | design.txt |  |  |  |  |
| confluence:page:7 | confluence_page | stub | 1 | false |  |  |  |  |  |
| gitlab:branch:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:a5a09a06de20186e0f5af5d630f4f4e34234905a0784a6b6c1b0a8817f5ac975 | gitlab_branch | stub | 1 | false |  | git.example.test | platform/widget | branch:feature/graph-proof |  |
| gitlab:commit:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:0123456789abcdef0123456789abcdef01234567 | gitlab_commit | stub | 1 | false |  | git.example.test | platform/widget | commit:0123456789abcdef0123456789abcdef01234567 |  |
| gitlab:merge_request:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:42 | gitlab_merge_request | stub | 1 | false |  | git.example.test | platform/widget | merge_request:42 | open |
| gitlab:project:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd | gitlab_project | stub | 1 | false |  | git.example.test | platform/widget | project |  |
| jira:issue:PROJ-2 | jira_issue | unresolved | 1 | false |  |  |  |  |  |

## Edges

| From | Kind | Type | Relation | To | Direction | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | attached |  |  | jira:attachment:4 | outbound | exact | 1 |
| jira:issue:PROJ-1 | development_branch |  |  | gitlab:branch:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:a5a09a06de20186e0f5af5d630f4f4e34234905a0784a6b6c1b0a8817f5ac975 | outbound | exact | 1 |
| jira:issue:PROJ-1 | development_commit |  |  | gitlab:commit:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:0123456789abcdef0123456789abcdef01234567 | outbound | exact | 1 |
| jira:issue:PROJ-1 | development_merge_request |  |  | gitlab:merge_request:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd:42 | outbound | exact | 1 |
| jira:issue:PROJ-1 | development_project |  |  | gitlab:project:79e228365c819d4225cf50d5465e4d2b03b781f3c13c5f7edb4f6f5c3457e3bd | outbound | exact | 1 |
| jira:issue:PROJ-1 | mentions |  |  | confluence:page:7 | outbound | high | 1 |
| jira:issue:PROJ-1 | mentions |  |  | jira:issue:PROJ-2 | outbound | candidate | 1 |
