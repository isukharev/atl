//go:build !windows

package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type attachmentCLIProcessFixture struct {
	chain             *attachmentChainFixture
	environment       []string
	sessionPath       string
	audit             bytes.Buffer
	directCalls       atomic.Int32
	stop              func()
	brokerURL         string
	brokerCertificate []byte
}

func newAttachmentCLIProcessFixture(t *testing.T, payload []byte) *attachmentCLIProcessFixture {
	t.Helper()
	f := &attachmentCLIProcessFixture{chain: newAttachmentChainFixture(t, payload)}
	certificate := testHostCertificate(t)
	data, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = data.Close()
		t.Fatal(err)
	}
	host, err := NewHost(HostConfig{DataAddress: data.Addr().String(), AdminAddress: admin.Addr().String(), AdminAudience: "atl-broker-admin", BrokerID: "broker-1", Certificate: certificate}, HostDependencies{
		Data: f.chain.handler, Authenticator: f.chain.handler.authenticator, Guard: f.chain.handler.guard, AuditWriter: &f.audit,
	})
	if err != nil {
		_ = data.Close()
		_ = admin.Close()
		t.Fatal(err)
	}
	listeners := []net.Listener{data, admin}
	var listenerMu sync.Mutex
	host.listen = func(_, _ string) (net.Listener, error) {
		listenerMu.Lock()
		defer listenerMu.Unlock()
		if len(listeners) == 0 {
			return nil, fmt.Errorf("synthetic listener inventory exhausted")
		}
		next := listeners[0]
		listeners = listeners[1:]
		return next, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- host.Run(ctx) }()
	var once sync.Once
	f.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("synthetic Host stop: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("synthetic Host did not stop")
			}
		})
	}
	t.Cleanup(f.stop)
	waitReady(t, host)
	configureAttachmentCLIProcessFixture(t, f, "https://"+data.Addr().String(), certificate.Certificate[0])
	return f
}

func configureAttachmentCLIProcessFixture(t *testing.T, f *attachmentCLIProcessFixture, baseURL string, certificateDER []byte) {
	t.Helper()
	f.brokerURL, f.brokerCertificate = baseURL, bytes.Clone(certificateDER)
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.directCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(direct.Close)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.sessionPath = filepath.Join(root, "session.json")
	session, err := json.Marshal(map[string]any{"schema_version": 1, "credential": attachmentChainWorkload, "execution_id": "execution-1", "execution_epoch": "epoch-1", "authority_revision": "revision-1"})
	if err != nil {
		t.Fatal(err)
	}
	projectPageProcessWriteFile(t, f.sessionPath, session)
	caPath := filepath.Join(root, "broker.ca")
	projectPageProcessWriteFile(t, caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}))
	config, err := json.Marshal(map[string]any{"connection_mode": "broker", "jira_url": direct.URL, "jira_list_views": map[string]any{}, "broker": map[string]any{
		"base_url": baseURL, "broker_id": "broker-1", "audience": "atl-broker", "ca_file": caPath, "jira_session_file": f.sessionPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	projectPageProcessWriteFile(t, filepath.Join(root, "config.json"), config)
	f.environment = []string{"PATH=" + os.Getenv("PATH"), "ATL_CONFIG_DIR=" + root, "ATL_NO_UPDATE=1", "ATL_READ_ONLY=1"}
}

func runAttachmentSelectedCLI(t *testing.T, binary string, environment []string, destination string) (string, string, error) {
	t.Helper()
	return runAttachmentSelectedCLISelector(t, binary, environment, destination, "PROJ-1", "7")
}

func runAttachmentSelectedCLISelector(t *testing.T, binary string, environment []string, destination, issue, attachment string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "jira", "issue", "attachment", "get", issue, "--id", attachment, "--into", destination)
	command.Env = environment
	command.WaitDelay = 2 * time.Second
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		t.Fatalf("selected attachment CLI exceeded bound: context=%v stdout=%t stderr=%t", ctx.Err(), stdout.exceeded, stderr.exceeded)
	}
	return stdout.String(), stderr.String(), err
}

