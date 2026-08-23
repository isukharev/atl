package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type guardedLinkTracker struct {
	domain.Tracker
	types                         []domain.JiraLinkTypeMetadata
	left, right                   domain.JiraStrictLinkEndpoint
	added, deleted, applyMutation bool
	candidateIDs                  []string
	reverseRoles                  bool
	replacementAfterDelete        bool
	oneSidedAfterWrite            bool
	moveAfterWrite                bool
	sourceUpdatedAt               int
	targetUpdatedAt               int
	readErrAt, driftAt            int
	writeErr                      error
	reads, writes                 int
	write                         domain.JiraGuardedLinkWrite
}

func (t *guardedLinkTracker) charge(ctx context.Context) error {
	if !domain.SingleAttempt(ctx) {
		return errors.New("missing single-attempt policy")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing deadline")
	}
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil {
		return errors.New("missing aggregate budget")
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	_, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	finish(2)
	return nil
}

func (t *guardedLinkTracker) ReadStrictLinkTypes(ctx context.Context) (domain.JiraStrictLinkCatalog, error) {
	t.reads++
	if t.reads == t.driftAt {
		t.types[0].Name += " changed"
	}
	if err := t.charge(ctx); err != nil {
		return domain.JiraStrictLinkCatalog{}, err
	}
	return domain.JiraStrictLinkCatalog{Types: append([]domain.JiraLinkTypeMetadata(nil), t.types...), Complete: true}, nil
}

func (t *guardedLinkTracker) ReadStrictLinkEndpoint(ctx context.Context, ref string) (domain.JiraStrictLinkEndpoint, error) {
	t.reads++
	if t.reads == t.readErrAt {
		return domain.JiraStrictLinkEndpoint{}, errors.New("private backend response")
	}
	if err := t.charge(ctx); err != nil {
		return domain.JiraStrictLinkEndpoint{}, err
	}
	left, right := t.left, t.right
	if t.reads == t.sourceUpdatedAt {
		left.Updated = "2026-08-23T10:00:01Z"
	}
	if t.reads == t.targetUpdatedAt {
		right.Updated = "2026-08-23T10:00:01Z"
	}
	if t.added && (!t.deleted || t.replacementAfterDelete) {
		ids := t.candidateIDs
		if len(ids) == 0 {
			ids = []string{"90"}
		}
		typ := t.types[0]
		if t.deleted {
			ids, typ.Name = []string{"91"}, typ.Name+" changed"
		}
		for _, id := range ids {
			leftRows, rightRows := guardedReciprocalRows(typ, id, left, right)
			if t.reverseRoles {
				leftRows[0].Role, rightRows[0].Role = "inward", "outward"
			}
			left.Links, right.Links = append(left.Links, leftRows...), append(right.Links, rightRows...)
		}
		if t.writes > 0 && t.oneSidedAfterWrite {
			right.Links = nil
		}
	}
	if t.writes > 0 && t.moveAfterWrite {
		right.Key = "OPS-3"
	}
	if ref == left.ID || ref == left.Key {
		return left, nil
	}
	if ref == right.ID || ref == right.Key {
		return right, nil
	}
	return domain.JiraStrictLinkEndpoint{}, domain.ErrNotFound
}

func TestGuardedLinkPreparedEffectBindsOnlyRequestedSourceUpdated(t *testing.T) {
	preview := guardedLinkFixture(false)
	reviewed, err := NewJiraService(JiraDependencies{Tracker: preview, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		configure  func(*guardedLinkTracker)
		wantStatus string
		wantWrites int
	}{
		{name: "source drift", configure: func(port *guardedLinkTracker) { port.sourceUpdatedAt = 5 }, wantStatus: "blocked", wantWrites: 0},
		{name: "target drift is unrelated", configure: func(port *guardedLinkTracker) { port.targetUpdatedAt = 6 }, wantStatus: "applied", wantWrites: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := guardedLinkFixture(false)
			test.configure(port)
			result, err := NewJiraService(JiraDependencies{Tracker: port, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{
				Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks", Apply: true, ExpectedProposalHash: reviewed.ProposalHash,
			})
			if result == nil || result.Status != test.wantStatus || port.writes != test.wantWrites || (err != nil) != (test.wantStatus == "blocked") {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writes)
			}
			if result.ProposalHash != reviewed.ProposalHash {
				t.Fatalf("updated marker changed standalone proposal hash: %s != %s", result.ProposalHash, reviewed.ProposalHash)
			}
		})
	}
}

func (t *guardedLinkTracker) AddGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	t.writes++
	t.write = write
	if err := t.charge(ctx); err != nil {
		return err
	}
	if t.applyMutation {
		t.added = true
	}
	return t.writeErr
}

