package cli

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestOrdinaryCLIExactReadsUseBrokerWithoutPATStore(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCLIPrivateFile(t, directory, "session.json", []byte(`{"schema_version":1,"credential":"synthetic-workload-credential","execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"}`))
	var protocolCalls, executeCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer synthetic-workload-credential" {
			t.Fatal("unexpected Broker credential")
		}
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
		}
	}))
	defer server.Close()
	writeCLIPrivateFile(t, directory, "broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	cfg := map[string]any{
		"connection_mode": "broker", "jira_list_views": map[string]any{},
		"broker": map[string]any{"base_url": server.URL, "broker_id": "broker-1", "audience": "atl-broker", "ca_file": filepath.Join(directory, "broker.ca"), "jira_session_file": filepath.Join(directory, "session.json"), "confluence_session_file": filepath.Join(directory, "session.json")},
	}
	encodedConfig, _ := json.Marshal(cfg)
	writeCLIPrivateFile(t, directory, "config.json", encodedConfig)
	env := map[string]string{"ATL_CONFIG_DIR": directory}
	stdout, _, err := executeCLIRaw(t, env, "jira", "issue", "get", "EXAMPLE-1", "--fields", "summary")
	if err != nil || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, "Synthetic summary") {
		t.Fatalf("jira stdout=%q err=%v", stdout, err)
	}
	stdout, _, err = executeCLIRaw(t, env, "conf", "page", "get", "--id", "42", "--format", "csf")
	if err != nil || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, "<p>Synthetic</p>") || !strings.Contains(stdout, `"url": ""`) {
		t.Fatalf("confluence stdout=%q err=%v", stdout, err)
	}
	if protocolCalls.Load() != 2 || executeCalls.Load() != 2 {
		t.Fatalf("protocol=%d execute=%d", protocolCalls.Load(), executeCalls.Load())
	}
	stdout, _, err = executeCLIRaw(t, env, "auth", "status")
	if err != nil || !strings.Contains(stdout, `"mode": "broker"`) || !strings.Contains(stdout, "broker_session_file") {
		t.Fatalf("auth status=%q err=%v", stdout, err)
	}
}

func writeCLIPrivateFile(t *testing.T, directory, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
