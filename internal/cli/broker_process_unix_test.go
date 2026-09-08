//go:build !windows

package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestSelectedBrokerBinaryServesAdminAndStopsOnSIGTERM(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	issuer := strings.Repeat("a", 64)
	var authorityCalls atomic.Int32
	authority := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorityCalls.Add(1)
		if request.URL.Path != "/v1/authenticate" || request.Header.Get("Authorization") != "Bearer synthetic-authority-credential" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(request.Body)
		authentication, err := brokertransport.DecodeAuthenticationRequestV1(body)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		now := time.Now()
		verified := domain.BrokerVerifiedContext{
			PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
			Audience: authentication.Audience, BrokerID: authentication.BrokerID, AuthorityRevision: "revision-1",
			ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(),
			GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(),
			Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("b", 64), WorkloadBackendID: "jira-primary"},
		}
		response := brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: authentication.Nonce, CredentialSHA256: authentication.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: verified}
		encoded, err := brokertransport.EncodeAuthenticationResponseV1(response)
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encoded)
	}))
	defer authority.Close()
	var backendCalls atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { backendCalls.Add(1) }))
	defer backend.Close()
	identity := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	identity.Close()
	certificate, privateKey := brokerProcessTLSIdentity(t, identity.TLS.Certificates[0])
	writeBrokerProcessFile(t, directory, "server.crt", certificate)
	writeBrokerProcessFile(t, directory, "server.key", privateKey)
	writeBrokerProcessFile(t, directory, "authority.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authority.Certificate().Raw}))
	writeBrokerProcessFile(t, directory, "jira.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: backend.Certificate().Raw}))
	writeBrokerProcessFile(t, directory, "authority.credential", []byte("synthetic-authority-credential"))
	writeBrokerProcessFile(t, directory, "jira.credential", []byte("synthetic-jira-credential"))
	dataAddress := brokerProcessFreeAddress(t)
	adminAddress := brokerProcessFreeAddress(t)
	cfg := brokerconfig.Config{
		SchemaVersion: 1, BrokerID: "broker-1", DataAudience: "broker-data", AdminAudience: "broker-admin",
		DataListen: dataAddress, AdminListen: adminAddress,
		TLS:       brokerconfig.TLSFiles{CertificateFile: "server.crt", PrivateKeyFile: "server.key"},
		Authority: brokerconfig.Authority{BaseURL: authority.URL, IssuerSHA256: issuer, CredentialFile: "authority.credential", CAFile: "authority.ca"},
		Jira:      &brokerconfig.Backend{BaseURL: backend.URL, WorkloadBackendID: "jira-primary", CredentialFile: "jira.credential", CAFile: "jira.ca"},
	}
	configBody, _ := json.Marshal(cfg)
	configPath := filepath.Join(directory, "broker.json")
	writeBrokerProcessFile(t, directory, "broker.json", configBody)

	binary := filepath.Join(t.TempDir(), "atl")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/atl")
	build.Env = brokerProcessEnvironment(os.Environ(), map[string]string{"GOTOOLCHAIN": "auto", "GOWORK": "off"})
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build selected binary: %v: %s", err, output)
	}
	ordinary := filepath.Join(t.TempDir(), "ordinary")
	if err := os.Mkdir(ordinary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ordinary, "config.json"), []byte("}{"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "broker", "serve", "--config", configPath)
	command.Env = brokerProcessEnvironment(os.Environ(), map[string]string{"ATL_CONFIG_DIR": ordinary, "ATL_JIRA_PAT": "ambient-client-token", "ATL_NO_UPDATE": "1"})
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	client := brokerProcessTLSClient(t, identity.TLS.Certificates[0])
	status := waitForBrokerReadiness(t, client, adminAddress)
	if status.Status != brokertransport.AdminStatusReady || authorityCalls.Load() != 1 || backendCalls.Load() != 0 {
		_ = command.Process.Kill()
		t.Fatalf("status=%+v authority=%d backend=%d", status, authorityCalls.Load(), backendCalls.Load())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("Broker process exit: %v stderr=%s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("Broker process exceeded shutdown bound")
	}
	var result struct {
		Status   string `json:"status"`
		Complete bool   `json:"complete"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "stopped" || !result.Complete {
		t.Fatalf("stdout=%q result=%+v err=%v", stdout.String(), result, err)
	}
	for _, forbidden := range []string{"synthetic-workload-credential", "synthetic-authority-credential", "synthetic-jira-credential", authority.URL, backend.URL, configPath} {
		if strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("audit exposed a private fixture category")
		}
	}
}

func waitForBrokerReadiness(t *testing.T, client *http.Client, address string) brokertransport.AdminStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+address+"/readyz", nil)
		request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
		response, err := client.Do(request)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if value, decodeErr := brokertransport.DecodeAdminStatusV1(body); decodeErr == nil {
				return value
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Broker admin listener did not become ready")
	return brokertransport.AdminStatus{}
}

func brokerProcessFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func brokerProcessTLSIdentity(t *testing.T, certificate tls.Certificate) ([]byte, []byte) {
	t.Helper()
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	privateDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}

func writeBrokerProcessFile(t *testing.T, directory, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func brokerProcessTLSClient(t *testing.T, certificate tls.Certificate) *http.Client {
	t.Helper()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, ForceAttemptHTTP2: false}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: time.Second}
}

func brokerProcessEnvironment(base []string, overrides map[string]string) []string {
	blocked := map[string]bool{"GOROOT": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true}
	for name := range overrides {
		blocked[name] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if !blocked[name] {
			out = append(out, value)
		}
	}
	for name, value := range overrides {
		out = append(out, name+"="+value)
	}
	return out
}
