# Jira fields, transitions, and editing large bodies

## Discover before you write

Jira rejects unknown field ids, status names, and link types — discover the valid values first:

- `atl jira fields` → `{ "fields": [ {id, name, custom} ] }`. Custom fields look like
  `customfield_10001`; use the `id`, not the display name, with `--field`.
- `atl jira field-options --project PROJ --type Bug --field priority` → `{ "options": [ ... ] }`
  the allowed values for a field in that project/issue-type context.
- `atl jira transitions --key PROJ-1` → `{ "transitions": [ {id, name, to} ] }`. Pass the
  transition/target name to `--to`.
- `atl jira link-types` → the valid link type names (e.g. `blocks`, `relates to`).

## Setting fields — value shapes matter

`--field key=value` is repeatable on `create`, `update`, and `transition`. The value is sent
**as a string**, unless it starts with `{` or `[` and parses as JSON — then it is sent as that
JSON object/array. Jira DC is strict about shapes, so pick the right form per field type:

| Field type | Shape Jira expects | Example |
|---|---|---|
| Text / textarea custom field | plain string | `--field customfield_10050=Some text` |
| Priority, resolution, single-select | object with `name` (or `value` for selects) | `--field 'priority={"name":"High"}'` |
| Components, fixVersions, versions | **array** of objects | `--field 'components=[{"name":"backend"}]'` |
| Labels | array of strings | `--field 'labels=["backend","bug"]'` (or use `jira issue labels`) |
| Number field | plain number as string | `--field customfield_10060=5` |
| Cascading select | nested object | `--field 'customfield_10070={"value":"Hardware","child":{"value":"Laptop"}}'` |

```bash
atl jira issue update PROJ-1 --field 'priority={"name":"High"}'
atl jira issue create --project PROJ --type Task --summary 'X' \
  --field 'components=[{"name":"backend"}]' --field 'fixVersions=[{"name":"1.2"}]'
```

**A bare string where an object is expected fails** (`--field priority=High` → 400). When Jira
rejects a value, re-check the shape here and the allowed values via
`atl jira field-options --project PROJ --type <Type> --field <id>`.

Special cases with dedicated commands — prefer them over `--field`:
- **Assignee** → `atl jira issue assign <KEY> --to <username> | --me | --none` (the generic field
  path needs `--field 'assignee={"name":"jdoe"}'`; a bare username fails).
- **Labels** (add/remove without clobbering) → `atl jira issue labels <KEY> --add a,b --remove c`.
- **Epic Link** → `atl jira issue link-epic <KEY> --epic EPIC-1`.

Use the field `id` from `atl jira fields` (custom fields look like `customfield_10001`), and
confirm allowed values with `field-options` rather than guessing.

## Editing a large description / epic body as a file

Inline flags are awful for long bodies. Edit the wiki body as a file instead — Jira's `--from-file`
accepts a body file (up to 64 MiB). Compose it in Jira wiki markup, **not Markdown** — see
[wiki-markup.md](wiki-markup.md):

1. **Seed** a working file from the current description:
   - from the pulled snapshot: read `~/.atl/<workspace>/<PROJECT>/<KEY>.json` → its `description`
     field, OR
   - `atl jira issue get <KEY>` → the `description` field.
   Write that wiki text to a scratch file, e.g. `PROJ-1.description.wiki`.
2. **Edit** `PROJ-1.description.wiki` with normal file tools (Read/Edit) — ideal for big epics.
3. **Apply** (re-`get` first, since there's no version gate):
   ```bash
   atl jira issue get PROJ-1            # confirm nobody changed it since you seeded
   atl jira issue update PROJ-1 --from-file PROJ-1.description.wiki
   ```

The scratch `.wiki` file is a working body file you feed to `--from-file` — it is **not** the
read-only `<KEY>.md`/`<KEY>.json` mirror snapshot, and editing those snapshots changes nothing on
the server.
