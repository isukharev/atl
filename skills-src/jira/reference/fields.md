# Jira fields, transitions, and editing large bodies

## Discover before you write

Jira rejects unknown field ids, status names, and link types — discover the valid values first:

- `atl jira fields` → `{schema_version, source, complete, total, count,
  fields:[{id,name,custom,schema}]}` without values. Custom fields look like
  `customfield_10001`; exact ids and unambiguous display names are both valid
  selectors. Treat an empty match as absence only when `complete:true`; never
  infer completeness from a successful call or non-empty result. Filters change
  `count`, not the source `total`/`complete` qualification.
- `atl jira issue fields <KEY> --metadata-only` → value-free named records for
  non-empty fields. Start here when evidence-bearing custom fields are unknown;
  then repeat `--field "Exact Name"` without metadata-only for only the selected
  compact values. Use `--raw` only when compact projection is insufficient and
  private transport/user data is acceptable. Metadata-only conflicts with raw.
- `atl jira issue history <KEY> --field "Exact Name"` → complete/partial
  provenance plus the selected field's `last_changes`. Time flags are local
  post-read filters. Date-only values use one observed Jira current-user IANA
  timezone lookup and expose the resolved UTC interval. Midnight gaps/folds
  cover the complete real civil day and a fully skipped date fails closed;
  explicit-offset RFC3339 values need no lookup. Never interpret
  `complete:false` as absence of evidence.
  An unsupported timestamp on a matching selected change fails closed because
  `last_changes` cannot be ordered safely.
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

## Large custom-field values from files

If a pulled view already records the field as editable `section` + `jira_wiki`,
it may use the combined mirror apply/push flow with Description. Otherwise, do
not put a long custom-field body in `--field key=value` or shell argv; use the
guarded file command, which previews by default:

```bash
export ATL_READ_ONLY=1
atl jira issue field preview PROJ-1 \
  --from-md customfield_10050=progress.md \
  --allow-fields customfield_10050
# review expected_updated, proposal_hash, and normalized values, then apply only
# after approval with field set and those exact gates:
env -u ATL_READ_ONLY atl jira issue field set PROJ-1 \
  --from-md customfield_10050=progress.md \
  --allow-fields customfield_10050 \
  --expected-updated '<exact value>' \
  --expected-proposal-hash '<exact hash>' --apply
```

`field preview` is a dedicated GET-only command and therefore remains available
under the inherited read-only policy. `field set` is classified as mutating even
without `--apply`; invoke it only for the exact approved apply command. A fresh
preview is required whenever the file, issue, field allowlist, or remote state
changes.

`--from-md FIELD=PATH` always converts to a Jira-wiki **string**.
`--from-file FIELD=PATH` keeps valid top-level JSON objects/arrays structured;
all other bytes are an exact string. The files/stdin and normalized proposals
are capped at 64 MiB in aggregate. Only Jira custom fields named in the exact
`--allow-fields` list are accepted. A stale `updated` value blocks with exit 8;
a changed input file or different issue key also blocks because apply requires
the key-bound schema-v2 aggregate `proposal_hash`. An already-satisfied value
does not write after both gates pass.

## Editing a large description / epic body as a file

**Check first:** for a bounded change (fix a value, add/remove a section, reword a
paragraph) skip the file round-trip entirely — one `atl jira issue edit <KEY> --old … --new …`
does fetch→splice→write with the `--old` match as the drift guard. For a structural rewrite of a
**pulled** issue, prefer the mirror md cycle — edit generated `# Description` or a configured
editable rich-text field in `<KEY>.md`, run `jira apply`, then `jira push` (jira skill §4b).
Editable fields require `section` + `jira_wiki` + `editable:true`; their proposed values are explicit
pending state and do not mutate the raw snapshot. The file pattern below is for composing a body in
wiki markup from scratch or a wholesale rewrite outside that cycle.

If push reports pending-field drift, pull fresh, compare the remote raw value in
`<KEY>.json` with the local proposal still overlaid in `<KEY>.md`, edit the proposal
if necessary, then run `jira apply --rebase-pending`. This explicit step adopts
the reviewed snapshot value as the new base; push immediately fresh-checks it again.

Inline flags are awful for long bodies. Edit the wiki body as a file instead — Jira's `--from-file`
accepts a body file. Compose it in Jira wiki markup, **not Markdown** — see
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
mirror's native `<KEY>.wiki` substrate. A bare edit to the derived `<KEY>.md` staging view changes
nothing until `jira apply` stages its supported edit; `<KEY>.json` is always read-only.
