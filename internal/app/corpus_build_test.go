package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

type corpusBuildJiraTracker struct {
	*jiraCompleteTracker
	user       *domain.User
	currentErr error
	budgeted   bool
}

func (tracker *corpusBuildJiraTracker) CurrentUser(ctx context.Context) (*domain.User, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 7); err != nil {
			return nil, err
		}
	}
	if tracker.currentErr != nil {
		return nil, tracker.currentErr
	}
	if tracker.user == nil {
		return nil, errors.New("missing configured fixture principal")
	}
	copy := *tracker.user
	return &copy, nil
}

func (tracker *corpusBuildJiraTracker) SearchQualified(ctx context.Context, query string, fields []string, limit int, cursor string) (domain.IssueSearchPage, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 11); err != nil {
			return domain.IssueSearchPage{}, err
		}
	}
	return tracker.jiraCompleteTracker.SearchQualified(ctx, query, fields, limit, cursor)
}

func (tracker *corpusBuildJiraTracker) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 13); err != nil {
			return nil, err
		}
	}
	return tracker.jiraCompleteTracker.GetIssue(ctx, key, fields)
}

type corpusBuildConfluenceStore struct {
	*completePullStore
	identity   domain.ConfluenceUserIdentity
	currentErr error
	budgeted   bool
	pageMu     sync.Mutex
}

func (store *corpusBuildConfluenceStore) CurrentConfluenceUser(ctx context.Context) (domain.ConfluenceUserIdentity, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 5); err != nil {
			return domain.ConfluenceUserIdentity{}, err
		}
	}
	if store.currentErr != nil {
		return domain.ConfluenceUserIdentity{}, store.currentErr
	}
	return store.identity, nil
}

func (store *corpusBuildConfluenceStore) SearchComplete(ctx context.Context, query string, limit int, cursor string) (domain.PageSearchPage, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 10); err != nil {
			return domain.PageSearchPage{}, err
		}
	}
	return store.completePullStore.SearchComplete(ctx, query, limit, cursor)
}

func (store *corpusBuildConfluenceStore) GetPage(ctx context.Context, id string, options domain.PullOpts) (*domain.Resource, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 20); err != nil {
			return nil, err
		}
	}
	store.pageMu.Lock()
	defer store.pageMu.Unlock()
	return store.completePullStore.GetPage(ctx, id, options)
}

func consumeCorpusBuildRead(ctx context.Context, size int64) error {
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil {
		return errors.New("missing corpus build read budget")
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if size > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(size)
	return nil
}

func TestCorpusBuildPublishesQualifiedCrossServiceGeneration(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	jira := newCorpusBuildJiraFixture(true)
	confluence := newCorpusBuildConfluenceFixture(true)
	service := newCorpusBuildTestService(jira, confluence)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	options.ConfluenceSpace = "DOC"
	options.MaxConfluencePages = 2

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "new" || result.Reused || result.Projection.Readiness != corpus.ProjectionReady ||
		len(result.Services) != 2 || len(result.Generation.Services) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if result.Usage != (corpus.CaptureUsage{Attempts: 12, ResponseBytes: 142}) {
		t.Fatalf("aggregate usage=%#v", result.Usage)
	}
	if result.Services[0].Service != corpus.ServiceConfluence || result.Services[0].Count != 2 ||
		result.Services[0].Usage != (corpus.CaptureUsage{Attempts: 4, ResponseBytes: 60}) ||
		result.Services[1].Service != corpus.ServiceJira || result.Services[1].Count != 2 ||
		result.Services[1].Usage != (corpus.CaptureUsage{Attempts: 6, ResponseBytes: 70}) {
		t.Fatalf("service results=%#v", result.Services)
	}

	store, err := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Receipt().GenerationDigest != result.Generation.GenerationDigest ||
		len(selected.Manifest().Qualifications) != 2 {
		t.Fatalf("selected=%#v result=%#v", selected.Summary(), result.Generation)
	}
	_ = selected.Close()
	_ = store.Close()

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "PROJ", "DOC", "fixture-jira-principal", "fixture-confluence-principal", "private fixture title"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("content-free result leaked %q: %s", private, encoded)
		}
	}
}

