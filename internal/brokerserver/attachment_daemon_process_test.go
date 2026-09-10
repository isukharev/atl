//go:build !windows

package brokerserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestSelectedAttachmentCLIDaemonCandidateConformance(t *testing.T) {
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok || !definition.Definition.Available {
		t.Fatal("the daemon oracle requires the real enabled registry revision")
	}
	binary := buildSelectedATLBinary(t, "")
	checkAttachmentCLIPositiveConformance(t, binary, func(t *testing.T, payload []byte) *attachmentCLIProcessFixture {
		return newAttachmentCLIDaemonFixture(t, binary, payload)
	})
}

func newAttachmentCLIDaemonFixture(t *testing.T, binary string, payload []byte) *attachmentCLIProcessFixture {
	t.Helper()
	f := &attachmentCLIProcessFixture{chain: newAttachmentChainFixtureWithPrefix(t, payload, "")}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	certificate := testHostCertificate(t)
	privateDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"server.crt":           pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}),
		"server.key":           pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		"authority.ca":         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.chain.authorityServer.Certificate().Raw}),
		"jira.ca":              pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.chain.backendServer.Certificate().Raw}),
		"authority.credential": []byte(attachmentChainAuthority),
		"jira.credential":      []byte(attachmentChainBackend),
	} {
		projectPageProcessWriteFile(t, filepath.Join(root, name), body)
	}
	clear(privateDER)
	dataAddress, adminAddress := attachmentDaemonAddresses(t)
	config := brokerconfig.Config{
		SchemaVersion: 1, BrokerID: "broker-1", DataAudience: "atl-broker", AdminAudience: "atl-broker-admin",
		DataListen: dataAddress, AdminListen: adminAddress,
		TLS:       brokerconfig.TLSFiles{CertificateFile: "server.crt", PrivateKeyFile: "server.key"},
		Authority: brokerconfig.Authority{BaseURL: f.chain.authorityServer.URL, IssuerSHA256: f.chain.issuer, CredentialFile: "authority.credential", CAFile: "authority.ca"},
		Jira:      &brokerconfig.Backend{BaseURL: f.chain.backendServer.URL, WorkloadBackendID: "jira-primary", CredentialFile: "jira.credential", CAFile: "jira.ca"},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "broker.json")
	projectPageProcessWriteFile(t, configPath, body)
	configureAttachmentCLIProcessFixture(t, f, "https://"+dataAddress, certificate.Certificate[0])
	ordinary := t.TempDir()
	projectPageProcessWriteFile(t, filepath.Join(ordinary, "config.json"), []byte("}{"))
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "broker", "serve", "--config", configPath)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "ATL_CONFIG_DIR=" + ordinary, "ATL_NO_UPDATE=1", "ATL_READ_ONLY=1"}
	command.WaitDelay = 2 * time.Second
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var runErr error
	go func() { runErr = command.Wait(); close(done) }()
	var once sync.Once
	f.stop = func() {
		once.Do(func() {
			select {
			case <-done:
			default:
				_ = command.Process.Signal(syscall.SIGTERM)
			}
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("attachment daemon exceeded shutdown bound")
			}
			if runErr != nil || ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
				t.Errorf("attachment daemon result=%v context=%v output bounds=%t/%t", runErr, ctx.Err(), stdout.exceeded, stderr.exceeded)
			}
			var result struct {
				Status   string `json:"status"`
				Complete bool   `json:"complete"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "stopped" || !result.Complete {
				t.Errorf("attachment daemon did not report complete shutdown: %v", err)
			}
			_, _ = f.audit.Write(stderr.Bytes())
		})
	}
	t.Cleanup(f.stop)
	waitAttachmentDaemonTLS(t, dataAddress, certificate, done)
	return f
}

func attachmentDaemonAddresses(t *testing.T) (string, string) {
	t.Helper()
	data, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// The process binds these distinct released addresses. A competing bind
	// fails this fixture; it never broadens listeners or retries on a new port.
	return data.Addr().String(), admin.Addr().String()
}

func waitAttachmentDaemonTLS(t *testing.T, address string, certificate tls.Certificate, done <-chan struct{}) {
	t.Helper()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return
		}
		select {
		case <-done:
			t.Fatal("attachment daemon exited before accepting TLS")
		case <-ctx.Done():
			t.Fatal("attachment daemon did not accept TLS within startup bound")
		case <-tick.C:
		}
	}
}
