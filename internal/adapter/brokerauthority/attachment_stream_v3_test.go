package brokerauthority

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type authorityAttachmentFixture struct {
	admission             domain.BrokerAttachmentAdmissionRequestV3
	admissionDecision     domain.BrokerAttachmentAdmissionDecisionV3
	qualification         domain.BrokerAttachmentQualificationRequestV3
	qualificationDecision domain.BrokerAttachmentQualificationDecisionV3
	operation             domain.BrokerAttachmentOperationAuthorizationRequestV3
	operationDecision     domain.BrokerAttachmentOperationDecisionV3
}

func TestAuthorityAttachmentV3UsesExactRoutesHeadersAndPhaseCodecs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	admissionResponse := mustAuthorityCall(t, func() ([]byte, error) {
		return brokercontract.EncodeAttachmentAdmissionDecisionV3(fixture.admissionDecision)
	})
	qualificationResponse := mustAuthorityCall(t, func() ([]byte, error) {
		return brokercontract.EncodeAttachmentQualificationDecisionV3(fixture.qualificationDecision)
	})
	operationResponse := mustAuthorityCall(t, func() ([]byte, error) {
		return brokercontract.EncodeAttachmentOperationDecisionV3(fixture.operationDecision)
	})
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-server-credential" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request method=%s path=%s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case brokertransport.AuthorizeAttachmentAdmissionPathV3:
			decoded, err := brokercontract.DecodeAttachmentAdmissionRequestV3(body)
			if err != nil || !reflect.DeepEqual(decoded, fixture.admission) {
				t.Errorf("admission=%+v err=%v", decoded, err)
			}
			_, _ = writer.Write(admissionResponse)
		case brokertransport.AuthorizeAttachmentQualificationPathV3:
			decoded, err := brokercontract.DecodeAttachmentQualificationRequestV3(body)
			if err != nil || !reflect.DeepEqual(decoded, fixture.qualification) || decoded.Phase != domain.BrokerAttachmentQualificationInitial {
				t.Errorf("qualification=%+v err=%v", decoded, err)
			}
			_, _ = writer.Write(qualificationResponse)
		case brokertransport.AuthorizeAttachmentOperationPathV3:
			decoded, err := brokercontract.DecodeAttachmentOperationAuthorizationRequestV3(body)
			if err != nil || !reflect.DeepEqual(decoded, fixture.operation) || decoded.Phase != domain.BrokerAttachmentOperationInitial {
				t.Errorf("operation=%+v err=%v", decoded, err)
			}
			_, _ = writer.Write(operationResponse)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }
	parent, _ := domain.NewReadBudget(3, 3*brokercontract.MaxAttachmentAuthorityCallBytesV3)
	ctx := domain.WithReadBudget(t.Context(), parent)

	admission, admissionErr := authority.AdmitAttachment(ctx, fixture.admission)
	qualification, qualificationErr := authority.AuthorizeAttachmentQualification(ctx, fixture.qualification)
	operation, operationErr := authority.AuthorizeAttachmentOperation(ctx, fixture.operation)
	if admissionErr != nil || qualificationErr != nil || operationErr != nil ||
		brokercontract.ValidateAttachmentAdmissionDecisionV3(admission, fixture.admission, now.UnixMilli()) != nil ||
		brokercontract.ValidateAttachmentQualificationDecisionV3(qualification, fixture.qualification, now.UnixMilli(), fixture.admission.DeadlineMillis) != nil ||
		brokercontract.ValidateAttachmentOperationDecisionV3(operation, fixture.operation, now.UnixMilli(), fixture.admission.DeadlineMillis) != nil {
		t.Fatalf("decisions=%+v/%+v/%+v errors=%v/%v/%v", admission, qualification, operation, admissionErr, qualificationErr, operationErr)
	}
	if !reflect.DeepEqual(paths, []string{brokertransport.AuthorizeAttachmentAdmissionPathV3, brokertransport.AuthorizeAttachmentQualificationPathV3, brokertransport.AuthorizeAttachmentOperationPathV3}) || parent.Usage().Attempts != 3 {
		t.Fatalf("paths=%v parent=%+v", paths, parent.Usage())
	}
}

