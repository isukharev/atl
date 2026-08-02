package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestOnePageLimitsRejectInvalidValuesBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer srv.Close()

	tests := []struct {
		name string
		env  map[string]string
		args []string
		max  int
	}{
		{name: "confluence search", env: confEnv(srv), args: []string{"conf", "search", "--cql", "type = page"}, max: 100},
		{name: "confluence page list", env: confEnv(srv), args: []string{"conf", "page", "list", "--space", "ENG"}, max: 100},
		{name: "jira issue search", env: jiraEnv(srv), args: []string{"jira", "issue", "search", "--jql", "project = ENG"}, max: 1000},
		{name: "jira issue children", env: jiraEnv(srv), args: []string{"jira", "issue", "children", "ENG-1"}, max: 1000},
		{name: "jira user search", env: jiraEnv(srv), args: []string{"jira", "user", "search", "alice"}, max: 1000},
		{name: "jira board list", env: jiraEnv(srv), args: []string{"jira", "board", "list"}, max: 50},
		{name: "jira board issues", env: jiraEnv(srv), args: []string{"jira", "board", "issues", "1"}, max: 50},
		{name: "jira board backlog", env: jiraEnv(srv), args: []string{"jira", "board", "backlog", "1"}, max: 50},
		{name: "jira sprint list", env: jiraEnv(srv), args: []string{"jira", "sprint", "list", "--board", "1"}, max: 50},
		{name: "jira sprint issues", env: jiraEnv(srv), args: []string{"jira", "sprint", "issues", "1"}, max: 50},
	}

	for _, tt := range tests {
		for _, limit := range []int{0, -1, tt.max + 1} {
			t.Run(tt.name+"/limit="+strconv.Itoa(limit), func(t *testing.T) {
				requests.Store(0)
				args := append(append([]string{}, tt.args...), "--limit", strconv.Itoa(limit))
				stdout, code := runCLI(t, tt.env, args...)
				if code != exitUsage {
					t.Fatalf("exit code = %d, want %d; stdout=%q", code, exitUsage, stdout)
				}
				if stdout != "" {
					t.Fatalf("stdout = %q, want empty", stdout)
				}
				if got := requests.Load(); got != 0 {
					t.Fatalf("requests = %d, want 0", got)
				}
			})
		}
	}
}

func TestLimitFlagDefaultsPreservePageAndAggregateSemantics(t *testing.T) {
	tests := []struct {
		name string
		path []string
		want string
	}{
		{name: "confluence search", path: []string{"conf", "search"}, want: "25"},
		{name: "confluence page list", path: []string{"conf", "page", "list"}, want: "25"},
		{name: "jira issue search", path: []string{"jira", "issue", "search"}, want: "50"},
		{name: "jira issue children", path: []string{"jira", "issue", "children"}, want: "50"},
		{name: "jira user search", path: []string{"jira", "user", "search"}, want: "50"},
		{name: "jira board list", path: []string{"jira", "board", "list"}, want: "50"},
		{name: "jira board issues", path: []string{"jira", "board", "issues"}, want: "50"},
		{name: "jira board backlog", path: []string{"jira", "board", "backlog"}, want: "50"},
		{name: "jira sprint list", path: []string{"jira", "sprint", "list"}, want: "50"},
		{name: "jira sprint issues", path: []string{"jira", "sprint", "issues"}, want: "50"},
		{name: "jira pull", path: []string{"jira", "pull"}, want: "100"},
		{name: "jira issue tree", path: []string{"jira", "issue", "tree"}, want: "100"},
		{name: "jira planning report", path: []string{"jira", "planning", "report"}, want: "100"},
		{name: "jira quality report", path: []string{"jira", "quality-report"}, want: "100"},
		{name: "jira structure pull issues", path: []string{"jira", "structure", "pull-issues"}, want: "0"},
	}

	root := newRoot()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, _, err := root.Find(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			flag := cmd.Flags().Lookup("limit")
			if flag == nil {
				t.Fatalf("%v has no --limit flag", tt.path)
			}
			if flag.DefValue != tt.want {
				t.Fatalf("default --limit = %q, want %q", flag.DefValue, tt.want)
			}
		})
	}
}

