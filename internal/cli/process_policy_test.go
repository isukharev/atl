package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

func TestCommandTreesKeepInvocationRuntimeIndependent(t *testing.T) {
	for _, name := range policyRelevantEnvironment {
		switch name {
		case "PATH", "HOME", "USERPROFILE":
			continue
		default:
			t.Setenv(name, "")
		}
	}
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())

	t.Setenv("ATL_POLICY", `{"schema_version":1,"rules":[{"id":"allow-docs","effect":"allow","verbs":["update"],"resource":{"service":"jira","project":"DOC"}}]}`)
	textRoot := newRoot()
	textRoot.SetArgs([]string{"--output", "text", "--read-only", "doctor"})
	var textOut, textErr bytes.Buffer
	textRoot.SetOut(&textOut)
	textRoot.SetErr(&textErr)

	t.Setenv("ATL_POLICY", "")
	jsonRoot := newRoot()
	jsonRoot.SetArgs([]string{"--output", "json", "doctor"})
	var jsonOut, jsonErr bytes.Buffer
	jsonRoot.SetOut(&jsonOut)
	jsonRoot.SetErr(&jsonErr)

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	blockRun := func(root *cobra.Command) {
		t.Helper()
		doctor, _, err := root.Find([]string{"doctor"})
		if err != nil {
			t.Fatalf("find doctor command: %v", err)
		}
		original := doctor.RunE
		doctor.RunE = func(cmd *cobra.Command, args []string) error {
			ready <- struct{}{}
			<-release
			return original(cmd, args)
		}
	}
	blockRun(textRoot)
	blockRun(jsonRoot)

	results := make(chan error, 2)
	run := func(root *cobra.Command) {
		results <- root.ExecuteContext(context.Background())
	}
	go run(textRoot)
	go run(jsonRoot)
	<-ready
	<-ready
	close(release)
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("execute command tree: %v", err)
		}
	}

	if got := textOut.String(); !strings.Contains(got, "safety: read_only=true configured=false effective=true source=flag status=available") ||
		!strings.Contains(got, "content_policy: active=true") || strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Fatalf("text/read-only/policy runtime crossed command trees: stdout=%q stderr=%q", got, textErr.String())
	}
	var result struct {
		Safety struct {
			ReadOnly bool `json:"read_only"`
		} `json:"safety"`
		ContentPolicy struct {
			Active bool `json:"active"`
		} `json:"content_policy"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &result); err != nil {
		t.Fatalf("JSON command tree output: %v; stdout=%q stderr=%q", err, jsonOut.String(), jsonErr.String())
	}
	if result.Safety.ReadOnly || result.ContentPolicy.Active {
		t.Fatalf("JSON command tree inherited peer runtime: %+v", result)
	}
}

func TestPolicyShowAndExplainAreOffline(t *testing.T) {
	policy := `{"schema_version":1,"rules":[{"id":"allow-docs","effect":"allow","verbs":["update"],"resource":{"service":"jira","project":"DOC"}}]}`
	env := map[string]string{"ATL_POLICY": policy, "ATL_CONFIG_DIR": t.TempDir()}
	out, code := runCLI(t, env, "policy", "show")
	if code != exitOK {
		t.Fatalf("policy show exit=%d output=%s", code, out)
	}
	var shown policyShowResult
	if json.Unmarshal([]byte(out), &shown) != nil || !shown.Active || shown.Source != "env_inline" || shown.Governs["jira"] != "guarded" {
		t.Fatalf("policy show=%s", out)
	}
	out, code = runCLI(t, env, "policy", "explain", "--service", "jira", "--verb", "update", "--key", "DOC-7")
	var explained policyExplainResult
	if code != exitOK || json.Unmarshal([]byte(out), &explained) != nil || explained.Decision != "allow" {
		t.Fatalf("policy explain exit=%d output=%s", code, out)
	}
	if _, code = runCLI(t, env, "policy", "explain", "--service", "jira", "--verb", "update", "--kind", "page"); code != exitUsage {
		t.Fatalf("policy explain accepted a cross-service resource kind: exit=%d", code)
	}
	conditionalPolicy := `{"schema_version":1,"rules":[{"id":"docs-tree","effect":"allow","verbs":["update"],"resource":{"service":"confluence","space":"DOC","under":"10"}}]}`
	out, code = runCLI(t, map[string]string{"ATL_POLICY": conditionalPolicy, "ATL_CONFIG_DIR": t.TempDir()},
		"policy", "explain", "--service", "confluence", "--verb", "update")
	if code != exitOK || json.Unmarshal([]byte(out), &explained) != nil || explained.Decision != "conditional" || strings.Join(explained.Unresolved, ",") != "space,under" {
		t.Fatalf("conditional policy explain exit=%d output=%s result=%+v", code, out, explained)
	}
}

func TestPolicyShowNeverOverstatesAllowMinusDeny(t *testing.T) {
	policy := `{"schema_version":1,"rules":[{"id":"allow-jira","effect":"allow","verbs":["update"],"resource":{"service":"jira"}},{"id":"deny-secret","effect":"deny","verbs":["update"],"resource":{"service":"jira","project":"SECRET"}}]}`
	out, code := runCLI(t, map[string]string{"ATL_POLICY": policy, "ATL_CONFIG_DIR": t.TempDir()}, "policy", "show")
	var shown policyShowResult
	if code != exitOK || json.Unmarshal([]byte(out), &shown) != nil {
		t.Fatalf("policy show exit=%d output=%s", code, out)
	}
	if grants := shown.Grants["jira"]["update"]; len(grants) != 0 {
		t.Fatalf("allow-minus-deny summarized as an effective grant: %v", grants)
	}
}

func TestPolicyShowIntersectsConfigurationReadOnly(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := `{"schema_version":1,"rules":[{"id":"allow-jira","effect":"allow","verbs":["update"],"resource":{"service":"jira"}}]}`
	out, code := runCLI(t, map[string]string{"ATL_POLICY": policy, "ATL_CONFIG_DIR": configDir}, "policy", "show")
	var shown policyShowResult
	if code != exitOK || json.Unmarshal([]byte(out), &shown) != nil {
		t.Fatalf("policy show exit=%d output=%s", code, out)
	}
	if !shown.ReadOnly.Active || shown.ReadOnly.Source != "configuration" || !shown.ReadOnly.ConfiguredReadOnly ||
		!shown.ReadOnly.EffectiveReadOnly || shown.ReadOnly.ReadOnlySource != "configuration" || len(shown.Grants["jira"]["update"]) != 0 {
		t.Fatalf("policy show did not intersect configured read-only mode: %+v", shown)
	}
}

func TestProcessPolicySourcesAreMutuallyExclusiveAndSticky(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, execErr := executeCLIRaw(t, map[string]string{
		"ATL_POLICY": `{"schema_version":1,"rules":[]}`, "ATL_POLICY_FILE": path,
	}, "policy", "show")
	if codeFor(execErr) != exitConfig || !strings.Contains(execErr.Error(), "mutually exclusive") {
		t.Fatalf("exclusive sources err=%v", execErr)
	}

	t.Setenv("ATL_POLICY", "")
	t.Setenv("ATL_POLICY_FILE", path)
	t.Setenv("ATL_POLICY_SHA256", "")
	t.Setenv("ATL_POLICY_REQUIRED", "")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"rules":[{"id":"allow","effect":"allow","verbs":["update"],"resource":{"service":"jira"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	frozen := newProcessPolicy()
	if err := os.WriteFile(path, []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := frozen.resolve()
	if err != nil || len(resolved.Layers) != 1 || resolved.Layers[0].Source != "env_file" {
		t.Fatalf("process policy was not frozen at construction: resolved=%+v err=%v", resolved, err)
	}
}

func TestInvalidContentPolicyDoesNotGovernReads(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"reader","displayName":"Reader"}`))
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code := runCLI(t, map[string]string{
		"ATL_POLICY": `{"schema_version":1,"rules":[]}`, "ATL_POLICY_FILE": path,
		"ATL_JIRA_URL": server.URL, "ATL_JIRA_PAT": "test-pat",
	}, "jira", "me")
	if code != exitOK || requests != 1 {
		t.Fatalf("ungoverned read exit=%d requests=%d", code, requests)
	}
}

