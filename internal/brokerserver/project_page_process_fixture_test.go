//go:build !windows

package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const (
	projectPageProcessWorkloadCredential    = "synthetic-workload-credential-a"
	projectPageProcessReplacementCredential = "synthetic-workload-credential-b"
	projectPageProcessAuthorityCredential   = "synthetic-authority-server-credential"
	projectPageProcessJiraCredential        = "synthetic-jira-backend-credential"
	projectPageProcessRemoteCanary          = "SYNTHETIC-REMOTE-DIAGNOSTIC-CANARY"
)

type projectPageProcessOptions struct {
	missingAuthorityPath  string
	denyForbiddenSibling  bool
	jiraMode              string
	finalRevisionDrift    bool
	replaceAfterDiscovery bool
	replaceAfterBuffer    bool
}

type projectPageProcessCounters struct {
	brokerNegotiate atomic.Int32
	brokerDiscovery atomic.Int32
	brokerExecute   atomic.Int32
	authentication  atomic.Int32
	discovery       atomic.Int32
	admission       atomic.Int32
	qualification   atomic.Int32
	operation       atomic.Int32
	v1Fallback      atomic.Int32
	jiraProject     atomic.Int32
	jiraIdentity    atomic.Int32
	jiraBusiness    atomic.Int32
	directJira      atomic.Int32
}

type projectPageProcessCountSnapshot struct {
	brokerNegotiate, brokerDiscovery, brokerExecute int32
	authentication, discovery                       int32
	admission, qualification, operation             int32
	v1Fallback                                      int32
	jiraProject, jiraIdentity, jiraBusiness         int32
	directJira                                      int32
}

func (c *projectPageProcessCounters) snapshot() projectPageProcessCountSnapshot {
	return projectPageProcessCountSnapshot{
		brokerNegotiate: c.brokerNegotiate.Load(), brokerDiscovery: c.brokerDiscovery.Load(), brokerExecute: c.brokerExecute.Load(),
		authentication: c.authentication.Load(), discovery: c.discovery.Load(), admission: c.admission.Load(),
		qualification: c.qualification.Load(), operation: c.operation.Load(), v1Fallback: c.v1Fallback.Load(),
		jiraProject: c.jiraProject.Load(), jiraIdentity: c.jiraIdentity.Load(), jiraBusiness: c.jiraBusiness.Load(),
		directJira: c.directJira.Load(),
	}
}

type projectPageProcessFixture struct {
	t              *testing.T
	options        projectPageProcessOptions
	counters       projectPageProcessCounters
	verified       domain.BrokerVerifiedContext
	issuerSHA256   string
	sessionPath    string
	environment    []string
	hostClockNanos atomic.Int64
	violationsMu   sync.Mutex
	violations     []string
	bufferHookOnce sync.Once
	discoveryOnce  sync.Once
	completions    projectPageProcessCompletionCounters
	completionWake chan struct{}
	failuresMu     sync.Mutex
	failureReasons map[domain.BrokerReason]int
}

