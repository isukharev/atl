<!-- Generated from skills-src/onboarding/reference/stages.md — edit the source and run 'make gen-plugins'. -->
# Reusable onboarding stages

Use these stages in order. A team-owned onboarding skill should reuse them by invoking or pointing
to the atl onboarding skill, supplying its declared seed/defaults, and adding team-specific
questions. It should not copy the full workflow or skip consent.

## 1. Readiness

- Confirm CLI, backend configuration, authentication source, and current profile presence.
- Delegate missing technical setup to `/atl:setup`.

## 2. Workflow discovery

Ask only questions that affect a saved choice:

- Jira, Confluence, or both?
- Frequent one-off reads, bulk/offline analysis, edits, or all three?
- Which issue/page groups recur?
- Which fields or sections are essential for reading and editing?
- Preferred external mirror root?
- Is there a named team policy/onboarding source?

Reflect the proposed preferences back and obtain confirmation.
Resolve a selected mirror location to a canonical absolute path before storing it; never evaluate
profile text as shell syntax.

## 3. Consent-gated evidence

Propose at most a few representative resources or bounded queries. For each, state the exact
identifier/query and the fact it should reveal. Read only approved items, using transient Jira views
and narrow fields where possible. Do not turn observed behavior into a preference or policy.

## 4. Declared team composition

Accept team defaults only from an explicit source selected by the user. Record the source label or
version. Distinguish mandatory rules from suggested defaults; ask the user when the source is
ambiguous. Never infer policy from issue/page content.

## 5. Candidate and review

Create a `0700` private temporary directory and a `0600` candidate within it. Build one versioned
candidate, validate it with `atl profile preview`, and present the complete normalized candidate
plus changes. Approval is for the exact candidate/current hashes. Clean the directory on decline,
error, interruption, or success.

## 6. Explicit writes

Apply the profile with the preview hashes. Treat `render_defaults` and `preferences.mirror_root` as
memory until separately synchronized. Compare them with `atl config show`, preview exact render
config commands and the chosen mirror mechanism (`--into`, current-session `ATL_MIRROR_ROOT`, or a
shell-profile handoff), and obtain separate approval. Surface conflicts between active and saved
roots instead of choosing one; expand `~` before using a root. Verify effective render from the
relevant global/target-mirror context and an environment-backed mirror with `atl config show`; for
explicit `--into`, verify the resulting command's root/path instead, or mark verification pending
until the first approved mirror operation. Do not read the backend or write files only to verify a
future parameter. A cleared memory preference does not reset runtime without another reviewed
action. Separately preview workspace-guidance edits; generic guidance must not contain private root
values and is not itself runtime sync. Never leave the candidate behind after any terminal outcome.

## 7. Verification and gaps

Read back only needed sections. Report provenance, verification timestamps, approved writes,
memory-only preferences that remain unsynchronized, and unknowns. Do not fill gaps by guessing.
