package mcpserver

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

func TestProductionMCPConfluenceMetadataUsesBrokerWithoutPATStore(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", directory)
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMCPBrokerFile(t, directory, "session.json", []byte(`{"schema_version":1,"credential":"synthetic-workload-credential","execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"}`))
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
		if err != nil || invocation.Operation != domain.BrokerOperationConfluencePageRead || invocation.Arguments.ConfluencePageRead.Projection != domain.BrokerConfluenceProjectionMetadata {
			t.Fatalf("invocation=%+v err=%v", invocation, err)
		}
		digest, _ := brokercontract.ArgumentsSHA256(invocation)
		encoded, _ := brokercontract.EncodeConfluencePageReadResultV1(domain.BrokerConfluencePageReadResult{
			SchemaVersion: 1, ArgumentsSHA256: digest, PageID: "42", Type: "page", Space: "DOCS", Version: 3,
			Title: "Synthetic page", Updated: "2026-09-08T12:00:00Z", Projection: domain.BrokerConfluenceProjectionMetadata, Complete: true,
		})
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	writeMCPBrokerFile(t, directory, "broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	configBody, _ := json.Marshal(map[string]any{
		"connection_mode": "broker", "jira_list_views": map[string]any{},
		"broker": map[string]any{"base_url": server.URL, "broker_id": "broker-1", "audience": "atl-broker", "ca_file": filepath.Join(directory, "broker.ca"), "confluence_session_file": filepath.Join(directory, "session.json")},
	})
	writeMCPBrokerFile(t, directory, "config.json", configBody)
	client, closeSessions := connectTestClient(t, NewForService("test", ProductionDependencies("test"), ServiceConfluence))
	defer closeSessions()
	result := callToolOK(t, client, "confluence_page_meta", map[string]any{"reference": "42"})
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), `"restriction_state":"unknown"`) || !strings.Contains(string(encoded), `"title":"Synthetic page"`) {
		t.Fatalf("content=%s err=%v", encoded, err)
	}
	if protocolCalls.Load() != 1 || executeCalls.Load() != 1 {
		t.Fatalf("protocol=%d execute=%d", protocolCalls.Load(), executeCalls.Load())
	}
}

func writeMCPBrokerFile(t *testing.T, directory, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