func TestSelectedAttachmentCLIAndHostCandidateConformance(t *testing.T) {
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok || !definition.Definition.Available {
		t.Fatal("the candidate oracle requires the real enabled registry revision")
	}
	binary := buildSelectedATLBinary(t, "")
	checkAttachmentCLIPositiveConformance(t, binary, newAttachmentCLIProcessFixture)
}

func checkAttachmentCLIPositiveConformance(t *testing.T, binary string, newFixture func(*testing.T, []byte) *attachmentCLIProcessFixture) {
	t.Helper()
	for _, size := range []int{0, 23, 1<<20 + 17, 16 << 20} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, size)
			fixture := newFixture(t, payload)
			destination := filepath.Join(t.TempDir(), "download")
			stdout, stderr, err := runAttachmentSelectedCLI(t, binary, fixture.environment, destination)
			fixture.stop()
			if err != nil || stderr != "" {
				fixture.chain.mu.Lock()
				defer fixture.chain.mu.Unlock()
				t.Fatalf("CLI error=%v stderr=%s counts=%v violations=%d", err, stderr, fixture.chain.counts, fixture.chain.violations)
			}
			var result map[string]string
			if err := json.Unmarshal([]byte(stdout), &result); err != nil || result["name"] != "example.bin" || result["id"] != "7" || result["key"] != "PROJ-1" {
				t.Fatalf("CLI result=%s decode=%v", stdout, err)
			}
			actual, err := os.ReadFile(filepath.Join(destination, "example.bin"))
			if err != nil || !bytes.Equal(actual, payload) || fixture.directCalls.Load() != 0 {
				t.Fatalf("file read=%v size=%d match=%t direct=%d", err, len(actual), bytes.Equal(actual, payload), fixture.directCalls.Load())
			}
			releases := max(1, (size+(1<<20)-1)/(1<<20))
			fixture.chain.mu.Lock()
			counts := fixture.chain.counts
			total := 0
			for _, count := range counts {
				total += count
			}
			if total != 12+4*releases || counts["authentication"] != 3+releases || counts["discovery"] != 1 || counts["operation_release"] != releases || counts["metadata"] != 2+releases || counts["body"] != 1 || fixture.chain.violations != 0 || len(fixture.chain.nonces) != counts["authentication"] {
				t.Errorf("CLI chain counts=%v nonces=%d violations=%d", counts, len(fixture.chain.nonces), fixture.chain.violations)
			}
			fixture.chain.mu.Unlock()
			decoder := json.NewDecoder(bytes.NewReader(fixture.audit.Bytes()))
			completed := 0
			for {
				var event AuditEvent
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else if err != nil || !validAuditEvent(event) {
					t.Fatalf("audit decode=%v", err)
				}
				if event.Route == "data_execute_v3" && event.Outcome == "success" {
					completed++
				}
			}
			if completed != 1 {
				t.Fatalf("successful stream audits=%d", completed)
			}
		})
	}
}

