# Live validation

Live checks validate Atlassian and source-control behavior that synthetic tests
cannot prove. They do not broaden authority: a configured credential or working
VPN permits connectivity, not arbitrary writes.

## Escalation ladder

1. Prove parsing, contracts, and failure modes with `httptest` or synthetic
   fixtures.
2. Run local deterministic and integration harnesses.
3. Use a configured live backend read-only and record only generalized results.
4. Preview an exact proposed write without applying it.
5. Apply only after exact authority exists for owned disposable targets.
6. Reconcile outcome once and execute the reviewed cleanup plan.

Load live values only from the gitignored integration environment, explicit
environment variables, or the isolated configuration selected by current
owner instructions. Never echo, copy, or persist credentials and private target
values into tracked files or public logs.

## Read-only checks

Start with setup/status/doctor and the smallest exact read. Bound pagination,
requests, output, and time. A connectivity or VPN failure invalidates that
attempt; record the category and ask the environment owner to repair external
state instead of retry-fishing.

Use the repository's normal clients: `atl` for Jira/Confluence behavior, `gh`
for GitHub, and `glab` for a configured GitLab host. Prefer dedicated CLI
subcommands over raw API requests. Authenticated reads must still honor the
public/private boundary.

## Write gate

Before any live create, edit, transition, upload, comment, resolve, delete, or
cleanup, state:

- exact owned objects and why they are disposable;
- exact content or field mutations;
- request order and maximum attempts;
- expected terminal state and reconciliation read;
- cleanup sequence and what must be retained;
- ambiguous-outcome policy.

Obtain explicit authority for that plan. Authority is scoped and consumable; it
does not transfer to another object or later session. Never modify or delete an
object not created or explicitly placed in scope by this work.

Send each non-replay-safe request at most once unless the reviewed plan
explicitly permits a bounded retry after proving the first attempt did not reach
the backend. On timeout or transport ambiguity, reconcile before any further
write. Preserve failed-attempt evidence privately and classify it; do not select
only successful retries.

Run live checks with `make integration` or `make live-smoke` when their fixture
contract matches the feature. A successful private validation becomes only a
generic PR statement such as “validated on a configured fixture”; private
hostnames, IDs, titles, fields, values, content, and screenshots remain private.
