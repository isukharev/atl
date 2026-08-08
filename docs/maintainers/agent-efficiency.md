# Efficient agent work

This runbook keeps long ATL maintainer sessions resumable, bounded, and cheap
enough to finish. It complements [Development and verification](development.md),
[Landing a change](landing-a-change.md), and
[Session recovery](session-recovery.md); it does not weaken their authority,
privacy, review, or verification requirements.

## Measured basis

A 6–7 August 2026 maintainer session produced 23 commits and about 12 pull
requests across 187 files. Its implementation quality was high, but orchestration
dominated the cost:

| Measurement | Observed value |
|---|---:|
| Model requests | 2,599 |
| Input tokens | 357 million, including 351 million cached |
| Output tokens | 792 thousand, including 265 thousand reasoning |
| Tool calls | 1,213 exec, 460 stdin writes, 517 waits, 378 patches |
| Context compactions | 15 |

Of those requests, 981 were status checks through wait or stdin-write loops.
They consumed 36% of all input tokens while producing 8% of output. There were
107 polling episodes and 977 individual polls. The worst repeated a status
check 52 times during one 25–27 minute operation. A nine-hour silent gap began
after a wait had already returned a normal completed result: the model-driven
polling loop itself stopped, not the underlying command.

The same session made 902 sliced reads across 338 files. One plan was read 66
times, 100 files were read at least three times, only 24 exec calls batched
multiple commands, and 128 outputs were truncated. Heavy gates were also
duplicated: `make check-maintainability` ran 13 times,
`make check-docs-freshness` 10 times, and `make agent-eval-full` six times.

These numbers establish the controls below. They are a measured operating
budget, not a promise that a client, shell, or hosted service cannot fail.

## Keep waiting outside the model loop

Never make each status tick a model turn. A streaming watch/follow command is
allowed only inside one bounded blocking tool invocation whose intermediate
ticks do not return to the model. Never drive `gh pr checks --watch`,
`gh run watch`, `--follow`, or a foreground process with repeated model-level
wait or stdin-write calls.

Inspect hosted state with one bounded snapshot and project only the fields
needed for the decision:

```sh
gh pr checks <PR> --required --json name,bucket \
  --jq '[.[] | "\(.bucket) \(.name)"] | join("\n")'
gh run view <RUN_ID> --json status,conclusion \
  --jq '.status + " " + (.conclusion // "-")'
```

Take a new snapshot only at a natural dependency boundary. The budget is at
most three model-visible status snapshots for one long local operation or
hosted workflow. A shell-side waiter may inspect state internally at intervals
of at least two minutes because those ticks do not reload model context. When
useful independent work is exhausted, start one bounded blocking waiter and
stay on the task. Return a pending result only when that waiter times out,
loses process liveness, needs new authority, or reaches another real blocker.

## Put long local commands in the background

Treat a local command expected to exceed about 90 seconds as a background job
with a private log and an explicit exit marker. In the Linux development
container, use this shape and substitute the real command and stable lowercase
tag:

```sh
umask 077
mkdir -p tmp/runs
run_tag=check-docs
run_state="tmp/runs/${run_tag}.state"
setsid nohup sh -c '
  make check-docs-catalog
  run_status=$?
  printf "__EXIT=%s\n" "$run_status"
  exit "$run_status"
' >"tmp/runs/${run_tag}.log" 2>&1 </dev/null &
run_pid=$!
run_started="$(awk '{print $22}' "/proc/${run_pid}/stat" 2>/dev/null || true)"
if [ -n "$run_started" ]; then
  printf '%s %s\n' "$run_pid" "$run_started" >"$run_state"
fi
printf 'started %s\n' "$run_tag"
```

Check the marker and the Linux process start tick together. A missing marker is
`running` only while that exact process instance remains alive; otherwise fail
closed instead of waiting forever:

```sh
if grep -m1 '^__EXIT=' "tmp/runs/${run_tag}.log"; then
  :
elif read -r run_pid run_started <"$run_state" &&
    [ "$(awk '{print $22}' "/proc/${run_pid}/stat" 2>/dev/null || true)" = "$run_started" ]; then
  printf 'running\n'
else
  printf 'failed: process ended without an exit marker\n' >&2
  exit 1
fi
tail -c 3000 "tmp/runs/${run_tag}.log"
```

After independent work is exhausted, wait for the same job inside one bounded
shell call. The shell may inspect the marker and liveness every two minutes;
the orchestration layer must not surface those sleeps as separate model turns:

