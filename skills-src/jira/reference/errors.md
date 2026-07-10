# Jira error reference

| Symptom / Exit | Likely cause | Remedy |
|---|---|---|
| Exit 7 | Backend URL or PAT not configured | Run the setup skill |
| Exit 3 | Token rejected | Re-run `atl auth login --service jira` |
| Exit 4 | Issue is absent/invisible | Verify the key and permissions |
| Exit 6 | Token lacks permission | Ask for the required permission |
| Required-field check fails | A required field is empty | Populate it, then retry |
| Transition rejected | Status is unavailable | Run `jira transitions --key <KEY>` |
| Field rejected | Option is unavailable | Run `jira field-options` for project/type/field |
| `issue edit` exit 4 | `--old` not found | Review closest region and current description |
| `issue edit` exit 2 | `--old` is ambiguous | Add context or explicitly use `--all` |
| `issue edit` exit 8 | Match crosses an omitted line break | Copy exact text including newlines |
| `jira push` exit 8 | Description/pending field drift | Pull, compare, and explicitly rebase fields; `--force` never overrides field drift |
| `jira push` exit 2 | No mirror baseline | Pull before mirror edit/apply/push |
| `jira apply` exit 8 | Stale view, lossy/unconvertible edit, read-only section, or diverged wiki | Migrate stale markers before editing; otherwise follow the named recovery and use `--allow-loss` only intentionally |

Legacy `comment <KEY>` and `link <KEY>` forms were restructured; use
`comment add|list` and `link add|list`. Structure exit 4/6 means the plugin,
object, or permission is unavailable; Structure commands remain read-only.
