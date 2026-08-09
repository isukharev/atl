package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type inverseReferenceTestTracker struct {
	domain.Tracker
	pages           []domain.JiraInverseReferencePage
	selectionErrs   map[int]error
	selections      []domain.JiraInverseReferenceSelection
	snapshots       map[string]domain.JiraInverseReferenceSnapshot
	snapshotErr     error
	snapshotCalls   int
	comments        map[string][]domain.Comment
	commentsErr     error
	commentCalls    int
	worklogs        map[string]*domain.IssueWorklogList
	worklogsErr     error
	remoteLinks     map[string]domain.JiraRemoteLinkInventory
	remoteErr       error
	development     map[string]domain.JiraDevelopmentInventory
	developmentErr  error
	consumeBytes    int64
	budgets         []*domain.ReadBudget
	untrustedTarget bool
}

func (t *inverseReferenceTestTracker) consume(ctx context.Context) error {
	t.untrustedTarget = t.untrustedTarget || domain.UntrustedConfluenceReference(ctx)
	budget := domain.ReadBudgetFromContext(ctx)
	t.budgets = append(t.budgets, budget)
	if budget == nil || t.consumeBytes == 0 {
		return nil
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if t.consumeBytes > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(t.consumeBytes)
	return nil
}

func (t *inverseReferenceTestTracker) SelectInverseReferencePage(ctx context.Context, selection domain.JiraInverseReferenceSelection) (domain.JiraInverseReferencePage, error) {
	call := len(t.selections)
	t.selections = append(t.selections, selection)
	if err := t.consume(ctx); err != nil {
		return domain.JiraInverseReferencePage{}, err
	}
	if err := t.selectionErrs[call]; err != nil {
		return domain.JiraInverseReferencePage{}, err
	}
	if call >= len(t.pages) {
		return domain.JiraInverseReferencePage{}, fmt.Errorf("unexpected selection call %d", call)
	}
	return t.pages[call], nil
}

func (t *inverseReferenceTestTracker) ReadInverseReferenceSnapshot(ctx context.Context, request domain.JiraInverseReferenceSnapshotRequest) (domain.JiraInverseReferenceSnapshot, error) {
	t.snapshotCalls++
	if err := t.consume(ctx); err != nil {
		return domain.JiraInverseReferenceSnapshot{}, err
	}
	if t.snapshotErr != nil {
		return domain.JiraInverseReferenceSnapshot{}, t.snapshotErr
	}
	return t.snapshots[request.Issue.Key], nil
}

func (t *inverseReferenceTestTracker) ListComments(ctx context.Context, key string) ([]domain.Comment, error) {
	t.commentCalls++
	if err := t.consume(ctx); err != nil {
		return nil, err
	}
	if t.commentsErr != nil {
		return nil, t.commentsErr
	}
	return t.comments[key], nil
}

func (t *inverseReferenceTestTracker) ListIssueWorklogs(ctx context.Context, key string) (*domain.IssueWorklogList, error) {
	if err := t.consume(ctx); err != nil {
		return nil, err
	}
	if t.worklogsErr != nil {
		return nil, t.worklogsErr
	}
	return t.worklogs[key], nil
}

func (t *inverseReferenceTestTracker) ReadIssueRemoteLinks(ctx context.Context, key string) (domain.JiraRemoteLinkInventory, error) {
	if err := t.consume(ctx); err != nil {
		return domain.JiraRemoteLinkInventory{}, err
	}
	if t.remoteErr != nil {
		return domain.JiraRemoteLinkInventory{}, t.remoteErr
	}
	return t.remoteLinks[key], nil
}

func (t *inverseReferenceTestTracker) ReadIssueDevelopment(ctx context.Context, issueID string) (domain.JiraDevelopmentInventory, error) {
	if err := t.consume(ctx); err != nil {
		return domain.JiraDevelopmentInventory{}, err
	}
	if t.developmentErr != nil {
		return domain.JiraDevelopmentInventory{}, t.developmentErr
	}
	return t.development[issueID], nil
}

type inverseReferenceTestResolver struct {
	calls     int
	id        string
	budget    *domain.ReadBudget
	untrusted bool
}

func (r *inverseReferenceTestResolver) ResolvePageReference(ctx context.Context, _ string) (*ConfluencePageResolution, error) {
	r.calls++
	if budget := domain.ReadBudgetFromContext(ctx); budget != nil {
		r.budget = budget
		if err := budget.TakeAttempt(); err != nil {
			return nil, err
		}
	}
	return &ConfluencePageResolution{ID: r.id, Kind: "display", NetworkRequests: 1, untrusted: r.untrusted}, nil
}

func inverseReferenceTestOptions() JiraInverseReferenceOptions {
	return JiraInverseReferenceOptions{
		Target: "https://git.example.test/group/repo", TargetKind: domain.JiraInverseReferenceTargetGitLabProject,
		ScopeJQL: "project = SAFE", Mode: domain.JiraInverseReferenceModeExhaustive,
		Sources:   []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceComments},
		MaxIssues: 10, MaxRequests: 100, MaxResponseBytes: 1 << 20,
	}
}

