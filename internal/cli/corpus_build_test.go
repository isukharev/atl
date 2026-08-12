package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

type corpusBuildCLIServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []string
}

func newCorpusBuildCLIServer(t *testing.T) *corpusBuildCLIServer {
	t.Helper()
	fixture := &corpusBuildCLIServer{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, request.Method+" "+request.URL.RequestURI())
		fixture.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/user/current":
			fmt.Fprint(writer, `{"userKey":"private-confluence-principal","displayName":"Private Person"}`)
		case "/rest/api/search":
			fmt.Fprint(writer, `{"results":[{"content":{"id":"200","type":"page","title":"Private page title","space":{"key":"ENG"},"version":{"number":3,"when":"2026-08-12T12:34:56Z"}}}],"size":1,"totalCount":1,"_links":{}}`)
		case "/rest/api/content/200":
			fmt.Fprint(writer, `{"id":"200","type":"page","title":"Private page title","space":{"key":"ENG"},"version":{"number":3,"when":"2026-08-12T12:34:56Z"},"body":{"storage":{"value":"<p>private confluence body</p>"}}}`)
		case "/rest/api/2/myself":
			fmt.Fprint(writer, `{"name":"private-jira-principal","key":"private-jira-key","displayName":"Private Person","active":true}`)
		case "/rest/api/2/search":
			fmt.Fprint(writer, `{"issues":[{"id":"100","key":"ENG-1","fields":{"project":{"key":"ENG"}}}],"startAt":0,"maxResults":100,"total":1}`)
		case "/rest/api/2/issue/100":
			fmt.Fprint(writer, `{"id":"100","key":"ENG-1","fields":{"summary":"Private issue title","description":"private jira body","status":{"name":"Open"},"issuetype":{"name":"Task"},"project":{"key":"ENG"}}}`)
		default:
			http.Error(writer, "private unexpected backend path", http.StatusTeapot)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *corpusBuildCLIServer) environment() map[string]string {
	return map[string]string{
		"ATL_JIRA_URL": fixture.server.URL, "ATL_JIRA_PAT": "private-jira-token",
		"ATL_CONFLUENCE_URL": fixture.server.URL, "ATL_CONFLUENCE_PAT": "private-confluence-token",
	}
}

func (fixture *corpusBuildCLIServer) snapshotRequests() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.requests...)
}

func TestCorpusBuildRequiresExplicitReadOnlyBeforeConfigOrEffects(t *testing.T) {
	root := corpusBuildCLIPrivateRoot(t)
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newCorpusBuildCLIServer(t)
	environment := server.environment()
	environment["ATL_CONFIG_DIR"] = configRoot

	stdout, _, err := executeCLIRaw(t, environment, corpusBuildCLIArgs(root, true)...)
	var closed *app.CorpusBuildError
	if err == nil || !errors.Is(err, domain.ErrUsage) || !errors.As(err, &closed) ||
		closed.Phase != app.CorpusBuildPhaseValidate || closed.Reason != app.CorpusBuildReasonUsage ||
		err.Error() != "corpus build failed: phase=validate reason=usage" || stdout != "" || len(server.snapshotRequests()) != 0 {
		t.Fatalf("stdout=%q err=%v requests=%v", stdout, err, server.snapshotRequests())
	}
	for _, private := range []string{configRoot, server.server.URL, "private-jira-token"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("preflight error leaked %q: %v", private, err)
		}
	}

	invalid := corpusBuildCLIArgs(root, true)
	invalid = append([]string{"--read-only"}, invalid...)
	for index := range invalid {
		if invalid[index] == "--max-requests" {
			invalid[index+1] = "0"
		}
	}
	stdout, _, err = executeCLIRaw(t, environment, invalid...)
	closed = nil
	if err == nil || !errors.Is(err, domain.ErrUsage) || !errors.As(err, &closed) ||
		closed.Phase != app.CorpusBuildPhaseValidate || closed.Reason != app.CorpusBuildReasonUsage ||
		err.Error() != "corpus build failed: phase=validate reason=usage" || stdout != "" || len(server.snapshotRequests()) != 0 {
		t.Fatalf("static validation stdout=%q err=%v requests=%v", stdout, err, server.snapshotRequests())
	}

	environment["ATL_READ_ONLY"] = "1"
	stdout, _, err = executeCLIRaw(t, environment, corpusBuildCLIArgs(root, false)...)
	if err == nil || !errors.Is(err, domain.ErrUsage) || strings.Contains(err.Error(), "requires explicit --read-only") ||
		len(server.snapshotRequests()) != 0 {
		t.Fatalf("environment policy stdout=%q err=%v requests=%v", stdout, err, server.snapshotRequests())
	}
}

