package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func relocationPage(title string, ancestors ...string) *domain.Resource {
	return &domain.Resource{
		ID: "100", Type: "page", Title: title, SpaceKey: "S", Version: 1,
		Body: []byte("<p>body</p>"), BodyPresent: true, Ancestors: ancestors,
	}
}

func TestPageRelocationRetiresOnlyOwnedPrimaryArtifacts(t *testing.T) {
	m := New(t.TempDir())
	old := relocationPage("Old title")
	oldDir, oldSlug, _ := m.ClaimPageDir(old.SpaceKey, old.Ancestors, old.Title, old.ID)
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	oldMD, err := os.ReadFile(filepath.Join(oldDir, oldSlug+".md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(oldDir, "child", "child.csf"),
		filepath.Join(oldDir, "notes.txt"),
		filepath.Join(oldDir, oldSlug+".assets", "image.png"),
		filepath.Join(oldDir, oldSlug+".comments.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	updated := relocationPage("New title")
	updated.Version = 2
	newDir, newSlug, _ := m.ClaimPageDir(updated.SpaceKey, updated.Ancestors, updated.Title, updated.ID)
	newRel, _ := filepath.Rel(m.Root, filepath.Join(newDir, newSlug+".csf"))
	plan, err := m.PlanPageRelocation(updated.ID, newRel, oldMD)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(newDir, newSlug, updated, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RetirePageRelocation(plan); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".csf", ".md", ".meta.json"} {
		if _, err := os.Stat(filepath.Join(oldDir, oldSlug+suffix)); !os.IsNotExist(err) {
			t.Fatalf("old primary artifact %s still exists: %v", suffix, err)
		}
	}
	for _, path := range []string{
		filepath.Join(oldDir, "child", "child.csf"),
		filepath.Join(oldDir, "notes.txt"),
		filepath.Join(oldDir, oldSlug+".assets", "image.png"),
		filepath.Join(oldDir, oldSlug+".comments.json"),
	} {
		if got, err := os.ReadFile(path); err != nil || string(got) != "keep" {
			t.Fatalf("preserved path %s = %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(newDir, newSlug+".csf")); err != nil {
		t.Fatalf("new canonical page missing: %v", err)
	}
	otherDir, otherSlug, err := m.ClaimPageDir("S", nil, old.Title, "200")
	if err != nil {
		t.Fatal(err)
	}
	if otherDir == oldDir || otherSlug != oldSlug+"-200" {
		t.Fatalf("retained auxiliaries were offered to another page: dir=%s slug=%s", otherDir, otherSlug)
	}
}

func TestPageRelocationFailsClosedOnLocalViewEditAndCollision(t *testing.T) {
	m := New(t.TempDir())
	page := relocationPage("Old")
	oldDir, oldSlug, _ := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err := m.Write(oldDir, oldSlug, page, nil); err != nil {
		t.Fatal(err)
	}
	oldMDPath := filepath.Join(oldDir, oldSlug+".md")
	pristine, _ := os.ReadFile(oldMDPath)
	newDir, newSlug := m.PageDir("S", nil, "New")
	newRel, _ := filepath.Rel(m.Root, filepath.Join(newDir, newSlug+".csf"))
	if err := os.WriteFile(oldMDPath, append(pristine, []byte("local edit")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PlanPageRelocation(page.ID, newRel, pristine); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("local view edit was accepted: %v", err)
	}
	if err := os.WriteFile(oldMDPath, pristine, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, newSlug+".csf"), []byte("collision"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PlanPageRelocation(page.ID, newRel, pristine); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("target collision was accepted: %v", err)
	}
}

func TestPageRelocationRequiresDurableValidReplacementBeforeRetirement(t *testing.T) {
	m := New(t.TempDir())
	old := relocationPage("Old")
	oldDir, oldSlug, _ := m.ClaimPageDir(old.SpaceKey, nil, old.Title, old.ID)
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	oldCSF := filepath.Join(oldDir, oldSlug+".csf")
	pristine, _ := os.ReadFile(filepath.Join(oldDir, oldSlug+".md"))
	updated := relocationPage("New")
	updated.Version = 2
	newDir, newSlug := m.PageDir(updated.SpaceKey, nil, updated.Title)
	newCSF := filepath.Join(newDir, newSlug+".csf")
	newRel, _ := filepath.Rel(m.Root, newCSF)
	plan, err := m.PlanPageRelocation(old.ID, newRel, pristine)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RetirePageRelocation(plan); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("retirement before publish was accepted: %v", err)
	}
	if _, err := os.Stat(oldCSF); err != nil {
		t.Fatalf("pre-publish refusal removed old page: %v", err)
	}
	if err := m.Write(newDir, newSlug, updated, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(newCSF); err != nil {
		t.Fatal(err)
	}
	if err := m.RetirePageRelocation(plan); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("retirement with missing replacement was accepted: %v", err)
	}
	if _, err := os.Stat(oldCSF); err != nil {
		t.Fatalf("invalid-replacement refusal removed old page: %v", err)
	}
}

func TestPageRelocationLateOldEditPreservesAllPrimaryArtifacts(t *testing.T) {
	m := New(t.TempDir())
	old := relocationPage("Old")
	oldDir, oldSlug, _ := m.ClaimPageDir(old.SpaceKey, nil, old.Title, old.ID)
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	oldMD := filepath.Join(oldDir, oldSlug+".md")
	pristine, _ := os.ReadFile(oldMD)
	updated := relocationPage("New")
	updated.Version = 2
	newDir, newSlug := m.PageDir(updated.SpaceKey, nil, updated.Title)
	newRel, _ := filepath.Rel(m.Root, filepath.Join(newDir, newSlug+".csf"))
	plan, err := m.PlanPageRelocation(old.ID, newRel, pristine)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(newDir, newSlug, updated, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldMD, append(pristine, []byte("late edit")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.RetirePageRelocation(plan); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("late edit retirement error = %v", err)
	}
	for _, suffix := range []string{".csf", ".md", ".meta.json"} {
		if _, err := os.Stat(filepath.Join(oldDir, oldSlug+suffix)); err != nil {
			t.Fatalf("late-edit refusal removed %s: %v", suffix, err)
		}
	}
}

func TestPageRelocationRefusesForeignOwnershipMarkerWithoutOverwrite(t *testing.T) {
	m := New(t.TempDir())
	page := relocationPage("Old")
	dir, slug, _ := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dir, slug+".relocated.json")
	original := []byte("user-owned bytes")
	if err := os.WriteFile(markerPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	newDir, newSlug := m.PageDir(page.SpaceKey, nil, "New")
	newRel, _ := filepath.Rel(m.Root, filepath.Join(newDir, newSlug+".csf"))
	md, _ := os.ReadFile(filepath.Join(dir, slug+".md"))
	if _, err := m.PlanPageRelocation(page.ID, newRel, md); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("foreign marker was accepted: %v", err)
	}
	if got, err := os.ReadFile(markerPath); err != nil || string(got) != string(original) {
		t.Fatalf("foreign marker changed: %q, %v", got, err)
	}
}

func TestLoadCSFDoesNotAttachStateFromDifferentPath(t *testing.T) {
	m := New(t.TempDir())
	page := relocationPage("Tracked")
	dir, slug, _ := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(m.Root, "S", "stale", "stale.csf")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, page.Body, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	if err := os.WriteFile(strings.TrimSuffix(other, ".csf")+".meta.json", meta, 0o644); err != nil {
		t.Fatal(err)
	}
	lc, _, err := m.LoadCSF(other)
	if err != nil {
		t.Fatal(err)
	}
	if lc.Synced != nil || !lc.Dirty {
		t.Fatalf("stale path inherited canonical state: %+v", lc)
	}
}
