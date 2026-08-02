package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/mirror"
)

func bindCLIMirrorBackend(t *testing.T, root, service, rawURL string) {
	t.Helper()
	digest, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.New(root).BindBackend(mirror.BackendBinding{Service: service, OriginSHA256: digest}); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorBackendBindCLIContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	rawURL := "https://backend.example.test/wiki"
	env := map[string]string{"ATL_CONFLUENCE_URL": rawURL}

	out, code := runCLI(t, env, "mirror", "backend", "bind", root, "--service", "confluence")
	if code != exitOK {
		t.Fatalf("preview exit = %d, out = %s", code, out)
	}
	var preview app.MirrorBackendBindResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	want, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || preview.Status != "would_bind" || preview.BackendSHA256 != want {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(root, ".atl", "backend-bindings.json")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote binding state: %v", err)
	}

	if out, code = runCLI(t, env, "mirror", "backend", "bind", root, "--service", "confluence", "--apply", "--expected-backend-sha256", want, "--confirm", "BIND"); code != exitOK {
		t.Fatalf("apply exit = %d, out = %s", code, out)
	}
	var applied app.MirrorBackendBindResult
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Mode != "apply" || applied.Status != "bound" {
		t.Fatalf("apply = %+v", applied)
	}

	out, code = runCLI(t, nil, "mirror", "backend", "status", root)
	if code != exitOK {
		t.Fatalf("status exit = %d, out = %s", code, out)
	}
	var status app.MirrorBackendStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Bindings) != 1 || status.Bindings[0].OriginSHA256 != want {
		t.Fatalf("status = %+v", status)
	}
}

func TestMirrorBackendBindCLIRequiresReviewGuards(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_JIRA_URL": "https://jira.example.test"}

	for name, args := range map[string][]string{
		"apply without hash": {"mirror", "backend", "bind", root, "--service", "jira", "--apply", "--confirm", "BIND"},
		"preview with guard": {"mirror", "backend", "bind", root, "--service", "jira", "--confirm", "BIND"},
		"missing service":    {"mirror", "backend", "bind", root},
		"two roots":          {"mirror", "backend", "bind", root, "--into", root, "--service", "jira"},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := runCLI(t, env, args...)
			if code != exitUsage && code != exitCheckFailed {
				t.Fatalf("exit = %d, out = %s", code, out)
			}
			if out != "" {
				t.Fatalf("failure stdout = %q", out)
			}
		})
	}
}

func TestMirrorBackendBindCLIReadOnlyPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, map[string]string{
		"ATL_JIRA_URL":  "https://jira.example.test",
		"ATL_READ_ONLY": "1",
	}, "mirror", "backend", "bind", root, "--service", "jira")
	if code != exitCheckFailed || out != "" {
		t.Fatalf("read-only preview exit = %d, out = %q", code, out)
	}
}

func TestMirrorBackendStatusTextIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, code := runCLI(t, nil, "mirror", "backend", "status", root, "-o", "text"); code != exitOK || strings.TrimSpace(out) != "no backend bindings" {
		t.Fatalf("empty status exit = %d, out = %q", code, out)
	}
}
