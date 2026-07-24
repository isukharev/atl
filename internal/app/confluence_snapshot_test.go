package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type snapshotMetaStore struct {
	domain.DocStore
	meta           map[string]*domain.PageMeta
	errs           map[string]error
	calls          []string
	singleAttempts []bool
	onGet          func()
}

func (s *snapshotMetaStore) GetMeta(ctx context.Context, id string) (*domain.PageMeta, error) {
	s.calls = append(s.calls, id)
	s.singleAttempts = append(s.singleAttempts, domain.SingleAttempt(ctx))
	if s.onGet != nil {
		s.onGet()
	}
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	return s.meta[id], nil
}

func TestConfluenceMirrorRemoteSnapshotHoldsMutationLockThroughProbe(t *testing.T) {
	root := t.TempDir()
	writeDiffPage(t, root, "301", "stable", `<p>body</p>`)
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	var mutationBlocked bool
	store := &snapshotMetaStore{
		meta: map[string]*domain.PageMeta{"301": {ID: "301", Version: 3}},
		onGet: func() {
			candidate, candidateErr := lockConfluenceMutations(root, false)
			mutationBlocked = errors.Is(candidateErr, domain.ErrCheckFailed) && candidate == nil
			if candidate != nil {
				_ = candidate.Unlock()
			}
		},
	}
	got, err := (&ConfluenceService{store: store}).SnapshotMirror(context.Background(), root, true)
	if err != nil || got == nil || !got.Complete || !mutationBlocked {
		t.Fatalf("snapshot=%+v mutation_blocked=%t err=%v", got, mutationBlocked, err)
	}
	lock, err = lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatalf("mutation remained blocked after snapshot: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestConfluenceMirrorRemoteSnapshotDiscardsLegacyRaceWithoutRetry(t *testing.T) {
	root := t.TempDir()
	writeDiffPage(t, root, "302", "stable", `<p>body</p>`)
	lockPath := filepath.Join(root, ".atl", confluenceMutationLockName)
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	store := &snapshotMetaStore{
		meta: map[string]*domain.PageMeta{"302": {ID: "302", Version: 3}},
		onGet: func() {
			lock, lockErr := lockConfluenceMutations(root, false)
			if lockErr != nil {
				t.Errorf("bootstrap mutation lock: %v", lockErr)
				return
			}
			_ = lock.Unlock()
		},
	}
	got, err := (&ConfluenceService{store: store}).SnapshotMirror(context.Background(), root, true)
	if !errors.Is(err, domain.ErrCheckFailed) || got != nil || len(store.calls) != 1 {
		t.Fatalf("snapshot=%+v calls=%v err=%v", got, store.calls, err)
	}
	for _, private := range []string{root, "302", "stable"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("content-free race error leaked %q: %v", private, err)
		}
	}
}

func TestConfluenceMirrorSnapshotReconcilesContentFreeHealthBuckets(t *testing.T) {
	root := t.TempDir()
	paths := map[string]string{}
	for _, page := range []struct {
		id, slug string
	}{
		{"101", "stable"}, {"102", "edited"}, {"103", "removed"},
		{"104", "malformed"}, {"105", "missing-base"}, {"106", "future-view"},
	} {
		paths[page.id] = writeDiffPage(t, root, page.id, page.slug, `<p>Private body `+page.id+`</p>`)
	}
	viewStates := map[string]mirror.ViewState{}
	for id := range paths {
		viewStates[id] = mirror.ViewState{Sections: []string{"content"}}
	}
	if err := mirror.New(root).SaveViewStates(viewStates); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths["102"], []byte(`<p>Changed body</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(paths["102"], ".csf")+".md", []byte("<!-- atl:document confluence-page v3 -->\nlegacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths["103"]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths["104"], []byte(`<p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(strings.TrimSuffix(paths["104"], ".csf") + ".md"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".atl", "base", "105.csf")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(paths["105"], ".csf")+".md", []byte("plain markdown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(paths["106"], ".csf")+".md", []byte("<!-- atl:document confluence-page v99 -->\nfuture\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SnapshotConfluenceMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.Service != "confluence" || got.Complete || !got.Reconciled {
		t.Fatalf("snapshot metadata=%+v", got)
	}
	if got.Local != (ConfluenceMirrorLocalSummary{Present: 5, Clean: 3, LocallyEdited: 2, Tracked: 5, Reconciled: true}) {
		t.Fatalf("local=%+v", got.Local)
	}
	wantNative := ConfluenceMirrorNativeSummary{
		Total: 6, Unchanged: 2, Added: 0, Removed: 1, Modified: 1, Malformed: 1, MissingBaseline: 1,
		BaselinePresent: 5, BaselineMissing: 1, BaselineValid: 5, Reconciled: true,
	}
	if got.Native != wantNative {
		t.Fatalf("native=%+v want=%+v", got.Native, wantNative)
	}
	wantValidation := ConfluenceMirrorValidationSummary{Total: 6, Present: 5, Absent: 1, Valid: 4, Invalid: 1, Reconciled: true}
	if got.Validation != wantValidation {
		t.Fatalf("validation=%+v want=%+v", got.Validation, wantValidation)
	}
	wantRender := ConfluenceMirrorRenderSummary{
		Expected: 5, Present: 4, Missing: 1, Current: 1, Legacy: 1, MissingMarker: 1, Unsupported: 1,
		StateRecorded: 5, RendererCompatible: false, Reconciled: true,
	}
	if got.Render != wantRender {
		t.Fatalf("render=%+v want=%+v", got.Render, wantRender)
	}
	wantRemote := ConfluenceMirrorRemoteSummary{Eligible: 5, NotAttempted: 5, Reconciled: true}
	if got.Remote != wantRemote || got.RemoteRequested {
		t.Fatalf("remote=%+v requested=%t", got.Remote, got.RemoteRequested)
	}

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Private body", "Changed body", "stable", "101", root, "v99"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("snapshot leaked %q: %s", private, data)
		}
	}
}

