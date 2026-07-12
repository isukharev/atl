package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// stubStore implements only the DocStore methods the push/status paths touch;
// the embedded interface makes any other call panic (it should not happen).
type stubStore struct {
	domain.DocStore
	meta         *domain.PageMeta
	metaErr      error
	updateCalled bool
	newVer       int
	updateErr    error
	page         *domain.Resource
	omitBody     bool
}

func (s *stubStore) GetMeta(context.Context, string) (*domain.PageMeta, error) {
	return s.meta, s.metaErr
}

func (s *stubStore) UpdatePage(context.Context, string, int, string, []byte, bool) (int, error) {
	s.updateCalled = true
	return s.newVer, s.updateErr
}

func (s *stubStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	if s.page != nil {
		page := *s.page
		if !s.omitBody {
			page.BodyPresent = true
		}
		return &page, nil
	}
	return &domain.Resource{ID: "123", Title: "T", Body: []byte("<p>x</p>"), BodyPresent: !s.omitBody, Version: s.newVer}, nil
}

func TestAppendWarningPreservesIndependentDegradations(t *testing.T) {
	got := appendWarning(appendWarning("first", "second"), "third")
	if got != "first; second; third" {
		t.Fatalf("warnings=%q", got)
	}
}

// syncedMirror lays down a page whose on-disk body matches its last-synced
// state (so lc.Dirty == false) and returns the mirror root and the .csf path.
func syncedMirror(t *testing.T, version int) (root, csfPath string) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{ID: "123", Title: "T", SpaceKey: "SP", Version: version, Body: []byte("<p>x</p>")}
	dir, slug := m.PageDir(page.SpaceKey, page.Ancestors, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(dir, slug+".csf")
}

func TestPushSkipsUnchangedFile(t *testing.T) {
	root, csfPath := syncedMirror(t, 3)
	stub := &stubStore{newVer: 4}
	svc := &ConfluenceService{store: stub}
	res, err := svc.Push(context.Background(), csfPath, PushOpts{Into: root})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Skipped != "unchanged" {
		t.Fatalf("expected unchanged skip, got %+v", res.Items)
	}
	if stub.updateCalled {
		t.Error("UpdatePage must not be called for an unchanged file (no no-op revision)")
	}
}

func TestPushDryRunReportsDrift(t *testing.T) {
	root, csfPath := syncedMirror(t, 3)
	// Make the file dirty so the dry-run drift check runs.
	if err := os.WriteFile(csfPath, []byte("<p>x edited</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &stubStore{meta: &domain.PageMeta{ID: "123", Version: 5}} // remote moved past synced v3
	svc := &ConfluenceService{store: stub}
	res, err := svc.Push(context.Background(), csfPath, PushOpts{DryRun: true, Into: root})
	if err != nil {
		t.Fatalf("dry-run push: %v", err)
	}
	it := res.Items[0]
	if !it.Drifted || it.Warning == "" {
		t.Fatalf("dry-run should report drift, got %+v", it)
	}
	if stub.updateCalled {
		t.Error("dry-run must not push")
	}
}

func TestPushPreservesLocalMirrorWhenRefreshOmitsNativeBody(t *testing.T) {
	root, csfPath := syncedMirror(t, 3)
	edited := []byte("<p>x edited</p>")
	if err := os.WriteFile(csfPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	stub := &stubStore{
		newVer:   4,
		omitBody: true,
		page:     &domain.Resource{ID: "123", Title: "T", SpaceKey: "SP", Version: 4},
	}
	svc := &ConfluenceService{store: stub}
	res, err := svc.Push(context.Background(), csfPath, PushOpts{Into: root})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Items) != 1 || !res.Items[0].Pushed || !strings.Contains(res.Items[0].Warning, "partial body projection") {
		t.Fatalf("push result = %+v", res.Items)
	}
	got, err := os.ReadFile(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatalf("local body was replaced after partial refresh: %q", got)
	}
	lc, _, err := mirror.New(root).LoadCSF(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if lc.Synced == nil || lc.Synced.Version != 3 {
		t.Fatalf("partial refresh changed sync state: %+v", lc.Synced)
	}
}

func TestPushRefusesStaleRelocationCopyEvenWithForce(t *testing.T) {
	root, oldCSF := syncedMirror(t, 3)
	m := mirror.New(root)
	newPage := &domain.Resource{ID: "123", Title: "Renamed", SpaceKey: "SP", Version: 3, Body: []byte("<p>x</p>")}
	newDir, newSlug := m.PageDir(newPage.SpaceKey, nil, newPage.Title)
	if err := m.Write(newDir, newSlug, newPage, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldCSF, []byte("<p>stale local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		target string
		dryRun bool
	}{
		{name: "single forced", target: oldCSF},
		{name: "single dry-run forced", target: oldCSF, dryRun: true},
		{name: "directory forced", target: root},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubStore{}
			res, err := (&ConfluenceService{store: stub}).Push(context.Background(), tt.target, PushOpts{Into: root, Force: true, DryRun: tt.dryRun})
			if !errors.Is(err, domain.ErrCheckFailed) || stub.updateCalled {
				t.Fatalf("res=%+v err=%v update=%v", res, err, stub.updateCalled)
			}
			if len(res.Items) != 1 || res.Items[0].Skipped != "non-canonical-path" {
				t.Fatalf("unexpected refusal result: %+v", res.Items)
			}
		})
	}
}

func TestStatusReportsRemoteCheckError(t *testing.T) {
	root, _ := syncedMirror(t, 3)
	stub := &stubStore{metaErr: domain.ErrForbidden}
	svc := &ConfluenceService{store: stub}
	entries, err := svc.Status(context.Background(), root, true)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].RemoteError != "forbidden" {
		t.Errorf("a failed remote check must be reported, got RemoteError=%q Drifted=%v", entries[0].RemoteError, entries[0].Drifted)
	}
}

func TestPushErrorPrecedence(t *testing.T) {
	// A version conflict must win over a generic/forbidden error so the batch
	// exit code stays actionable (exit 5).
	if got := moreSevereErr(domain.ErrForbidden, domain.ErrVersionConflict); got != domain.ErrVersionConflict {
		t.Errorf("version conflict should outrank forbidden, got %v", got)
	}
	if got := moreSevereErr(domain.ErrVersionConflict, domain.ErrForbidden); got != domain.ErrVersionConflict {
		t.Errorf("order must not matter, got %v", got)
	}
	if got := moreSevereErr(nil, domain.ErrNotFound); got != domain.ErrNotFound {
		t.Errorf("first error should be kept, got %v", got)
	}
}

func TestDiffFragmentsDeterministicOrder(t *testing.T) {
	// Two drawio fragments removed; order must be stable (document order) across
	// repeated runs, not Go's randomized map iteration.
	old := []byte(`<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">alpha</ac:parameter></ac:structured-macro>` +
		`<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">beta</ac:parameter></ac:structured-macro>`)
	neu := []byte(`<p>nothing</p>`)
	first, _ := diffFragments(old, neu)
	if len(first) != 2 {
		t.Fatalf("expected 2 removed, got %d", len(first))
	}
	for i := 0; i < 50; i++ {
		got, _ := diffFragments(old, neu)
		if len(got) != 2 || got[0].Key != first[0].Key || got[1].Key != first[1].Key {
			t.Fatalf("non-deterministic removed order: %v vs %v", keysOf(got), keysOf(first))
		}
	}
}

func keysOf(rs []domain.Ref) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Key
	}
	return out
}

