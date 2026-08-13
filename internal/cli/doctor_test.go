package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
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

func TestCommandTreesKeepVerboseTransportTracingIndependent(t *testing.T) {
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_VERBOSE", "")
	t.Setenv("ATL_CONFLUENCE_URL", "")
	t.Setenv("ATL_CONFLUENCE_PAT", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rest/api/2/serverInfo" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"Data Center"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("ATL_JIRA_URL", server.URL)
	t.Setenv("ATL_JIRA_PAT", "test-pat")

	verboseRoot := newRoot()
	verboseRoot.SetArgs([]string{"--verbose", "doctor", "--remote"})
	var verboseOut, verboseErr strings.Builder
	verboseRoot.SetOut(&verboseOut)
	verboseRoot.SetErr(&verboseErr)

	silentRoot := newRoot()
	silentRoot.SetArgs([]string{"doctor", "--remote"})
	var silentOut, silentErr strings.Builder
	silentRoot.SetOut(&silentOut)
	silentRoot.SetErr(&silentErr)

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	for _, root := range []*cobra.Command{verboseRoot, silentRoot} {
		doctor, _, err := root.Find([]string{"doctor"})
		if err != nil {
			t.Fatal(err)
		}
		original := doctor.RunE
		doctor.RunE = func(cmd *cobra.Command, args []string) error {
			ready <- struct{}{}
			<-release
			return original(cmd, args)
		}
	}
	results := make(chan error, 2)
	go func() { results <- verboseRoot.ExecuteContext(context.Background()) }()
	go func() { results <- silentRoot.ExecuteContext(context.Background()) }()
	<-ready
	<-ready
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("doctor execution: %v", err)
		}
	}

	if !strings.Contains(verboseErr.String(), "<redacted>") {
		t.Fatalf("verbose command tree trace = %q", verboseErr.String())
	}
	if got := silentErr.String(); got != "" {
		t.Fatalf("silent command tree inherited trace = %q", got)
	}
	for name, output := range map[string]string{"verbose": verboseOut.String(), "silent": silentOut.String()} {
		var result app.DoctorResult
		if err := json.Unmarshal([]byte(output), &result); err != nil || result.Services.Jira.Remote.Status != "available" {
			t.Fatalf("%s doctor output = %q, result=%+v, err=%v", name, output, result, err)
		}
	}
}

