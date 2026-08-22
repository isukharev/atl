# Registration and write-guard contracts

Shared created-object registration and guarded copy proposal/result shapes.

[Reference index](README.md) · [Documentation home](../../README.md)

## Explicit created-object registration

`conf page create` preserves its remote-only behavior when registration is
omitted. `jira issue create` and `conf page copy` are independently
preview-first; the two registration flags are part of their reviewed proposals and
must also be supplied together. In default JSON mode, an explicit registration
adds this object to the created page/issue result:

```json
{
  "registration": {
    "status": "registered",
    "root": "mirror",
    "path": "SPACE/page/page.csf",
    "version": 1,
    "sha256": "<sha256>",
    "readback_reconciled": true
  }
}
```

The envelope above shows only the additive member; the existing page fields
(`id`, `title`, `version`, `url`) or Jira issue fields remain alongside it.
`version` is present for Confluence and omitted for Jira. `path` is relative to
`root`. The digest, version, path, native file, pristine base, derived view, and
sync/view state are derived from one authoritative post-create readback, never
from the submitted body or the create response. Local artifacts and the base
are written and verified before sync state is saved.

After a known remote success followed by a readback, collision, or local commit
failure, stdout still identifies the created object. JSON uses
`registration.status:"not_registered"`, `readback_reconciled:false` until a
readback has qualified the object, a stable `reason`, and recovery text when an
identifier is available. The command then emits its normal structured error on
stderr and exits `8`. Guarded Jira `-o id` is apply-only and emits a key only
for terminal `applied`; text is unsupported. This is not authorization to replay the non-idempotent create.
Preserve local files and use the reported narrow `conf pull --id ... --into ...`
or `jira pull --jql 'key = ...' --limit 1 --into ...` recovery.

Guarded Jira create instead uses outer status `applied_not_registered` after
remote proof. Its registration object is `not_registered` for a definite local
failure, or `registration_outcome_unknown` when exact state and artifacts are
visible after a late durability failure but their durable commit cannot be
proved. In the latter case, preserve and inspect those files; do not pull or
replay. `registration_effects.planned_files` is present from preview onward,
and `actual_files` reports only coordination, privacy scaffold, backend-binding,
and mirror-state files created by that apply, including on blocked or
`applied_not_registered` results.

## Guarded Jira issue create

Both create leaves emit schema version 1. The proposal contains content-free
project/type, summary/description/typed-field, request, metadata, optional
registration, bound, and backend digests plus `proposal_hash`; it never emits
raw candidate values. Apply requires `--apply --expected-proposal-hash`. Only a
positive immutable-ID acknowledgement plus exact ID readback is `applied`.
Definitive HTTP refusal is `not_applied`; ambiguous or unproved writes are
`outcome_unknown` and never automatically replayed.

## Guarded Confluence page copy

`conf page copy` emits schema version 1. Dry-run status is `would_apply` and
contains no created `id`. Its content-minimized evidence includes
`source_id`, `current_version`, source body/title/hierarchy hashes and byte count,
target title/space/parent and complete hierarchy evidence, target-parent
version/body/hierarchy evidence when applicable,
`backend_sha256`, optional `registration_root_sha256`, and `proposal_hash`.
`complete:true` means the exact current source and destination-parent
projections were qualified; it does not mean a write occurred.

Apply requires `--apply --expected-version --expected-proposal-hash`. After one
POST, `write_attempted:true` always turns stdout failure into exit 8 with a
no-replay instruction. Proven results are `applied` or `recovered`; both include
the authoritative created `id`, `version:1`, and `reconciled:true`.
`not_applied` is reserved for a definitive rejection. Missing IDs, ambiguous
writes without a usable ID, and incomplete or mismatched readback use
`outcome_unknown`, `complete:false`, exit 8, and must never trigger a retry or
title search. A local registration failure uses `applied_not_registered`, keeps
the known ID and registration recovery object, and exits 8. `-o id` is rejected
for preview and prints the known created ID on apply, including a post-write
failure path.
