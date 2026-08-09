package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestEditConfluenceFileWithoutCobra(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.csf")
	before := "<p>alpha beta</p>"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EditConfluenceFile(ConfluenceEditOptions{
		File: path,
		Old:  "alpha",
		New:  "gamma",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.File != path || result.Pass != "exact" || result.Count != 1 || result.DryRun {
		t.Fatalf("result = %+v", result)
	}
	if result.CSFOK == nil || !*result.CSFOK || len(result.Problems) != 0 {
		t.Fatalf("CSF result = %+v", result)
	}
	if len(result.Offsets) != 1 || result.Offsets[0].Start != 3 || result.Offsets[0].End != 8 {
		t.Fatalf("offsets = %+v", result.Offsets)
	}
	if result.RegionBefore != `"<p>alpha beta</p>"` || result.RegionAfter != `"<p>gamma beta</p>"` {
		t.Fatalf("regions = before %q, after %q", result.RegionBefore, result.RegionAfter)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<p>gamma beta</p>" {
		t.Fatalf("file = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEditConfluenceFileDryRunPreservesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.csf")
	before := []byte("<p>alpha beta</p>")
	if err := os.WriteFile(path, before, 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := EditConfluenceFile(ConfluenceEditOptions{
		File:   path,
		Old:    "alpha",
		New:    "gamma",
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Count != 1 {
		t.Fatalf("result = %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(before) {
		t.Fatalf("dry run changed file: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestEditConfluenceFileMirrorLockPrecedesReadAndReplacement(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "page.csf")
	if err := os.WriteFile(path, []byte("<p>old</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireConfluenceMutation(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	_, err = EditConfluenceFile(ConfluenceEditOptions{
		File: path,
		Old:  "not present",
		New:  "new",
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want check-failed lock contention", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("replacement ran before lock: %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "<p>old</p>" {
		t.Fatalf("locked file = %q, err = %v", got, readErr)
	}
}

func TestEditConfluenceFileExternalAliasJoinsMirrorLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "page.csf")
	if err := os.WriteFile(target, []byte("<p>old</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.csf")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireConfluenceMutation(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	_, err = EditConfluenceFile(ConfluenceEditOptions{File: alias, Old: "old", New: "new"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want mirror lock contention", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "<p>old</p>" {
		t.Fatalf("locked target = %q, err = %v", got, readErr)
	}
}

func TestEditConfluenceFileRefusesVisibleMirrorEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.csf")
	if err := os.WriteFile(target, []byte("<p>old</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.csf")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	_, err := EditConfluenceFile(ConfluenceEditOptions{File: alias, Old: "old", New: "new"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want visible-mirror containment failure", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "<p>old</p>" {
		t.Fatalf("external target = %q, err = %v", got, readErr)
	}
}

func TestEditConfluenceFileInvalidCSFWritesWithProblems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.csf")
	if err := os.WriteFile(path, []byte("<p>keep <strong>bold</strong></p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := EditConfluenceFile(ConfluenceEditOptions{
		File: path,
		Old:  "</strong>",
		New:  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CSFOK == nil || *result.CSFOK || len(result.Problems) == 0 {
		t.Fatalf("validation result = %+v", result)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "<p>keep <strong>bold</p>" {
		t.Fatalf("invalid staged bytes = %q, err = %v", got, readErr)
	}
}
