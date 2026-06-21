package config

import (
	"strings"
	"testing"
)

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
