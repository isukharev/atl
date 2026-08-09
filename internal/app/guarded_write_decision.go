package app

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// guardedWritePolicy owns only the shared, pure preview/apply decision. The
// feature owner still owns proposal bytes, transport calls, reconciliation,
// result fields, and operation-specific error wording.
type guardedWritePolicy struct {
	apply        bool
	expectedHash string
}

type guardedWriteDecision struct {
	mode          string
	status        string
	proposalHash  string
	hashMismatch  bool
	writeRequired bool
}

func newGuardedWritePolicy(apply bool, expectedHash string) (guardedWritePolicy, error) {
	expectedHash = strings.TrimSpace(expectedHash)
	if apply && expectedHash == "" {
		return guardedWritePolicy{}, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	return guardedWritePolicy{apply: apply, expectedHash: expectedHash}, nil
}

func (policy guardedWritePolicy) decide(proposalHash string, alreadySatisfied bool) guardedWriteDecision {
	decision := guardedWriteDecision{
		mode: "dry-run", status: "would_apply", proposalHash: proposalHash,
	}
	if policy.apply {
		decision.mode = "apply"
	}
	if policy.apply && policy.expectedHash != proposalHash {
		decision.status = "blocked"
		decision.hashMismatch = true
		return decision
	}
	if alreadySatisfied {
		decision.status = "already_satisfied"
		return decision
	}
	decision.writeRequired = policy.apply
	return decision
}
