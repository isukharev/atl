package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestLoadInvalidJiraListViewsIsConfigErrorButEditable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	body := `{"jira_list_views":{"broken":{"search":["board.column"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, domain.ErrConfig) || !strings.Contains(err.Error(), "jira_list_views") {
		t.Fatalf("strict load error=%v", err)
	}
	cfg, err := LoadForEdit()
	if err != nil || cfg.JiraListViews["broken"].Search[0] != "board.column" {
		t.Fatalf("editable load=%+v err=%v", cfg, err)
	}
}

func TestCheckSecureURL(t *testing.T) {
	t.Setenv("ATL_ALLOW_INSECURE", "")
	ok := []string{
		"https://confluence.example.com",
		"https://jira.example.com/path",
		"http://localhost:8080",
		"http://127.0.0.1:9000",
		"http://[::1]:8090",
	}
	for _, u := range ok {
		if err := CheckSecureURL(u); err != nil {
			t.Errorf("CheckSecureURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"http://confluence.example.com",
		"http://jira.internal",
		"ftp://example.com",
	}
	for _, u := range bad {
		if err := CheckSecureURL(u); err == nil {
			t.Errorf("CheckSecureURL(%q) = nil, want error", u)
		} else if !IsSecureURLError(err) {
			t.Errorf("CheckSecureURL(%q) error is not identifiable for boundary redaction: %v", u, err)
		}
	}
}

func TestCheckSecureURLAllowInsecureOptOut(t *testing.T) {
	t.Setenv("ATL_ALLOW_INSECURE", "1")
	if err := CheckSecureURL("http://confluence.internal"); err != nil {
		t.Errorf("with ATL_ALLOW_INSECURE set, http should be allowed, got %v", err)
	}
}

func TestCheckSecureURLErrorMentionsOverride(t *testing.T) {
	t.Setenv("ATL_ALLOW_INSECURE", "")
	err := CheckSecureURL("http://corp.example.com")
	if err == nil || !strings.Contains(err.Error(), "ATL_ALLOW_INSECURE") {
		t.Fatalf("error should explain the override, got %v", err)
	}
}
