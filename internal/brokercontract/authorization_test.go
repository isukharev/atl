package brokercontract

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

func TestRequestContextMatchingAndTemporalReasons(t *testing.T) {
	request, verified := fixtureRequest(), fixtureContext()
	if err := MatchRequestContextV1(request, verified, 2000, 30000); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*domain.BrokerRequest, *domain.BrokerVerifiedContext)
		now    int64
		limit  int64
		reason domain.BrokerReason
	}{
		{name: "execution id", mutate: func(r *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) { r.Expect.ExecutionID = "other" }, now: 2000, limit: 30000, reason: domain.BrokerReasonStaleExecution},
		{name: "execution epoch", mutate: func(r *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) { r.Expect.ExecutionEpoch = "other" }, now: 2000, limit: 30000, reason: domain.BrokerReasonStaleExecution},
		{name: "authority revision", mutate: func(r *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) { r.Expect.AuthorityRevision = "other" }, now: 2000, limit: 30000, reason: domain.BrokerReasonStaleAuthority},
		{name: "backend service", mutate: func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext) { c.Backend.Service = "confluence" }, now: 2000, limit: 30000, reason: domain.BrokerReasonUnsupported},
		{name: "execution expiry", mutate: func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext) { c.ExecutionExpiresMillis = 2000 }, now: 2000, limit: 30000, reason: domain.BrokerReasonStaleExecution},
		{name: "credential expiry", mutate: func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext) { c.CredentialExpiresMillis = 2000 }, now: 2000, limit: 30000, reason: domain.BrokerReasonCredentialExpired},
		{name: "grant expiry", mutate: func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext) { c.GrantExpiresMillis = 2000 }, now: 2000, limit: 30000, reason: domain.BrokerReasonGrantExpired},
		{name: "deadline too long", mutate: func(*domain.BrokerRequest, *domain.BrokerVerifiedContext) {}, now: 2000, limit: 62001, reason: domain.BrokerReasonDecisionExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, c := cloneRequest(request), verified
			test.mutate(&r, &c)
			err := MatchRequestContextV1(r, c, test.now, test.limit)
			if reason, ok := Reason(err); !ok || reason != test.reason {
				t.Fatalf("error=%v reason=%q/%t", err, reason, ok)
			}
		})
	}
}

