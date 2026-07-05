<!-- Generated from skills-src/confluence/reference/csf-authoring.md — edit the source and run 'make gen-plugins'. -->
# Authoring new CSF content (pages, sections, comments)

> **Check first:** a whole new page usually doesn't need hand-written CSF — `conf page create
> --from-md body.md` converts a markdown body directly (fail-closed, exit 8 names any block
> outside the subset). Reach for these snippets when that refuses, or for comments and
> sections spliced into an existing `.csf`.

Use these snippets when you **write CSF from scratch** — a new page body for `conf page create
--from-file`, a comment for `conf comment add --from-file`, or a new section spliced into an
existing `.csf`. CSF is XHTML plus `<ac:*>`/`<ri:*>` elements; it is **not Markdown and not
HTML5** — every tag must be closed, attributes quoted, and `&` written as `&amp;`.

Every snippet below passes `atl conf validate`. Always validate your composed body before
`create`/`push`; malformed CSF is rejected.

## Minimal page skeleton

A page body is a fragment — no `<html>`/`<body>` wrapper, no XML prolog:

```xml
<h1>Overview</h1>
<p>What this page covers and why it exists.</p>
<h2>Details</h2>
<p>Body text with <strong>bold</strong>, <em>italic</em>, <code>inline code</code>
and a <a href="https://example.com">plain link</a>.</p>
<ul>
  <li>First point</li>
  <li>Second point</li>
</ul>
```

Prose formatting is plain XHTML: `<p>`, `<h1>`–`<h6>`, `<strong>`, `<em>`, `<code>`, `<ul>/<ol>/<li>`,
`<blockquote>`, `<hr/>`, `<br/>`.

## Code block (macro)

The code lives in CDATA, so nothing inside needs escaping:

```xml
<ac:structured-macro ac:name="code">
  <ac:parameter ac:name="language">go</ac:parameter>
  <ac:parameter ac:name="title">main.go</ac:parameter>
  <ac:plain-text-body><![CDATA[func main() {
	fmt.Println("hello")
}]]></ac:plain-text-body>
</ac:structured-macro>
```

If the code itself contains `]]>`, split it: `]]]]><![CDATA[>`.

## Panels: info / warning / note

```xml
<ac:structured-macro ac:name="info">
  <ac:rich-text-body><p>Deployed behind the feature flag <code>new-sync</code>.</p></ac:rich-text-body>
</ac:structured-macro>
<ac:structured-macro ac:name="warning">
  <ac:rich-text-body><p>Do not run this against production.</p></ac:rich-text-body>
</ac:structured-macro>
```

(`note` and `tip` work the same way.)

## TOC, status lozenge, expand

```xml
<ac:structured-macro ac:name="toc"/>
<p>Rollout: <ac:structured-macro ac:name="status">
  <ac:parameter ac:name="colour">Green</ac:parameter>
  <ac:parameter ac:name="title">ON TRACK</ac:parameter>
</ac:structured-macro></p>
<ac:structured-macro ac:name="expand">
  <ac:parameter ac:name="title">Full error log</ac:parameter>
  <ac:rich-text-body><p>Long details hidden by default.</p></ac:rich-text-body>
</ac:structured-macro>
```

Status colours: `Grey`, `Red`, `Yellow`, `Green`, `Blue` (note the British spelling of the
`colour` parameter).

## Task list (checkboxes)

```xml
<ac:task-list>
  <ac:task>
    <ac:task-status>incomplete</ac:task-status>
    <ac:task-body>Ship the migration</ac:task-body>
  </ac:task>
  <ac:task>
    <ac:task-status>complete</ac:task-status>
    <ac:task-body>Write the design doc</ac:task-body>
  </ac:task>
</ac:task-list>
```

## Table

```xml
<table>
  <tbody>
    <tr><th><p>Service</p></th><th><p>Status</p></th></tr>
    <tr><td><p>api</p></td><td><p>live</p></td></tr>
    <tr><td><p>worker</p></td><td><p>canary</p></td></tr>
  </tbody>
</table>
```

Wrap cell content in `<p>` — that is what Confluence's own editor produces, and it keeps later
in-place edits diff-friendly.

## Page links, user mentions, attachment links

```xml
<p>See <ac:link><ri:page ri:content-title="Design: Sync Engine" ri:space-key="ENG"/></ac:link>
for the design, ping <ac:link><ri:user ri:username="jdoe"/></ac:link> with questions,
and check the attachment <ac:link><ri:attachment ri:filename="report.pdf"/></ac:link>.</p>
```

- `ri:page` links by **exact title** (add `ri:space-key` for cross-space links).
- `ri:user` uses the **DC username** (`atl jira user search` / `conf me` help find it).
- Omitting `ri:space-key` resolves the title in the page's own space.

## Minimal comment body

A comment body is the same CSF fragment format — usually just a paragraph:

```xml
<p>Implemented in <a href="https://github.com/org/repo/pull/42">PR #42</a>; updated the
rollout section above to match.</p>
```

```bash
atl conf comment add --id <page-id> --from-file comment.csf
```

## Gotchas

- **Escape `&` as `&amp;`** in text and attribute values (`R&amp;D`). `<` in text is `&lt;`.
  Prefer literal UTF-8 characters or numeric entities (`&#8594;`) over HTML named entities —
  XML defines only `&amp; &lt; &gt; &quot; &apos;`.
- **Self-close empty elements**: `<hr/>`, `<br/>`, `<ac:structured-macro ac:name="toc"/>`.
- **Don't invent macros.** If you need one not listed here, pull an existing page that uses it
  and copy the element from its `.csf` — parameters differ per macro and per instance.
- The `atl` write path pushes your bytes verbatim; there is no cleanup pass. What you validate
  is exactly what publishes.
