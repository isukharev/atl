package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	testConfluenceBackendURL = "https://confluence.example.test/wiki"
	testJiraBackendURL       = "https://jira.example.test"
)

type createdConfluenceStore struct {
	domain.DocStore
	created          *domain.Resource
	readback         *domain.Resource
	createCalls      int
	readbackCalls    int
	createdBody      []byte
	createErr        error
	readbackErr      error
	createSingle     bool
	createRedacted   bool
	readbackSingle   bool
	readbackRedacted bool
}

func (s *createdConfluenceStore) CreatePage(ctx context.Context, _, _, _ string, body []byte) (*domain.Resource, error) {
	s.createCalls++
	s.createdBody = append([]byte(nil), body...)
	s.createSingle = domain.SingleAttempt(ctx)
	s.createRedacted = domain.RedactedHTTPTrace(ctx)
	return s.created, s.createErr
}

func (s *createdConfluenceStore) GetPage(ctx context.Context, _ string, _ domain.PullOpts) (*domain.Resource, error) {
	s.readbackCalls++
	s.readbackSingle = domain.SingleAttempt(ctx)
	s.readbackRedacted = domain.RedactedHTTPTrace(ctx)
	return s.readback, s.readbackErr
}

func TestConfluenceCreateRegistrationUsesAuthoritativeReadback(t *testing.T) {
	root := t.TempDir()
	store := &createdConfluenceStore{
		created: &domain.Resource{ID: "42"},
		readback: &domain.Resource{
			ID: "42", Type: "page", Title: "New", SpaceKey: "DOC", Version: 1,
			Body: []byte("<p>server normalized</p>"), BodyPresent: true,
			AncestorsPresent: true,
		},
	}
	svc := &ConfluenceService{store: store, baseURL: testConfluenceBackendURL}
	page, registration, err := svc.CreateAndRegister(context.Background(), "DOC", "", "New", []byte("<p>submitted</p>"), root)
	if err != nil {
		t.Fatal(err)
	}
	if page != store.readback || registration.Status != "registered" || !registration.ReadbackReconciled {
		t.Fatalf("page=%+v registration=%+v", page, registration)
	}
	if store.createCalls != 1 || store.readbackCalls != 1 || string(store.createdBody) != "<p>submitted</p>" {
		t.Fatalf("calls create=%d readback=%d submitted=%q", store.createCalls, store.readbackCalls, store.createdBody)
	}
	if err := requireMirrorBackend(root, "confluence", testConfluenceBackendURL); err != nil {
		t.Fatalf("created Confluence registration did not bind mirror: %v", err)
	}
	if !store.createSingle || !store.createRedacted || !store.readbackSingle || !store.readbackRedacted {
		t.Fatalf("request policies create=%t/%t readback=%t/%t", store.createSingle, store.createRedacted, store.readbackSingle, store.readbackRedacted)
	}
	csfPath := filepath.Join(root, filepath.FromSlash(registration.Path))
	local, body, err := mirror.New(root).LoadCSF(csfPath)
	if err != nil || local.Synced == nil || local.Dirty || string(body) != "<p>server normalized</p>" {
		t.Fatalf("local=%+v body=%q err=%v", local, body, err)
	}
	base, present, err := mirror.New(root).ReadBaseBody("42")
	if err != nil || !present || string(base) != "<p>server normalized</p>" {
		t.Fatalf("base=%q present=%v err=%v", base, present, err)
	}
}

func TestConfluenceCreateRegistrationKeepsMalformedAuthoritativeCSF(t *testing.T) {
	root := t.TempDir()
	malformed := []byte(`<p><broken></p>`)
	store := &createdConfluenceStore{
		created: &domain.Resource{ID: "42"},
		readback: &domain.Resource{
			ID: "42", Type: "page", Title: "New", SpaceKey: "DOC", Version: 1,
			Body: malformed, BodyPresent: true, AncestorsPresent: true,
		},
	}
	page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).CreateAndRegister(context.Background(), "DOC", "", "New", []byte(`<p>submitted</p>`), root)
	if err != nil || page != store.readback || registration == nil || registration.Status != "registered" || !registration.ReadbackReconciled {
		t.Fatalf("page=%+v registration=%+v err=%v", page, registration, err)
	}
	nativePath := filepath.Join(root, filepath.FromSlash(registration.Path))
	_, native, err := mirror.New(root).LoadCSF(nativePath)
	if err != nil || !slices.Equal(native, malformed) {
		t.Fatalf("native=%q err=%v", native, err)
	}
	base, present, err := mirror.New(root).ReadBaseBody("42")
	if err != nil || !present || !slices.Equal(base, malformed) {
		t.Fatalf("base=%q present=%v err=%v", base, present, err)
	}
	mdPath := strings.TrimSuffix(nativePath, ".csf") + ".md"
	md, err := os.ReadFile(mdPath)
	if err != nil || string(md) != mirror.MDUnavailableStub {
		t.Fatalf("md=%q err=%v", md, err)
	}
}

