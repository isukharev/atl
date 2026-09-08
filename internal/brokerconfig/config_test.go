package brokerconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

func TestLoadUsesOnlyBoundedOwnerPrivateReferencedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Broker hosting requires POSIX owner evidence")
	}
	directory := t.TempDir()
	mustChmod(t, directory, 0o700)
	clearProxyEnvironment(t)
	cfg := testConfig()
	path := writeConfigFixture(t, directory, cfg)
	t.Setenv("ATL_JIRA_TOKEN", "ambient-client-token")
	material, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(material.AuthorityCredential) != "synthetic-authority-credential" || string(material.JiraCredential) != "synthetic-jira-credential" || material.ConfluenceCredential != nil {
		t.Fatalf("unexpected selected material: authority=%d jira=%d confluence=%d", len(material.AuthorityCredential), len(material.JiraCredential), len(material.ConfluenceCredential))
	}
	retained := material.JiraCredential
	material.Close()
	if len(material.JiraCredential) != 0 {
		t.Fatal("closed material retained credential reference")
	}
	for _, current := range retained {
		if current != 0 {
			t.Fatal("closed material did not clear credential bytes")
		}
	}
}

func TestLoadRejectsExpandedOrUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "same audience", mutate: func(cfg *Config) { cfg.AdminAudience = cfg.DataAudience }},
		{name: "public listener", mutate: func(cfg *Config) { cfg.DataListen = "0.0.0.0:8443" }},
		{name: "zero port", mutate: func(cfg *Config) { cfg.DataListen = "127.0.0.1:0" }},
		{name: "authority path", mutate: func(cfg *Config) { cfg.Authority.BaseURL = "https://authority.example.invalid/path" }},
		{name: "insecure backend", mutate: func(cfg *Config) { cfg.Jira.BaseURL = "http://jira.example.invalid" }},
		{name: "traversal", mutate: func(cfg *Config) { cfg.Jira.CredentialFile = "../credential" }},
		{name: "shared reference", mutate: func(cfg *Config) { cfg.Jira.CAFile = cfg.Authority.CAFile }},
		{name: "shared origin", mutate: func(cfg *Config) { cfg.Jira.BaseURL = cfg.Authority.BaseURL }},
		{name: "config as secret", mutate: func(cfg *Config) { cfg.Jira.CredentialFile = "broker.json" }},
		{name: "no backend", mutate: func(cfg *Config) { cfg.Jira = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearProxyEnvironment(t)
			directory := t.TempDir()
			mustChmod(t, directory, 0o700)
			cfg := testConfig()
			test.mutate(&cfg)
			path := writeConfigFixture(t, directory, cfg)
			if _, err := Load(path); !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLoadRejectsSymlinkedConfigurationParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Broker hosting requires POSIX owner evidence")
	}
	clearProxyEnvironment(t)
	real := t.TempDir()
	mustChmod(t, real, 0o700)
	path := writeConfigFixture(t, real, testConfig())
	aliasParent := t.TempDir()
	mustChmod(t, aliasParent, 0o700)
	alias := filepath.Join(aliasParent, "config")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(alias, filepath.Base(path))); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsLooseSymlinkSpecialAndOversizedInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are required by the Broker host")
	}
	for _, test := range []struct {
		name      string
		breakFile func(t *testing.T, directory, path string)
	}{
		{name: "loose config", breakFile: func(t *testing.T, _, path string) { t.Helper(); mustChmod(t, path, 0o644) }},
		{name: "loose secret", breakFile: func(t *testing.T, directory, _ string) {
			t.Helper()
			mustChmod(t, filepath.Join(directory, "jira.credential"), 0o640)
		}},
		{name: "symlink secret", breakFile: func(t *testing.T, directory, _ string) {
			t.Helper()
			target := filepath.Join(directory, "real.credential")
			mustWrite(t, target, []byte("synthetic-jira-credential"), 0o600)
			if err := os.Remove(filepath.Join(directory, "jira.credential")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, "jira.credential")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized secret", breakFile: func(t *testing.T, directory, _ string) {
			t.Helper()
			mustWrite(t, filepath.Join(directory, "jira.credential"), []byte(strings.Repeat("x", int(MaxCredentialBytes)+1)), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearProxyEnvironment(t)
			directory := t.TempDir()
			mustChmod(t, directory, 0o700)
			path := writeConfigFixture(t, directory, testConfig())
			test.breakFile(t, directory, path)
			if _, err := Load(path); !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLoadRejectsAmbientProxyBeforeReadingConfiguration(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example.invalid")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err=%v", err)
	}
}

func testConfig() Config {
	return Config{
		SchemaVersion: 1,
		BrokerID:      "broker-1", DataAudience: "broker-data", AdminAudience: "broker-admin",
		DataListen: "127.0.0.1:8443", AdminListen: "127.0.0.1:8444",
		TLS:       TLSFiles{CertificateFile: "server.crt", PrivateKeyFile: "server.key"},
		Authority: Authority{BaseURL: "https://authority.example.invalid", IssuerSHA256: strings.Repeat("a", 64), CredentialFile: "authority.credential", CAFile: "authority.ca"},
		Jira:      &Backend{BaseURL: "https://jira.example.invalid", WorkloadBackendID: "jira-primary", CredentialFile: "jira.credential", CAFile: "jira.ca"},
	}
}

func writeConfigFixture(t *testing.T, directory string, cfg Config) string {
	t.Helper()
	files := map[string][]byte{
		"server.crt": []byte("certificate"), "server.key": []byte("private-key"),
		"authority.credential": []byte("synthetic-authority-credential"), "authority.ca": []byte("authority-ca"),
		"jira.credential": []byte("synthetic-jira-credential"), "jira.ca": []byte("jira-ca"),
		"confluence.credential": []byte("synthetic-confluence-credential"), "confluence.ca": []byte("confluence-ca"),
	}
	for name, body := range files {
		mustWrite(t, filepath.Join(directory, name), body, 0o600)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "broker.json")
	mustWrite(t, path, body, 0o600)
	return path
}

func mustWrite(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func clearProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "")
	}
}

func FuzzBrokerConfigEnvelope(f *testing.F) {
	valid, _ := json.Marshal(testConfig())
	f.Add(valid)
	f.Add([]byte(`{"schema_version":1,"unknown":true}`))
	f.Add([]byte(`{"schema_version":1,"schema_version":1}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > int(MaxConfigBytes) {
			t.Skip()
		}
		var cfg Config
		if strictjson.DecodeExact(body, brokercontract.MaxCanonicalDepth, &cfg) == nil && validate(cfg) == nil {
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip Config
			if strictjson.DecodeExact(encoded, brokercontract.MaxCanonicalDepth, &roundTrip) != nil || validate(roundTrip) != nil {
				t.Fatal("valid Broker configuration did not round trip")
			}
		}
	})
}