func TestCorpusBuildFlagParseUsesClosedEnvelopeBeforeEffects(t *testing.T) {
	server := newCorpusBuildCLIServer(t)
	stdout, _, err := executeCLIRaw(t, server.environment(), "--read-only", "corpus", "build", "--max-requests", "invalid")
	var closed *app.CorpusBuildError
	if err == nil || !errors.Is(err, domain.ErrUsage) || !errors.As(err, &closed) ||
		closed.Phase != app.CorpusBuildPhaseValidate || closed.Reason != app.CorpusBuildReasonUsage ||
		err.Error() != "corpus build failed: phase=validate reason=usage" || stdout != "" || len(server.snapshotRequests()) != 0 {
		t.Fatalf("stdout=%q err=%v requests=%v", stdout, err, server.snapshotRequests())
	}
}

func TestCorpusBuildPositionalArgumentUsesClosedEnvelopeBeforeEffects(t *testing.T) {
	server := newCorpusBuildCLIServer(t)
	stdout, _, err := executeCLIRaw(t, server.environment(), "--read-only", "corpus", "build", "unexpected")
	var closed *app.CorpusBuildError
	if err == nil || !errors.Is(err, domain.ErrUsage) || !errors.As(err, &closed) ||
		closed.Phase != app.CorpusBuildPhaseValidate || closed.Reason != app.CorpusBuildReasonUsage ||
		err.Error() != "corpus build failed: phase=validate reason=usage" || stdout != "" || len(server.snapshotRequests()) != 0 {
		t.Fatalf("stdout=%q err=%v requests=%v", stdout, err, server.snapshotRequests())
	}
}

