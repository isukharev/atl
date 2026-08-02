package mirror

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestStageReconcileArtifactsPreservesExactBytesModeAndLayout(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "SPACE", "page", "page.csf")
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("ours remains unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := []byte{'\x00', '<', 'p', '>', '\r', '\n', 0xff}
	theirs := []byte("<p>remote</p>\n")

	basePath, theirsPath, err := New(root).StageReconcileArtifacts("confluence", native, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if want := ".atl/reconcile/confluence/SPACE/page/page.csf.base"; basePath != want {
		t.Errorf("base path = %q, want %q", basePath, want)
	}
	if want := ".atl/reconcile/confluence/SPACE/page/page.csf.theirs"; theirsPath != want {
		t.Errorf("theirs path = %q, want %q", theirsPath, want)
	}
	for path, want := range map[string][]byte{basePath: base, theirsPath: theirs} {
		full := filepath.Join(root, filepath.FromSlash(path))
		got, readErr := os.ReadFile(full)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes = %x, want %x", path, got, want)
		}
		info, statErr := os.Stat(full)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, err = %v; want 0600", path, info, statErr)
		}
	}
	ours, err := os.ReadFile(native)
	if err != nil || string(ours) != "ours remains unchanged" {
		t.Fatalf("native file changed to %q, err = %v", ours, err)
	}
}

