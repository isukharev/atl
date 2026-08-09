package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestIssueWorklogsListPaginatesAndSanitizesAuthors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/rest/api/2/issue/PROJ-1/worklog" || request.URL.Query().Get("maxResults") != "100" {
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
		start, _ := strconv.Atoi(request.URL.Query().Get("startAt"))
		w.Header().Set("Content-Type", "application/json")
		if start == 0 {
			_, _ = io.WriteString(w, `{"startAt":0,"total":2,"worklogs":[{"id":"10","issueId":"1","author":{"name":"alice","key":"u1","displayName":"Alice","active":true,"emailAddress":"private@example.test","avatarUrls":{"48x48":"https://private.example.test/a"}},"comment":"first","started":"2026-07-01T10:00:00.000+0000","timeSpent":"1h","timeSpentSeconds":3600}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"startAt":1,"total":2,"worklogs":[{"id":"11","author":{"name":"bob","displayName":"Bob","active":true},"started":"2026-07-01T11:00:00.000+0000","timeSpentSeconds":1800}]}`)
	}))
	t.Cleanup(server.Close)
	result, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
	if err != nil || !result.Complete || result.Total != 2 || len(result.Worklogs) != 2 || result.Worklogs[1].ID != "11" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"private@example.test", "avatarUrls", "private.example.test"} {
		if string(encoded) == forbidden || containsBytes(encoded, forbidden) {
			t.Fatalf("worklog projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestIssueWorklogsListPreservesValidEmptyCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"startAt":0,"total":0,"worklogs":[]}`)
	}))
	t.Cleanup(server.Close)

	result, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
	if err != nil || result == nil || !result.Complete || result.Total != 0 || result.Worklogs == nil || len(result.Worklogs) != 0 {
		t.Fatalf("result=%+v error=%v, want complete non-nil empty collection", result, err)
	}
}

func containsBytes(data []byte, value string) bool {
	for start := 0; start+len(value) <= len(data); start++ {
		if string(data[start:start+len(value)]) == value {
			return true
		}
	}
	return false
}

func TestIssueWorklogsListFailsClosedOnPaginationAnomalies(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"missing total", `{"startAt":0,"worklogs":[]}`, "omitted total"},
		{"missing startAt", `{"total":0,"worklogs":[]}`, "omitted startAt"},
		{"null startAt", `{"startAt":null,"total":0,"worklogs":[]}`, "omitted startAt"},
		{"missing worklogs", `{"startAt":0,"total":0}`, "omitted or nullified worklogs"},
		{"null worklogs", `{"startAt":0,"total":0,"worklogs":null}`, "omitted or nullified worklogs"},
		{"negative total", `{"startAt":0,"total":-1,"worklogs":[]}`, "invalid pagination"},
		{"wrong offset", `{"startAt":1,"total":1,"worklogs":[]}`, "invalid pagination"},
		{"empty incomplete", `{"startAt":0,"total":1,"worklogs":[]}`, "empty incomplete page"},
		{"past total", `{"startAt":0,"total":0,"worklogs":[{"id":"1"}]}`, "with total 0"},
		{"missing identity", `{"startAt":0,"total":1,"worklogs":[{"id":""}]}`, "missing or duplicate worklog id"},
		{"duplicate identity", `{"startAt":0,"total":2,"worklogs":[{"id":"1"},{"id":"1"}]}`, "missing or duplicate worklog id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			result, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
			if !errors.Is(err, domain.ErrCheckFailed) || result != nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("result=%+v error=%v, want nil ErrCheckFailed containing %q", result, err, test.wantMessage)
			}
		})
	}
}

func TestIssueWorklogsListFailsClosedWhenTotalChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("startAt") == "0" {
			_, _ = io.WriteString(w, `{"startAt":0,"total":2,"worklogs":[{"id":"1"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"startAt":1,"total":1,"worklogs":[]}`)
	}))
	t.Cleanup(server.Close)

	result, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) || result != nil {
		t.Fatalf("result=%+v error=%v, want nil and ErrCheckFailed", result, err)
	}
}

func TestIssueWorklogsListPageGuardBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		wantError bool
	}{
		{name: "incomplete at guard", total: worklogPageGuard + 1, wantError: true},
		{name: "complete at guard", total: worklogPageGuard},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests++
				start := request.URL.Query().Get("startAt")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"startAt":`+start+`,"total":`+strconv.Itoa(test.total)+`,"worklogs":[{"id":"`+start+`"}]}`)
			}))
			t.Cleanup(server.Close)

			result, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
			if test.wantError {
				if !errors.Is(err, domain.ErrCheckFailed) || result != nil {
					t.Fatalf("result=%+v error=%v, want nil and ErrCheckFailed", result, err)
				}
			} else if err != nil || result == nil || !result.Complete || result.Total != test.total || len(result.Worklogs) != test.total {
				t.Fatalf("result=%+v error=%v, want exact complete boundary", result, err)
			}
			if requests != worklogPageGuard {
				t.Fatalf("requests=%d, want %d", requests, worklogPageGuard)
			}
		})
	}
}

func TestAddIssueWorklogPayloadAndNoRetry(t *testing.T) {
	var calls int
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/rest/api/2/issue/PROJ-1/worklog" || request.URL.Query().Get("adjustEstimate") != "leave" {
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"12","author":{"name":"alice","displayName":"Alice","active":true},"comment":"done","started":"2026-07-01T10:00:00.000+0000","timeSpent":"1h 30m","timeSpentSeconds":5400}`)
	}))
	t.Cleanup(server.Close)
	created, err := newTestJira(server).AddIssueWorklog(context.Background(), "PROJ-1", domain.IssueWorklogCreate{
		TimeSpentSeconds: 5400, Comment: "done", Started: "2026-07-01T10:00:00.000+0000",
	})
	if err != nil || created.ID != "12" || calls != 1 {
		t.Fatalf("created=%+v calls=%d err=%v", created, calls, err)
	}
	if payload["timeSpentSeconds"] != float64(5400) || payload["comment"] != "done" || payload["started"] != "2026-07-01T10:00:00.000+0000" {
		t.Fatalf("payload=%v", payload)
	}

	retryCalls := 0
	retryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		retryCalls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(retryServer.Close)
	_, _ = newTestJira(retryServer).AddIssueWorklog(context.Background(), "PROJ-1", domain.IssueWorklogCreate{TimeSpentSeconds: 60})
	if retryCalls != 1 {
		t.Fatalf("ambiguous worklog POST was retried %d times", retryCalls)
	}
}
