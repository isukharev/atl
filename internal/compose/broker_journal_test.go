//go:build linux || darwin

package compose

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/domain"
)

func TestInitializeBrokerJournalIsLocalOnlyAndCreateOnly(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	authority := httptest.NewTLSServer(handler)
	t.Cleanup(authority.Close)
	jira := httptest.NewTLSServer(handler)
	t.Cleanup(jira.Close)
	cfg := brokerconfig.Config{
		SchemaVersion: 1, BrokerID: "broker-1", DataAudience: "broker-data", AdminAudience: "broker-admin",
		DataListen: "127.0.0.1:8443", AdminListen: "127.0.0.1:8444",
		TLS:         brokerconfig.TLSFiles{CertificateFile: "absent.crt", PrivateKeyFile: "absent.key"},
		Authority:   brokerconfig.Authority{BaseURL: authority.URL, IssuerSHA256: strings.Repeat("a", 64), CredentialFile: "absent-authority.credential", CAFile: "absent-authority.ca"},
		Jira:        &brokerconfig.Backend{BaseURL: jira.URL, WorkloadBackendID: "jira-primary", CredentialFile: "absent-jira.credential", CAFile: "absent-jira.ca"},
		JiraComment: &brokerconfig.JiraComment{QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1, JournalDirectory: "journal", JournalRecords: 2, JournalReservedBytes: 8 << 20, LocalPolicyFile: "absent-policy.json", LocalPolicySHA256: strings.Repeat("b", 64)},
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeBrokerFile(t, directory, "broker.json", encoded)
	path := filepath.Join(directory, "broker.json")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.invalid:8080")
	t.Setenv("ATL_POLICY", "malformed ambient policy")
	if err := InitializeBrokerJournal(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := brokerconfig.LoadJournalConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, limits, err := brokerJournalParameters(loaded)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := brokerjournal.Open(loaded.Directory, identity, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := InitializeBrokerJournal(path); err == nil || strings.Contains(err.Error(), directory) {
		t.Fatal("initializer adopted existing storage or leaked its path")
	}
	journal, err = brokerjournal.Open(loaded.Directory, identity, limits)
	if err != nil {
		t.Fatal("refused repeated initialization changed existing storage")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("local initialization contacted a configured backend")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatal("initializer created unrelated material")
	}
}