func (t *guardedLinkTracker) DeleteGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	t.writes++
	t.write = write
	if err := t.charge(ctx); err != nil {
		return err
	}
	if t.applyMutation {
		t.deleted = true
	}
	return t.writeErr
}

func guardedLinkFixture(neutral bool) *guardedLinkTracker {
	typ := domain.JiraLinkTypeMetadata{ID: "7", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}
	if neutral {
		typ = domain.JiraLinkTypeMetadata{ID: "8", Name: "Relates", Inward: "relates to", Outward: "relates to"}
	}
	return &guardedLinkTracker{types: []domain.JiraLinkTypeMetadata{typ},
		left:  domain.JiraStrictLinkEndpoint{ID: "10", Key: "APP-1", Project: "APP", Updated: "2026-08-23T10:00:00Z", UpdatedPresent: true, Complete: true},
		right: domain.JiraStrictLinkEndpoint{ID: "20", Key: "OPS-2", Project: "OPS", Updated: "2026-08-23T10:00:00Z", UpdatedPresent: true, Complete: true}, applyMutation: true}
}

func guardedReciprocalRows(typ domain.JiraLinkTypeMetadata, id string, outward, inward domain.JiraStrictLinkEndpoint) ([]domain.JiraStrictIssueLink, []domain.JiraStrictIssueLink) {
	return []domain.JiraStrictIssueLink{{ID: id, Type: typ, Role: "outward", OtherID: inward.ID, OtherKey: inward.Key}},
		[]domain.JiraStrictIssueLink{{ID: id, Type: typ, Role: "inward", OtherID: outward.ID, OtherKey: outward.Key}}
}

func TestGuardedLinkPreviewAndApplyUseExactClosedRequestPlans(t *testing.T) {
	previewTracker := guardedLinkFixture(false)
	preview, err := NewJiraService(JiraDependencies{Tracker: previewTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
	if err != nil || preview.Status != "would_apply" || previewTracker.reads != 3 || previewTracker.writes != 0 {
		t.Fatalf("preview=%+v err=%v reads/writes=%d/%d", preview, err, previewTracker.reads, previewTracker.writes)
	}
	applyTracker := guardedLinkFixture(false)
	result, err := NewJiraService(JiraDependencies{Tracker: applyTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks", Apply: true, ExpectedProposalHash: preview.ProposalHash})
	if err != nil || result.Status != "applied" || !result.WriteAttempted || !result.Reconciled || applyTracker.reads != 8 || applyTracker.writes != 1 {
		t.Fatalf("apply=%+v err=%v reads/writes=%d/%d", result, err, applyTracker.reads, applyTracker.writes)
	}
	if applyTracker.write.TypeID != "7" || applyTracker.write.Outward.ID != "10" || applyTracker.write.Inward.ID != "20" {
		t.Fatalf("write=%+v", applyTracker.write)
	}
}

func TestGuardedLinkNeutralHashIsStableAndIgnoresUnrelatedLinks(t *testing.T) {
	tracker := guardedLinkFixture(true)
	service := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"})
	forward, err := service.GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "relates to"})
	if err != nil {
		t.Fatal(err)
	}
	tracker.left.Links = []domain.JiraStrictIssueLink{{ID: "70", Type: tracker.types[0], Role: "outward", OtherID: "30", OtherKey: "APP-3"}}
	reverse, err := service.GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "OPS-2", To: "APP-1", Type: "relates to"})
	if err != nil {
		t.Fatal(err)
	}
	if forward.ProposalHash != reverse.ProposalHash {
		t.Fatalf("neutral hashes differ: %s %s", forward.ProposalHash, reverse.ProposalHash)
	}
}

