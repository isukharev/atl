# Safe writes with `atl`

Every remote change in `atl` is deliberate. A normal flow is:

1. read fresh state;
2. prepare a local candidate or deterministic proposal;
3. inspect the diff or dry-run;
4. bind the reviewed state with a version, baseline, or proposal hash;
5. apply once;
6. reconcile the result without replay.

`ATL_READ_ONLY=1` blocks all mutating commands. Keep it enabled for
investigations; remove it only after the target and proposed change have been
reviewed. The examples below use `env -u ATL_READ_ONLY` for one exact mutating
command so the shell-wide policy remains enabled afterwards.

Mirror refresh, backend binding, three-way conflict handling, and created-object
registration are covered in [durable mirrors and recovery](mirrors-and-recovery.md).

The shared transport never follows a redirect from a mutating HTTP request.
This applies to every Jira and Confluence POST, PUT, PATCH, and DELETE: a 3xx is
reported as an error for endpoint-aware reconciliation, and its method or body
is never replayed at a server-selected path. Explicit single-attempt operations
also refuse redirects for reads. After an ambiguous result, inspect the exact
target before deciding whether a separately reviewed command is justified;
never wrap a write in a retry loop.

## Confluence: edit without losing native content

Pull one page into a dedicated mirror:

```sh
atl conf pull --id 123456 --into "$HOME/.atl/example-workspace"
```

The `.csf` file is the native Confluence Storage Format body. The `.md` file is
a derived staging view. For Markdown edits:

```sh
env -u ATL_READ_ONLY atl conf apply \
  "$HOME/.atl/example-workspace/SPACE/page/page.md" --dry-run
env -u ATL_READ_ONLY atl conf apply \
  "$HOME/.atl/example-workspace/SPACE/page/page.md"
atl conf validate "$HOME/.atl/example-workspace/SPACE/page/page.csf"
atl conf diff "$HOME/.atl/example-workspace/SPACE/page/page.csf" -o text
```

`conf apply` merges only supported changed blocks into the native bytes and
fails closed on constructs it cannot represent. Untouched blocks stay
byte-preserved.

Preview current remote consequences:

```sh
env -u ATL_READ_ONLY atl conf push \
  "$HOME/.atl/example-workspace/SPACE/page/page.csf" --dry-run
```

After review, repeat without `--dry-run`. A remote version change exits `5`;
preserve the local candidate and run `conf reconcile preview` before any
refresh. Resolve or stage the exact base/ours/theirs evidence, then create a
fresh push preview. Never add `--force` automatically—overriding concurrent
changes is a human decision.

## Confluence: reviewed footer comment

Keep the exact native-CSF body in a bounded file. The dedicated preview command
is read-only; `comment add` is dry-run by default but remains mutating-classified:

```sh
ATL_READ_ONLY=1 atl conf comment preview --id 123456 --from-file comment.csf
env -u ATL_READ_ONLY atl conf comment add --id 123456 --from-file comment.csf \
  --apply \
  --expected-proposal-hash <reviewed-hash>
```

The body is validated and preserved exactly up to 1 MiB. The proposal binds the
backend, page/version, stable actor, body, capability evidence, and complete
root-only footer baseline. Apply revalidates immediately before at most one
POST and reconciles by complete readback. Retain `applied` or `recovered` as
proven success; `outcome_unknown` may have committed and must never be replayed.
This workflow creates footer roots only—not replies, inline comments, or
resolution changes. Those operations use the separately activated provider
below.

## Confluence: reviewed inline comment mutation

Activate and remotely qualify an exact compiled Data Center compatibility
profile first. Preview is read-only. Existing-thread operations bind a root
thread; inline create instead binds exact private body and selection files plus
a zero-based occurrence:

```sh
ATL_READ_ONLY=1 atl conf comment mutation preview --id 123456 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0
env -u ATL_READ_ONLY atl conf comment mutation apply --id 123456 \
  --operation inline-create --from-file comment.csf \
  --selection-file selection.txt --occurrence 0 --apply \
  --expected-proposal-hash <reviewed-hash>
```

For reply, pass `--thread-id`, `--operation reply`, and a native-CSF body.
Resolve/reopen pass the exact root id and omit the body. For inline create, ATL
binds the raw selection bytes but searches using the pinned client's exact
NBSP/edge-whitespace normalization, native exclusion masks, and overlapping
match indexing. It rejects masked occurrences, footer-fallback regions,
unsupported highlighter subtrees, and layout-dependent floating headers before
any POST. Apply requalifies the exact product pin and stable page/DOM/comment
evidence immediately before one fixed write. Inline create uses only a fresh server request-time in that attempt;
ATL never writes marker CSF. Complete readback must prove the exact new root,
reply, or state transition and, for create, only one server-owned marker wrapper
in native page storage. Accept only reconciled `applied` or `recovered`; never
replay `outcome_unknown`. The surface is CLI-only and absent from MCP.

## Confluence: reviewed page copy

Page copy is a non-idempotent create and is dry-run by default:

```sh
env -u ATL_READ_ONLY atl conf page copy --id 123456 --title 'Copied page'
env -u ATL_READ_ONLY atl conf page copy --id 123456 --title 'Copied page' --apply \
  --expected-version 7 --expected-proposal-hash '<preview hash>'
```

