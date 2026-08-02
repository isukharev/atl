package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestSaveNativeStashPreservesExactBytesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	body := []byte{'\x00', '<', 'p', '>', '\r', '\n', 0xff, '<', '/', 'p', '>'}

	got, err := m.SaveNativeStash("confluence", "12345", ".csf", body)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(".atl", "stash", "confluence", "12345", Hash(body)+".csf"))
	if got != want {
		t.Fatalf("stash path = %q, want %q", got, want)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(got)))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(body) {
		t.Fatalf("stash bytes = %x, want %x", stored, body)
	}
	if again, err := m.SaveNativeStash("confluence", "12345", ".csf", body); err != nil || again != got {
		t.Fatalf("idempotent save path = %q, err = %v; want %q", again, err, got)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(got)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stash mode = %v, err = %v; want 0600", info, err)
	}
	info, err = os.Stat(filepath.Dir(filepath.Join(root, filepath.FromSlash(got))))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("stash directory mode = %v, err = %v; want 0700", info, err)
	}
}

func TestSaveNativeStashUsesDistinctContentPaths(t *testing.T) {
	m := New(t.TempDir())
	first, err := m.SaveNativeStash("jira", "ABC-1", ".wiki", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.SaveNativeStash("jira", "ABC-1", ".wiki", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("distinct bodies used the same stash path %q", first)
	}
}

func TestSaveNativeStashSanitizesBackendIdentifiers(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	body := []byte("native")
	got, err := m.SaveNativeStash("../confluence\\prod", "../../page/42", ".csf", body)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(".atl", "stash", "_..-confluence-prod", "_..-..-page-42", Hash(body)+".csf"))
	if got != want {
		t.Fatalf("sanitized stash path = %q, want %q", got, want)
	}
	if strings.Contains(got, "/../") || strings.HasPrefix(got, "../") {
		t.Fatalf("stash escaped through traversal path %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(got))); err != nil {
		t.Fatal(err)
	}
}

func TestSaveNativeStashRejectsInvalidExtension(t *testing.T) {
	m := New(t.TempDir())
	for _, ext := range []string{"", "csf", ".md", ".csf.bak", "../x.csf"} {
		if _, err := m.SaveNativeStash("confluence", "1", ext, nil); !errors.Is(err, domain.ErrCheckFailed) {
			t.Errorf("extension %q error = %v, want ErrCheckFailed", ext, err)
		}
	}
}

func TestSaveNativeStashRejectsMismatchedExistingContent(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	body := []byte("canonical")
	target := filepath.Join(root, ".atl", "stash", "jira", "ABC-1", Hash(body)+".wiki")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveNativeStash("jira", "ABC-1", ".wiki", body); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("collision error = %v, want ErrCheckFailed", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "different" {
		t.Fatalf("existing stash changed to %q, err = %v", got, err)
	}
}

func TestSaveNativeStashRefusesSymlinkedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, ".atl", "stash")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(root).SaveNativeStash("confluence", "1", ".csf", []byte("secret")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("symlink refusal error = %v, want ErrCheckFailed", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("stash write escaped through symlink: %v", entries)
	}
}

func TestSaveNativeStashRefusesSymlinkedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	body := []byte("secret")
	dir := filepath.Join(root, ".atl", "stash", "jira", "ABC-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.wiki")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, Hash(body)+".wiki")
	if err := os.Symlink(victim, target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(root).SaveNativeStash("jira", "ABC-1", ".wiki", body); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("symlink refusal error = %v, want ErrCheckFailed", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "original" {
		t.Fatalf("symlink victim changed to %q, err = %v", got, err)
	}
}