func TestGuardedLinkNeutralAcceptsReverseStoredOrientation(t *testing.T) {
	addTracker := guardedLinkFixture(true)
	addTracker.added, addTracker.reverseRoles = true, true
	add, err := NewJiraService(JiraDependencies{Tracker: addTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "relates to"})
	if err != nil || add.Status != "already_satisfied" || len(add.Candidates) != 1 || add.Candidates[0].ID != "90" || addTracker.writes != 0 {
		t.Fatalf("add=%+v err=%v writes=%d", add, err, addTracker.writes)
	}

	previewTracker := guardedLinkFixture(true)
	previewTracker.added, previewTracker.reverseRoles = true, true
	opts := JiraGuardedLinkOpts{Operation: "delete", From: "APP-1", To: "OPS-2", Type: "relates to", LinkID: "90"}
	preview, err := NewJiraService(JiraDependencies{Tracker: previewTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
	if err != nil || preview.Status != "would_apply" || len(preview.Candidates) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	applyTracker := guardedLinkFixture(true)
	applyTracker.added, applyTracker.reverseRoles = true, true
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	result, err := NewJiraService(JiraDependencies{Tracker: applyTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
	if err != nil || result.Status != "applied" || !result.Reconciled || applyTracker.writes != 1 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, applyTracker.writes)
	}

	left, right := guardedReciprocalRows(applyTracker.types[0], "90", applyTracker.left, applyTracker.right)
	right[0].Role = left[0].Role
	if _, err := guardedLinkCandidates(domain.JiraStrictLinkEndpoint{ID: "10", Key: "APP-1", Links: left}, domain.JiraStrictLinkEndpoint{ID: "20", Key: "OPS-2", Links: right}, applyTracker.types[0]); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("same-role reciprocal evidence err=%v", err)
	}
}

func TestGuardedLinkResolutionAndCandidateRefusals(t *testing.T) {
	t.Run("collision", func(t *testing.T) {
		tracker := guardedLinkFixture(false)
		tracker.types = append(tracker.types, domain.JiraLinkTypeMetadata{ID: "8", Name: "blocks", Inward: "x", Outward: "y"})
		_, err := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
		if !errors.Is(err, domain.ErrCheckFailed) || tracker.writes != 0 {
			t.Fatalf("err=%v writes=%d", err, tracker.writes)
		}
	})
	t.Run("one sided", func(t *testing.T) {
		tracker := guardedLinkFixture(false)
		tracker.left.Links, _ = guardedReciprocalRows(tracker.types[0], "90", tracker.left, tracker.right)
		_, err := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "Blocks"})
		if !errors.Is(err, domain.ErrCheckFailed) || tracker.writes != 0 {
			t.Fatalf("err=%v writes=%d", err, tracker.writes)
		}
	})
}

func TestGuardedLinkDeleteRequiresExactCandidateAndProvesAbsence(t *testing.T) {
	previewTracker := guardedLinkFixture(false)
	previewTracker.added = true
	preview, err := NewJiraService(JiraDependencies{Tracker: previewTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "delete", From: "APP-1", To: "OPS-2", Type: "Blocks", LinkID: "90"})
	if err != nil || preview.Status != "would_apply" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	applyTracker := guardedLinkFixture(false)
	applyTracker.added = true
	result, err := NewJiraService(JiraDependencies{Tracker: applyTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "delete", From: "APP-1", To: "OPS-2", Type: "Blocks", LinkID: "90", Apply: true, ExpectedProposalHash: preview.ProposalHash})
	if err != nil || result.Status != "applied" || applyTracker.writes != 1 || applyTracker.write.LinkID != "90" {
		t.Fatalf("result=%+v err=%v write=%+v", result, err, applyTracker.write)
	}
	absent := guardedLinkFixture(false)
	blocked, err := NewJiraService(JiraDependencies{Tracker: absent, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "delete", From: "APP-1", To: "OPS-2", Type: "Blocks", LinkID: "90"})
	if !errors.Is(err, domain.ErrCheckFailed) || blocked == nil || blocked.Status != "blocked" || absent.writes != 0 {
		t.Fatalf("blocked=%+v err=%v writes=%d", blocked, err, absent.writes)
	}
}

