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

If the rule selects the open-duplicate alternative, list the complete baseline, run only this write once, then reconcile one ambiguous response with exactly one complete list:

atl jira issue comment list OPS-88
env -u ATL_READ_ONLY atl jira issue comment add OPS-88 --from-md duplicate-comment.md
atl jira issue comment list OPS-88

The synthetic comment is committed but its POST returns HTTP 500. Do not retry. Accept `commented_reconciled` only when the second complete list has exactly one new id and its body is byte-for-byte the converted reviewed comment.

Return only the requested structured response. Report exact JQL, candidate signals and score, decision, reconciled comment id, one write attempt, no replay, one CLI failure, and `next_action` `complete`.
