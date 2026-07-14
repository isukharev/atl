package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestEnvironmentInspectIsBoundedGETOnlyAndPrivacySafe(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/serverInfo":
			_, _ = w.Write([]byte(`{"serverTime":"2026-07-14T12:00:00.000+0000","serverTitle":"Private title"}`))
		case "/rest/api/2/myself":
			_, _ = w.Write([]byte(`{"timeZone":"Europe/Moscow","displayName":"Private Person","emailAddress":"private@example.com"}`))
		case "/rest/api/user/current":
			_, _ = w.Write([]byte(`{"timeZone":"Europe/Berlin","displayName":"Private Person","username":"private-user"}`))
		default:
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{
		"ATL_JIRA_URL": srv.URL, "ATL_JIRA_PAT": "jira-secret",
		"ATL_CONFLUENCE_URL": srv.URL, "ATL_CONFLUENCE_PAT": "conf-secret",
		"ATL_READ_ONLY": "1",
	}
	out, code := runCLI(t, env, "environment", "inspect")
	if code != exitOK {
		t.Fatalf("environment inspect exit=%d output=%s", code, out)
	}
	var got app.EnvironmentInspectResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Jira.ServerUTCOffset.Value != "+00:00" || got.Jira.JQLTimeZone.Evidence != "assumed" || got.Confluence.CQLTimeZone.Evidence != "unknown" {
		t.Fatalf("result=%+v", got)
	}
	mu.Lock()
	want := []string{"GET /rest/api/2/serverInfo", "GET /rest/api/2/myself", "GET /rest/api/user/current"}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		mu.Unlock()
		t.Fatalf("requests=%v want=%v", paths, want)
	}
	mu.Unlock()
	for _, secret := range []string{srv.URL, "Private title", "Private Person", "private@example.com", "private-user", "jira-secret", "conf-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("output leaked %q: %s", secret, out)
		}
	}
	assertGolden(t, "environment_inspect.json", []byte(out))

	textOut, code := runCLI(t, env, "environment", "inspect", "-o", "text")
	if code != exitOK || !strings.Contains(textOut, "hidden_calibration_requests: false") || strings.Contains(textOut, "Private") {
		t.Fatalf("text exit=%d output=%s", code, textOut)
	}
}

func TestEnvironmentInspectUnconfiguredIsExplicitAndOffline(t *testing.T) {
	out, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1"}, "environment", "inspect")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var got app.EnvironmentInspectResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Jira.Configured || got.Jira.Status != "not_configured" || got.Confluence.Configured || got.Confluence.Status != "not_configured" || !got.Complete {
		t.Fatalf("result=%+v", got)
	}
}

func TestEnvironmentInspectMissingCredentialsDoesNotContactBackend(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	out, code := runCLI(t, map[string]string{
		"ATL_JIRA_URL": srv.URL, "ATL_CONFLUENCE_URL": srv.URL, "ATL_READ_ONLY": "1",
	}, "environment", "inspect")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var got app.EnvironmentInspectResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || got.Complete || got.Jira.Status != "credentials_missing" || got.Confluence.Status != "credentials_missing" {
		t.Fatalf("requests=%d result=%+v", requests, got)
	}
}
