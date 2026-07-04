# The dev loop: driving a ticket from a coding agent

The recipe for the core `atl` scenario — you are implementing a change in a code repo and keep
Jira (the ticket) and Confluence (the design/runbook page) current **as you work**, instead of
batch-updating them afterwards. Command details live in the `jira` and `confluence` skills; this
is the sequence and the safety rails.

Throughout: Jira bodies are **wiki markup** (`jira` skill → wiki-markup reference), Confluence
bodies are **CSF** (`confluence` skill → csf-authoring reference). Never Markdown.

## Phase 1 — start the ticket

```bash
atl jira issue get PROJ-123                                   # read scope, links, comments
atl jira issue assign PROJ-123 --me                           # take it
atl jira transitions --key PROJ-123                           # discover the workflow…
atl jira issue transition PROJ-123 --to "In Progress"         # …then move it
```

Ground the work locally:

```bash
atl jira pull --jql 'key = PROJ-123' --into ~/.atl/<workspace>/
atl jira issue refs PROJ-123          # artifact links (pages, PRs, docs) referenced by the issue
atl conf pull --id <page-id> --assets --into ~/.atl/<workspace>/   # the linked spec/design page
```

Read the pulled `.md` views for context; keep the mirror as your grounding set while coding.

## Phase 2 — during development

**Progress comments** when something material happens (design decision, blocker, scope change) —
not for every commit. Compose in wiki markup, reference code by URL:

```bash
cat > /tmp/progress.wiki <<'EOF'
Implemented the retry path in [PR #42|https://github.com/org/repo/pull/42].
Open question: do we cap retries per host or globally? Leaning per-host.
EOF
atl jira issue comment add PROJ-123 --from-file /tmp/progress.wiki
```

**Description stays truthful.** If scope changed, update it — re-`get` first (Jira has **no
version gate**; last writer wins):

```bash
atl jira issue get PROJ-123                    # confirm nobody edited it meanwhile
atl jira issue update PROJ-123 --from-file PROJ-123.description.wiki
```

**Blocked?** Say so where it's visible:

```bash
atl jira issue link add PROJ-123 --to PROJ-99 --type blocks   # check `atl jira link-types` first
atl jira issue transition PROJ-123 --to Blocked --comment "Waiting on PROJ-99"
```

## Phase 3 — finish

1. **Gate**: required fields populated before the workflow lets you through?
   ```bash
   atl jira issue check PROJ-123 --require fixVersions,components   # exit 8 = fix fields first
   ```
2. **Close the ticket** with the evidence attached:
   ```bash
   atl jira issue comment add PROJ-123 --from-file closing-note.wiki   # what shipped + PR link
   atl jira issue transition PROJ-123 --to Done --field 'resolution={"name":"Fixed"}'
   ```
3. **Update the living doc** — the Confluence page that described the design/runbook:
   ```bash
   atl conf pull --id <page-id> --into ~/.atl/<workspace>/   # fresh base right before editing
   # edit the .csf (only the .csf) — update the section that the change affects
   atl conf validate <…>/<page-slug>.csf
   atl conf push --dry-run <…>/<page-slug>.csf               # review diff + removed_fragments
   atl conf push <…>/<page-slug>.csf                         # version gate protects you
   ```
   On **exit 5** (the page moved while you worked): re-pull, reconcile, re-push. **Never
   auto-`--force`** — that clobbers someone's edit; a human decides.

## Rails (the short list)

- Re-`get` immediately before every Jira `update` — no version gate, last-writer-wins.
- Discover before writing: `transitions` before `transition`, `field-options` before `--field`,
  `link-types` before `link add`.
- Object-typed `--field` values are JSON: `resolution={"name":"Fixed"}` — a bare string fails.
- Pull fresh right before editing a Confluence page; push the exact bytes you dry-ran.
- Comment when there is signal (decision, blocker, done), not noise (every commit).
- Exit codes are the protocol: `5` re-pull & reconcile, `8` fill required fields, `7` run
  `/atl:setup`, `3` re-auth. Full table: [exit-codes.md](exit-codes.md).
