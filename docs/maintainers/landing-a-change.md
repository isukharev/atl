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

Premerge checks run on GitHub-hosted runners only for a `ready_for_review`
event targeting `main`. Opening, reopening, synchronizing, or labeling a PR
does not launch the suite; main pushes retain only a bounded build smoke.
`ci-ready` is the single required aggregate, with strict up-to-date branch
protection. Weekly CodeQL and full release gates remain automatic.

Update the same-repository PR branch to contain current `main`, finish focused
local verification and review, and capture the exact PR head/base revisions. If
the reviewed PR is already ready, first run `gh pr ready <number> --undo` and
wait for it to finish. Re-read the PR and confirm that it is draft and that its
head/base revisions are still the reviewed values before running:

```sh
gh pr ready <number>
```

Do not fire-and-forget or overlap the draft and ready transitions. A direct-ready
PR creation produces no qualifying event; correct or review it as needed, then
complete the draft-to-ready sequence. A merge conflict cannot produce admitted
evidence; resolve and review it before another transition. The credential making
the transition must be the coordinator's user, PAT, or GitHub App credential,
not a workflow `GITHUB_TOKEN` trampoline whose events can be suppressed.

To widen the selected plan to every hosted gate, apply the exact `ci-full` label
before requesting ready. The event captures that label snapshot; changing labels
later neither changes nor starts a run. Callers cannot omit required lanes. The
maintained impact owner reads the committed base/head policy union, and the
binding log records the closed plan. See
[Development](development.md#select-verification-once-the-diff-is-stable) for
the module-level contour and conservative fallback rules.

The workflow requires an open, non-draft, same-repository PR and checks the
event's exact head/base branches, synthetic merge ref, and merge checkout. The
checkout must have the event base/head as ordered parents, the base must be an
ancestor of the head, and the merge tree must equal the reviewed head tree. The
same facts are read from current GitHub metadata before and after the gates.
Fork revisions must first move to a reviewed same-repository branch and PR.

`ci-ready` recomputes the committed plan, requires successful binding/contracts
and every selected job, and accepts skips only for jobs explicitly excluded by
that exact plan. Failed, cancelled, missing, or unexpectedly skipped work fails
the aggregate. If the head or base changes, update and review the branch, finish
the draft transition, re-confirm current refs, and request ready again. For a
same-head retry, use the same completed draft-to-ready sequence; rerunning an old
event does not update its revision or label snapshot. Because synchronization
does not trigger the workflow, stale runs are not cancelled automatically; the
coordinator explicitly cancels a superseded run when needed. Final current-ref
and non-draft checks reject stale evidence. Before merge, reconcile the run's
event, `headSha`, plan, and current PR head/base; strict protection guards base
movement after the run completes.

For the required-check migration, first observe the native `ci-ready` aggregate
on a PR head under existing protection. Add and verify `ci-ready` before retiring
legacy contexts, then activate ready-event-only selection. Never disable required
checks between updates. Configuration changes remain separate authorized
actions; a local workflow patch does not change protection.

Mark the PR ready only after focused local verification and review. Inspect hosted
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
