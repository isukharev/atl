package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func cliConfluencePlanFixture(t *testing.T) (root, planPath string) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{ID: "100", Title: "Example", SpaceKey: "DOC", Type: "page", Version: 3, Body: []byte("<p>old</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".csf"), []byte("<p>new</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath = filepath.Join(t.TempDir(), "plan.json")
	return
}

func normalizePlanCLIOutput(value string, paths ...string) []byte {
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, path := range paths {
		value = strings.ReplaceAll(value, path, "<PATH>")
	}
	value = regexp.MustCompile(`[0-9a-f]{64}`).ReplaceAllString(value, "<SHA256>")
	return []byte(value)
}

func TestConfPlanCreateIsConfiglessAndGolden(t *testing.T) {
	root, out := cliConfluencePlanFixture(t)
	stdout, code := runCLI(t, nil, "--read-only", "conf", "plan", "create", root, "--out", out)
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, stdout)
	}
	assertGolden(t, "conf_plan_create.json", normalizePlanCLIOutput(stdout, out))
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan info=%v err=%v", info, err)
	}
}

func TestConfPlanPreviewGoldenAndGETOnly(t *testing.T) {
	root, path := cliConfluencePlanFixture(t)
	if _, err := app.CreateConfluencePlan(root, root, path); err != nil {
		t.Fatal(err)
	}
	gets, puts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"100","type":"page","title":"Example","space":{"key":"DOC"},"version":{"number":3},"body":{"storage":{"value":"<p>old</p>","representation":"storage"}}}`)
		case http.MethodPut:
			puts++
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	stdout, code := runCLI(t, confEnv(srv), "conf", "plan", "apply", path)
	if code != exitOK || gets != 1 || puts != 0 {
		t.Fatalf("exit=%d gets=%d puts=%d out=%q", code, gets, puts, stdout)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "conf_plan_apply_preview.json", normalizePlanCLIOutput(stdout, root, canonicalRoot))
}

func TestConfPlanApplyFlagsFailBeforeConfig(t *testing.T) {
	if _, code := runCLI(t, nil, "conf", "plan", "apply", "missing.json", "--confirm", "YES"); code != exitUsage {
		t.Fatalf("wrong confirm exit=%d", code)
	}
	if _, code := runCLI(t, nil, "conf", "plan", "apply", "missing.json", "--confirm", "APPLY"); code != exitUsage {
		t.Fatalf("missing hash exit=%d", code)
	}
}

func TestConfPlanApplyIsBlockedByGlobalReadOnlyBeforePlanOrConfig(t *testing.T) {
	if _, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1"}, "conf", "plan", "apply", "missing.json"); code != exitCheckFailed {
		t.Fatalf("read-only exit=%d", code)
	}
}
