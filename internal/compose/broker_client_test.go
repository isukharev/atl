package compose

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerServiceCompositionNeverReadsOrdinaryPATStore(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", directory)
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(directory, "session.json")
	writeBrokerFile(t, directory, "session.json", []byte(`{"schema_version":1,"credential":"synthetic-workload-credential","execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"}`))
	var protocolCalls, executeCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		if request.URL.Path == brokertransport.ProtocolPath {
			protocolCalls.Add(1)
			protocol, _ := brokertransport.ProtocolV1("broker-1", "atl-broker")
			body, _ := brokertransport.EncodeProtocolV1(protocol)
			_, _ = writer.Write(body)
			return
		}
		executeCalls.Add(1)
		body, _ := io.ReadAll(io.LimitReader(request.Body, 64<<10))
		invocation, err := brokercontract.DecodeRequestV1(body)
		if err != nil {
			t.Fatal(err)
		}
		digest, _ := brokercontract.ArgumentsSHA256(invocation)
		switch invocation.Operation {
		case domain.BrokerOperationJiraIssueRead:
			encoded, _ := brokercontract.EncodeJiraIssueReadResultV1(domain.BrokerJiraIssueReadResult{
				SchemaVersion: 1, ArgumentsSHA256: digest, IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "2026-09-08T12:00:00Z",
				Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Synthetic summary"}}, Complete: true,
			})
			_, _ = writer.Write(encoded)
		case domain.BrokerOperationConfluencePageRead:
			encoded, _ := brokercontract.EncodeConfluencePageReadResultV1(domain.BrokerConfluencePageReadResult{
				SchemaVersion: 1, ArgumentsSHA256: digest, PageID: "42", Type: "page", Space: "DOCS", Version: 3, Title: "Synthetic page", Updated: "2026-09-08T12:00:00Z",
				Projection: domain.BrokerConfluenceProjectionStorage, Storage: []byte("<p>Synthetic</p>"), StoragePresent: true, Complete: true,
			})
			_, _ = writer.Write(encoded)
		default:
			t.Fatalf("operation=%q", invocation.Operation)
		}
	}))
	defer server.Close()
	caPath := filepath.Join(directory, "broker.ca")
	writeBrokerFile(t, directory, "broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	cfg := &config.Config{ConnectionMode: config.ConnectionModeBroker, Broker: &config.BrokerClientConfig{
		BaseURL: server.URL, BrokerID: "broker-1", Audience: "atl-broker", CAFile: caPath,
		JiraSessionFile: sessionPath, ConfluenceSessionFile: sessionPath,
	}}
	jira, err := NewJira(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := jira.IssueResolved(t.Context(), "EXAMPLE-1", []string{"summary"})
	if err != nil || issue.Summary != "Synthetic summary" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	confluence, err := NewConfluence(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	page, err := confluence.Get(t.Context(), "42", "csf")
	if err != nil || string(page.Body) != "<p>Synthetic</p>" || page.URL != "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if protocolCalls.Load() != 2 || executeCalls.Load() != 2 {
		t.Fatalf("protocol=%d execute=%d", protocolCalls.Load(), executeCalls.Load())
	}
}

func TestBrokerDoctorProjectionNeverReadsOrdinaryPATStore(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", directory)
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	configBody := []byte(`{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_session_file":"/runtime/jira.json"},"jira_list_views":{}}`)
	if err := os.WriteFile(filepath.Join(directory, "config.json"), configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	deps := doctorDependencies(app.DoctorServiceJira, func(string) error { return nil })
	if deps.Config.ConnectionMode != config.ConnectionModeBroker || deps.Credentials.Store.Status != "not_used" || deps.Credentials.Jira.Source != "broker_session_file" || deps.Credentials.Jira.Status != "available" {
		t.Fatalf("deps=%+v", deps)
	}
	if deps.Token != nil || deps.Reader != nil {
		t.Fatal("Broker doctor retained a direct PAT or backend reader path")
	}
	result, err := RunDoctor(t.Context(), app.DoctorOptions{Remote: true, Service: app.DoctorServiceJira})
	if err != nil || !result.Healthy || result.Services.Jira.Remote.Status != "skipped" || result.Services.Jira.Remote.Reason != "broker_operation_unsupported" {
		t.Fatalf("doctor=%+v err=%v", result, err)
	}
}

func TestInvalidConnectionModeDoctorDoesNotReadPATStore(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", directory)
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(`{"connection_mode":"invalid-mode","jira_list_views":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := doctorDependencies(app.DoctorServiceJira, func(string) error { return nil })
	if deps.Config.ConnectionMode != "invalid" || deps.Credentials.Store.Status != "not_used" || deps.Token != nil || deps.Reader != nil {
		t.Fatalf("deps=%+v", deps)
	}
}

func TestMalformedBrokerConfigDoctorDoesNotReadPATStore(t *testing.T) {
	for _, body := range []string{
		`{"connection_mode":"broker","broker":123}`,
		`{"connection_mode":123,"broker":{"base_url":"https://broker.example.test"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("ATL_CONFIG_DIR", directory)
			if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			deps := doctorDependencies(app.DoctorServiceJira, func(string) error { return nil })
			if deps.Config.ConnectionMode != "invalid" || deps.Config.Status != "invalid" || deps.Credentials.Store.Status != "not_used" || deps.Token != nil || deps.Reader != nil {
				t.Fatalf("deps=%+v", deps)
			}
		})
	}
}
