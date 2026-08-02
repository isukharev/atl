package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	confluenceTestBackendURL  = "https://confluence.example.test/wiki"
	confluenceOtherBackendURL = "https://other-confluence.example.test/wiki"
	jiraTestBackendURL        = "https://jira.example.test"
	jiraOtherBackendURL       = "https://other-jira.example.test"
)

func bindConfluenceTestMirror(t *testing.T, root string) {
	t.Helper()
	bindTestMirrorBackend(t, root, "confluence", confluenceTestBackendURL)
}

func bindJiraTestMirror(t *testing.T, root string) {
	t.Helper()
	bindTestMirrorBackend(t, root, "jira", jiraTestBackendURL)
}

func bindTestMirrorBackend(t *testing.T, root, service, rawURL string) {
	t.Helper()
	want, err := backendBinding(service, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.New(root).BindBackend(want); err != nil {
		t.Fatal(err)
	}
}

type confluenceBindingStore struct {
	domain.DocStore
	page  *domain.Resource
	calls int
}

func (s *confluenceBindingStore) Tree(context.Context, string, int) ([]domain.PageRef, bool, error) {
	s.calls++
	return []domain.PageRef{{ID: s.page.ID}}, false, nil
}

func (s *confluenceBindingStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	s.calls++
	copy := *s.page
	copy.Body = append([]byte(nil), s.page.Body...)
	copy.BodyPresent = true
	return &copy, nil
}

func (s *confluenceBindingStore) GetMeta(context.Context, string) (*domain.PageMeta, error) {
	s.calls++
	return &domain.PageMeta{ID: s.page.ID, Version: s.page.Version}, nil
}

func (s *confluenceBindingStore) UpdatePage(context.Context, string, int, string, []byte, bool) (int, error) {
	s.calls++
	return s.page.Version + 1, nil
}

func testConfluenceBindingPage() *domain.Resource {
	return &domain.Resource{ID: "123", Title: "Bound", SpaceKey: "DOC", Type: "page", Version: 3, Body: []byte(`<p>body</p>`), BodyPresent: true}
}

func TestConfluencePullBindsFreshRootBeforeSelection(t *testing.T) {
	root := t.TempDir()
	store := &confluenceBindingStore{page: testConfluenceBindingPage()}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
		context.Background(), PullOpts{Space: "DOC", Into: root},
	)
	if err != nil || result == nil || len(result.Pages) != 1 || store.calls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
	}
	if err := requireMirrorBackend(root, "confluence", confluenceTestBackendURL); err != nil {
		t.Fatalf("fresh pull did not bind the mirror: %v", err)
	}
}

func TestConfluencePullDryRunChecksBindingWithoutPersistingIt(t *testing.T) {
	root := t.TempDir()
	store := &confluenceBindingStore{page: testConfluenceBindingPage()}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
		context.Background(), PullOpts{Space: "DOC", Into: root, DryRun: true},
	)
	if err != nil || result == nil || len(result.Pages) != 1 || store.calls != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
	}
	if _, err := os.Stat(mirror.New(root).Root + "/.atl/backend-bindings.json"); !os.IsNotExist(err) {
		t.Fatalf("dry-run persisted backend binding: %v", err)
	}
}

func TestConfluencePullMismatchFailsBeforeSelection(t *testing.T) {
	root := t.TempDir()
	if err := mirror.New(root).EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	bindConfluenceTestMirror(t, root)
	store := &confluenceBindingStore{page: testConfluenceBindingPage()}
	result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).Pull(
		context.Background(), PullOpts{Space: "DOC", Into: root},
	)
	if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
	}
}