func TestCreatedRegistrationCarriesNonSerializedRenderWarnings(t *testing.T) {
	t.Run("Confluence render and Jira macro warnings", func(t *testing.T) {
		root := t.TempDir()
		writeMalformedRegistrationRenderConfig(t, root)
		store := &createdConfluenceStore{
			created: &domain.Resource{ID: "42"},
			readback: &domain.Resource{
				ID: "42", Type: "page", Title: "New", SpaceKey: "DOC", Version: 1,
				Body: []byte(jiraQueryMacroCSF), BodyPresent: true, AncestorsPresent: true,
			},
		}
		registrationService := &ConfluenceService{store: store, baseURL: testConfluenceBackendURL, jiraReadReason: "Jira read access is unavailable for this test"}
		_, registration, err := registrationService.CreateAndRegister(context.Background(), "DOC", "", "New", []byte(`<p>submitted</p>`), root)
		if err != nil {
			t.Fatal(err)
		}
		assertRegistrationWarning(t, registration, "malformed JSON")
		assertRegistrationWarning(t, registration, "Jira query macro(s) kept as placeholders")
		assertRegistrationWarningsNotSerialized(t, registration)
	})

	t.Run("Jira render warning", func(t *testing.T) {
		root := t.TempDir()
		writeMalformedRegistrationRenderConfig(t, root)
		tracker := &createdJiraTracker{
			created: &domain.Issue{Key: "PROJ-7"},
			readback: &domain.Issue{
				ID: "10007", Key: "PROJ-7", Project: "PROJ", Type: "Task", Summary: "S",
				Body: "remote", Fields: completeCreatedIssueFields("remote"),
			},
		}
		_, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).CreateAndRegister(context.Background(), "PROJ", "Task", "S", nil, nil, root)
		if err != nil {
			t.Fatal(err)
		}
		assertRegistrationWarning(t, registration, "malformed JSON")
		assertRegistrationWarningsNotSerialized(t, registration)
	})
}

