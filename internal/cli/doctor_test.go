package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestDoctorOfflineEmitsQualifiedAggregate(t *testing.T) {
	out, code := runCLI(t, nil, "doctor")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.Mode != "offline" || !got.Complete || got.Healthy {
		t.Fatalf("result=%+v", got)
	}
	if got.Services.Jira.Remote.Status != "not_requested" || got.Services.Confluence.Remote.Status != "not_requested" {
		t.Fatalf("offline remote status: jira=%+v confluence=%+v", got.Services.Jira.Remote, got.Services.Confluence.Remote)
	}
	if strings.Contains(out, os.TempDir()) {
		t.Fatalf("offline result leaked a filesystem path: %s", out)
	}

	text, textCode := runCLI(t, nil, "doctor", "-o", "text")
	for _, fact := range []string{
		"schema_version: 1", "status: fail", "config_file:", "credential_store:",
		"credentials_jira:", "safety:", "jira_remote:", "jira_compatibility:",
		"mirror_jira:", "plugin:", "services.none_configured",
	} {
		if !strings.Contains(text, fact) {
			t.Fatalf("text output missing %q: %s", fact, text)
		}
	}
	if textCode != exitCheckFailed {
		t.Fatalf("text exit=%d output=%s", textCode, text)
	}
}

func TestDoctorRemoteIsExactlyOneMetadataGETPerServiceAndPrivacySafe(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/serverInfo":
			_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"Data Center","serverTitle":"Private Jira","baseUrl":"https://private-jira.example"}`))
		case "/rest/api/server-information":
			_, _ = w.Write([]byte(`{"version":"9.4.1","baseUrl":"https://private-conf.example","buildDate":"private-date"}`))
		default:
			http.Error(w, "private unexpected response", http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{
		"ATL_JIRA_URL": srv.URL, "ATL_JIRA_PAT": "jira-secret",
		"ATL_CONFLUENCE_URL": srv.URL, "ATL_CONFLUENCE_PAT": "conf-secret",
	}

	out, stderr, code := runCLIFull(t, env, "--verbose", "doctor", "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Healthy || got.Mode != "remote" ||
		got.Services.Jira.Remote.Status != "available" ||
		got.Services.Confluence.Remote.Status != "available" ||
		got.Services.Jira.Remote.Version != "9.12.7" ||
		got.Services.Jira.Remote.DeploymentType != "data_center" ||
		got.Services.Confluence.Remote.Version != "9.4.1" {
		t.Fatalf("result=%+v", got)
	}
	mu.Lock()
	gotRequests := strings.Join(requests, "\n")
	mu.Unlock()
	wantRequests := "GET /rest/api/2/serverInfo\nGET /rest/api/server-information"
	if gotRequests != wantRequests {
		t.Fatalf("requests=%q want=%q", gotRequests, wantRequests)
	}
	for _, private := range []string{
		srv.URL, "jira-secret", "conf-secret", "Private Jira",
		"private-jira.example", "private-conf.example", "private-date",
		"/rest/api/2/serverInfo", "/rest/api/server-information",
	} {
		if strings.Contains(out, private) || strings.Contains(stderr, private) {
			t.Fatalf("doctor output/trace leaked %q\nstdout=%s\nstderr=%s", private, out, stderr)
		}
	}
}

func TestDoctorMalformedConfigBlocksRemoteButStillEmits(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"jira_url":"https://private.invalid"`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ATL_CONFIG_DIR": dir,
		"ATL_JIRA_URL":   srv.URL,
		"ATL_JIRA_PAT":   "private-token",
	}
	out, code := runCLI(t, env, "doctor", "--remote")
	if code != exitCheckFailed || requests != 0 {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Config.Status != "invalid" || got.Services.Jira.Remote.Status != "skipped" ||
		got.Services.Jira.Remote.Reason != "configuration_preflight_failed" {
		t.Fatalf("result=%+v", got)
	}
	for _, private := range []string{dir, srv.URL, "private.invalid", "private-token"} {
		if strings.Contains(out, private) {
			t.Fatalf("doctor output leaked %q: %s", private, out)
		}
	}
}

func TestDoctorLocalSiblingFailureDoesNotBlockReadyRemoteService(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/rest/api/2/serverInfo" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"Server"}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"confluence":"stored"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, map[string]string{
		"ATL_CONFIG_DIR": dir,
		"ATL_JIRA_URL":   srv.URL,
		"ATL_JIRA_PAT":   "jira-secret",
	}, "doctor", "--remote")
	if code != exitCheckFailed || requests != 1 {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Services.Jira.Remote.Status != "available" ||
		got.Services.Confluence.Remote.Status != "skipped" ||
		got.Services.Confluence.Remote.Reason != "service_not_ready" {
		t.Fatalf("result=%+v", got)
	}
}

func TestDoctorRemoteFailureDoesNotHideSibling(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/serverInfo":
			http.Error(w, "private auth response", http.StatusUnauthorized)
		case "/rest/api/server-information":
			_, _ = w.Write([]byte(`{"version":"9.4.1"}`))
		}
	}))
	t.Cleanup(srv.Close)
	out, code := runCLI(t, map[string]string{
		"ATL_JIRA_URL": srv.URL, "ATL_JIRA_PAT": "jira-secret",
		"ATL_CONFLUENCE_URL": srv.URL, "ATL_CONFLUENCE_PAT": "conf-secret",
	}, "doctor", "--remote")
	if code != exitCheckFailed || requests != 2 {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Services.Jira.Remote.Status != "authentication_failed" ||
		got.Services.Confluence.Remote.Status != "available" {
		t.Fatalf("result=%+v", got)
	}
	if strings.Contains(out, "private auth response") {
		t.Fatalf("backend error leaked: %s", out)
	}
}
