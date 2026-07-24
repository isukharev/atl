Use the available read-only Confluence interface to determine the current
decision for `Silver retention decision`. Do not delegate, use web search,
inspect local workspace files, or write anything.

Call Confluence search exactly once with CQL
`siteSearch ~ "Silver retention decision"`, limit 10, and max_bytes 1024.

If the tool returns `output_limit_exceeded` / `narrow_or_raise_bound`, stop. Do
not invoke the tool again, narrow or broaden the query, raise the bound, or
interpret the rejected result as partial evidence. Preserve the exact query and
selected bound, report search as output_limit_exceeded, mark overall evidence
incomplete and decision undetermined, and leave source claims empty. Return only
the requested structured response. Use exactly
`["selected_bound_rejected_complete_result"]` for observed_facts,
`["no_result_returned"]` for access_limitations, and
`"output_bound_prevented_grounded_decision"` for brief; these are
machine-readable statuses, not source claims.
