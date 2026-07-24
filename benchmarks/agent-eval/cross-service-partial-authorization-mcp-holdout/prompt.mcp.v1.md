Use the available read-only Jira and Confluence interfaces to determine the
current decision for `Cobalt failover readiness`. Do not delegate, use web
search, inspect local workspace files, or write anything.

Call Jira search exactly once with JQL
`text ~ "Cobalt failover readiness" ORDER BY updated DESC`, columns `key`,
`summary`, `status`, and `updated` in that order, limit 10, and max_bytes
131072. Then call Confluence search exactly once with CQL
`siteSearch ~ "Cobalt failover readiness"`, limit 10, and max_bytes 131072.
Select the single matching page and request only its `Outcome` section as
occurrence 1 with max_bytes 32768. This three-call order is mandatory.

If the section request is forbidden, stop. Do not retry, broaden either query,
request a full page, use another identifier, or infer the restricted section's
contents from its title or excerpt. Report the two searches as complete and the
section as forbidden, set the overall evidence to incomplete and the decision
to undetermined, and leave section claims empty. Preserve both exact queries,
the selected identities, and the observed Jira status. Return only the
requested structured response.
