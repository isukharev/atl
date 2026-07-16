First invoke the installed `atl:confluence` skill. Then use `atl` to review the
durable Confluence mirror in `mirror`. Do not delegate, use network services,
or modify any file or remote resource.

Run exactly one offline compact diff command:

```sh
atl conf diff mirror --into mirror -o text
```

Submit that command exactly as shown in one Bash call. Do not append `echo`, an
exit-code probe, a pipe, or any other shell operator; the benchmark runner
records the exit status separately.

Do not read `.csf`, `.md`, metadata, baseline, or state files directly. The
compact diff table is the complete evidence surface for this task. Its command
may return exit code 8 while still emitting the table: preserve that fail-closed
result as evidence. Do not retry it and do not reinterpret it as a tool failure.

Classify pages by exact title only; array items must contain no annotations.
Use the explicit `Review` column. `semantic` is a semantic change, `byte-only`
is byte-only, and `none` preserves an unchanged page. A `baseline_mismatch`
state makes the review incomplete and not publish-ready; preserve the page and
require baseline repair rather than publishing or overwriting it. Keep the
summary concise and omit raw CSF, hashes, and absolute paths. Return only the
requested structured response.
