# Durable mirrors and recovery

An ATL mirror is a reviewed working copy, not a disposable download directory.
It keeps native Jira or Confluence bytes, a pristine baseline, derived views,
and sync evidence together so local and remote changes can be distinguished.

Use this guide when you need to refresh an existing mirror, adopt a legacy
mirror, preserve local work, recover after a version conflict, or register a
newly created object. Exact flags and JSON schemas remain in the
[command reference](reference/cli/README.md) and [output contract](reference/output/README.md).

## Start with a dedicated root

Keep backend content outside a source repository and name the root explicitly:

```sh
export ATL_READ_ONLY=1
export ATL_WORKSPACE_ROOT=/absolute/path/to/atl-workspace

atl conf pull --id 123456 --into "$ATL_WORKSPACE_ROOT"
# or:
atl jira pull --jql 'project = EXAMPLE order by key' \
  --limit 20 --into "$ATL_WORKSPACE_ROOT"
```

Pull writes local files but never mutates Jira or Confluence. The native `.csf`
or `.wiki` file is authoritative for remote writes. The neighboring `.md` is a
derived view whose supported edits must pass through `conf apply` or
`jira apply` before publication.

## Inspect identity and local state

Every durable mirror is bound separately to a content-minimized digest of each
configured backend it contains. URLs and hostnames are not stored in the
binding file. Inspect bindings and local state without credentials or network:

```sh
atl mirror backend status --into "$ATL_WORKSPACE_ROOT"
atl conf status --into "$ATL_WORKSPACE_ROOT"
atl jira status --into "$ATL_WORKSPACE_ROOT"
```

A first successful non-dry-run pull into a service-empty root creates the
binding automatically. A legacy root that already contains service evidence is
not adopted implicitly. Review and bind it explicitly:

```sh
env -u ATL_READ_ONLY atl mirror backend bind \
  --into "$ATL_WORKSPACE_ROOT" --service confluence
env -u ATL_READ_ONLY atl mirror backend bind \
  --into "$ATL_WORKSPACE_ROOT" --service confluence \
  --apply \
  --expected-backend-sha256 'sha256:<reviewed digest>' \
  --confirm BIND
```

The entire `bind` leaf is mutation-classified even though preview performs no
network request and writes nothing. Apply is a local compare-and-set: it never
replaces a different binding. Repeat the workflow separately for Jira when the
root contains both services.

## Qualify a refresh before changing files

Use pull dry-run when a mirror may contain local work:

```sh
atl conf pull --space EXAMPLE --into "$ATL_WORKSPACE_ROOT" --dry-run
atl jira pull --jql 'project = EXAMPLE order by key' \
  --into "$ATL_WORKSPACE_ROOT" --limit 0 --dry-run
# Use this separate mode when downstream indexing requires proven whole-project
# membership rather than an uncapped ordinary JQL walk.
atl jira pull --complete --project EXAMPLE --max-issues 5000 \
  --into "$ATL_WORKSPACE_ROOT" --dry-run
```

Normal pull is non-destructive. For each tracked object ATL reconciles the
canonical path, sidecar, pristine baseline, native bytes, metadata, and derived
view before replacement. Unsafe objects are preserved and reported while clean
siblings continue; the command exits `8` so a partial refresh cannot be treated
as complete.

For a qualified native edit that you intentionally want to discard, choose one
explicit recovery:

```sh
# Preserve the exact old native bytes in the content-addressed stash first.
atl conf pull --id 123456 --into "$ATL_WORKSPACE_ROOT" --stash-local

# Or discard the qualified native edit without a stash.
atl jira pull --jql 'key = EXAMPLE-1' --limit 1 \
  --into "$ATL_WORKSPACE_ROOT" --overwrite-local
```

Neither flag bypasses an edited Markdown view, a missing/corrupt baseline,
future-format view, path drift, or inconsistent state. Apply or otherwise
preserve supported `.md` work before refreshing; do not use overwrite as a
generic repair switch.

For Jira, `--limit 0` removes only the client count cap. It does not prove
terminal pagination and has no resumable selector checkpoint. Complete-project
mode instead requires two identical qualified numeric-ID passes before its
first payload publication and resumes only an accepted durable suffix. It
retains objects absent from a later selection and never grants deletion or
remote-write authority.

## Recover an on-demand corpus build

Use `corpus build` when an indexer needs one ready generation rather than a
working mirror. Its attempt mirrors are private recovery state, not consumer
input:

