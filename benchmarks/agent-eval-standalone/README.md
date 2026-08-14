# Neutral agent-eval conformance benchmark

This compact public fixture is a contract example, not a model leaderboard.
All bytes are synthetic and domain-neutral. The conformance test runs the
in-process reference adapter, hermetic reference backend, deterministic grader,
extension protocol, JUnit projection, and static-report lane without provider,
backend, network, credential, or private-workspace authority.

The ten cases cover positive skill lift, no lift, stale guidance, autonomous
activation, a near-miss negative, distractor selection, stateful multi-step
work, verifier isolation, resource tax, and lifecycle security. Their names
are stable identifiers only; they do not describe model quality.

The checked-in outputs include a content-addressed result projection, a small
statistics summary, and deterministic JUnit/HTML reports. They are fixtures for
codec and reporting conformance, not claims about agent quality.

Authors may replace the fake adapter with a real local agent only after
declaring its cost, network, credential, filesystem, process, and verifier
authority in the selected adapter/backend contracts. The reference fixture is
kept separate from the ATL Jira/Confluence corpus and generated product skills.
