package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueWatchersReadAndWriteContracts(t *testing.T) {
	var postedUsername, removedUsername string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/rest/api/2/issue/PROJ-1/watchers" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"watchCount":2,"isWatching":true,"watchers":[{"name":"alice","key":"user-1","displayName":"Alice","active":true},{"name":"bob","key":"user-2","displayName":"Bob","active":false}]}`)
		case http.MethodPost:
			if err := json.NewDecoder(request.Body).Decode(&postedUsername); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			removedUsername = request.URL.Query().Get("username")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	t.Cleanup(server.Close)
	adapter := newTestJira(server)
	watchers, err := adapter.ListIssueWatchers(context.Background(), "PROJ-1")
	if err != nil || !watchers.Complete || watchers.Truncated || watchers.WatchCount != 2 || len(watchers.Watchers) != 2 || watchers.Watchers[1].Name != "bob" {
		t.Fatalf("watchers=%+v err=%v", watchers, err)
	}
	if err := adapter.AddIssueWatcher(context.Background(), "PROJ-1", "carol"); err != nil {
		t.Fatal(err)
	}
	if postedUsername != "carol" {
		t.Fatalf("POST body username=%q", postedUsername)
	}
	if err := adapter.RemoveIssueWatcher(context.Background(), "PROJ-1", "name/with space"); err != nil {
		t.Fatal(err)
	}
	if removedUsername != "name/with space" {
		t.Fatalf("DELETE username=%q", removedUsername)
	}
}

func TestIssueWatchersIncompleteWhenIdentitiesAreHidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"watchCount":2,"isWatching":false,"watchers":[{"name":"visible"}]}`)
	}))
	t.Cleanup(server.Close)
	result, err := newTestJira(server).ListIssueWatchers(context.Background(), "PROJ-1")
	if err != nil || result.Complete || !result.Truncated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestIssueWatcherWritesAreNeverRetried(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(strings.ToLower(method), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			t.Cleanup(server.Close)
			adapter := newTestJira(server)
			if method == http.MethodPost {
				_ = adapter.AddIssueWatcher(context.Background(), "PROJ-1", "alice")
			} else {
				_ = adapter.RemoveIssueWatcher(context.Background(), "PROJ-1", "alice")
			}
			if calls != 1 {
				t.Fatalf("ambiguous %s was retried %d times", method, calls)
			}
		})
	}
}
