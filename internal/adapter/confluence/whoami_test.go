package confluence

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
		if r.URL.Path != "/rest/api/user/current" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	name, err := cf.Whoami(context.Background())
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

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.Whoami(context.Background()); !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("got %v, want ErrAuth", err)
	}
}

func TestWhoamiForbiddenMapsToErrForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.Whoami(context.Background()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
}

func TestCurrentConfluenceUserUsesStableKeyWithoutEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/user/current" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"userKey":"stable-key","username":"legacy-name","displayName":"Jane Doe","email":"not-modeled@example.invalid"}`))
	}))
	defer srv.Close()

	identity, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).CurrentConfluenceUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "stable-key" || identity.DisplayName != "Jane Doe" {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestCurrentConfluenceUserFallsBackToUsername(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"username":"legacy-name","displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	identity, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).CurrentConfluenceUser(context.Background())
	if err != nil || identity.ID != "legacy-name" {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
}

func TestCurrentConfluenceUserRejectsOmittedOrMalformedIdentity(t *testing.T) {
	for name, response := range map[string]string{
		"missing id":      `{"displayName":"Jane Doe"}`,
		"blank user key":  `{"userKey":" ","username":"legacy-name","displayName":"Jane Doe"}`,
		"missing display": `{"userKey":"stable-key"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer srv.Close()
			_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).CurrentConfluenceUser(context.Background())
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}