func TestAuthorityAttachmentV3ReturnsSignedDenialForCurrentValidator(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	denied := fixture.admissionDecision
	denied.Status, denied.Reason, denied.DecisionSHA256 = domain.BrokerDecisionDenied, domain.BrokerReasonDenied, ""
	denied = signAttachmentAdmissionDecision(t, denied)
	response := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentAdmissionDecisionV3(denied) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(response)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	got, err := authority.AdmitAttachment(t.Context(), fixture.admission)
	if err != nil || got.Status != domain.BrokerDecisionDenied || got.Reason != domain.BrokerReasonDenied {
		t.Fatalf("decision=%+v err=%v", got, err)
	}
	if err := brokercontract.ValidateAttachmentAdmissionDecisionV3(got, fixture.admission, now.UnixMilli()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("current validator err=%v", err)
	}
}

func TestAuthorityAttachmentV3LeavesBindingAndExpiryToCurrentValidator(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fixture := newAuthorityAttachmentFixture(t, now)
	for _, test := range []struct {
		name       string
		response   func() []byte
		wantDecode bool
		wantReason domain.BrokerReason
	}{
		{name: "wrong binding", wantDecode: true, wantReason: domain.BrokerReasonStaleAuthority, response: func() []byte {
			changed := fixture.admissionDecision
			changed.RequestSHA256, changed.DecisionSHA256 = strings.Repeat("f", 64), ""
			return mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentAdmissionDecisionV3(changed) })
		}},
		{name: "expired", wantDecode: true, wantReason: domain.BrokerReasonDecisionExpired, response: func() []byte {
			changed := fixture.admissionDecision
			changed.IssuedAtMillis, changed.ExpiresAtMillis, changed.DecisionSHA256 = now.Add(-900*time.Millisecond).UnixMilli(), now.Add(-time.Millisecond).UnixMilli(), ""
			return mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentAdmissionDecisionV3(changed) })
		}},
		{name: "malformed", response: func() []byte {
			valid := mustAuthorityCall(t, func() ([]byte, error) {
				return brokercontract.EncodeAttachmentAdmissionDecisionV3(fixture.admissionDecision)
			})
			return bytes.Replace(valid, []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = writer.Write(test.response())
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			got, err := authority.AdmitAttachment(t.Context(), fixture.admission)
			if test.wantDecode {
				if err != nil || calls.Load() != 1 {
					t.Fatalf("decision=%+v err=%v calls=%d", got, err, calls.Load())
				}
				validationErr := brokercontract.ValidateAttachmentAdmissionDecisionV3(got, fixture.admission, now.UnixMilli())
				if reason, ok := brokercontract.Reason(validationErr); !ok || reason != test.wantReason {
					t.Fatalf("validator err=%v reason=%s", validationErr, reason)
				}
			} else if err == nil || calls.Load() != 1 {
				t.Fatalf("malformed decision=%+v err=%v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func newAuthorityAttachmentFixture(t testing.TB, now time.Time) authorityAttachmentFixture {
	t.Helper()
	contextValue := authorityVerifiedContext(now)
	arguments := domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-1", AttachmentID: "200"}
	argumentsDigest, _ := brokercontract.AttachmentArgumentsSHA256V3(arguments)
	admission := domain.BrokerAttachmentAdmissionRequestV3{Context: contextValue, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1, RequestID: "request-1", Features: []string{"atomic_local_publish_v1", "attachment_id_v1", "step_snapshot_v1"}, Arguments: arguments, ArgumentsSHA256: argumentsDigest, DeadlineMillis: now.Add(time.Minute).UnixMilli()}
	admissionDigest, _ := brokercontract.AttachmentAdmissionRequestSHA256V3(admission)
	contextDigest, _ := brokercontract.VerifiedContextSHA256(contextValue)
	admissionDecision := signAttachmentAdmissionDecision(t, domain.BrokerAttachmentAdmissionDecisionV3{BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "admission-1", AuthorityRevision: contextValue.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: admissionDigest, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli()}})
	plan := domain.BrokerAttachmentMetadataPlanV3{SelectorSHA256: argumentsDigest, MetadataFields: []string{"attachment.created", "attachment.filename", "attachment.id", "attachment.media_type", "attachment.parent_id", "attachment.size", "issue.id", "issue.key", "issue.project", "issue.updated"}, Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 1 << 20}}
	qualification := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationInitial, Initial: &domain.BrokerAttachmentInitialQualificationV3{Admission: admission, AdmissionDecision: admissionDecision, Plan: plan}}
	qualificationDigest, _ := brokercontract.AttachmentQualificationRequestSHA256V3(qualification)
	planDigest, _ := brokercontract.AttachmentMetadataPlanSHA256V3(plan)
	qualificationDecision := signAttachmentQualificationDecision(t, domain.BrokerAttachmentQualificationDecisionV3{Phase: qualification.Phase, BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "qualification-1", AuthorityRevision: contextValue.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: qualificationDigest, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli()}, PlanSHA256: planDigest})
	snapshot, err := brokercontract.NewAttachmentSnapshotEvidenceV3(domain.BrokerJiraAttachmentSnapshotV3{IssueID: "100", IssueKey: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T00:00:00Z", AttachmentID: "200", ParentID: "100", Filename: "example.bin", MediaType: "application/octet-stream", Created: "2026-09-09T00:00:00Z", DeclaredSize: 23})
	if err != nil {
		t.Fatal(err)
	}
	effects := []domain.BrokerAttachmentEffectV3{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"id", "key", "project", "updated"}}, {Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraAttachment, Fields: []string{"body", "created", "filename", "id", "media_type", "parent_id", "size"}}}
	operation := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationInitial, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: qualification, QualificationDecision: qualificationDecision, Snapshot: snapshot, Effects: effects}}
	operationDigest, _ := brokercontract.AttachmentOperationAuthorizationRequestSHA256V3(operation)
	resourcesDigest, _ := brokercontract.AttachmentResourcesSHA256V3(snapshot)
	effectsDigest, _ := brokercontract.AttachmentEffectsSHA256V3(effects)
	operationDecision := signAttachmentOperationDecision(t, domain.BrokerAttachmentOperationDecisionV3{Phase: operation.Phase, BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "operation-1", AuthorityRevision: contextValue.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: operationDigest, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli()}, QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: argumentsDigest, ResourcesSHA256: resourcesDigest, EffectsSHA256: effectsDigest})
	return authorityAttachmentFixture{admission, admissionDecision, qualification, qualificationDecision, operation, operationDecision}
}

func signAttachmentAdmissionDecision(t testing.TB, value domain.BrokerAttachmentAdmissionDecisionV3) domain.BrokerAttachmentAdmissionDecisionV3 {
	t.Helper()
	body := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentAdmissionDecisionV3(value) })
	decoded, err := brokercontract.DecodeAttachmentAdmissionDecisionV3(body)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func signAttachmentQualificationDecision(t testing.TB, value domain.BrokerAttachmentQualificationDecisionV3) domain.BrokerAttachmentQualificationDecisionV3 {
	t.Helper()
	body := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentQualificationDecisionV3(value) })
	decoded, err := brokercontract.DecodeAttachmentQualificationDecisionV3(body)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func signAttachmentOperationDecision(t testing.TB, value domain.BrokerAttachmentOperationDecisionV3) domain.BrokerAttachmentOperationDecisionV3 {
	t.Helper()
	body := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeAttachmentOperationDecisionV3(value) })
	decoded, err := brokercontract.DecodeAttachmentOperationDecisionV3(body)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustAuthorityBytes(t testing.TB, body []byte, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustAuthorityCall(t testing.TB, call func() ([]byte, error)) []byte {
	t.Helper()
	body, err := call()
	return mustAuthorityBytes(t, body, err)
}
