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

## Setting fields

`--field key=value` is repeatable on `create` and `update`:

```bash
atl jira issue update PROJ-1 --field priority=High --field labels=backend
atl jira issue create --project PROJ --type Task --summary 'X' --field customfield_10020=Sprint-7
```

Use the field `id` from `atl jira fields` and a value from `atl jira field-options`. If a field
expects a specific shape, confirm it with `field-options` rather than guessing.

## Editing a large description / epic body as a file

Inline flags are awful for long bodies. Edit the wiki body as a file instead — Jira's `--from-file`
accepts a body file (up to 64 MiB):

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