func TestStageReconcileArtifactsIsIdempotentAndRecoversPartialStage(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "ABC-1.wiki")
	if err := os.WriteFile(native, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(root)
	base := []byte("base")
	theirs := []byte("theirs")
	baseTarget := filepath.Join(root, ".atl", "reconcile", "jira", "ABC-1.wiki.base")
	if err := os.MkdirAll(filepath.Dir(baseTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseTarget, base, 0o600); err != nil {
		t.Fatal(err)
	}

	firstBase, firstTheirs, err := m.StageReconcileArtifacts("jira", native, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	secondBase, secondTheirs, err := m.StageReconcileArtifacts("jira", native, base, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if firstBase != secondBase || firstTheirs != secondTheirs {
		t.Fatalf("idempotent paths changed: (%q, %q) then (%q, %q)", firstBase, firstTheirs, secondBase, secondTheirs)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(firstTheirs)))
	if err != nil || !bytes.Equal(got, theirs) {
		t.Fatalf("recovered theirs bytes = %q, err = %v", got, err)
	}
}

func TestStageReconcileArtifactsPreservesConflicts(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "page.csf")
	if err := os.WriteFile(native, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(root)
	base := []byte("base")
	theirs := []byte("theirs")
	if _, _, err := m.StageReconcileArtifacts("confluence", native, base, theirs); err != nil {
		t.Fatal(err)
	}
	theirsTarget := filepath.Join(root, ".atl", "reconcile", "confluence", "page.csf.theirs")
	conflict := []byte("user-preserved conflict")
	if err := os.WriteFile(theirsTarget, conflict, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := m.StageReconcileArtifacts("confluence", native, base, theirs); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("conflict error = %v, want ErrCheckFailed", err)
	}
	got, err := os.ReadFile(theirsTarget)
	if err != nil || !bytes.Equal(got, conflict) {
		t.Fatalf("conflicting artifact changed to %q, err = %v", got, err)
	}
	baseTarget := filepath.Join(root, ".atl", "reconcile", "confluence", "page.csf.base")
	got, err = os.ReadFile(baseTarget)
	if err != nil || !bytes.Equal(got, base) {
		t.Fatalf("matching base artifact changed to %q, err = %v", got, err)
	}
}

func TestStageReconcileArtifactsRejectsPermissiveExistingArtifact(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "page.csf")
	if err := os.WriteFile(native, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, ".atl", "reconcile", "confluence", "page.csf.base")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(root).StageReconcileArtifacts("confluence", native, []byte("base"), []byte("theirs")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsafe mode error = %v, want ErrCheckFailed", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("existing artifact mode changed: info=%v err=%v", info, err)
	}
}

func TestStageReconcileArtifactsValidatesServiceNativePathAndSuffix(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	csf := filepath.Join(root, "page.csf")
	if err := os.WriteFile(csf, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.csf")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "dir.csf")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		service string
		path    string
	}{
		{name: "unknown service", service: "../jira", path: csf},
		{name: "service extension mismatch", service: "jira", path: csf},
		{name: "invalid extension", service: "confluence", path: filepath.Join(root, "page.csf.bak")},
		{name: "outside root", service: "confluence", path: outside},
		{name: "non-regular native", service: "confluence", path: directory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := m.StageReconcileArtifacts(tc.service, tc.path, nil, nil); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
	target := filepath.Join(root, ".atl", "reconcile", "confluence", "page.csf.base")
	if err := stageReconcileArtifact(root, target, ".csf", ".wrong", nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid suffix error = %v, want ErrCheckFailed", err)
	}
}

func TestStageReconcileArtifactsRefusesSymlinkAttacks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}

	t.Run("native", func(t *testing.T) {
		root := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim.csf")
		if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
			t.Fatal(err)
		}
		native := filepath.Join(root, "page.csf")
		if err := os.Symlink(victim, native); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, _, err := New(root).StageReconcileArtifacts("confluence", native, []byte("base"), []byte("theirs")); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("symlink native error = %v, want ErrCheckFailed", err)
		}
		if _, err := os.Stat(filepath.Join(root, ".atl")); !os.IsNotExist(err) {
			t.Fatalf("staging state created after native symlink refusal: %v", err)
		}
	})

	t.Run("artifact directory", func(t *testing.T) {
		root := t.TempDir()
		native := filepath.Join(root, "page.csf")
		if err := os.WriteFile(native, []byte("ours"), 0o644); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".atl", "reconcile")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, _, err := New(root).StageReconcileArtifacts("confluence", native, []byte("secret base"), []byte("secret theirs")); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("symlink directory error = %v, want ErrCheckFailed", err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("artifact write escaped through symlink: %v", entries)
		}
	})

	t.Run("existing artifact", func(t *testing.T) {
		root := t.TempDir()
		native := filepath.Join(root, "ABC-1.wiki")
		if err := os.WriteFile(native, []byte("ours"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, ".atl", "reconcile", "jira")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(dir, "ABC-1.wiki.base")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, _, err := New(root).StageReconcileArtifacts("jira", native, []byte("base"), []byte("theirs")); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("symlink artifact error = %v, want ErrCheckFailed", err)
		}
		got, err := os.ReadFile(victim)
		if err != nil || string(got) != "untouched" {
			t.Fatalf("artifact symlink victim changed to %q, err = %v", got, err)
		}
	})
}

func TestStageReconcileArtifactsAreIgnoredByNativeListings(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	csf := filepath.Join(root, "page.csf")
	if err := os.WriteFile(csf, []byte("ours csf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "page.meta.json"), []byte(`{"id":"page"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wiki := filepath.Join(root, "ABC-1.wiki")
	if err := os.WriteFile(wiki, []byte("ours wiki"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.StageReconcileArtifacts("confluence", csf, []byte("base csf"), []byte("theirs csf")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.StageReconcileArtifacts("jira", wiki, []byte("base wiki"), []byte("theirs wiki")); err != nil {
		t.Fatal(err)
	}

	csfs, err := m.ListCSF()
	if err != nil || len(csfs) != 1 || csfs[0].Path != csf {
		t.Fatalf("ListCSF = %+v, err = %v; want only %s", csfs, err, csf)
	}
	wikis, err := m.ListWiki()
	if err != nil || len(wikis) != 1 || wikis[0].Path != wiki {
		t.Fatalf("ListWiki = %+v, err = %v; want only %s", wikis, err, wiki)
	}
}