func TestConfluenceMirrorRemoteOperationsRequireMatchingBinding(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		root, _ := syncedMirror(t, 3)
		store := &confluenceBindingStore{page: testConfluenceBindingPage()}
		result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).Status(context.Background(), root, true)
		if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		root, _ := syncedMirror(t, 3)
		store := &confluenceBindingStore{page: testConfluenceBindingPage()}
		result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).SnapshotMirror(context.Background(), root, true)
		if !errors.Is(err, domain.ErrCheckFailed) || result == nil || store.calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
		}
	})

	t.Run("push", func(t *testing.T) {
		root, path := syncedMirror(t, 3)
		if err := os.WriteFile(path, []byte(`<p>local</p>`), 0o644); err != nil {
			t.Fatal(err)
		}
		store := &confluenceBindingStore{page: testConfluenceBindingPage()}
		result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).Push(context.Background(), path, PushOpts{Into: root})
		if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
		}
	})

	t.Run("reconcile", func(t *testing.T) {
		root, path := syncedMirror(t, 3)
		store := &reconcileDocStore{page: testConfluenceBindingPage()}
		result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).PreviewConfluenceReconcile(context.Background(), path, root)
		if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
		}
	})

	t.Run("plan", func(t *testing.T) {
		_, path, plan, oldBodies, newBodies := createPlanFixture(t, 1)
		store := &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies}
		result, err := (&ConfluenceService{baseURL: confluenceOtherBackendURL, store: store}).PreviewConfluencePlan(context.Background(), path)
		if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || len(store.getCalls) != 0 {
			t.Fatalf("result=%+v gets=%v err=%v", result, store.getCalls, err)
		}
	})
}

func TestConfluenceRemoteStatusWithEmptyBaseURLFailsBeforeAdapter(t *testing.T) {
	root, _ := syncedMirror(t, 3)
	store := &confluenceBindingStore{page: testConfluenceBindingPage()}
	result, err := (&ConfluenceService{store: store}).Status(context.Background(), root, true)
	if !errors.Is(err, domain.ErrConfig) || result != nil || store.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.calls, err)
	}
}

func TestConfluencePullJiraMacroBindingFailsBeforeJiraRead(t *testing.T) {
	root := t.TempDir()
	if err := mirror.New(root).EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	bindConfluenceTestMirror(t, root)
	bindTestMirrorBackend(t, root, "jira", jiraOtherBackendURL)
	tracker := &recordingTracker{}
	page := testConfluenceBindingPage()
	page.Body = []byte(jiraQueryMacroCSF)
	result, err := (&ConfluenceService{
		baseURL: confluenceTestBackendURL,
		store:   &recordingStore{page: page}, jiraRead: tracker,
		cfg: &config.Config{JiraURL: jiraTestBackendURL},
	}).Pull(context.Background(), PullOpts{ID: page.ID, Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || tracker.searchJQL != "" {
		t.Fatalf("result=%+v jira_jql=%q err=%v", result, tracker.searchJQL, err)
	}
}

func TestConfluencePullJiraMacroBindsFreshJiraEvidence(t *testing.T) {
	root := t.TempDir()
	tracker := &recordingTracker{}
	page := testConfluenceBindingPage()
	page.Body = []byte(jiraQueryMacroCSF)
	result, err := (&ConfluenceService{
		baseURL: confluenceTestBackendURL,
		store:   &recordingStore{page: page}, jiraRead: tracker,
		cfg: &config.Config{JiraURL: jiraTestBackendURL},
	}).Pull(context.Background(), PullOpts{ID: page.ID, Into: root})
	if err != nil || result == nil || tracker.searchJQL == "" {
		t.Fatalf("result=%+v jira_jql=%q err=%v", result, tracker.searchJQL, err)
	}
	if err := requireMirrorBackend(root, "jira", jiraTestBackendURL); err != nil {
		t.Fatalf("Jira macro evidence was not bound: %v", err)
	}
}

func TestConfluencePullJiraMacroDryRunWritesNoBinding(t *testing.T) {
	root := t.TempDir()
	tracker := &recordingTracker{}
	page := testConfluenceBindingPage()
	page.Body = []byte(jiraQueryMacroCSF)
	result, err := (&ConfluenceService{
		baseURL: confluenceTestBackendURL,
		store:   &recordingStore{page: page}, jiraRead: tracker,
		cfg: &config.Config{JiraURL: jiraTestBackendURL},
	}).Pull(context.Background(), PullOpts{ID: page.ID, Into: root, DryRun: true})
	if err != nil || result == nil || tracker.searchJQL != "" {
		t.Fatalf("result=%+v jira_jql=%q err=%v", result, tracker.searchJQL, err)
	}
	if _, exists, err := mirror.New(root).BackendBinding("jira"); err != nil || exists {
		t.Fatalf("dry-run Jira binding exists=%t err=%v", exists, err)
	}
}
