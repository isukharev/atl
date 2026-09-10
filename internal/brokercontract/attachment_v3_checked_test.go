package brokercontract

import (
	"encoding/json"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentV3DeepAncestorBindingMutationIsMalformedAtEveryRequestBoundary(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)

	// Isolate the admission binding before testing deeper envelopes: there is
	// no enclosing qualification/operation decision whose stale hash could
	// incidentally reject the mutation if the admission check were missing.
	t.Run("initial_qualification", func(t *testing.T) {
		request := mustAttachmentDecode(t, fixture.initialQ, EncodeAttachmentQualificationRequestV3, DecodeAttachmentQualificationRequestV3)
		request.Initial.AdmissionDecision = attachmentAdmissionDecisionWithWrongRequestBindingV3(t, request.Initial.AdmissionDecision)
		if _, err := EncodeAttachmentQualificationRequestV3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("encode reason=%v", err)
		}
		if _, err := AttachmentQualificationRequestSHA256V3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("hash reason=%v", err)
		}
		if err := ValidateAttachmentQualificationDecisionV3(fixture.initialQD, request, 2_100, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("validate reason=%v", err)
		}
		wire := mutateDeepAdmissionRequestBindingWireV3(t, mustAttachmentEncode(t, fixture.initialQ, EncodeAttachmentQualificationRequestV3))
		if _, err := DecodeAttachmentQualificationRequestV3(wire); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("decode reason=%v", err)
		}
	})

	t.Run("pre_open_qualification", func(t *testing.T) {
		request := mustAttachmentDecode(t, fixture.preOpenQ, EncodeAttachmentQualificationRequestV3, DecodeAttachmentQualificationRequestV3)
		request.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial.AdmissionDecision = attachmentAdmissionDecisionWithWrongRequestBindingV3(t, request.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial.AdmissionDecision)

		if _, err := EncodeAttachmentQualificationRequestV3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("encode reason=%v", err)
		}
		if _, err := AttachmentQualificationRequestSHA256V3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("hash reason=%v", err)
		}
		if err := ValidateAttachmentQualificationDecisionV3(fixture.preOpenQD, request, 2_300, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("validate reason=%v", err)
		}

		wire := mutateDeepAdmissionRequestBindingWireV3(t, mustAttachmentEncode(t, fixture.preOpenQ, EncodeAttachmentQualificationRequestV3))
		if _, err := DecodeAttachmentQualificationRequestV3(wire); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("decode reason=%v", err)
		}
	})

	t.Run("body_dispatch_operation", func(t *testing.T) {
		request := mustAttachmentDecode(t, fixture.bodyO, EncodeAttachmentOperationAuthorizationRequestV3, DecodeAttachmentOperationAuthorizationRequestV3)
		request.Qualified.QualificationRequest.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial.AdmissionDecision = attachmentAdmissionDecisionWithWrongRequestBindingV3(t, request.Qualified.QualificationRequest.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial.AdmissionDecision)

		if _, err := EncodeAttachmentOperationAuthorizationRequestV3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("encode reason=%v", err)
		}
		if _, err := AttachmentOperationAuthorizationRequestSHA256V3(request); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("hash reason=%v", err)
		}
		if err := ValidateAttachmentOperationDecisionV3(fixture.bodyOD, request, 2_400, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("validate reason=%v", err)
		}

		wire := mutateDeepAdmissionRequestBindingWireV3(t, mustAttachmentEncode(t, fixture.bodyO, EncodeAttachmentOperationAuthorizationRequestV3))
		if _, err := DecodeAttachmentOperationAuthorizationRequestV3(wire); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
			t.Fatalf("decode reason=%v", err)
		}
	})
}

func TestAttachmentV3OperationDecisionErrorPrecedence(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)

	malformedBinding := fixture.bodyOD
	malformedBinding.ArgumentsSHA256 = digestTest('f')
	malformedBinding = recomputeAttachmentOperationDecisionV3(t, malformedBinding)
	if err := ValidateAttachmentOperationDecisionV3(malformedBinding, fixture.bodyO, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
		t.Fatalf("malformed binding did not outrank expired ancestry: %v", err)
	}

	staleBinding := fixture.bodyOD
	staleBinding.RequestSHA256 = digestTest('e')
	staleBinding = recomputeAttachmentOperationDecisionV3(t, staleBinding)
	if err := ValidateAttachmentOperationDecisionV3(staleBinding, fixture.bodyO, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonStaleAuthority) {
		t.Fatalf("current binding did not outrank expired ancestry: %v", err)
	}

	denied := fixture.bodyOD
	denied.Status = domain.BrokerDecisionDenied
	denied.Reason = domain.BrokerReasonDenied
	denied = recomputeAttachmentOperationDecisionV3(t, denied)
	if err := ValidateAttachmentOperationDecisionV3(denied, fixture.bodyO, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonDecisionExpired) {
		t.Fatalf("expired ancestry did not outrank current denial: %v", err)
	}
}