func inverseReferenceEmptyPage(max int) domain.JiraInverseReferencePage {
	return domain.JiraInverseReferencePage{StartAt: 0, MaxResults: max, Total: 0, Issues: []domain.JiraInverseReferenceIssueIdentity{}}
}

func TestInverseReferenceValidationPrecedesReaders(t *testing.T) {
	tracker := &inverseReferenceTestTracker{}
	service := &JiraService{tr: tracker}
	base := inverseReferenceTestOptions()
	tests := []JiraInverseReferenceOptions{
		{},
		func() JiraInverseReferenceOptions {
			value := base
			value.ScopeJQL = `summary ~ "ORDER BY" ORDER BY key`
			return value
		}(),
		func() JiraInverseReferenceOptions { value := base; value.Mode = "all"; return value }(),
		func() JiraInverseReferenceOptions {
			value := base
			value.Sources = append(value.Sources, value.Sources[0])
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceFields}
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.MaxIssues = jiraInverseReferenceMaxIssues + 1
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.Target = "https://docs.example.test/wiki/spaces/SAFE/pages/42/" + string([]byte{0xff})
			value.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.Target = "https://docs.example.test/wiki/spaces/SAFE/pages/42/\uFFFD"
			value.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.Target = "https://docs.example.test/wiki/spaces/SAFE/pages/42/%FF"
			value.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.ScopeJQL += string([]byte{0xff})
			return value
		}(),
		func() JiraInverseReferenceOptions {
			value := base
			value.ScopeJQL += " \uFFFD"
			return value
		}(),
	}
	for _, opts := range tests {
		if _, err := service.SearchInverseReferences(t.Context(), opts); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("SearchInverseReferences error=%v, want usage", err)
		}
	}
	if len(tracker.selections) != 0 || tracker.snapshotCalls != 0 || len(tracker.budgets) != 0 {
		t.Fatalf("validation reached readers: selections=%d snapshots=%d budgets=%d", len(tracker.selections), tracker.snapshotCalls, len(tracker.budgets))
	}
	normalized, err := NormalizeJiraInverseReferenceOptions(func() JiraInverseReferenceOptions {
		value := base
		value.ScopeJQL = `summary ~ "ORDER BY"`
		return value
	}())
	if err != nil || normalized.ScopeJQL == "" {
		t.Fatalf("quoted ORDER BY rejected: normalized=%+v err=%v", normalized, err)
	}
}

func TestInverseReferenceGitLabTargetIsOfflineCanonicalAndFastIncomplete(t *testing.T) {
	var opaque string
	for _, target := range []string{
		"https://Git.Example.Test:443/Group/Repo.git/",
		"https://git.example.test/Group/Repo",
	} {
		opts := inverseReferenceTestOptions()
		opts.Target, opts.Mode = target, domain.JiraInverseReferenceModeFast
		tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{inverseReferenceEmptyPage(opts.MaxIssues)}}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if result.Complete || result.Selection.Complete || result.Selection.Reason != JiraInverseReferenceReasonModeFast || result.AbsenceProven {
			t.Fatalf("fast completeness=%+v", result)
		}
		if got := tracker.selections[0].Target.Value; got != "https://git.example.test/Group/Repo" {
			t.Fatalf("canonical target=%q", got)
		}
		if opaque == "" {
			opaque = result.Target.OpaqueID
		} else if result.Target.OpaqueID != opaque {
			t.Fatalf("opaque IDs differ: %q != %q", result.Target.OpaqueID, opaque)
		}
		if !strings.Contains(tracker.selections[0].JQL, "ORDER BY key ASC") || strings.Contains(strings.ToUpper(strings.TrimSuffix(tracker.selections[0].JQL, "ORDER BY key ASC")), "ORDER BY") {
			t.Fatalf("qualified fast JQL=%q", tracker.selections[0].JQL)
		}
	}
}

func TestInverseReferenceConfluenceTargetNormalizesDefaultHTTPSPort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   string
		target string
	}{
		{name: "target explicit", base: "https://docs.example.test/wiki", target: "https://docs.example.test:443/wiki/pages/viewpage.action?pageId=42"},
		{name: "base explicit", base: "https://docs.example.test:443/wiki", target: "https://docs.example.test/wiki/pages/viewpage.action?pageId=42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Target = tc.target
			opts.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{
				inverseReferenceEmptyPage(opts.MaxIssues), inverseReferenceEmptyPage(opts.MaxIssues),
			}}
			result, err := (&JiraService{tr: tracker, inverseConfluenceBaseURL: tc.base}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result == nil || !result.Complete || !result.AbsenceProven || len(tracker.selections) != 2 {
				t.Fatalf("result=%+v err=%v selections=%d", result, err, len(tracker.selections))
			}
		})
	}
}

