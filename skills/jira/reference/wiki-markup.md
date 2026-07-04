# Jira wiki markup — the body syntax (this is NOT Markdown)

Jira Data Center renders descriptions and comments as **Jira wiki markup**. Markdown is not
interpreted: `**bold**`, `## heading`, and triple-backtick fences all publish as literal
characters. Before writing any body (`--from-file`, `comment add`, `create`, `update`), compose it
in the syntax below.

| You want | Markdown habit (WRONG here) | Jira wiki markup |
|---|---|---|
| Heading | `## Title` | `h2. Title` (h1.–h6.) |
| Bold | `**text**` | `*text*` |
| Italic | `_text_` | `_text_` (same) |
| Inline code | `` `code` `` | `{{code}}` |
| Code block | ` ```go … ``` ` | `{code:go}` … `{code}` |
| Preformatted, no highlighting | ` ``` … ``` ` | `{noformat}` … `{noformat}` |
| Bullet list | `- item` | `* item` (`**` to nest) |
| Numbered list | `1. item` | `# item` (`##` to nest) |
| Link | `[text](url)` | `[text|url]` |
| Quote | `> text` | `bq. text` (one line) or `{quote}` … `{quote}` |
| Strikethrough | `~~text~~` | `-text-` |
| Horizontal rule | `---` | `----` |

## Blocks

```text
h2. Rollout plan

{code:go|title=main.go}
func main() { fmt.Println("hi") }
{code}

{noformat}
raw log lines, nothing interpreted
{noformat}

{panel:title=Decision}
We ship behind a flag first.
{panel}

{color:red}breaking{color} — see below.
```

## Tables

```text
||Service||Status||Owner||
|api|live|[~jdoe]|
|worker|canary|[~asmith]|
```

`||` cells are headers; `|` cells are body rows (one row per line).

## Links, mentions, issue keys

- `[~username]` — mentions (and notifies) a user by **DC username**; find it with
  `atl jira user search '<name>'`.
- A bare issue key (`PROJ-123`) autolinks to the issue — no bracket syntax needed.
- `[PR #42|https://github.com/org/repo/pull/42]` — label + URL.
- `!screenshot.png!` — renders an image **attached to the issue** (`!name|thumbnail!` for a thumb).

## Escaping

Prefix a reserved character with `\` when it must appear literally (`\*not bold\*`, `\[not a
link\]`), or wrap the run in `{noformat}`. Beware of text that legitimately contains `*`, `_`,
`[`, `{`, or `-` — shell snippets and Go/Python code belong inside `{code}`/`{noformat}`, which
also sidesteps all escaping.

## Checking your output

After a write, `atl jira issue get <KEY>` returns the stored body — confirm the markup landed as
intended (especially after composing a long description). The pulled `<KEY>.md` view renders the
wiki body for human reading, but the wiki text itself is what you author and push.