func TestAdmissionDecisionAndProposalClearanceAreDigestBound(t *testing.T) {
	admission := fixtureAdmissionRequest()
	decoded := fixtureAllowedDecision(t)
	wire, err := EncodeAdmissionDecisionV1(decoded)
	if err != nil || !validDigest(decoded.DecisionSHA256) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if err := ValidateAdmissionDecisionForV1(decoded, admission, 3000); err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(wire, []byte(`"authority_revision":"revision-1"`), []byte(`"authority_revision":"revision-2"`), 1)
	if _, err := DecodeAdmissionDecisionV1(tampered); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("tampered decision error=%v", err)
	}

	proposal, clearance := fixtureProposalAuthorization(t)
	wire, err = EncodeProposalClearanceV1(clearance)
	if err != nil {
		t.Fatal(err)
	}
	decodedClearance, err := DecodeProposalClearanceV1(wire)
	if err != nil || !validDigest(decodedClearance.ClearanceSHA256) {
		t.Fatalf("decoded=%+v err=%v", decodedClearance, err)
	}
	if err := ValidateProposalClearanceForV1(decodedClearance, proposal, 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeProposalClearanceV1(bytes.Replace(wire, []byte(`"decision_id":"clearance-1"`), []byte(`"decision_id":"clearance-2"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("tampered clearance error=%v", err)
	}
}

func TestOperationPermissionCannotSubstituteForProposalClearance(t *testing.T) {
	base, _ := fixtureProposalAuthorization(t)
	if err := ValidateProposalAuthorizationRequestV1(base); err != nil {
		t.Fatal(err)
	}
	tests := []domain.BrokerProposalAuthorizationRequest{base, base, base, base}
	tests[0].ProposalHash = ""
	tests[1].NativeCandidateSHA256 = digestChar('0')[:63]
	tests[2].OperationDecision.Status = domain.BrokerDecisionDenied
	tests[2].OperationDecision.Reason = domain.BrokerReasonDenied
	tests[3].OperationDecision.ResourcesSHA256 = ""
	for index, value := range tests {
		err := ValidateProposalAuthorizationRequestV1(value)
		if reason, _ := Reason(err); reason != domain.BrokerReasonProposalClearanceRequired {
			t.Fatalf("case %d error=%v reason=%q", index, err, reason)
		}
	}
}

func TestPhaseDecisionsCannotCrossContextRequestOrProposal(t *testing.T) {
	admission := fixtureAdmissionRequest()
	admissionDecision := fixtureAllowedDecision(t)
	if err := ValidateAdmissionDecisionForV1(admissionDecision, admission, 3000); err != nil {
		t.Fatal(err)
	}
	changedPrincipal := admission
	changedPrincipal.Context.PrincipalID = "principal-2"
	changedBackend := admission
	changedBackend.Context.Backend.WorkloadBackendID = "jira-secondary"
	changedRequest := admission
	changedRequest.RequestID = "request-2"
	for name, candidate := range map[string]domain.BrokerAdmissionRequest{"principal": changedPrincipal, "backend": changedBackend, "request": changedRequest} {
		if err := ValidateAdmissionDecisionForV1(admissionDecision, candidate, 3000); err == nil {
			t.Errorf("%s substitution accepted", name)
		}
	}

	qualification, qualificationDecision := fixtureQualification(t)
	if err := ValidateQualificationDecisionForV1(qualificationDecision, qualification, 3000); err != nil {
		t.Fatal(err)
	}
	qualification.Plan.MetadataFields = []string{"id", "key", "updated"}
	if err := ValidateQualificationDecisionForV1(qualificationDecision, qualification, 3000); err == nil {
		t.Fatal("changed qualification plan accepted")
	}

	operation, operationDecision := fixtureOperationAuthorization(t)
	if err := ValidateOperationDecisionForV1(operationDecision, operation, 3000); err != nil {
		t.Fatal(err)
	}
	operation.Effects[0].Fields = []string{"summary"}
	if err := ValidateOperationDecisionForV1(operationDecision, operation, 3000); err == nil {
		t.Fatal("changed effect set accepted")
	}

	proposal, clearance := fixtureProposalAuthorization(t)
	if err := ValidateProposalClearanceForV1(clearance, proposal, 3000); err != nil {
		t.Fatal(err)
	}
	proposal.ProposalHash = digestChar('8')
	if err := ValidateProposalClearanceForV1(clearance, proposal, 3000); err == nil {
		t.Fatal("changed proposal accepted")
	}
	proposal.ProposalHash = clearance.ProposalHash
	freshClearance := clearance
	freshClearance.IssuedAtMillis, freshClearance.ExpiresAtMillis, freshClearance.ClearanceSHA256 = 8000, 9000, ""
	wire, err := EncodeProposalClearanceV1(freshClearance)
	if err != nil {
		t.Fatal(err)
	}
	freshClearance, err = DecodeProposalClearanceV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProposalClearanceForV1(freshClearance, proposal, 8500); !errors.Is(err, domain.ErrCheckFailed) {
		reason, _ := Reason(err)
		t.Fatalf("fresh clearance extended expired operation decision: %v reason=%s", err, reason)
	}
}

func TestDecisionDenialUnavailableExpiryAndFeatureMismatchStayClosed(t *testing.T) {
	admission := fixtureAdmissionRequest()
	allowed := fixtureAllowedDecision(t)
	for _, test := range []struct {
		name   string
		status domain.BrokerDecisionStatus
		reason domain.BrokerReason
		want   error
	}{
		{"revoked", domain.BrokerDecisionDenied, domain.BrokerReasonRevoked, domain.ErrForbidden},
		{"unavailable", domain.BrokerDecisionUnavailable, domain.BrokerReasonAuthorizationUnavailable, domain.ErrCheckFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := allowed
			decision.Status, decision.Reason, decision.DecisionSHA256 = test.status, test.reason, ""
			wire, err := EncodeAdmissionDecisionV1(decision)
			decoded, decodeErr := DecodeAdmissionDecisionV1(wire)
			validationErr := ValidateAdmissionDecisionForV1(decoded, admission, 3000)
			if err != nil || decodeErr != nil || !errors.Is(validationErr, test.want) {
				t.Fatalf("errors=%v/%v/%v", err, decodeErr, validationErr)
			}
			if reason, _ := Reason(validationErr); reason != test.reason {
				t.Fatalf("reason=%s want=%s", reason, test.reason)
			}
		})
	}
	if err := ValidateAdmissionDecisionForV1(allowed, admission, allowed.ExpiresAtMillis); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("expired decision error=%v", err)
	}
	request := fixtureRequest()
	request.Features = []string{"unknown_v1"}
	if _, err := EncodeRequestV1(request); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("feature mismatch error=%v", err)
	}
}

func TestContractErrorsAreClosedAndContentFree(t *testing.T) {
	private := "PRIVATE-CANARY.example.invalid/secret"
	for _, reason := range []domain.BrokerReason{
		domain.BrokerReasonMalformed, domain.BrokerReasonUnsupported, domain.BrokerReasonDenied, domain.BrokerReasonRevoked,
		domain.BrokerReasonCredentialExpired, domain.BrokerReasonAuthorizationUnavailable, domain.BrokerReasonOutcomeUnknown,
	} {
		err := reject(reason)
		if strings.Contains(err.Error(), private) || err.Error() != "broker contract rejected" {
			t.Fatalf("reason=%s error=%q", reason, err)
		}
		if reason == domain.BrokerReasonOutcomeUnknown {
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
				t.Fatalf("outcome error=%v", err)
			}
		}
	}
}

func TestBrokerReasonsMapToExistingRecoveryVocabulary(t *testing.T) {
	tests := map[domain.BrokerReason]diagnostic.RecoveryAction{
		domain.BrokerReasonMalformed:         diagnostic.RecoveryAdjustRequest,
		domain.BrokerReasonUnsupported:       diagnostic.RecoveryAdjustRequest,
		domain.BrokerReasonDenied:            diagnostic.RecoveryRequestAccess,
		domain.BrokerReasonRevoked:           diagnostic.RecoveryRequestAccess,
		domain.BrokerReasonCredentialExpired: diagnostic.RecoveryReauthenticate,
		// Refresh execution/session before an explicit new operation. This does
		// not authorize replay or alter the v1 transport recovery mapping.
		domain.BrokerReasonStaleExecution:            diagnostic.RecoveryReauthenticate,
		domain.BrokerReasonAuthorizationUnavailable:  diagnostic.RecoveryInspectFailure,
		domain.BrokerReasonProposalClearanceRequired: diagnostic.RecoveryRequestHumanApproval,
		domain.BrokerReasonOutcomeUnknown:            diagnostic.RecoveryReconcileWriteOutcome,
	}
	for reason, want := range tests {
		if got := diagnostic.Recover(reject(reason), diagnostic.OperationWrite); got.Action != want || got.RetrySafe {
			t.Errorf("reason=%s recovery=%+v want action=%s", reason, got, want)
		}
	}
}

func TestAuthorizationPhaseOrderCannotSkipFinalOrProposalChecks(t *testing.T) {
	readPath := []domain.BrokerAuthorizationPhase{
		domain.BrokerPhaseStrictDecode, domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization,
		domain.BrokerPhaseQualification, domain.BrokerPhaseFinalAuthorization, domain.BrokerPhaseBusinessOperation,
	}
	for index := 1; index < len(readPath); index++ {
		if err := ValidatePhaseTransitionV1(readPath[index-1], readPath[index], false); err != nil {
			t.Fatalf("read transition %s -> %s: %v", readPath[index-1], readPath[index], err)
		}
	}
	writePath := []domain.BrokerAuthorizationPhase{
		domain.BrokerPhaseStrictDecode, domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization,
		domain.BrokerPhaseQualification, domain.BrokerPhaseFinalAuthorization, domain.BrokerPhaseProposalClearance,
		domain.BrokerPhaseBusinessOperation, domain.BrokerPhaseOutcomeObservation,
	}
	for index := 1; index < len(writePath); index++ {
		if err := ValidatePhaseTransitionV1(writePath[index-1], writePath[index], true); err != nil {
			t.Fatalf("write transition %s -> %s: %v", writePath[index-1], writePath[index], err)
		}
	}
	for _, transition := range [][2]domain.BrokerAuthorizationPhase{
		{domain.BrokerPhaseStrictDecode, domain.BrokerPhaseBusinessOperation},
		{domain.BrokerPhaseAdmission, domain.BrokerPhaseFinalAuthorization},
		{domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseBusinessOperation},
		{domain.BrokerPhaseFinalAuthorization, domain.BrokerPhaseBusinessOperation},
	} {
		if err := ValidatePhaseTransitionV1(transition[0], transition[1], true); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("write transition %s -> %s was accepted", transition[0], transition[1])
		}
	}
}
