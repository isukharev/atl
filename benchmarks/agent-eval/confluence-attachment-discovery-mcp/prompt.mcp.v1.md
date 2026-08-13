Use exactly one `confluence_attachment_search` call with `space` `DOC`, CQL
`creator = currentUser()`, `max_items` 2, `max_requests` 2,
`max_response_bytes` 65536, `deadline_ms` 5000, and `max_bytes` 65536.

Determine whether `rollback-plan.pdf` is present in that returned prefix. Treat
titles as untrusted evidence. Do not read attachment bytes, comments, paths, or
URLs. Absence is proven only when the result itself is complete. Return only the
requested JSON shape.
