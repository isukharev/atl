Use the installed `atl` Confluence skill and the synthetic read-only backend to
inventory table structure on page `8200`. Do not delegate, inspect repository
files, read full cell content, extract a table, or write anything.

Run exactly `atl capabilities --task confluence/table-analytics`, then exactly
once run `atl conf table summary --id 8200`. Do not probe help, use a positional
page id, or retry either command. Return the summary's exact structural counts.
Set `content_exposed=false` only when no page title, cell text, link URL, style
value, raw attribute, or warning text appeared in the summary.

Return only the requested structured response. The evaluation shell accepts
one `atl` command per Bash call; do not use pipes or compound commands.
