# `atl` exit codes and how to react

`atl` maps failure conditions to stable exit codes (driven by sentinel errors). Parse the JSON on
stdout for detail, and branch on the exit code:

| Code | Meaning | How to react |
|---|---|---|
| `0` | Success | Continue. |
| `1` | Generic error | Read the stderr/JSON message; fix and retry. |
| `2` | Usage error (bad flags/args) | Correct the command; check the flag with `--help`. |
| `3` | Auth failure — the server **rejected** the token | The PAT was supplied but refused (expired/revoked/wrong instance) → `atl auth login --service <svc>` with a valid token. |
| `4` | Not found | The id/key/page/issue doesn't exist or isn't visible — verify the identifier. |
| `5` | Version conflict (Confluence push) | Preserve the local candidate, run `conf reconcile preview` against fresh remote state, resolve base/ours/theirs, and make a new preview. Only consider `--force` after a human explicitly chooses to clobber reviewed remote changes. |
| `6` | Forbidden | The token authenticated but lacks permission for this object/space. Don't retry blindly — surface it; the user may need a broader-scoped token or access. |
| `7` | Not configured — backend URL or PAT **not set** yet | Setup is incomplete (no URL, or no token stored/in env) → run `{{atl.setup_cmd}}` (or `atl config set` + `atl auth login`). |
| `8` | Safety or check gate refused the operation | Preserve local work and follow the structured recovery. Depending on the result, repair validation, satisfy required fields, reconcile drift or an ambiguous outcome, or create and review a fresh proposal. Never treat this code as permission to replay a write. |

Notes:
- JSON failures include a schema-v1 `recovery` object. Follow its closed action
  and optional `next_capability` instead of parsing prose. `retry_safe:true`
  means only that the exact same explicitly modeled read may be replayed after
  the stated wait/transport repair; it never grants write authority. Fresh-read,
  changed-argument, approval, and reconciliation workflows remain false.
- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token), `3` = "the token you
  gave me was refused". `7` → finish setup; `3` → replace the token.
- Codes `3` vs `6` are distinct: `3` = "who are you?" (re-auth), `6` = "you may not" (permissions).
- Only Confluence `push` uses the version gate (`5`). Jira updates are last-writer-wins (no `5`);
  `jira push` guards drift with an app-layer compare instead and refuses on drift with `8`, not `5`.
- `8` is a *gate* signal, not permission to retry — validation, local safety,
  proposal, baseline, drift, and ambiguous-outcome checks can all produce it.
  Preserve the candidate and follow the structured recovery. For Jira mirror
  drift, reconcile or deliberately rebase the pending proposal; use force only
  after a human reviews the remote change and chooses the overwrite.
- Error-severity CSF validation is one exit-8 `check_failed` gate for
  `conf validate`, `conf push`, `conf page create`, and `conf blog create`.
  Treat the emitted JSON `problems[]` as the repair list; the invalid body did
  not reach the backend.
