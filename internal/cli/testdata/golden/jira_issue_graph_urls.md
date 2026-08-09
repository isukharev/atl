# Jira work-artifact graph

- Root: `jira:issue:PROJ-1`
- Complete: `true`
- Depth: `0` (expanded `1`, followed `0`)
- Transport: `4/100` attempts; `701/16777216` buffered response bytes
- Nodes: `5`; edges: `4`; evidence: `4`; sources: `8`

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
| candidate:url:cdb85488220446017b1a84a160cf6d228a992dcbb44c20b993d42d4285a78c6d | url | unresolved | 1 | false |  |  |
| url:b3319ec922a86bc2ede2f349875b3703c0277a2a856af25736c154b89faa4fae | url | stub | 1 | false |  | https://external.example.test/docs?redacted=redacted |
| url:eefb63505be99936c49270d9ff5ae2b0a066854dc6383f576800117e5ca4e3e1 | url | stub | 1 | false |  | https://external.example.test/guide/a&amp;b |

## Edges

| From | Kind | Type | Relation | To | Direction | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| jira:issue:PROJ-1 | attached |  |  | jira:attachment:4 | outbound | exact | 1 |
| jira:issue:PROJ-1 | mentions |  |  | candidate:url:cdb85488220446017b1a84a160cf6d228a992dcbb44c20b993d42d4285a78c6d | outbound | candidate | 1 |
| jira:issue:PROJ-1 | mentions |  |  | url:b3319ec922a86bc2ede2f349875b3703c0277a2a856af25736c154b89faa4fae | outbound | high | 1 |
| jira:issue:PROJ-1 | mentions |  |  | url:eefb63505be99936c49270d9ff5ae2b0a066854dc6383f576800117e5ca4e3e1 | outbound | high | 1 |