func writeMalformedRegistrationRenderConfig(t *testing.T, root string) {
	t.Helper()
	path := config.LocalConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRegistrationWarning(t *testing.T, registration *CreatedMirrorRegistration, contains string) {
	t.Helper()
	if registration == nil {
		t.Fatal("registration is nil")
	}
	for _, warning := range registration.Warnings {
		if strings.Contains(warning, contains) {
			return
		}
	}
	t.Fatalf("warnings=%q, want one containing %q", registration.Warnings, contains)
}

func assertRegistrationWarningsNotSerialized(t *testing.T, registration *CreatedMirrorRegistration) {
	t.Helper()
	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range registration.Warnings {
		if strings.Contains(string(encoded), warning) {
			t.Fatalf("serialized registration contains warning %q: %s", warning, encoded)
		}
	}
}

func TestCreatedRegistrationRejectsEmptyRootBeforeRemoteCreate(t *testing.T) {
	conf := &createdConfluenceStore{created: &domain.Resource{ID: "42"}}
	if _, _, err := (&ConfluenceService{store: conf}).CreateAndRegister(context.Background(), "DOC", "", "New", nil, " \t"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("Confluence error=%v, want ErrUsage", err)
	}
	if conf.createCalls != 0 {
		t.Fatalf("Confluence create calls=%d, want 0", conf.createCalls)
	}

	jira := &createdJiraTracker{created: &domain.Issue{Key: "PROJ-7"}}
	if _, _, err := (&JiraService{tr: jira}).CreateAndRegister(context.Background(), "PROJ", "Task", "New", nil, nil, "\n"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("Jira error=%v, want ErrUsage", err)
	}
	if jira.createCalls != 0 {
		t.Fatalf("Jira create calls=%d, want 0", jira.createCalls)
	}
}

func TestCreatedRegistrationRejectsUnsupportedPlatformBeforeRemoteAccessOrLocalState(t *testing.T) {
	t.Run("Confluence create", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "mirror")
		store := &createdConfluenceStore{}
		page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).createAndRegisterConfluence(
			context.Background(), "DOC", "", "New", nil, root, "create", "windows",
		)
		if page != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("page=%+v registration=%+v err=%v", page, registration, err)
		}
		if store.createCalls != 0 || store.readbackCalls != 0 {
			t.Fatalf("create calls=%d readback calls=%d, want 0/0", store.createCalls, store.readbackCalls)
		}
		assertRegistrationRootAbsent(t, root)
	})

	t.Run("Confluence copy", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "mirror")
		store := &copiedConfluenceStore{}
		page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).copyPageAndRegister(
			context.Background(), "10", "Copied", "", "", root, "windows",
		)
		if page != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("page=%+v registration=%+v err=%v", page, registration, err)
		}
		if len(store.getIDs) != 0 || store.createCalls != 0 {
			t.Fatalf("read ids=%v create calls=%d, want no remote access", store.getIDs, store.createCalls)
		}
		assertRegistrationRootAbsent(t, root)
	})

	t.Run("Jira create", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "mirror")
		tracker := &createdJiraTracker{}
		issue, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).createAndRegister(
			context.Background(), "PROJ", "Task", "New", nil, nil, root, "windows",
		)
		if issue != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("issue=%+v registration=%+v err=%v", issue, registration, err)
		}
		if tracker.createCalls != 0 || tracker.readbackCalls != 0 {
			t.Fatalf("create calls=%d readback calls=%d, want 0/0", tracker.createCalls, tracker.readbackCalls)
		}
		assertRegistrationRootAbsent(t, root)
	})
}

func TestCreatedRegistrationPlatformSupportIsClosed(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		if err := validateCreatedRegistrationPlatform(goos); err != nil {
			t.Fatalf("platform %q: %v", goos, err)
		}
	}
	if err := validateCreatedRegistrationPlatform("windows"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("Windows error=%v, want ErrCheckFailed", err)
	}
}

func assertRegistrationRootAbsent(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration root exists or could not be checked: %v", err)
	}
}

func TestConfluenceCreateRegistrationFailureKeepsKnownCreatedResult(t *testing.T) {
	root := t.TempDir()
	store := &createdConfluenceStore{
		created:  &domain.Resource{ID: "42", Title: "POST value"},
		readback: &domain.Resource{ID: "other", Type: "page", BodyPresent: true, AncestorsPresent: true, Version: 1},
	}
	page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).CreateAndRegister(context.Background(), "DOC", "", "New", []byte("<p>x</p>"), root)
	if !errors.Is(err, domain.ErrCheckFailed) || page == nil || page.ID != "42" || registration == nil || registration.Status != "not_registered" {
		t.Fatalf("page=%+v registration=%+v err=%v", page, registration, err)
	}
	if states, stateErr := mirror.New(root).SyncStates(); stateErr != nil || len(states) != 0 {
		t.Fatalf("states=%+v err=%v", states, stateErr)
	}
}

