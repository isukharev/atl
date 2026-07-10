# Profile schema version 1

The private profile lives at `ATL_CONFIG_DIR/profile.json` (normally under the user's ATL config
directory), mode 0600. It may contain private names and selectors. Never commit or publish it.

```json
{
  "schema_version": 1,
  "schema": {
    "jira_fields": [
      {
        "id": "customfield_10001",
        "name": "Risk Notes",
        "type": "string",
        "source": "atl jira fields",
        "verified_at": "2026-07-01T12:00:00Z"
      }
    ],
    "confluence_spaces": [
      {
        "key": "ENG",
        "name": "Engineering",
        "source": "approved space lookup",
        "verified_at": "2026-07-01T12:00:00Z"
      }
    ]
  },
  "preferences": {
    "confirmed": true,
    "services": ["confluence", "jira"],
    "mirror_root": "/home/user/.atl/workspace"
  },
  "team_policy": {
    "source": "team onboarding policy v1",
    "rules": ["Review a dry-run before writes"]
  },
  "render_defaults": {
    "jira": {
      "profile": "full",
      "field_views": [
        {
          "id": "customfield_10001",
          "label": "Risk Notes",
          "placement": "section",
          "format": "jira_wiki"
        }
      ]
    },
    "confluence": {"profile": "default"}
  },
  "selectors": {
    "jira": [
      {
        "name": "my-open-work",
        "jql": "assignee = currentUser() AND resolution is EMPTY",
        "fields": ["status", "summary"]
      }
    ],
    "confluence": [
      {"name": "team-pages", "cql": "space = ENG AND type = page"}
    ]
  }
}
```

Rules:

- `schema_version` must be `1`.
- Every schema fact requires explicit `source` and `verified_at`; omit facts that were not checked.
- Any non-empty `preferences` requires `confirmed: true`.
- `team_policy` requires `source`; omit the whole section when no declared policy exists.
- New onboarding candidates store `preferences.mirror_root` as a canonical absolute path. Existing
  profiles may contain a legacy value beginning with `~`; expand it from the user's home without
  `eval` before operational use, and always pass paths as one shell-quoted argument/value.
- `render_defaults` uses the same service/field-view shape as atl render config, but applying the
  profile does not modify runtime config.
- Clearing `mirror_root` or a render service removes the memory default only. It is not an implicit
  request to unset an environment value or reset runtime config.
- Selector names are unique per service. Store reusable JQL/CQL, never sampled content.
- Unknown JSON keys are rejected.

`atl profile preview` normalizes unordered lists and returns `candidate_hash` and `current_hash`.
`atl profile apply` requires both exact hashes, so a changed candidate fails with exit 8 and a
concurrently changed current profile fails with exit 5. A syntactically valid profile with a future
schema version remains opaque: preview reports `migration_from_schema_version` and hashes its exact
bytes so an explicitly reviewed version-1 replacement can be applied without interpreting unknown
fields.
