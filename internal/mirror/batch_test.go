package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestLoadCSFManyPreservesSelectionOrderAndViewSnapshot(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	var paths []string
	for _, id := range []string{"1", "2"} {
		page := &domain.Resource{ID: id, Title: "Page " + id, SpaceKey: "DOC", Version: 1, Body: []byte("<p>" + id + "</p>")}
		dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
		if err := m.Write(dir, slug, page, nil); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Join(dir, slug+".csf"))
	}
	locals, bodies, err := m.LoadCSFMany([]string{paths[1], paths[0]})
	if err != nil || len(locals) != 2 || locals[0].Meta.ID != "2" || locals[1].Meta.ID != "1" || string(bodies[0]) != "<p>2</p>" {
		t.Fatalf("locals=%+v bodies=%q err=%v", locals, bodies, err)
	}
	if err := m.SaveViewStates(map[string]ViewState{"2": {Sections: []string{"content"}}}); err != nil {
		t.Fatal(err)
	}
	views, err := m.ViewStatesOf([]string{"1", "2"})
	if err != nil || len(views) != 1 || len(views["2"].Sections) != 1 {
		t.Fatalf("views=%+v err=%v", views, err)
	}
}

func TestReadSnapshotCapturesSidecarButStreamsNativeBodies(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	page := &domain.Resource{ID: "1", Title: "Page 1", SpaceKey: "DOC", Version: 1, Body: []byte("<p>old</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveViewStates(map[string]ViewState{"1": {Sections: []string{"content"}}}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveViewStates(map[string]ViewState{"1": {Sections: []string{"metadata"}}}); err != nil {
		t.Fatal(err)
	}
	csfPath := filepath.Join(dir, slug+".csf")
	if err := os.WriteFile(csfPath, []byte("<p>new</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	local, body, err := snapshot.LoadCSF(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<p>new</p>" || !local.Dirty || local.Synced == nil || local.Synced.Version != 1 {
		t.Fatalf("local=%+v body=%q", local, body)
	}
	view, ok := snapshot.ViewStateOf("1")
	if !ok || len(view.Sections) != 1 || view.Sections[0] != "content" {
		t.Fatalf("captured view=%+v ok=%t", view, ok)
	}
	view.Sections[0] = "mutated-by-caller"
	again, ok := snapshot.ViewStateOf("1")
	if !ok || again.Sections[0] != "content" {
		t.Fatalf("snapshot exposed mutable view state: %+v", again)
	}
	fresh, ok, err := m.ViewStateOf("1")
	if err != nil || !ok || fresh.Sections[0] != "metadata" {
		t.Fatalf("fresh view=%+v ok=%t err=%v", fresh, ok, err)
	}
}

func TestReadSnapshotDoesNotReDecodeCorruptedSidecar(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	page := &domain.Resource{ID: "1", Title: "Page 1", SpaceKey: "DOC", Version: 1, Body: []byte("<p>body</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".atl", "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	csfPath := filepath.Join(dir, slug+".csf")
	if local, _, err := snapshot.LoadCSF(csfPath); err != nil || local.Synced == nil {
		t.Fatalf("captured snapshot local=%+v err=%v", local, err)
	}
	if _, _, err := m.LoadCSF(csfPath); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("fresh load error=%v, want corrupt-sidecar check failure", err)
	}
}

func TestReadSnapshotListsCSFInPathOrder(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	for _, page := range []*domain.Resource{
		{ID: "2", Title: "Zulu", SpaceKey: "DOC", Version: 1, Body: []byte("<p>2</p>")},
		{ID: "1", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: []byte("<p>1</p>")},
	} {
		dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
		if err := m.Write(dir, slug, page, nil); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	locals, err := snapshot.ListCSF()
	if err != nil {
		t.Fatal(err)
	}
	if len(locals) != 2 || locals[0].Meta.ID != "1" || locals[1].Meta.ID != "2" || locals[0].Path >= locals[1].Path {
		t.Fatalf("locals=%+v", locals)
	}
	states := snapshot.SyncStates()
	if len(states) != 2 || states[0].ID != "1" || states[1].ID != "2" {
		t.Fatalf("states=%+v", states)
	}
}
