package app

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedWriteDecisionConformance(t *testing.T) {
	tests := []struct {
		name             string
		apply            bool
		expected         string
		proposal         string
		alreadySatisfied bool
		wantMode         string
		wantStatus       string
		wantMismatch     bool
		wantWrite        bool
	}{
		{name: "preview", proposal: "proposal", wantMode: "dry-run", wantStatus: "would_apply"},
		{name: "preview already satisfied", proposal: "proposal", alreadySatisfied: true, wantMode: "dry-run", wantStatus: "already_satisfied"},
		{name: "apply", apply: true, expected: "proposal", proposal: "proposal", wantMode: "apply", wantStatus: "would_apply", wantWrite: true},
		{name: "apply already satisfied", apply: true, expected: "proposal", proposal: "proposal", alreadySatisfied: true, wantMode: "apply", wantStatus: "already_satisfied"},
		{name: "changed proposal wins over satisfaction", apply: true, expected: "old", proposal: "proposal", alreadySatisfied: true, wantMode: "apply", wantStatus: "blocked", wantMismatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := newGuardedWritePolicy(test.apply, test.expected)
			if err != nil {
				t.Fatal(err)
			}
			decision := policy.decide(test.proposal, test.alreadySatisfied)
			if decision.mode != test.wantMode || decision.status != test.wantStatus ||
				decision.proposalHash != test.proposal || decision.hashMismatch != test.wantMismatch ||
				decision.writeRequired != test.wantWrite {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestGuardedWriteDecisionRequiresReviewedHashBeforeStateReads(t *testing.T) {
	if _, err := newGuardedWritePolicy(true, "  "); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("missing hash error=%v", err)
	}
	policy, err := newGuardedWritePolicy(true, " reviewed ")
	if err != nil || policy.expectedHash != "reviewed" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
}