func TestInverseReferenceConfluenceTargetThreadsResolutionProvenance(t *testing.T) {
	for name, target := range map[string]string{
		"direct URL":  "https://docs.example.test/wiki/pages/viewpage.action?pageId=42",
		"display URL": "https://docs.example.test/wiki/display/SAFE/Page",
	} {
		t.Run(name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Target = target
			opts.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{
				inverseReferenceEmptyPage(opts.MaxIssues), inverseReferenceEmptyPage(opts.MaxIssues),
			}}
			resolver := &inverseReferenceTestResolver{id: "42", untrusted: true}
			result, err := (&JiraService{
				tr: tracker, inverseConfluenceBaseURL: "https://docs.example.test/wiki", inverseConfluence: resolver,
			}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result == nil || !result.Complete || !tracker.untrustedTarget {
				t.Fatalf("result=%+v err=%v untrusted=%t", result, err, tracker.untrustedTarget)
			}
			if name == "direct URL" && resolver.calls != 0 {
				t.Fatalf("direct URL resolver calls=%d", resolver.calls)
			}
			if name == "display URL" && resolver.calls != 1 {
				t.Fatalf("display URL resolver calls=%d", resolver.calls)
			}
		})
	}
}

func TestInverseReferenceExhaustiveTwoPassEmptyProvesAbsence(t *testing.T) {
	opts := inverseReferenceTestOptions()
	tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{inverseReferenceEmptyPage(opts.MaxIssues), inverseReferenceEmptyPage(opts.MaxIssues)}}
	result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.AbsenceProven || len(tracker.selections) != 2 || result.Counts.ScannedIssues != 0 || !result.Usage.Reconciled {
		t.Fatalf("result=%+v calls=%d", result, len(tracker.selections))
	}
	if tracker.selections[0].JQL != tracker.selections[1].JQL || tracker.selections[0].Order != domain.JiraInverseReferenceOrderAscending || tracker.selections[1].Order != domain.JiraInverseReferenceOrderAscending {
		t.Fatalf("passes are not the same stable order: %+v", tracker.selections)
	}
}

