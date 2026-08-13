Run exactly:

```text
atl conf space tree --space OPS --depth 0 --max-items 2 --max-scanned-items 20 --max-requests 2 --max-response-bytes 65536 --deadline 5s --
```

Use the JSON result to report the schema version, completeness, count, and only
the observed page-to-parent pairs. Preserve the partial reason as
`qualification`; omitted pages and parents are not proven absent. Return only
the requested JSON shape.
