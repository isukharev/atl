package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestWhoamiReturnsDisplayName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe","name":"jdoe"}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	name, err := j.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if name != "Jane Doe" {
		t.Fatalf("got %q, want Jane Doe", name)
	}
}

func TestWhoamiUnauthorizedMapsToErrAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.Whoami(context.Background()); !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("got %v, want ErrAuth", err)
	}
}

func TestWhoamiForbiddenMapsToErrForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.Whoami(context.Background()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
}