```sh
timeout 2700 sh -c '
  log=$1
  state=$2
  stat_program=$3
  while ! grep -q "^__EXIT=" "$log"; do
    read -r pid started <"$state" || exit 1
    current="$(awk "$stat_program" "/proc/${pid}/stat" 2>/dev/null || true)"
    [ -n "$current" ] && [ "$current" = "$started" ] || exit 1
    sleep 120
  done
' sh "tmp/runs/${run_tag}.log" "$run_state" '{print $22}'
```

GNU `timeout` and `/proc` make this exact form Linux-specific. On another host,
use an equivalent bounded waiter that proves process identity, or report the
missing mechanism. Do not substitute frequent model-visible polling.

At the beginning of a session, verify once that a two-second background probe
survives the end of an exec cell. `setsid` is Linux-specific and background
survival depends on the orchestration environment. If the probe fails or the
launcher is unavailable, report that limitation and agree on a replacement;
do not silently return to polling.

The background mechanism preserves progress and evidence after a model or
client interruption. It does not guarantee that the model will automatically
resume when the process finishes. Before an operation expected to take more
than ten minutes, post one concise commentary line:

```text
<operation> · ~N min · check: tail -c 3000 tmp/runs/<tag>.log
```

Keep a worktree covered by a verification command immutable until that command
finishes. While it runs, prepare external issue/PR text or work in a separate
non-overlapping worktree; do not edit files the command can read and do not run
concurrent generators against the same tree. Inspect the log only when that
result becomes a dependency.

`tmp/runs` is ignored local state, not a general evidence store. Use it only
for public repository builds, tests, and reviews whose output is already safe
for the ordinary development context. Never send live-backend output, private
benchmark material, credentials, owner-only paths, or proprietary content to
these logs. Private evaluation keeps using its fixed owner-only workspace.

## Read once and bound output

- Read a file up to roughly 800 lines in one bounded call. For a larger file,
  locate the relevant symbols or headings with `rg -n`, then read one window
  with enough surrounding context.
- Batch several independent file reads or status commands into one exec call.
- If a file would be read for a third time, write the facts needed for the task
  into `tmp/session-state.md` and consult that summary instead.
- Inspect a diff with `--stat` or `--name-only` first, then request only the
  relevant files. Do not pull a repository-wide diff into context by default.
- Project JSON at the producer with `--json` and `--jq`, or inspect its keys
  once before writing a non-trivial filter. Do not guess a response shape.
- Size output limits from the expected result. After truncation, narrow by
  path, field, pattern, or range instead of requesting the same output again.
- Check tool availability once with `command -v`; do not probe alternate tools
  repeatedly.

## Use one verification boundary

Group related changes when they share one architecture owner, risk class,
impact-selected gate set, and release window. Several coherent features may
use one pull request when those conditions hold. Do not enlarge a PR merely to
reduce CI if review ownership or risk differs.

Iterate with focused tests, then run the impact-selected contour once on the
stable integrated head. `make agent-eval-full` is for evaluator/corpus changes
and release preparation; ordinary product work retains
`make agent-eval-compat`. Do not repeat locally a gate that hosted CI will run
on identical bytes unless the repository contract requires local exact-head
evidence. A material fix starts a new boundary only for paths and gates it can
affect.

## Keep state outside model context

Maintain `tmp/session-state.md` during a long issue. It is ignored, local, and
content-minimized. Record:

- objective, issue, branch, worktree, HEAD, and base;
- dirty-path ownership and the next acceptance criterion;
- background tag, command class, start time, expected duration, and log route;
- verification completed for the current bytes;
- blockers and every active authority category.

Update it at natural milestones, before a long background operation, and
before an expected compaction. Never place credentials, private paths, backend
values, raw evidence, prompts, or proprietary content in it. It supplements
the durable checkpoint required by session recovery; it does not replace a
safe issue/PR-boundary handoff.

Name each linked worktree after its current issue or objective. If the
objective materially changes, create or rename the worktree at a safe clean
boundary rather than continuing under a stale release or task name.

## Deferred release proposal

One possible separate improvement is to start a release workflow at an exact
`main` SHA, run the complete release contour, and create the tag only after a
protected approval. That could bind testing, tagging, and publication to one
commit and avoid repeating full evaluation after a defective tag. This
runbook does not authorize or implement that design. It requires a separate
owner decision, threat review, issue plan, and release-workflow change.
