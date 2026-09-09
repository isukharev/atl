package brokerconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func commentConfigFixture() (Config, []byte) {
	cfg := testConfig()
	policy := []byte(`{"schema_version":1,"rules":[]}`)
	digest := sha256.Sum256(policy)
	cfg.JiraComment = &JiraComment{
		QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1,
		JournalDirectory:     "comment-journal", JournalRecords: 128,
		JournalReservedBytes: 64 << 20, LocalPolicyFile: "comment-policy.json",
		LocalPolicySHA256: hex.EncodeToString(digest[:]),
	}
	return cfg, policy
}

func TestCommentJournalConfigurationReadsNoReferencedMaterial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Broker hosting requires POSIX owner evidence")
	}
	directory := t.TempDir()
	mustChmod(t, directory, 0o700)
	cfg, _ := commentConfigFixture()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "broker.json")
	mustWrite(t, path, encoded, 0o600)
	// No certificate, credential, policy or journal exists. Ambient proxy and
	// ordinary client policy must be irrelevant to this local-only read.
	t.Setenv("HTTPS_PROXY", "http://proxy.example.invalid:8080")
	t.Setenv("ATL_POLICY_FILE", filepath.Join(directory, "absent-ambient-policy"))
	loaded, err := LoadJournalConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := backendid.OriginSHA256(cfg.Jira.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BrokerID != cfg.BrokerID || loaded.Directory != filepath.Join(directory, "comment-journal") || loaded.Records != 128 || loaded.ReservedBytes != 64<<20 || loaded.Backend != (domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.TrimPrefix(origin, backendid.Prefix), WorkloadBackendID: cfg.Jira.WorkloadBackendID}) {
		t.Fatal("journal configuration lost an exact deployment or storage binding")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("configuration read created referenced material")
	}
}

func TestCommentRuntimeRequiresBothOptInsAndClearsPinnedPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Broker hosting requires POSIX owner evidence")
	}
	clearProxyEnvironment(t)
	directory := t.TempDir()
	mustChmod(t, directory, 0o700)
	cfg, policy := commentConfigFixture()
	path := writeConfigFixture(t, directory, cfg)
	mustWrite(t, filepath.Join(directory, cfg.JiraComment.LocalPolicyFile), policy, 0o600)
	if _, err := Load(path); !errors.Is(err, domain.ErrConfig) {
		t.Fatal("read-only loader accepted comment configuration")
	}
	material, err := LoadWithJiraComments(path, true)
	if err != nil {
		t.Fatal(err)
	}
	retained := material.JiraCommentPolicy
	if string(retained) != string(policy) || material.Journal == nil {
		t.Fatal("missing pinned comment material")
	}
	material.Close()
	if material.Journal != nil || material.JiraCommentPolicy != nil {
		t.Fatal("closed material retains comment state")
	}
	for _, value := range retained {
		if value != 0 {
			t.Fatal("closed policy was not cleared")
		}
	}
	plain := writeConfigFixture(t, directory, testConfig())
	if _, err := LoadWithJiraComments(plain, true); !errors.Is(err, domain.ErrConfig) {
		t.Fatal("mutation flag accepted without configuration")
	}
	if _, err := LoadJournalConfiguration(plain); !errors.Is(err, domain.ErrConfig) {
		t.Fatal("initializer accepted absent comment configuration")
	}
}

func TestCommentConfigurationRejectsUnsafeOrExpandedScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"profile", func(c *Config) { c.JiraComment.QualificationProfile = "atomic_current_project" }},
		{"missing jira", func(c *Config) { c.Jira = nil }},
		{"zero records", func(c *Config) { c.JiraComment.JournalRecords = 0 }},
		{"excess records", func(c *Config) { c.JiraComment.JournalRecords = 1025 }},
		{"zero bytes", func(c *Config) { c.JiraComment.JournalReservedBytes = 0 }},
		{"excess bytes", func(c *Config) { c.JiraComment.JournalReservedBytes = 256<<20 + 1 }},
		{"journal traversal", func(c *Config) { c.JiraComment.JournalDirectory = "../journal" }},
		{"policy traversal", func(c *Config) { c.JiraComment.LocalPolicyFile = "../policy" }},
		{"journal credential alias", func(c *Config) { c.JiraComment.JournalDirectory = c.Jira.CredentialFile }},
		{"policy journal alias", func(c *Config) { c.JiraComment.LocalPolicyFile = c.JiraComment.JournalDirectory }},
		{"missing pin", func(c *Config) { c.JiraComment.LocalPolicySHA256 = "" }},
		{"noncanonical pin", func(c *Config) { c.JiraComment.LocalPolicySHA256 = strings.Repeat("A", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, _ := commentConfigFixture()
			test.change(&cfg)
			if err := validate(cfg); !errors.Is(err, domain.ErrConfig) {
				t.Fatal("unsafe comment configuration accepted")
			}
		})
	}
	if cfg, _ := commentConfigFixture(); validate(cfg) != nil {
		t.Fatal("valid control rejected")
	}
}

func TestCommentPolicyPinAndPrivateReadRefuseAtOwningBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Broker hosting requires POSIX owner evidence")
	}
	clearProxyEnvironment(t)
	for _, kind := range []string{"changed bytes", "oversize", "loose file", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			mustChmod(t, directory, 0o700)
			cfg, policy := commentConfigFixture()
			path := writeConfigFixture(t, directory, cfg)
			policyPath := filepath.Join(directory, cfg.JiraComment.LocalPolicyFile)
			switch kind {
			case "changed bytes":
				mustWrite(t, policyPath, append(policy, '\n'), 0o600)
			case "oversize":
				mustWrite(t, policyPath, []byte(strings.Repeat(" ", int(maxCommentPolicyBytes)+1)), 0o600)
			case "loose file":
				mustWrite(t, policyPath, policy, 0o600)
				mustChmod(t, policyPath, 0o644)
			case "symlink":
				target := filepath.Join(directory, "policy-target")
				mustWrite(t, target, policy, 0o600)
				if err := os.Symlink(target, policyPath); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadWithJiraComments(path, true); !errors.Is(err, domain.ErrConfig) || strings.Contains(err.Error(), directory) {
				t.Fatal("unsafe pinned policy was accepted or leaked its path")
			}
		})
	}
}
