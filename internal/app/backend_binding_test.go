package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func initializedMirrorRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMirrorBackendBindPreviewApplyAndStatus(t *testing.T) {
	root := initializedMirrorRoot(t)
	rawURL := "https://backend.example.test/wiki/"
	want, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		t.Fatal(err)
	}

	preview, err := PreviewMirrorBackendBind(root, "confluence", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || preview.Status != "would_bind" || preview.BackendSHA256 != want {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(root, ".atl", "backend-bindings.json")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote binding state: %v", err)
	}

	result, err := ApplyMirrorBackendBind(root, "confluence", rawURL, want, "BIND")
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "apply" || result.Status != "bound" || result.BackendSHA256 != want {
		t.Fatalf("apply = %+v", result)
	}

	status, err := InspectMirrorBackends(root)
	if err != nil {
		t.Fatal(err)
	}
	if status.SchemaVersion != mirrorBackendBindingSchemaVersion || len(status.Bindings) != 1 || status.Bindings[0].Service != "confluence" || status.Bindings[0].OriginSHA256 != want {
		t.Fatalf("status = %+v", status)
	}

	again, err := ApplyMirrorBackendBind(root, "confluence", rawURL, want, "BIND")
	if err != nil || again.Status != "already_bound" {
		t.Fatalf("idempotent apply = %+v, %v", again, err)
	}
}

func TestMirrorBackendBindGuards(t *testing.T) {
	root := initializedMirrorRoot(t)
	rawURL := "https://backend.example.test"
	want, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		t.Fatal(err)
	}

	for name, guard := range map[string][2]string{
		"missing hash":  {"", "BIND"},
		"wrong hash":    {strings.Repeat("a", 64), "BIND"},
		"wrong confirm": {want, "APPLY"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyMirrorBackendBind(root, "jira", rawURL, guard[0], guard[1])
			if name == "wrong confirm" {
				if !errors.Is(err, domain.ErrUsage) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMirrorBackendBindRejectsReplacementWithoutIdentityDisclosure(t *testing.T) {
	root := initializedMirrorRoot(t)
	first := "https://one.example.test/wiki"
	firstHash, err := backendid.OriginSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMirrorBackendBind(root, "confluence", first, firstHash, "BIND"); err != nil {
		t.Fatal(err)
	}

	second := "https://two.example.test/wiki"
	_, err = PreviewMirrorBackendBind(root, "confluence", second)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), firstHash) {
		t.Fatalf("error disclosed backend identity: %v", err)
	}
}

func TestMirrorBackendInspectionRequiresInitializedRoot(t *testing.T) {
	if _, err := InspectMirrorBackends(t.TempDir()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestMirrorBackendInspectionRejectsSymlinkedStateRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".atl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := InspectMirrorBackends(root); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}
