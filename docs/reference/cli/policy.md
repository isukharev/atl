# Scoped write policy

`atl` can freeze a local, content-scoped policy at process startup and apply it
to Jira and Confluence writes. Reads, local mirror edits, authentication,
configuration, profile commands, and self-update are outside this policy; use
the global read-only policy when those surfaces must also be blocked.

[CLI reference](README.md) · [Safe writes](../../safe-writes.md) · [Output contract](../output/common.md)

## Discover the effective policy

Run this before planning a write:

```sh named-policy-discovery
atl policy show
atl policy explain --service jira --verb update --key DOC-7
```

Both commands are offline. They load no credential, make no backend request,
and remain available under `ATL_READ_ONLY=1`.

`policy show` reports:

- whether a policy is active and which non-path source supplied it;
- the per-layer SHA-256 digest and conservative effective `grants`;
- the intersection with global read-only mode;
- which Jira, Confluence, read, local-command, and local-mirror surfaces are
  governed;
- `advisory_because`, including a missing digest pin or backend binding.

The nested `read_only.active` field retains its legacy effective meaning, and
legacy `read_only.source` remains the active source or `null` when inactive.
`read_only.configured_read_only`, `read_only.effective_read_only`, and
`read_only.read_only_source` make the configured value, monotonic process value,
and its highest-precedence source explicit. The source vocabulary is
`flag|environment|configuration|none`, with flag before environment before
configuration. This is an informational projection of the existing global
preflight, not content-policy enforcement.

`enforcement` is `advisory` or `sealed_unverified`; it never claims that an
in-process policy is an independently enforced security boundary.

`policy explain` accepts `--service`, `--verb`, and optional canonical target
fields: `--kind`, `--id`, `--project`, `--key`, `--space`, and `--under`.
Its decision is `allow`, `deny`, or `conditional`. Conditional means the
offline target lacks backend-canonical selector data; it is never reported as
an allow. For example, a Confluence space or ancestor rule may remain
conditional until the adapter resolves the numeric page id.

## Policy sources and freezing

The managed process layer is one of:

- `ATL_POLICY`: inline JSON; or
- `ATL_POLICY_FILE`: a strict local JSON file.

They are mutually exclusive. An optional user layer at
`$ATL_CONFIG_DIR/policy.json` is conjoined with the managed layer: every active
layer must allow the operation, so adding a layer cannot widen another layer.
The resolved value and any load failure are sticky for the process. Editing a
file does not alter a running `atl mcp serve` or command; restart the process.

For a file source, `ATL_POLICY_SHA256=sha256:<64 lowercase hex digits>` pins the
exact file bytes. It deliberately does not apply to inline JSON. Set
`ATL_POLICY_REQUIRED=1` to deny governed writes when no policy is active. In
required mode the relevant backend binding is also mandatory.

Files are opened without following symlinks, capped at 64 KiB, required to be
regular and owner-only on POSIX, checked against the process uid, and decoded
with unknown fields, duplicate keys, trailing values, and unknown vocabulary
rejected. Read errors fail closed.

## Schema

The v1 document contains ordered rules and optional backend origin bindings:

```json named-policy-example
{
  "schema_version": 1,
  "backend": {
    "jira_sha256": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  },
  "rules": [
    {
      "id": "docs-updates",
      "effect": "allow",
      "verbs": ["update", "comment"],
      "resource": {"service": "jira", "project": "DOC"}
    },
    {
      "id": "protect-release-page",
      "effect": "deny",
      "verbs": ["delete", "move"],
      "resource": {"service": "confluence", "under": "7001"}
    }
  ]
}
```

`effect` is `allow` or `deny`. The leaf verbs are `create`, `update`,
`comment`, `transition`, `move`, and `delete`; `write` is shorthand for
`create`, `update`, and `comment`, while the other verbs remain explicit. A
resource selector is a conjunction of exact values. Each key accepts one
string or a string array:
`service`, `kind`, `project`, `key`, `space`, `id`, and `under`. Patterns and
wildcards are unsupported.

Each layer defaults to deny. Explicit deny wins independently of rule order or
specificity. Compound operations require every verb on every target: a Jira
link update checks both issues, a Confluence move checks source and destination,
and a transition with a comment checks both verbs. Jira field updates that
relocate an issue require both `update` and `move`.

Backend values are tagged digests produced from the canonical configured
origin, never raw URLs. Required mode needs the binding for the backend being
written. A mismatch exits before credentials or a mutating request.

## Preflight and adapter enforcement

Every mutating CLI leaf declares its verb set and identity source in the
reviewed command registry. The process preflight uses only argv or bounded plan
data and may **deny only**. Missing canonical identity is not an allow: the
adapter resolves the exact backend target and performs the authoritative check
immediately before its single-attempt write. Under an active policy, mutating
Confluence references accepted by CLI write paths must be canonical numeric
ids; URLs and search-derived references cannot ground an allow.

`conf plan apply` checks the whole durable plan before configuration or
network, then rechecks each target at the adapter. A later authoritative denial
aborts with the existing partial-progress report; the operation is not
transactional. `jira issue plan apply --csv` marks conclusively denied rows
`blocked`; unresolved offline rows continue to the authoritative adapter gate.
Push commands likewise stop on the first target denied by the adapter.

Policy refusal is exit 8 with `kind:"content_policy"`,
`remediation:"request_human_approval"`, `policy:"content"`, a structured
`denial`, and recovery `retry_safe:false` unless a resolving read was
temporarily unavailable. Do not retry with rewritten wording or a different
reference.

## Boundary and sealing

No in-process mechanism survives an agent that controls `atl`'s environment
and argv. The only real seals are a launcher the agent cannot modify owning the
environment (an unprivileged filesystem sandbox such as Landlock can protect
the file), or keeping the credential outside the agent's reach. The digest,
required mode, strict file checks, and backend binding turn common accidental
bypasses into loud failures; they are not a credential broker.

The launcher must build a new environment and pin all inputs that can redirect
policy, configuration, credentials, transport, or a backend: `PATH`, `HOME`,
`USERPROFILE`, `XDG_CONFIG_HOME`, `ATL_CONFIG_DIR`, all Jira/Confluence URL and
PAT variants,
`ATL_UPDATE_URL`, `ATL_INTEGRATION` and its test PAT fallbacks, upper/lowercase
HTTP proxy variables, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `ATL_ALLOW_INSECURE`,
`ATL_READ_ONLY`, `ATL_MIRROR_ROOT`, `ATL_NO_UPDATE`, `ATL_UPDATE_DEBUG`,
`ATL_VERBOSE`, and all four `ATL_POLICY*` variables. Subtracting selected names
from an inherited environment is insufficient.

The current MCP server remains read-only and exposes no policy resource or
write tool. Any future write tool must use the same adapter guard; MCP
annotations are hints, not enforcement.