func TestAttachmentV3QualificationDecisionErrorPrecedence(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)

	malformedBinding := fixture.preOpenQD
	malformedBinding.PlanSHA256 = digestTest('f')
	malformedBinding = recomputeAttachmentQualificationDecisionV3(t, malformedBinding)
	if err := ValidateAttachmentQualificationDecisionV3(malformedBinding, fixture.preOpenQ, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonMalformed) {
		t.Fatalf("malformed binding did not outrank expired ancestry: %v", err)
	}

	staleBinding := fixture.preOpenQD
	staleBinding.RequestSHA256 = digestTest('e')
	staleBinding = recomputeAttachmentQualificationDecisionV3(t, staleBinding)
	if err := ValidateAttachmentQualificationDecisionV3(staleBinding, fixture.preOpenQ, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonStaleAuthority) {
		t.Fatalf("current binding did not outrank expired ancestry: %v", err)
	}

	denied := fixture.preOpenQD
	denied.Status = domain.BrokerDecisionDenied
	denied.Reason = domain.BrokerReasonDenied
	denied = recomputeAttachmentQualificationDecisionV3(t, denied)
	if err := ValidateAttachmentQualificationDecisionV3(denied, fixture.preOpenQ, 8_000, attachmentTestDeadline); !isAttachmentReasonV3(err, domain.BrokerReasonDecisionExpired) {
		t.Fatalf("expired ancestry did not outrank current denial: %v", err)
	}
}

func TestAttachmentV3ReleaseOperationUsesCallerDeadlineForCurrentQualification(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)
	releaseQ := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	releaseQD := attachmentQualificationDecisionFixture(t, releaseQ, 10_000)
	releaseO := attachmentReleaseOperationFixture(t, fixture, releaseQ, releaseQD)
	releaseOD := attachmentOperationDecisionFixture(t, releaseO, 10_000)
	releaseOD.ExpiresAtMillis = 13_000
	releaseOD = recomputeAttachmentOperationDecisionV3(t, releaseOD)

	if err := ValidateAttachmentOperationDecisionV3(releaseOD, releaseO, 10_000, 14_000); !isAttachmentReasonV3(err, domain.BrokerReasonDecisionExpired) {
		t.Fatalf("release qualification ignored caller deadline: %v", err)
	}
}

func attachmentAdmissionDecisionWithWrongRequestBindingV3(t *testing.T, value domain.BrokerAttachmentAdmissionDecisionV3) domain.BrokerAttachmentAdmissionDecisionV3 {
	t.Helper()
	value.RequestSHA256 = digestTest('f')
	value.DecisionSHA256 = ""
	return mustAttachmentDecode(t, value, EncodeAttachmentAdmissionDecisionV3, DecodeAttachmentAdmissionDecisionV3)
}

func recomputeAttachmentOperationDecisionV3(t *testing.T, value domain.BrokerAttachmentOperationDecisionV3) domain.BrokerAttachmentOperationDecisionV3 {
	t.Helper()
	value.DecisionSHA256 = ""
	return mustAttachmentDecode(t, value, EncodeAttachmentOperationDecisionV3, DecodeAttachmentOperationDecisionV3)
}

func recomputeAttachmentQualificationDecisionV3(t *testing.T, value domain.BrokerAttachmentQualificationDecisionV3) domain.BrokerAttachmentQualificationDecisionV3 {
	t.Helper()
	value.DecisionSHA256 = ""
	return mustAttachmentDecode(t, value, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
}

func mutateDeepAdmissionRequestBindingWireV3(t *testing.T, body []byte) []byte {
	t.Helper()
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	found := false
	var visit func(any)
	visit = func(value any) {
		if found {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			if admission, ok := typed["admission_decision"].(map[string]any); ok {
				decision, ok := admission["decision"].(map[string]any)
				if !ok {
					t.Fatal("admission decision core is absent")
				}
				decision["request_sha256"] = digestTest('f')
				admission["decision_sha256"] = ""
				digest, err := digestExecutionV3("admission-decision", admission)
				if err != nil {
					t.Fatal(err)
				}
				admission["decision_sha256"] = digest
				standalone, err := json.Marshal(admission)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeAttachmentAdmissionDecisionV3(standalone); err != nil {
					t.Fatalf("mutated ancestor is not independently well formed: %v", err)
				}
				found = true
				return
			}
			for _, child := range typed {
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(root)
	if !found {
		t.Fatal("deep admission decision was not found")
	}
	mutated, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func isAttachmentReasonV3(err error, want domain.BrokerReason) bool {
	if err == nil {
		return false
	}
	got, ok := Reason(err)
	return ok && got == want
}
