# Owner-private corpus in a development container

Use the checked-in runtime template when a coding session needs a fresh,
qualified Jira or Confluence corpus without placing mirrors, credentials, or
index artifacts in the source checkout. The workflow captures one explicitly
bounded selection, publishes one immutable sealed generation, and gives an
indexer only a verified copy of the canonical document inventory.

[Documentation home](README.md) ·
[Sealed corpus generations](corpus-generations.md) ·
[Network egress](network-egress.md) ·
[CLI reference](reference/cli/local-artifacts.md)

This is an on-demand read-only capture workflow, not GitOps. It neither watches
Git commits nor pushes local changes back to Jira or Confluence.

## Trust boundary

The template is separate from the repository maintainer `.devcontainer`. Its
runtime wrapper enforces these locations:

| Location | Contents and rule |
|---|---|
| source checkout | Scripts and source only; corpus, index, secret, CA, and provider roots must not overlap it through a direct path or symlink alias |
| `/tmp/atl-context.XXXXXX` | Fresh exact-`0700` runtime with private config, attempts, sealed store, handoff, diagnostics, and an indexer-only document copy |
| index root | Caller-created, empty exact-`0700` directory outside both source and runtime roots |
| mounted inputs | Caller-owned regular files with mode `0400` or `0600`; backend URLs and PATs enter only selected ATL child environments, while project/space selectors necessarily become ATL child arguments inside the private container |

Every runtime directory is `0700`. Sealed documents, handoffs, the staged
indexer input, and index receipts are `0600`. Ordinary attempt-mirror views may
retain `0644`, but every path to them crosses an exact-`0700` runtime directory,
so other local users cannot traverse to those bytes. Container isolation does
not replace host filesystem policy: anyone who can control the container
runtime, the mounted host paths, or the process can read the corpus.

The wrapper gives the downstream indexer a separate digest-verified `0600`
copy of one `documents.indexer-v1.jsonl` member. ATL and indexer processes start
through `env -i` allowlists rather than a finite unset list. The indexer never
receives backend URLs, PATs, secret-file routes, CA routes, proxy variables,
unrelated ambient values, the corpus store, or an attempt directory. Selected
ATL processes receive only the mounted canonical inputs, the private runtime
routes, read-only/no-update controls, a fixed system path, and the optional
mounted CA file. Ambient legacy aliases, integration credentials, trust/update
overrides, process policy, mirror routing, and proxies cannot become fallback
inputs.

## Disposable cold start

Prerequisites are Linux, Docker-compatible Dev Containers support, outbound
access to the pinned base image and GitHub release verification endpoints, and
an exact ATL release version plus the SHA-256 for the current architecture's
release asset. Keep `.devcontainer-lock.json` beside `devcontainer.json`; the
CLI can enforce it with its frozen-lockfile mode. Hosted CI installs one exact
`@devcontainers/cli` dependency graph with `npm ci`; it does not delegate to an
action that downloads a floating CLI. The post-create installer checks the
supplied checksum, GitHub build provenance bound to `refs/tags/<ATL_VERSION>`,
and the binary's reported version before publishing it.

Start the container from the repository root:

```sh named-private-corpus-devcontainer-cold-start
export ATL_VERSION=v0.7.1
export ATL_ASSET_SHA256=<64-lowercase-asset-sha256>
devcontainer up --workspace-folder . \
  --config examples/corpus-devcontainer/.devcontainer/devcontainer.json \
  --frozen-lockfile
```

Mount runtime inputs with the Dev Container client or IDE, not by editing and
committing the template. Each URL, PAT, Jira project, and Confluence space file
must contain exactly one non-empty line. Mount them read-only at private paths
such as `/run/secrets/atl-jira-pat`; do not put the values in
`devcontainer.json`, image build arguments, the outer launch command, or shell
traces.
An optional corporate CA bundle follows the same file rule and is capped at
1 MiB. Establish any required direct/VPN route before capture; ambient proxy
variables are deliberately not passed to ATL. The template does not configure
a VPN, accept an ambient trust directory, or weaken TLS verification.