func TestGuardedLinkProposalHashBindsEveryReviewedMember(t *testing.T) {
	baseline := func() JiraGuardedLinkResult {
		return JiraGuardedLinkResult{SchemaVersion: 1, Operation: "delete", BackendSHA256: "backend", RequestedFrom: "APP-1", RequestedTo: "OPS-2", RequestedType: "blocks", RequestedLinkID: "90",
			Outward: JiraGuardedLinkEndpoint{ID: "10", Key: "APP-1", Project: "APP", Role: "outward"}, Inward: JiraGuardedLinkEndpoint{ID: "20", Key: "OPS-2", Project: "OPS", Role: "inward"},
			Type: domain.JiraLinkTypeMetadata{ID: "7", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}, ResolvedRole: "outward", CatalogCount: 1, CatalogSHA256: "catalog",
			Candidates: []JiraGuardedLinkCandidate{{ID: "90", OutwardEvidence: true, InwardEvidence: true}}}
	}
	tests := []struct {
		name   string
		mutate func(*JiraGuardedLinkResult)
	}{
		{"schema", func(v *JiraGuardedLinkResult) { v.SchemaVersion++ }}, {"operation", func(v *JiraGuardedLinkResult) { v.Operation = "add" }},
		{"backend", func(v *JiraGuardedLinkResult) { v.BackendSHA256 += "x" }}, {"requested from", func(v *JiraGuardedLinkResult) { v.RequestedFrom = "APP-2" }},
		{"requested to", func(v *JiraGuardedLinkResult) { v.RequestedTo = "OPS-3" }}, {"requested type", func(v *JiraGuardedLinkResult) { v.RequestedType += "x" }},
		{"link id", func(v *JiraGuardedLinkResult) { v.RequestedLinkID = "91" }}, {"outward id", func(v *JiraGuardedLinkResult) { v.Outward.ID = "11" }},
		{"outward key", func(v *JiraGuardedLinkResult) { v.Outward.Key = "APP-2" }}, {"outward project", func(v *JiraGuardedLinkResult) { v.Outward.Project = "ALT" }},
		{"outward role", func(v *JiraGuardedLinkResult) { v.Outward.Role = "inward" }}, {"inward id", func(v *JiraGuardedLinkResult) { v.Inward.ID = "21" }},
		{"inward key", func(v *JiraGuardedLinkResult) { v.Inward.Key = "OPS-3" }}, {"inward project", func(v *JiraGuardedLinkResult) { v.Inward.Project = "ALT" }},
		{"inward role", func(v *JiraGuardedLinkResult) { v.Inward.Role = "outward" }}, {"type id", func(v *JiraGuardedLinkResult) { v.Type.ID = "8" }},
		{"type name", func(v *JiraGuardedLinkResult) { v.Type.Name += "x" }}, {"type inward", func(v *JiraGuardedLinkResult) { v.Type.Inward += "x" }},
		{"type outward", func(v *JiraGuardedLinkResult) { v.Type.Outward += "x" }}, {"resolved role", func(v *JiraGuardedLinkResult) { v.ResolvedRole = "inward" }},
		{"catalog count", func(v *JiraGuardedLinkResult) { v.CatalogCount++ }}, {"catalog digest", func(v *JiraGuardedLinkResult) { v.CatalogSHA256 += "x" }},
		{"candidate id", func(v *JiraGuardedLinkResult) { v.Candidates[0].ID = "91" }}, {"outward evidence", func(v *JiraGuardedLinkResult) { v.Candidates[0].OutwardEvidence = false }},
		{"inward evidence", func(v *JiraGuardedLinkResult) { v.Candidates[0].InwardEvidence = false }},
	}
	want := baseline()
	wantHash := guardedLinkProposalHash(&want)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := baseline()
			test.mutate(&got)
			if guardedLinkProposalHash(&got) == wantHash {
				t.Fatal("reviewed member was not bound")
			}
		})
	}
}

