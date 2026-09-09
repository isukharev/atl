package compose

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/isukharev/atl/internal/brokerconfig"
)

func TestLoadBrokerRuntimeUsesExplicitFilesWithoutStartupProbes(t *testing.T) {
	configPath, authorityCalls, jiraCalls := brokerRuntimeFixture(t)
	directory := filepath.Dir(configPath)
	t.Setenv("ATL_CONFIG_DIR", filepath.Join(directory, "ordinary-client"))
	t.Setenv("ATL_JIRA_TOKEN", "ambient-client-token")
	runtime, err := LoadBrokerRuntime(configPath, "test", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.host == nil || authorityCalls.Load() != 0 || jiraCalls.Load() != 0 || runtime.material.Config.Confluence != nil {
		t.Fatalf("runtime=%+v authority=%d jira=%d", runtime, authorityCalls.Load(), jiraCalls.Load())
	}
	retained := runtime.material.JiraCredential
	runtime.Close()
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("runtime close did not clear loaded credential")
	}
}

func brokerRuntimeFixture(t *testing.T) (string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	clearBrokerProxyEnvironment(t)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var authorityCalls atomic.Int32
	authority := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { authorityCalls.Add(1) }))
	t.Cleanup(authority.Close)
	var jiraCalls atomic.Int32
	jira := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { jiraCalls.Add(1) }))
	t.Cleanup(jira.Close)
	identity := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	identity.Close()
	certificate, privateKey := encodeTLSIdentity(t, identity.TLS.Certificates[0])
	writeBrokerFile(t, directory, "server.crt", certificate)
	writeBrokerFile(t, directory, "server.key", privateKey)
	writeBrokerFile(t, directory, "authority.ca", encodeCertificate(authority.Certificate().Raw))
	writeBrokerFile(t, directory, "jira.ca", encodeCertificate(jira.Certificate().Raw))
	writeBrokerFile(t, directory, "authority.credential", []byte("synthetic-authority-credential"))
	writeBrokerFile(t, directory, "jira.credential", []byte("synthetic-jira-credential"))
	cfg := brokerconfig.Config{
		SchemaVersion: 1, BrokerID: "broker-1", DataAudience: "broker-data", AdminAudience: "broker-admin",
		DataListen: "127.0.0.1:8443", AdminListen: "127.0.0.1:8444",
		TLS:       brokerconfig.TLSFiles{CertificateFile: "server.crt", PrivateKeyFile: "server.key"},
		Authority: brokerconfig.Authority{BaseURL: authority.URL, IssuerSHA256: strings.Repeat("a", 64), CredentialFile: "authority.credential", CAFile: "authority.ca"},
		Jira:      &brokerconfig.Backend{BaseURL: jira.URL, WorkloadBackendID: "jira-primary", CredentialFile: "jira.credential", CAFile: "jira.ca"},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "broker.json")
	writeBrokerFile(t, directory, "broker.json", body)
	return configPath, &authorityCalls, &jiraCalls
}

func TestBrokerCompositionDoesNotImportAmbientCredentialOwner(t *testing.T) {
	body, err := os.ReadFile("broker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte(`internal/auth`), []byte(`auth.Token(`)} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("Broker composition contains forbidden ambient credential path %q", forbidden)
		}
	}
}

func encodeTLSIdentity(t *testing.T, certificate tls.Certificate) ([]byte, []byte) {
	t.Helper()
	var certificatePEM []byte
	for _, current := range certificate.Certificate {
		certificatePEM = append(certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: current})...)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}

func encodeCertificate(certificate []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
}

func writeBrokerFile(t *testing.T, directory, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func clearBrokerProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "")
	}
}
