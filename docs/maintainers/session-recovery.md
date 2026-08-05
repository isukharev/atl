# Session recovery

Use this after transcript compaction, interruption, a new root session, or any
time remembered state may be stale. Reconstruct facts read-only before
continuing implementation.

## Source order

1. Use the active `AGENTS.md` instruction chain. Codex supplies this before the
   task; do not reread the whole file unless it is missing or demonstrably
   stale.
2. Capture the optional owner-only knowledge root and bootstrap digest without
   rendering them, and record both lookup statuses. Only status 1 for both keys
   means this source is unconfigured and skipped. Any partial configuration,
   other nonzero status, or empty configured value stops recovery without
   guessing. Do not read anything under the captured root yet. The keys are
   `atl.ownerKnowledgeRoot` and `atl.ownerKnowledgeBootstrapSHA256`; capture
   each exactly once before the snapshot.
3. Run the literal read-only snapshot below to classify repository state.
4. For configured values, verify the data-only bootstrap protocol below, then
   read only the two current routes it emits.
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

Run this literal transaction as one subshell invocation. It captures the two
owner settings exactly once, disables Git hooks, filesystem monitors, and
optional index locks, and executes no repository Makefile, script, package, or
binary before dirty-state classification. Only after a complete snapshot does
it validate the data-only owner bootstrap and emit its two relative routes. A
partial result returns nonzero without exiting the caller's shell:

```sh
(
  owner_root="$(git config --local --get atl.ownerKnowledgeRoot)"
  owner_root_status=$?
  owner_bootstrap_sha="$(
    git config --local --get atl.ownerKnowledgeBootstrapSHA256
  )"
  owner_bootstrap_status=$?

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

  [ "$snapshot_partial" -eq 0 ] || exit 1

  if [ "$owner_root_status" -eq 1 ] &&
      [ "$owner_bootstrap_status" -eq 1 ]; then
    exit 0
  fi
  [ "$owner_root_status" -eq 0 ] &&
    [ "$owner_bootstrap_status" -eq 0 ] &&
    [ -n "$owner_root" ] ||
    exit 1
  case "$owner_root" in
    /*) ;;
    *) exit 1 ;;
  esac
  case "$owner_bootstrap_sha" in
    *[!0-9a-f]*|'') exit 1 ;;
  esac
  [ "${#owner_bootstrap_sha}" -eq 64 ] || exit 1

  owner_root_real="$(CDPATH= cd -- "$owner_root" && pwd -P)" || exit 1
  [ "$owner_root_real" = "$owner_root" ] && [ ! -L "$owner_root" ] || exit 1
  file_mode() {
    stat -c '%a' -- "$1" 2>/dev/null || stat -f '%Lp' "$1"
  }
  file_uid() {
    stat -c '%u' -- "$1" 2>/dev/null || stat -f '%u' "$1"
  }
  [ "$(file_mode "$owner_root")" = 700 ] &&
    [ "$(file_uid "$owner_root")" = "$(id -u)" ] ||
    exit 1

  owner_bootstrap="$owner_root/bootstrap.v1"
  [ -f "$owner_bootstrap" ] &&
    [ ! -L "$owner_bootstrap" ] &&
    [ "$(file_mode "$owner_bootstrap")" = 600 ] &&
    [ "$(file_uid "$owner_bootstrap")" = "$(id -u)" ] ||
    exit 1
  hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -- "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 -- "$1" | awk '{print $1}'
    else
      return 1
    fi
  }
  actual_sha="$(hash_file "$owner_bootstrap")" || exit 1
  [ "$actual_sha" = "$owner_bootstrap_sha" ] || exit 1

  [ "$(wc -l < "$owner_bootstrap" | tr -d ' ')" = 3 ] || exit 1
  [ "$(sed -n '1p' "$owner_bootstrap")" = schema_version=1 ] || exit 1
  state_line="$(sed -n '2p' "$owner_bootstrap")"
  handoff_line="$(sed -n '3p' "$owner_bootstrap")"
  state_rel="${state_line#current_state=}"
  handoff_rel="${handoff_line#current_handoff=}"
  [ "$state_line" != "$state_rel" ] &&
    [ "$handoff_line" != "$handoff_rel" ] ||
    exit 1
  valid_relative() {
    case "$1" in
      ''|/*|.|..|../*|*/../*|*/..|*//*|*\\*) return 1 ;;
      *) return 0 ;;
    esac
  }
  validate_current_file() {
    valid_relative "$1" || return 1
    current_path="$owner_root/$1"
    [ -f "$current_path" ] &&
      [ ! -L "$current_path" ] &&
      [ "$(file_mode "$current_path")" = 600 ] &&
      [ "$(file_uid "$current_path")" = "$(id -u)" ] ||
      return 1
    current_parent="$(CDPATH= cd -- "$(dirname -- "$current_path")" &&
      pwd -P)" || return 1
    case "$current_parent" in
      "$owner_root"|"$owner_root"/*) ;;
      *) return 1 ;;
    esac
  }
  validate_current_file "$state_rel" &&
    validate_current_file "$handoff_rel" ||
    exit 1
  [ "$(wc -c < "$owner_root/$state_rel")" -le 65536 ] &&
    [ "$(wc -c < "$owner_root/$handoff_rel")" -le 131072 ] ||
    exit 1
  printf '%s\n' \
    "owner_current_state=$state_rel" \
    "owner_current_handoff=$handoff_rel"
)
```

It captures branch/HEAD/base, worktrees, dirty paths, identity, separately
labeled declared/local/automatic Go versions, GitHub auth, active issues, and a
maintainer PR/merge-state summary. An absent root and absent digest skip owner
context. Partial configuration, a non-canonical or non-owner-only boundary,
digest mismatch, or malformed/missing/escaping route fails closed. The bootstrap
is data-only context routing and grants no authority. A local/declared mismatch
means the exact local-toolchain gate is unavailable even when automatic Go
succeeds. Do not split the batch into polls. Repeat only after a real state
transition or contradiction.

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
