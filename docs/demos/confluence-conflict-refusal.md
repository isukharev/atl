# Demo: refuse a conflict without losing local work

After pulling and staging a local change, review a push:

```sh
atl conf edit \
  "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf" \
  --old "Draft" --new "Local"
atl conf push \
  "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf" \
  --dry-run
```

If the remote page moved after the mirror baseline, ATL reports drift. An
actual guarded push targets exactly the next reviewed version; a server-side
version conflict exits `5` instead of silently overwriting the newer page.

Recovery is explicit:

```sh
atl conf reconcile preview \
  "$ATL_MIRROR_ROOT/DEMO/complex-page/complex-page.csf" \
  --into "$ATL_MIRROR_ROOT" -o text
```

Do not run an overwriting pull or add `--force` automatically. Reconcile shows
base, local, and remote classifications without replacing the working file.

The repository rehearsal runs the equivalent sequence against a synthetic
backend that accepts the initial pull and rejects exactly one PUT with HTTP
409. It proves:

- ATL exits with the version-conflict class;
- exactly one write attempt reached only the loopback fixture;
- the complete local mirror artifact set, including Markdown, native CSF,
  baseline, metadata, and sidecars, remains byte-identical;
- the result never claims that the page was pushed.

Run it with:

```sh
make check-onboarding-docs
```
