<!-- Generated from skills-src/atl/reference/context-efficient-output.md — edit the source and run 'make gen-plugins'. -->
# Context-efficient CLI results

Use this reference when an ATL read may return more structured evidence than
the active decision needs. Smaller output is useful only when it keeps the
qualification that makes the evidence safe to use.

## Narrow in this order

1. **Choose the route once.** For unfamiliar work that is not already governed
   by a reviewed exact-command workflow, use
   `atl capabilities --task <exact-class> -o text` to select the command and
   one focused reference. The text table is a routing view. Use the default
   JSON only when the decision itself needs the omitted MCP scope, CLI-only
   boundary, output modes, or capability metadata.
2. **Reduce collection at the producer.** Scope the CQL/JQL, set a total row
   limit and cursor, and use command-native selectors such as Jira `--fields`
   or `--columns`, Confluence `--heading` or `--table`, and explicit byte
   bounds. This is the only stage that can also reduce backend work and
   content exposure.
3. **Use semantic projections.** Prefer `--metadata-only`, `--summary-only`,
   `--projection compact`, `--select`, or a content-free summary command when
   they match the question. These modes retain command-specific qualification
   that a generic filter can easily discard.
4. **Choose the output mode.** Keep JSON when a decision depends on
   completeness, pagination, version binding, reconciliation, warnings, or a
   recovery action. Use `-o text` for route selection or final human-readable
   content that needs no hidden machine state. Use `-o id` only when the exact
   identifiers are sufficient and completeness was established separately.
5. **Shape locally last.** A compact `jq` projection can reduce bytes entering
   the agent context, but it does not reduce the response ATL fetched.

Stop when the bounded result is sufficient. Do not re-read the same evidence
in another format merely to make it prettier.

Do not fetch a representative Jira issue or Confluence page merely to discover
the response shape: optional fields may be absent and the read exposes user
data. Use the command's focused output reference or the result-family map below.

## Qualification that projections must retain

| Result family | Keep before making a decision |
|---|---|
| Jira IssueList | `schema_version`, `source`, `selection`, `projection`, `page`, and required row identity/order/content: `rows[].id`, `key`, `position`, `values`, and source-specific `context` as applicable |
| Confluence outline, section, or table | identity and `version`; version-gate state; `complete`, `truncated`, and `partial_reason`; byte/count reconciliation; selected content |
| Jira field evidence | issue/update provenance, field identity and presence, `complete`, `truncated`, byte accounting, and `value` |
| Graph, aggregate, or inverse-reference evidence | top-level and per-source completeness, bounds, projection metadata, reconciled summaries, frontier/warnings, and the required facts |
| Failure or refusal | stderr JSON `kind`, numeric `code`, `remediation`, and closed `recovery`; stdout only when that command documents qualified evidence on a non-zero strict result |

Prefer ATL's emitted summaries and reconciliation booleans over recounting a
nested array. A smaller array without its page/source qualification can turn a
partial result into a false absence claim.

## Safe local JSON shaping

Use a shell projection only in a read-only workflow that permits ordinary
pipes. Preserve ATL's exit status and keep the qualification envelope:

```bash
export ATL_READ_ONLY=1
set -o pipefail
atl jira issue search --jql '<narrow JQL>' \
  --columns key,summary,status,updated --limit 10 |
  jq -c '{schema_version,source,selection,projection,page,rows:[.rows[]|{id,key,position,values,context}]}'
```

`jq -c` removes presentation whitespace; selecting fields can remove semantics.
For dynamic Jira field ids, use `.values[$field]` with `--arg field ...`
rather than guessed dot syntax. Hyphenated object keys require bracket syntax.

Do not add a pipe, redirection, `tee`, or another shell command to a reviewed
exact-command or guarded agent workflow: its command policy and evidence
capture are part of the contract. Follow that workflow's exact command and let
the client consume its bounded JSON directly. A pipe filters stdout only;
stderr remains visible. Without `pipefail`, a successful `jq` can mask an ATL
failure, including an intentional strict exit.

For a complete payload that must be reused, prefer a command's native file
export and retain any small receipt it returns. Otherwise use an owner-approved
private or ignored artifact only when persistence is intended, and read later
projections rather than printing the whole file. Both explicit artifacts and
client spill-files retain plaintext data and need the same privacy treatment.

Automatic truncation, spill-to-disk, and conversation compaction happen after
the command produced its result. Treat them as recovery mechanisms, not as a
substitute for bounding and projecting before output enters the agent context.