```sh
export ATL_READ_ONLY=1
atl corpus build --root /private/indexer-corpus \
  --jira-project EXAMPLE --max-jira-issues 5000 \
  --max-requests 10000 --max-response-bytes 2147483648 \
  --max-members 100000 --max-generation-bytes 4294967296 \
  --deadline 1h --max-in-flight 4 --requests-per-second 20
```

Repeat the exact command after an ordinary returned read failure. ATL resumes
the retained attempt with its original deadline and cumulative request/byte
usage. Do not edit an attempt mirror or point an indexer at it.

If recovery reports `phase=recover reason=outcome_unknown`, the process stopped
while a remote read phase was marked in flight. ATL refuses to guess whether
that phase completed. Preserve the root and rerun the same selection and bounds
with `--restart`; ATL first reconciles local complete-pull state, retains the
old attempt, and then begins a fresh one. Restart does not delete a prior
generation or switch `current.v1.json` until the fresh capture, projection,
seal, and pointer verification all succeed.

`phase=publish reason=outcome_unknown` is different: a verified generation may
already be current while the final completed attempt record lacks a confirmed
durability barrier. Preserve the root and repeat the exact command without
`--restart`; ATL verifies the visible current generation and active record
before deciding whether to resume recovery or begin the next bounded capture.

## Reconcile local and remote changes

After a version conflict, do not begin with a bare pull. Preserve the local
candidate and compare exact `base`, `ours`, and `theirs` bytes first:

```sh
ATL_READ_ONLY=1 atl conf reconcile preview \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf"
ATL_READ_ONLY=1 atl jira reconcile preview \
  "$ATL_WORKSPACE_ROOT/EXAMPLE/EXAMPLE-1.wiki"
```

Preview performs one single-attempt remote read and reports
`unchanged|local_only|remote_only|diverged` without returning bodies or changing
the mirror. If external merge tools or an agent need exact inputs, stage
immutable review artifacts without touching the working file or baseline:

```sh
env -u ATL_READ_ONLY atl conf reconcile stage \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf"
env -u ATL_READ_ONLY atl jira reconcile stage \
  "$ATL_WORKSPACE_ROOT/EXAMPLE/EXAMPLE-1.wiki"
```

Resolve the candidate deliberately, run the local validation/diff again, then
create a fresh remote preview. A stale version, baseline, or proposal is never
permission to force or replay a write.

## Register newly created objects immediately

Confluence create remains remote-only unless both `--register` and `--into` are
explicit. Guarded Jira create is always preview until the exact reviewed apply;
omit the registration pair from both phases for a remote-only apply.
Registration uses an authoritative post-create readback rather than assuming
the submitted body is the stored result:

```sh
env -u ATL_READ_ONLY atl conf page create \
  --space EXAMPLE --title 'Tracked page' --from-md page.md \
  --register --into "$ATL_WORKSPACE_ROOT"

atl jira issue create preview \
  --project EXAMPLE --type Task --summary 'Tracked task' \
  --from-md description.md --register --into "$ATL_WORKSPACE_ROOT"
# Review proposal_hash, then repeat the exact candidate and registration root:
env -u ATL_READ_ONLY atl jira issue create \
  --project EXAMPLE --type Task --summary 'Tracked task' \
  --from-md description.md --register --into "$ATL_WORKSPACE_ROOT" \
  --apply --expected-proposal-hash '<reviewed hash>'
```

If the remote create succeeds but readback or local registration fails, the
result still identifies the new object, reports the command-specific local
registration failure (`not_registered` for legacy created-object workflows or
`applied_not_registered` for guarded Jira create), and exits `8`. Do not repeat
the create. Preserve the input/result, remove the reported
local obstruction, and recover only the returned object:

```sh
atl conf pull --id '<returned page id>' --into "$ATL_WORKSPACE_ROOT"
atl jira pull --jql 'key = RETURNED-1' --limit 1 \
  --into "$ATL_WORKSPACE_ROOT"
```

## Recovery decision table

| Evidence | Safe next action |
|---|---|
| Local safety refusal during pull | Preserve the object; apply its view or choose qualified stash/overwrite |
| Version conflict | Keep the candidate; run reconcile preview before any refresh |
| `diverged` reconcile state | Stage exact review artifacts and merge deliberately |
| Missing or mismatched backend binding | Stop before network; inspect and bind only the intended service/root |
| Remote create with `not_registered` | Do not replay; pull only the returned identity after fixing local state |
| `outcome_unknown` after any write | Retain evidence and reconcile; never infer absence or retry automatically |
| Corpus build returned a known read failure | Rerun exact options; deadline and cumulative budgets do not reset |
| Corpus build `recover/outcome_unknown` | Preserve the root and use explicit `--restart`; never auto-replay the in-flight read |