func TestRequiredPolicyAndDenyOnlyPreflightPrecedeConfig(t *testing.T) {
	brokenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenDir, "config.json"), []byte(`{"jira_url":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, execErr := executeCLIRaw(t, map[string]string{
		"ATL_CONFIG_DIR": brokenDir, "ATL_POLICY_REQUIRED": "1",
	}, "jira", "issue", "assign", "DOC-1", "--none")
	if codeFor(execErr) != exitCheckFailed {
		t.Fatalf("required policy err=%v", execErr)
	}

	policy := `{"schema_version":1,"rules":[{"id":"allow-docs","effect":"allow","verbs":["update"],"resource":{"service":"jira","project":"DOC"}}]}`
	_, _, execErr = executeCLIRaw(t, map[string]string{
		"ATL_CONFIG_DIR": brokenDir, "ATL_POLICY": policy,
	}, "jira", "issue", "assign", "OPS-1", "--none")
	var denial *contentpolicy.DenialError
	if codeFor(execErr) != exitCheckFailed || !errors.As(execErr, &denial) || denial.Details.Phase != "preflight" {
		t.Fatalf("preflight denial err=%v details=%+v", execErr, denial)
	}
}

func TestEveryRemoteJiraMutatorUsesTheProcessPolicyBeforeBackendAccess(t *testing.T) {
	commands := [][]string{
		{"jira", "issue", "assign", "DOC-1"},
		{"jira", "issue", "attachment", "upload", "DOC-1"},
		{"jira", "issue", "comment", "add", "DOC-1"},
		{"jira", "issue", "comment", "delete", "DOC-1", "1"},
		{"jira", "issue", "create", "--project", "DOC", "--type", "Task", "--summary", "reviewed", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64)},
		{"jira", "issue", "delete", "DOC-1"},
		{"jira", "issue", "edit", "DOC-1"},
		{"jira", "issue", "field", "set", "DOC-1"},
		{"jira", "issue", "labels", "DOC-1"},
		{"jira", "issue", "link", "add", "DOC-1", "--to", "OPS-1", "--type", "Blocks"},
		{"jira", "issue", "link", "delete", "1", "--from", "DOC-1", "--to", "OPS-1", "--type", "Blocks"},
		{"jira", "issue", "link-epic", "DOC-1"},
		{"jira", "issue", "plan", "apply"},
		{"jira", "issue", "transition", "DOC-1"},
		{"jira", "issue", "update", "DOC-1"},
		{"jira", "issue", "watchers", "add", "DOC-1"},
		{"jira", "issue", "watchers", "remove", "DOC-1"},
		{"jira", "issue", "worklog", "add", "DOC-1"},
		{"jira", "push", "missing-mirror"},
		{"jira", "sprint", "add", "1", "DOC-1"},
		{"jira", "sprint", "remove", "DOC-1"},
	}
	if len(commands) != 21 {
		t.Fatalf("remote Jira mutator oracle=%d want=21", len(commands))
	}
	for _, args := range commands {
		name := strings.Join(args[1:], "_")
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			t.Cleanup(server.Close)
			_, code := runCLI(t, map[string]string{
				"ATL_POLICY_REQUIRED": "1", "ATL_JIRA_URL": server.URL, "ATL_JIRA_PAT": "test-pat",
			}, args...)
			if code != exitCheckFailed || requests != 0 {
				t.Fatalf("%v exit=%d requests=%d, want policy refusal before backend", args, code, requests)
			}
		})
	}
}

func TestContentPolicyPreflightNeverDeniesAResolvedAllow(t *testing.T) {
	layers := []contentpolicy.Layer{{Source: "env_inline", Policy: contentpolicy.Policy{SchemaVersion: 1, Rules: []contentpolicy.Rule{{
		ID: "allow-docs", Effect: contentpolicy.EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: contentpolicy.Selector{Services: []string{"jira"}, Projects: []string{"DOC"}},
	}}}}}
	partial := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Key: "DOC-1"}}}
	if denial := contentpolicy.PreflightDeny(layers, partial); denial != nil {
		t.Fatalf("partial target denied: %v", denial)
	}
	resolved := partial
	resolved.Targets[0].Project = "DOC"
	if decision := contentpolicy.Decide(layers, resolved); !decision.Allowed {
		t.Fatalf("resolved target denied: %+v", decision)
	}
}

func TestPolicyRelevantEnvironmentIsClosedAndComplete(t *testing.T) {
	want := []string{
		"PATH", "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "ATL_CONFIG_DIR", "ATL_JIRA_URL", "JIRA_URL", "ATL_CONFLUENCE_URL", "CONFLUENCE_URL", "ATL_UPDATE_URL",
		"ATL_JIRA_CA_BUNDLE", "ATL_CONFLUENCE_CA_BUNDLE",
		"ATL_JIRA_PAT", "JIRA_PAT", "ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_INTEGRATION", "TEST_JIRA_PAT", "TEST_CONFLUENCE_PAT",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"ATL_ALLOW_INSECURE", "ATL_READ_ONLY", "ATL_MIRROR_ROOT", "ATL_NO_UPDATE", "ATL_UPDATE_DEBUG", "ATL_VERBOSE",
		"ATL_POLICY", "ATL_POLICY_FILE", "ATL_POLICY_SHA256", "ATL_POLICY_REQUIRED",
	}
	got := append([]string(nil), policyRelevantEnvironment...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("policy environment=%v want=%v", got, want)
	}
	for index := 1; index < len(got); index++ {
		if got[index] == got[index-1] {
			t.Fatalf("duplicate policy environment name %q", got[index])
		}
	}
	observed, sawUserHomeDir := productPolicyEnvironmentOracle(t)
	declared := make(map[string]bool, len(got))
	for _, name := range got {
		declared[name] = true
	}
	for name, source := range observed {
		if !declared[name] {
			t.Errorf("policy-relevant environment %s read by %s is absent from launcher contract", name, source)
		}
	}
	if !sawUserHomeDir || !declared["HOME"] || !declared["USERPROFILE"] {
		t.Errorf("os.UserHomeDir oracle=%t HOME=%t USERPROFILE=%t", sawUserHomeDir, declared["HOME"], declared["USERPROFILE"])
	}
}

func productPolicyEnvironmentOracle(t *testing.T) (map[string]string, bool) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate process policy test")
	}
	internalRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), ".."))
	observed := map[string]string{}
	sawUserHomeDir := false
	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == "agenteval" || entry.Name() == "testbackend") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == filepath.Join(filepath.Dir(testFile), "process_policy.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				if value.Kind != token.STRING {
					return true
				}
				name, err := strconv.Unquote(value.Value)
				if err == nil && policyEnvironmentName(name) {
					observed[name] = strings.TrimPrefix(path, internalRoot+string(filepath.Separator))
				}
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
					if ident, selected := selector.X.(*ast.Ident); selected && ident.Name == "os" && selector.Sel.Name == "UserHomeDir" {
						sawUserHomeDir = true
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan product environment reads: %v", err)
	}
	return observed, sawUserHomeDir
}

func policyEnvironmentName(value string) bool {
	if value == "JIRA_URL" || value == "JIRA_PAT" || value == "CONFLUENCE_URL" || value == "CONFLUENCE_PAT" ||
		value == "TEST_JIRA_PAT" || value == "TEST_CONFLUENCE_PAT" {
		return true
	}
	if !strings.HasPrefix(value, "ATL_") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "ATL_") {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return len(value) > len("ATL_")
}

func TestBackendBindingAppliesOnlyToGovernedWrites(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"reader","displayName":"Reader"}`))
	}))
	t.Cleanup(server.Close)
	policy := `{"schema_version":1,"backend":{"jira_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"rules":[{"id":"allow-update","effect":"allow","verbs":["update"],"resource":{"service":"jira"}}]}`
	env := map[string]string{
		"ATL_POLICY": policy, "ATL_POLICY_REQUIRED": "1", "ATL_JIRA_URL": server.URL, "ATL_JIRA_PAT": "test-pat",
	}
	if _, code := runCLI(t, env, "jira", "me"); code != exitOK || requests != 1 {
		t.Fatalf("ungoverned read exit=%d requests=%d", code, requests)
	}
	_, _, execErr := executeCLIRaw(t, env, "jira", "issue", "assign", "DOC-1", "--none")
	if codeFor(execErr) != exitCheckFailed || requests != 1 {
		t.Fatalf("binding mismatch err=%v requests=%d", execErr, requests)
	}
}

