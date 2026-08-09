package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type guardedFeatureObservation struct {
	mode         string
	status       string
	proposalHash string
	writes       int
	err          error
}

func TestFocusedGuardedWriteFeatureConformance(t *testing.T) {
	tests := []struct {
		name     string
		exercise func(string, bool) guardedFeatureObservation
	}{
		{
			name: "confluence labels",
			exercise: func(expected string, ambiguous bool) guardedFeatureObservation {
				store := &confluenceLabelStoreStub{}
				if ambiguous {
					store.writeErr = errors.New("connection lost")
					store.verificationErr = errors.New("verification unavailable")
				}
				service := newConfluenceLabelServiceForTest(store)
				opts := ConfluenceLabelMutationOpts{Operation: "add", Labels: []string{"reviewed"}}
				if expected != "" {
					opts.Apply = true
					opts.ExpectedProposalHash = expected
				}
				result, err := service.MutateLabelsGuarded(context.Background(), "42", opts)
				if result == nil {
					return guardedFeatureObservation{err: err}
				}
				return guardedFeatureObservation{mode: result.Mode, status: result.Status, proposalHash: result.ProposalHash, writes: store.addCalls, err: err}
			},
		},
		{
			name: "jira watchers",
			exercise: func(expected string, ambiguous bool) guardedFeatureObservation {
				store := &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}}
				if ambiguous {
					store.writeErr = errors.New("connection lost")
					store.verificationErr = errors.New("verification unavailable")
				}
				service := newJiraWatcherServiceForTest(store)
				opts := JiraWatcherMutationOpts{Operation: "add", Username: "reviewed"}
				if expected != "" {
					opts.Apply = true
					opts.ExpectedProposalHash = expected
				}
				result, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", opts)
				if result == nil {
					return guardedFeatureObservation{err: err}
				}
				return guardedFeatureObservation{mode: result.Mode, status: result.Status, proposalHash: result.ProposalHash, writes: store.addCalls, err: err}
			},
		},
		{
			name: "jira worklogs",
			exercise: func(expected string, ambiguous bool) guardedFeatureObservation {
				store := &jiraWorklogStoreStub{current: domain.User{Name: "reviewed"}}
				if ambiguous {
					store.addErr = errors.New("connection lost")
				}
				service := newJiraWorklogServiceForTest(store)
				opts := JiraWorklogAddOpts{Time: "1m", Started: "2026-08-01T10:00:00Z"}
				if expected != "" {
					opts.Apply = true
					opts.ExpectedProposalHash = expected
				}
				result, err := service.AddWorklogGuarded(context.Background(), "PROJ-1", opts)
				if result == nil {
					return guardedFeatureObservation{err: err}
				}
				return guardedFeatureObservation{mode: result.Mode, status: result.Status, proposalHash: result.ProposalHash, writes: store.addCalls, err: err}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview := test.exercise("", false)
			if preview.err != nil || preview.mode != "dry-run" || preview.status != "would_apply" || preview.proposalHash == "" || preview.writes != 0 {
				t.Fatalf("preview=%+v", preview)
			}

			blocked := test.exercise("stale", false)
			if !errors.Is(blocked.err, domain.ErrCheckFailed) || blocked.mode != "apply" || blocked.status != "blocked" || blocked.writes != 0 {
				t.Fatalf("blocked=%+v", blocked)
			}

			unknown := test.exercise(preview.proposalHash, true)
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if unknown.mode != "apply" || unknown.status != "unknown" || unknown.writes != 1 ||
				!errors.As(unknown.err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || errors.Is(unknown.err, domain.ErrCheckFailed) {
				t.Fatalf("unknown=%+v", unknown)
			}
		})
	}
}