func TestDoctorRemoteRecognizesLegacyConfluenceReachabilityWithoutClaimingCompatibility(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Body != nil && r.ContentLength > 0 {
			t.Errorf("%s %s sent a request body", r.Method, r.URL.RequestURI())
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/serverInfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"Data Center"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/server-information":
			http.Error(w, "private legacy route detail", http.StatusNotFound)
		case r.Method == http.MethodHead && r.URL.Path == "/rest/api/content":
			w.Header().Set("X-Private-Backend", "private legacy header")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "private unexpected response", http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	out, stderr, code := runCLIFull(t, map[string]string{
		"ATL_JIRA_URL": srv.URL, "ATL_JIRA_PAT": "jira-secret",
		"ATL_CONFLUENCE_URL": srv.URL, "ATL_CONFLUENCE_PAT": "conf-secret",
	}, "--verbose", "doctor", "--remote")
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	confluence := got.Services.Confluence
	if !got.Healthy || confluence.Remote.Status != "available" ||
		confluence.Remote.Product != "confluence" || confluence.Remote.Version != "" ||
		confluence.Compatibility.Status != "unverified" ||
		confluence.Compatibility.Evidence != "metadata_only" ||
		confluence.Compatibility.Reason != "version_unavailable" {
		t.Fatalf("result=%+v", got)
	}
	mu.Lock()
	gotRequests := strings.Join(requests, "\n")
	mu.Unlock()
	wantRequests := "GET /rest/api/2/serverInfo\nGET /rest/api/server-information\nHEAD /rest/api/content"
	if gotRequests != wantRequests {
		t.Fatalf("requests=%q want=%q", gotRequests, wantRequests)
	}
	for _, private := range []string{
		srv.URL, "jira-secret", "conf-secret", "private legacy route detail",
		"private legacy header", "/rest/api/2/serverInfo", "/rest/api/server-information",
		"/rest/api/content",
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

func TestDoctorReportsInvalidCABundleWithoutLeakingPath(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "configured-private-ca.pem")
	configBody := `{"transport":{"jira":{"ca_bundle":` + strconv.Quote(privatePath) + `}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": dir}, "doctor")
	if code != exitCheckFailed || strings.Contains(out, privatePath) || !strings.Contains(out, `"reason": "ca_bundle_invalid"`) {
		t.Fatalf("exit=%d output=%s", code, out)
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

func TestDoctorServiceScopesSiblingChecksAndRemoteProbes(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/rest/api/2/serverInfo" {
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.12.7","deploymentType":"Data Center"}`))
	}))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	privateCAPath := filepath.Join(configDir, "unused-private-ca.pem")
	configBody := `{"transport":{"confluence":{"ca_bundle":` + strconv.Quote(privateCAPath) + `}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, map[string]string{
		"ATL_CONFIG_DIR": configDir,
		"ATL_JIRA_URL":   server.URL, "ATL_JIRA_PAT": "jira-secret",
	}, "doctor", "--service", "jira", "--remote")
	if code != exitOK || requests != 1 {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Service != app.DoctorServiceJira || got.Services.Jira.Remote.Status != "available" ||
		got.Services.Confluence.Status != "not_selected" || got.Services.Confluence.URLStatus != "not_selected" ||
		got.Mirror.Confluence.Status != "not_selected" || !got.Config.Transport.Confluence.Configured ||
		got.Config.Transport.Confluence.Source != "config_file" || got.Config.Transport.Confluence.Status != "not_selected" ||
		got.Config.Transport.Confluence.Reason != "" {
		t.Fatalf("scoped doctor=%+v", got)
	}
	if strings.Contains(out, privateCAPath) || strings.Contains(out, "transport.confluence.ca_bundle") {
		t.Fatalf("unselected sibling affected or leaked into output: %s", out)
	}
}

func TestDoctorServiceKeepsCommonCredentialSafetyChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX owner-only permission contract")
	}
	configDir := t.TempDir()
	credentialsPath := filepath.Join(configDir, "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"confluence":"stored"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The high-risk gate intentionally runs beneath an owner-only umask. Set
	// the unsafe fixture mode explicitly so this safety oracle cannot become a
	// false negative when creation masks group/world bits.
	if err := os.Chmod(credentialsPath, 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, map[string]string{
		"ATL_CONFIG_DIR": configDir,
		"ATL_JIRA_URL":   "http://127.0.0.1:1", "ATL_JIRA_PAT": "jira-secret",
	}, "doctor", "--service", "jira")
	if code != exitCheckFailed {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var got app.DoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, problem := range got.Problems {
		found = found || problem.ID == "credentials.permissions"
	}
	if !found || got.Services.Confluence.Status != "not_selected" {
		t.Fatalf("common credential check or sibling projection missing: %+v", got)
	}
}

func TestDoctorRejectsUnknownServiceBeforeRemoteEffects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	out, code := runCLI(t, map[string]string{
		"ATL_JIRA_URL": server.URL, "ATL_JIRA_PAT": "jira-secret",
	}, "doctor", "--service", "other", "--remote")
	if code != exitUsage || requests != 0 || out != "" {
		t.Fatalf("exit=%d requests=%d output=%q", code, requests, out)
	}
}

func TestDoctorRejectsUnknownServiceBeforePersistentPreRun(t *testing.T) {
	root := newRoot()
	preRuns := 0
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		preRuns++
		return nil
	}
	root.SetArgs([]string{"doctor", "--service", "other"})
	if err := root.ExecuteContext(context.Background()); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want ErrUsage", err)
	}
	if preRuns != 0 {
		t.Fatalf("persistent pre-runs = %d, want 0", preRuns)
	}
}

func TestDoctorRejectsPositionalArgumentBeforePersistentPreRun(t *testing.T) {
	out, code := runCLI(t, nil, "doctor", "extra")
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d output=%q", code, out)
	}

	root := newRoot()
	preRuns := 0
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		preRuns++
		return nil
	}
	root.SetArgs([]string{"doctor", "extra"})
	if err := root.ExecuteContext(context.Background()); !errors.Is(err, domain.ErrUsage) ||
		codeFor(err) != exitUsage || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v code=%d, want positional-argument usage error", err, codeFor(err))
	}
	if preRuns != 0 {
		t.Fatalf("persistent pre-runs = %d, want 0", preRuns)
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