func TestInverseReferencePaginationDriftAndCapAreQualified(t *testing.T) {
	identity := func(id, key string) domain.JiraInverseReferenceIssueIdentity {
		return domain.JiraInverseReferenceIssueIdentity{ID: id, Key: key}
	}
	t.Run("drift", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		tracker := &inverseReferenceTestTracker{
			pages: []domain.JiraInverseReferencePage{
				{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{identity("1", "SAFE-1")}},
				{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{identity("2", "SAFE-2")}},
			},
			comments: map[string][]domain.Comment{"SAFE-1": {}, "SAFE-2": {}},
		}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || result.Selection.Reason != JiraInverseReferenceReasonSelectionDrift || result.Complete || result.AbsenceProven || result.Counts.SelectedIssues != 2 || result.Counts.ScannedIssues != 2 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("drift union exceeds issue cap", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.MaxIssues = 2
		tracker := &inverseReferenceTestTracker{
			pages: []domain.JiraInverseReferencePage{
				{StartAt: 0, MaxResults: 2, Total: 2, Issues: []domain.JiraInverseReferenceIssueIdentity{identity("1", "SAFE-1"), identity("2", "SAFE-2")}},
				{StartAt: 0, MaxResults: 2, Total: 2, Issues: []domain.JiraInverseReferenceIssueIdentity{identity("3", "SAFE-3"), identity("4", "SAFE-4")}},
			},
			comments: map[string][]domain.Comment{"SAFE-1": {}, "SAFE-2": {}},
		}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || result.Selection.Reason != JiraInverseReferenceReasonSelectionDrift || result.Complete ||
			result.Counts.CandidateIssues != 4 || result.Counts.SelectedIssues != 2 || result.Counts.ScannedIssues != 4 || !result.Reconciliation.Counts {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("cap", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.MaxIssues = 2
		tracker := &inverseReferenceTestTracker{
			pages:    []domain.JiraInverseReferencePage{{StartAt: 0, MaxResults: 2, Total: 3, Issues: []domain.JiraInverseReferenceIssueIdentity{identity("1", "SAFE-1"), identity("2", "SAFE-2")}}},
			comments: map[string][]domain.Comment{"SAFE-1": {}, "SAFE-2": {}},
		}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || result.Selection.Reason != JiraInverseReferenceReasonIssueLimit || result.Counts.CandidateIssues != 3 || result.Counts.SelectedIssues != 2 || result.Frontier.Pass != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestInverseReferenceConfluenceDirectAndStructuredMatchingNeverResolveDiscoveredURL(t *testing.T) {
	opts := inverseReferenceTestOptions()
	opts.Target = "https://docs.example.test/wiki/pages/viewpage.action?pageId=42"
	opts.TargetKind = domain.JiraInverseReferenceTargetConfluencePage
	opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceRemoteLinks}
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	tracker := &inverseReferenceTestTracker{
		pages: []domain.JiraInverseReferencePage{
			{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
			{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
		},
		remoteLinks: map[string]domain.JiraRemoteLinkInventory{"SAFE-1": {
			Total: 1, Links: []domain.JiraRemoteLink{{ID: "7", ApplicationType: confluenceRemoteApplicationType,
				ObjectURL: "https://docs.example.test/wiki/spaces/SAFE/pages/42/Page", GlobalID: "appId=opaque&pageId=42"}},
		}},
	}
	resolver := &inverseReferenceTestResolver{id: "999"}
	service := &JiraService{tr: tracker, inverseConfluenceBaseURL: "https://docs.example.test/wiki", inverseConfluence: resolver}
	result, err := service.SearchInverseReferences(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 0 || !result.Complete || len(result.Matches) != 1 || result.Matches[0].Relation != JiraInverseReferenceRelationStructuredRemoteLink || result.Matches[0].Confidence != "exact" {
		t.Fatalf("result=%+v resolver calls=%d", result, resolver.calls)
	}

	tracker.remoteLinks["SAFE-1"] = domain.JiraRemoteLinkInventory{Total: 1, Links: []domain.JiraRemoteLink{{ID: "7", ApplicationType: confluenceRemoteApplicationType,
		ObjectURL: "https://docs.example.test/wiki/spaces/SAFE/pages/42/Page", GlobalID: "appId=opaque&pageId=43"}}}
	tracker.selections = nil
	tracker.pages = tracker.pages[:2]
	result, err = service.SearchInverseReferences(t.Context(), opts)
	if err != nil || result.Complete || len(result.Matches) != 0 || result.AbsenceProven {
		t.Fatalf("conflicting globalId result=%+v err=%v", result, err)
	}

	tracker.remoteLinks["SAFE-1"] = domain.JiraRemoteLinkInventory{Total: 1, Links: []domain.JiraRemoteLink{{ID: "7", ApplicationType: confluenceRemoteApplicationType,
		ObjectURL: "https://docs.example.test/wiki/spaces/SAFE/pages/42/%FF", GlobalID: "appId=opaque&pageId=42"}}}
	tracker.selections = nil
	result, err = service.SearchInverseReferences(t.Context(), opts)
	if err != nil || result.Complete || len(result.Matches) != 0 || result.AbsenceProven || result.SourceCounts[0].Partial != 1 {
		t.Fatalf("malformed URL result=%+v err=%v", result, err)
	}

	for name, link := range map[string]domain.JiraRemoteLink{
		"absent globalId direct fallback": {ID: "7", ApplicationType: confluenceRemoteApplicationType, ObjectURL: "https://docs.example.test/wiki/spaces/SAFE/pages/42/Page"},
		"wrong app literal":               {ID: "7", ApplicationType: "com.example.other", ObjectURL: "HTTPS://docs.example.test:443/wiki/spaces/SAFE/pages/42/Page"},
	} {
		t.Run(name, func(t *testing.T) {
			tracker.remoteLinks["SAFE-1"] = domain.JiraRemoteLinkInventory{Total: 1, Links: []domain.JiraRemoteLink{link}}
			tracker.selections = nil
			result, err := service.SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || len(result.Matches) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			wantRelation, wantConfidence := JiraInverseReferenceRelationStructuredRemoteLink, "high"
			if name == "wrong app literal" {
				wantRelation, wantConfidence = JiraInverseReferenceRelationLiteral, "high"
			}
			if result.Matches[0].Relation != wantRelation || result.Matches[0].Confidence != wantConfidence {
				t.Fatalf("match=%+v", result.Matches[0])
			}
		})
	}
}

func TestInverseReferenceDisplayResolutionSharesBudget(t *testing.T) {
	opts := inverseReferenceTestOptions()
	opts.Target, opts.TargetKind = "https://docs.example.test/wiki/display/SAFE/Page", domain.JiraInverseReferenceTargetConfluencePage
	opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription}
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	tracker := &inverseReferenceTestTracker{
		pages: []domain.JiraInverseReferencePage{page, page}, consumeBytes: 5,
		snapshots: map[string]domain.JiraInverseReferenceSnapshot{issue.Key: {Issue: issue, Fields: []domain.JiraInverseReferenceFieldSnapshot{{FieldID: "description", Present: true, Value: json.RawMessage("null")}}}},
	}
	resolver := &inverseReferenceTestResolver{id: "42"}
	result, err := (&JiraService{tr: tracker, inverseConfluenceBaseURL: "https://docs.example.test/wiki", inverseConfluence: resolver}).SearchInverseReferences(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || result.Usage.Requests != 4 || result.Usage.ResponseBytes != 15 || len(tracker.budgets) != 3 ||
		tracker.budgets[0] != tracker.budgets[1] || tracker.budgets[1] != tracker.budgets[2] || resolver.budget != tracker.budgets[0] {
		t.Fatalf("resolver=%d usage=%+v budgets=%v", resolver.calls, result.Usage, tracker.budgets)
	}
}

func TestInverseReferencePartialSelectionReturnsPrivateQualifiedResult(t *testing.T) {
	opts := inverseReferenceTestOptions()
	opts.ScopeJQL = `project = SECRET AND text ~ "private-fragment"`
	tracker := &inverseReferenceTestTracker{selectionErrs: map[int]error{0: errors.New("backend https://hidden.example.test/path?query=secret")}}
	result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
	if err != nil || result == nil || result.TargetResolution.Complete != true || result.Selection.Complete || result.Selection.Reason != JiraInverseReferenceReasonRequestFailed || result.Frontier.Phase != "selection" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"hidden.example.test", "private-fragment", "git.example.test", "/group/repo", `"Source"`, `"Status"`, `"Reason"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("private result contains %q: %s", forbidden, text)
		}
	}
}

func TestInverseReferenceSelectionBudgetFailuresAreQualified(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxRequests  int
		maxBytes     int64
		consume      int64
		wantReason   JiraInverseReferenceCompletenessReason
		wantRequests int
		wantBytes    int64
	}{
		{"request", 1, 100, 1, JiraInverseReferenceReasonRequestLimit, 1, 1},
		{"bytes", 10, 1, 2, JiraInverseReferenceReasonByteLimit, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.MaxRequests, opts.MaxResponseBytes = tc.maxRequests, tc.maxBytes
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{inverseReferenceEmptyPage(opts.MaxIssues)}, consumeBytes: tc.consume}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result == nil || result.Complete || result.Selection.Reason != tc.wantReason || result.Usage.Requests != tc.wantRequests || result.Usage.ResponseBytes != tc.wantBytes || !result.Usage.Reconciled {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceJSONOmitsNumericIssueIDsAndReconcilesSources(t *testing.T) {
	opts := inverseReferenceTestOptions()
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "987654321", Key: "SAFE-1"}
	tracker := &inverseReferenceTestTracker{
		pages: []domain.JiraInverseReferencePage{
			{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
			{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
		},
		comments: map[string][]domain.Comment{"SAFE-1": {{ID: "1", Body: "see https://git.example.test/group/repo/-/merge_requests/7"}}},
	}
	result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "987654321") || strings.Contains(text, "git.example.test") || strings.Contains(text, "/group/repo") ||
		strings.Contains(text, `"issues"`) || strings.Contains(text, `"effective_field_ids":null`) {
		t.Fatalf("public JSON leaked internal identity: %s", text)
	}
	if result.Counts.SelectedIssues != 1 || result.Counts.ScannedIssues != 2 || !result.Reconciliation.Counts ||
		len(result.SourceCounts) != 1 || result.SourceCounts[0].Total != 1 || !result.SourceCounts[0].Reconciled || !result.Reconciliation.Sources || !result.Usage.Reconciled {
		t.Fatalf("reconciliation=%+v source=%+v usage=%+v", result.Reconciliation, result.SourceCounts, result.Usage)
	}
	rendered, err := RenderJiraInverseReferencesText(&JiraInverseReferenceResult{SchemaVersion: jiraInverseReferenceSchemaVersion, Matches: []JiraInverseReferenceResultMatch{{IssueKey: "SAFE|1", Relation: JiraInverseReferenceRelationLiteral, Source: domain.JiraInverseReferenceSourceComments, Confidence: "high", Complete: true}}})
	if err != nil || !strings.Contains(rendered, `SAFE\|1`) {
		t.Fatalf("rendered=%q err=%v", rendered, err)
	}
}

func TestInverseReferenceSnapshotMissingNullFieldsAndDedup(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	pages := []domain.JiraInverseReferencePage{
		{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
		{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}},
	}
	t.Run("missing description", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription}
		tracker := &inverseReferenceTestTracker{pages: pages, snapshots: map[string]domain.JiraInverseReferenceSnapshot{
			issue.Key: {Issue: issue, Fields: []domain.JiraInverseReferenceFieldSnapshot{{FieldID: "description"}}},
		}}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || result.Complete || result.SourceCounts[0].Partial != 1 || len(result.SourceCounts[0].Reasons) != 1 || result.SourceCounts[0].Reasons[0].Reason != domain.JiraInverseReferenceReasonFieldMissing {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("explicit null", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription}
		tracker := &inverseReferenceTestTracker{pages: pages, snapshots: map[string]domain.JiraInverseReferenceSnapshot{
			issue.Key: {Issue: issue, Fields: []domain.JiraInverseReferenceFieldSnapshot{{FieldID: "description", Present: true, Value: json.RawMessage("null")}}},
		}}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || !result.Complete || !result.AbsenceProven || result.SourceCounts[0].Empty != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("distinct exact fields and property dedup", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceFields, domain.JiraInverseReferenceSourceProperties}
		opts.Fields = []string{"customfield_2", "customfield_1"}
		value := json.RawMessage(`"https://git.example.test/group/repo/-/commit/0123456789012345678901234567890123456789"`)
		tracker := &inverseReferenceTestTracker{pages: pages, snapshots: map[string]domain.JiraInverseReferenceSnapshot{
			issue.Key: {
				Issue: issue,
				Fields: []domain.JiraInverseReferenceFieldSnapshot{
					{FieldID: "customfield_1", Present: true, Value: value},
					{FieldID: "customfield_2", Present: true, Value: value},
				},
				Properties: []domain.JiraInverseReferencePropertySnapshot{{Key: "opaque-a", Value: value}, {Key: "opaque-b", Value: value}},
			},
		}}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || !result.Complete || len(result.Matches) != 3 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.Matches[0].TechnicalFieldID != "customfield_1" || result.Matches[1].TechnicalFieldID != "customfield_2" || result.Matches[2].Source != domain.JiraInverseReferenceSourceProperties {
			t.Fatalf("matches=%+v", result.Matches)
		}
	})
	t.Run("exact fields and properties inspect nested content", func(t *testing.T) {
		opts := inverseReferenceTestOptions()
		opts.Sources = []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceFields, domain.JiraInverseReferenceSourceProperties}
		opts.Fields = []string{"customfield_1"}
		value := json.RawMessage(`{"content":"https://git.example.test/group/repo"}`)
		tracker := &inverseReferenceTestTracker{pages: pages, snapshots: map[string]domain.JiraInverseReferenceSnapshot{
			issue.Key: {
				Issue:      issue,
				Fields:     []domain.JiraInverseReferenceFieldSnapshot{{FieldID: "customfield_1", Present: true, Value: value}},
				Properties: []domain.JiraInverseReferencePropertySnapshot{{Key: "opaque", Value: value}},
			},
		}}
		result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
		if err != nil || !result.Complete || len(result.Matches) != 2 || result.Matches[0].Source != domain.JiraInverseReferenceSourceFields || result.Matches[1].Source != domain.JiraInverseReferenceSourceProperties {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestInverseReferenceVerificationCancellationStopsImmediately(t *testing.T) {
	opts := inverseReferenceTestOptions()
	issueA := domain.JiraInverseReferenceIssueIdentity{ID: "1", Key: "SAFE-1"}
	issueB := domain.JiraInverseReferenceIssueIdentity{ID: "2", Key: "SAFE-2"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 2, Issues: []domain.JiraInverseReferenceIssueIdentity{issueA, issueB}}
	tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}, commentsErr: context.Canceled}
	result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
	if result != nil || !errors.Is(err, context.Canceled) || tracker.commentCalls != 1 {
		t.Fatalf("result=%+v err=%v comment_calls=%d", result, err, tracker.commentCalls)
	}
}

func TestInverseReferenceEveryAuxiliarySourceAndErrorsAreQualified(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for _, tc := range []struct {
		name    string
		source  domain.JiraInverseReferenceSource
		prepare func(*inverseReferenceTestTracker)
	}{
		{"comments", domain.JiraInverseReferenceSourceComments, func(tracker *inverseReferenceTestTracker) {
			tracker.comments = map[string][]domain.Comment{issue.Key: {{ID: "1", Body: "See HTTPS://git.example.test/group/repo!"}}}
		}},
		{"worklogs", domain.JiraInverseReferenceSourceWorklogs, func(tracker *inverseReferenceTestTracker) {
			tracker.worklogs = map[string]*domain.IssueWorklogList{issue.Key: {Total: 1, Complete: true, Worklogs: []domain.IssueWorklog{{ID: "1", Comment: "See https://git.example.test/group/repo?"}}}}
		}},
		{"remote links", domain.JiraInverseReferenceSourceRemoteLinks, func(tracker *inverseReferenceTestTracker) {
			tracker.remoteLinks = map[string]domain.JiraRemoteLinkInventory{issue.Key: {Total: 1, Links: []domain.JiraRemoteLink{{ID: "1", ObjectURL: "https://git.example.test/group/repo/-/blob/main/file.go#L20"}}}}
		}},
		{"development", domain.JiraInverseReferenceSourceDevelopment, func(tracker *inverseReferenceTestTracker) {
			tracker.development = map[string]domain.JiraDevelopmentInventory{issue.ID: {
				Projects: []domain.JiraDevelopmentProject{{Host: "git.example.test", ProjectPath: "group/repo"}},
				Commits:  []domain.JiraDevelopmentCommit{{Host: "git.example.test", ProjectPath: "group/repo", SHA: "0123456789012345678901234567890123456789"}},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Sources = []domain.JiraInverseReferenceSource{tc.source}
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}}
			tc.prepare(tracker)
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || len(result.Matches) != 1 || !result.Matches[0].Complete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}

	for name, sourceErr := range map[string]error{
		"forbidden": domain.ErrForbidden,
		"malformed": domain.ErrCheckFailed,
		"request":   errors.New("backend private prose"),
	} {
		t.Run(name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}, commentsErr: sourceErr}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result.Complete || !result.SourceCounts[0].Reconciled || !result.Usage.Reconciled || len(result.SourceCounts[0].Reasons) != 1 || result.SourceCounts[0].Reasons[0].Count != 1 || strings.Contains(string(mustJSON(t, result)), "private prose") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceLiteralFormattingBoundaries(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for name, body := range map[string]string{
		"jira inline code":  `{{https://git.example.test/group/repo}}`,
		"jira bold":         `*https://git.example.test/group/repo*`,
		"markdown code":     "`https://git.example.test/group/repo`",
		"guillemets":        `«https://git.example.test/group/repo»`,
		"nonbreaking space": "https://git.example.test/group/repo\u00a0next",
	} {
		t.Run(name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			tracker := &inverseReferenceTestTracker{
				pages:    []domain.JiraInverseReferencePage{page, page},
				comments: map[string][]domain.Comment{issue.Key: {{ID: "1", Body: body}}},
			}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || result.AbsenceProven || len(result.Matches) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceLiteralIPv6Host(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for name, body := range map[string]string{
		"plain":           "https://[2001:db8::1]/group/repo",
		"closing bracket": "[https://[2001:db8::1]/group/repo]",
	} {
		t.Run(name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Target = "https://[2001:db8::1]/group/repo"
			tracker := &inverseReferenceTestTracker{
				pages:    []domain.JiraInverseReferencePage{page, page},
				comments: map[string][]domain.Comment{issue.Key: {{ID: "1", Body: body}}},
			}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || result.AbsenceProven || len(result.Matches) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceGitLabArtifactQueryAndFragmentMatchLocally(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for _, tc := range []struct {
		name   string
		source domain.JiraInverseReferenceSource
		value  string
	}{
		{name: "description fragment", source: domain.JiraInverseReferenceSourceDescription, value: "https://git.example.test/group/repo/-/blob/main/file.go#L20"},
		{name: "comment query", source: domain.JiraInverseReferenceSourceComments, value: "https://git.example.test/group/repo/-/tree/main?ref_type=heads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Sources = []domain.JiraInverseReferenceSource{tc.source}
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}}
			if tc.source == domain.JiraInverseReferenceSourceDescription {
				tracker.snapshots = map[string]domain.JiraInverseReferenceSnapshot{issue.Key: {
					Issue: issue,
					Fields: []domain.JiraInverseReferenceFieldSnapshot{{
						FieldID: "description", Present: true, Value: json.RawMessage(fmt.Sprintf("%q", tc.value)),
					}},
				}}
			} else {
				tracker.comments = map[string][]domain.Comment{issue.Key: {{ID: "1", Body: tc.value}}}
			}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || result.AbsenceProven || len(result.Matches) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceMalformedCommentReadCannotProveAbsence(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	tracker := &inverseReferenceTestTracker{
		pages:       []domain.JiraInverseReferencePage{page, page},
		commentsErr: fmt.Errorf("%w: malformed comment body", domain.ErrCheckFailed),
	}
	result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), inverseReferenceTestOptions())
	if err != nil || result.Complete || result.AbsenceProven || result.SourceCounts[0].Partial != 1 ||
		len(result.SourceCounts[0].Reasons) != 1 || result.SourceCounts[0].Reasons[0].Reason != domain.JiraInverseReferenceReasonMalformed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInverseReferenceMissingCollectionsCannotProveAbsence(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for _, tc := range []struct {
		name    string
		source  domain.JiraInverseReferenceSource
		prepare func(*inverseReferenceTestTracker)
	}{
		{name: "comments", source: domain.JiraInverseReferenceSourceComments},
		{name: "worklogs", source: domain.JiraInverseReferenceSourceWorklogs, prepare: func(tracker *inverseReferenceTestTracker) {
			tracker.worklogs = map[string]*domain.IssueWorklogList{issue.Key: {Total: 0, Complete: true}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Sources = []domain.JiraInverseReferenceSource{tc.source}
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}}
			if tc.prepare != nil {
				tc.prepare(tracker)
			}
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result.Complete || result.AbsenceProven || result.SourceCounts[0].Partial != 1 ||
				len(result.SourceCounts[0].Reasons) != 1 || result.SourceCounts[0].Reasons[0].Reason != domain.JiraInverseReferenceReasonMalformed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferencePresentEmptyCollectionsCanProveAbsence(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for _, tc := range []struct {
		name    string
		source  domain.JiraInverseReferenceSource
		prepare func(*inverseReferenceTestTracker)
	}{
		{name: "comments", source: domain.JiraInverseReferenceSourceComments, prepare: func(tracker *inverseReferenceTestTracker) {
			tracker.comments = map[string][]domain.Comment{issue.Key: {}}
		}},
		{name: "worklogs", source: domain.JiraInverseReferenceSourceWorklogs, prepare: func(tracker *inverseReferenceTestTracker) {
			tracker.worklogs = map[string]*domain.IssueWorklogList{issue.Key: {Worklogs: []domain.IssueWorklog{}, Total: 0, Complete: true}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Sources = []domain.JiraInverseReferenceSource{tc.source}
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}}
			tc.prepare(tracker)
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || !result.Complete || !result.AbsenceProven || result.SourceCounts[0].Empty != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceDecodedRuneErrorCannotProveSourceAbsence(t *testing.T) {
	issue := domain.JiraInverseReferenceIssueIdentity{ID: "10001", Key: "SAFE-1"}
	page := domain.JiraInverseReferencePage{StartAt: 0, MaxResults: 10, Total: 1, Issues: []domain.JiraInverseReferenceIssueIdentity{issue}}
	for _, tc := range []struct {
		name    string
		source  domain.JiraInverseReferenceSource
		prepare func(*inverseReferenceTestTracker)
	}{
		{"comment", domain.JiraInverseReferenceSourceComments, func(tracker *inverseReferenceTestTracker) {
			tracker.comments = map[string][]domain.Comment{issue.Key: {{ID: "1", Body: "repaired \uFFFD bytes"}}}
		}},
		{"worklog", domain.JiraInverseReferenceSourceWorklogs, func(tracker *inverseReferenceTestTracker) {
			tracker.worklogs = map[string]*domain.IssueWorklogList{issue.Key: {Total: 1, Complete: true, Worklogs: []domain.IssueWorklog{{ID: "1", Comment: "repaired \uFFFD bytes"}}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := inverseReferenceTestOptions()
			opts.Sources = []domain.JiraInverseReferenceSource{tc.source}
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{page, page}}
			tc.prepare(tracker)
			result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result.Complete || result.AbsenceProven || result.SourceCounts[0].Partial != 1 ||
				len(result.SourceCounts[0].Reasons) != 1 || result.SourceCounts[0].Reasons[0].Reason != domain.JiraInverseReferenceReasonMalformed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInverseReferenceConfluenceRequiresConfiguredOriginEvenForNumericID(t *testing.T) {
	opts := inverseReferenceTestOptions()
	opts.Target, opts.TargetKind = "42", domain.JiraInverseReferenceTargetConfluencePage
	tracker := &inverseReferenceTestTracker{}
	if result, err := (&JiraService{tr: tracker}).SearchInverseReferences(t.Context(), opts); result != nil || !errors.Is(err, domain.ErrConfig) || len(tracker.selections) != 0 {
		t.Fatalf("result=%+v err=%v selections=%d", result, err, len(tracker.selections))
	}
}

func TestInverseReferenceConfluenceOriginSecurityPrecedesEnumeration(t *testing.T) {
	t.Run("insecure remote", func(t *testing.T) {
		t.Setenv("ATL_ALLOW_INSECURE", "")
		opts := inverseReferenceTestOptions()
		opts.Target, opts.TargetKind = "42", domain.JiraInverseReferenceTargetConfluencePage
		tracker := &inverseReferenceTestTracker{}
		result, err := (&JiraService{tr: tracker, inverseConfluenceBaseURL: "http://docs.example.test/wiki"}).SearchInverseReferences(t.Context(), opts)
		if result != nil || !errors.Is(err, domain.ErrConfig) || len(tracker.selections) != 0 {
			t.Fatalf("result=%+v err=%v selections=%d", result, err, len(tracker.selections))
		}
	})
	t.Run("scheme downgrade target", func(t *testing.T) {
		t.Setenv("ATL_ALLOW_INSECURE", "")
		opts := inverseReferenceTestOptions()
		opts.Target, opts.TargetKind = "http://docs.example.test/wiki/spaces/SAFE/pages/42/Page", domain.JiraInverseReferenceTargetConfluencePage
		tracker := &inverseReferenceTestTracker{}
		result, err := (&JiraService{tr: tracker, inverseConfluenceBaseURL: "https://docs.example.test/wiki"}).SearchInverseReferences(t.Context(), opts)
		if result != nil || !errors.Is(err, domain.ErrUsage) || len(tracker.selections) != 0 {
			t.Fatalf("result=%+v err=%v selections=%d", result, err, len(tracker.selections))
		}
	})
	for name, baseURL := range map[string]string{
		"loopback":         "http://127.0.0.1:8090/wiki",
		"explicit trusted": "http://docs.example.test/wiki",
	} {
		t.Run(name, func(t *testing.T) {
			if name == "explicit trusted" {
				t.Setenv("ATL_ALLOW_INSECURE", "1")
			} else {
				t.Setenv("ATL_ALLOW_INSECURE", "")
			}
			opts := inverseReferenceTestOptions()
			opts.Target, opts.TargetKind = "42", domain.JiraInverseReferenceTargetConfluencePage
			tracker := &inverseReferenceTestTracker{pages: []domain.JiraInverseReferencePage{inverseReferenceEmptyPage(opts.MaxIssues), inverseReferenceEmptyPage(opts.MaxIssues)}}
			result, err := (&JiraService{tr: tracker, inverseConfluenceBaseURL: baseURL}).SearchInverseReferences(t.Context(), opts)
			if err != nil || result == nil || !result.Complete || len(tracker.selections) != 2 {
				t.Fatalf("result=%+v err=%v selections=%d", result, err, len(tracker.selections))
			}
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
