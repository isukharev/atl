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

The premerge bootstrap retains all existing automatic checks and adds
`ci-ready`, an aggregate of the complete hosted contour, including a reusable
CodeQL scan. Existing required contexts and branch protection remain in force;
the aggregate does not activate impact-based skipping or replace protection by
itself. The standalone CodeQL PR and weekly runs remain available during this
transition.

For a manual full run, update the same-repository PR branch to contain current
`main`, finish review, and capture the exact PR head and base revisions. Dispatch
the PR branch, supplying those immutable revisions as inputs:

```sh
gh workflow run ci.yml --ref <pr-branch> \
  -f pr=<number> -f head_sha=<reviewed-head-sha> -f base_sha=<current-main-sha>
```

The workflow checks the dispatched commit, PR branch, open PR, and exact
head/base against current GitHub metadata before and after the gates. Manual
runs require the base to be an ancestor of the checked head. Automatic PR runs
instead validate GitHub's synthetic merge commit and its exact base/head
parents; fork PRs retain this automatic route. Selecting `main` and merely
checking out a different commit cannot produce valid manual PR evidence.

`ci-ready` succeeds only when every premerge dependency succeeds; a skipped,
cancelled, failed, or missing dependency fails the aggregate. The final binding
check rejects head or base movement during the run. If either revision changes,
update and review the branch as applicable, then dispatch a fresh run. A rerun
of the old event does not update its bound revisions. Before merge, reconcile
the run's event, `headSha`, and current PR head/base; retain strict up-to-date
branch protection when migrating required contexts to the aggregate.

The initial binding job deliberately does not gate the legacy jobs: a binding
failure must not turn existing required checks into skipped jobs while the
required-check migration is still pending. After the bootstrap is merged and
the manual aggregate has been observed on a PR head, the separately authorized
protection migration can add `ci-ready`, verify it, and retire old contexts.
Do not remove old contexts first or disable required checks between updates.

Mark the PR ready only after local gates and review are green. Inspect hosted
checks rather than assuming that a queued workflow passed. Never keep a watch
alive with model-driven waits. Take a bounded required-check snapshot at a
natural dependency boundary:

```sh
gh pr checks <number> --required --json name,bucket \
  --jq '[.[] | "\(.bucket) \(.name)"] | join("\n")'
gh pr view <number> --json mergeable,isDraft,state,statusCheckRollup
```

For one known workflow run, inspect only its terminal fields:

```sh
gh run view <run-id> --json status,conclusion \
  --jq '.status + " " + (.conclusion // "-")'
```

While checks are pending, prepare an independent non-conflicting task. Take at
most three model-visible snapshots for the hosted workflow. If useful work is
exhausted, stay on the task with one bounded blocking watch whose intermediate
ticks remain inside the tool invocation:

```sh
umask 077
mkdir -p tmp/runs
timeout 2700 gh pr checks <number> --required --watch --interval 120 --fail-fast \
  >tmp/runs/pr-<number>-checks.log 2>&1
```

After it returns, take one final projected snapshot within the same
three-snapshot budget and inspect mergeability. Follow
[Efficient agent work](agent-efficiency.md) for the common liveness, background,
output, and session-state contract. A timeout or lost waiter is a real pending
boundary; ordinary queued checks are not.

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