The wrapper reads selectors from mounted files so they are absent from the
checked-in template and outer launch command, but the ATL CLI receives the
selected project or space as a child-process argument. Treat the process list
inside the container as private; the template does not defend against another
process with the same container authority.

Inside the container, create an empty index root and provide an explicit
selector and every finite bound. This Jira-only example keeps the temporary
corpus disposable:

```sh named-private-corpus-jira-run
install -d -m 0700 /tmp/atl-index

export ATL_SOURCE_ROOT="$PWD"
export ATL_INDEX_ROOT=/tmp/atl-index
export ATL_INDEXER="$PWD/examples/corpus-devcontainer/local-indexer-stub.sh"
export ATL_JIRA_URL_FILE=/run/secrets/atl-jira-url
export ATL_JIRA_PAT_FILE=/run/secrets/atl-jira-pat
export ATL_JIRA_PROJECT_FILE=/run/secrets/atl-jira-project
export ATL_CA_FILE=/run/secrets/atl-ca.pem

export ATL_MAX_JIRA_ISSUES=5000
export ATL_MAX_REQUESTS=20000
export ATL_MAX_RESPONSE_BYTES=4294967296
export ATL_MAX_MEMBERS=100000
export ATL_MAX_GENERATION_BYTES=8589934592
export ATL_DEADLINE=2h
export ATL_MAX_IN_FLIGHT=4
export ATL_REQUESTS_PER_SECOND=20

sh examples/corpus-devcontainer/run-corpus.sh
```

For Confluence, provide `ATL_CONFLUENCE_URL_FILE`,
`ATL_CONFLUENCE_PAT_FILE`, `ATL_CONFLUENCE_SPACE_FILE`, and
`ATL_MAX_CONFLUENCE_PAGES`. Both services may be selected in one run, but their
captures have independent time windows rather than a cross-service remote
transaction.

Comments, attachment inventories, and attachment bodies default off. Enable
them only with the corresponding exact `0` or `1` controls and all required
bounds:

- `ATL_CAPTURE_COMMENTS=1`, `ATL_MAX_COMMENT_PAGES_PER_ITEM`, and
  `ATL_MAX_COMMENTS_PER_ITEM`;
- `ATL_CAPTURE_ATTACHMENTS=1`, `ATL_MAX_ATTACHMENT_PAGES_PER_ITEM`, and
  `ATL_MAX_ATTACHMENTS_PER_ITEM`;
- `ATL_CAPTURE_ATTACHMENT_BODIES=1`, per-body and total byte bounds, and an
  owner-private `ATL_ATTACHMENT_MEDIA_TYPES_FILE` containing one exact media
  type per line.

Choose caps from an approved resource budget. The response-byte and generation
limits bound consumed remote bytes and the sealed generation, not all container
overhead: retained attempts, diagnostics, a staged indexer copy, and the index
need additional disk. Container CPU, memory, process, and network limits remain
the caller's responsibility.

## What the checks prove

The wrapper forces `ATL_READ_ONLY=1` and `ATL_NO_UPDATE=1` and makes the
qualified complete `corpus build` its first and only remote operation. The
command validates the complete selector, bound, and evidence contract before
loading configuration or contacting a backend. This ordering prevents an
invalid selector from causing a broader diagnostic or sample query before the
canonical build validator rejects it. The bounded build reconciles the selected
membership and either publishes a sealed generation or fails.

A successful run prints one content-free result:

```json named-private-corpus-bootstrap-result
{"schema_version":1,"status":"complete","handoff":"sealed","indexer":"completed"}
```

Diagnostics stay inside the owner-private runtime. A failed build or handoff
never invokes the indexer. No in-progress generation becomes current, and an
already selected generation is never evidence that the remote backend will not
change later. Preserve a failed persistent runtime when the underlying ATL
error reports an ambiguous outcome; do not infer rollback and do not replay a
remote read automatically.