func TestCorpusBuildOneServiceDoesNotFabricateAbsentBackend(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	service := newCorpusBuildTestService(newCorpusBuildJiraFixture(false), nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services) != 1 || result.Services[0].Service != corpus.ServiceJira ||
		len(result.Generation.Services) != 1 || result.Generation.Services[0] != corpus.ServiceJira ||
		len(result.Projection.Qualifications) != 1 || result.Projection.Qualifications[0].Service != corpus.ServiceJira {
		t.Fatalf("result=%#v", result)
	}
	workspace, err := corpus.OpenBuildWorkspace(t.Context(), root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	active, found, err := workspace.LoadActive()
	if err != nil || !found {
		t.Fatalf("active found=%t err=%v", found, err)
	}
	attemptRoot, err := workspace.AttemptRoot(active.AttemptID, corpus.ServiceJira)
	if err != nil {
		t.Fatal(err)
	}
	receipt, found, err := workspace.LoadCaptureReceipt(active.AttemptID, corpus.ServiceJira)
	if err != nil || !found {
		t.Fatalf("receipt found=%t err=%v", found, err)
	}
	expectedOptions, err := service.captureOptionsDigest(corpus.ServiceJira, options)
	validationErr := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], receipt, expectedOptions, active.Deadline, corpusBuildLimits(options))
	if err != nil || validationErr != nil {
		t.Fatalf("valid adoption options=%q receipt_options=%q state=%#v receipt=%#v digest_error=%v validation_error=%v", expectedOptions, receipt.OptionsDigest, active.Services[0], receipt, err, validationErr)
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], receipt, strings.Repeat("f", 64), active.Deadline, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("mismatched options adoption error=%v", err)
	}
	changedDimensions := receipt
	changedDimensions.Dimensions = append([]corpus.CaptureDimensionEvidence(nil), receipt.Dimensions...)
	for index := range changedDimensions.Dimensions {
		if changedDimensions.Dimensions[index].Dimension == corpus.CaptureComments {
			changedDimensions.Dimensions[index].State = corpus.CaptureComplete
		}
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], changedDimensions, expectedOptions, active.Deadline, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("widened evidence adoption error=%v", err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, active.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	started, err := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	late, err := corpus.BuildCaptureReceipt(corpus.CaptureReceiptInput{
		Service: receipt.Service, ScopeDigest: receipt.ScopeDigest, SelectorDigest: receipt.SelectorDigest,
		OptionsDigest: receipt.OptionsDigest, SelectionDigest: receipt.SelectionDigest, SnapshotDigest: receipt.SnapshotDigest,
		StartedAt: started, CompletedAt: deadline.Add(time.Nanosecond), Total: receipt.Total, Completed: receipt.Completed,
		Usage: receipt.Usage, Dimensions: receipt.Dimensions,
	}, corpusBuildLimits(options))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], late, expectedOptions, active.Deadline, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("late receipt adoption error=%v", err)
	}
}

func TestCorpusBuildJiraUsesExactMinimalFieldProjection(t *testing.T) {
	options := corpusBuildTestOptions("/synthetic/private-root")
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	pull := corpusBuildJiraPullOptions("/synthetic/attempt", options)
	fields := jiraCompletePullFields(pull, []string{"comment"}, *pull.exactRender)
	if got, want := strings.Join(fields, ","), "summary,description,project"; got != want {
		t.Fatalf("fields=%q want=%q", got, want)
	}
}

func TestCorpusBuildResumesExactAttemptWithoutResettingGuards(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(true)
	tracker.getErrorAt = "10"
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted capture unexpectedly succeeded")
	}
	tracker.getErrorAt = ""
	options.Initialize = false
	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "resumed" || result.Usage != (corpus.CaptureUsage{Attempts: 9, ResponseBytes: 97}) ||
		result.Services[0].Usage != (corpus.CaptureUsage{Attempts: 7, ResponseBytes: 83}) {
		t.Fatalf("resumed result=%#v", result)
	}
}

func TestCorpusBuildResumeRejectsPrincipalScopeDrift(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.getErrorAt = "10"
	confluence := newCorpusBuildConfluenceFixture(false)
	service := newCorpusBuildTestService(tracker, confluence)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.ConfluenceSpace = "DOC"
	options.MaxConfluencePages = 2
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted cross-service capture unexpectedly succeeded")
	}
	confluence.identity.ID = "rotated-fixture-principal"
	tracker.getErrorAt = ""
	options.Initialize = false
	result, err := service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhasePrincipal || closed.Reason != CorpusBuildReasonDrift {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
}

func TestCorpusBuildRequiresExplicitRestartAfterAmbiguousRemotePhase(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.getErrorAt = "10"
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted capture unexpectedly succeeded")
	}

	workspace, err := corpus.OpenBuildWorkspace(t.Context(), root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := workspace.LoadActive()
	if err != nil || !found {
		t.Fatalf("active found=%t err=%v", found, err)
	}
	active.RemoteInFlight = true
	active.RemoteService = corpus.ServiceJira
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	_ = workspace.Close()

	options.Initialize = false
	_, err = service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if !errors.Is(err, corpus.ErrOutcomeUnknown) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhaseRecover || closed.Reason != CorpusBuildReasonOutcomeUnknown {
		t.Fatalf("ambiguous error=%#v", err)
	}
	tracker.getErrorAt = ""
	options.Restart = true
	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "restarted" || result.Projection.Readiness != corpus.ProjectionReady {
		t.Fatalf("restart result=%#v", result)
	}
}

