<!-- Generated from skills-src/onboarding/reference/learning.md — edit the source and run 'make gen-plugins'. -->
# Consent-gated learning and revalidation

Use this lifecycle only after the user asks to remember, refine, or revalidate workflow knowledge,
or after a repeated discovery where you first offer that option and the user agrees.

## Observation boundary

1. Load only the necessary profile slice with `atl profile show --section ... --service ...`.
2. State the exact proposed memory change and its evidence. Ask permission to create a private
   observation/suggestion; ordinary task completion is not consent.
3. Create a `0700` temporary directory with cleanup on decline, error, interruption, or success.
   Keep observation, revalidation, and suggestion files mode `0600` inside it.
4. Set `base_profile_hash` to the current profile hash. Schema facts require source and
   `verified_at`. Preference/render/selector proposals require evidence with `source`,
   `observed_at`, and `reason`.
   Preference keys and render services are partial updates: omit values that should remain
   unchanged, and never reconstruct an unloaded sibling section from guesses.
5. Never put `team_policy` in observations. Policy changes require a declared team source and the
   full onboarding/profile preview flow.

Minimal observations shape:

```json
{
  "schema_version": 1,
  "base_profile_hash": "<current-profile-hash>",
  "schema": {
    "jira_fields": [{
      "id": "customfield_10001",
      "name": "Risk Notes",
      "type": "string",
      "source": "approved field metadata read",
      "verified_at": "2026-07-10T12:00:00Z"
    }]
  },
  "preferences": {"services": ["jira"]},
  "evidence": [{
    "source": "approved workflow review",
    "observed_at": "2026-07-10T12:05:00Z",
    "reason": "user confirmed this recurring Jira workflow"
  }]
}
```

## Suggest and decide

```bash
atl profile suggest --from-file "$PRIVATE_TMP/observations.json" \
  --out "$PRIVATE_TMP/learning.atl-suggestion.json"
atl profile suggestion review --from-file "$PRIVATE_TMP/learning.atl-suggestion.json"
```

Show the evidence and normalized candidate. If `previously_rejected:true`, call it out and do not
recommend applying again without new user direction.

After exact approval:

```bash
atl profile suggestion apply --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash> \
  --candidate-hash <preview.candidate_hash> \
  --expected-current-hash <preview.current_hash>
```

Applying the suggestion changes private memory only. If review reports changed `render_defaults`
or `preferences`, load only those slices and compare them with `atl config show`. Separately preview
and obtain approval for any runtime render command or mirror choice (`--into`, current-session
`ATL_MIRROR_ROOT`, or a shell-profile handoff). Surface conflicts between active and saved roots;
neither wins silently, and an earlier declined sync remains declined until the user approves it.
Expand legacy `~` without `eval`, use an absolute path, and shell-quote it as one value. Verify
effective render from the relevant global/target-mirror context and an environment-backed mirror
with `atl config show`, or verify the resulting command's root/path when using explicit `--into`.
If no approved mirror operation runs, mark `--into` verification pending rather than causing a
read/write solely to test it. A cleared memory value does not reset runtime; that requires another
exact preview and approval. If synchronization is declined, report the changed values as
memory-only; never imply that the runtime adopted them.

After rejection:

```bash
atl profile suggestion reject --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash>
```

Reject stores only the hash, never evidence or content. Remove the temporary directory after either
decision. Do not retry exit 5/8 by regenerating hashes without a new review.

## Revalidate schema knowledge

1. Choose and state an absolute cutoff, then load only the relevant service:

   ```bash
   atl profile revalidation status --stale-before <RFC3339> --service jira
   ```

2. Propose the exact Jira field/Confluence space metadata reads needed for stale, missing, or failed
   entries. Wait for approval. Do not fetch issue/page bodies for schema revalidation.
3. Encode approved results as `verified|missing|failed` with the current `base_profile_hash`, one
   explicit `checked_at`, per-check source, and a sanitized error for failures. Never store tokens,
   raw backend responses, URLs, or sampled content.

   ```json
   {
     "schema_version": 1,
     "base_profile_hash": "<current-profile-hash>",
     "checked_at": "2026-07-10T12:00:00Z",
     "jira_fields": [
       {
         "id": "customfield_10001",
         "status": "verified",
         "name": "Risk Notes",
         "type": "string",
         "source": "approved field metadata read"
       },
       {
         "id": "customfield_10002",
         "status": "failed",
         "source": "approved field metadata read",
         "error": "sanitized failure summary"
       }
     ]
   }
   ```
4. Run:

   ```bash
   atl profile revalidate --from-file "$PRIVATE_TMP/checks.json" \
     --out "$PRIVATE_TMP/verified.atl-observations.json"
   ```

5. Explain missing/failed results. They remain private check state and never delete the last
   verified fact. Route verified observations through `suggest → review → apply|reject`; until
   apply, status is `verified_pending`.

## Efficient runtime memory

- Preferences: `atl profile show --section preferences`.
- One service's fields/spaces: `--section schema --service jira|confluence`.
- One service's selectors: `--section selectors --service ...`.
- Team policy: load only when a task needs policy decisions.
- Revalidation status: load only when a fact is stale/missing/failed or before changing field
  configuration.

Do not load `--section all` as a routine session preamble.
