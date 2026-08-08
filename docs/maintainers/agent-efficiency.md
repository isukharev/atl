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

## Do not stream or poll

Never use streaming watch/follow commands such as `gh pr checks --watch`,
`gh run watch`, or `--follow`. Do not keep a foreground session alive with
repeated wait or stdin-write calls.

Inspect hosted state with one bounded snapshot and project only the fields
needed for the decision:

```sh
gh pr checks <PR> --json name,bucket \
  --jq '[.[] | "\(.bucket) \(.name)"] | join("\n")'
gh run view <RUN_ID> --json status,conclusion \
  --jq '.status + " " + (.conclusion // "-")'
```

Take a new snapshot only at a natural dependency boundary. The budget is at
most three status snapshots for one long local operation or hosted workflow.
If useful independent work is exhausted while state remains pending, report
the expected duration and end the turn. A later session recovers from durable
state; it does not reconstruct a polling loop.

## Put long local commands in the background

Treat a local command expected to exceed about 90 seconds as a background job
with a private log and an explicit exit marker. In the Linux development
container, use this shape and substitute the real command and stable lowercase
tag:

```sh
umask 077
mkdir -p tmp/runs
run_tag=check-docs
setsid nohup sh -c '
  make check-docs-catalog
  run_status=$?
  printf "__EXIT=%s\n" "$run_status"
  exit "$run_status"
' >"tmp/runs/${run_tag}.log" 2>&1 </dev/null &
printf 'started %s\n' "$run_tag"
```

Check it with one short bounded command, never through the process session:

```sh
grep -m1 '^__EXIT=' "tmp/runs/${run_tag}.log" || printf 'running\n'
tail -c 3000 "tmp/runs/${run_tag}.log"
```

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

While it runs, perform the next independent implementation, documentation,
review, or PR-preparation step. Inspect the log only when that result becomes a
dependency.

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