The leaf remains mutating-classified even in preview mode, so
`ATL_READ_ONLY=1` blocks both forms. Temporarily remove that policy only for the
reviewed preview/apply workflow; preview itself performs reads only.

The proposal binds the backend, exact current source bytes/version/hierarchy,
resolved target title/space/parent and parent state, plus optional registration
intent and canonical root identity. Apply repeats exact source/parent reads,
sends one POST without redirects or transport retries, and requires an exact
current version-1 readback. It never searches by title. Treat
`outcome_unknown` as terminal evidence to investigate, not permission to replay.

## Confluence: reviewed page trash

Page deletion means moving one current page to the Confluence trash. Preview is
read-only and emits the exact version and content-minimized proposal hash:

```sh
env -u ATL_READ_ONLY atl conf page delete --id 123456
env -u ATL_READ_ONLY atl conf page delete --id 123456 \
  --apply --confirm TRASH \
  --expected-version <reviewed-version> \
  --expected-proposal-hash <reviewed-hash>
```

The preview sends no write, but the destructive leaf remains
mutating-classified, so `ATL_READ_ONLY=1` blocks both preview and apply before
credentials or network access.

The proposal binds backend, page status/version, hierarchy, title, and native
body identity. Apply repeats that complete read immediately before one DELETE.
Confluence supplies no delete-time version compare-and-set, so ATL then checks
both explicit current and trashed views. DELETE is qualified as
`status=current`, never as permanent purge. Only an exact trashed match proves
`applied` or `recovered`; never replay `outcome_unknown`. Restoring or purging a
trashed page is outside this command.

## Jira: mirror description edits

Pull a bounded issue set:

```sh
atl jira pull \
  --jql 'project = EXAMPLE and key = EXAMPLE-1' \
  --into "$HOME/.atl/example-workspace"
```

Edit a supported section in the generated `.md` view, then stage it into the
guarded local write set:

```sh
env -u ATL_READ_ONLY atl jira apply \
  "$HOME/.atl/example-workspace/EXAMPLE/EXAMPLE-1.md" --dry-run
env -u ATL_READ_ONLY atl jira apply \
  "$HOME/.atl/example-workspace/EXAMPLE/EXAMPLE-1.md"
atl jira status "$HOME/.atl/example-workspace"
env -u ATL_READ_ONLY atl jira push \
  "$HOME/.atl/example-workspace"
```

`jira push` is a dry-run by default. Review its item/diff and baseline
evidence, then repeat the same target with `--apply`. Jira does not provide the
same server-side page version gate as Confluence, so `atl` revalidates its
stored baseline before writing.

## Jira: reviewed comment

Keep the comment body out of command-line arguments:

```sh
atl jira issue comment preview EXAMPLE-1 --from-md comment.md
```

Review the normalized body and complete comment baseline. Apply the same body
once with the exact emitted hash:

```sh
env -u ATL_READ_ONLY atl jira issue comment add EXAMPLE-1 \
  --from-md comment.md \
  --apply \
  --expected-proposal-hash <reviewed-hash>
```

The command sends at most one POST and reconciles an ambiguous outcome without
replaying it. The same principle applies to guarded field, transition, watcher,
worklog, and multi-object plan commands.

## Jira: permanent issue deletion

Jira Data Center has no issue trash. The whole deletion leaf is
mutation-classified, including its GET-only preview, so enter an explicitly
approved workflow before temporarily removing the read-only policy:

```sh
env -u ATL_READ_ONLY atl jira issue delete EXAMPLE-1
env -u ATL_READ_ONLY atl jira issue delete EXAMPLE-1 \
  --apply --confirm DELETE \
  --expected-updated '<exact value from preview>' \
  --expected-proposal-hash '<hash from preview>'
```

The proposal binds the backend, canonical key, immutable numeric issue id,
freshness marker, complete permission-relative direct-subtask inventory, and
cascade intent. Existing subtasks block deletion unless `--delete-subtasks`
was explicitly reviewed in both preview and apply. Apply repeats the complete
snapshot immediately before at most one DELETE addressed by numeric id.

Only an acknowledged DELETE followed by exact numeric-id not-found evidence is
`applied`. Any ambiguous response is `outcome_unknown`, even if a
permission-relative read cannot find the issue. Retain the result and never
retry the deletion automatically.

## Pre-write checklist

- The backend and object were freshly identified.
- The candidate content is in a bounded file or native mirror, not argv.
- The dry-run/diff is complete and has been reviewed.
- Version, baseline, and proposal hashes come from that exact preview.
- The account has only the required permissions.
- `--force` is absent unless a human explicitly chose to overwrite drift.
- No retry loop surrounds the write.
- The final readback or result classification is retained.

## Recovery rules

| Result | Action |
|---|---|
| Exit `5`, version conflict | Preserve the candidate and run reconcile preview; do not force automatically |
| Exit `8`, stale proposal/baseline | Recreate and review a fresh proposal |
| Timeout or transport error after a write | Use the command's reconciliation result; never replay blindly |
| HTTP 429 on a write | Respect the cooldown; do not assume the write was absent |
| Validation failure | Fix the candidate locally; no network write occurred |

The exhaustive command flags are in [usage.md](usage.md); stable output and
recovery envelopes are in [OUTPUT_CONTRACT.md](OUTPUT_CONTRACT.md).
