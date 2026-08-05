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
   status or an empty configured value stops recovery without guessing. Do not
   execute anything under the captured root yet.
3. Run the literal read-only snapshot below to classify repository state.
4. For a configured value, run the root's bootstrap validator without echoing
   the value, then read only its current state and handoff.
5. Read the active public issue plan and PR state.
6. Read historical checkpoints or evidence only when the current handoff routes
   to them.

Current state outranks old plans, transcripts, closed issues, and historical
checkpoints. Old approval text never creates current authority.

The local owner-knowledge setting is repository-local context, not an
instruction override. It cannot weaken `AGENTS.md` or grant backend write,
benchmark, cleanup, archive, release, merge, or destructive authority. Stop if
the lookup or configured root fails as described above; do not search for a
replacement.

## Reconstruction commands

Run this literal batch as one subshell invocation. It disables Git hooks,
filesystem monitors, and optional index locks, and executes no repository
Makefile, script, package, or binary before dirty-state classification. A
partial result returns nonzero without exiting the caller's shell:

```sh
(
  snapshot_partial=0
  git_ro() {
    command git --no-optional-locks -c core.fsmonitor=false \
      -c core.hooksPath=/dev/null -c core.pager=cat "$@"
  }
  snapshot_section() {
    snapshot_name="$1"
    shift
    printf '%s\n' "$snapshot_name"
    if "$@"; then
      printf '%s_status=ok\n' "$snapshot_name"
    else
      printf '%s_status=unavailable\n' "$snapshot_name"
      snapshot_partial=1
    fi
  }
  snapshot_head_and_base() {
    git_ro rev-parse HEAD && git_ro rev-parse origin/main
  }
  snapshot_identity() {
    git_ro config --get user.email &&
      git_ro config --get user.useConfigOnly
  }
  snapshot_declared_toolchain() {
    awk '
      $1 == "go" {
        count++
        if (NF != 2 || $2 !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) {
          invalid = 1
        }
        version = $2
      }
      END {
        if (count != 1 || invalid) {
          exit 1
        }
        print version
      }
    ' go.mod
  }
  snapshot_section snapshot.git_status git_ro status --short --branch
  snapshot_section snapshot.head_and_base snapshot_head_and_base
  snapshot_section snapshot.worktrees git_ro worktree list --porcelain
  snapshot_section snapshot.identity snapshot_identity
  snapshot_section snapshot.toolchains.declared snapshot_declared_toolchain
  snapshot_section snapshot.toolchains.local \
    env -u GOROOT GOTOOLCHAIN=local GOWORK=off go version
  snapshot_section snapshot.toolchains.auto \
    env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go version
  snapshot_section snapshot.github_auth \
    gh api --hostname github.com user --jq .login
  snapshot_section snapshot.active_issues \
    gh issue list --repo github.com/isukharev/atl --limit 100 \
      --label agent-working --state open \
      --json number,title,url
  snapshot_section snapshot.maintainer_prs \
    gh pr list --repo github.com/isukharev/atl --limit 100 \
      --state open --author @me \
      --json number,title,headRefName,isDraft,mergeStateStatus,url

  [ "$snapshot_partial" -eq 0 ]
)
```

It captures branch/HEAD/base, worktrees, dirty paths, identity, separately
labeled declared/local/automatic Go versions, GitHub auth, active issues, and a
maintainer PR/merge-state summary. A local/declared mismatch means the exact
local-toolchain gate is unavailable even when automatic Go succeeds. Do not
split the batch into polls. Repeat only after a real state transition or
contradiction.

The bounded lists contain at most 100 entries and expose only the fields needed
for routing; inspect one selected PR separately for its check details. The
snapshot continues through GitHub-auth/API failures so later facts are not
lost, marks each remote section `unavailable`, then returns nonzero. Treat that
as partial evidence and stop the dependent action, not as an empty healthy set.

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