func TestCreatedRegistrationRecoveryDoesNotInterpolateIdentifiersOrRoot(t *testing.T) {
	root := "root; printf ROOT_MARKER\n\x1b[31m"
	pageID := "42; printf PAGE_MARKER\r\n$(touch nope)"
	issueKey := "PROJ-7; printf ISSUE_MARKER\r\n`touch nope`"
	nestedFailure := errors.New("COLLISION_MARKER\r\n\x1b[31m")

	page := &domain.Resource{ID: pageID}
	returnedPage, pageRegistration, pageErr := confluenceRegistrationFailure(
		page, newRegistration(root), "local_registration_failed", nestedFailure,
	)
	issue := &domain.Issue{Key: issueKey}
	returnedIssue, issueRegistration, issueErr := jiraRegistrationFailure(
		issue, newRegistration(root), "local_registration_failed", nestedFailure,
	)
	if returnedPage != page || returnedPage.ID != pageID || returnedIssue != issue || returnedIssue.Key != issueKey {
		t.Fatalf("returned identities page=%+v issue=%+v", returnedPage, returnedIssue)
	}
	for _, tc := range []struct {
		value    any
		identity string
	}{
		{value: returnedPage, identity: pageID},
		{value: returnedIssue, identity: issueKey},
	} {
		encoded, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("structured result is not JSON serializable: %v", err)
		}
		encodedIdentity, err := json.Marshal(tc.identity)
		if err != nil || !strings.Contains(string(encoded), string(encodedIdentity)) {
			t.Fatalf("structured result %s does not carry identity %q: %v", encoded, tc.identity, err)
		}
	}
	for _, tc := range []struct {
		name         string
		registration *CreatedMirrorRegistration
		err          error
	}{
		{name: "Confluence", registration: pageRegistration, err: pageErr},
		{name: "Jira", registration: issueRegistration, err: issueErr},
	} {
		registration := tc.registration
		if registration.Recovery == "" {
			t.Fatalf("%s recovery is empty", tc.name)
		}
		if registration.Root != filepath.Clean(root) || registration.Reason != "local_registration_failed" {
			t.Fatalf("%s structured registration=%+v", tc.name, registration)
		}
		for _, publicText := range []string{registration.Recovery, tc.err.Error()} {
			for _, unsafe := range []string{root, pageID, issueKey, "COLLISION_MARKER", "ROOT_MARKER", "PAGE_MARKER", "ISSUE_MARKER", "\r", "\n", "\x1b", "`", "$("} {
				if strings.Contains(publicText, unsafe) {
					t.Fatalf("%s public failure text includes unsafe value %q: %q", tc.name, unsafe, publicText)
				}
			}
		}
	}
}

type copiedConfluenceStore struct {
	domain.DocStore
	source        *domain.Resource
	created       *domain.Resource
	readback      *domain.Resource
	getIDs        []string
	createCalls   int
	createdBody   []byte
	createdSpace  string
	createdParent string
	createdTitle  string
}

func (s *copiedConfluenceStore) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	s.getIDs = append(s.getIDs, id)
	if id == s.source.ID {
		return s.source, nil
	}
	return s.readback, nil
}

func (s *copiedConfluenceStore) CreatePage(_ context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	s.createCalls++
	s.createdSpace = space
	s.createdParent = parent
	s.createdTitle = title
	s.createdBody = append([]byte(nil), body...)
	return s.created, nil
}

func TestConfluenceCopyRegistrationUsesAuthoritativeCreatedReadback(t *testing.T) {
	root := t.TempDir()
	store := &copiedConfluenceStore{
		source: &domain.Resource{
			ID: "10", Type: "page", Title: "Source", SpaceKey: "DOC", Version: 3,
			Body: []byte("<p>source body</p>"), BodyPresent: true, AncestorsPresent: true,
		},
		created: &domain.Resource{ID: "42"},
		readback: &domain.Resource{
			ID: "42", Type: "page", Title: "Copied", SpaceKey: "DOC", Version: 1,
			Body: []byte("<p>server normalized copy</p>"), BodyPresent: true,
			AncestorsPresent: true,
		},
	}

	page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).CopyPageAndRegister(context.Background(), "10", "Copied", "", "", root)
	if err != nil {
		t.Fatal(err)
	}
	if page != store.readback || registration == nil || registration.Status != "registered" || !registration.ReadbackReconciled {
		t.Fatalf("page=%+v registration=%+v", page, registration)
	}
	if store.createCalls != 1 || !slices.Equal(store.getIDs, []string{"10", "42"}) {
		t.Fatalf("create calls=%d get ids=%v", store.createCalls, store.getIDs)
	}
	if store.createdSpace != "DOC" || store.createdParent != "" || store.createdTitle != "Copied" || string(store.createdBody) != "<p>source body</p>" {
		t.Fatalf("create space=%q parent=%q title=%q body=%q", store.createdSpace, store.createdParent, store.createdTitle, store.createdBody)
	}
	local, body, err := mirror.New(root).LoadCSF(filepath.Join(root, filepath.FromSlash(registration.Path)))
	if err != nil || local.Synced == nil || local.Dirty || string(body) != "<p>server normalized copy</p>" {
		t.Fatalf("local=%+v body=%q err=%v", local, body, err)
	}
}

