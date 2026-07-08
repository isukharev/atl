package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/mirror"
)

// scaffoldJiraMirror writes a minimal pulled Jira mirror (one issue) rooted at a
// RELATIVE dir under the test's working directory, so command output carries a
// stable relative path (golden-friendly). It returns the .wiki path.
func scaffoldJiraMirror(t *testing.T, root, key, body string) string {
	t.Helper()
	const proj = "PROJ"
	dir := filepath.Join(root, proj)
	if err := os.MkdirAll(filepath.Join(root, ".atl", "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wiki := filepath.Join(dir, key+".wiki")
	if err := os.WriteFile(wiki, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", key+".wiki"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	relWiki := filepath.Join(proj, key+".wiki")
	sc := fmt.Sprintf(`{"pages":{"%s":{"id":"%s","version":0,"hash":"%s","path":"%s"}}}`+"\n",
		key, key, mirror.Hash([]byte(body)), relWiki)
	if err := os.WriteFile(filepath.Join(root, ".atl", "state.json"), []byte(sc), 0o600); err != nil {
		t.Fatal(err)
	}
	return wiki
}

// issueJSON renders a canned Jira issue response with the given description.
func issueJSON(key, desc string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"S","description":%q,"status":{"name":"Open"},"issuetype":{"name":"Task"},"project":{"key":"PROJ"}}}`, key, desc)
}

// TestJiraStatusGolden locks the JSON shape of `jira status` on a locally-edited
// mirror (local-only, no --remote — so no server is contacted). The random temp
// root is normalized to a fixed token so the golden is host-independent.
func TestJiraStatusGolden(t *testing.T) {
	root := t.TempDir()
	wiki := scaffoldJiraMirror(t, root, "PROJ-1", "line one\nline two")
	if err := os.WriteFile(wiki, []byte("line one\nline two edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := jsonServer(t, http.StatusOK, `{}`) // never hit without --remote
	out, code := runCLI(t, jiraEnv(srv), "jira", "status", root)
	if code != exitOK {
		t.Fatalf("jira status: exit %d (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_status.json", []byte(normalizeRoot(out, root)))
}

// TestJiraPushDryRunGolden locks the JSON shape of `jira push` in its default
// dry-run mode: the remote description matches the base (no drift), so the item
// carries the unified diff of the local edit and pushes nothing.
func TestJiraPushDryRunGolden(t *testing.T) {
	root := t.TempDir()
	base := "line one\nline two"
	wiki := scaffoldJiraMirror(t, root, "PROJ-1", base)
	if err := os.WriteFile(wiki, []byte("line one\nline two changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := jsonServer(t, http.StatusOK, issueJSON("PROJ-1", base))
	out, code := runCLI(t, jiraEnv(srv), "jira", "push", wiki, "--into", root)
	if code != exitOK {
		t.Fatalf("jira push (dry-run): exit %d (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_push_dryrun.json", []byte(normalizeRoot(out, root)))
}

// normalizeRoot replaces the absolute temp mirror root with a stable token so
// path-carrying output can be pinned in a golden file across machines.
func normalizeRoot(out, root string) string {
	return strings.ReplaceAll(out, root, "MIRROR")
}

// TestJiraPushApplyWiring proves --apply issues the write (a PUT to the issue)
// and reports the issue pushed. A dry-run over the same setup must NOT PUT.
func TestJiraPushApplyWiring(t *testing.T) {
	base := "before"
	newBody := "after"

	// Stateful fake: GET returns the current server description; a PUT updates it
	// (last-writer-wins, as Jira DC does) so the drift check reads the base while
	// the post-apply refresh reads the written body.
	var mu sync.Mutex
	var methods []string
	current := base
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			var payload struct {
				Fields struct {
					Description string `json:"description"`
				} `json:"fields"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			current = payload.Fields.Description
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body := current
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueJSON("PROJ-1", body)))
	}))
	t.Cleanup(srv.Close)

	hasPut := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range methods {
			if m == http.MethodPut {
				return true
			}
		}
		return false
	}

	t.Run("dry-run does not write", func(t *testing.T) {
		t.Chdir(t.TempDir())
		wiki := scaffoldJiraMirror(t, "mirror-jira", "PROJ-1", base)
		_ = os.WriteFile(wiki, []byte(newBody), 0o644)
		mu.Lock()
		methods = nil
		current = base
		mu.Unlock()
		out, code := runCLI(t, jiraEnv(srv), "jira", "push", "mirror-jira/PROJ/PROJ-1.wiki")
		if code != exitOK {
			t.Fatalf("dry-run push: exit %d (%q)", code, out)
		}
		if hasPut() {
			t.Fatal("a dry-run push must not issue a PUT")
		}
	})

	t.Run("apply writes", func(t *testing.T) {
		t.Chdir(t.TempDir())
		wiki := scaffoldJiraMirror(t, "mirror-jira", "PROJ-1", base)
		_ = os.WriteFile(wiki, []byte(newBody), 0o644)
		mu.Lock()
		methods = nil
		current = base
		mu.Unlock()
		out, code := runCLI(t, jiraEnv(srv), "jira", "push", "--apply", "mirror-jira/PROJ/PROJ-1.wiki")
		if code != exitOK {
			t.Fatalf("apply push: exit %d (%q)", code, out)
		}
		if !hasPut() {
			t.Fatal("--apply must issue a PUT")
		}
		if !strings.Contains(out, `"pushed": true`) {
			t.Fatalf("apply output must report pushed:true, got %q", out)
		}
	})
}
