package corpus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRootIdentityDigestAndIndependentRoots(t *testing.T) {
	parent := privateDirectory(t, filepath.Join(t.TempDir(), "parent"))
	first := privateDirectory(t, filepath.Join(parent, "first"))
	second := privateDirectory(t, filepath.Join(parent, "second"))
	if err := ValidateIndependentRoots(first, second); err != nil {
		t.Fatal(err)
	}
	digest, err := RootIdentityDigest(first)
	if err != nil || !isLowerSHA256(digest) {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	alias := filepath.Join(filepath.Dir(parent), "first-alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	aliasDigest, err := RootIdentityDigest(alias)
	if err != nil || aliasDigest != digest {
		t.Fatalf("alias digest=%q want=%q err=%v", aliasDigest, digest, err)
	}
	if err := ValidateIndependentRoots(first, alias); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("alias error=%v", err)
	}
	child := privateDirectory(t, filepath.Join(first, "child"))
	if err := ValidateIndependentRoots(first, child); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nested error=%v", err)
	}
}

func TestRootIdentityRejectsSpecialAndUnsafeRoots(t *testing.T) {
	base := t.TempDir()
	unsafe := privateDirectory(t, filepath.Join(base, "unsafe"))
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "missing")
	for name, path := range map[string]string{"unsafe": unsafe, "regular": regular, "missing": missing} {
		t.Run(name, func(t *testing.T) {
			if digest, err := RootIdentityDigest(path); digest != "" || !errors.Is(err, ErrIntegrity) {
				t.Fatalf("digest=%q err=%v", digest, err)
			}
		})
	}
}

func TestPinnedRootIdentityRejectsRetargetedSpelling(t *testing.T) {
	base := t.TempDir()
	first := privateDirectory(t, filepath.Join(base, "first"))
	second := privateDirectory(t, filepath.Join(base, "second"))
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	identity, err := qualifyRootIdentity(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = identity.close() }()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	if err := identity.revalidate(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("retarget error=%v", err)
	}
}

func privateDirectory(t testing.TB, path string) string {
	t.Helper()
	if err := os.Mkdir(path, privateDirMode); err != nil {
		t.Fatal(err)
	}
	return path
}
