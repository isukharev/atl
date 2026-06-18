package auth

import (
	"os"
	"testing"
)

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
	t.Setenv("TEST_CONFLUENCE_PAT", "fromtest")
	tok, err := Token(Confluence)
	if err != nil || tok != "fromtest" {
		t.Fatalf("token=%q err=%v", tok, err)
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