type createdJiraTracker struct {
	domain.Tracker
	created          *domain.Issue
	readback         *domain.Issue
	createCalls      int
	readbackCalls    int
	projection       []string
	createErr        error
	readbackErr      error
	createSingle     bool
	createRedacted   bool
	readbackSingle   bool
	readbackRedacted bool
}

func (t *createdJiraTracker) Create(ctx context.Context, _ string, _ string, _ string, _ []byte, _ map[string]string) (*domain.Issue, error) {
	t.createCalls++
	t.createSingle = domain.SingleAttempt(ctx)
	t.createRedacted = domain.RedactedHTTPTrace(ctx)
	return t.created, t.createErr
}

func (t *createdJiraTracker) GetIssue(ctx context.Context, _ string, fields []string) (*domain.Issue, error) {
	t.readbackCalls++
	t.projection = append([]string(nil), fields...)
	t.readbackSingle = domain.SingleAttempt(ctx)
	t.readbackRedacted = domain.RedactedHTTPTrace(ctx)
	return t.readback, t.readbackErr
}

func completeCreatedIssueFields(body string) map[string]any {
	return map[string]any{
		"summary": "Server summary", "description": body,
		"status": map[string]any{"name": "Open"}, "issuetype": map[string]any{"name": "Task"},
		"project": map[string]any{"key": "PROJ"}, "assignee": nil, "reporter": nil,
		"labels": []any{}, "issuelinks": []any{}, "comment": map[string]any{"comments": []any{}},
		"attachment": []any{}, "updated": "2026-08-02T14:00:00.000+0000",
		"priority": nil, "parent": nil, "created": "2026-08-02T14:00:00.000+0000",
		"resolution": nil, "duedate": nil, "components": []any{}, "fixVersions": []any{}, "subtasks": []any{},
	}
}

func TestJiraCreateRegistrationUsesAuthoritativeReadback(t *testing.T) {
	root := t.TempDir()
	fields := completeCreatedIssueFields("server normalized")
	tracker := &createdJiraTracker{
		created: &domain.Issue{Key: "PROJ-7", Summary: "submitted", Body: "submitted"},
		readback: &domain.Issue{
			ID: "10007", Key: "PROJ-7", Project: "PROJ", Type: "Task", Summary: "Server summary",
			Body: "server normalized", Fields: fields,
		},
	}
	issue, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).CreateAndRegister(context.Background(), "PROJ", "Task", "submitted", []byte("submitted"), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if issue != tracker.readback || registration.Status != "registered" || tracker.createCalls != 1 || tracker.readbackCalls != 1 {
		t.Fatalf("issue=%+v registration=%+v calls=%d/%d", issue, registration, tracker.createCalls, tracker.readbackCalls)
	}
	if err := requireMirrorBackend(root, "jira", testJiraBackendURL); err != nil {
		t.Fatalf("created Jira registration did not bind mirror: %v", err)
	}
	if !tracker.createSingle || !tracker.createRedacted || !tracker.readbackSingle || !tracker.readbackRedacted {
		t.Fatalf("request policies create=%t/%t readback=%t/%t", tracker.createSingle, tracker.createRedacted, tracker.readbackSingle, tracker.readbackRedacted)
	}
	if !slices.Contains(tracker.projection, "updated") {
		t.Fatalf("projection omits updated: %v", tracker.projection)
	}
	wikiPath := filepath.Join(root, filepath.FromSlash(registration.Path))
	local, body, err := mirror.New(root).LoadWiki(wikiPath)
	if err != nil || local.Synced == nil || local.Dirty || string(body) != "server normalized" {
		t.Fatalf("local=%+v body=%q err=%v", local, body, err)
	}
	base, present, err := mirror.New(root).ReadBaseBodyExt("PROJ-7", wikiExt)
	if err != nil || !present || string(base) != "server normalized" {
		t.Fatalf("base=%q present=%v err=%v", base, present, err)
	}
	var snapshot JiraIssueSnapshot
	snapshotBytes, err := os.ReadFile(filepath.Join(root, "PROJ", "PROJ-7.json"))
	if err != nil || json.Unmarshal(snapshotBytes, &snapshot) != nil || snapshot.ID != "10007" {
		t.Fatalf("snapshot=%+v bytes=%q err=%v", snapshot, snapshotBytes, err)
	}
}

