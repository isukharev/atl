# Landing a change

This runbook carries one stable change from issue to synchronized `main`.
[GitHub issue workflow](../github-issue-workflow.md) owns the public hierarchy,
labels, and templates; this page owns the execution sequence.

## Before code

For non-trivial work:

1. Find or create a generic public issue and link its parent when applicable.
2. Add area, kind, horizon, and agent-state labels.
3. Post an `## Agent plan` covering problem, approach, files, acceptance,
   verification, risks, and non-goals.
4. Create the linked branch with `gh issue develop <number> --checkout`.
5. Confirm the branch, remote base, identity, and dirty state again.

If implementation evidence changes scope, acceptance criteria, or ordering,
post a revised issue plan before continuing. Do not let the public plan drift
silently from the code under review.

Do not publish private target values, internal planning paths, credentials,
proprietary content, or raw live evidence. A public plan describes generic
fixtures and backend classes.

## Prepare the PR

Keep commits coherent and use `<type>: <summary>`. Open a small draft PR early
enough for traceability. Its body includes:

- `Fixes` or `Refs` for the issue, plus parent and roadmap route;
- concise implementation summary;
- exact verification actually run;
- privacy review of the complete diff;
- docs, generated-tree, migration, and live-validation notes when relevant.

Run the independent read-only review required by the issue's process class. The
reviewer reports findings with file/line evidence and does not edit. A material
fix changes compiled/tested bytes, an executable contract, a security/authority
boundary, or the design. Name its changed paths and rerun only their impact-map
gates; prose and naming fixes do not create another full wave. Request a bounded
follow-up only when the finding or fix warrants it.

## CI and merge

Mark the PR ready only after local gates and review are green. Inspect hosted
checks rather than assuming that a queued workflow passed. Never use streaming
watch/follow commands or keep a process session alive with model-driven waits.
Take a bounded snapshot at a natural dependency boundary:

```sh
gh pr checks <number> --json name,bucket \
  --jq '[.[] | "\(.bucket) \(.name)"] | join("\n")'
gh pr view <number> --json mergeable,isDraft,state,statusCheckRollup
```

For one known workflow run, inspect only its terminal fields:

```sh
gh run view <run-id> --json status,conclusion \
  --jq '.status + " " + (.conclusion // "-")'
```

While checks are pending, prepare an independent non-conflicting task. Take at
most three snapshots for the hosted workflow. If useful work is exhausted,
record the pending state and expected duration, then end the turn instead of
polling. Follow [Efficient agent work](agent-efficiency.md) for the common
background, output, and session-state contract.

Never merge a PR authored by anyone other than `isukharev` without explicit
authorization for that exact PR. When the author and authority are valid, all
required checks are green, conversations are resolved, and the final head is
the reviewed head, merge using the repository's linear-history policy.

After merge:

1. Synchronize local `main` with `origin/main` without disturbing unrelated
   changes.
2. Confirm the merge commit author identity and repository dirty state.
3. Remove `agent-working` from the closed issue.
4. Delete only obsolete branches/worktrees whose useful state is already
   integrated or preserved inside the durable workspace.
5. Update the current durable handoff/checkpoint at this safe boundary. Record
   HEAD, issue/PR, dirty state, verification, remaining work, and authority;
   never copy private payloads into a public artifact.

Do not repeat the entire PR matrix locally or on `main` merely because the PR
merged. The push workflow supplies provenance/smoke coverage; investigate only
an actual post-merge failure.