func TestLimitValidatorBoundaries(t *testing.T) {
	for _, limit := range []int{1, 100} {
		if err := validatePageLimit(limit, 100); err != nil {
			t.Fatalf("validatePageLimit(%d, 100): %v", limit, err)
		}
	}
	for _, limit := range []int{0, 1} {
		if err := validateAggregateLimit(limit); err != nil {
			t.Fatalf("validateAggregateLimit(%d): %v", limit, err)
		}
	}
}

func TestAggregateLimitsRejectNegativeBeforeEffects(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer srv.Close()

	tests := []struct {
		name       string
		args       func(string) []string
		effectPath bool
	}{
		{
			name: "jira pull",
			args: func(path string) []string {
				return []string{"jira", "pull", "--jql", "project = ENG", "--into", path, "--limit", "-1"}
			},
			effectPath: true,
		},
		{
			name: "issue tree",
			args: func(string) []string {
				return []string{"jira", "issue", "tree", "--jql", "project = ENG", "--limit", "-1"}
			},
		},
		{
			name: "planning report",
			args: func(path string) []string {
				return []string{"jira", "planning", "report", "--jql", "project = ENG", "--csv", path, "--limit", "-1"}
			},
			effectPath: true,
		},
		{
			name: "quality report alias",
			args: func(path string) []string {
				return []string{"jira", "quality-report", "--jql", "project = ENG", "--csv", path, "--limit", "-1"}
			},
			effectPath: true,
		},
		{
			name: "structure pull issues",
			args: func(path string) []string {
				return []string{"jira", "structure", "pull-issues", "1", "--out", path, "--limit", "-1"}
			},
			effectPath: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests.Store(0)
			effect := filepath.Join(t.TempDir(), "must-not-exist")
			stdout, code := runCLI(t, jiraEnv(srv), tt.args(effect)...)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d; stdout=%q", code, exitUsage, stdout)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
			if tt.effectPath {
				if _, err := os.Stat(effect); !os.IsNotExist(err) {
					t.Fatalf("effect path exists or stat failed unexpectedly: %v", err)
				}
			}
		})
	}
}

func TestResolveInspectionMirrorRootPrecedence(t *testing.T) {
	start, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(start); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	base := t.TempDir()
	nearest := initializedMirror(t, filepath.Join(base, "nearest"))
	nested := filepath.Join(nearest, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".atl"), []byte("not a marker directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	argumentRoot := initializedMirror(t, filepath.Join(base, "argument"))
	intoRoot := initializedMirror(t, filepath.Join(base, "into"))
	envRoot := initializedMirror(t, filepath.Join(base, "environment"))
	fallbackRoot := initializedMirror(t, filepath.Join(base, "fallback"))

	tests := []struct {
		name    string
		args    []string
		into    string
		intoSet bool
		env     string
		want    string
	}{
		{name: "positional wins", args: []string{argumentRoot}, env: envRoot, want: argumentRoot},
		{name: "into wins", into: intoRoot, intoSet: true, env: envRoot, want: intoRoot},
		{name: "environment wins", env: envRoot, want: envRoot},
		{name: "nearest initialized wins", want: nearest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ATL_MIRROR_ROOT", tt.env)
			got, err := resolveInspectionMirrorRoot(tt.args, tt.into, tt.intoSet, fallbackRoot)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if tt.name == "nearest initialized wins" {
				want, err = filepath.EvalSymlinks(want)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got != want {
				t.Fatalf("root = %q, want %q", got, want)
			}
		})
	}

	lonely := filepath.Join(base, "lonely")
	if err := os.MkdirAll(lonely, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(lonely); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_MIRROR_ROOT", "")
	got, err := resolveInspectionMirrorRoot(nil, "", false, fallbackRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got != fallbackRoot {
		t.Fatalf("fallback root = %q, want %q", got, fallbackRoot)
	}

	fileRoot := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveInspectionMirrorRoot([]string{fileRoot}, "", false, fallbackRoot); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("file root error = %v, want ErrNotFound", err)
	}
}

func TestNearestInitializedMirrorRootIgnoresSymlinkMarker(t *testing.T) {
	base := t.TempDir()
	root := initializedMirror(t, filepath.Join(base, "root"))
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".atl"), filepath.Join(nested, ".atl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, ok, err := nearestInitializedMirrorRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != root {
		t.Fatalf("nearest root = %q, %v; want %q, true", got, ok, root)
	}
}

func TestInspectionRootPreservesUnexpectedFilesystemErrors(t *testing.T) {
	invalidPath := "invalid\x00path"
	if _, err := resolveInspectionMirrorRoot([]string{invalidPath}, "", false, "mirror"); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("explicit invalid path error = %v, want operational error", err)
	}
	if _, _, err := nearestInitializedMirrorRoot(invalidPath); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("nearest invalid path error = %v, want operational error", err)
	}
}

