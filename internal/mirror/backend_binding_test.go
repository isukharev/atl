package mirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func testBackendBinding(t *testing.T, service, raw string) BackendBinding {
	t.Helper()
	digest, err := backendid.OriginSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	return BackendBinding{Service: service, OriginSHA256: digest}
}

func TestBackendBindingLegacyAndCompareAndSet(t *testing.T) {
	m := New(t.TempDir())
	want := testBackendBinding(t, "confluence", "https://one.example.test/wiki")
	if got, ok, err := m.BackendBinding("confluence"); err != nil || ok || got.OriginSHA256 != "" {
		t.Fatalf("legacy binding = %+v, %t, %v", got, ok, err)
	}
	created, err := m.BindBackend(want)
	if err != nil || !created {
		t.Fatalf("BindBackend = %t, %v", created, err)
	}
	created, err = m.BindBackend(want)
	if err != nil || created {
		t.Fatalf("idempotent BindBackend = %t, %v", created, err)
	}
	if err := m.RequireBackendBinding(want); err != nil {
		t.Fatal(err)
	}
	other := testBackendBinding(t, "confluence", "https://two.example.test/wiki")
	if _, err := m.BindBackend(other); !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), "example") || strings.Contains(err.Error(), other.OriginSHA256) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestBindBackendIfFreshRefusesServiceEvidence(t *testing.T) {
	for _, tc := range []struct {
		service string
		ext     string
		path    string
	}{
		{service: "confluence", ext: ".csf", path: "space/page.csf"},
		{service: "confluence", ext: ".csf", path: "space/page.meta.json"},
		{service: "jira", ext: ".wiki", path: ".atl/pending/jira/P-1.json"},
		{service: "jira", ext: ".wiki", path: "space/page.jira-macros.json"},
		{service: "jira", ext: ".wiki", path: "PROJ/PROJ-1.json"},
		{service: "jira", ext: ".wiki", path: "PROJ/PROJ-1.assets/image.png"},
	} {
		t.Run(tc.service, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			m := New(root)
			want := testBackendBinding(t, tc.service, "https://backend.example.test")
			if _, err := m.BindBackendIfFresh(want, tc.ext); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("BindBackendIfFresh error = %v", err)
			}
			if _, ok, err := m.BackendBinding(tc.service); err != nil || ok {
				t.Fatalf("binding persisted after refusal: ok=%t err=%v", ok, err)
			}
			if created, err := m.BindBackend(want); err != nil || !created {
				t.Fatalf("explicit bind = %t, %v", created, err)
			}
		})
	}
}

func TestBindBackendIfFreshWritesPrivateStrictState(t *testing.T) {
	m := New(t.TempDir())
	want := testBackendBinding(t, "jira", "https://jira.example.test")
	if created, err := m.BindBackendIfFresh(want, ".wiki"); err != nil || !created {
		t.Fatalf("BindBackendIfFresh = %t, %v", created, err)
	}
	info, err := os.Stat(m.backendBindingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	b, err := os.ReadFile(m.backendBindingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "jira.example.test") || !strings.Contains(string(b), want.OriginSHA256) {
		t.Fatalf("binding bytes expose or omit identity: %s", b)
	}
}

func TestBackendBindingsConcurrentServicesSurvive(t *testing.T) {
	m := New(t.TempDir())
	bindings := []BackendBinding{
		testBackendBinding(t, "jira", "https://jira.example.test"),
		testBackendBinding(t, "confluence", "https://conf.example.test/wiki"),
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(bindings))
	for _, binding := range bindings {
		binding := binding
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.BindBackend(binding)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.BackendBindings()
	if err != nil || len(got) != 2 || got[0].Service != "confluence" || got[1].Service != "jira" {
		t.Fatalf("bindings = %+v, %v", got, err)
	}
}

func TestBackendBindingLockRetriesOnlyTransientMissingPath(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		attempt int
		retry   bool
	}{
		{name: "first missing", err: &os.PathError{Op: "openat", Path: ".atl/backend-bindings.lock", Err: fs.ErrNotExist}, retry: true},
		{name: "last missing", err: fs.ErrNotExist, attempt: backendBindingsLockAttempts - 1},
		{name: "permission", err: fs.ErrPermission},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRetryBackendBindingLock(tc.err, tc.attempt)
			if got != tc.retry {
				t.Fatalf("retry = %t, want %t", got, tc.retry)
			}
		})
	}
}

func TestBackendBindingsSurviveLegacyMirrorWriters(t *testing.T) {
	m := New(t.TempDir())
	bindings := []BackendBinding{
		testBackendBinding(t, "confluence", "https://conf.example.test/wiki"),
		testBackendBinding(t, "jira", "https://jira.example.test"),
	}
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if _, err := m.BindBackend(binding); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(SyncState{ID: "page-1", Version: 1, Hash: Hash([]byte("page")), Path: "DOC/page.csf"})
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	state, view, base, artifacts := registrationFixture(m.Root)
	if err := m.RegisterNew(state, view, ".wiki", base, artifacts); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if err := m.RequireBackendBinding(binding); err != nil {
			t.Fatalf("legacy writer lost %s binding: %v", binding.Service, err)
		}
	}
}

func TestBackendBindingStrictDecode(t *testing.T) {
	valid := `{"schema_version":1,"services":{"jira":"sha256:` + strings.Repeat("a", 64) + `"}}`
	for name, body := range map[string]string{
		"future":    strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"unknown":   strings.Replace(valid, `"services"`, `"extra":true,"services"`, 1),
		"duplicate": strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"uppercase": strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"empty":     `{"schema_version":1,"services":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := New(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(m.backendBindingsPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(m.backendBindingsPath(), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.BackendBindings(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("BackendBindings error = %v", err)
			}
		})
	}
}

func TestBackendBindingRejectsSymlinkedState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := os.MkdirAll(filepath.Dir(m.backendBindingsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "binding.json")
	if err := os.WriteFile(outside, []byte(`{"schema_version":1,"services":{"jira":"sha256:`+strings.Repeat("a", 64)+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, m.backendBindingsPath()); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := m.BackendBindings(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("BackendBindings error = %v", err)
	}
}
