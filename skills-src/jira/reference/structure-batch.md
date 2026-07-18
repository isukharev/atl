# Structure subtree plus explicit issue batch

Use this focused read-only route when a Structure subtree defines membership
but the final analysis needs only a small explicit set of Jira fields. Do not
load a full `structure view` or call Structure Value APIs when rows plus one
issue batch are sufficient.

```sh
export ATL_READ_ONLY=1
atl jira structure rows <structure-id> --root <row-id-or-folder>
atl jira export --ids <id,id,...> --fields summary,status --format json --out -
```

Preserve Structure row order and repeated issue rows. Build the explicit
selector list from unique issue identities, then preserve the export's
first-occurrence selector order. A missing identity in a complete explicit
export is missing evidence; it does not erase the Structure row. Report
Structure completeness and export omissions separately.

Prefer `--ids` when Structure rows expose stable Jira ids. Use `--keys` only
when keys are the reviewed selector source. Keep `--out -` for transient agent
analysis so no artifact or manifest is written.
