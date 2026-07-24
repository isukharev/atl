Use the available read-only Confluence interfaces to determine the current
decision for `Quartz retention decision`. Do not delegate, use web search,
inspect local workspace files, or write anything.

Call Confluence search exactly once with CQL
`siteSearch ~ "Quartz retention decision"`, limit 10, and max_bytes 131072.
Select the single candidate and request only its `Current decision` section as
occurrence 1 with max_bytes 32768. This two-call order is mandatory.

If the section request returns not found, stop. Do not retry, broaden the
search, use another identifier, request a full page, or infer the missing
section from the title or excerpt. Preserve the accessible candidate identity
and exact query, report search as complete and section as not_found, mark
overall evidence incomplete and decision undetermined, and leave section
claims empty. Return only the requested structured response.