func TestConfluenceMirrorSnapshotStopsBeforeRemoteOnBaselineMismatch(t *testing.T) {
	root := t.TempDir()
	writeDiffPage(t, root, "201", "blocked", `<p>current</p>`)
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "201.csf"), []byte(`<p>other</p>`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &snapshotMetaStore{meta: map[string]*domain.PageMeta{"201": {ID: "201", Version: 3}}}
	svc := &ConfluenceService{store: store}
	got, err := svc.SnapshotMirror(context.Background(), root, true)
	if !errors.Is(err, domain.ErrCheckFailed) || got == nil {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	for _, private := range []string{"201", "blocked", root} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("content-free snapshot error leaked %q: %v", private, err)
		}
	}
	if len(store.calls) != 0 || got.Native.BaselineMismatch != 1 || got.Complete || !got.Reconciled ||
		!got.Remote.Requested || got.Remote.Attempted != 0 || got.Remote.NotAttempted != 1 {
		t.Fatalf("snapshot=%+v calls=%v", got, store.calls)
	}
}

func TestConfluenceMirrorSnapshotCoordinatesWithoutCreatingMutationLock(t *testing.T) {
	root := t.TempDir()
	writeDiffPage(t, root, "201", "stable", `<p>body</p>`)
	lockPath := filepath.Join(root, ".atl", confluenceMutationLockName)
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	got, err := SnapshotConfluenceMirror(root)
	if err != nil || got == nil || !got.Complete {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("write-free snapshot created mutation lock: %v", err)
	}

	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()
	store := &snapshotMetaStore{meta: map[string]*domain.PageMeta{"201": {ID: "201", Version: 3}}}
	got, err = (&ConfluenceService{store: store}).SnapshotMirror(context.Background(), root, true)
	if !errors.Is(err, domain.ErrCheckFailed) || got != nil || len(store.calls) != 0 {
		t.Fatalf("busy snapshot=%+v calls=%v err=%v", got, store.calls, err)
	}
	for _, private := range []string{root, "201", "stable"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("content-free busy error leaked %q: %v", private, err)
		}
	}
}