// perIDStore returns a different UpdatePage error per page id, so a batch push
// over a directory can be exercised end-to-end (the single-error stubStore can't).
type perIDStore struct {
	domain.DocStore
	errByID map[string]error
}

func (s *perIDStore) UpdatePage(_ context.Context, id string, _ int, _ string, _ []byte, _ bool) (int, error) {
	if err := s.errByID[id]; err != nil {
		return 0, err
	}
	return 1, nil
}

// mkDirty writes a synced page then edits it so lc.Dirty == true.
func mkDirty(t *testing.T, m *mirror.Mirror, id, title string) {
	t.Helper()
	page := &domain.Resource{ID: id, Title: title, SpaceKey: "SP", Version: 1, Body: []byte("<p>x</p>")}
	dir, slug := m.PageDir(page.SpaceKey, page.Ancestors, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".csf"), []byte("<p>"+id+" edited</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A directory push with a version-conflict on one page and a forbidden error on
// another must return the version-conflict (exit 5), regardless of order — the
// headline guarantee of the batch precedence logic, exercised through svc.Push.
func TestPushBatchSurfacesVersionConflict(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	// "aaa" sorts before "zzz", so the version-conflict page is processed LAST: a
	// regression to "first error wins" would surface forbidden (exit 6) instead.
	mkDirty(t, m, "1", "aaa")
	mkDirty(t, m, "2", "zzz")
	stub := &perIDStore{errByID: map[string]error{
		"1": fmt.Errorf("%w: nope", domain.ErrForbidden),
		"2": fmt.Errorf("%w: stale", domain.ErrVersionConflict),
	}}
	svc := &ConfluenceService{store: stub}
	res, err := svc.Push(context.Background(), root, PushOpts{Into: root})
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res.Items))
	}
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("batch push error = %v, want version-conflict to win", err)
	}
}

func TestPushMissingTargetIsUsageError(t *testing.T) {
	svc := &ConfluenceService{store: &stubStore{}}
	res, err := svc.Push(context.Background(), filepath.Join(t.TempDir(), "nope.csf"), PushOpts{Into: t.TempDir()})
	if res != nil {
		t.Fatalf("expected nil result for unresolvable target, got %+v", res)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing push target must map to ErrNotFound (exit 4), got %v", err)
	}
}
