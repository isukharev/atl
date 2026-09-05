package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestReadRedirectLimitStopsWithoutRetry(t *testing.T) {
	for _, mode := range []string{"buffered", "stream", "resolve"} {
		for _, cycle := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cycle=%v", mode, cycle), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					next, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
					if cycle {
						next = -1
					}
					http.Redirect(w, r, fmt.Sprintf("/%d", next+1), http.StatusFound)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				client := New(server.URL, "synthetic", "test")
				var err error
				switch mode {
				case "buffered":
					_, err = client.Do(ctx, http.MethodGet, "/0", nil, nil)
				case "resolve":
					_, err = client.ResolveGET(ctx, "/0")
				case "stream":
					var body io.ReadCloser
					body, err = client.GetStream(ctx, "/0")
					if body != nil {
						_ = body.Close()
					}
				}
				if !errors.Is(err, errRedirectLimit) || calls.Load() != maxRedirects {
					t.Fatalf("err=%v attempts=%d, want redirect refusal after %d requests without retry", err, calls.Load(), maxRedirects)
				}
			})
		}
	}
}

func TestDownloadErrorBodyIdleLimitReleasesScheduler(t *testing.T) {
	previous := downloadIdleTimeout
	downloadIdleTimeout = 20 * time.Millisecond
	defer func() { downloadIdleTimeout = previous }()
	for _, status := range []int{404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthy" {
					_, _ = io.WriteString(w, "ok")
					return
				}
				w.WriteHeader(status)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			scheduler, err := NewScheduler(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			client := NewWithScheduler(server.URL, "synthetic", "test", scheduler)
			budget, err := domain.NewReadBudget(2, 1024)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(domain.WithReadBudget(t.Context(), budget), 2*time.Second)
			defer cancel()
			_, err = client.GetStream(ctx, "/stalled")
			if err == nil || !strings.Contains(err.Error(), "download stalled") || ctx.Err() != nil {
				t.Fatalf("expected idle watchdog, got err=%v ctx=%v", err, ctx.Err())
			}
			body, err := client.GetStream(ctx, "/healthy")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || string(data) != "ok" {
				t.Fatalf("healthy download=%q err=%v", data, err)
			}
		})
	}
}

func TestDownloadRedirectBodyIdleLimitReleasesScheduler(t *testing.T) {
	previous := downloadIdleTimeout
	downloadIdleTimeout = 20 * time.Millisecond
	defer func() { downloadIdleTimeout = previous }()
	for _, stallAt := range []int{1, maxRedirects} {
		t.Run(strconv.Itoa(stallAt), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthy" {
					_, _ = io.WriteString(w, "ok")
					return
				}
				n := calls.Add(1)
				if int(n) != stallAt {
					http.Redirect(w, r, "/redirect", http.StatusFound)
					return
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(http.StatusFound)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			scheduler, err := NewScheduler(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			client := NewWithScheduler(server.URL, "synthetic", "test", scheduler)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err = client.GetStream(ctx, "/redirect")
			if err == nil || ctx.Err() != nil || int(calls.Load()) != stallAt {
				t.Fatalf("err=%v parent=%v calls=%d", err, ctx.Err(), calls.Load())
			}
			body, err := client.GetStream(ctx, "/healthy")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || string(data) != "ok" {
				t.Fatalf("healthy data=%q err=%v", data, err)
			}
		})
	}
}