func TestGuardedLinkSelectorRolesAndPrewriteDrift(t *testing.T) {
	typ := domain.JiraLinkTypeMetadata{ID: "7", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}
	for selector, want := range map[string]string{" Blocks ": "outward", "BLOCKS": "outward", " IS BLOCKED BY ": "inward"} {
		_, got, err := resolveGuardedLinkType([]domain.JiraLinkTypeMetadata{typ}, selector)
		if err != nil || got != want {
			t.Errorf("selector %q role=%q err=%v, want %q", selector, got, err, want)
		}
	}
	tracker := guardedLinkFixture(false)
	preview, err := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
	if err != nil {
		t.Fatal(err)
	}
	drift := guardedLinkFixture(false)
	drift.driftAt = 4
	result, err := NewJiraService(JiraDependencies{Tracker: drift, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks", Apply: true, ExpectedProposalHash: preview.ProposalHash})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || drift.writes != 0 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, drift.writes)
	}
}

func TestGuardedLinkClosedWriteAndReadbackOutcomes(t *testing.T) {
	tests := []struct {
		name, operation, wantStatus string
		configure                   func(*guardedLinkTracker)
		wantErr, wantAttempted      bool
	}{
		{"add applied", "add", "applied", nil, false, true},
		{"add recovered", "add", "recovered", func(v *guardedLinkTracker) { v.writeErr = errors.New("private response") }, false, true},
		{"add absent", "add", "outcome_unknown", func(v *guardedLinkTracker) { v.applyMutation = false }, true, true},
		{"add duplicate", "add", "outcome_unknown", func(v *guardedLinkTracker) { v.candidateIDs = []string{"90", "91"} }, true, true},
		{"add reciprocal conflict", "add", "outcome_unknown", func(v *guardedLinkTracker) { v.oneSidedAfterWrite = true }, true, true},
		{"add moved endpoint", "add", "outcome_unknown", func(v *guardedLinkTracker) { v.moveAfterWrite = true }, true, true},
		{"add unavailable", "add", "outcome_unknown", func(v *guardedLinkTracker) { v.readErrAt = 7 }, true, true},
		{"add definitive rejection", "add", "not_applied", func(v *guardedLinkTracker) { v.applyMutation = false; v.writeErr = notAttemptedTestError{} }, true, false},
		{"delete recovered", "delete", "recovered", func(v *guardedLinkTracker) { v.added = true; v.writeErr = errors.New("private response") }, false, true},
		{"delete retained", "delete", "outcome_unknown", func(v *guardedLinkTracker) { v.added = true; v.applyMutation = false }, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previewTracker := guardedLinkFixture(false)
			if test.operation == "delete" {
				previewTracker.added = true
			}
			opts := JiraGuardedLinkOpts{Operation: test.operation, From: "APP-1", To: "OPS-2", Type: "blocks"}
			if test.operation == "delete" {
				opts.LinkID = "90"
			}
			preview, err := NewJiraService(JiraDependencies{Tracker: previewTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			tracker := guardedLinkFixture(false)
			if test.configure != nil {
				test.configure(tracker)
			}
			opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
			result, err := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
			if (err != nil) != test.wantErr || result == nil || result.Status != test.wantStatus || result.WriteAttempted != test.wantAttempted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("error leaked backend content: %v", err)
			}
		})
	}
}

func TestGuardedLinkDeleteRejectsPairLocalReplacementWithDriftedTypeMetadata(t *testing.T) {
	previewTracker := guardedLinkFixture(false)
	previewTracker.added = true
	opts := JiraGuardedLinkOpts{Operation: "delete", From: "APP-1", To: "OPS-2", Type: "blocks", LinkID: "90"}
	preview, err := NewJiraService(JiraDependencies{Tracker: previewTracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	tracker := guardedLinkFixture(false)
	tracker.added, tracker.replacementAfterDelete = true, true
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	result, err := NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.test"}).GuardedLink(t.Context(), opts)
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result == nil || result.Status != "outcome_unknown" || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("result=%+v err=%v ambiguous=%T", result, err, ambiguous)
	}
}