func TestPreflightUsesTheAdapterResourceKind(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		args   []string
	}{
		{
			name:   "jira attachment",
			policy: `{"schema_version":1,"rules":[{"id":"allow-attachments","effect":"allow","verbs":["create"],"resource":{"service":"jira","kind":"attachment"}}]}`,
			args:   []string{"jira", "issue", "attachment", "upload", "DOC-1", "--file", "missing.bin"},
		},
		{
			name:   "confluence attachment",
			policy: `{"schema_version":1,"rules":[{"id":"allow-attachments","effect":"allow","verbs":["create"],"resource":{"service":"confluence","kind":"attachment"}}]}`,
			args:   []string{"conf", "attachment", "upload", "--id", "10", "--file", "missing.bin"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := executeCLIRaw(t, map[string]string{"ATL_POLICY": test.policy}, test.args...)
			var denial *contentpolicy.DenialError
			if errors.As(err, &denial) || codeFor(err) != exitConfig {
				var details any
				if denial != nil {
					details = denial.Details
				}
				t.Fatalf("preflight used container kind: err=%T %v denial=%+v", err, err, details)
			}
		})
	}
}

func TestProcessPolicyReachesAuthoritativeAdapterGuard(t *testing.T) {
	reads, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"100","key":"NEW-1","fields":{"project":{"key":"NEW"}}}`))
			return
		}
		writes++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	policy := `{"schema_version":1,"rules":[{"id":"allow-update","effect":"allow","verbs":["update"],"resource":{"service":"jira"}},{"id":"deny-new","effect":"deny","verbs":["update"],"resource":{"service":"jira","key":"NEW-1"}}]}`
	_, _, execErr := executeCLIRaw(t, map[string]string{
		"ATL_POLICY": policy, "ATL_JIRA_URL": server.URL, "ATL_JIRA_PAT": "test-pat",
	}, "jira", "issue", "assign", "OLD-1", "--none")
	var denial *contentpolicy.DenialError
	if codeFor(execErr) != exitCheckFailed || !errors.As(execErr, &denial) || reads != 1 || writes != 0 || denial.Details.Phase != "resolved" {
		t.Fatalf("adapter denial err=%v denial=%+v reads=%d writes=%d", execErr, denial, reads, writes)
	}
}

func TestContentPolicyErrorWireCarriesCommandAndDenial(t *testing.T) {
	root := newRoot()
	command, _, err := root.Find([]string{"jira", "issue", "update"})
	if err != nil {
		t.Fatal(err)
	}
	denial := contentpolicy.NewSourceDenial(contentpolicy.ReasonPolicyRequiredAbsent, "policy required", "required", &contentpolicy.Resolved{})
	var output bytes.Buffer
	writeErrorWithCommand(&output, "json", denial, exitCheckFailed, recoveryOperation(command), command)
	var body map[string]any
	if json.Unmarshal(output.Bytes(), &body) != nil || body["kind"] != "content_policy" || body["policy"] != "content" || body["command"] != "atl jira issue update" || body["denial"] == nil {
		t.Fatalf("content policy error=%s", output.String())
	}
}