The default `ATL_CONTEXT_PARENT=/tmp` is disposable with the container, but the
wrapper deliberately does not delete it while the container is alive. This
keeps failure evidence available to the owner. For host persistence, mount a
dedicated exact-`0700` directory outside the checkout and index, then set its
container path as `ATL_CONTEXT_PARENT`. Each invocation still allocates a new
`atl-context.XXXXXX` child; it does not silently resume or overwrite an older
run. Cleanup belongs to the owner of that exact directory after verifying that
no recovery evidence or needed generation remains.

## Sealed handoff and indexers

`atl corpus handoff` reopens the current pointer through the sealed reader,
requires a qualified projection, and identifies exactly one canonical document
inventory. Ordinary stdout is content-free. The explicit handoff artifact
contains a generation identity, relative member route, mode, size, and digests,
so it is private, created exclusively as `0600`, and must live outside the
sealed store. The wrapper validates it, checks the sealed member again, and
copies only those verified bytes to the isolated indexer input.

The included local stub is a zero-egress contract demonstration. It writes one
private receipt and asserts that backend credentials and routes are absent.
Replace it only with a reviewed executable and keep its output outside both the
corpus and source checkout.

The optional `graphify-indexer.sh` wrapper is not a bundled Graphify install or
an ATL dependency. It gives `graphify extract` an isolated directory containing
only the canonical document inventory, deliberately omits `--code-only`, writes to
`ATL_INDEX_ROOT`, and uses `--no-cluster`. Native Jira wiki, Confluence CSF,
assets, mirror metadata, attempts, and duplicate Markdown members are never
handed to Graphify.

The isolated indexer copy has a `.txt` suffix so Graphify classifies it as a
document rather than ignoring an unsupported JSONL suffix or treating it as
code. Graphify scans that one-file directory rather than a single path. The
copy's bytes, size, and SHA-256 remain identical to the verified sealed JSONL
member; this is only a downstream filename adapter.

Graphify semantic extraction can send document contents to its configured
model provider. A loopback Ollama endpoint is accepted as the local example:

```sh named-private-corpus-graphify-local
export ATL_INDEXER="$PWD/examples/corpus-devcontainer/graphify-indexer.sh"
export GRAPHIFY_BIN="$(command -v graphify)"
export GRAPHIFY_BACKEND=ollama
export OLLAMA_HOST=http://127.0.0.1:11434
```

`GRAPHIFY_BIN` must resolve to an absolute regular executable; it is not looked
up through the caller's ambient `PATH`. A local Ollama URL must be exactly
`http://127.0.0.1:<port>`, `http://localhost:<port>`, or
`http://[::1]:<port>` with a decimal port in `1..65535`. Userinfo, path, query,
fragment, HTTPS, and other host spellings are not classified as loopback.
Any non-loopback Ollama endpoint or other backend additionally requires
`ATL_APPROVE_SEMANTIC_EGRESS=1`. That switch is an explicit acknowledgement,
not a network control. Apply container egress policy and review the provider's
retention, tenancy, region, and credentials before setting it. Derived graph,
vector, cache, and model outputs inherit the corpus privacy boundary.

## Maintainer verification

`make check-corpus-devcontainer` is hermetic: it uses synthetic content, a
loopback Jira server, a fake release boundary, and a stub indexer. It asserts
the exact read-only request set, zero requests for an invalid Jira selector,
source-tree cleanliness, modes, sealed-only handoff, failed-build isolation,
clean child environments, tag/version-bound installer verification, and
Graphify refusal of forged-loopback userinfo without contacting configured
backends or an external model. The Linux container lane separately exercises
the pinned Dev Container image, locked feature, exact lockfile-installed CLI,
and the same runtime scripts. Neither check grants authority for a live
provider write.
