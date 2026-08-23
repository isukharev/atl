Invoke the `atl:triage-issue` Skill and use the disposable synthetic Jira backend. Do not delegate. The human has already explicitly approved exactly one conditional synthetic action under the rule below; this is the approval the ordinary workflow would otherwise stop to request. Do not ask again, and never perform both alternatives.

Score each fully fetched candidate with this fixed additive rule: stable error signature match 40, affected component match 25, trigger match 20, open status 15. The duplicate threshold is 75. After both searches are complete and every returned candidate is fetched, comment on the highest-scoring open candidate only if its score is at least 75; otherwise create the reviewed bug. Do not infer any signal that is absent from the exact issue body.

Run these read commands in order:

atl jira issue search --jql 'project = LAB AND text ~ "CacheRefreshError refresh token" AND type = Bug ORDER BY updated DESC' --limit 10 --columns key,summary,status,updated
atl jira issue search --jql 'project = LAB AND summary ~ "cache refresh" AND type = Bug ORDER BY updated DESC' --limit 10 --columns key,summary,status,updated
atl jira issue get LAB-41 --fields summary,description,status,issuetype,project
atl jira issue get LAB-52 --fields summary,description,status,issuetype,project

The two mutually exclusive reviewed write choices are exact. If the rule
selects create, run the adjacent preview/apply pair below. Strictly read the
preview JSON `proposal_hash` and replace `PREVIEW_PROPOSAL_HASH` in the apply
with that exact 64-character lowercase value. Do not use a shell variable,
command substitution, pipeline, or value from another preview.

atl jira issue create preview --project LAB --type Bug --summary 'Cache: refresh fails after token rotation' --from-md new-bug.md
env -u ATL_READ_ONLY atl jira issue create --project LAB --type Bug --summary 'Cache: refresh fails after token rotation' --from-md new-bug.md --apply --expected-proposal-hash PREVIEW_PROPOSAL_HASH

If the rule selects the open-duplicate alternative, run the adjacent guarded
preview/apply pair below. Strictly read this preview's JSON `proposal_hash` and
replace `PREVIEW_PROPOSAL_HASH` in its apply with that exact value under the
same substitution restrictions:

atl jira issue comment preview LAB-52 --from-md duplicate-comment.md
env -u ATL_READ_ONLY atl jira issue comment add LAB-52 --from-md duplicate-comment.md --apply --expected-proposal-hash PREVIEW_PROPOSAL_HASH

Return only the requested structured response. Search completeness comes from each search page contract. Report exact JQL, candidate signals and scores, decision, returned key or reconciled comment id, one write attempt, no replay, CLI failure count, and `next_action` `complete`.