func TestCorpusBuildRestartBeforeMirrorInitializationAndCompletedRefusal(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.currentErr = errors.New("configured fixture principal unavailable")
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	if result, err := service.Build(t.Context(), options); result != nil || err == nil {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	tracker.currentErr = nil
	options.Initialize = false
	options.Restart = true
	result, err := service.Build(t.Context(), options)
	if err != nil || result.Source != "restarted" || result.Projection.Readiness != corpus.ProjectionReady {
		t.Fatalf("restart result=%#v error=%v", result, err)
	}
	if result, err := service.Build(t.Context(), options); result != nil || !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("completed restart result=%#v error=%v", result, err)
	}
}

func TestCorpusBuildFailurePreservesPreviousGenerationAndDiagnosticsAreClosed(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	first, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}

	tracker.getErrorAt = "10"
	options.Initialize = false
	_, err = service.Build(t.Context(), options)
	if err == nil || strings.Contains(err.Error(), "injected read interruption") {
		t.Fatalf("failure=%v", err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	selected, selectErr := store.SelectCurrent(t.Context())
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if selected.Receipt().GenerationDigest != first.Generation.GenerationDigest {
		t.Fatalf("current=%s first=%s", selected.Receipt().GenerationDigest, first.Generation.GenerationDigest)
	}
	_ = selected.Close()
	_ = store.Close()

	privateFailure := errors.New("private selector title /private/path")
	principalTracker := newCorpusBuildJiraFixture(false)
	principalTracker.currentErr = privateFailure
	privateRoot := corpusBuildPrivateRoot(t)
	privateOptions := corpusBuildTestOptions(privateRoot)
	privateOptions.Initialize = true
	privateOptions.JiraProject = "PROJ"
	privateOptions.MaxJiraIssues = 2
	_, err = newCorpusBuildTestService(principalTracker, nil).Build(t.Context(), privateOptions)
	var closed *CorpusBuildError
	if !errors.As(err, &closed) || closed.Phase != CorpusBuildPhasePrincipal ||
		strings.Contains(err.Error(), privateFailure.Error()) || !errors.Is(err, privateFailure) {
		t.Fatalf("principal failure=%#v", err)
	}
}

func TestCorpusBuildPublicationRecoveryBarrierIsOutcomeUnknownAndContentFree(t *testing.T) {
	privateFailure := errors.New("private completed-record path")
	err := CorpusBuildFailure(CorpusBuildPhasePublish, errors.Join(corpus.ErrOutcomeUnknown, privateFailure))
	var closed *CorpusBuildError
	if !errors.As(err, &closed) || closed.Phase != CorpusBuildPhasePublish ||
		closed.Reason != CorpusBuildReasonOutcomeUnknown || !errors.Is(err, corpus.ErrOutcomeUnknown) ||
		!errors.Is(err, privateFailure) || strings.Contains(err.Error(), privateFailure.Error()) {
		t.Fatalf("publication recovery error=%#v", err)
	}
}

func TestCorpusBuildCanceledDeadlineNeverPublishes(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.currentErr = context.Canceled
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Build(ctx, options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhasePrincipal || closed.Reason != CorpusBuildReasonDeadline {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(context.Background()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func TestCorpusBuildExpiredAttemptDeadlineNeverPublishes(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	service := newCorpusBuildTestService(newCorpusBuildJiraFixture(true), nil)
	service.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	options.Deadline = time.Hour

	result, err := service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &closed) ||
		closed.Reason != CorpusBuildReasonDeadline {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(context.Background()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func TestValidateCorpusBuildOptionsRejectsInvalidBoundsAndSelectorsWithoutEffects(t *testing.T) {
	base := corpusBuildTestOptions("/synthetic/private-root")
	base.JiraProject = "PROJ"
	base.MaxJiraIssues = 2
	tests := map[string]func(*CorpusBuildOptions){
		"missing root":         func(options *CorpusBuildOptions) { options.Root = "" },
		"missing service":      func(options *CorpusBuildOptions) { options.JiraProject, options.MaxJiraIssues = "", 0 },
		"noncanonical project": func(options *CorpusBuildOptions) { options.JiraProject = "proj" },
		"cap without service":  func(options *CorpusBuildOptions) { options.JiraProject = "" },
		"control in space": func(options *CorpusBuildOptions) {
			options.JiraProject, options.MaxJiraIssues = "", 0
			options.ConfluenceSpace, options.MaxConfluencePages = "private\nspace", 1
		},
		"initialize and restart": func(options *CorpusBuildOptions) { options.Initialize, options.Restart = true, true },
		"request bound":          func(options *CorpusBuildOptions) { options.MaxRequests = corpusBuildMaxRequests + 1 },
		"response bound":         func(options *CorpusBuildOptions) { options.MaxResponseBytes = corpusBuildMaxResponse + 1 },
		"deadline bound":         func(options *CorpusBuildOptions) { options.Deadline = corpusBuildMaxDeadline + time.Second },
		"schedule bound":         func(options *CorpusBuildOptions) { options.MaxInFlight = corpusBuildMaxInFlight + 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := base
			mutate(&options)
			err := ValidateCorpusBuildOptions(options)
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "private\nspace") || (options.Root != "" && strings.Contains(err.Error(), options.Root)) {
				t.Fatalf("validation leaked caller value: %v", err)
			}
		})
	}
}

func TestCorpusBuildActiveBindingRejectsGuardAndSelectorDrift(t *testing.T) {
	options := corpusBuildTestOptions("/synthetic/private-root")
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	_, services, err := corpusBuildServices(options)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	base := corpus.BuildActive{
		SchemaVersion: corpus.BuildActiveSchemaV1, AttemptID: strings.Repeat("1", 32),
		Status: corpus.BuildAttemptActive, OptionsDigest: strings.Repeat("a", 64), Services: services,
		StartedAt: corpus.NewBuildActiveTime(started), Deadline: corpus.NewBuildActiveTime(started.Add(options.Deadline)),
		MaxAttempts: options.MaxRequests, MaxResponseBytes: options.MaxResponseBytes,
	}
	if err := validateCorpusBuildActiveBinding(base, options); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corpus.BuildActive){
		"attempt budget": func(active *corpus.BuildActive) { active.MaxAttempts++ },
		"byte budget":    func(active *corpus.BuildActive) { active.MaxResponseBytes++ },
		"deadline": func(active *corpus.BuildActive) {
			active.Deadline = corpus.NewBuildActiveTime(started.Add(options.Deadline + time.Second))
		},
		"selector": func(active *corpus.BuildActive) { active.Services[0].SelectorDigest = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			active := base
			active.Services = append([]corpus.BuildServiceState(nil), base.Services...)
			mutate(&active)
			if err := validateCorpusBuildActiveBinding(active, options); !errors.Is(err, corpus.ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func newCorpusBuildJiraFixture(budgeted bool) *corpusBuildJiraTracker {
	return &corpusBuildJiraTracker{
		jiraCompleteTracker: newCompleteJiraTracker(), budgeted: budgeted,
		user: &domain.User{Key: "fixture-jira-principal", DisplayName: "Configured Fixture", Active: true},
	}
}

func newCorpusBuildConfluenceFixture(budgeted bool) *corpusBuildConfluenceStore {
	first := completeTestPage("10")
	first.Title = "private fixture title"
	second := completeTestPage("20")
	return &corpusBuildConfluenceStore{
		completePullStore: &completePullStore{
			pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": first, "20": second}},
			searchSequence: []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")},
		},
		identity: domain.ConfluenceUserIdentity{ID: "fixture-confluence-principal", DisplayName: "Configured Fixture"},
		budgeted: budgeted,
	}
}

func newCorpusBuildTestService(jira *corpusBuildJiraTracker, confluence *corpusBuildConfluenceStore) *CorpusBuildService {
	dependencies := CorpusBuildDependencies{
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		Now: time.Now,
	}
	if jira != nil {
		dependencies.Jira = &JiraService{tr: jira, baseURL: jiraMirrorTestBackendURL}
	}
	if confluence != nil {
		dependencies.Confluence = &ConfluenceService{
			store: confluence, baseURL: confluenceTestBackendURL,
			requestMaxInFlight: 2, requestsPerSecond: 100,
		}
	}
	return NewCorpusBuildService(dependencies)
}

func corpusBuildTestOptions(root string) CorpusBuildOptions {
	return CorpusBuildOptions{
		Root: root, MaxRequests: 1000, MaxResponseBytes: 1 << 20,
		MaxMembers: 100, MaxGenerationBytes: 4 << 20,
		Deadline: time.Hour, MaxInFlight: 2, RequestsPerSecond: 100,
	}
}

func corpusBuildPrivateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