func TestInspectionLeavesRejectAmbiguousAndUninitializedRootsBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer srv.Close()

	tests := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{name: "confluence status", env: confEnv(srv), args: []string{"conf", "status"}},
		{name: "confluence snapshot", env: confEnv(srv), args: []string{"conf", "snapshot"}},
		{name: "jira status", env: jiraEnv(srv), args: []string{"jira", "status"}},
		{name: "jira snapshot", env: jiraEnv(srv), args: []string{"jira", "snapshot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" empty into", func(t *testing.T) {
			requests.Store(0)
			args := append(append([]string{}, tt.args...), "--into=", "--remote")
			stdout, code := runCLI(t, tt.env, args...)
			if code != exitUsage || stdout != "" {
				t.Fatalf("exit code = %d, stdout=%q; want usage and empty stdout", code, stdout)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})

		t.Run(tt.name+" ambiguous", func(t *testing.T) {
			requests.Store(0)
			root := initializedMirror(t, filepath.Join(t.TempDir(), "mirror"))
			args := append(append([]string{}, tt.args...), root, "--into", root, "--remote")
			stdout, code := runCLI(t, tt.env, args...)
			if code != exitUsage || stdout != "" {
				t.Fatalf("exit code = %d, stdout=%q; want usage and empty stdout", code, stdout)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})

		t.Run(tt.name+" uninitialized", func(t *testing.T) {
			requests.Store(0)
			missing := filepath.Join(t.TempDir(), "missing")
			args := append(append([]string{}, tt.args...), "--into", missing, "--remote")
			stdout, code := runCLI(t, tt.env, args...)
			if code != exitNotFound || stdout != "" {
				t.Fatalf("exit code = %d, stdout=%q; want not-found and empty stdout", code, stdout)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})
	}
}

func TestInspectionLeavesAcceptEquivalentExplicitRootForms(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "confluence status", args: []string{"conf", "status"}},
		{name: "confluence snapshot", args: []string{"conf", "snapshot"}},
		{name: "jira status", args: []string{"jira", "status"}},
		{name: "jira snapshot", args: []string{"jira", "snapshot"}},
	}
	for _, tt := range tests {
		for _, form := range []string{"positional", "into"} {
			t.Run(tt.name+" "+form, func(t *testing.T) {
				root := initializedMirror(t, filepath.Join(t.TempDir(), "mirror"))
				args := append([]string{}, tt.args...)
				if form == "positional" {
					args = append(args, root)
				} else {
					args = append(args, "--into", root)
				}
				if stdout, code := runCLI(t, nil, args...); code != exitOK {
					t.Fatalf("exit code = %d, want %d; stdout=%q", code, exitOK, stdout)
				}
			})
		}
	}
}

func initializedMirror(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
