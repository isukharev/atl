package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// A credentials.json managed as a symlink (e.g. into a dotfiles repo) must still
// be readable: the previous hard refusal broke this legitimate setup for no real
// security gain, since the write path replaces the file by rename.
func TestLoadStoreReadsThroughSymlink(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", cfg)
	real := filepath.Join(t.TempDir(), "real-creds.json")
	if err := os.WriteFile(real, []byte(`{"jira":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(cfg, "credentials.json")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	m, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore through symlink: %v", err)
	}
	if m["jira"] != "tok" {
		t.Errorf("store[jira] = %q, want tok", m["jira"])
	}
}

// A pre-existing world-readable credentials file must be replaced with a 0600
// inode on the next write, not left at its looser mode.
func TestLoginReplacesLooserPerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	os.Unsetenv("ATL_JIRA_PAT")
	os.Unsetenv("JIRA_PAT")
	os.Unsetenv("TEST_JIRA_PAT")
	if err := os.WriteFile(credPath(), []byte(`{"jira":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Login(Jira, "new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(credPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm after relogin = %o, want 600", perm)
	}
}

func TestTokenEnvPrecedence(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_CONFLUENCE_PAT", "primary")
	t.Setenv("TEST_CONFLUENCE_PAT", "fallback")
	tok, err := Token(Confluence)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "primary" {
		t.Errorf("token = %q, want primary (ATL_ wins)", tok)
	}
}

func TestTokenFallbackEnv(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	os.Unsetenv("ATL_CONFLUENCE_PAT")
	os.Unsetenv("CONFLUENCE_PAT")
	// TEST_*_PAT is an integration-only fallback, gated behind ATL_INTEGRATION.
	t.Setenv("ATL_INTEGRATION", "1")
	t.Setenv("TEST_CONFLUENCE_PAT", "fromtest")
	tok, err := Token(Confluence)
	if err != nil || tok != "fromtest" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
}

func TestTestPATIgnoredWithoutIntegration(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	os.Unsetenv("ATL_CONFLUENCE_PAT")
	os.Unsetenv("CONFLUENCE_PAT")
	os.Unsetenv("ATL_INTEGRATION")
	t.Setenv("TEST_CONFLUENCE_PAT", "fromtest")
	if _, err := Token(Confluence); err == nil {
		t.Fatal("TEST_CONFLUENCE_PAT must not be used outside ATL_INTEGRATION")
	}
}

func TestLoginStoresMode0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	os.Unsetenv("ATL_JIRA_PAT")
	os.Unsetenv("JIRA_PAT")
	os.Unsetenv("TEST_JIRA_PAT")
	if err := Login(Jira, "stored-pat"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(credPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials perm = %o, want 600", perm)
	}
	tok, err := Token(Jira)
	if err != nil || tok != "stored-pat" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	if src := Source(Jira); src == "" {
		t.Error("Source should report the keychain file")
	}
	if err := Logout(Jira); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(Jira); err == nil {
		t.Error("expected error after logout")
	}
}

func TestSourceNeverReturnsToken(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_CONFLUENCE_PAT", "supersecret")
	src := Source(Confluence)
	if src == "supersecret" {
		t.Fatal("Source leaked the token value")
	}
	if src != "env:ATL_CONFLUENCE_PAT" {
		t.Errorf("src = %q", src)
	}
}
