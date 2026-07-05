package mirror

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// TestClaimPageDirDisambiguatesCollision is the regression test for lossy-slug
// collisions: two sibling titles that slugify identically must land in two
// dirs, and the first page's bytes must survive the second page's pull intact.
func TestClaimPageDirDisambiguatesCollision(t *testing.T) {
	m := New(t.TempDir())
	a := &domain.Resource{ID: "100", Title: "Foo Bar", SpaceKey: "S", Version: 1, Body: []byte("<p>page A</p>")}
	b := &domain.Resource{ID: "200", Title: "Foo-Bar?", SpaceKey: "S", Version: 1, Body: []byte("<p>page B</p>")}

	dirA, slugA, err := m.ClaimPageDir(a.SpaceKey, nil, a.Title, a.ID)
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	if err := m.Write(dirA, slugA, a, nil); err != nil {
		t.Fatal(err)
	}

	dirB, slugB, err := m.ClaimPageDir(b.SpaceKey, nil, b.Title, b.ID)
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if dirB == dirA {
		t.Fatalf("collision not disambiguated: both pages claimed %s", dirA)
	}
	if slugB != slugA+"-200" {
		t.Errorf("slug B = %q, want %q", slugB, slugA+"-200")
	}
	if err := m.Write(dirB, slugB, b, nil); err != nil {
		t.Fatal(err)
	}

	gotA, err := os.ReadFile(filepath.Join(dirA, slugA+".csf"))
	if err != nil {
		t.Fatalf("page A csf gone after B's pull: %v", err)
	}
	if !bytes.Equal(gotA, a.Body) {
		t.Errorf("page A body overwritten: got %q", gotA)
	}
	gotB, err := os.ReadFile(filepath.Join(dirB, slugB+".csf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotB, b.Body) {
		t.Errorf("page B body = %q", gotB)
	}
}

// TestClaimPageDirStableAcrossRepulls locks that a page keeps its dir on
// re-pull, both for the plain slug and for a disambiguated one.
func TestClaimPageDirStableAcrossRepulls(t *testing.T) {
	m := New(t.TempDir())
	a := &domain.Resource{ID: "100", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>a</p>")}
	b := &domain.Resource{ID: "200", Title: "Doc!", SpaceKey: "S", Version: 1, Body: []byte("<p>b</p>")}
	for _, p := range []*domain.Resource{a, b} {
		dir, slug, err := m.ClaimPageDir(p.SpaceKey, nil, p.Title, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Write(dir, slug, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*domain.Resource{a, b} {
		dir1, slug1, err := m.ClaimPageDir(p.SpaceKey, nil, p.Title, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		dir2, slug2, err := m.ClaimPageDir(p.SpaceKey, nil, p.Title, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if dir1 != dir2 || slug1 != slug2 {
			t.Errorf("claim for id %s not stable: (%s,%s) then (%s,%s)", p.ID, dir1, slug1, dir2, slug2)
		}
	}
}

// TestClaimPageDirDivertIsSticky: once a page has been diverted to an
// id-suffixed dir, it stays there even after the plain dir frees up —
// migrating back would fork the page into two dirs and orphan the old one.
func TestClaimPageDirDivertIsSticky(t *testing.T) {
	m := New(t.TempDir())
	a := &domain.Resource{ID: "100", Title: "Foo Bar", SpaceKey: "S", Version: 1, Body: []byte("<p>a</p>")}
	b := &domain.Resource{ID: "200", Title: "Foo-Bar?", SpaceKey: "S", Version: 1, Body: []byte("<p>b</p>")}
	for _, p := range []*domain.Resource{a, b} {
		dir, slug, err := m.ClaimPageDir(p.SpaceKey, nil, p.Title, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Write(dir, slug, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	dirB, slugB, err := m.ClaimPageDir(b.SpaceKey, nil, b.Title, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Page A disappears (deleted remotely, dir removed locally).
	dirA, _ := m.PageDir(a.SpaceKey, nil, a.Title)
	if err := os.RemoveAll(dirA); err != nil {
		t.Fatal(err)
	}
	dirB2, slugB2, err := m.ClaimPageDir(b.SpaceKey, nil, b.Title, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dirB2 != dirB || slugB2 != slugB {
		t.Errorf("diverted page migrated after plain dir freed: (%s,%s) -> (%s,%s)", dirB, slugB, dirB2, slugB2)
	}
}

// TestClaimPageDirUnreadableMetaIsForeign asserts fail-closed behavior: page
// files with an absent or corrupt meta must be treated as another page's, so
// the newcomer is diverted instead of overwriting them.
func TestClaimPageDirUnreadableMetaIsForeign(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, dir, slug string)
	}{
		{"csf without meta", func(t *testing.T, dir, slug string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, slug+".csf"), []byte("<p>x</p>"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt meta", func(t *testing.T, dir, slug string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, slug+".meta.json"), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"meta without id", func(t *testing.T, dir, slug string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, slug+".meta.json"), []byte(`{"title":"x"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			plainDir, plainSlug := m.PageDir("S", nil, "Doc")
			if err := os.MkdirAll(plainDir, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.seed(t, plainDir, plainSlug)
			dir, slug, err := m.ClaimPageDir("S", nil, "Doc", "300")
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if dir == plainDir {
				t.Fatalf("%s: claimed the occupied dir %s", tc.name, plainDir)
			}
			if slug != plainSlug+"-300" {
				t.Errorf("slug = %q, want %q", slug, plainSlug+"-300")
			}
		})
	}
}

// TestClaimPageDirScaffoldDirIsFree: a dir that exists only as ancestor
// scaffolding (no page files inside) is free to claim — nesting children under
// a parent must not force a suffix on the parent page itself.
func TestClaimPageDirScaffoldDirIsFree(t *testing.T) {
	m := New(t.TempDir())
	plainDir, plainSlug := m.PageDir("S", nil, "Parent")
	if err := os.MkdirAll(filepath.Join(plainDir, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, slug, err := m.ClaimPageDir("S", nil, "Parent", "100")
	if err != nil {
		t.Fatal(err)
	}
	if dir != plainDir || slug != plainSlug {
		t.Errorf("scaffold dir not claimed as-is: got (%s,%s), want (%s,%s)", dir, slug, plainDir, plainSlug)
	}
}

// TestClaimPageDirDoubleCollisionFailsLoudly: when both the plain and the
// id-suffixed dir belong to other pages, the claim must refuse (ErrCheckFailed)
// rather than overwrite either.
func TestClaimPageDirDoubleCollisionFailsLoudly(t *testing.T) {
	m := New(t.TempDir())
	a := &domain.Resource{ID: "100", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>a</p>")}
	dirA, slugA, err := m.ClaimPageDir(a.SpaceKey, nil, a.Title, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(dirA, slugA, a, nil); err != nil {
		t.Fatal(err)
	}
	// Occupy the dir the newcomer (id 200) would be diverted to.
	squatter := &domain.Resource{ID: "999", Title: "irrelevant", SpaceKey: "S", Version: 1, Body: []byte("<p>s</p>")}
	if err := m.Write(dirA+"-200", slugA+"-200", squatter, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err = m.ClaimPageDir("S", nil, "Doc", "200")
	if err == nil {
		t.Fatal("expected double collision to fail")
	}
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Errorf("double collision error = %v, want ErrCheckFailed", err)
	}
}

// TestClaimPageDirEmptyIDCollisionRefuses: a collision with no id to
// disambiguate with must refuse rather than guess.
func TestClaimPageDirEmptyIDCollisionRefuses(t *testing.T) {
	m := New(t.TempDir())
	a := &domain.Resource{ID: "100", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>a</p>")}
	dirA, slugA, err := m.ClaimPageDir(a.SpaceKey, nil, a.Title, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(dirA, slugA, a, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err = m.ClaimPageDir("S", nil, "Doc", "")
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Errorf("empty-id collision error = %v, want ErrCheckFailed", err)
	}
}
