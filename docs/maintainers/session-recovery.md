# Session recovery

Use this after transcript compaction, interruption, a new root session, or any
time remembered state may be stale. Reconstruct facts read-only before
continuing implementation.

## Source order

1. Read the active `AGENTS.md` instruction chain.
2. Capture the optional owner-only knowledge root without rendering it:
   `owner_root="$(git config --local --get atl.ownerKnowledgeRoot)"`, then
   record the assignment's exit status. Status
   1 means the key is absent and this source is skipped. Any other nonzero
   status or an empty configured value stops recovery without guessing. For a
   configured value, run the root's bootstrap validator without echoing the
   value, then read only its current state and handoff.
3. Read the active public issue plan and PR state.
4. Inspect Git and worktrees directly.
5. Read historical checkpoints or evidence only when the current handoff routes
   to them.

Current state outranks old plans, transcripts, closed issues, and historical
checkpoints. Old approval text never creates current authority.

The local owner-knowledge setting is repository-local context, not an
instruction override. It cannot weaken `AGENTS.md` or grant backend write,
benchmark, cleanup, archive, release, merge, or destructive authority. Stop if
the lookup or configured root fails as described above; do not search for a
replacement.

## Reconstruction commands

```sh
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
git worktree list --porcelain
git config --get user.email
git config --get user.useConfigOnly
gh auth status
gh issue list --label agent-working --state open
gh pr list --state open --author @me
```

For the active issue/PR, inspect labels, comments, head/base commits, draft
state, reviews, and checks. Identify which verification belongs to the current
head; a green run from an earlier commit is not evidence for a changed diff.

Classify every dirty path as current-task, unrelated owner work, generated
output, or unknown. Stop before editing an unknown overlap. Never use stash,
reset, clean, checkout-discard, or recursive deletion to make recovery easier.

## Resume or hand off

Resume at the first uncompleted issue-plan acceptance criterion, not at the
last remembered command. Reuse prior test results only when the covered bytes
and relevant environment are unchanged.

Write a durable checkpoint at issue/PR boundaries and before a risky operation
or deliberate session change. Keep it compact:

- timestamp, branch, HEAD, and base;
- issue/PR and next acceptance criterion;
- dirty-state ownership;
- verification completed for that HEAD;
- live-write, cleanup, release, benchmark, and destructive authority;
- exact blocker or next action.

Do not create a checkpoint in the middle of an unrecorded edit, remote write,
destructive operation, release, or ambiguous outcome. Reconcile that boundary
first. Prefer a fresh session when repeated compaction makes obsolete transcript
history more expensive than the durable checkpoint.