func TestCreatedRegistrationBackendMismatchStopsBeforeCreate(t *testing.T) {
	t.Run("Confluence", func(t *testing.T) {
		root := t.TempDir()
		bindTestMirrorBackend(t, root, "confluence", confluenceOtherBackendURL)
		store := &createdConfluenceStore{}
		page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).CreateAndRegister(
			context.Background(), "DOC", "", "New", []byte(`<p>submitted</p>`), root,
		)
		if page != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) || store.createCalls != 0 || store.readbackCalls != 0 {
			t.Fatalf("page=%+v registration=%+v calls=%d/%d err=%v", page, registration, store.createCalls, store.readbackCalls, err)
		}
	})

	t.Run("Jira", func(t *testing.T) {
		root := t.TempDir()
		bindTestMirrorBackend(t, root, "jira", jiraOtherBackendURL)
		tracker := &createdJiraTracker{}
		issue, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).CreateAndRegister(
			context.Background(), "PROJ", "Task", "New", nil, nil, root,
		)
		if issue != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) || tracker.createCalls != 0 || tracker.readbackCalls != 0 {
			t.Fatalf("issue=%+v registration=%+v calls=%d/%d err=%v", issue, registration, tracker.createCalls, tracker.readbackCalls, err)
		}
	})
}

func TestConfluenceRegistrationJiraBindingIsMacroScoped(t *testing.T) {
	t.Run("no macro does not require Jira binding", func(t *testing.T) {
		root := t.TempDir()
		bindTestMirrorBackend(t, root, "jira", jiraOtherBackendURL)
		store := &createdConfluenceStore{
			created: &domain.Resource{ID: "42"},
			readback: &domain.Resource{
				ID: "42", Type: "page", Title: "New", SpaceKey: "DOC", Version: 1,
				Body: []byte(`<p>plain</p>`), BodyPresent: true, AncestorsPresent: true,
			},
		}
		page, registration, err := (&ConfluenceService{
			store: store, baseURL: testConfluenceBackendURL,
			cfg: &config.Config{JiraURL: testJiraBackendURL},
		}).CreateAndRegister(context.Background(), "DOC", "", "New", []byte(`<p>plain</p>`), root)
		if err != nil || page == nil || registration == nil || registration.Status != "registered" || store.createCalls != 1 {
			t.Fatalf("page=%+v registration=%+v calls=%d err=%v", page, registration, store.createCalls, err)
		}
	})

	t.Run("submitted macro mismatch stops before create", func(t *testing.T) {
		root := t.TempDir()
		bindTestMirrorBackend(t, root, "jira", jiraOtherBackendURL)
		store := &createdConfluenceStore{}
		page, registration, err := (&ConfluenceService{
			store: store, baseURL: testConfluenceBackendURL, jiraRead: &recordingTracker{},
			cfg: &config.Config{JiraURL: testJiraBackendURL},
		}).CreateAndRegister(context.Background(), "DOC", "", "New", []byte(jiraQueryMacroCSF), root)
		if page != nil || registration != nil || !errors.Is(err, domain.ErrCheckFailed) || store.createCalls != 0 {
			t.Fatalf("page=%+v registration=%+v calls=%d err=%v", page, registration, store.createCalls, err)
		}
	})

	t.Run("unexpected readback macro never strands created page", func(t *testing.T) {
		root := t.TempDir()
		bindTestMirrorBackend(t, root, "jira", jiraOtherBackendURL)
		tracker := &recordingTracker{}
		store := &createdConfluenceStore{
			created: &domain.Resource{ID: "42"},
			readback: &domain.Resource{
				ID: "42", Type: "page", Title: "New", SpaceKey: "DOC", Version: 1,
				Body: []byte(jiraQueryMacroCSF), BodyPresent: true, AncestorsPresent: true,
			},
		}
		page, registration, err := (&ConfluenceService{
			store: store, baseURL: testConfluenceBackendURL, jiraRead: tracker,
			cfg: &config.Config{JiraURL: testJiraBackendURL},
		}).CreateAndRegister(context.Background(), "DOC", "", "New", []byte(`<p>plain</p>`), root)
		if err != nil || page == nil || registration == nil || registration.Status != "registered" || len(registration.Warnings) == 0 {
			t.Fatalf("page=%+v registration=%+v err=%v", page, registration, err)
		}
		if tracker.searchJQL != "" {
			t.Fatalf("Jira adapter reached after binding mismatch: %q", tracker.searchJQL)
		}
	})
}