func TestSelectedAttachmentCLIAtomicFailureCandidateConformance(t *testing.T) {
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok || !definition.Definition.Available {
		t.Fatal("the candidate oracle requires the real enabled registry revision")
	}
	binary := buildSelectedATLBinary(t, "")
	for _, test := range []struct {
		name          string
		prepare       func(*attachmentChainFixture)
		rotateSession bool
		payloadSize   int
	}{
		{name: "first release denied", prepare: func(f *attachmentChainFixture) { f.denyRelease = 1 }},
		{name: "second release denied", prepare: func(f *attachmentChainFixture) { f.denyRelease = 2 }},
		{name: "metadata drift", prepare: func(f *attachmentChainFixture) { f.metadataDrift = 3 }},
		{name: "credential at frame boundary", prepare: func(f *attachmentChainFixture) { copy(f.payload[(1<<20)-4:], attachmentChainBackend) }},
		{name: "short source", prepare: func(f *attachmentChainFixture) { f.sourceMode = "short" }},
		{name: "extra source", prepare: func(f *attachmentChainFixture) { f.sourceMode = "extra" }},
		{name: "extra byte after maximum source", payloadSize: 16 << 20, prepare: func(f *attachmentChainFixture) { f.sourceMode = "extra" }},
		{name: "source redirect", prepare: func(f *attachmentChainFixture) { f.sourceMode = "redirect" }},
		{name: "source failure", prepare: func(f *attachmentChainFixture) { f.sourceMode = "failure" }},
		{name: "first release authentication revoked", prepare: func(f *attachmentChainFixture) { f.revokeAuthentication = 4 }},
		{name: "second release authentication revoked", prepare: func(f *attachmentChainFixture) { f.revokeAuthentication = 5 }},
		{name: "second release authentication expired", prepare: func(f *attachmentChainFixture) { f.expireAuthentication = 5 }},
		{name: "session replaced before first release", rotateSession: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			size := test.payloadSize
			if size == 0 {
				size = 1<<20 + 128
			}
			fixture := newAttachmentCLIProcessFixture(t, bytes.Repeat([]byte{'x'}, size))
			if test.prepare != nil {
				test.prepare(fixture.chain)
			}
			if test.rotateSession {
				fixture.chain.releaseHook = func(index int) {
					if index == 1 {
						body, marshalErr := json.Marshal(map[string]any{"schema_version": 1, "credential": "synthetic-replacement-workload", "execution_id": "execution-1", "execution_epoch": "epoch-1", "authority_revision": "revision-1"})
						if marshalErr != nil {
							t.Errorf("encode replacement session: %v", marshalErr)
							return
						}
						if writeErr := os.WriteFile(fixture.sessionPath, body, 0o600); writeErr != nil {
							t.Errorf("replace session: %v", writeErr)
						}
					}
				}
			}
			destination := t.TempDir()
			prior := []byte("prior-owned-content")
			target := filepath.Join(destination, "example.bin")
			projectPageProcessWriteFile(t, target, prior)
			stdout, stderr, err := runAttachmentSelectedCLI(t, binary, fixture.environment, destination)
			fixture.stop()
			if err == nil {
				t.Fatal("failed stream returned CLI success")
			}
			actual, readErr := os.ReadFile(target)
			entries, listErr := os.ReadDir(destination)
			if readErr != nil || listErr != nil || !bytes.Equal(actual, prior) || len(entries) != 1 || entries[0].Name() != "example.bin" {
				t.Fatalf("failed stream changed destination: read=%v list=%v unchanged=%t entries=%d", readErr, listErr, bytes.Equal(actual, prior), len(entries))
			}
			for _, credential := range []string{attachmentChainWorkload, attachmentChainAuthority, attachmentChainBackend} {
				if strings.Contains(stdout+stderr+fixture.audit.String(), credential) {
					t.Fatal("process output or audit exposed a credential")
				}
			}
			if fixture.directCalls.Load() != 0 {
				t.Fatal("failed stream fell back to direct Jira")
			}
			fixture.chain.mu.Lock()
			if fixture.chain.violations != 0 || fixture.chain.counts["body"] != 1 || fixture.chain.counts["redirect"] != 0 {
				t.Errorf("fixture violations=%d counts=%v", fixture.chain.violations, fixture.chain.counts)
			}
			if test.payloadSize == 16<<20 && fixture.chain.counts["operation_release"] != 15 {
				t.Errorf("maximum overflow did not reach the final source boundary: counts=%v", fixture.chain.counts)
			}
			fixture.chain.mu.Unlock()
			if len(fixture.chain.handler.permits) != 0 || len(fixture.chain.handler.attachmentPermits) != 0 {
				t.Error("failed stream retained a request permit")
			}
			decoder := json.NewDecoder(bytes.NewReader(fixture.audit.Bytes()))
			streamEvents := 0
			for {
				var event AuditEvent
				if decodeErr := decoder.Decode(&event); decodeErr == io.EOF {
					break
				} else if decodeErr != nil || !validAuditEvent(event) {
					t.Fatalf("audit decode=%v", decodeErr)
				}
				if event.Route == "data_execute_v3" {
					streamEvents++
					if !test.rotateSession && event.Outcome == "success" {
						t.Error("failed server stream was audited as success")
					}
				}
			}
			if streamEvents != 1 {
				t.Errorf("stream audits=%d", streamEvents)
			}
		})
	}
}
