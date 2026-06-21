package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestVerifyConfluenceReturnsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	name, err := VerifyConfluence(srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("VerifyConfluence: %v", err)
	}
	if name != "Jane Doe" {
		t.Fatalf("got %q, want Jane Doe", name)
	}
}

func TestVerifyJiraReturnsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	name, err := VerifyJira(srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("VerifyJira: %v", err)
	}
	if name != "Jane Doe" {
		t.Fatalf("got %q, want Jane Doe", name)
	}
}

func TestVerifyRejectsInsecureURL(t *testing.T) {
	if _, err := VerifyConfluence("http://confluence.example.com", "tok", "test"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("got %v, want ErrUsage for non-https non-loopback URL", err)
	}
}
