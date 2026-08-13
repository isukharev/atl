Use exactly one `confluence_attachment_search` call with `space` `OPS`, CQL
`mediaType = "application/pdf"`, `max_items` 1, `max_requests` 2,
`max_response_bytes` 65536, `deadline_ms` 5000, and `max_bytes` 65536.

Determine whether `release-checklist.pdf` is present in that returned prefix.
Do not continue the query and do not read bytes, comments, paths, or URLs. An
omission from a partial live prefix is undetermined, not absence. Return only
the requested JSON shape.
