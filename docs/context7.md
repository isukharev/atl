# Context7 documentation index

Context7 can index atl's public documentation so an agent can retrieve the
current CLI contract without cloning or opening this repository. The root
[`context7.json`](../context7.json) deliberately selects `docs/`, then excludes
maintainer-only, implementation, private, historical-plan, and generated plugin
content. Context7 always considers root-level Markdown even when `folders` is
narrow, so every root file except the user-facing `README.md` is listed in
`excludeFiles`. Context7 never receives Jira or Confluence mirror content
through this integration.

Registration is external state. The presence of `context7.json` does not by
itself prove that the library is registered or fresh; verify it before relying
on the index.

## Use atl through the Context7 CLI

Resolve the library first when its id is not already known:

```sh
npx ctx7@latest library atl "How does atl safely edit a Jira issue or Confluence page?"
```

Select the official `isukharev/atl` result, then query its exact id:

```sh
npx ctx7@latest docs /isukharev/atl "Show the current guarded Jira and Confluence edit workflows, including exit codes"
```

Use a concrete question rather than downloading the whole corpus. Resolve
again when an agent only has the name `atl`; do not guess among similarly named
libraries. In an MCP client the equivalent sequence is
`resolve-library-id("atl", question)` followed by
`query-docs("/isukharev/atl", question)`.

If the CLI reports a quota error, authenticate with `npx ctx7@latest login` or
provide `CONTEXT7_API_KEY` through the calling environment. Never place the key
in a prompt, repository file, command argument, or captured log.

## Register the library once

This is a maintainer operation performed after `context7.json` is present on
the default branch:

1. Open [Add a Library](https://context7.com/add-library), choose the GitHub
   tab, and select or enter the public `isukharev/atl` repository.
2. Submit it and wait for parsing to complete. If offered, claim ownership from
   the library/admin page so maintainers receive the owner refresh limits. The
   claim dialog provides library-specific `url` and `public_key` values. Add
   those two public values to the existing `context7.json` (do not replace its
   indexing scope or rules), merge them to `main`, then complete verification.
   The public key is an ownership verifier, not the private API key;
   `CONTEXT7_API_KEY` must remain a secret.
3. Verify both lookup and content:

   ```sh
   npx ctx7@latest library atl "atl CLI Jira Confluence Server Data Center"
   npx ctx7@latest docs /isukharev/atl "What is the default output format and how do sentinel errors map to exit codes?"
   ```

4. Confirm that the answer reflects the current `docs/usage.md` and does not
   expose implementation, generated skills, or any non-public content.

Do not close the tracking issue merely because the config merged. Registration
and the two successful verification queries are separate acceptance gates.

## Add or change indexed documentation

Put maintained runtime/user documentation under `docs/`. A normal merge to
`main` makes it eligible for the next parse unless its filename or directory is
excluded. Keep implementation details in code and private backend artifacts
outside the selected tree. When adding a new documentation category, review
`folders`, `excludeFolders`, `excludeFiles`, and the short `rules` list rather
than widening the index implicitly.

Context7 extracts explanations around code examples and may ignore a page that
has no code. Every selected atl document therefore needs at least one real,
non-empty fenced example with a language tag such as `sh`, `json`, `yaml`,
`text`, or `xml`. Do not add decorative or impossible commands merely to raise
the snippet count. Prefer a small task recipe that a user can run, followed by
the output shape or safety constraint it demonstrates. The curated
[agent recipes](agent-recipes.md) cover high-frequency workflows while
`usage.md` remains the exhaustive reference.

The index intentionally excludes `docs/proposals/` and `docs/superpowers/`:
they describe historical or prospective designs and can conflict with the
shipped CLI contract. Promote durable behavior into `docs/usage.md`,
`docs/OUTPUT_CONTRACT.md`, or another maintained reference before expecting an
agent to use it.

Before merging an indexed documentation change, run:

```sh
make check-context7-docs
```

The repository check fails when a root Markdown file would be indexed
implicitly without review or when a selected document lacks a non-empty named
fenced snippet. It does not replace Context7's parser or benchmark; it protects
the local scope and minimum snippet contract.

## How often Context7 updates atl

Context7 checks staleness when a library is requested. Its current documented
automatic thresholds depend on popularity:

| Popularity rank | Staleness threshold |
|---|---:|
| Top 100 | 1 day |
| Top 1,000 | 15 days |
| Top 5,000 | 30 days |
| Other libraries | 45 days |

The request that notices staleness starts a background refresh and still
returns the previously indexed documentation. An infrequently requested
library may therefore remain old until it is requested. These service-side
thresholds can change; check the official [library update
policy](https://context7.com/docs/library-updates) when freshness matters.

For a release or important CLI contract change, use one of the deterministic
paths instead of waiting:

- trigger **Refresh** from the library page while logged in; or
- call the owner refresh API; or
- after adding the repository secret `CONTEXT7_API_KEY`, install a GitHub
  Actions workflow that refreshes on pushes to `main`.

The minimal refresh request is:

```sh
curl --fail-with-body --silent --show-error \
  --request POST https://context7.com/api/v1/refresh \
  --header 'Content-Type: application/json' \
  --header "Authorization: Bearer $CONTEXT7_API_KEY" \
  --data '{"libraryName":"/isukharev/atl"}'
```

Keep the API key in an environment variable or GitHub Actions secret. The
official [GitHub Actions guide](https://context7.com/docs/integrations/github-actions)
contains the current workflow. This repository does not enable that workflow
until the library is registered and a maintainer deliberately configures the
secret; an absent secret must not turn every documentation push red.

## Validate `context7.json`

The file declares the official schema URL, so JSON-aware editors can validate
it directly. Before changing its shape, validate it against the current schema
and review what is selected:

```sh
jq empty context7.json
make check-context7-docs
curl --fail --silent --show-error https://context7.com/schema/context7.json | jq '.properties | keys'
```

Schema validity does not replace the post-index query check: Context7 parsing
and library registration are external operations with their own status.

After registration or a major documentation rewrite, run representative
queries rather than checking only that the library resolves:

```sh
npx ctx7@latest docs /isukharev/atl "Find Jira issues, paginate the JSON result, and explain partial results"
npx ctx7@latest docs /isukharev/atl "Safely edit a Confluence page through pull, apply, preview, and push"
npx ctx7@latest docs /isukharev/atl "Read a board and a Structure folder as JSONL for jq processing"
npx ctx7@latest docs /isukharev/atl "Set a large Jira custom field with review gates and no value in argv"
```

The returned snippets should contain runnable commands plus the relevant
write/partial-result warning. Library owners can then use Context7's Benchmark
tab to compare retrieval quality after scope or recipe changes.

Official references: [adding libraries](https://context7.com/docs/adding-libraries),
[configuration schema](https://context7.com/schema/context7.json), and
[keeping libraries fresh](https://context7.com/docs/library-updates).
