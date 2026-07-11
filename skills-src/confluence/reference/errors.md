# Confluence error recovery

| Symptom / exit | Meaning | Recovery |
|---|---|---|
| Exit 7 | Backend URL/PAT missing | Run `{{atl.setup_cmd}}` |
| Exit 3 | Token rejected | `atl auth login --service confluence` with a valid PAT |
| Exit 4 | Page/attachment not found or invisible | Verify identity and permissions; do not guess another write target |
| Exit 5 on push | Remote version advanced | Re-pull, reconcile, validate/dry-run/push; human-only force |
| Exit 6 | Forbidden | Surface missing access to the user |
| Exit 8 on apply | Stale marker, reserved edit, fragment loss, divergence, or unconvertible block | Follow the named refusal; migrate pristine old view or choose direct CSF |
| Exit 8: mutation active | Another pull/render/apply/push or mirror-local `conf edit` holds the mirror lock | Wait; never remove the lock or run concurrently |
| Exit 8: corrupt/missing sidecar | Mirror scan cannot prove complete state | Repair or re-pull; never accept partial clean status |
| Exit 8 on `create --from-md` | Block outside Markdown subset | Use validated CSF `--from-file` |
| `unknown` guarded write | Verification could not prove outcome | Inspect/re-read; never auto-replay |
| Search says query required | No CQL/filter | Supply CQL or `--space/--title/--label/--type` |

For apply fragment loss, restore the opaque marker unless the user explicitly
accepts `--allow-fragment-loss`. For a future document marker, update `atl`;
never render/downgrade it with an older binary.

For direct CSF exact-match misses, try once with the shortest unique anchor,
then use `atl conf edit` and inspect its hidden-byte diagnostics. Do not burn
turns retrying visually identical NBSP/zero-width variants.

For corrupt `.atl/state.json`, do not edit or delete it in place and do not
declare the mirror clean. Preserve the whole mirror as a private backup, create
a fresh root, re-pull the explicitly approved pages, and compare old `.csf`
files against their pristine `.atl/base` copies before carrying any local work
into the new mirror. Also review every derived `.md` against its regenerated
view so unapplied Markdown edits are preserved and deliberately reapplied.
Only retire the corrupt root after every local edit is accounted for.

If a tool failure remains misleading or repeatedly costly, offer the `atl`
skill's consent-gated feedback flow. Public reports must be sanitized; private
case details stay in the private artifact.
