package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type guardedCreateServer struct {
	t           *testing.T
	server      *httptest.Server
	mu          sync.Mutex
	posts       int
	reads       int
	drift       bool
	status      int
	ack         string
	mutate      func(map[string]any)
	postEntered chan struct{}
	releasePost chan struct{}
}

func newGuardedCreateServer(t *testing.T) *guardedCreateServer {
	fixture := &guardedCreateServer{t: t, status: http.StatusCreated, ack: `{"id":"11","key":"OPS-1"}`}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *guardedCreateServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/project":
		_, _ = io.WriteString(w, `[{"id":"7","key":"OPS","name":"Operations","archived":false}]`)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/createmeta/OPS/issuetypes":
		name := "Task"
		if s.drift && s.reads > 4 {
			name = "Changed task"
		}
		_, _ = io.WriteString(w, `{"startAt":0,"total":1,"isLast":true,"values":[{"id":"3","name":"`+name+`","subtask":false}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/createmeta/OPS/issuetypes/3":
		_, _ = io.WriteString(w, guardedCreateMetadataFixture())
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue":
		s.posts++
		if s.postEntered != nil {
			close(s.postEntered)
			<-s.releasePost
		}
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.ack)
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/11":
		fields := map[string]any{}
		for _, name := range strings.Split(r.URL.Query().Get("fields"), ",") {
			fields[name] = nil
		}
		fields["project"] = map[string]any{"id": "7", "key": "OPS"}
		fields["issuetype"] = map[string]any{"id": "3", "name": "Task"}
		fields["summary"] = "Reviewed"
		fields["description"] = "wiki"
		fields["created"] = "2026-08-22T10:00:00.000+0000"
		fields["updated"] = "2026-08-22T10:00:01.000+0000"
		fields["status"] = map[string]any{"id": "5", "name": "Ready"}
		fields["assignee"] = map[string]any{"name": "user", "displayName": "Assigned User"}
		fields["reporter"] = map[string]any{"name": "reporter", "displayName": "Reporting User"}
		fields["labels"] = []any{"reviewed", "guarded"}
		fields["customfield_1"] = map[string]any{"value": json.Number("9007199254740993"), "server": true}
		if s.mutate != nil {
			s.mutate(fields)
		}
		encoded, _ := json.Marshal(map[string]any{"id": "11", "key": "OPS-1", "fields": fields})
		_, _ = w.Write(encoded)
	default:
		http.NotFound(w, r)
	}
}

func guardedCreateMetadataFixture() string {
	field := func(id, name, typ string, required bool) string {
		return `{"fieldId":"` + id + `","name":"` + name + `","required":` + fmt.Sprint(required) + `,"schema":{"type":"` + typ + `","system":"` + id + `"},"hasDefaultValue":false,"allowedValues":[],"autoCompleteUrl":null}`
	}
	return `{"startAt":0,"total":5,"isLast":true,"values":[` +
		field("project", "Project", "project", true) + `,` +
		field("issuetype", "Issue Type", "issuetype", true) + `,` +
		field("summary", "Summary", "string", true) + `,` +
		field("description", "Description", "string", false) + `,` +
		`{"fieldId":"customfield_1","name":"Number","required":false,"schema":{"type":"number","custom":"number","customId":1},"hasDefaultValue":false,"allowedValues":[],"autoCompleteUrl":null}]}`
}

func guardedCreateOpts() JiraGuardedCreateOpts {
	return JiraGuardedCreateOpts{
		Project: "OPS", IssueType: "Task", Summary: "Reviewed", Description: []byte("wiki"), DescriptionSource: "wiki",
		Fields: map[string]domain.JiraFieldInput{"customfield_1": {Value: `{"value":9007199254740993}`, ExplicitJSON: true}},
	}
}

func guardedCreateService(server *guardedCreateServer) *JiraService {
	adapter := jiraadapter.New(server.server.URL, "token", "test")
	return NewJiraService(JiraDependencies{Tracker: adapter, BaseURL: server.server.URL, Config: &config.Config{}})
}

func TestGuardedCreatePreviewApplyAndIDOnlyAcknowledgement(t *testing.T) {
	fixture := newGuardedCreateServer(t)
	service := guardedCreateService(fixture)
	opts := guardedCreateOpts()
	preview, err := service.GuardedCreate(t.Context(), opts)
	if err != nil || preview.Status != "would_apply" || preview.ProposalHash == "" || preview.WriteAttempted || fixture.posts != 0 {
		t.Fatalf("preview=%+v err=%v posts=%d", preview, err, fixture.posts)
	}
	if preview.Bounds.MaxRequests != jiraGuardedCreatePreviewRequests || preview.Bounds.DeadlineMillis != jiraGuardedCreateDeadline.Milliseconds() || preview.Bounds.MaxResponseBytes != jiraGuardedCreateMaxResponseBytes {
		t.Fatalf("preview bounds=%+v", preview.Bounds)
	}
	fixture.ack = `{"id":"11"}`
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	result, err := service.GuardedCreate(t.Context(), opts)
	if err != nil || result.Status != "applied" || !result.WriteAttempted || !result.ReadbackReconciled || result.Issue == nil || result.Issue.Key != "OPS-1" || result.Acknowledgement == nil || result.Acknowledgement.Key != "" || fixture.posts != 1 {
		t.Fatalf("result=%+v err=%v posts=%d", result, err, fixture.posts)
	}
}

func TestGuardedCreateCancellationBeforeDispatchMakesNoRequest(t *testing.T) {
	fixture := newGuardedCreateServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := guardedCreateService(fixture).GuardedCreate(ctx, guardedCreateOpts())
	if err == nil || result.Status != "blocked" || result.WriteAttempted || fixture.reads != 0 || fixture.posts != 0 {
		t.Fatalf("result=%+v err=%v reads=%d posts=%d", result, err, fixture.reads, fixture.posts)
	}
}

func TestGuardedCreatePrewriteDriftAndClosedOutcomes(t *testing.T) {
	tests := []struct {
		name, status string
		configure    func(*guardedCreateServer)
		attempted    bool
	}{
		{"prewrite drift", "blocked", func(f *guardedCreateServer) { f.drift = true }, false},
		{"definitive rejection", "not_applied", func(f *guardedCreateServer) { f.status, f.ack = http.StatusForbidden, `{"error":"private"}` }, true},
		{"missing acknowledgement", "outcome_unknown", func(f *guardedCreateServer) { f.ack = `{}` }, true},
		{"moved readback", "outcome_unknown", func(f *guardedCreateServer) {
			f.mutate = func(fields map[string]any) { fields["project"] = map[string]any{"id": "8", "key": "ALT"} }
		}, true},
		{"typed mismatch", "outcome_unknown", func(f *guardedCreateServer) {
			f.mutate = func(fields map[string]any) { fields["customfield_1"] = json.Number("2") }
		}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGuardedCreateServer(t)
			preview, previewErr := guardedCreateService(fixture).GuardedCreate(t.Context(), guardedCreateOpts())
			if previewErr != nil {
				t.Fatal(previewErr)
			}
			test.configure(fixture)
			opts := guardedCreateOpts()
			opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
			result, err := guardedCreateService(fixture).GuardedCreate(t.Context(), opts)
			if err == nil || result.Status != test.status || result.WriteAttempted != test.attempted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("remote content leaked: %v", err)
			}
		})
	}
}

func TestGuardedCreateRegistrationSuccessAndPostProofCollision(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint("collision=", collision), func(t *testing.T) {
			fixture := newGuardedCreateServer(t)
			root := filepath.Join(t.TempDir(), "mirror")
			opts := guardedCreateOpts()
			opts.Register, opts.Into = true, root
			if collision {
				if err := mirror.New(root).EnsureScaffold(); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveLocal(root, &config.LocalConfig{Render: &config.RenderConfig{Jira: &config.RenderService{Profile: "full"}}}); err != nil {
				t.Fatal(err)
			}
			if collision {
				if err := prepareMirrorBackendPopulation(root, "jira", fixture.server.URL, wikiExt, false); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := guardedCreateService(fixture).GuardedCreate(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if collision {
				if err := os.MkdirAll(filepath.Join(root, "OPS"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "OPS", "OPS-1.wiki"), []byte("owned"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
			result, err := guardedCreateService(fixture).GuardedCreate(t.Context(), opts)
			if collision {
				if err == nil || result.Status != "applied_not_registered" || !result.ReadbackReconciled || fixture.posts != 1 {
					t.Fatalf("result=%+v err=%v posts=%d", result, err, fixture.posts)
				}
				return
			}
			if err != nil || result.Status != "applied" || result.Registration == nil || result.Registration.Status != "registered" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			markdown, readErr := os.ReadFile(filepath.Join(root, "OPS", "OPS-1.md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, evidence := range []string{"| Status | Ready |", "| Assignee | Assigned User |", "| Reporter | Reporting User |", "| Labels | reviewed, guarded |"} {
				if !strings.Contains(string(markdown), evidence) {
					t.Fatalf("registered markdown omitted authoritative %q:\n%s", evidence, markdown)
				}
			}
			if result.RegistrationEffects == nil || !containsString(result.RegistrationEffects.PlannedFiles, ".atl/backend-bindings.json") {
				t.Fatalf("registration effects=%+v", result.RegistrationEffects)
			}
			for _, actual := range []string{".gitignore", ".atl/pending/jira/.mirror.lock", ".atl/backend-bindings.json", ".atl/state.json"} {
				if !containsString(result.RegistrationEffects.ActualFiles, actual) {
					t.Fatalf("registration actual effects omitted %q: %+v", actual, result.RegistrationEffects)
				}
			}
			states, stateErr := mirror.New(root).SyncStates()
			if stateErr != nil || len(states) != 1 {
				t.Fatalf("states=%v err=%v", states, stateErr)
			}
		})
	}
}

func TestGuardedCreateRegistrationCoordinatesBeforeFreshRootMutationAndQualifiesPrivacyGuard(t *testing.T) {
	fixture := newGuardedCreateServer(t)
	service := guardedCreateService(fixture)
	parent := t.TempDir()
	root := filepath.Join(parent, "fresh")
	opts := guardedCreateOpts()
	opts.Register, opts.Into = true, root
	preview, err := service.GuardedCreate(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := lockGuardedCreateRegistrationOwner(root)
	if err != nil {
		t.Fatal(err)
	}
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	result, applyErr := service.GuardedCreate(t.Context(), opts)
	_ = owner.Unlock()
	if applyErr == nil || result.Status != "blocked" || result.WriteAttempted || fixture.posts != 0 {
		t.Fatalf("result=%+v err=%v posts=%d", result, applyErr, fixture.posts)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("fresh root mutated before coordination: %v", statErr)
	}

	badRoot := filepath.Join(parent, "bad-privacy")
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, ".gitignore"), []byte("custom-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Into, opts.Apply, opts.ExpectedProposalHash = badRoot, false, ""
	preview, err = service.GuardedCreate(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	result, applyErr = service.GuardedCreate(t.Context(), opts)
	if applyErr == nil || result.Status != "blocked" || result.WriteAttempted || fixture.posts != 0 || result.RegistrationEffects == nil {
		t.Fatalf("result=%+v err=%v posts=%d", result, applyErr, fixture.posts)
	}
	if guardedCreatePathExists(badRoot, jiraPendingFieldsLockPath(badRoot)) {
		t.Fatal("internal staging lock created before privacy qualification")
	}
}

func TestGuardedCreateRegistrationHoldsSharedBackendBindingCASUntilCloseout(t *testing.T) {
	fixture := newGuardedCreateServer(t)
	fixture.postEntered, fixture.releasePost = make(chan struct{}), make(chan struct{})
	root := filepath.Join(t.TempDir(), "mirror")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := guardedCreateOpts()
	opts.Register, opts.Into = true, root
	service := guardedCreateService(fixture)
	preview, err := service.GuardedCreate(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	type outcome struct {
		result *JiraGuardedCreateResult
		err    error
	}
	applied := make(chan outcome, 1)
	go func() {
		result, createErr := service.GuardedCreate(t.Context(), opts)
		applied <- outcome{result: result, err: createErr}
	}()
	select {
	case <-fixture.postEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("guarded create did not reach the single POST")
	}
	bound, bindErr := ApplyMirrorBackendBind(root, "jira", fixture.server.URL, preview.BackendSHA256, "BIND")
	if !errors.Is(bindErr, domain.ErrCheckFailed) || bound != nil {
		t.Fatalf("concurrent bind result=%+v err=%v, want lock refusal", bound, bindErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".atl", "backend-bindings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("concurrent bind published state while create was in flight: %v", statErr)
	}
	close(fixture.releasePost)
	select {
	case got := <-applied:
		if got.err != nil || got.result.Status != "applied" || got.result.Registration == nil || got.result.Registration.Status != "registered" {
			t.Fatalf("result=%+v err=%v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("guarded create did not finish after POST release")
	}
	binding, present, err := mirror.New(root).BackendBinding("jira")
	if err != nil || !present || binding.OriginSHA256 != preview.BackendSHA256 {
		t.Fatalf("binding=%+v present=%t err=%v", binding, present, err)
	}
}

type delayedGuardedCreateTracker struct {
	domain.Tracker
	cancelAt string
	cancel   context.CancelFunc
	writes   int
}

func (d *delayedGuardedCreateTracker) ReadProjects(context.Context, bool) ([]domain.JiraProject, error) {
	archived := false
	return []domain.JiraProject{{ID: "7", Key: "OPS", Name: "Operations", Archived: &archived}}, nil
}

func (d *delayedGuardedCreateTracker) ReadQualifiedCreateMetadata(context.Context, string, string) (*domain.JiraQualifiedCreateMetadata, error) {
	required, optional, noDefault := true, false, false
	field := func(id, typ string, required *bool) domain.JiraQualifiedCreateField {
		return domain.JiraQualifiedCreateField{FieldID: id, Name: id, Required: required, Schema: &domain.JiraCreateFieldSchema{Type: typ, System: id}, HasDefaultValue: &noDefault, AllowedValuesPresent: true}
	}
	return &domain.JiraQualifiedCreateMetadata{Project: "OPS", IssueType: domain.JiraIssueType{ID: "3", Name: "Task"}, Fields: []domain.JiraQualifiedCreateField{
		field("project", "project", &required), field("issuetype", "issuetype", &required),
		field("summary", "string", &required), field("description", "string", &optional),
	}}, nil
}

func (d *delayedGuardedCreateTracker) PrepareGuardedCreate(request domain.JiraGuardedCreatePreparationRequest) (domain.JiraGuardedCreatePreparation, error) {
	payload, err := json.Marshal(map[string]any{"fields": map[string]any{
		"project": map[string]string{"key": request.ProjectKey}, "issuetype": map[string]string{"id": request.IssueTypeID},
		"summary": request.Summary, "description": string(request.Description),
	}})
	if d.cancelAt == "preview" && d.cancel != nil {
		d.cancel()
	}
	return domain.JiraGuardedCreatePreparation{Payload: payload}, err
}

func (d *delayedGuardedCreateTracker) WriteGuardedCreate(context.Context, domain.JiraGuardedCreateWrite) (domain.JiraGuardedCreateAcknowledgement, error) {
	d.writes++
	return domain.JiraGuardedCreateAcknowledgement{ID: "11"}, nil
}

func (d *delayedGuardedCreateTracker) ReadGuardedCreate(ctx context.Context, request domain.JiraGuardedCreateReadRequest) (domain.JiraGuardedCreateReadback, error) {
	fields := make(map[string]domain.JiraGuardedCreateFieldEvidence, len(request.Fields))
	for _, field := range request.Fields {
		fields[field] = domain.JiraGuardedCreateFieldEvidence{Present: true}
	}
	fields["description"] = domain.JiraGuardedCreateFieldEvidence{Present: true, Value: "wiki"}
	fields["created"] = domain.JiraGuardedCreateFieldEvidence{Present: true, Value: "2026-08-22T10:00:00.000+0000"}
	fields["updated"] = domain.JiraGuardedCreateFieldEvidence{Present: true, Value: "2026-08-22T10:00:01.000+0000"}
	if d.cancelAt == "readback" {
		<-ctx.Done()
	}
	return domain.JiraGuardedCreateReadback{
		ID: "11", Key: "OPS-1", ProjectID: "7", ProjectKey: "OPS", IssueTypeID: "3", Summary: "Reviewed",
		Description: fields["description"], Created: fields["created"], Updated: fields["updated"], Fields: fields,
	}, nil
}

func delayedGuardedCreateService(tracker *delayedGuardedCreateTracker) *JiraService {
	return NewJiraService(JiraDependencies{Tracker: tracker, BaseURL: "https://jira.example.invalid", Config: &config.Config{}})
}

func TestGuardedCreateDeadlineTruthAfterContextInsensitivePorts(t *testing.T) {
	t.Run("preview qualification completes late", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		tracker := &delayedGuardedCreateTracker{cancelAt: "preview", cancel: cancel}
		result, err := delayedGuardedCreateService(tracker).GuardedCreate(ctx, guardedCreateOptsWithoutFields())
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.WriteAttempted || tracker.writes != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, tracker.writes)
		}
	})

	t.Run("immutable id readback completes late", func(t *testing.T) {
		tracker := &delayedGuardedCreateTracker{}
		service := delayedGuardedCreateService(tracker)
		opts := guardedCreateOptsWithoutFields()
		preview, err := service.GuardedCreate(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		tracker.cancelAt, tracker.cancel = "readback", nil
		opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
		result, err := service.GuardedCreate(ctx, opts)
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "outcome_unknown" || !result.WriteAttempted || tracker.writes != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, tracker.writes)
		}
		var diagnostic interface{ DiagnosticAmbiguousWrite() bool }
		if !errors.As(err, &diagnostic) || !diagnostic.DiagnosticAmbiguousWrite() {
			t.Fatalf("error does not retain ambiguous terminal classification: %v", err)
		}
	})
}

func TestGuardedCreateRegistrationDeadlineAtLogicalCommitBoundary(t *testing.T) {
	for _, boundary := range []string{"before_register_new", "after_register_new"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newGuardedCreateServer(t)
			service := guardedCreateService(fixture)
			root := filepath.Join(t.TempDir(), "mirror")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			opts := guardedCreateOpts()
			opts.Register, opts.Into = true, root
			preview, err := service.GuardedCreate(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			service.guardedCreateRegistrationBoundary = func(got string, cancel func()) {
				if got == boundary {
					cancel()
				}
			}
			opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
			result, applyErr := service.GuardedCreate(t.Context(), opts)
			if fixture.posts != 1 || result.RegistrationEffects == nil || !containsString(result.RegistrationEffects.ActualFiles, ".atl/backend-bindings.json") {
				t.Fatalf("result=%+v err=%v posts=%d", result, applyErr, fixture.posts)
			}
			wikiPath := filepath.Join(root, "OPS", "OPS-1.wiki")
			switch boundary {
			case "before_register_new":
				if !errors.Is(applyErr, domain.ErrCheckFailed) || result.Status != "applied_not_registered" || result.Registration == nil ||
					result.Registration.Status != "not_registered" || !strings.Contains(result.Registration.Recovery, "do not repeat issue create") {
					t.Fatalf("result=%+v err=%v", result, applyErr)
				}
				for _, effect := range []string{".atl/state.lock", ".atl/state.json"} {
					if containsString(result.RegistrationEffects.ActualFiles, effect) {
						t.Fatalf("pre-commit cancellation reported nonexistent effect %q: %+v", effect, result.RegistrationEffects)
					}
				}
				for _, ext := range []string{".wiki", ".md", ".json"} {
					if _, err := os.Stat(filepath.Join(root, "OPS", "OPS-1"+ext)); !os.IsNotExist(err) {
						t.Fatalf("public artifact %s exists before logical commit: %v", ext, err)
					}
				}
				if states, err := mirror.New(root).SyncStates(); err != nil || len(states) != 0 {
					t.Fatalf("states=%v err=%v, want no registration state", states, err)
				}
			case "after_register_new":
				if applyErr != nil || result.Status != "applied" || result.Registration == nil || result.Registration.Status != "registered" {
					t.Fatalf("result=%+v err=%v", result, applyErr)
				}
				if _, err := os.Stat(wikiPath); err != nil {
					t.Fatalf("durable registered artifact: %v", err)
				}
			}
		})
	}
}

func guardedCreateOptsWithoutFields() JiraGuardedCreateOpts {
	return JiraGuardedCreateOpts{Project: "OPS", IssueType: "Task", Summary: "Reviewed", Description: []byte("wiki"), DescriptionSource: "wiki", Fields: map[string]domain.JiraFieldInput{}}
}

func TestGuardedCreateErrorFormattingAndUnwrapStayPrivate(t *testing.T) {
	private := "private-response-marker"
	err := guardedCreateFailure("safe message", fmt.Errorf("%w: %s", domain.ErrForbidden, private), false, false)
	var typed *jiraGuardedCreateError
	if !errors.As(err, &typed) || !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("typed=%T err=%v", typed, err)
	}
	rendered := []string{err.Error(), fmt.Sprintf("%+v", err)}
	for _, cause := range typed.Unwrap() {
		rendered = append(rendered, cause.Error(), fmt.Sprintf("%+v", cause))
	}
	for _, rendered := range rendered {
		if strings.Contains(rendered, private) {
			t.Fatalf("private cause leaked: %q", rendered)
		}
	}
}

func TestGuardedCreateLateRegistrationFailureReportsPossiblyCommittedState(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	wikiRel, err := mirror.PublicArtifactPathWithin(root, filepath.Join(root, "OPS", "OPS-1.wiki"))
	if err != nil {
		t.Fatal(err)
	}
	mdRel, _ := mirror.PublicArtifactPathWithin(root, filepath.Join(root, "OPS", "OPS-1.md"))
	jsonRel, _ := mirror.PublicArtifactPathWithin(root, filepath.Join(root, "OPS", "OPS-1.json"))
	body := []byte("wiki")
	artifacts := []mirror.RegistrationArtifact{
		{Path: wikiRel, Data: body, Mode: 0o644},
		{Path: mdRel, Data: []byte("markdown"), Mode: 0o644},
		{Path: jsonRel, Data: []byte("{}\n"), Mode: 0o644},
	}
	state := mirror.SyncState{ID: "OPS-1", Identity: "11", Hash: mirror.Hash(body), Path: wikiRel.String()}
	if err := m.RegisterNew(state, mirror.ViewState{}, wikiExt, body, artifacts); err != nil {
		t.Fatal(err)
	}
	if got, present, readErr := m.SyncStateOf(state.ID); readErr != nil || !present || got != state {
		t.Fatalf("state got=%+v present=%t err=%v want=%+v", got, present, readErr, state)
	}
	if got, present, readErr := m.ReadBaseBodyExt(state.ID, wikiExt); readErr != nil || !present || !bytes.Equal(got, body) {
		t.Fatalf("base=%q present=%t err=%v", got, present, readErr)
	}
	registration := newRegistration(root)
	err = classifyGuardedCreateRegistrationCommitFailure(m, registration, state, body, artifacts)
	if !errors.Is(err, domain.ErrCheckFailed) || registration.Status != "registration_outcome_unknown" || registration.Path != wikiRel.String() || registration.Reason != "local_registration_durability_unknown" || !strings.Contains(registration.Recovery, "may already be complete") {
		t.Fatalf("registration=%+v err=%v", registration, err)
	}
}

func TestGuardedCreateIssueMapsDedicatedReadbackFields(t *testing.T) {
	fields := map[string]domain.JiraGuardedCreateFieldEvidence{
		"status":   {Present: true, Value: map[string]any{"id": "5", "name": "Ready"}},
		"assignee": {Present: true, Value: map[string]any{"name": "user", "displayName": "Assigned User"}},
		"reporter": {Present: true, Value: map[string]any{"name": "reporter", "displayName": "Reporting User"}},
		"labels":   {Present: true, Value: []any{"reviewed", "guarded"}},
	}
	issue := guardedCreateIssue(
		&jiraGuardedCreateSnapshot{metadata: &domain.JiraQualifiedCreateMetadata{IssueType: domain.JiraIssueType{Name: "Task"}}},
		domain.JiraGuardedCreateReadback{ID: "11", Key: "OPS-1", ProjectKey: "OPS", Summary: "Reviewed", Description: domain.JiraGuardedCreateFieldEvidence{Present: true, Value: "wiki"}, Fields: fields},
	)
	if issue.Status != "Ready" || issue.StatusID != "5" || issue.Assignee != "Assigned User" || issue.Reporter != "Reporting User" || !reflect.DeepEqual(issue.Labels, []string{"reviewed", "guarded"}) {
		t.Fatalf("issue=%+v", issue)
	}
}

func TestGuardedCreateRequestAndDeadlineMaximaAreFixed(t *testing.T) {
	for _, test := range []struct {
		apply, register bool
		want            int
	}{
		{want: jiraGuardedCreatePreviewRequests},
		{register: true, want: jiraGuardedCreatePreviewRegisterRequests},
		{apply: true, want: jiraGuardedCreateApplyRequests},
		{apply: true, register: true, want: jiraGuardedCreateApplyRegisterRequests},
	} {
		opts := JiraGuardedCreateOpts{Apply: test.apply, Register: test.register}
		if got := guardedCreateMaxRequests(opts); got != test.want {
			t.Fatalf("apply=%t register=%t requests=%d want=%d", test.apply, test.register, got, test.want)
		}
		result := newGuardedCreateResult(opts)
		if result.Bounds.MaxRequests != test.want || result.Bounds.DeadlineMillis != 60_000 || result.Bounds.MaxResponseBytes != 16<<20 {
			t.Fatalf("apply=%t register=%t bounds=%+v", test.apply, test.register, result.Bounds)
		}
	}
}

func TestGuardedCreateProposalHashBindsEveryReviewedMemberAndIsInputOrderStable(t *testing.T) {
	base := &JiraGuardedCreateResult{
		SchemaVersion: 1, Operation: "create", BackendSHA256: strings.Repeat("a", 64), RequestedProject: "OPS",
		Project: JiraGuardedCreateProject{ID: "7", Key: "OPS"}, TypeSelector: JiraGuardedCreateDigest{SHA256: strings.Repeat("b", 64), Bytes: 4},
		IssueType: domain.JiraIssueType{ID: "3", Name: "Task"}, Summary: JiraGuardedCreateDigest{SHA256: strings.Repeat("c", 64), Bytes: 8},
		Description:   JiraGuardedCreateDescription{Source: "wiki", Present: true, SHA256: strings.Repeat("d", 64), Bytes: 4},
		Fields:        []JiraGuardedCreateField{{FieldID: "customfield_1", InputKind: "explicit_json", JSONKind: "object", NormalizedSHA: strings.Repeat("e", 64), NormalizedBytes: 11, SchemaSHA256: strings.Repeat("f", 64)}},
		MetadataCount: 5, MetadataSHA256: strings.Repeat("1", 64), RequestSHA256: strings.Repeat("2", 64), RequestBytes: 101,
		RegistrationRequested: true, RegistrationRootSHA256: strings.Repeat("3", 64), RenderProjectionSHA256: strings.Repeat("4", 64),
		Bounds: JiraGuardedCreateBounds{MaxFields: 1000, MaxInventoryRows: 1000, MaxStringBytes: 1024, MaxPayloadBytes: 64 << 20, MaxReadbackFields: 1024, MaxReadbackQueryBytes: 64 << 10, MaxRequests: jiraGuardedCreatePreviewRegisterRequests, MaxResponseBytes: 16 << 20, DeadlineMillis: 60_000},
	}
	clone := func() *JiraGuardedCreateResult {
		encoded, _ := json.Marshal(base)
		var result JiraGuardedCreateResult
		_ = json.Unmarshal(encoded, &result)
		return &result
	}
	want := guardedCreateProposalHash(base)
	mutations := []struct {
		name   string
		mutate func(*JiraGuardedCreateResult)
	}{
		{"schema_version", func(r *JiraGuardedCreateResult) { r.SchemaVersion++ }},
		{"operation", func(r *JiraGuardedCreateResult) { r.Operation += "x" }},
		{"backend_sha256", func(r *JiraGuardedCreateResult) { r.BackendSHA256 = strings.Repeat("9", 64) }},
		{"requested_project", func(r *JiraGuardedCreateResult) { r.RequestedProject += "X" }},
		{"project_id", func(r *JiraGuardedCreateResult) { r.Project.ID += "1" }},
		{"project_key", func(r *JiraGuardedCreateResult) { r.Project.Key += "X" }},
		{"project_archived", func(r *JiraGuardedCreateResult) { r.Project.Archived = true }},
		{"type_selector_sha", func(r *JiraGuardedCreateResult) { r.TypeSelector.SHA256 = strings.Repeat("8", 64) }},
		{"type_selector_bytes", func(r *JiraGuardedCreateResult) { r.TypeSelector.Bytes++ }},
		{"issue_type_id", func(r *JiraGuardedCreateResult) { r.IssueType.ID += "1" }},
		{"issue_type_name", func(r *JiraGuardedCreateResult) { r.IssueType.Name += "X" }},
		{"issue_type_subtask", func(r *JiraGuardedCreateResult) { r.IssueType.Subtask = true }},
		{"summary_sha", func(r *JiraGuardedCreateResult) { r.Summary.SHA256 = strings.Repeat("7", 64) }},
		{"summary_bytes", func(r *JiraGuardedCreateResult) { r.Summary.Bytes++ }},
		{"description_source", func(r *JiraGuardedCreateResult) { r.Description.Source += "x" }},
		{"description_present", func(r *JiraGuardedCreateResult) { r.Description.Present = false }},
		{"description_sha", func(r *JiraGuardedCreateResult) { r.Description.SHA256 = strings.Repeat("6", 64) }},
		{"description_bytes", func(r *JiraGuardedCreateResult) { r.Description.Bytes++ }},
		{"field_id", func(r *JiraGuardedCreateResult) { r.Fields[0].FieldID += "x" }},
		{"field_input_kind", func(r *JiraGuardedCreateResult) { r.Fields[0].InputKind += "x" }},
		{"field_json_kind", func(r *JiraGuardedCreateResult) { r.Fields[0].JSONKind += "x" }},
		{"field_normalized_sha", func(r *JiraGuardedCreateResult) { r.Fields[0].NormalizedSHA = strings.Repeat("5", 64) }},
		{"field_normalized_bytes", func(r *JiraGuardedCreateResult) { r.Fields[0].NormalizedBytes++ }},
		{"field_schema_sha", func(r *JiraGuardedCreateResult) { r.Fields[0].SchemaSHA256 = strings.Repeat("0", 64) }},
		{"metadata_count", func(r *JiraGuardedCreateResult) { r.MetadataCount++ }},
		{"metadata_sha", func(r *JiraGuardedCreateResult) { r.MetadataSHA256 = strings.Repeat("a", 64) }},
		{"request_sha", func(r *JiraGuardedCreateResult) { r.RequestSHA256 = strings.Repeat("b", 64) }},
		{"request_bytes", func(r *JiraGuardedCreateResult) { r.RequestBytes++ }},
		{"registration_requested", func(r *JiraGuardedCreateResult) { r.RegistrationRequested = false }},
		{"registration_root_sha", func(r *JiraGuardedCreateResult) { r.RegistrationRootSHA256 = strings.Repeat("c", 64) }},
		{"render_sha", func(r *JiraGuardedCreateResult) { r.RenderProjectionSHA256 = strings.Repeat("d", 64) }},
		{"max_fields", func(r *JiraGuardedCreateResult) { r.Bounds.MaxFields++ }},
		{"max_inventory_rows", func(r *JiraGuardedCreateResult) { r.Bounds.MaxInventoryRows++ }},
		{"max_string_bytes", func(r *JiraGuardedCreateResult) { r.Bounds.MaxStringBytes++ }},
		{"max_payload_bytes", func(r *JiraGuardedCreateResult) { r.Bounds.MaxPayloadBytes++ }},
		{"max_readback_fields", func(r *JiraGuardedCreateResult) { r.Bounds.MaxReadbackFields++ }},
		{"max_readback_query_bytes", func(r *JiraGuardedCreateResult) { r.Bounds.MaxReadbackQueryBytes++ }},
		{"max_response_bytes", func(r *JiraGuardedCreateResult) { r.Bounds.MaxResponseBytes++ }},
		{"deadline_millis", func(r *JiraGuardedCreateResult) { r.Bounds.DeadlineMillis++ }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := clone()
			mutation.mutate(changed)
			if got := guardedCreateProposalHash(changed); got == want {
				t.Fatalf("proposal hash did not bind %s", mutation.name)
			}
		})
	}

	inputsA := map[string]domain.JiraFieldInput{"customfield_2": {Value: "b"}, "customfield_1": {Value: "a"}}
	inputsB := map[string]domain.JiraFieldInput{"customfield_1": {Value: "a"}, "customfield_2": {Value: "b"}}
	schemasA := map[string]string{"customfield_2": strings.Repeat("2", 64), "customfield_1": strings.Repeat("1", 64)}
	schemasB := map[string]string{"customfield_1": strings.Repeat("1", 64), "customfield_2": strings.Repeat("2", 64)}
	field := func(id, sha string) domain.JiraGuardedCreatePreparedField {
		return domain.JiraGuardedCreatePreparedField{FieldID: id, InputKind: "legacy", JSONKind: "string", SHA256: sha, Bytes: 3}
	}
	fieldsA, errA := guardedCreateProposalFields(domain.JiraGuardedCreatePreparation{Fields: []domain.JiraGuardedCreatePreparedField{field("customfield_2", strings.Repeat("4", 64)), field("customfield_1", strings.Repeat("3", 64))}}, inputsA, schemasA)
	fieldsB, errB := guardedCreateProposalFields(domain.JiraGuardedCreatePreparation{Fields: []domain.JiraGuardedCreatePreparedField{field("customfield_1", strings.Repeat("3", 64)), field("customfield_2", strings.Repeat("4", 64))}}, inputsB, schemasB)
	if errA != nil || errB != nil || !reflect.DeepEqual(fieldsA, fieldsB) {
		t.Fatalf("order stability fieldsA=%+v errA=%v fieldsB=%+v errB=%v", fieldsA, errA, fieldsB, errB)
	}
	orderedA, orderedB := clone(), clone()
	orderedA.Fields, orderedB.Fields = fieldsA, fieldsB
	if guardedCreateProposalHash(orderedA) != guardedCreateProposalHash(orderedB) {
		t.Fatal("proposal hash depends on field map or adapter projection order")
	}
	modeBound := clone()
	modeBound.Bounds.MaxRequests = jiraGuardedCreateApplyRegisterRequests
	if guardedCreateProposalHash(modeBound) != want {
		t.Fatal("mode-specific max_requests changed the fixed four-mode request-maxima proposal")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestGuardedCreateMetadataRefusesUnknownRequiredAndNonScreenFields(t *testing.T) {
	fixture := newGuardedCreateServer(t)
	service := guardedCreateService(fixture)
	opts := guardedCreateOpts()
	opts.Fields["not_on_screen"] = domain.JiraFieldInput{Value: "x"}
	result, err := service.GuardedCreate(t.Context(), opts)
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || fixture.posts != 0 {
		t.Fatalf("result=%+v err=%v posts=%d", result, err, fixture.posts)
	}
}
