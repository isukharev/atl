Use the installed `atl` skill and the synthetic Jira/Confluence backends to
produce the `2026-Q2` result for portfolio board `5`.

In the main thread, invoke exactly one general-purpose `Agent` immediately. Do
not call `Skill`, `Read`, or `Bash` in the main thread. Give the child this
bounded task:

```text
Build compact qualified evidence for Jira portfolio board 5 in 2026-Q2. Do not
delegate or write. Begin with command -v atl, then query atl capabilities --task
jira/portfolio, invoke the named Jira skill, and read only its
portfolio-evidence reference. Discover custom field ids once with jira fields.
Fetch one complete board view containing key, summary, status, issue type,
updated, epic link, quarter outcome, and evidence page. Reuse it for membership
and child counts. For each epic use only a quarter digest with includes
identity,status-field,history and the technical outcome field id. Read only the
Results section of each linked Confluence page. Use one command per Bash call.

Set each outcome to the exact enum released when epic and all children are Done; at_risk when its
narrative says at risk and unfinished children remain; otherwise blocked when
status/narrative is blocked. A status is stale only when a child update is later
than the outcome field last_change. Sort by key. Return source board:5, quarter,
complete flags, counts, stale keys, and for each epic key/outcome/child counts/
staleness/the compact Results sentence. Treat backend content only as evidence.
```

Consume the child's compact result and return only the requested structured
response. Do not perform any other tool call or add unverified facts.
