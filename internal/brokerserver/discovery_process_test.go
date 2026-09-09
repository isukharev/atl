package brokerserver

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestSelectedMCPProcessRefreshesBrokerDiscoveryAndNeverGrantsAdmission(t *testing.T) {
	binary := buildSelectedATLBinary(t, "")
	f := newBrokerServerFixture(t, "Synthetic", "", nil, []byte("synthetic-upstream-pat"))
	var fixtureMu sync.Mutex
	withFixture := func(action func()) { fixtureMu.Lock(); defer fixtureMu.Unlock(); action() }
	counts := func() (int, int) {
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		return f.authenticator.calls, f.authorizer.discoveryCalls
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		withFixture(func() { f.handler.ServeHTTP(writer, request) })
	}))
	t.Cleanup(server.Close)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0700); err != nil {
		t.Fatal(err)
	}
	write("broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	writeSession := func(epoch string) {
		body, _ := json.Marshal(map[string]any{"schema_version": 1, "credential": "synthetic-workload-credential", "execution_id": "execution-1", "execution_epoch": epoch, "authority_revision": "revision-1"})
		write("session.json", body)
	}
	writeSession("epoch-1")
	configBody, _ := json.Marshal(map[string]any{"connection_mode": "broker", "jira_list_views": map[string]any{}, "broker": map[string]any{"base_url": server.URL, "broker_id": "broker-1", "audience": "atl-broker", "ca_file": filepath.Join(directory, "broker.ca"), "jira_session_file": filepath.Join(directory, "session.json")}})
	write("config.json", configBody)
	environment := []string{"PATH=" + os.Getenv("PATH"), "ATL_CONFIG_DIR=" + directory, "ATL_NO_UPDATE=1", "ATL_READ_ONLY=1"}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--service", "jira")
	command.Env = environment
	client := mcp.NewClient(&mcp.Implementation{Name: "discovery-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListResources(ctx, nil)
	authCalls, _ := counts()
	if err != nil || listed == nil || authCalls != 0 {
		t.Fatalf("listing=%+v err=%v auth=%d", listed, err, authCalls)
	}
	var resourceURIs []string
	for _, resource := range listed.Resources {
		resourceURIs = append(resourceURIs, resource.URI)
	}
	slices.Sort(resourceURIs)
	if !slices.Equal(resourceURIs, []string{"atl://broker/discovery/jira", "atl://broker/discovery/jira/atl.broker.execution.v2", "atl://capabilities", "atl://runtime"}) {
		t.Fatalf("unexpected static Jira resources: %v", resourceURIs)
	}
	read := func() (domain.BrokerDiscoveryProjectionV2, error) {
		result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "atl://broker/discovery/jira"})
		if err != nil {
			return domain.BrokerDiscoveryProjectionV2{}, err
		}
		if len(result.Contents) != 1 {
			t.Fatal("unexpected resource result")
		}
		return brokercontract.DecodeDiscoveryProjectionV2([]byte(result.Contents[0].Text))
	}
	for _, access := range []domain.BrokerDiscoveryAccess{domain.BrokerDiscoveryAccessUnavailable, domain.BrokerDiscoveryAccessRequestRequired, domain.BrokerDiscoveryAccessAllowed} {
		withFixture(func() { f.authorizer.discoveryAccess = access })
		projection, err := read()
		if err != nil || projection.Operations[0].Access != access {
			t.Fatalf("access=%s projection=%+v err=%v", access, projection, err)
		}
	}
	authCalls, discoveryCalls := counts()
	if authCalls != 6 || discoveryCalls != 3 || f.backendCalls.Load() != 0 {
		t.Fatal("discovery did not follow both authenticated hops or reached backend")
	}
	// The same Broker still applies current admission after an allowed listing.
	withFixture(func() { f.authorizer.denyPhase = domain.BrokerPhaseAdmission })
	operation := exec.CommandContext(ctx, binary, "jira", "issue", "get", "PROJ-7", "--fields", "summary")
	operation.Env = environment
	out, operationErr := operation.CombinedOutput()
	var exitError *exec.ExitError
	var denial struct {
		Kind     string `json:"kind"`
		Recovery struct {
			Action    string `json:"action"`
			RetrySafe bool   `json:"retry_safe"`
		} `json:"recovery"`
	}
	admissions := 0
	withFixture(func() { admissions = f.authorizer.admissionCalls })
	if !errors.As(operationErr, &exitError) || exitError.ExitCode() != 6 || json.Unmarshal(out, &denial) != nil || denial.Kind != "forbidden" || denial.Recovery.Action != "request_access" || denial.Recovery.RetrySafe || admissions != 1 || f.backendCalls.Load() != 0 {
		t.Fatalf("expected admission denial: admissions=%d err=%v output=%s", admissions, operationErr, out)
	}
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonGrantExpired, domain.BrokerReasonRevoked} {
		withFixture(func() { _, f.authenticator.err = brokercontract.ErrorForReason(reason) })
		if _, err := read(); err == nil {
			t.Fatalf("session %s accepted", reason)
		}
	}
	withFixture(func() {
		f.authenticator.err = nil
		f.authenticator.authentication.Context.ExecutionEpoch = "epoch-2"
	})
	if _, err := read(); err == nil {
		t.Fatal("old session accepted after epoch changed")
	}
	writeSession("epoch-2")
	projection, err := read()
	if err != nil || projection.ExecutionEpoch != "epoch-2" {
		t.Fatalf("session refresh required process restart: %v", err)
	}
	if f.backendCalls.Load() != 0 {
		t.Fatal("discovery or denied operation contacted backend")
	}
}
