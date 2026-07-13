package mirror

import (
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
