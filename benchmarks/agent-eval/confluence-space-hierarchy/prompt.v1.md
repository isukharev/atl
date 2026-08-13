Run exactly:

```text
atl conf space tree --space DOC --depth 0 --max-items 10 --max-scanned-items 20 --max-requests 2 --max-response-bytes 65536 --deadline 5s --
```

Use the JSON result to report the schema version, completeness, count, and the
observed page-to-parent pairs. `live_unproven` is not snapshot isolation. Return
only the requested JSON shape.
