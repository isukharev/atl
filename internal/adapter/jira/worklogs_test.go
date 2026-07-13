package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		name string
		body string
	}{
		{"missing total", `{"startAt":0,"worklogs":[]}`},
		{"wrong offset", `{"startAt":1,"total":1,"worklogs":[]}`},
		{"empty incomplete", `{"startAt":0,"total":1,"worklogs":[]}`},
		{"past total", `{"startAt":0,"total":0,"worklogs":[{"id":"1"}]}`},
		{"missing identity", `{"startAt":0,"total":1,"worklogs":[{"id":""}]}`},
		{"duplicate identity", `{"startAt":0,"total":2,"worklogs":[{"id":"1"},{"id":"1"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			_, err := newTestJira(server).ListIssueWorklogs(context.Background(), "PROJ-1")
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want ErrCheckFailed", err)
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