For large, incremental, or resumable selections, see the exact
[`conf pull`](reference/cli/confluence-mirrors.md#atl-conf-pull) and
[`jira pull`](reference/cli/jira-mirrors.md#atl-jira-pull) reference sections.

## Workflow: pull → edit → validate → push

This is the canonical edit loop for Confluence pages:

```bash
# 1. Keep investigations read-only and pull the page. Pull writes only local
#    mirror artifacts; --assets is optional.
export ATL_READ_ONLY=1
atl conf pull --id 12345678 --assets --into mirror

# 2. Inspect the on-disk layout
#    mirror/DOCS/parent/child/child.csf   ← your source of truth
#    mirror/DOCS/parent/child/child.md    ← versioned staging view

# 3. Edit the supported body in child.md, then inspect and apply the local
#    merge. conf apply is mutation-classified even in dry-run mode.
env -u ATL_READ_ONLY atl conf apply \
  mirror/DOCS/parent/child/child.md --dry-run -o text
env -u ATL_READ_ONLY atl conf apply mirror/DOCS/parent/child/child.md
#    Direct native .csf edits remain available when Markdown cannot represent
#    the required construct.

# 4. Validate before pushing
atl conf validate mirror/DOCS/parent/child/child.csf
atl conf diff mirror/DOCS/parent/child/child.csf -o text

# 5. Preview the remote write. conf push is mutation-classified even when its
#    --dry-run prevents the PUT.
env -u ATL_READ_ONLY atl conf push \
  --dry-run mirror/DOCS/parent/child/child.csf

# 6. After reviewing the exact preview, run the same target once without
#    --dry-run. The Confluence version gate remains automatic.
env -u ATL_READ_ONLY atl conf push mirror/DOCS/parent/child/child.csf
```

If push exits `5`, preserve the working candidate. Do not immediately overwrite
it with a pull and do not add `--force`. Qualify the refresh and inspect one
content-free three-way comparison first:

```bash
ATL_READ_ONLY=1 atl conf pull --id 12345678 --into mirror --dry-run
ATL_READ_ONLY=1 atl conf reconcile preview \
  mirror/DOCS/parent/child/child.csf --into mirror -o text
```

The pull dry-run may report `local_safety` and exit `8`; that proves the local
candidate was preserved. Reconcile reads the exact current remote page once and
leaves the working native/view/baseline artifacts unchanged. If exact review
files are useful, the separately mutation-classified stage still leaves the
working candidate unchanged:

```bash
env -u ATL_READ_ONLY atl conf reconcile stage \
  mirror/DOCS/parent/child/child.csf --into mirror
```

After reviewing base/ours/theirs, explicitly merge or reapply the intended
change onto current remote bytes. Use a qualified `pull --stash-local` when the
local native edit should be retained in `.atl/stash/` before refresh; it does
not bypass a dirty Markdown view or broken baseline. Validate, diff, and run a
fresh push preview before one new write.

For a whole space:

```bash
atl conf pull --space DOCS --into mirror
# ... edit files ...
atl conf status mirror                # see which files are dirty
env -u ATL_READ_ONLY atl conf push mirror/DOCS/ # push reviewed dirty files
```

For Jira issues the workflow is read-heavy:

```bash
atl jira pull --jql "project=PROJ and status=Open" --into mirror-jira
# read mirror-jira/PROJ/PROJ-1.md  and  mirror-jira/PROJ/PROJ-1.json
# edit a supported section in PROJ-1.md, then stage and preview it explicitly:
env -u ATL_READ_ONLY atl jira apply \
  mirror-jira/PROJ/PROJ-1.md --dry-run -o text
env -u ATL_READ_ONLY atl jira apply mirror-jira/PROJ/PROJ-1.md
env -u ATL_READ_ONLY atl jira push mirror-jira/PROJ/PROJ-1.wiki
# after reviewing the preview:
env -u ATL_READ_ONLY atl jira push \
  --apply mirror-jira/PROJ/PROJ-1.wiki

# dedicated proposal commands remain read-only until their explicit apply:
ATL_READ_ONLY=1 atl jira issue transition preview PROJ-1 --to "In Review"
# Repeat the exact target/fields/comment with transition --apply and the reviewed hash.
atl jira issue comment preview PROJ-1 --from-file - <<'EOF'
Updated as discussed in today's meeting.
EOF
# Repeat the exact body with `comment add --apply --expected-proposal-hash ...`
```
