# Neutral agent-eval conformance corpus

The public neutral corpus at
[`benchmarks/agent-eval-standalone/README.md`](../../benchmarks/agent-eval-standalone/README.md)
is a provider-free contract fixture for the reusable evaluator layers. It is
separate from the ATL Jira/Confluence benchmark and from generated client
skills. The fixture covers ten synthetic families: skill lift, no lift, stale
guidance, autonomous activation, near-miss negatives, distractors, stateful
steps, verifier isolation, resource tax, and lifecycle security.

The conformance tests run a deterministic in-process adapter, hermetic
reference backend, generic grader, extension protocol, JUnit projection, and
static report. They bind the expected result projection and corpus file tree
to SHA-256 fixtures. No provider, backend, network, credential, private
workspace, or model quality claim is part of this lane.

Run the bounded contract lane while iterating, then the complete evaluator
lane at a stable reviewed boundary:

```sh
make agent-eval-contract
make agent-eval-full
```

Replacing the fake adapter with a real local agent is a separate reviewed
change. The replacement must declare cost, network, credential, filesystem,
process, and verifier authority in the selected adapter and execution-backend
contracts, and must retain the same content-addressed fixture and report
boundaries. A real agent must not be connected to a configured provider or
private benchmark merely by changing this public fixture.
