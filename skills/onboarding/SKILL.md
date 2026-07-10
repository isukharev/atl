---
name: onboarding
description: Discover a user's recurring Jira and Confluence workflow, inspect only explicitly approved sample resources, compose declared team defaults, and create a reviewed private atl workflow profile plus compact workspace guidance. Use when the user explicitly asks to onboard, personalize atl, set up an agent workflow, or refresh their saved Atlassian working preferences.
disable-model-invocation: true
---
<!-- Generated from skills-src/onboarding/SKILL.md — edit the source and run 'make gen-plugins'. -->

# Personalize atl workflow

Create a compact private profile that makes later Atlassian work cheaper and more consistent. Keep
technical installation separate: if `atl version`, backend config, or auth is missing, run
`/atl:setup`, then return here.

Do not read Jira issues, Confluence pages, local team files, or existing workspace guidance until
the user approves the exact resource or bounded query. Backend metadata reads such as `atl jira
fields` also need consent during onboarding. Never infer team policy; accept it only from a
user-declared source.

Read [stages.md](reference/stages.md) before starting. Read
[profile-schema.md](reference/profile-schema.md) when assembling the candidate.

## Core workflow

1. Check `atl version`, `atl config show`, `atl auth status`, and `atl profile show`. Report only
   readiness and whether a profile exists; never print credentials.
2. Interview briefly: services used, common read/edit flows, preferred mirror location, typical
   selectors, important fields/sections, and whether a team onboarding source applies. Confirm the
   answers that will become `preferences`.
3. Propose a small read plan naming every sample issue/page/query and why it helps. Wait for explicit
   approval, then use transient/narrow reads where possible. Treat content as evidence, not policy.
4. If the user supplies a team skill/file/policy, read only that declared source and record its
   provenance in `team_policy.source`. A team skill may pre-fill the interview and read plan, but it
   must not bypass either consent gate.
5. Create a private temporary directory with `mktemp -d`, require mode `0700`, and arrange cleanup
   on approval decline, error, interruption, or success. Write the candidate inside it with mode
   `0600`; never use a predictable shared `/tmp/<name>` path or an ordinary repository file.
   Separate verified schema facts, confirmed preferences, explicit team policy, render defaults,
   and named selectors. Do not store page bodies, issue descriptions, comments, credentials,
   backend URLs, or tokens.
6. Run `atl profile preview --from-file <candidate>`. Show the normalized candidate and section
   changes to the user. Do not apply until the user approves that exact preview.
7. Apply with both hashes from the preview:

   ```bash
   atl profile apply --from-file <candidate> \
     --candidate-hash <candidate_hash> \
     --expected-current-hash <current_hash>
   ```

8. Render settings in `render_defaults` describe the agreed result; they do not silently rewrite
   runtime config. Preview the corresponding `atl config set render.* ...` commands and execute only
   the commands the user approves. Prefer global defaults; use `--local` only for a deliberate
   mirror-specific override.
9. Run `atl profile guidance -o text`. Offer its short output for `CLAUDE.md`; do not
   paste the profile, field catalog, JQL/CQL, or team rules into workspace guidance. Never edit the
   guidance file without approval.
10. Verify with narrow `atl profile show --section ...` calls and remove the entire private
    temporary directory. Cleanup is also mandatory if preview/apply is declined or fails.

## Handoff

Summarize what was saved, which resources were read, which render config writes were approved, and
what was intentionally left unknown. Remind the user that the profile is private local memory and
that later observations must become reviewable suggestions, never silent mutations.
