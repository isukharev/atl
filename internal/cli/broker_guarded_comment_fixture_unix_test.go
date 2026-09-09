//go:build !windows

package cli

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokerconfig"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	guardedProcessAdminCredential       = "synthetic-workload-credential"
	guardedProcessWriterCredential      = "synthetic-writer-workload-credential"
	guardedProcessObserverCredential    = "synthetic-observer-workload-credential"
	guardedProcessReplacementCredential = "synthetic-replacement-workload-credential"
	guardedProcessForeignCredential     = "synthetic-foreign-workload-credential"
	guardedProcessAuthorityCredential   = "synthetic-authority-server-credential"
	guardedProcessJiraCredential        = "synthetic-jira-backend-credential"
	guardedProcessBody                  = "synthetic native *wiki* body\n"
	guardedProcessIssueID               = "101"
	guardedProcessIssueKey              = "PROJ-1"
	guardedProcessProject               = "PROJ"
	guardedProcessPostWireMaxBytes      = int64(1<<20) + 128
)

type guardedProcessBarrier struct {
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newGuardedProcessBarrier() *guardedProcessBarrier {
	return &guardedProcessBarrier{reached: make(chan struct{}), release: make(chan struct{})}
}

func (b *guardedProcessBarrier) arrive() {
	if b == nil {
		return
	}
	b.reachedOnce.Do(func() { close(b.reached) })
	<-b.release
}

func (b *guardedProcessBarrier) open() {
	if b != nil {
		b.releaseOnce.Do(func() { close(b.release) })
	}
}

type guardedProcessFixtureOptions struct {
	blockApplyOperationCall int
	operationBarrier        *guardedProcessBarrier
	postBarrier             *guardedProcessBarrier
	postMode                string
}

type guardedProcessAuthorityPolicy struct {
	commentProfile string
	outcomeProfile string
}

type guardedProcessJiraCounts struct {
	Qualification int
	Actor         int
	Issue         int
	Inventory     int
	Post          int
}

type guardedProcessCountSnapshot struct {
	Authentication map[string]int
	Discovery      int
	Admission      map[domain.BrokerOperationID]int
	Qualification  map[domain.BrokerOperationID]int
	Operation      map[domain.BrokerOperationID]int
	Proposal       map[domain.BrokerOperationID]int
	Jira           guardedProcessJiraCounts
}

type guardedProcessFixture struct {
	t                        *testing.T
	options                  guardedProcessFixtureOptions
	issuerSHA256             string
	executionNotBeforeMillis int64
	executionExpiresMillis   int64
	backend                  domain.BrokerBackendBinding
	hostConfigPath           string
	clientRoot               string
	writerSession            string
	observerSession          string
	bodyPath                 string
	adminAddress             string
	dataOrigin               string
	identity                 tls.Certificate
	authority                *httptest.Server
	jira                     *httptest.Server

	mu              sync.Mutex
	counts          guardedProcessCountSnapshot
	violations      []string
	posted          bool
	postedBody      string
	hidePostedReads bool
	discoveryAccess map[domain.BrokerOperationID]domain.BrokerDiscoveryAccess
	admissionDenial map[domain.BrokerOperationID]domain.BrokerReason
	authorityPolicy guardedProcessAuthorityPolicy
	approvedHash    string
	approvedTicket  string
	operationShapes map[string]domain.BrokerQualifiedResource
}

func newGuardedProcessFixture(t *testing.T, options guardedProcessFixtureOptions) *guardedProcessFixture {
	t.Helper()
	started := time.Now().UTC().Truncate(time.Millisecond)
	f := &guardedProcessFixture{
		t: t, options: options, issuerSHA256: strings.Repeat("a", 64),
		executionNotBeforeMillis: started.Add(-time.Second).UnixMilli(),
		executionExpiresMillis:   started.Add(2 * time.Minute).UnixMilli(),
		counts: guardedProcessCountSnapshot{
			Authentication: map[string]int{}, Admission: map[domain.BrokerOperationID]int{},
			Qualification: map[domain.BrokerOperationID]int{}, Operation: map[domain.BrokerOperationID]int{},
			Proposal: map[domain.BrokerOperationID]int{},
		},
		discoveryAccess: map[domain.BrokerOperationID]domain.BrokerDiscoveryAccess{},
		admissionDenial: map[domain.BrokerOperationID]domain.BrokerReason{},
		authorityPolicy: guardedProcessAuthorityPolicy{commentProfile: domain.BrokerJiraCommentQualificationProfileV1, outcomeProfile: "operation_ticket_v1"},
		operationShapes: map[string]domain.BrokerQualifiedResource{},
	}
	f.jira = httptest.NewTLSServer(http.HandlerFunc(f.serveJira))
	t.Cleanup(f.jira.Close)
	origin, err := backendid.OriginSHA256(f.jira.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.backend = domain.BrokerBackendBinding{
		Service: "jira", OriginSHA256: strings.TrimPrefix(origin, backendid.Prefix), WorkloadBackendID: "jira-primary",
	}
	f.authority = httptest.NewTLSServer(http.HandlerFunc(f.serveAuthority))
	t.Cleanup(f.authority.Close)
	// Cleanup is LIFO: release fixture barriers before waiting for servers,
	// including when a count assertion fails before the intended crash point.
	if options.operationBarrier != nil {
		t.Cleanup(options.operationBarrier.open)
	}
	if options.postBarrier != nil {
		t.Cleanup(options.postBarrier.open)
	}

	hostRoot := t.TempDir()
	if err := os.Chmod(hostRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	identityServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	f.identity = identityServer.TLS.Certificates[0]
	identityServer.Close()
	certificate, privateKey := brokerProcessTLSIdentity(t, f.identity)
	writeBrokerProcessFile(t, hostRoot, "server.crt", certificate)
	writeBrokerProcessFile(t, hostRoot, "server.key", privateKey)
	writeBrokerProcessFile(t, hostRoot, "authority.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.authority.Certificate().Raw}))
	writeBrokerProcessFile(t, hostRoot, "jira.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.jira.Certificate().Raw}))
	writeBrokerProcessFile(t, hostRoot, "authority.credential", []byte(guardedProcessAuthorityCredential))
	writeBrokerProcessFile(t, hostRoot, "jira.credential", []byte(guardedProcessJiraCredential))

	policy := []byte(`{"schema_version":1,"backend":{"jira_sha256":"` + origin + `"},"rules":[{"id":"selected-comment","effect":"allow","verbs":["comment"],"resource":{"service":"jira","kind":"issue","id":"101","project":"PROJ","key":"PROJ-1"}}]}`)
	policyDigest := sha256.Sum256(policy)
	writeBrokerProcessFile(t, hostRoot, "comment-policy.json", policy)
	f.hostConfigPath = filepath.Join(hostRoot, "broker.json")
	hostConfig := brokerconfig.Config{
		SchemaVersion: 1, BrokerID: "broker-1", DataAudience: "broker-data", AdminAudience: "broker-admin",
		DataListen: brokerProcessFreeAddress(t), AdminListen: brokerProcessFreeAddress(t),
		TLS:       brokerconfig.TLSFiles{CertificateFile: "server.crt", PrivateKeyFile: "server.key"},
		Authority: brokerconfig.Authority{BaseURL: f.authority.URL, IssuerSHA256: f.issuerSHA256, CredentialFile: "authority.credential", CAFile: "authority.ca"},
		Jira:      &brokerconfig.Backend{BaseURL: f.jira.URL, WorkloadBackendID: f.backend.WorkloadBackendID, CredentialFile: "jira.credential", CAFile: "jira.ca"},
		JiraComment: &brokerconfig.JiraComment{
			QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1,
			JournalDirectory:     "comment-journal", JournalRecords: 16, JournalReservedBytes: 64 << 20,
			LocalPolicyFile: "comment-policy.json", LocalPolicySHA256: hex.EncodeToString(policyDigest[:]),
		},
	}
	f.adminAddress = hostConfig.AdminListen
	f.dataOrigin = "https://" + hostConfig.DataListen
	hostBody, err := json.Marshal(hostConfig)
	if err != nil {
		t.Fatal(err)
	}
	writeBrokerProcessFile(t, hostRoot, "broker.json", hostBody)

	f.clientRoot = t.TempDir()
	if err := os.Chmod(f.clientRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writerSession = filepath.Join(f.clientRoot, "writer-session.json")
	f.observerSession = filepath.Join(f.clientRoot, "observer-session.json")
	f.writeSession(t, f.writerSession, "writer")
	f.writeSession(t, f.observerSession, "observer")
	f.bodyPath = filepath.Join(f.clientRoot, "comment.wiki")
	writeBrokerProcessFile(t, f.clientRoot, "comment.wiki", []byte(guardedProcessBody))
	brokerCA := filepath.Join(f.clientRoot, "broker.ca")
	writeBrokerProcessFile(t, f.clientRoot, "broker.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.identity.Certificate[0]}))
	clientConfig := map[string]any{
		"connection_mode": "broker", "jira_list_views": map[string]any{},
		"broker": map[string]any{
			"base_url": "https://" + hostConfig.DataListen, "broker_id": hostConfig.BrokerID, "audience": hostConfig.DataAudience,
			"ca_file": brokerCA, "jira_session_file": f.writerSession, "jira_observation_session_file": f.observerSession,
		},
	}
	clientBody, err := json.Marshal(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	writeBrokerProcessFile(t, f.clientRoot, "config.json", clientBody)
	return f
}

func (f *guardedProcessFixture) clientEnvironment() []string {
	return guardedProcessEnvironment(os.Environ(), map[string]string{
		"ATL_CONFIG_DIR": f.clientRoot, "ATL_NO_UPDATE": "1",
	})
}

func (f *guardedProcessFixture) daemonEnvironment() []string {
	return guardedProcessEnvironment(os.Environ(), map[string]string{"ATL_NO_UPDATE": "1"})
}

func (f *guardedProcessFixture) writeSession(t *testing.T, path, kind string) {
	t.Helper()
	credential, execution, epoch := guardedProcessSession(kind)
	body, err := json.Marshal(map[string]any{
		"schema_version": 1, "credential": credential, "execution_id": execution,
		"execution_epoch": epoch, "authority_revision": "revision-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func guardedProcessSession(kind string) (credential, execution, epoch string) {
	switch kind {
	case "writer":
		return guardedProcessWriterCredential, "writer-execution", "writer-epoch"
	case "observer":
		return guardedProcessObserverCredential, "observer-execution", "observer-epoch"
	case "replacement":
		return guardedProcessReplacementCredential, "writer-execution-2", "writer-epoch-2"
	case "foreign":
		return guardedProcessForeignCredential, "foreign-execution", "foreign-epoch"
	default:
		return "", "", ""
	}
}

func (f *guardedProcessFixture) setDiscoveryAccess(operation domain.BrokerOperationID, access domain.BrokerDiscoveryAccess) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoveryAccess[operation] = access
}

func (f *guardedProcessFixture) setAdmissionDenial(operation domain.BrokerOperationID, reason domain.BrokerReason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admissionDenial[operation] = reason
}

func (f *guardedProcessFixture) hideAcceptedPostFromReadback() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidePostedReads = true
}

func (f *guardedProcessFixture) approveGuardedProcessProposal(t *testing.T, preview guardedProcessCommentResult) {
	t.Helper()
	previewDigest, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentPreview, []byte(guardedProcessBody))
	if err != nil || preview.SchemaVersion != 1 || preview.Operation != string(domain.BrokerOperationJiraCommentPreview) ||
		preview.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 || preview.Mode != "preview" || preview.Status != "proposed" ||
		!guardedProcessDigest(preview.ArgumentsSHA256) || !guardedProcessDigest(preview.ProposalHash) || !guardedProcessIdentifier(preview.OperationTicket) ||
		preview.NativeCandidateSHA256 != previewDigest || !guardedProcessDigest(preview.VersionEvidenceSHA256) ||
		preview.WriteAttempted || !preview.Complete || preview.Reconciled {
		t.Fatalf("preview is not eligible for external synthetic approval: %+v", preview)
	}
	f.mu.Lock()
	f.approvedHash, f.approvedTicket = preview.ProposalHash, preview.OperationTicket
	f.mu.Unlock()
}

func (f *guardedProcessFixture) violationf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
}

func (f *guardedProcessFixture) assertNoViolations(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.violations) != 0 {
		t.Fatalf("synthetic guarded-comment fixture violations: %s", strings.Join(f.violations, "; "))
	}
}

func (f *guardedProcessFixture) snapshot() guardedProcessCountSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return guardedProcessCountSnapshot{
		Authentication: cloneGuardedProcessCounts(f.counts.Authentication), Discovery: f.counts.Discovery,
		Admission: cloneGuardedOperationCounts(f.counts.Admission), Qualification: cloneGuardedOperationCounts(f.counts.Qualification),
		Operation: cloneGuardedOperationCounts(f.counts.Operation), Proposal: cloneGuardedOperationCounts(f.counts.Proposal), Jira: f.counts.Jira,
	}
}

func cloneGuardedProcessCounts(source map[string]int) map[string]int {
	out := make(map[string]int, len(source))
	for key, value := range source {
		if value != 0 {
			out[key] = value
		}
	}
	return out
}

func cloneGuardedOperationCounts(source map[domain.BrokerOperationID]int) map[domain.BrokerOperationID]int {
	out := make(map[domain.BrokerOperationID]int, len(source))
	for key, value := range source {
		if value != 0 {
			out[key] = value
		}
	}
	return out
}

func guardedProcessCountDelta(after, before guardedProcessCountSnapshot) guardedProcessCountSnapshot {
	return guardedProcessCountSnapshot{
		Authentication: subtractGuardedStringCounts(after.Authentication, before.Authentication),
		Discovery:      after.Discovery - before.Discovery,
		Admission:      subtractGuardedOperationCounts(after.Admission, before.Admission),
		Qualification:  subtractGuardedOperationCounts(after.Qualification, before.Qualification),
		Operation:      subtractGuardedOperationCounts(after.Operation, before.Operation),
		Proposal:       subtractGuardedOperationCounts(after.Proposal, before.Proposal),
		Jira: guardedProcessJiraCounts{
			Qualification: after.Jira.Qualification - before.Jira.Qualification,
			Actor:         after.Jira.Actor - before.Jira.Actor, Issue: after.Jira.Issue - before.Jira.Issue,
			Inventory: after.Jira.Inventory - before.Jira.Inventory, Post: after.Jira.Post - before.Jira.Post,
		},
	}
}

func subtractGuardedStringCounts(after, before map[string]int) map[string]int {
	out := map[string]int{}
	for key, value := range after {
		if delta := value - before[key]; delta != 0 {
			out[key] = delta
		}
	}
	return out
}

func subtractGuardedOperationCounts(after, before map[domain.BrokerOperationID]int) map[domain.BrokerOperationID]int {
	out := map[domain.BrokerOperationID]int{}
	for key, value := range after {
		if delta := value - before[key]; delta != 0 {
			out[key] = delta
		}
	}
	return out
}

func (f *guardedProcessFixture) assertDelta(t *testing.T, before, want guardedProcessCountSnapshot) {
	t.Helper()
	got := guardedProcessCountDelta(f.snapshot(), before)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("guarded-comment call delta=%+v want=%+v", got, want)
	}
}

func guardedOperationCounts(operation domain.BrokerOperationID, count int) map[domain.BrokerOperationID]int {
	if count == 0 {
		return map[domain.BrokerOperationID]int{}
	}
	return map[domain.BrokerOperationID]int{operation: count}
}

func guardedAuthenticationCounts(kind string, count int) map[string]int {
	if count == 0 {
		return map[string]int{}
	}
	return map[string]int{kind: count}
}

func (f *guardedProcessFixture) serveAuthority(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+guardedProcessAuthorityCredential {
		f.violationf("authority method or server credential mismatch")
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := readGuardedProcessBody(request.Body, brokercontract.MaxEnvelopeBytes)
	if err != nil {
		f.violationf("authority request exceeded its fixture bound")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/v1/authenticate":
		f.serveAuthentication(writer, body)
	case "/v2/discovery":
		f.serveDiscovery(writer, body)
	case "/v1/authorize/admission":
		f.serveAdmission(writer, body)
	case "/v1/authorize/qualification":
		f.serveQualification(writer, body)
	case "/v1/authorize/operation":
		f.serveOperation(writer, body)
	case "/v1/authorize/proposal":
		f.serveProposal(writer, body)
	default:
		f.violationf("unexpected authority route")
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (f *guardedProcessFixture) serveAuthentication(writer http.ResponseWriter, body []byte) {
	value, err := brokertransport.DecodeAuthenticationRequestV1(body)
	if err != nil {
		f.violationf("authentication request failed strict decode")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	credential := value.Credential()
	defer clear(credential)
	kind, execution, epoch, principal, workload := guardedAuthenticationIdentity(string(credential))
	if kind == "" {
		f.violationf("authentication used an unknown workload credential")
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	f.mu.Lock()
	f.counts.Authentication[kind]++
	f.mu.Unlock()
	now := time.Now().UTC().Truncate(time.Millisecond)
	contextValue := domain.BrokerVerifiedContext{
		PrincipalID: principal, WorkloadID: workload, ExecutionID: execution, ExecutionEpoch: epoch,
		Audience: value.Audience, BrokerID: value.BrokerID, AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: f.executionNotBeforeMillis, ExecutionExpiresMillis: f.executionExpiresMillis,
		GrantExpiresMillis: f.executionExpiresMillis, CredentialExpiresMillis: f.executionExpiresMillis, Backend: f.backend,
	}
	response := brokertransport.AuthenticationResponse{
		SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: f.issuerSHA256,
		IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli(), Context: contextValue,
	}
	encoded, encodeErr := brokertransport.EncodeAuthenticationResponseV1(response)
	f.writeAuthorityResponse(writer, encoded, encodeErr, "authentication")
}

func guardedAuthenticationIdentity(credential string) (kind, execution, epoch, principal, workload string) {
	principal, workload = "principal-1", "workload-1"
	switch credential {
	case guardedProcessAdminCredential:
		return "admin", "admin-execution", "admin-epoch", principal, workload
	case guardedProcessWriterCredential:
		return "writer", "writer-execution", "writer-epoch", principal, workload
	case guardedProcessObserverCredential:
		return "observer", "observer-execution", "observer-epoch", principal, workload
	case guardedProcessReplacementCredential:
		return "replacement", "writer-execution-2", "writer-epoch-2", principal, workload
	case guardedProcessForeignCredential:
		return "foreign", "foreign-execution", "foreign-epoch", "principal-foreign", "workload-foreign"
	default:
		return "", "", "", "", ""
	}
}

func (f *guardedProcessFixture) validateAuthorityAdmission(request domain.BrokerAdmissionRequest) error {
	if request.OperationVersion != 1 || request.DeadlineMillis <= request.Context.ExecutionNotBeforeMillis ||
		request.DeadlineMillis > request.Context.ExecutionExpiresMillis || request.DeadlineMillis > request.Context.GrantExpiresMillis ||
		request.DeadlineMillis > request.Context.CredentialExpiresMillis || request.Context.Backend != f.backend {
		return fmt.Errorf("invalid fixed operation binding")
	}
	digest, err := brokercontract.ArgumentsSHA256(domain.BrokerRequest{
		SchemaVersion: 1, Operation: request.Operation, OperationVersion: request.OperationVersion,
		RequestID: request.RequestID, Features: append([]string(nil), request.Features...),
		Expect: domain.BrokerRequestExpectations{
			ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch, AuthorityRevision: request.Context.AuthorityRevision,
		},
		Arguments: request.Arguments,
	})
	if err != nil || digest != request.ArgumentsSHA256 {
		return fmt.Errorf("invalid argument binding")
	}
	role := guardedProcessAuthorityRole(request.Context)
	switch request.Operation {
	case domain.BrokerOperationJiraCommentPreview:
		if f.authorityPolicy.commentProfile != domain.BrokerJiraCommentQualificationProfileV1 || role != "writer" ||
			!reflect.DeepEqual(request.Features, []string{"guarded_proposal_v1"}) ||
			!guardedProcessCommentArguments(request.Arguments, false, "", "") {
			return fmt.Errorf("invalid preview authority policy")
		}
	case domain.BrokerOperationJiraCommentApply:
		f.mu.Lock()
		approvedHash, approvedTicket := f.approvedHash, f.approvedTicket
		f.mu.Unlock()
		if f.authorityPolicy.commentProfile != domain.BrokerJiraCommentQualificationProfileV1 || role != "writer" ||
			!reflect.DeepEqual(request.Features, []string{"durable_outcome_v1", "guarded_proposal_v1", "proposal_clearance_v1"}) ||
			!guardedProcessDigest(approvedHash) || !guardedProcessIdentifier(approvedTicket) ||
			!guardedProcessCommentArguments(request.Arguments, true, approvedHash, approvedTicket) {
			return fmt.Errorf("invalid apply authority policy")
		}
	case domain.BrokerOperationOutcomeLookup:
		if f.authorityPolicy.outcomeProfile != "operation_ticket_v1" || role != "observer" ||
			!reflect.DeepEqual(request.Features, []string{"durable_outcome_v1"}) || request.Arguments.Outcome == nil ||
			!guardedProcessIdentifier(request.Arguments.Outcome.OperationTicket) || request.Arguments.JiraComment != nil ||
			request.Arguments.JiraIssueRead != nil || request.Arguments.ConfluencePageRead != nil {
			return fmt.Errorf("invalid outcome authority policy")
		}
	default:
		return fmt.Errorf("unsupported synthetic authority operation")
	}
	return nil
}

func guardedProcessCommentArguments(arguments domain.BrokerOperationArguments, apply bool, proposalHash, ticket string) bool {
	comment := arguments.JiraComment
	return comment != nil && arguments.JiraIssueRead == nil && arguments.ConfluencePageRead == nil && arguments.Outcome == nil &&
		comment.IssueKey == guardedProcessIssueKey && len(comment.NativeBody) <= 1<<20 && utf8.Valid(comment.NativeBody) && len(bytes.TrimSpace(comment.NativeBody)) != 0 &&
		comment.SatisfactionPolicy == "append_always" && comment.ExpectedProposalHash == proposalHash && comment.OperationTicket == ticket &&
		apply == (proposalHash != "" && ticket != "")
}

func guardedProcessAuthorityRole(contextValue domain.BrokerVerifiedContext) string {
	if contextValue.Audience != "broker-data" || contextValue.BrokerID != "broker-1" || contextValue.AuthorityRevision != "revision-1" {
		return ""
	}
	principal, workload := "principal-1", "workload-1"
	if contextValue.ExecutionID == "foreign-execution" {
		principal, workload = "principal-foreign", "workload-foreign"
	}
	if contextValue.PrincipalID != principal || contextValue.WorkloadID != workload {
		return ""
	}
	switch contextValue.ExecutionID {
	case "writer-execution":
		if contextValue.ExecutionEpoch != "writer-epoch" {
			return ""
		}
		return "writer"
	case "writer-execution-2":
		if contextValue.ExecutionEpoch != "writer-epoch-2" {
			return ""
		}
		return "writer"
	case "observer-execution":
		if contextValue.ExecutionEpoch != "observer-epoch" {
			return ""
		}
		return "observer"
	case "foreign-execution":
		if contextValue.ExecutionEpoch != "foreign-epoch" {
			return ""
		}
		return "observer"
	default:
		return ""
	}
}

func (f *guardedProcessFixture) validateAuthorityQualification(request domain.BrokerQualificationRequest) error {
	if err := f.validateAuthorityAdmission(request.Admission); err != nil || request.Plan.SelectorSHA256 != request.Admission.ArgumentsSHA256 {
		return fmt.Errorf("invalid qualification admission")
	}
	wantFields := []string{"id", "key", "project", "updated"}
	wantLimits := domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}
	if request.Admission.Operation == domain.BrokerOperationOutcomeLookup {
		wantFields = []string{"operation_id"}
		wantLimits = domain.BrokerPhaseLimits{}
	}
	if !reflect.DeepEqual(request.Plan.MetadataFields, wantFields) || request.Plan.Limits != wantLimits {
		return fmt.Errorf("invalid qualification plan")
	}
	return nil
}

func (f *guardedProcessFixture) validateAuthorityOperation(request domain.BrokerOperationAuthorizationRequest) error {
	if err := f.validateAuthorityQualification(request.QualificationRequest); err != nil || len(request.QualifiedResources) != 1 {
		return fmt.Errorf("invalid operation qualification")
	}
	admission := request.QualificationRequest.Admission
	resource := request.QualifiedResources[0]
	var expectedEffects []domain.BrokerEffect
	switch admission.Operation {
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		if !guardedProcessJiraResource(resource) {
			return fmt.Errorf("invalid Jira comment resource")
		}
		expectedEffects = []domain.BrokerEffect{{
			Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"actor", "comments", "identity", "updated"},
		}}
		if admission.Operation == domain.BrokerOperationJiraCommentApply {
			expectedEffects = append(expectedEffects, domain.BrokerEffect{Kind: domain.BrokerEffectComment, Resource: resource, Fields: []string{"comments"}})
		}
	case domain.BrokerOperationOutcomeLookup:
		ticket := admission.Arguments.Outcome.OperationTicket
		expected, err := brokercontract.BrokerOperationObservationResourceV1(ticket)
		if err != nil || !reflect.DeepEqual(resource, expected) {
			return fmt.Errorf("invalid operation-ticket resource")
		}
		expectedEffects = []domain.BrokerEffect{{Kind: domain.BrokerEffectObserve, Resource: expected, Fields: []string{}}}
	default:
		return fmt.Errorf("unsupported operation authorization")
	}
	if !reflect.DeepEqual(request.Effects, expectedEffects) {
		return fmt.Errorf("invalid operation effects")
	}
	key := string(admission.Operation) + "\x00" + admission.RequestID
	f.mu.Lock()
	previous, exists := f.operationShapes[key]
	if !exists {
		f.operationShapes[key] = resource
	}
	f.mu.Unlock()
	if exists && !reflect.DeepEqual(previous, resource) {
		return fmt.Errorf("operation resource changed between decisions")
	}
	return nil
}

func guardedProcessJiraResource(resource domain.BrokerQualifiedResource) bool {
	if resource.Kind != domain.BrokerResourceJiraIssue || resource.ImmutableID != guardedProcessIssueID ||
		resource.Key != guardedProcessIssueKey || resource.Project != guardedProcessProject || resource.Space != "" ||
		resource.AncestorsPresent || resource.AncestorIDs == nil || len(resource.AncestorIDs) != 0 {
		return false
	}
	for _, updated := range []string{"2026-09-09T10:00:00Z", "2026-09-09T10:00:01Z"} {
		version, projection, err := brokercontract.JiraIssueIdentityEvidenceSHA256V1(domain.BrokerJiraIssueIdentity{
			ID: guardedProcessIssueID, Key: guardedProcessIssueKey, Project: guardedProcessProject, Updated: updated, Complete: true,
		})
		if err == nil && resource.VersionEvidence == version && resource.ProjectionSHA256 == projection {
			return true
		}
	}
	return false
}

func (f *guardedProcessFixture) validateAuthorityProposal(request domain.BrokerProposalAuthorizationRequest) error {
	if err := f.validateAuthorityOperation(request.OperationRequest); err != nil {
		return err
	}
	admission := request.OperationRequest.QualificationRequest.Admission
	f.mu.Lock()
	approvedHash, approvedTicket := f.approvedHash, f.approvedTicket
	f.mu.Unlock()
	nativeDigest, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, []byte(guardedProcessBody))
	resourcesDigest, resourcesErr := brokercontract.QualifiedResourcesSHA256(request.OperationRequest.QualifiedResources)
	effectsDigest, effectsErr := brokercontract.EffectsSHA256(request.OperationRequest.Effects)
	decision := request.OperationDecision
	if err != nil || resourcesErr != nil || effectsErr != nil || admission.Operation != domain.BrokerOperationJiraCommentApply ||
		request.ProposalSchemaVersion != 1 || request.ProposalHash != approvedHash || request.NativeCandidateSHA256 != nativeDigest ||
		!bytes.Equal(admission.Arguments.JiraComment.NativeBody, []byte(guardedProcessBody)) ||
		request.VersionEvidenceSHA256 != request.OperationRequest.QualifiedResources[0].VersionEvidence ||
		admission.Arguments.JiraComment.OperationTicket != approvedTicket || admission.Arguments.JiraComment.ExpectedProposalHash != approvedHash ||
		decision.Operation != admission.Operation || decision.OperationVersion != admission.OperationVersion || decision.ArgumentsSHA256 != admission.ArgumentsSHA256 ||
		decision.ResourcesSHA256 != resourcesDigest || decision.EffectsSHA256 != effectsDigest {
		return fmt.Errorf("invalid approved proposal binding")
	}
	return nil
}

func guardedProcessDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func guardedProcessIdentifier(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func (f *guardedProcessFixture) serveDiscovery(writer http.ResponseWriter, body []byte) {
	request, err := brokercontract.DecodeDiscoveryAuthorizationRequestV2(body)
	if err != nil || request.Context.Backend != f.backend || request.Request.Service != "jira" {
		f.violationf("discovery request failed strict binding")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.mu.Lock()
	f.counts.Discovery++
	access := make(map[domain.BrokerOperationID]domain.BrokerDiscoveryAccess, len(f.discoveryAccess))
	for operation, current := range f.discoveryAccess {
		access[operation] = current
	}
	f.mu.Unlock()
	projection := domain.BrokerDiscoveryProjectionV2{
		SchemaVersion: 2, RequestID: request.Request.RequestID, RequestSHA256: request.RequestSHA256,
		ContextSHA256: request.Request.ContextSHA256, ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch,
		Audience: request.Context.Audience, BrokerID: request.Context.BrokerID, AuthorityRevision: request.Context.AuthorityRevision,
		Service: "jira", RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(),
		DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(), IssuedAtMillis: now.UnixMilli(),
		ExpiresAtMillis: min(now.Add(4*time.Second).UnixMilli(), request.Request.NotAfterMillis), Complete: true,
	}
	for _, definition := range brokercontract.AvailableDefinitions() {
		if definition.BackendService != "jira" {
			continue
		}
		current := access[definition.ID]
		if current == "" {
			current = domain.BrokerDiscoveryAccessAllowed
		}
		projection.Operations = append(projection.Operations, domain.BrokerDiscoveryOperationV2{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: current,
			Features: append([]string(nil), definition.RequiredFeatures...), Limits: definition.Limits, Effects: definition.Effects,
		})
	}
	encoded, encodeErr := brokercontract.EncodeDiscoveryProjectionV2(projection)
	f.writeAuthorityResponse(writer, encoded, encodeErr, "discovery")
}

func (f *guardedProcessFixture) serveAdmission(writer http.ResponseWriter, body []byte) {
	request, err := brokercontract.DecodeAdmissionRequestV1(body)
	if err != nil || f.validateAuthorityAdmission(request) != nil {
		f.violationf("admission request failed strict binding")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.counts.Admission[request.Operation]++
	index := f.counts.Admission[request.Operation]
	reason := f.admissionDenial[request.Operation]
	f.mu.Unlock()
	requestSHA256, digestErr := brokercontract.AdmissionRequestSHA256(request)
	core, coreErr := guardedDecisionCore(domain.BrokerPhaseAdmission, request.Context, requestSHA256, request.DeadlineMillis, reason, index)
	encoded, encodeErr := brokercontract.EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{BrokerDecisionCore: core})
	f.writeAuthorityResponse(writer, encoded, firstGuardedError(digestErr, coreErr, encodeErr), "admission")
}

func (f *guardedProcessFixture) serveQualification(writer http.ResponseWriter, body []byte) {
	request, err := brokercontract.DecodeQualificationRequestV1(body)
	if err != nil || f.validateAuthorityQualification(request) != nil {
		f.violationf("qualification request failed strict binding")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	operation := request.Admission.Operation
	f.mu.Lock()
	f.counts.Qualification[operation]++
	index := f.counts.Qualification[operation]
	f.mu.Unlock()
	requestSHA256, requestErr := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, admissionErr := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, planErr := brokercontract.QualificationPlanSHA256(request.Plan)
	core, coreErr := guardedDecisionCore(domain.BrokerPhaseQualificationAuthorization, request.Admission.Context, requestSHA256, request.Admission.DeadlineMillis, "", index)
	decision := domain.BrokerQualificationDecision{
		BrokerDecisionCore: core, AdmissionRequestSHA256: admissionSHA256,
		AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256,
	}
	encoded, encodeErr := brokercontract.EncodeQualificationDecisionV1(decision)
	f.writeAuthorityResponse(writer, encoded, firstGuardedError(requestErr, admissionErr, planErr, coreErr, encodeErr), "qualification")
}

func (f *guardedProcessFixture) serveOperation(writer http.ResponseWriter, body []byte) {
	request, err := brokercontract.DecodeOperationAuthorizationRequestV1(body)
	if err != nil || f.validateAuthorityOperation(request) != nil {
		f.violationf("operation request failed strict binding")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	admission := request.QualificationRequest.Admission
	f.mu.Lock()
	f.counts.Operation[admission.Operation]++
	index := f.counts.Operation[admission.Operation]
	f.mu.Unlock()
	if admission.Operation == domain.BrokerOperationJiraCommentApply && index == f.options.blockApplyOperationCall {
		f.options.operationBarrier.arrive()
	}
	requestSHA256, requestErr := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, resourcesErr := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, effectsErr := brokercontract.EffectsSHA256(request.Effects)
	core, coreErr := guardedDecisionCore(domain.BrokerPhaseFinalAuthorization, admission.Context, requestSHA256, admission.DeadlineMillis, "", index)
	decision := domain.BrokerOperationDecision{
		BrokerDecisionCore: core, QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256,
		Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256,
	}
	encoded, encodeErr := brokercontract.EncodeOperationDecisionV1(decision)
	f.writeAuthorityResponse(writer, encoded, firstGuardedError(requestErr, resourcesErr, effectsErr, coreErr, encodeErr), "operation")
}

func (f *guardedProcessFixture) serveProposal(writer http.ResponseWriter, body []byte) {
	request, err := brokercontract.DecodeProposalAuthorizationRequestV1(body)
	if err != nil || f.validateAuthorityProposal(request) != nil {
		f.violationf("proposal request failed strict binding")
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	admission := request.OperationRequest.QualificationRequest.Admission
	f.mu.Lock()
	f.counts.Proposal[admission.Operation]++
	index := f.counts.Proposal[admission.Operation]
	f.mu.Unlock()
	requestSHA256, requestErr := brokercontract.ProposalAuthorizationRequestSHA256(request)
	core, coreErr := guardedDecisionCore(domain.BrokerPhaseProposalClearance, admission.Context, requestSHA256, admission.DeadlineMillis, "", index)
	clearance := domain.BrokerProposalClearance{
		BrokerDecisionCore: core, OperationDecisionSHA256: request.OperationDecision.DecisionSHA256,
		ProposalHash: request.ProposalHash, NativeCandidateSHA256: request.NativeCandidateSHA256,
		VersionEvidenceSHA256: request.VersionEvidenceSHA256,
	}
	encoded, encodeErr := brokercontract.EncodeProposalClearanceV1(clearance)
	f.writeAuthorityResponse(writer, encoded, firstGuardedError(requestErr, coreErr, encodeErr), "proposal")
}

func guardedDecisionCore(phase domain.BrokerAuthorizationPhase, contextValue domain.BrokerVerifiedContext, requestSHA256 string, deadline int64, reason domain.BrokerReason, index int) (domain.BrokerDecisionCore, error) {
	contextSHA256, err := brokercontract.VerifiedContextSHA256(contextValue)
	now := time.Now().UTC().Truncate(time.Millisecond).UnixMilli()
	expires := min(now+4_000, deadline, contextValue.ExecutionExpiresMillis, contextValue.GrantExpiresMillis, contextValue.CredentialExpiresMillis)
	status := domain.BrokerDecisionAllowed
	if reason != "" {
		status = domain.BrokerDecisionDenied
	}
	return domain.BrokerDecisionCore{
		Status: status, Reason: reason, DecisionID: fmt.Sprintf("%s-%d", phase, index), AuthorityRevision: contextValue.AuthorityRevision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: now, ExpiresAtMillis: expires,
	}, err
}

func firstGuardedError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *guardedProcessFixture) writeAuthorityResponse(writer http.ResponseWriter, body []byte, err error, phase string) {
	if err != nil || len(body) == 0 || int64(len(body)) > brokercontract.MaxEnvelopeBytes {
		f.violationf("%s response failed strict encoding", phase)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = writer.Write(body)
}

func (f *guardedProcessFixture) serveJira(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer "+guardedProcessJiraCredential {
		f.violationf("Jira credential mismatch")
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/rest/api/2/issue/"+guardedProcessIssueKey && request.URL.Query().Get("fields") == "project,updated" && len(request.URL.Query()) == 1:
		f.mu.Lock()
		f.counts.Jira.Qualification++
		posted := f.posted && !f.hidePostedReads
		f.mu.Unlock()
		f.writeJiraIssue(writer, posted)
	case request.Method == http.MethodGet && request.URL.Path == "/rest/api/2/myself" && request.URL.RawQuery == "":
		f.mu.Lock()
		f.counts.Jira.Actor++
		f.mu.Unlock()
		_, _ = io.WriteString(writer, `{"name":"writer","key":"writer-key"}`)
	case request.Method == http.MethodGet && request.URL.Path == "/rest/api/2/issue/"+guardedProcessIssueID && request.URL.Query().Get("fields") == "project,updated" && len(request.URL.Query()) == 1:
		f.mu.Lock()
		f.counts.Jira.Issue++
		posted := f.posted && !f.hidePostedReads
		f.mu.Unlock()
		f.writeJiraIssue(writer, posted)
	case request.Method == http.MethodGet && request.URL.Path == "/rest/api/2/issue/"+guardedProcessIssueID+"/comment" && request.URL.Query().Get("startAt") == "0" && request.URL.Query().Get("maxResults") == "100" && len(request.URL.Query()) == 2:
		f.mu.Lock()
		f.counts.Jira.Inventory++
		posted, postedBody := f.posted && !f.hidePostedReads, f.postedBody
		f.mu.Unlock()
		f.writeJiraInventory(writer, posted, postedBody)
	case request.Method == http.MethodPost && request.URL.Path == "/rest/api/2/issue/"+guardedProcessIssueID+"/comment" && request.URL.RawQuery == "":
		body, err := readGuardedProcessBody(request.Body, guardedProcessPostWireMaxBytes)
		comment, decodeErr := decodeGuardedProcessJiraPost(body)
		if err != nil || decodeErr != nil || comment != guardedProcessBody {
			f.violationf("Jira POST failed strict body validation")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.counts.Jira.Post++
		if f.options.postMode != "reject" {
			f.posted, f.postedBody = true, comment
		}
		if f.options.postMode == "lose-hidden" {
			f.hidePostedReads = true
		}
		f.mu.Unlock()
		f.options.postBarrier.arrive()
		switch f.options.postMode {
		case "reject":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"errors":{},"errorMessages":[]}`)
			return
		case "lose-hidden":
			connection, _, hijackErr := http.NewResponseController(writer).Hijack()
			if hijackErr != nil {
				f.violationf("could not drop the owned Jira response")
				return
			}
			_ = connection.Close()
			return
		case "":
		default:
			f.violationf("unknown Jira fixture POST mode")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(writer, `{"id":"10"}`)
	default:
		f.violationf("unexpected Jira method or route")
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (f *guardedProcessFixture) writeJiraIssue(writer http.ResponseWriter, posted bool) {
	updated := "2026-09-09T10:00:00Z"
	if posted {
		updated = "2026-09-09T10:00:01Z"
	}
	_, _ = fmt.Fprintf(writer, `{"id":"%s","key":"%s","fields":{"project":{"key":"%s"},"updated":"%s"}}`, guardedProcessIssueID, guardedProcessIssueKey, guardedProcessProject, updated)
}

func (f *guardedProcessFixture) writeJiraInventory(writer http.ResponseWriter, posted bool, postedBody string) {
	comments := []map[string]any{{
		"id": "9", "author": map[string]string{"name": "other", "key": "other-key"},
		"created": "2026-09-08T10:00:00Z", "updated": "2026-09-08T10:00:00Z", "body": "existing",
	}}
	if posted {
		comments = append(comments, map[string]any{
			"id": "10", "author": map[string]string{"name": "writer", "key": "writer-key"},
			"created": "2026-09-09T10:00:01Z", "updated": "2026-09-09T10:00:01Z", "body": postedBody,
		})
	}
	body, err := json.Marshal(map[string]any{"startAt": 0, "total": len(comments), "comments": comments})
	if err != nil || int64(len(body)) > brokercontract.MaxJiraCommentResponses {
		f.violationf("Jira inventory response exceeded its fixture bound")
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = writer.Write(body)
}

func readGuardedProcessBody(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil || maximum <= 0 {
		return nil, fmt.Errorf("invalid bounded body")
	}
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || len(body) == 0 || int64(len(body)) > maximum {
		return nil, fmt.Errorf("bounded body rejected")
	}
	return body, nil
}

func decodeGuardedProcessJiraPost(body []byte) (string, error) {
	if len(body) == 0 || int64(len(body)) > guardedProcessPostWireMaxBytes {
		return "", fmt.Errorf("invalid Jira POST body")
	}
	var value struct {
		Body string `json:"body"`
	}
	if strictjson.DecodeExact(body, 8, &value) != nil || value.Body == "" || len(value.Body) > 1<<20 {
		return "", fmt.Errorf("invalid Jira POST body")
	}
	return value.Body, nil
}

func guardedProcessEnvironment(base []string, overrides map[string]string) []string {
	blocked := map[string]bool{
		"GOROOT": true, "GOWORK": true, "GOTOOLCHAIN": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true,
	}
	for name := range overrides {
		blocked[name] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if blocked[name] || strings.HasPrefix(name, "ATL_") || strings.HasPrefix(name, "JIRA_") || strings.HasPrefix(name, "CONFLUENCE_") {
			continue
		}
		out = append(out, value)
	}
	for name, value := range overrides {
		out = append(out, name+"="+value)
	}
	return out
}

func TestGuardedCommentProcessFixtureRejectsAmbiguousJiraPost(t *testing.T) {
	valid, err := json.Marshal(map[string]string{"body": guardedProcessBody})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := decodeGuardedProcessJiraPost(valid); err != nil || body != guardedProcessBody {
		t.Fatalf("valid body=%q err=%v", body, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"body":"one","body":"two"}`),
		[]byte(`{"body":"one","extra":true}`),
		[]byte(`{"body":"one"}{}`),
		[]byte(`{"body":""}`),
		[]byte(strconv.Quote(strings.Repeat("x", int(guardedProcessPostWireMaxBytes)+1))),
	} {
		if _, err := decodeGuardedProcessJiraPost(invalid); err == nil {
			t.Fatalf("ambiguous Jira POST fixture input accepted: size=%d", len(invalid))
		}
	}
}
