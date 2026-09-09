package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/domain"
)

func brokerCanonicalTempDir(t *testing.T) string {
	t.Helper()
	// Resolve only synthetic storage: Darwin's temporary directory can use a
	// system symlink alias, while production journal paths remain no-follow.
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func configureBrokerCommentFixture(t *testing.T, path string, policyBackend string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg brokerconfig.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if policyBackend == "matched" {
		policyBackend, err = backendid.OriginSHA256(cfg.Jira.BaseURL)
		if err != nil {
			t.Fatal(err)
		}
	}
	policy := []byte(`{"schema_version":1,"backend":{"jira_sha256":"` + policyBackend + `"},"rules":[{"id":"one-issue","effect":"allow","verbs":["comment"],"resource":{"service":"jira","kind":"issue","id":"101","project":"PROJ","key":"PROJ-1"}}]}`)
	digest := sha256.Sum256(policy)
	cfg.JiraComment = &brokerconfig.JiraComment{
		QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1,
		JournalDirectory:     "comment-journal", JournalRecords: 2, JournalReservedBytes: 16 << 20,
		LocalPolicyFile: "comment-policy.json", LocalPolicySHA256: hex.EncodeToString(digest[:]),
	}
	body, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeBrokerFile(t, filepath.Dir(path), "comment-policy.json", policy)
	writeBrokerFile(t, filepath.Dir(path), filepath.Base(path), body)
}

func TestBrokerCommentRuntimeOpensExistingJournalBeforeReadiness(t *testing.T) {
	path, authorityCalls, jiraCalls := brokerRuntimeFixture(t)
	configureBrokerCommentFixture(t, path, "matched")
	if _, err := LoadBrokerRuntime(path, "test", io.Discard); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("comment configuration accepted without operator flag: %v", err)
	}
	if _, err := LoadBrokerRuntimeWithJiraComments(path, "test", io.Discard, true); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("missing journal silently created: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "comment-journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed load created storage: %v", err)
	}
	if err := InitializeBrokerJournal(path); err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadBrokerRuntimeWithJiraComments(path, "test", io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if runtime.journal == nil || runtime.host.Readiness() == "ready" {
		t.Fatal("journal missing or host ready before Run")
	}
	if _, err := LoadBrokerRuntimeWithJiraComments(path, "test", io.Discard, true); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("second writer admitted: %v", err)
	}
	retained := runtime.material.JiraCommentPolicy
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !runtime.host.Drained() || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("drained runtime retained private policy")
	}
	reopened, err := LoadBrokerRuntimeWithJiraComments(path, "test", io.Discard, true)
	if err != nil {
		t.Fatal("drain did not release journal lock", err)
	}
	reopened.Close()
	if authorityCalls.Load() != 0 || jiraCalls.Load() != 0 {
		t.Fatalf("startup/recovery contacted a backend: authority=%d Jira=%d", authorityCalls.Load(), jiraCalls.Load())
	}
}

func TestBrokerCommentPolicyRequiresPinnedBackendAndFreezesBytes(t *testing.T) {
	for _, backend := range []string{"", "sha256:" + strings.Repeat("a", 64), "matched"} {
		t.Run(backend, func(t *testing.T) {
			path, authorityCalls, jiraCalls := brokerRuntimeFixture(t)
			configureBrokerCommentFixture(t, path, backend)
			material, err := brokerconfig.LoadWithJiraComments(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer material.Close()
			policy, err := brokerCommentPolicy(material)
			if backend != "matched" {
				if policy != nil || !errors.Is(err, domain.ErrConfig) {
					t.Fatal("unbound policy accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clear(material.JiraCommentPolicy)
			request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", ID: "101", Key: "PROJ-1", Project: "PROJ"}}}
			if err := policy.Preflight(request); err != nil {
				t.Fatal(err)
			}
			if _, err := policy.Authorize(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			request.Targets[0].ID = "102"
			if _, err := policy.Authorize(t.Context(), request); err == nil {
				t.Fatal("frozen policy lost exact issue boundary")
			}
			if authorityCalls.Load() != 0 || jiraCalls.Load() != 0 {
				t.Fatal("policy parsing contacted a backend")
			}
		})
	}
}

type failingBrokerJournalClose struct{ io.Closer }

func (closer failingBrokerJournalClose) Close() error {
	return errors.Join(closer.Closer.Close(), errors.New("synthetic close failure"))
}

func TestBrokerCommentRuntimeCloseFailureCannotReportSuccess(t *testing.T) {
	path, _, _ := brokerRuntimeFixture(t)
	configureBrokerCommentFixture(t, path, "matched")
	if err := InitializeBrokerJournal(path); err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadBrokerRuntimeWithJiraComments(path, "test", io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	journalConfig := *runtime.material.Journal
	runtime.journal = failingBrokerJournalClose{runtime.journal}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.Run(ctx); !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("close error lost or leaked: %v", err)
	}
	identity, limits, err := brokerJournalParameters(&journalConfig)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := brokerjournal.Open(journalConfig.Directory, identity, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
}
