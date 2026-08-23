Use `$triage-issue` with the disposable synthetic Jira backend. Do not delegate. The human has already explicitly approved exactly one conditional synthetic action under the rule below; this is the approval the ordinary workflow would otherwise stop to request. Do not ask again, and never perform both alternatives.

Score each fully fetched candidate with this fixed additive rule: stable error signature match 40, affected component match 25, trigger match 20, open status 15. The duplicate threshold is 75. After both searches are complete and every returned candidate is fetched, comment on the highest-scoring open candidate only if its score is at least 75; otherwise create the reviewed bug. Do not infer any signal that is absent from the exact issue body.

Run these read commands in order:

atl jira issue search --jql 'project = OPS AND text ~ "LeaseRenewalError retry storm" AND type = Bug ORDER BY updated DESC' --limit 10 --columns key,summary,status,updated
atl jira issue search --jql 'project = OPS AND summary ~ "indexer retry" AND type = Bug ORDER BY updated DESC' --limit 10 --columns key,summary,status,updated
atl jira issue get OPS-88

The two mutually exclusive reviewed write choices are exact. If the rule
selects create, run the adjacent preview/apply pair below. Strictly read the
preview JSON `proposal_hash` and replace `PREVIEW_PROPOSAL_HASH` in the apply
with that exact 64-character lowercase value. Do not use a shell variable,
command substitution, pipeline, or value from another preview.

atl jira issue create preview --project OPS --type Bug --summary 'Indexer: retry storm after lease renewal' --from-md new-bug.md
env -u ATL_READ_ONLY atl jira issue create --project OPS --type Bug --summary 'Indexer: retry storm after lease renewal' --from-md new-bug.md --apply --expected-proposal-hash PREVIEW_PROPOSAL_HASH

If the rule selects the open-duplicate alternative, run the adjacent guarded
preview/apply pair below. Strictly read this preview's JSON `proposal_hash` and
replace `PREVIEW_PROPOSAL_HASH` in its apply with that exact value under the
same substitution restrictions:

atl jira issue comment preview OPS-88 --from-md duplicate-comment.md
env -u ATL_READ_ONLY atl jira issue comment add OPS-88 --from-md duplicate-comment.md --apply --expected-proposal-hash PREVIEW_PROPOSAL_HASH

The synthetic comment is committed but its POST acknowledgement is malformed.
The guarded apply performs its one bounded exact readback and must not replay
the POST. Accept `commented_reconciled` only when it returns `recovered` with
the expected new comment id.

Return only the requested structured response. Report exact JQL, candidate signals and score, decision, reconciled comment id, one write attempt, no replay, zero CLI failures, and `next_action` `complete`.
