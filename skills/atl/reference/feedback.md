# Reporting `atl` friction — sanitized issue + private case file

When working with `atl` went wrong in a way the tool or its docs should have
prevented, offer the user to report it. Two artifacts, two **separate consent
gates** — never write or send anything without an explicit yes:

1. a **sanitized public GitHub issue** in `isukharev/atl` (no private data), and
2. a **detailed private case file** on the user's machine, for their internal
   development team to reproduce the problem, test internally, and prepare a
   fix/MR. It references the issue number but is never attached to it.

## What counts as reportable friction

Objective triggers — not every hiccup:

- The same operation failed repeatedly (≥2 non-zero exits) before you found a
  workaround, or you had to fall back to a different technique than the skill
  recommended.
- An error message left you guessing (you had to inspect source/bytes to
  understand it), or output contradicted the documented contract.
- A fail-closed refusal (`exit 8`) fired on something that looks like it
  should be expressible, and the suggested remedy didn't apply.
- Data-level surprises: a push/apply produced content the user did not expect,
  drift/dirty state that didn't match reality.

Not reportable: user typos, missing permissions on the backend, tasks outside
`atl`'s documented scope.

## Step 1 — ask the user

Summarize in one or two sentences what went wrong and ask two questions
separately:

> 1. Хотите, я заведу публичный issue в репозитории atl (без каких-либо ваших
>    данных — только обезличенное описание)?
> 2. Хотите, я сохраню подробный локальный файл с полными деталями для вашей
>    внутренней команды разработки?

Either may be accepted independently. If both are declined, drop it and move on.

## Step 2 — the sanitized public issue (on consent)

File with `gh issue create --repo isukharev/atl --label kind/bug` (or
`kind/feature` for a capability gap). If `gh` is unavailable, give the user
the prefilled text and the new-issue URL instead.

**Redaction checklist — the issue must contain NONE of:**

- backend hostnames/URLs, IP addresses,
- page IDs, space keys, issue keys, board/sprint/Structure IDs,
- page or issue titles/bodies, table contents, attachment names,
- personal names, usernames, user keys, emails,
- tokens/PATs (never, in any form), local paths containing a username or
  company name,
- company/product names from the user's environment.

Replace them with placeholders: "the configured backend", "a pulled page",
"a styled multi-table page", "`<PAGE_ID>`", "`<KEY-123>`".

Template:

```markdown
## What happened
<command shape with placeholders, e.g. `atl conf apply <page>.md` → exit 8>
<error text with identifying values replaced>

## Expected
<what the docs/skill led the agent to expect>

## Actual / impact
<what actually happened; how many turns/retries it cost; workaround used>

## Environment
atl version: <output of `atl version`>
OS: <linux/macos + arch>
Backend: Confluence DC / Jira DC (no URL)
```

## Step 3 — the private case file (on consent)

Write `atl-feedback/<YYYY-MM-DD>-<slug>.md` under the current working
directory (create the dir; suggest adding `atl-feedback/` to `.gitignore`).
Tell the user explicitly: **this file contains real identifiers and must not
be committed, shared publicly, or pasted into the issue** — it is a handoff
artifact for their internal development team.

Full detail belongs here, verbatim:

```markdown
# atl friction case — <date>, refs isukharev/atl#<N>

## Environment
<atl version, OS, backend URL, auth method (never the token itself)>

## Timeline
<numbered: every command actually run, its full output, exit codes,
 what the agent tried next and why>

## Artifacts
<real page IDs / issue keys involved; relevant .csf/.md excerpts;
 validate/apply reports>

## Workaround
<what eventually worked, or "unresolved">

## Suggested internal follow-up
<repro steps for internal testing; where a fix likely lands if known>
```

Even here, **never** write the PAT/token value.

## Hard rules

- Consent first, every time, for each artifact separately. No auto-reporting,
  no telemetry.
- The public issue and the private file must never be merged: sanitized data
  goes public, real data stays local.
- One issue per distinct problem; check for an existing similar issue first
  (`gh issue list --repo isukharev/atl --search "<keywords>"`).