func TestCorpusBuildCLIUsesGETOnlySharedBudgetAndContentFreeOutput(t *testing.T) {
	root := corpusBuildCLIPrivateRoot(t)
	server := newCorpusBuildCLIServer(t)
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"render":{"jira":{"profile":"full"},"confluence":{"profile":"full","jira_macros":"auto"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := server.environment()
	environment["ATL_CONFIG_DIR"] = configRoot
	args := append([]string{"--read-only", "--verbose"}, corpusBuildCLIArgs(root, true)...)
	stdout, stderr, err := executeCLIRaw(t, environment, args...)
	if err != nil {
		t.Fatalf("error=%v stdout=%s requests=%v", err, stdout, server.snapshotRequests())
	}
	var result app.CorpusBuildResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Projection.Readiness != corpus.ProjectionReady || len(result.Services) != 2 ||
		result.Usage.Attempts != 8 || result.Usage.ResponseBytes <= 0 ||
		len(result.Generation.Services) != 2 {
		t.Fatalf("result=%#v", result)
	}
	requests := server.snapshotRequests()
	if len(requests) != result.Usage.Attempts {
		t.Fatalf("physical requests=%v usage=%#v", requests, result.Usage)
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, http.MethodGet+" ") {
			t.Fatalf("non-read request: %s", request)
		}
		for _, presentationField := range []string{"priority", "fixVersions", "comment", "attachment", "issuelinks"} {
			if strings.Contains(request, presentationField) {
				t.Fatalf("ambient full render policy widened corpus capture: %s", request)
			}
		}
		if strings.Contains(request, "/comment") {
			t.Fatalf("ambient full render policy widened corpus capture: %s", request)
		}
	}
	for _, private := range []string{
		root, server.server.URL, "ENG", "Private Person", "Private page title", "Private issue title",
		"private confluence body", "private jira body", "private-jira-token", "private-confluence-token",
	} {
		if strings.Contains(stdout, private) || strings.Contains(stderr, private) {
			t.Fatalf("content-free output leaked %q: stdout=%s stderr=%s", private, stdout, stderr)
		}
	}
	if !strings.Contains(stderr, "<redacted>") || strings.Contains(stderr, "/rest/api/") {
		t.Fatalf("verbose trace was not identity-redacted: %s", stderr)
	}
}

func TestCorpusBuildCLIRequestCapStopsBeforeAnotherTransportAttempt(t *testing.T) {
	root := corpusBuildCLIPrivateRoot(t)
	server := newCorpusBuildCLIServer(t)
	args := []string{
		"--read-only", "corpus", "build", "--root", root, "--initialize",
		"--confluence-space", "ENG", "--max-confluence-pages", "1",
		"--max-requests", "1", "--max-response-bytes", "1048576",
		"--max-members", "100", "--max-generation-bytes", "4194304",
		"--deadline", "1m", "--max-in-flight", "2", "--requests-per-second", "1000",
	}
	stdout, _, err := executeCLIRaw(t, server.environment(), args...)
	var closed *app.CorpusBuildError
	if err == nil || stdout != "" || !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) ||
		!errors.As(err, &closed) || closed.Reason != app.CorpusBuildReasonBudget ||
		len(server.snapshotRequests()) != 1 {
		t.Fatalf("stdout=%q err=%#v requests=%v", stdout, err, server.snapshotRequests())
	}
	if strings.Contains(err.Error(), "Private") || strings.Contains(err.Error(), server.server.URL) {
		t.Fatalf("budget error leaked backend data: %v", err)
	}
	store, openErr := corpus.Open(root, corpus.Options{})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(t.Context()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func TestCorpusBuildCLIResponseByteCapStopsPublication(t *testing.T) {
	root := corpusBuildCLIPrivateRoot(t)
	server := newCorpusBuildCLIServer(t)
	args := []string{
		"--read-only", "corpus", "build", "--root", root, "--initialize",
		"--confluence-space", "ENG", "--max-confluence-pages", "1",
		"--max-requests", "10", "--max-response-bytes", "10",
		"--max-members", "100", "--max-generation-bytes", "4194304",
		"--deadline", "1m", "--max-in-flight", "2", "--requests-per-second", "1000",
	}
	stdout, _, err := executeCLIRaw(t, server.environment(), args...)
	var closed *app.CorpusBuildError
	if err == nil || stdout != "" || !errors.Is(err, domain.ErrReadResponseBudgetExhausted) ||
		!errors.As(err, &closed) || closed.Reason != app.CorpusBuildReasonBudget ||
		len(server.snapshotRequests()) != 1 {
		t.Fatalf("stdout=%q err=%#v requests=%v", stdout, err, server.snapshotRequests())
	}
	store, openErr := corpus.Open(root, corpus.Options{})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(t.Context()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func corpusBuildCLIArgs(root string, valid bool) []string {
	maxRequests := "20"
	if !valid {
		maxRequests = "0"
	}
	return []string{
		"corpus", "build", "--root", root, "--initialize",
		"--jira-project", "ENG", "--max-jira-issues", "1",
		"--confluence-space", "ENG", "--max-confluence-pages", "1",
		"--max-requests", maxRequests, "--max-response-bytes", "1048576",
		"--max-members", "100", "--max-generation-bytes", "4194304",
		"--deadline", "1m", "--max-in-flight", "2", "--requests-per-second", "1000",
	}
}

func corpusBuildCLIPrivateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
