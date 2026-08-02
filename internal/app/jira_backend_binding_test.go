package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type jiraBindingProbeTracker struct {
	domain.Tracker
	fieldsCalls int
	searchCalls int
	getCalls    int
	updateCalls int
}

func (t *jiraBindingProbeTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	t.fieldsCalls++
	return []domain.FieldDef{{ID: "customfield_10000", Name: "Details", Custom: true}}, nil
}

func (t *jiraBindingProbeTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	t.searchCalls++
	return []domain.Issue{{
		ID: "10001", Key: "PROJ-1", Project: "PROJ", Summary: "summary", Status: "Open", Type: "Task", Body: "base",
		Fields: map[string]any{"description": "base", "customfield_10000": "details"},
	}}, "", nil
}

func (t *jiraBindingProbeTracker) GetIssue(context.Context, string, []string) (*domain.Issue, error) {
	t.getCalls++
	return &domain.Issue{ID: "10001", Key: "PROJ-1", Body: "base", Fields: map[string]any{
		"description": "base", "updated": "2026-08-02T12:34:56.000+0000",
	}}, nil
}

func (t *jiraBindingProbeTracker) Update(context.Context, string, string, []byte, map[string]string) error {
	t.updateCalls++
	return nil
}

func TestJiraPullFreshMirrorBindsBeforeRemoteDiscovery(t *testing.T) {
	root := t.TempDir()
	tracker := &jiraBindingProbeTracker{}
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	if _, err := service.Pull(context.Background(), JiraPullOpts{
		JQL: "project = PROJ", Into: root, Limit: 1, Fields: []string{"Details"},
	}); err != nil {
		t.Fatal(err)
	}
	if tracker.fieldsCalls != 1 || tracker.searchCalls != 1 {
		t.Fatalf("fields=%d search=%d", tracker.fieldsCalls, tracker.searchCalls)
	}
	want, err := backendBinding("jira", jiraMirrorTestBackendURL)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := mirror.New(root).BackendBinding("jira")
	if err != nil || !ok || got != want {
		t.Fatalf("binding ok=%t got=%+v want=%+v err=%v", ok, got, want, err)
	}
}

func TestJiraPullRejectsAbsentMismatchAndEmptyBackendBeforeAdapter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string)
		baseURL string
		wantErr error
	}{
		{
			name: "absent",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, ".atl", "backend-bindings.json")); err != nil {
					t.Fatal(err)
				}
			},
			baseURL: jiraMirrorTestBackendURL,
			wantErr: domain.ErrCheckFailed,
		},
		{name: "mismatch", prepare: func(*testing.T, string) {}, baseURL: "https://other-jira.example.test", wantErr: domain.ErrCheckFailed},
		{name: "empty backend", prepare: func(*testing.T, string) {}, baseURL: "", wantErr: domain.ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, root, _ := setupPulled(t, "base")
			tc.prepare(t, root)
			tracker := &jiraBindingProbeTracker{}
			_, err := (&JiraService{tr: tracker, baseURL: tc.baseURL}).Pull(context.Background(), JiraPullOpts{
				JQL: "project = PROJ", Into: root, Limit: 1, Fields: []string{"Details"},
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if tracker.fieldsCalls != 0 || tracker.searchCalls != 0 || tracker.getCalls != 0 || tracker.updateCalls != 0 {
				t.Fatalf("adapter reached: %+v", tracker)
			}
		})
	}
}

func TestJiraRemoteMirrorOperationsRequireExactBackendBeforeAdapter(t *testing.T) {
	for _, bindingCase := range []struct {
		name    string
		wantErr error
	}{
		{name: "absent", wantErr: domain.ErrCheckFailed},
		{name: "mismatch", wantErr: domain.ErrCheckFailed},
		{name: "empty backend", wantErr: domain.ErrConfig},
	} {
		for _, operation := range []string{"status", "snapshot", "push clean file", "push clean directory", "push preview", "push apply", "reconcile preview", "reconcile stage"} {
			t.Run(bindingCase.name+"/"+operation, func(t *testing.T) {
				_, _, root, path := setupPulled(t, "base")
				baseURL := jiraMirrorTestBackendURL
				switch bindingCase.name {
				case "absent":
					if err := os.Remove(filepath.Join(root, ".atl", "backend-bindings.json")); err != nil {
						t.Fatal(err)
					}
				case "mismatch":
					baseURL = "https://other-jira.example.test"
				case "empty backend":
					baseURL = ""
				}
				tracker := &jiraBindingProbeTracker{}
				service := &JiraService{tr: tracker, baseURL: baseURL}
				var err error
				switch operation {
				case "status":
					_, err = service.Status(context.Background(), root, true)
				case "snapshot":
					_, err = service.SnapshotMirror(context.Background(), root, true)
				case "push clean file":
					_, err = service.Push(context.Background(), path, JiraPushOpts{Into: root})
				case "push clean directory":
					_, err = service.Push(context.Background(), root, JiraPushOpts{Into: root})
				case "push preview", "push apply":
					if writeErr := os.WriteFile(path, []byte("local"), 0o644); writeErr != nil {
						t.Fatal(writeErr)
					}
					_, err = service.Push(context.Background(), path, JiraPushOpts{Into: root, Apply: operation == "push apply"})
				case "reconcile preview", "reconcile stage":
					if writeErr := os.WriteFile(path, []byte("local"), 0o644); writeErr != nil {
						t.Fatal(writeErr)
					}
					if operation == "reconcile stage" {
						_, err = service.StageJiraReconcile(context.Background(), path, root)
					} else {
						_, err = service.PreviewJiraReconcile(context.Background(), path, root)
					}
				}
				if !errors.Is(err, bindingCase.wantErr) {
					t.Fatalf("err=%v, want %v", err, bindingCase.wantErr)
				}
				if tracker.fieldsCalls != 0 || tracker.searchCalls != 0 || tracker.getCalls != 0 || tracker.updateCalls != 0 {
					t.Fatalf("adapter reached: %+v", tracker)
				}
			})
		}
	}
}
