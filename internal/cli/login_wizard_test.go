package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// whoamiServer replies with a fixed display name on any path.
func whoamiServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
}

// cleanAuthEnv neutralises ambient PAT env so resolveToken takes the
// "no stored token" path and the canned input lines stay aligned.
func cleanAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_INTEGRATION", "")
	for _, k := range []string{"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_JIRA_PAT", "JIRA_PAT"} {
		t.Setenv(k, "")
	}
}

func wizardFrom(input string) wizardIO {
	return wizardIO{
		in:         bufio.NewReader(strings.NewReader(input)),
		out:        io.Discard,
		readSecret: func() (string, error) { return "the-pat", nil },
	}
}

func TestWizardConfiguresConfluenceSkipsJira(t *testing.T) {
	cleanAuthEnv(t)
	srv := whoamiServer(t)
	defer srv.Close()

	// Configure Confluence? y / URL / (PAT canned) ; Configure Jira? n
	sum, err := runLoginWizard(wizardFrom("y\n" + srv.URL + "\nn\n"))
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if sum.Confluence.Status != "configured" || sum.Confluence.User != "Jane Doe" {
		t.Fatalf("confluence = %+v, want configured/Jane Doe", sum.Confluence)
	}
	if sum.Jira.Status != "skipped" {
		t.Fatalf("jira = %+v, want skipped", sum.Jira)
	}
	// URL persisted to config, PAT persisted to credentials.
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "config.json")); !strings.Contains(string(b), srv.URL) {
		t.Fatalf("config.json missing confluence URL: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "credentials.json")); !strings.Contains(string(b), "the-pat") {
		t.Fatalf("credentials.json missing PAT: %s", b)
	}
}

func TestWizardSkipsBothLeavesNothing(t *testing.T) {
	cleanAuthEnv(t)
	sum, err := runLoginWizard(wizardFrom("n\nn\n"))
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if sum.Confluence.Status != "skipped" || sum.Jira.Status != "skipped" {
		t.Fatalf("want both skipped, got %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials.json should not exist after skipping both")
	}
}

func TestWizardBadTokenRetryDecline(t *testing.T) {
	cleanAuthEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	// Confluence: y / URL / (PAT) / validation 401 / Retry? n -> skipped ; Jira: n
	sum, err := runLoginWizard(wizardFrom("y\n" + srv.URL + "\nn\nn\n"))
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if sum.Confluence.Status != "skipped" {
		t.Fatalf("confluence = %+v, want skipped after declined retry", sum.Confluence)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("a failed validation must not write credentials.json")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "config.json")); !os.IsNotExist(err) {
		t.Fatalf("a failed validation must not write config.json")
	}
}

func TestWizardKeepExistingPAT(t *testing.T) {
	cleanAuthEnv(t)
	srv := whoamiServer(t)
	defer srv.Close()
	// Pre-store a Confluence PAT so the Replace prompt appears.
	if err := authLoginForTest("confluence", "stored-pat"); err != nil {
		t.Fatalf("seed PAT: %v", err)
	}

	// Confluence? y / URL / Replace stored PAT? n (keep) / validate ok ; Jira? n
	wz := wizardIO{
		in:         bufio.NewReader(strings.NewReader("y\n" + srv.URL + "\nn\nn\n")),
		out:        io.Discard,
		readSecret: func() (string, error) { return "NEW-should-not-be-used", nil },
	}
	sum, err := runLoginWizard(wz)
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	if sum.Confluence.Status != "configured" {
		t.Fatalf("confluence = %+v, want configured", sum.Confluence)
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("ATL_CONFIG_DIR"), "credentials.json")); !strings.Contains(string(b), "stored-pat") {
		t.Fatalf("kept PAT should remain in credentials.json: %s", b)
	}
}

func TestWizardURLPromptEOFDoesNotHang(t *testing.T) {
	cleanAuthEnv(t)
	// "y" configures Confluence, then input is exhausted at the URL prompt (EOF).
	_, err := runLoginWizard(wizardFrom("y\n"))
	if err == nil {
		t.Fatalf("expected an error when stdin is exhausted at the URL prompt, got nil")
	}
}

// authLoginForTest seeds a stored PAT through the same path the wizard uses.
func authLoginForTest(svc, token string) error {
	s, err := svcOf(svc)
	if err != nil {
		return err
	}
	return authLogin(s, token)
}

func TestAuthLoginFromFileWithoutServiceErrors(t *testing.T) {
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	pat := filepath.Join(t.TempDir(), "pat")
	if err := os.WriteFile(pat, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	root.SetArgs([]string{"auth", "login", "--from-file", pat})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--from-file requires --service") {
		t.Fatalf("want --from-file-requires-service usage error, got %v", err)
	}
	if codeFor(err) != exitUsage {
		t.Fatalf("want exit %d, got %d (err=%v)", exitUsage, codeFor(err), err)
	}
}

func TestAuthLoginNonTTYIsUsageError(t *testing.T) {
	// In `go test`, os.Stdin is not a TTY, so bare `auth login` must refuse.
	_, code := runCLI(t, nil, "auth", "login")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage)", code, exitUsage)
	}
}

func TestAuthLoginWizardGolden(t *testing.T) {
	cleanAuthEnv(t)
	srv := whoamiServer(t)
	defer srv.Close()

	sum, err := runLoginWizard(wizardFrom("y\n" + srv.URL + "\nn\n"))
	if err != nil {
		t.Fatalf("wizard: %v", err)
	}
	// emit() is called directly (no flag parse), so pin the package-global
	// output format to JSON rather than depend on whatever a prior test left.
	prev := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = prev }()

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := emit(cmd, sum, nil); err != nil {
		t.Fatalf("emit: %v", err)
	}
	assertGolden(t, "auth_login_wizard.json", buf.Bytes())
}
