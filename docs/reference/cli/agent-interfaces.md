# Agent interfaces

Offline capability routing, MCP serving, and profile review/apply contracts.

[Reference index](README.md) · [Documentation home](../../README.md)

<!-- reference-navigation:start -->
## Navigate this reference

- [Offline agent capability catalog](#offline-agent-capability-catalog)
- [`atl mcp serve`](#atl-mcp-serve)
- [`atl profile`](#atl-profile)
- [Preview and apply](#preview-and-apply)
- [Context-efficient reads and guidance](#context-efficient-reads-and-guidance)
- [Consent-gated suggestions](#consent-gated-suggestions)
- [Explicit schema revalidation](#explicit-schema-revalidation)
<!-- reference-navigation:end -->

## Offline agent capability catalog

`atl capabilities` maps a closed exact task class to a small ordered command
route. It loads no config or credentials, makes no network request, and skips
self-update, so agents can use it before broad help/skill discovery:

```bash
atl capabilities --task jira/evidence
atl capabilities --task jira/setup -o text
atl capabilities --task jira/graph-evidence -o text
atl capabilities --task confluence/edit -o text
atl capabilities --task jira/portfolio -o id
atl capabilities --task jira/board-portfolio -o text
atl capabilities --task jira/batch-analysis -o text
atl capabilities --task jira/structure-planning -o text
atl capabilities --task jira/edit -o text
atl capabilities --task jira/mirror -o text
atl capabilities --task confluence/attachment-discovery -o text
atl capabilities --task confluence/table-analytics -o text
atl capabilities --task confluence/comments -o text
atl capabilities --task confluence/mirror -o text
atl capabilities --task confluence/space-hierarchy -o text
atl capabilities --task knowledge/search -o text
atl capabilities --id confluence.page.section
```

Supported task classes are `confluence/attachment-discovery`,
`confluence/comments`, `confluence/edit`,
`confluence/evidence`, `confluence/mirror`, `confluence/space-hierarchy`,
`confluence/table-analytics`,
`jira/batch-analysis`, `jira/board-portfolio`, `jira/edit`, `jira/evidence`,
`jira/graph-evidence`, `jira/inverse-reference`, `jira/mirror`, `jira/portfolio`, `jira/setup`,
`jira/structure-planning`, and `knowledge/search`. Exact `--service` and `--access
read-only|mutating` filters can narrow the result. An unknown task or capability
id exits 4; an invalid service/access value exits 2. No fuzzy classification is
performed.

The following named machine-readable list is the published task-class
contract. The documentation freshness guard compares this exact block with
`capability.TaskClasses()`; unrelated skill workflow classes are not part of
the comparison.

```json capability-task-classes
[
  "confluence/attachment-discovery",
  "confluence/comments",
  "confluence/edit",
  "confluence/evidence",
  "confluence/mirror",
  "confluence/space-hierarchy",
  "confluence/table-analytics",
  "jira/batch-analysis",
  "jira/board-portfolio",
  "jira/edit",
  "jira/evidence",
  "jira/graph-evidence",
  "jira/inverse-reference",
  "jira/mirror",
  "jira/portfolio",
  "jira/setup",
  "jira/structure-planning",
  "knowledge/search"
]
```

Attachment discovery and space hierarchy are singleton task classes. They
route respectively to `conf attachment search` (and its narrower typed MCP
mapping) and CLI-only `conf space tree`; neither is folded into the broader
`confluence/evidence` route.

For `jira/evidence`, the ordered route starts with `jira issue search` for
broad candidate discovery before exact per-issue field qualification and
bounded expansion.

`jira/structure-planning` returns separate routes for hierarchy rows, an
explicit per-row Structure value matrix, and transient issue export. The value
operation remains read-only even though the Structure API carries that query
payload over HTTP POST.

`jira/portfolio` includes `jira structure get` as the qualification step for an
exact Structure id before folder discovery or a bounded view. On the typed MCP
surface, `jira_structure_get` narrows that result to id, name, and read-only
state and omits owner, permission, saved-view, and forest transport payloads.

`jira/edit` includes complete worklog listing and the single-entry add as a
bounded pair. The add previews by default, applies only an exact reviewed
proposal hash, and reconciles an ambiguous POST through one read without
replaying the write.

`confluence/comments` is an additive four-step route: qualified list discovery,
one exact thread expansion, read-only guarded preview, and guarded add. The
catalog maps list/thread to the narrower read-only MCP tools described below;
preview/add are explicitly CLI-only and the route grants no write authority.

JSON uses schema version 1:

```json
{
  "schema_version": 1,
  "routing": {
    "match": "exact",
    "reference_load": "invoke capability.skill, then open capability.reference relative to that skill; do not search the filesystem",
    "stop": "stop expanding the route when sufficient complete evidence is available"
  },
  "selection": {"task": "jira/evidence", "count": 4},
  "capabilities": [{
    "id": "jira.epic.digest",
    "task_class": "jira/evidence",
    "service": "jira",
    "role": "primary",
    "priority": 20,
    "summary": "Collect bounded multi-source evidence for one epic and period",
    "command": "jira epic digest",
    "cli_command": "jira epic digest",
    "mcp_tool": "jira_epic_digest",
    "mcp_scope": "Bounded digest with explicit include sources; no Confluence expansion.",
    "cli_only": false,
    "access": "read-only",
    "effect_profile": "remote-read-capped",
    "output_modes": ["json", "text"],
    "evidence": "qualified",
    "completeness": "per-source",
    "skill": "jira",
    "reference": "reference/evidence-workflow.md"
  }]
}
```

The transport and `effect_profile` fields are additive within schema version
1. `command` remains a compatibility alias for `cli_command`. `mcp_tool` names a reviewed bounded
typed route only inside `mcp_scope`; it does not promise full CLI output or
workflow equivalence. CLI-only entries omit `mcp_tool` and `mcp_scope` and
set `cli_only:true`.

`access` is derived from the CLI's reviewed process-wide policy inventory:
`mutating` commands are refused by `ATL_READ_ONLY=1`; `read-only` means no
remote mutation (some reads such as `pull` intentionally write local mirror
artifacts). `output_modes` is derived from the same command-tree preflight used
at execution. `effect_profile` is derived from that command's canonical static
effect owner rather than from the curated task route. CI verifies that every
catalog command exists and these facts do
not drift. The catalog describes safe routing only; it never grants approval to
execute a mutating entry.

### Static command effects

Inspect the complete executable-command effect catalog offline, or select one
exact leaf:

```bash
atl capabilities --effects
atl capabilities --effects --command "jira issue search"
atl capabilities --effects -o id
```

`--command` requires `--effects`. Effect inspection cannot be combined with
the curated `--task`, `--service`, `--access`, or `--id` filters. Like ordinary
capability inspection, it loads no configuration or credential and performs no
network request or self-update.

The schema-v1 effect result is
`{schema_version,enforcement,selection,profiles,commands}`. `enforcement` is
always `informational`. Every one of the 171 executable leaves has exactly one
`effect_profile` in the canonical command registry, and a newly constructed
leaf fails startup validation until classified. Each command row includes its
path, access class, output modes, optional mutation profile, and any curated
capability ids that reference that same command owner.

Each profile is a static upper bound across successful invocations of that
leaf; flags and inputs may narrow the effects of one invocation but cannot
widen the published profile.

Profiles use closed dimensions:

- `remote_effect`: `none|read|write`;
- `local_effect`: `none|read|write|download`;
- `credential_access`: `none|possible|required`;
- `network_bound`: `none|fixed|caller|required_internal_cap|unknown`;
- `process_effect`: `none|launch`;
- `replay_class`: `replay_safe|non_replay_safe|mixed`;
- `output_kind`: `data|generator|prose|protocol`;
- additive `local_artifact`: `none|possible|required`, `configuration`:
  `none|read|write`, and `self_update`: `disabled|possible`.

`unknown` is deliberate: it does not imply that caller input controls the
number of backend requests. `none` makes no backend request, `fixed` has a
statically fixed request plan, `required_internal_cap` bounds a data-dependent
request loop with a mandatory implementation-owned ceiling, and `caller` means
the caller supplies the physical-request budget, not merely a target. In the
remote/local dimensions, `write` is the dominant possible effect and subsumes
any preparatory reads. `output_kind` describes
stdout (or the stdio protocol), while downloads and durable files remain local
effects and `local_artifact` facts. Base remote/local effects exclude the
best-effort startup updater; when `self_update` is `possible`, that updater can
add its separately documented check, download, and local replacement before
the command runs. These facts never authorize execution and do not replace the
existing read-only or guarded-write enforcement paths.

Credential access describes material, not merely opening a credential-store
file: `none` never handles credential bytes, `possible` handles them only on
some successful branches, and `required` means every successful invocation
necessarily reads or processes credential material. Thus credential status and
idempotent logout are `possible`, while a successful authenticated backend read
is `required`.

## `atl mcp serve`

Run the typed read-only agent tool surface over MCP stdio:

```bash
atl mcp serve
atl mcp serve --service jira
atl mcp serve --service confluence
atl mcp serve --service offline
```

The default process registers twenty-four explicit Jira/Confluence evidence tools and no
mutation, shell, arbitrary-file, mirror-write, or raw-REST tool. Two no-argument
tools inspect only an explicit valid `ATL_MIRROR_ROOT`, offline, and return
content-free mirror health counts. Stdout is
reserved for protocol frames, startup skips self-update, and tool errors expose
the same stable `kind`/`remediation` classes and closed `recovery` object as CLI
JSON. Install through the
Claude Code/Codex plugin or see [mcp.md](../../mcp.md) for the exact tools, bounds,
standalone Codex config, and CLI fallback guidance.

The server is dual-era. Modern `2026-07-28` clients use stateless
`server/discover` and list tools without initialize; legacy `2025-11-25`
clients retain initialize/initialized. Unsupported future versions fail with
structured requested/supported version data. The closed one-page `tools/list`
result has no cursor and always carries `ttlMs:0` plus
`cacheScope:"public"`.

Committed Claude Code and Codex plugin definitions add hidden generated
`plugin-interface-contract` and `plugin-product-version` startup markers. The
interface marker is the compatibility gate: an incompatible marked invocation
is a value-free usage failure (exit `2`) before config, credentials, dependency
construction, or network access. The product marker is evaluated separately as
`match` or `mismatch` and never rejects a compatible interface, but the startup
gate does not emit that status at runtime. Compare `atl version` with the
installed plugin or manifest version when diagnosing skew. A bare
standalone invocation, or an indistinguishable older unmarked plugin, remains
supported with both facts `unverified`. A newer marked plugin used with an old
binary fails through that binary's ordinary unknown-flag path; no symmetric
old-plugin/new-binary rejection is claimed.

Generated definitions also set exactly
`CODEX_MCP_PROTOCOL_VERSION=2026-07-28`. On Codex 0.147 that per-server marker
and the user-controlled under-development global `mcp_2026_07_28` feature are
both required for modern mode; either alone remains legacy. The plugin cannot
enable the global feature. The marker selects client protocol behavior and is
not identity, authentication, or provenance evidence.

Omitting `--service` preserves the complete twenty-four-tool inventory and existing
instructions. The closed Jira/Confluence/offline profiles expose 11/13/2 tools;
`offline` contains only the two no-argument mirror snapshots and
constructs no backend reader. Unknown or repeated service selections fail
before dependency construction. All profiles also publish one fixed
`application/json` resource, `atl://capabilities`, containing only static
capability ids, ordering, CLI routes, bounded MCP mappings/scopes, and CLI-only
facts. Reading it loads no config, credentials, backend, mirror path, or user
content.

`confluence_page_meta` is the body-free governance read: it returns only
schema/page identity, title, space, a positive version, an optional update
stamp, and explicit `restricted`, `unrestricted`, or `unknown` state. It has a
fixed 32 KiB encoded-result cap and omits URLs, labels, ancestors, restriction
principals, page content, and arbitrary backend metadata.

`confluence_comment_list` is body-free comment discovery for one positive
canonical page id. `confluence_comment_thread` expands one exact positive
canonical comment id from that inventory as bounded plain text. Bind either
read with `expected_page_version` when the page id/version came from earlier
evidence; omission is explicitly ungated. Both have a fixed 32-comment-page
cap, a selectable 1..1000 item bound, and a selectable 1 KiB..1 MiB encoded-result
bound, preserve completeness
qualification, and return only minimized comment
facts. Partial output never proves absence. Neither tool returns raw CSF,
inline-selection text, dedicated URL fields, email-like author identity, or
arbitrary backend error prose, and neither can preview or add a comment. Thread
`body_text` remains untrusted user-authored evidence and may contain ordinary
links or email text.

`jira_issue_refs` is the summary-only reference read: pass exactly one issue
`key`, or bounded `jql` with `limit` from 1 through 25. Up to eight exact
technical field ids may add qualified reference sources. The result preserves
selection, per-source completeness, per-issue and top-level counts, kind
buckets, and reconciliation facts, but omits raw reference URLs, issue
summaries/types, and source text. JQL mode performs one paginated comment
listing per emitted issue, so backend traffic scales with the selected limit.
Use the CLI `jira issue refs` when the URLs themselves are required evidence.

## `atl profile`

Store compact private workflow memory separately from credentials, mirrors, and
workspace guidance. The profile lives at `ATL_CONFIG_DIR/profile.json` (normally
`~/.config/atl/profile.json`), is written atomically with mode `0600`, and has five
deliberately separate sections:

- `schema`: Jira field and Confluence space facts with source + verification time;
- `preferences`: human-confirmed services and mirror choice;
- `team_policy`: explicit rules with declared provenance (never inferred);
- `render_defaults`: the agreed render shape (it does not silently rewrite config);
- `selectors`: named reusable JQL/CQL, without sampled issue/page content.

The profile may contain private field names and selectors even though it contains
no credentials. Never commit or publish it.
All `profile` commands are local/offline and skip the synchronous bounded
self-update check, so preview performs no network or unrelated filesystem
mutation.

## Preview and apply

Every write is a two-phase optimistic operation:

```sh
PRIVATE_TMP="$(mktemp -d)"       # verify mode 0700
CANDIDATE="$PRIVATE_TMP/profile.json"  # create/write with mode 0600
atl profile preview --from-file "$CANDIDATE"

atl profile apply --from-file "$CANDIDATE" \
  --candidate-hash <candidate_hash> \
  --expected-current-hash <current_hash>
```

Remove the private temporary directory on approval decline, error,
interruption, or success; never use a predictable shared `/tmp` filename.

`preview` strictly validates schema version 1, rejects unknown keys, normalizes
unordered lists, and returns the complete normalized candidate plus per-section
`added|removed|changed|unchanged` status. It never writes. `apply` requires both
exact hashes: a modified candidate fails with exit 8; a current profile changed by
another actor fails with exit 5. Concurrent cooperating applies are serialized by
an owner-only advisory lock. Apply also repairs a semantically identical profile
back to mode `0600` if it was restored with permissive permissions.

Candidates must use schema version `1`. An ordinary `show` rejects an unsupported
stored version, but `preview` may treat syntactically valid future-version bytes
as opaque state: it reports `migration_from_schema_version` and a raw current
hash. The same guarded apply can then replace those exact bytes with an approved
version-1 candidate without interpreting unknown fields.

## Context-efficient reads and guidance

```sh
atl profile show
atl profile show --section all
atl profile show --section preferences
atl profile show --section schema --service jira
atl profile show --section render_defaults --service confluence
atl profile show --section selectors --service confluence
atl profile guidance -o text
```

`show` returns metadata `{exists,path,hash}` by default. Use an explicit `--section`
and optional `--service` for `schema`, `render_defaults`, or `selectors` to load
only one backend's data; `--section all` is the deliberate full-profile escape
hatch. Service-scoped render reads return only the selected `jira` or
`confluence` object (`null` means no saved memory for that service, independent
of its sibling). They remain memory: neither `show` nor suggestion apply changes
active render config.
`guidance` emits only a short generic instruction pointing agents to those slices;
it never embeds fields, selectors, policy rules, or sampled content. The optional
`onboarding` client skill performs the consent-gated interview and preview/apply
flow. Saved `render_defaults` and `preferences.mirror_root` are memory, not active
runtime. The onboarding/learning flow compares them with `atl config show` and
requires separate approval for `atl config set render.* ...`, current-session
`ATL_MIRROR_ROOT`, explicit `--into`, or a shell-profile handoff. Declined sync is
reported as memory-only; conflicts between active and saved roots require a choice,
and shell/workspace files are never edited implicitly. Effective local render is
verified by running `atl config show` from the target mirror root; an explicit
`--into` is verified from the next approved command result's root/path, never by
causing a read/write solely for verification. Newly captured mirror paths are
canonical absolute values and are passed as one shell-quoted argument; a legacy
leading `~` is expanded without `eval`. Clearing a profile preference removes only
memory and never resets runtime implicitly. Generic workspace guidance retains
this approval protocol but never embeds the private root itself.

## Consent-gated suggestions

Later sessions can propose memory changes without silently mutating the profile.
The caller creates a version-1 observations file in a private directory. It must
name the exact current `base_profile_hash`; schema facts carry their own source
and verification time, while preference/render/selector proposals require an
`evidence` item. There is deliberately no `team_policy` key—strict decoding
rejects attempts to infer policy.

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
    "reason": "user confirmed this recurring workflow"
  }]
}
```

```sh
atl profile suggest --from-file "$PRIVATE_TMP/observations.json" \
  --out "$PRIVATE_TMP/learning.atl-suggestion.json"

atl profile suggestion review --from-file "$PRIVATE_TMP/learning.atl-suggestion.json"

# approve the exact three hashes returned by review
atl profile suggestion apply --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash> \
  --candidate-hash <preview.candidate_hash> \
  --expected-current-hash <preview.current_hash>

# or reject that exact artifact
atl profile suggestion reject --from-file "$PRIVATE_TMP/learning.atl-suggestion.json" \
  --suggestion-hash <suggestion_hash>
```

`suggest` is deterministic for the same normalized observations + base profile.
It writes only the explicitly selected mode-0600 suggestion under a mode-0700
parent; the required `.atl-suggestion.json` suffix cannot collide with profile,
credential, or state filenames even if that private parent is the ATL config
directory. Parent mode validation and atomic rename use one held directory
handle. `profile.json` is untouched. `review` is read-only and returns evidence
plus the ordinary complete profile preview. `apply` is the confirmation that
turns proposed preferences into `confirmed:true`. `reject` stores only a bounded
recent window of suggestion hashes in owner-only decision state—never evidence,
selectors, or sampled content—so an identical proposal still in that window reports
`previously_rejected:true`. Delete the temporary observation/suggestion files
after either decision.

Observation objects are partial: omitted preference fields preserve their
current values, and a Jira-only/Confluence-only `render_defaults` proposal
preserves the other service. An explicit empty value clears only that named
preference/service value. Schema facts and selectors are upsert-only; removals
require the ordinary full profile preview flow. Suggestion apply updates only the
private profile; changed render/mirror preferences still require the same separate
runtime comparison, approval, and verification as initial onboarding.

## Explicit schema revalidation

Staleness uses a caller-selected absolute cutoff, never the wall clock hidden
inside the CLI:

```sh
atl profile revalidation status \
  --stale-before 2026-04-01T00:00:00Z \
  --service jira
```

The result classifies relevant facts as `fresh`, `stale`, `verified_pending`,
`missing`, or `failed`. After the user approves exact metadata reads, encode the
results (`verified|missing|failed`) in a version-1 revalidation batch carrying
the current profile hash and one explicit `checked_at`, then run:

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

```sh
atl profile revalidate --from-file "$PRIVATE_TMP/checks.json" \
  --out "$PRIVATE_TMP/verified.atl-observations.json"
```

Revalidation stores a bounded, newest-first-per-service set of check outcomes
in private owner-only state and
emits only successfully verified facts as a normal observations artifact.
The output name must end in `.atl-observations.json`, the corresponding reserved
non-state suffix.
Failure summaries reject control characters, redact URLs, hostnames, and IP
addresses, and are length-capped before persistence. Missing/failed checks never
delete or overwrite the last verified profile fact.
Feed the observations through `suggest → review → apply|reject`; until apply,
new facts appear as `verified_pending`. Backend reads are performed by the
calling agent only after consent—these profile commands are local/offline.

---
