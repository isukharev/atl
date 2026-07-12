package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
)

func TestEveryExecutableCommandHasExplicitAccessPolicy(t *testing.T) {
	root := newRoot()
	seen := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Run != nil || cmd.RunE != nil {
			path := cmd.CommandPath()[len(root.Name())+1:]
			seen[path] = true
			access := cmd.Annotations[accessAnnotation]
			if access != "read-only" && access != "mutating" {
				t.Errorf("%s access=%q", cmd.CommandPath(), access)
			}
			if (access == "mutating") != mutatingCommandPaths[path] {
				t.Errorf("%s mutation classification drift", cmd.CommandPath())
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	for path := range knownCommandPaths {
		if !seen[path] {
			t.Errorf("classified command %q is no longer registered", path)
		}
	}
}

func TestReadOnlyFlagBlocksMutationBeforeNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	_, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "issue", "create", "--project", "PROJ", "--type", "Task", "--summary", "blocked", "--from-file", "/definitely/missing/description.wiki")
	if code != exitCheckFailed || requests != 0 {
		t.Fatalf("exit=%d requests=%d", code, requests)
	}
}

func TestReadOnlyPolicyAllowsBackendRead(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"issues":[],"total":0}`)
	defer srv.Close()
	if _, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "issue", "search", "--jql", "project=PROJ"); code != exitOK {
		t.Fatalf("read exit=%d", code)
	}
}

func TestReadOnlyEnvironmentAndConfigCannotBeDowngradedByFalseFlag(t *testing.T) {
	if _, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1"}, "jira", "issue", "delete", "PROJ-1", "--force"); code != exitCheckFailed {
		t.Fatalf("env policy exit=%d", code)
	}

	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_READ_ONLY", "")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	if err := config.Save(&config.Config{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	root.SetArgs([]string{"--read-only=false", "config", "set", "safety.read_only", "false"})
	if err := root.ExecuteContext(context.Background()); codeFor(err) != exitCheckFailed {
		t.Fatalf("config policy error=%v code=%d", err, codeFor(err))
	}
}

func TestUnclassifiedCommandFailsClosed(t *testing.T) {
	cmd := &cobra.Command{Use: "future", Annotations: map[string]string{accessAnnotation: "unclassified"}}
	if err := enforceAccessPolicy(cmd, false); codeFor(err) != exitCheckFailed {
		t.Fatalf("error=%v code=%d", err, codeFor(err))
	}
}

func TestReadOnlyRefusalHasStableJSONMetadata(t *testing.T) {
	var output bytes.Buffer
	writeError(&output, "json", &readOnlyPolicyError{Command: "atl jira push"}, exitCheckFailed)
	var body map[string]any
	if err := json.Unmarshal(output.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["policy"] != "read_only" || body["command"] != "atl jira push" || body["code"] != float64(exitCheckFailed) {
		t.Fatalf("body=%v", body)
	}
}
