<!-- Generated from skills-src/confluence/reference/editing.md — edit the source and run 'make gen-plugins'. -->
# Confluence Markdown review and write cycle

Load this reference for ordinary Markdown body edits, offline diff review,
multi-page plans, or version-gated push. It does not cover direct CSF surgery,
metadata/comments, attachments, or tables; select those direct references from
the main skill instead of mixing surfaces.

## Gate the edit

1. Fix one mirror root for the cycle and confirm local/remote status.
2. Require `<!-- atl:document confluence-page v4 -->`. Preserve edited legacy
   views outside `.md` before render; update atl for future markers.
3. Treat generated metadata/comments/Jira query regions as readonly.
4. Use `.md` only for supported prose, headings, lists, code, and simple tables.
   Choose direct CSF for opaque wrappers, ambiguous mentions, spans/nested
   tables, or byte surgery.
5. Never edit `.md` and `.csf` concurrently or expect one to preserve unapplied
   changes from the other.

## One-page Markdown cycle

After explicit approval to begin the body-write workflow, remove the inherited
read-only policy only for each reviewed apply/push command:

```bash
env -u ATL_READ_ONLY atl conf apply <page.md> --dry-run
env -u ATL_READ_ONLY atl conf apply <page.md>
atl conf validate <page.csf>
ATL_READ_ONLY=1 atl conf diff <page.csf> -o text
env -u ATL_READ_ONLY atl conf push --dry-run <page.csf>
env -u ATL_READ_ONLY atl conf push <page.csf>
```

Large edits and every table merge require apply dry-run. Require `wrote:true`
and `csf_ok:true`; exit 8 writes nothing. Use compact diff for initial review
and JSON only for hashes/features/byte windows/validation. Preserve a
`baseline_mismatch` candidate and repair/re-pull its baseline before planning or
pushing.

Review push dry-run `removed_fragments`, `remote_drifted`, exact candidate
hash, and expected version. Exit 5 means remote drift: re-pull and reconcile.
Never auto-force. After an ambiguous write, reconcile by fresh state and never
replay `unknown` automatically.

## Review several pages as one closed set

```bash
export ATL_READ_ONLY=1
atl conf diff <DIR> -o text
atl conf plan create <page.csf|DIR> --out <private-plan.json>
atl conf plan preview <private-plan.json>
# after approval of the exact proposal hash:
env -u ATL_READ_ONLY atl conf plan apply <private-plan.json> \
  --expected-proposal-hash <reviewed-hash> --confirm APPLY
```

The mode-0600 plan omits body prose but contains titles/paths. Use a new output
path; creation never overwrites. Preview must qualify every entry as
`would_apply` or `already_satisfied`. Never edit/re-hash a plan, invent entries,
replace its exact hash/confirmation, or replay an unknown outcome.

## Concurrency and follow-up

Pull/render/apply/push and mirror-local edit share a persistent mutation lock;
wait rather than deleting or bypassing it. Local writes/checkpoints stay
serial. Finish the body cycle before title/move operations, re-pull after a
successful relocation, and add comments last after duplicate/reconciliation
checks. Never infer clean state from a partial scan or missing sidecar/body.