func TestConfluenceMirrorSnapshotIncompleteLocalEvidenceStopsRemote(t *testing.T) {
	tests := map[string]struct {
		mutate       func(*testing.T, string, string)
		wantCheckErr bool
	}{
		"missing baseline": {
			mutate: func(t *testing.T, root, _ string) {
				if err := os.Remove(filepath.Join(root, ".atl", "base", "211.csf")); err != nil {
					t.Fatal(err)
				}
			},
		},
		"malformed candidate": {
			mutate: func(t *testing.T, _, path string) {
				if err := os.WriteFile(path, []byte(`<p>`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		"unreadable view": {
			mutate: func(t *testing.T, _, path string) {
				mdPath := strings.TrimSuffix(path, ".csf") + ".md"
				if err := os.Remove(mdPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(mdPath, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantCheckErr: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := writeDiffPage(t, root, "211", "local-block", `<p>body</p>`)
			test.mutate(t, root, path)
			store := &snapshotMetaStore{meta: map[string]*domain.PageMeta{"211": {ID: "211", Version: 3}}}
			got, err := (&ConfluenceService{store: store}).SnapshotMirror(context.Background(), root, true)
			if errors.Is(err, domain.ErrCheckFailed) != test.wantCheckErr {
				t.Fatalf("snapshot err=%v want_check_failed=%t", err, test.wantCheckErr)
			}
			if got == nil || got.Complete || !got.Reconciled || !got.Remote.Requested || got.Remote.Attempted != 0 ||
				got.Remote.NotAttempted != 1 || len(store.calls) != 0 {
				t.Fatalf("snapshot=%+v calls=%v", got, store.calls)
			}
			if test.wantCheckErr {
				if got.Render.Unreadable != 1 {
					t.Fatalf("unreadable render summary=%+v", got.Render)
				}
				for _, private := range []string{root, "211", "local-block"} {
					if strings.Contains(err.Error(), private) {
						t.Fatalf("content-free snapshot error leaked %q: %v", private, err)
					}
				}
			}
		})
	}
}

func TestConfluenceMirrorSnapshotRemoteUsesOneProbePerTrackedPage(t *testing.T) {
	root := t.TempDir()
	for _, page := range []struct{ id, slug string }{{"301", "one"}, {"302", "two"}, {"303", "three"}} {
		writeDiffPage(t, root, page.id, page.slug, `<p>body</p>`)
	}
	store := &snapshotMetaStore{
		meta: map[string]*domain.PageMeta{
			"301": {ID: "301", Version: 3},
			"302": {ID: "302", Version: 4},
		},
		errs: map[string]error{"303": errors.New("offline")},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	want := ConfluenceMirrorRemoteSummary{
		Requested: true, Eligible: 3, Attempted: 3, Checked: 2, InSync: 1, Drifted: 1, Unavailable: 1, Reconciled: true,
	}
	if got.Remote != want || got.Complete || !got.Reconciled || !got.RemoteRequested {
		t.Fatalf("snapshot=%+v", got)
	}
	if strings.Join(store.calls, ",") != "301,303,302" {
		t.Fatalf("remote calls=%v", store.calls)
	}
	for i, single := range store.singleAttempts {
		if !single {
			t.Fatalf("remote call %d did not carry the single-attempt policy", i)
		}
	}
}

func TestConfluenceMirrorSnapshotSkipsNonCanonicalCopyRemotely(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	old := &domain.Resource{ID: "401", Title: "Old", SpaceKey: "DOC", Version: 3, Body: []byte(`<p>body</p>`)}
	oldDir, oldSlug := m.PageDir(old.SpaceKey, nil, old.Title)
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	current := *old
	current.Title = "Current"
	currentDir, currentSlug := m.PageDir(current.SpaceKey, nil, current.Title)
	if err := m.Write(currentDir, currentSlug, &current, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveViewStates(map[string]mirror.ViewState{"401": {Sections: []string{"content"}}}); err != nil {
		t.Fatal(err)
	}
	store := &snapshotMetaStore{meta: map[string]*domain.PageMeta{"401": {ID: "401", Version: 3}}}
	got, err := (&ConfluenceService{store: store}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Local.Present != 2 || got.Local.Tracked != 1 || got.Local.Untracked != 1 || got.Local.NonCanonical != 1 ||
		got.Native.BaselinePresent != 1 || got.Native.BaselineMissing != 1 ||
		got.Render.StateRecorded != 1 || got.Render.StateMissing != 1 ||
		got.Remote.Eligible != 1 || got.Remote.Attempted != 1 || got.Remote.NotAttempted != 1 || len(store.calls) != 1 {
		t.Fatalf("snapshot=%+v calls=%v", got, store.calls)
	}
}

func TestConfluenceMirrorSnapshotEmptyMirror(t *testing.T) {
	root := t.TempDir()
	if err := mirror.New(root).EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	got, err := SnapshotConfluenceMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || !got.Reconciled || !got.Local.Reconciled || !got.Native.Reconciled ||
		!got.Validation.Reconciled || !got.Render.Reconciled || !got.Render.RendererCompatible || !got.Remote.Reconciled {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestConfluenceViewMarkerClass(t *testing.T) {
	for name, test := range map[string]struct {
		body, want string
	}{
		"current CRLF": {mirror.ConfluenceDocumentMarker + "\r\nbody", "current"},
		"legacy":       {"<!-- atl:document confluence-page v1 -->\nbody", "legacy"},
		"unsupported":  {"<!-- atl:document confluence-page v88 -->\nbody", "unsupported"},
		"missing":      {"# plain\n", "missing"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := confluenceViewMarkerClass([]byte(test.body)); got != test.want {
				t.Fatalf("class=%q want=%q", got, test.want)
			}
		})
	}
}