func TestJiraCreateRegistrationCollisionPreservesExistingBytes(t *testing.T) {
	root := t.TempDir()
	if err := prepareMirrorBackendPopulation(root, "jira", testJiraBackendURL, ".wiki", false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "PROJ"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "PROJ", "PROJ-7.wiki")
	if err := os.WriteFile(target, []byte("user bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := &createdJiraTracker{
		created:  &domain.Issue{Key: "PROJ-7"},
		readback: &domain.Issue{ID: "10007", Key: "PROJ-7", Project: "PROJ", Type: "Task", Summary: "S", Body: "remote", Fields: completeCreatedIssueFields("remote")},
	}
	issue, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).CreateAndRegister(context.Background(), "PROJ", "Task", "S", nil, nil, root)
	if !errors.Is(err, domain.ErrCheckFailed) || issue == nil || registration == nil || registration.Reason != "target_collision" {
		t.Fatalf("issue=%+v registration=%+v err=%v", issue, registration, err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "user bytes" {
		t.Fatalf("target=%q err=%v", got, readErr)
	}
}

func TestCreatedRegistrationAmbiguousCreateStopsWithoutReadbackOrState(t *testing.T) {
	t.Run("Confluence", func(t *testing.T) {
		root := t.TempDir()
		store := &createdConfluenceStore{createErr: errors.New("TRANSPORT_MARKER\r\n\x1b[31m")}
		page, registration, err := (&ConfluenceService{store: store, baseURL: testConfluenceBackendURL}).CreateAndRegister(
			context.Background(), "DOC", "", "New", []byte(`<p>submitted</p>`), root,
		)
		assertAmbiguousRegistrationCreate(t, root, page != nil, registration, err, store.createCalls, store.readbackCalls)
		if !store.createSingle || !store.createRedacted {
			t.Fatalf("create request policy single=%t redacted=%t", store.createSingle, store.createRedacted)
		}
	})

	t.Run("Jira", func(t *testing.T) {
		root := t.TempDir()
		tracker := &createdJiraTracker{createErr: errors.New("TRANSPORT_MARKER\r\n\x1b[31m")}
		issue, registration, err := (&JiraService{tr: tracker, baseURL: testJiraBackendURL}).CreateAndRegister(
			context.Background(), "PROJ", "Task", "New", nil, nil, root,
		)
		assertAmbiguousRegistrationCreate(t, root, issue != nil, registration, err, tracker.createCalls, tracker.readbackCalls)
		if !tracker.createSingle || !tracker.createRedacted {
			t.Fatalf("create request policy single=%t redacted=%t", tracker.createSingle, tracker.createRedacted)
		}
	})
}

func assertAmbiguousRegistrationCreate(t *testing.T, root string, created bool, registration *CreatedMirrorRegistration, err error, createCalls, readbackCalls int) {
	t.Helper()
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if created || registration != nil || !errors.Is(err, domain.ErrCheckFailed) ||
		!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("created=%+v registration=%+v ambiguous=%T err=%v", created, registration, ambiguous, err)
	}
	if createCalls != 1 || readbackCalls != 0 {
		t.Fatalf("create calls=%d readback calls=%d, want 1/0", createCalls, readbackCalls)
	}
	for _, unsafe := range []string{"TRANSPORT_MARKER", "\r", "\n", "\x1b"} {
		if strings.Contains(err.Error(), unsafe) {
			t.Fatalf("ambiguous create error includes unsafe nested text %q: %q", unsafe, err)
		}
	}
	states, stateErr := mirror.New(root).SyncStates()
	if stateErr != nil || len(states) != 0 {
		t.Fatalf("states=%+v err=%v", states, stateErr)
	}
}
