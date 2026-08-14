# Standalone agent-eval support policy

This page is the maintainer-owned support contour for the standalone
`agent-eval` proposal. Its machine-readable source is
[`agent-eval-support.v1.json`](agent-eval-support.v1.json); the JSON is
canonical, and this page explains the deliberately conservative status.

## Current status

The standalone evaluator is `pre_release`: source-implemented compatibility
surfaces and provider-free fixtures exist, but there is no signed distribution,
stable compatibility clock, or supported external consumer. Internal tests and
source presence do not create support. The first stable claim requires the listed prerequisites in the policy file and a separately approved release.

The support owner is the ATL repository maintainer through the repository
[security route](../../SECURITY.md). Before stable support, an out-of-tree
provider-free conformance consumer must be named and independently exercised;
the policy intentionally records that evidence as pending instead of inventing
a consumer or treating an internal package as one.

## Candidate contour

The only candidate platform is Linux/amd64 for the provider-free process
surface. Windows persistence and the Darwin/Linux-arm64 signed distribution
matrix are explicitly excluded until owner-only storage, reproducibility, and
hosted evidence are proven. Containers and GitHub Actions are not part of this
contour; they are separately scoped to issue #1389.

The compatibility bundle is content-addressed and must be verified against the
selected binary, schema registry, process protocol, and exact source identity.
Future schema generations refuse rather than downgrade. Rollback is a release prerequisite, not an automatic updater or an implicit source rewrite.

## Security and lifecycle

Security reports use `SECURITY.md`. Until stable policy approval, response
timing is best-effort and no service-level promise is made. There are no automatic updates, provider credentials, backend access, or network access in the candidate process boundary.

The deprecation shape is 180 days and two later stable minor releases after
notice, with removal requiring a later major release. That clock starts only
at the first conforming signed standalone release; pre-release source builds do
not consume it. A security issue may disable execution earlier when necessary,
while historical artifact meaning remains explicit and never silently changes.

Actual release publication, tag creation, uploads, container/Action
publication, and repository extraction require separate exact authority.
