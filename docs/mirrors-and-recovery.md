# Durable mirrors and recovery

An ATL mirror is a reviewed working copy, not a disposable download directory.
It keeps native Jira or Confluence bytes, a pristine baseline, derived views,
and sync evidence together so local and remote changes can be distinguished.

Use this guide when you need to refresh an existing mirror, adopt a legacy
mirror, preserve local work, recover after a version conflict, or register a
newly created object. Exact flags and JSON schemas remain in the
[command reference](usage.md) and [output contract](OUTPUT_CONTRACT.md).

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

Create commands remain remote-only unless both `--register` and `--into` are
explicit. Registration uses an authoritative post-create readback rather than
assuming the submitted body is the stored result:

```sh
env -u ATL_READ_ONLY atl conf page create \
  --space EXAMPLE --title 'Tracked page' --from-md page.md \
  --register --into "$ATL_WORKSPACE_ROOT"

env -u ATL_READ_ONLY atl jira issue create \
  --project EXAMPLE --type Task --summary 'Tracked task' \
  --from-md description.md --register --into "$ATL_WORKSPACE_ROOT"
```

If the remote create succeeds but readback or local registration fails, the
result still identifies the new object, reports `not_registered`, and exits
`8`. Do not repeat the create. Preserve the input/result, remove the reported
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

For large, incremental, or resumable selections, see the exact
[`conf pull`](usage.md#atl-conf-pull) and
[`jira pull`](usage.md#atl-jira-pull) reference sections.
