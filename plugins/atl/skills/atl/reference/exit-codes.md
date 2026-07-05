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
| `5` | Version conflict (Confluence push) | The remote moved past your synced version. Re-pull and reconcile; only `--force` (clobber) after a human decides. |
| `6` | Forbidden | The token authenticated but lacks permission for this object/space. Don't retry blindly — surface it; the user may need a broader-scoped token or access. |
| `7` | Not configured — backend URL or PAT **not set** yet | Setup is incomplete (no URL, or no token stored/in env) → run the setup skill (or `atl config set` + `atl auth login`). |
| `8` | Check failed (`jira issue check`) | A field listed in `--require` is empty. Populate the missing fields (the JSON report names them), then re-run `check` before transitioning. |

Notes:
- Codes `3` vs `7` are distinct: `7` = "you haven't set me up" (no URL/token), `3` = "the token you
  gave me was refused". `7` → finish setup; `3` → replace the token.
- Codes `3` vs `6` are distinct: `3` = "who are you?" (re-auth), `6` = "you may not" (permissions).
- Only Confluence `push` uses the version gate (`5`). Jira updates are last-writer-wins (no `5`).
- `8` is a *gate* signal, not a command failure — `jira issue check` ran fine and is telling you
  the issue is not ready (e.g. for a transition). Fix the fields; don't retry blindly.
- `conf validate` exits non-zero when the CSF is not well-formed; treat `error`-severity problems
  in its JSON `problems[]` as a hard block before pushing.
