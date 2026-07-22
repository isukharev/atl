Use the installed `atl` Confluence skill and the synthetic read-only backend to
inventory table structure on page `8200`. Do not delegate, inspect repository
files, read full cell content, extract a table, or write anything.

Run exactly `atl capabilities --task confluence/table-analytics`, then exactly
once run `atl conf table summary --id 8200`. Do not probe help, use a positional
page id, or retry either command.

Return the summary's exact structural counts with these semantics:

- `table_count` is page-wide even when a table selector is used; this run has
  no selector, so return `selected_table=null`.
- `row_count` counts expanded rows and includes header rows.
- `column_count` is the width of the expanded rectangular grid.
- every cell count is over that expanded grid.
- a repeated cell is a coordinate covered by a rowspan or colspan; the original
  source cell is not repeated.
- source counts include only non-repeated cells whose rowspan or colspan is
  greater than one.
- a coordinate covered on both row and column axes contributes to both covered
  counts while remaining one repeated cell.

Set `count_semantics` to the exact requested structured values. Set
`content_exposed=false` only when no page title, cell text, link URL, style
value, raw attribute, or warning text appeared in the summary.

Return only the requested structured response. The evaluation shell accepts
one `atl` command per Bash call; do not use pipes or compound commands.
