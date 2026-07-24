Use the available read-only Confluence interface to determine the current
approval for `Indigo recovery approval`. Do not delegate, use web search,
inspect local workspace files, or write anything.

Call Confluence search exactly once with CQL
`siteSearch ~ "Indigo recovery approval"`, limit 10, and max_bytes 131072.

ATL may make its own bounded replay-safe transport attempts inside that single
tool call. If the tool returns `rate_limited` / `wait_before_retry`, stop. Do
not invoke the tool again, broaden or change the query, or claim any unavailable
source content. Preserve the exact query, report search as rate_limited, mark
overall evidence incomplete and decision undetermined, and leave source claims
empty. Return only the requested structured response.