func newProjectPageProcessFixture(t *testing.T, options projectPageProcessOptions) *projectPageProcessFixture {
	t.Helper()
	f := &projectPageProcessFixture{
		t: t, options: options, issuerSHA256: strings.Repeat("a", 64),
		completionWake: make(chan struct{}, 1), failureReasons: make(map[domain.BrokerReason]int),
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("project-page fixture witness: %s", f.diagnosticWitness())
		}
	})
	f.hostClockNanos.Store(time.Now().UnixNano())

	configRoot := t.TempDir()
	if err := os.Chmod(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(configRoot, "credentials.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.sessionPath = filepath.Join(configRoot, "jira-session.json")
	if err := f.writeSession(projectPageProcessWorkloadCredential); err != nil {
		t.Fatal(err)
	}

	direct := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		f.counters.directJira.Add(1)
	}))
	t.Cleanup(direct.Close)

	jiraServer := httptest.NewTLSServer(http.HandlerFunc(f.serveJira))
	t.Cleanup(jiraServer.Close)
	jiraTLS := projectPageProcessTLSOptions(t, jiraServer)
	jiraReader, err := jiraadapter.NewWithSchedulerTLS(jiraServer.URL, projectPageProcessJiraCredential, "test", nil, jiraTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jiraReader.CloseIdleConnections)
	origin, err := jiraReader.BrokerOriginSHA256()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.verified = domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(2 * time.Minute).UnixMilli(),
		GrantExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(2 * time.Minute).UnixMilli(),
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: origin, WorkloadBackendID: "jira-primary"},
	}

	authorityServer := httptest.NewTLSServer(http.HandlerFunc(f.serveAuthority))
	t.Cleanup(authorityServer.Close)
	authority, err := brokerauthority.New(brokerauthority.Config{
		BaseURL: authorityServer.URL, ServerCredential: projectPageProcessAuthorityCredential,
		IssuerSHA256: f.issuerSHA256, Version: "test", TLS: projectPageProcessTLSOptions(t, authorityServer),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.CloseIdleConnections)

	binding := app.BrokerJiraIssueReader{Backend: f.verified.Backend, Reader: jiraReader}
	reads, err := app.NewBrokerReadService(authority, binding, app.BrokerConfluencePageReader{})
	if err != nil {
		t.Fatal(err)
	}
	projectPages, err := app.NewBrokerProjectPageService(authority, app.BrokerJiraProjectPageReader{Backend: binding.Backend, Reader: jiraReader})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCredentialGuard([]byte(projectPageProcessAuthorityCredential), []byte(projectPageProcessJiraCredential))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	data, err := New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{
		Authenticator: authority, Reads: reads, ProjectPages: projectPages, Guard: guard,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrappedData := f.observeDataHandler(data)

	certificate := testHostCertificate(t)
	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		dataListener.Close()
		t.Fatal(err)
	}
	host, err := NewHost(HostConfig{
		DataAddress: dataListener.Addr().String(), AdminAddress: adminListener.Addr().String(), AdminAudience: "atl-broker-admin",
		BrokerID: "broker-1", Certificate: certificate,
	}, HostDependencies{Data: wrappedData, Authenticator: authority, Guard: guard, AuditWriter: io.Discard})
	if err != nil {
		dataListener.Close()
		adminListener.Close()
		t.Fatal(err)
	}
	host.now = func() time.Time { return time.Unix(0, f.hostClockNanos.Load()) }
	listeners := []net.Listener{dataListener, adminListener}
	var listenersMu sync.Mutex
	host.listen = func(_, _ string) (net.Listener, error) {
		listenersMu.Lock()
		defer listenersMu.Unlock()
		if len(listeners) == 0 {
			return nil, fmt.Errorf("synthetic listener inventory exhausted")
		}
		selected := listeners[0]
		listeners = listeners[1:]
		return selected, nil
	}
	hostContext, stopHost := context.WithCancel(context.Background())
	hostDone := make(chan error, 1)
	go func() { hostDone <- host.Run(hostContext) }()
	t.Cleanup(func() {
		stopHost()
		select {
		case err := <-hostDone:
			if err != nil {
				t.Errorf("stop synthetic Broker Host: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Error("synthetic Broker Host did not stop")
		}
	})
	waitReady(t, host)

	caPath := filepath.Join(configRoot, "broker.ca")
	projectPageProcessWriteFile(t, caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}))
	configBody, err := json.Marshal(map[string]any{
		"connection_mode": "broker", "jira_url": direct.URL, "jira_list_views": map[string]any{},
		"broker": map[string]any{
			"base_url": "https://" + dataListener.Addr().String(), "broker_id": "broker-1", "audience": "atl-broker",
			"ca_file": caPath, "jira_session_file": f.sessionPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectPageProcessWriteFile(t, filepath.Join(configRoot, "config.json"), configBody)
	f.environment = []string{"PATH=" + os.Getenv("PATH"), "ATL_CONFIG_DIR=" + configRoot, "ATL_NO_UPDATE=1", "ATL_READ_ONLY=1"}
	return f
}

func projectPageProcessWriteFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *projectPageProcessFixture) writeSession(credential string) error {
	body, err := json.Marshal(map[string]any{
		"schema_version": 1, "credential": credential, "execution_id": "execution-1",
		"execution_epoch": "epoch-1", "authority_revision": "revision-1",
	})
	if err != nil {
		return err
	}
	return os.WriteFile(f.sessionPath, body, 0o600)
}

func (f *projectPageProcessFixture) advanceHostClock(duration time.Duration) {
	f.hostClockNanos.Add(int64(duration))
}

func (f *projectPageProcessFixture) violationf(format string, args ...any) {
	f.violationsMu.Lock()
	defer f.violationsMu.Unlock()
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
}

func (f *projectPageProcessFixture) assertNoViolations() {
	f.t.Helper()
	f.violationsMu.Lock()
	defer f.violationsMu.Unlock()
	if len(f.violations) != 0 {
		f.t.Fatalf("synthetic chain violations: %s", strings.Join(f.violations, "; "))
	}
}

func (f *projectPageProcessFixture) serveAuthority(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+projectPageProcessAuthorityCredential || request.Header.Get("Content-Type") != "application/json" {
		f.violationf("authority request method/header mismatch at %s", request.URL.Path)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 3<<20))
	if err != nil || len(body) == 0 {
		f.violationf("authority body read failed at %s", request.URL.Path)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	switch request.URL.Path {
	case "/v1/authenticate":
		f.counters.authentication.Add(1)
	case brokertransport.DiscoveryPathV3:
		f.counters.discovery.Add(1)
	case "/v2/authorize/project-page/admission":
		f.counters.admission.Add(1)
	case "/v2/authorize/project-page/qualification":
		f.counters.qualification.Add(1)
	case "/v2/authorize/project-page/operation":
		f.counters.operation.Add(1)
	}
	if request.URL.Path == f.options.missingAuthorityPath {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, projectPageProcessRemoteCanary)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	source := &projectPageServerAuthorizer{nowMillis: time.Now().UnixMilli()}
	switch request.URL.Path {
	case "/v1/authenticate":
		value, decodeErr := brokertransport.DecodeAuthenticationRequestV1(body)
		credential := value.Credential()
		defer clear(credential)
		if decodeErr != nil || string(credential) != projectPageProcessWorkloadCredential {
			f.violationf("invalid workload authentication request")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		encoded, encodeErr := brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{
			SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: f.issuerSHA256,
			IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli(), Context: f.verified,
		})
		f.writeAuthorityResponse(writer, encoded, encodeErr)
	case brokertransport.DiscoveryPathV3:
		value, decodeErr := brokercontract.DecodeFamilyDiscoveryAuthorizationRequestV3(body)
		if decodeErr != nil {
			f.violationf("invalid discovery-v3 authority request")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		projection, projectionErr := source.DiscoverFamilyV3(request.Context(), value)
		encoded, encodeErr := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
		if projectionErr != nil {
			encodeErr = projectionErr
		}
		if f.options.replaceAfterDiscovery {
			f.discoveryOnce.Do(func() {
				if err := f.writeSession(projectPageProcessReplacementCredential); err != nil {
					f.violationf("replace session after discovery: %v", err)
				}
			})
		}
		f.writeAuthorityResponse(writer, encoded, encodeErr)
	case "/v2/authorize/project-page/admission":
		value, decodeErr := brokercontract.DecodeProjectPageAdmissionRequestV2(body)
		decision, decisionErr := source.AdmitProjectPage(request.Context(), value)
		encoded, encodeErr := brokercontract.EncodeProjectPageAdmissionDecisionV2(decision)
		f.writeDecodedAuthorityResponse(writer, decodeErr, decisionErr, encodeErr, encoded, "admission")
	case "/v2/authorize/project-page/qualification":
		value, decodeErr := brokercontract.DecodeProjectPageQualificationRequestV2(body)
		decision, decisionErr := source.AuthorizeProjectPageQualification(request.Context(), value)
		encoded, encodeErr := brokercontract.EncodeProjectPageQualificationDecisionV2(decision)
		f.writeDecodedAuthorityResponse(writer, decodeErr, decisionErr, encodeErr, encoded, "qualification")
	case "/v2/authorize/project-page/operation":
		value, decodeErr := brokercontract.DecodeProjectPageOperationAuthorizationRequestV2(body)
		if f.options.denyForbiddenSibling {
			for _, issue := range value.Issues {
				if issue.Key == "PROJ-13" {
					source.deny = domain.BrokerPhaseFinalAuthorization
				}
			}
		}
		decision, decisionErr := source.AuthorizeProjectPage(request.Context(), value)
		if f.options.finalRevisionDrift {
			decision.AuthorityRevision = "revision-other"
			decision.DecisionSHA256 = ""
		}
		encoded, encodeErr := brokercontract.EncodeProjectPageOperationDecisionV2(decision)
		f.writeDecodedAuthorityResponse(writer, decodeErr, decisionErr, encodeErr, encoded, "operation")
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/authorize/") || request.URL.Path == brokertransport.DiscoveryPathV2 {
			f.counters.v1Fallback.Add(1)
		}
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, projectPageProcessRemoteCanary)
	}
}

func (f *projectPageProcessFixture) writeDecodedAuthorityResponse(writer http.ResponseWriter, decodeErr, decisionErr, encodeErr error, encoded []byte, phase string) {
	if decodeErr != nil || decisionErr != nil || encodeErr != nil {
		f.violationf("%s authority codec failed: decode=%v decision=%v encode=%v", phase, decodeErr, decisionErr, encodeErr)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = writer.Write(encoded)
}

func (f *projectPageProcessFixture) writeAuthorityResponse(writer http.ResponseWriter, encoded []byte, err error) {
	if err != nil {
		f.violationf("authority response codec failed: %v", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = writer.Write(encoded)
}

func (f *projectPageProcessFixture) serveJira(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+projectPageProcessJiraCredential {
		f.violationf("Jira request method/credential mismatch: %s %s", request.Method, request.URL.RequestURI())
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if request.URL.Path == "/rest/api/2/project/PROJ" && request.URL.RawQuery == "" {
		f.counters.jiraProject.Add(1)
		_, _ = io.WriteString(writer, `{"id":"100","key":"PROJ","name":"Synthetic project"}`)
		return
	}
	if request.URL.Path != "/rest/api/2/search" {
		f.violationf("unexpected Jira path %s", request.URL.RequestURI())
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	query := request.URL.Query()
	startAt, startErr := strconv.Atoi(query.Get("startAt"))
	maxResults, maxErr := strconv.Atoi(query.Get("maxResults"))
	fields := query.Get("fields")
	if startErr != nil || maxErr != nil || query.Get("jql") != "project = 100 ORDER BY id ASC" || len(query) != 4 {
		f.violationf("unexpected Jira search query %s", request.URL.RawQuery)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	business := fields != "project,updated"
	if business {
		f.counters.jiraBusiness.Add(1)
		if fields != "description,project,summary,updated" {
			f.violationf("unexpected Jira business fields %q", fields)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		f.counters.jiraIdentity.Add(1)
	}
	page := f.jiraPage(startAt, maxResults, business)
	if err := json.NewEncoder(writer).Encode(page); err != nil {
		f.violationf("encode Jira response: %v", err)
	}
}

func (f *projectPageProcessFixture) jiraPage(startAt, maxResults int, business bool) map[string]any {
	total := 3
	ids := []string{"20", "3"}
	switch f.options.jiraMode {
	case "forbidden":
		ids = []string{"20", "13"}
	case "stalled":
		ids = []string{}
	case "offset_limit":
		total, ids = domain.BrokerProjectPageMaxStartAt+2, []string{"1000001"}
	default:
		if startAt == 2 {
			ids = []string{"100"}
		}
	}
	if f.options.jiraMode == "order_drift" && business {
		sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	}
	issues := make([]map[string]any, len(ids))
	for index, id := range ids {
		updated := "2026-09-08T10:00:00.000+0000"
		fields := map[string]any{"project": map[string]any{"id": "100", "key": "PROJ"}, "updated": updated}
		if business {
			fields["summary"] = "Synthetic summary " + id
			fields["description"] = "Native *wiki* body " + id
			if f.options.jiraMode == "extra_field" {
				fields["status"] = projectPageProcessRemoteCanary
			}
		}
		issues[index] = map[string]any{"id": id, "key": "PROJ-" + id, "fields": fields}
	}
	return map[string]any{"startAt": startAt, "maxResults": maxResults, "total": total, "issues": issues}
}

type projectPageProcessBufferedWriter struct {
	underlying http.ResponseWriter
	header     http.Header
	body       bytes.Buffer
	status     int
}

func newProjectPageProcessBufferedWriter(underlying http.ResponseWriter) *projectPageProcessBufferedWriter {
	return &projectPageProcessBufferedWriter{underlying: underlying, header: make(http.Header)}
}

func (w *projectPageProcessBufferedWriter) Header() http.Header { return w.header }
func (w *projectPageProcessBufferedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *projectPageProcessBufferedWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}
func (*projectPageProcessBufferedWriter) Flush() {}
func (w *projectPageProcessBufferedWriter) SetReadDeadline(deadline time.Time) error {
	return http.NewResponseController(w.underlying).SetReadDeadline(deadline)
}
func (w *projectPageProcessBufferedWriter) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(w.underlying).SetWriteDeadline(deadline)
}
func (w *projectPageProcessBufferedWriter) publish(target http.ResponseWriter) error {
	for name, values := range w.header {
		for _, value := range values {
			target.Header().Add(name, value)
		}
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	target.WriteHeader(status)
	if _, err := target.Write(w.body.Bytes()); err != nil {
		return err
	}
	return http.NewResponseController(target).Flush()
}

func runProjectPageProcessCLI(t *testing.T, binary string, environment []string, arguments ...string) selectedCacheCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = environment
	command.WaitDelay = 2 * time.Second
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
		t.Fatalf("selected project-page CLI exceeded bound: context=%v stdout=%t stderr=%t", ctx.Err(), stdout.exceeded, stderr.exceeded)
	}
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("selected project-page CLI process: %v", err)
		}
		code = exitError.ExitCode()
	}
	return selectedCacheCLIResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}
