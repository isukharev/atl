# Standalone agent-eval distribution dry run

This runbook describes the release-candidate boundary implemented for issue
#1388. It creates artifacts locally from an exact reviewed commit; it does not
publish a tag, release, container, Action, or repository extraction. The
support contour is defined separately in issue #1387, and containers/Actions
are separately scoped to #1389.

## Build

Build the nested evaluator from a clean checkout and pass an absolute,
previously absent output directory. The complete nested evaluator source tree
(the CLI's local dependency closure) is selected and its digest is recorded in
`manifest.json`:

```sh
source_commit="$(git rev-parse HEAD)"
build_date="$(git show -s --format=%cI "$source_commit")"
version="0.1.0-pre-release"
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  CGO_ENABLED=0 go -C internal/agenteval build -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid= -X main.standaloneBuildVersion=$version -X main.standaloneBuildCommit=$source_commit -X main.standaloneBuildDate=$build_date" \
  -o /tmp/agent-eval ./cmd/agent-eval

env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode build \
  --binary /tmp/agent-eval \
  --compatibility internal/agenteval/testdata/standalone-conformance.v1.json \
  --source-root . \
  --source-files internal/agenteval \
  --schema-registry internal/agenteval/schemaregistry/registry.v1.json \
  --protocol internal/agenteval/cmd/agent-eval/standalone_process.go \
  --source-commit "$source_commit" \
  --version "$version" \
  --output /absolute/new/distribution
```

The release-candidate contract currently admits only version
`0.1.0-pre-release` on Linux/amd64. Builds are host-only:
the requested platform and architecture must equal the build host, so a
foreign target is refused rather than stamped with a false identity. The
output must be a different, absent path outside the selected source tree. The
builder reads the bounded candidate bytes once, copies that exact snapshot, and
probes the copied bytes with `version --output json`; the reported version,
contract version, and source commit must match the manifest. The compatibility
bundle, schema registry, and process protocol must also be regular files inside
the selected source tree, so their bytes are covered by the source digest. This
is a bounded local build probe, not provider or backend execution.

The builder writes a bounded manifest, checksum, SPDX SBOM, provenance record,
compatibility bundle, static scratch container descriptor, and composite Action
descriptor. The generated Action is deliberately a pre-verified runner: it
does not replace detached-signature verification or release approval. A
`.incomplete` marker remains if the build is interrupted; a
marker-bearing directory is never accepted as a distribution.

Two builds with the same inputs must have byte-identical members. The manifest
binds the binary, schema registry, process protocol, compatibility bundle,
source tree, platform, architecture, and version. The builder reads only
regular files under explicit paths and rejects symlinks, sensitive names such
as `.git`, `.env`, private-key extensions, ignored source residue, and
source/output overlap. The version probe is a bounded local process execution,
not a sandbox: it runs the supplied candidate as the current user for at most
two seconds with a scrubbed environment and private working directory, but it
does not prove that descendants, syscalls, or network access are contained.
Run builds for untrusted bytes in a separate isolated host/container. The
manifest records probe network/credential status as `probe-unverified`; verify,
sign, install, and uninstall never execute the candidate.

## Verify and sign

Development inspection may use an unsigned allowance through the Go API tests,
but the command-line verifier and installer require a detached Ed25519
signature:

```sh
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode sign --distribution /absolute/distribution \
  --private-key /owner-private/release-ed25519.key

env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode verify --distribution /absolute/distribution \
  --public-key /owner-private/release-ed25519.pub
```

Verification rejects unknown/special members, symlinks, non-canonical JSON,
future/invalid metadata, size or digest drift, missing required artifacts,
unsigned distributions, and signature mismatch. It rereads the exact bytes
that installation will use, so a source mutation between verification and
copying cannot silently become an installed artifact. Signing performs the same
manifest/member and static host-target validation before creating the detached
signature; it never executes the candidate. The private-key invocation is an
explicit operator signing authority, not an independent provenance attestation,
and the compatibility bundle digest is reconciled strictly.

## Install, rollback, uninstall

Installation accepts only one exact absent absolute prefix on the matching
host platform and writes an
owner-bounded `.incomplete` marker before creating files. On success the marker
is removed only after file and directory sync/readback. A failed installation
must be recovered explicitly; it is never silently treated as complete.

```sh
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode install --distribution /absolute/distribution \
  --public-key /owner-private/release-ed25519.pub \
  --prefix /absolute/new/prefix
```

Rollback is an explicit, verified replacement of an exact installed candidate
with a different signed candidate. It records a target-bound rollback marker,
validates the current installation before removal, verifies the replacement
before writing, and refuses uninstall while a marker remains. A process
interruption is therefore recoverable only by rerunning the same target-bound
rollback or by operator inspection; there is no automatic updater.

```sh
env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode rollback --distribution /absolute/previous-distribution \
  --public-key /owner-private/release-ed25519.pub \
  --prefix /absolute/installed/prefix

env -u GOROOT GOTOOLCHAIN=auto GOWORK=off \
  go run ./scripts/agent-eval-distribution \
  --mode uninstall --prefix /absolute/installed/prefix \
  --public-key /owner-private/release-ed25519.pub \
  --confirm 'UNINSTALL AGENT-EVAL'
```

Uninstall refuses malformed, incomplete, tampered, or extra-member installs
before deleting any file. The exact confirmation token is required. Directory
Windows persistence is explicitly unsupported in this release candidate; the
operation refuses before creating a prefix.

## Release boundary

The release candidate is not a supported release. A protected approval is
required after `make agent-eval-full`, reproducible two-build comparison,
offline conformance, installer/rollback evidence, supply-chain/privacy review,
and hosted CI. No command in this slice contacts a provider or backend or
publishes release material.
