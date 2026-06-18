package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestClassifyToSentinels(t *testing.T) {
	cases := map[int]error{
		401: domain.ErrAuth,
		403: domain.ErrForbidden,
		404: domain.ErrNotFound,
		409: domain.ErrVersionConflict,
		400: nil,
		500: nil,
	}
	for status, want := range cases {
		if got := classify(status); got != want {
			t.Errorf("classify(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestAPIErrorUnwraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"gone"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	data, err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("body = %s", data)
	}
	if hits < 3 {
		t.Errorf("expected >=3 attempts, got %d", hits)
	}
}

func TestNo4xxRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, _ = c.Do(context.Background(), "GET", "/x", nil, nil)
	if hits != 1 {
		t.Errorf("4xx should not retry; attempts = %d", hits)
	}
}

func TestNoTokenLeakToForeignHost(t *testing.T) {
	// A second host (simulating a server-supplied absolute attachment URL) must
	// NOT receive the Authorization header.
	foreignAuth := make(chan string, 1)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth <- r.Header.Get("Authorization")
		w.Write([]byte("data"))
	}))
	defer foreign.Close()
	// Base is a different host.
	c := New("http://configured.invalid", "secret-pat", "test")
	_, _ = c.Do(context.Background(), "GET", foreign.URL+"/dl", nil, nil)
	if h := <-foreignAuth; h != "" {
		t.Fatalf("PAT leaked to foreign host: %q", h)
	}
}

func TestBearerHeaderSent(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-pat", "test")
	_, _ = c.Do(context.Background(), "GET", "/x", nil, nil)
	if h := <-got; h != "Bearer secret-pat" {
		t.Errorf("auth header = %q", h)
	}
}
