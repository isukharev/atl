package extension

import (
	"bytes"
	"strings"
	"testing"
)

const (
	testInvocationID      = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testOtherInvocationID = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func TestExtensionProtocolV1StateMachineIsClosed(t *testing.T) {
	for _, role := range []Role{RoleAgentAdapter, RoleExecutionBackend, RoleGrader, RoleProfile, RoleReporter} {
		manifest := validManifest(role)
		for _, operation := range OperationsForRole(role) {
			t.Run(string(role)+"/"+string(operation), func(t *testing.T) {
				initialize, err := NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
				if err != nil {
					t.Fatal(err)
				}
				initialized, err := NewInitialized(manifest, initialize)
				if err != nil || ValidateInitialized(manifest, initialize, initialized) != nil {
					t.Fatalf("initialized: %v", err)
				}
				attempts := int64(1)
				invoke, err := NewInvoke(manifest, initialize, initialized, testInvocationID, operation,
					[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}},
					[]ArtifactReference{validArtifact("input", 3)},
					InvocationPolicy{MaxOutputArtifacts: 1, MaxOutputBytes: 5, OutputPrivacy: PrivacyContentMinimized, Replay: ReplayUnsafe})
				if err != nil {
					t.Fatal(err)
				}
				if invoke.Invoke.InvocationID != testInvocationID {
					t.Fatalf("invoke invocation_id=%q", invoke.Invoke.InvocationID)
				}
				result, err := NewResult(invoke, []ArtifactReference{validArtifact("output", 5)})
				if err != nil || ValidateTerminal(manifest, invoke, result) != nil {
					t.Fatalf("terminal: %v", err)
				}
				if result.Result.InvocationID != testInvocationID {
					t.Fatalf("result invocation_id=%q", result.Result.InvocationID)
				}
				line, err := EncodeFrameLine(result)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeFrameLine(line)
				if err != nil || ValidateTerminal(manifest, invoke, decoded) != nil {
					t.Fatalf("decode terminal: %v", err)
				}
				cancel, err := NewCancel(invoke)
				if err != nil {
					t.Fatal(err)
				}
				if cancel.Cancel.InvocationID != testInvocationID {
					t.Fatalf("cancel invocation_id=%q", cancel.Cancel.InvocationID)
				}
				canceled, err := NewCanceled(cancel)
				if err != nil || ValidateCanceled(cancel, canceled) != nil {
					t.Fatalf("cancel: %v", err)
				}
				if canceled.Canceled.InvocationID != testInvocationID {
					t.Fatalf("canceled invocation_id=%q", canceled.Canceled.InvocationID)
				}
			})
		}
	}
}

func TestExtensionProtocolRejectsIdentityCapabilityAndPolicyDrift(t *testing.T) {
	manifest := validManifest(RoleAgentAdapter)
	initialize, _ := NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	initialized, _ := NewInitialized(manifest, initialize)

	identityDrift := initialized
	identityDrift.AttemptID = strings.Repeat("d", 64)
	if ValidateInitialized(manifest, initialize, identityDrift) == nil {
		t.Fatal("attempt identity drift passed")
	}
	capabilityDrift := initialized
	capabilityDrift.Initialized = &InitializedPayload{SelectedProtocolVersion: ProtocolVersion, Capabilities: append([]CapabilityClaim(nil), initialized.Initialized.Capabilities...)}
	capabilityDrift.Initialized.Capabilities[0].State = CapabilityUnsupported
	if ValidateInitialized(manifest, initialize, capabilityDrift) == nil {
		t.Fatal("capability drift passed")
	}
	unknownRequired := initialize
	unknownRequired.Initialize = &InitializePayload{
		OfferedProtocolVersions: append([]int(nil), initialize.Initialize.OfferedProtocolVersions...),
		RequiredCapabilities:    append([]CapabilityID(nil), initialize.Initialize.RequiredCapabilities...),
	}
	unknownRequired.Initialize.RequiredCapabilities[0] = "ambient.secret"
	if ValidateFrame(unknownRequired) == nil {
		t.Fatal("unknown required capability passed frame validation")
	}

	attempts := int64(1)
	invoke, err := NewInvoke(manifest, initialize, initialized, testInvocationID, OperationExecute,
		[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}}, nil,
		InvocationPolicy{MaxOutputArtifacts: 1, MaxOutputBytes: 4, OutputPrivacy: PrivacyContentMinimized, Replay: ReplayUnsafe})
	if err != nil {
		t.Fatal(err)
	}
	if invoke.Invoke.Inputs == nil {
		t.Fatal("constructor preserved a null input collection")
	}
	tooLarge, _ := NewResult(invoke, []ArtifactReference{validArtifact("output", 5)})
	if ValidateTerminal(manifest, invoke, tooLarge) == nil {
		t.Fatal("output budget escalation passed")
	}
	wrongPrivacy := validArtifact("output", 4)
	wrongPrivacy.Privacy = PrivacyPublic
	privacyResult, _ := NewResult(invoke, []ArtifactReference{wrongPrivacy})
	if ValidateTerminal(manifest, invoke, privacyResult) == nil {
		t.Fatal("output privacy drift passed")
	}
	validResult, err := NewResult(invoke, []ArtifactReference{validArtifact("output", 4)})
	if err != nil || ValidateTerminal(manifest, invoke, validResult) != nil {
		t.Fatalf("valid terminal baseline: %v", err)
	}

	identityManifest := manifest
	identityManifest.Component.ID = "other-component"
	if ValidateTerminal(identityManifest, invoke, validResult) == nil {
		t.Fatal("self-consistent terminal escaped manifest identity binding")
	}

	unsupportedManifest := manifest
	unsupportedManifest.Component.Capabilities = append([]CapabilityClaim(nil), manifest.Component.Capabilities...)
	unsupportedManifest.Component.Capabilities[0].State = CapabilityUnsupported
	unsupportedInvoke := invoke
	unsupportedInvoke.Invoke = &InvokePayload{
		InvocationID: invoke.Invoke.InvocationID, Control: invoke.Invoke.Control, Operation: invoke.Invoke.Operation,
		Configuration: cloneConfiguration(invoke.Invoke.Configuration),
		Inputs:        append([]ArtifactReference{}, invoke.Invoke.Inputs...), Policy: invoke.Invoke.Policy,
	}
	if ValidateTerminal(unsupportedManifest, unsupportedInvoke, validResult) == nil {
		t.Fatal("terminal for a manifest-unsupported operation passed")
	}
}

func TestExtensionProtocolRejectsInvocationCausalDrift(t *testing.T) {
	manifest := validManifest(RoleAgentAdapter)
	initialize, err := NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := NewInitialized(manifest, initialize)
	if err != nil {
		t.Fatal(err)
	}
	attempts := int64(1)
	invoke, err := NewInvoke(manifest, initialize, initialized, testInvocationID, OperationExecute,
		[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}}, nil,
		InvocationPolicy{MaxOutputArtifacts: 1, MaxOutputBytes: 5, OutputPrivacy: PrivacyContentMinimized, Replay: ReplayUnsafe})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewInvoke(manifest, initialize, initialized, "", OperationExecute,
		[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}}, nil,
		invoke.Invoke.Policy); err == nil {
		t.Fatal("invoke without an invocation nonce passed construction")
	}

	wrongResult := responseBase(invoke)
	wrongResult.Sequence = 4
	wrongResult.Type = MessageResult
	wrongResult.Result = &ResultPayload{
		InvocationID: testOtherInvocationID,
		Operation:    invoke.Invoke.Operation,
		Outputs:      []ArtifactReference{validArtifact("output", 5)},
	}
	if err := ValidateFrame(wrongResult); err != nil {
		t.Fatalf("precomputed wrong-nonce terminal is not independently well-formed: %v", err)
	}
	if ValidateTerminal(manifest, invoke, wrongResult) == nil {
		t.Fatal("terminal from a different invocation passed causal binding")
	}
	absentResult := wrongResult
	absentResult.Result = &ResultPayload{
		Operation: invoke.Invoke.Operation,
		Outputs:   []ArtifactReference{validArtifact("output", 5)},
	}
	if ValidateTerminal(manifest, invoke, absentResult) == nil {
		t.Fatal("terminal without an invocation nonce passed causal binding")
	}

	componentError, err := NewComponentError(invoke, ComponentFailure)
	if err != nil || componentError.Error.InvocationID != testInvocationID || ValidateTerminal(manifest, invoke, componentError) != nil {
		t.Fatalf("component error causal binding: frame=%+v err=%v", componentError.Error, err)
	}
	wrongError := componentError
	wrongError.Error = &ComponentError{
		InvocationID: testOtherInvocationID,
		Operation:    componentError.Error.Operation,
		Code:         componentError.Error.Code,
	}
	if err := ValidateFrame(wrongError); err != nil {
		t.Fatalf("precomputed wrong-nonce component error is not independently well-formed: %v", err)
	}
	if ValidateTerminal(manifest, invoke, wrongError) == nil {
		t.Fatal("component error from a different invocation passed causal binding")
	}

	cancel, err := NewCancel(invoke)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := NewCanceled(cancel)
	if err != nil {
		t.Fatal(err)
	}
	wrongCanceled := canceled
	wrongCanceled.Canceled = &CanceledPayload{
		InvocationID: testOtherInvocationID,
		Operation:    canceled.Canceled.Operation,
	}
	if err := ValidateFrame(wrongCanceled); err != nil {
		t.Fatalf("precomputed wrong-nonce cancel acknowledgment is not independently well-formed: %v", err)
	}
	if ValidateCanceled(cancel, wrongCanceled) == nil {
		t.Fatal("cancel acknowledgment from a different invocation passed causal binding")
	}

	probe, err := NewCancellationProbeInvoke(manifest, initialize, initialized, testOtherInvocationID, OperationExecute,
		[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}}, nil, invoke.Invoke.Policy)
	if err != nil || probe.Invoke.Control != InvocationAwaitCancel {
		t.Fatalf("construct cancellation probe: control=%q err=%v", probe.Invoke.Control, err)
	}
	if _, err := NewResult(probe, nil); err == nil {
		t.Fatal("cancellation probe emitted a result before cancel")
	}
	if _, err := NewComponentError(probe, ComponentFailure); err == nil {
		t.Fatal("cancellation probe emitted an error before cancel")
	}
	probeCancel, err := NewCancel(probe)
	if err != nil {
		t.Fatal(err)
	}
	probeCanceled, err := NewCanceled(probeCancel)
	if err != nil || ValidateCanceled(probeCancel, probeCanceled) != nil {
		t.Fatalf("cancellation probe acknowledgment: %v", err)
	}
}

func TestExtensionSessionConstructorsNormalizeEmptyCollectionsAndRejectNullDrift(t *testing.T) {
	manifest := validManifest(RoleAgentAdapter)
	initialize, _ := NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	initialized, _ := NewInitialized(manifest, initialize)
	attempts := int64(1)
	invoke, err := NewInvoke(manifest, initialize, initialized, testInvocationID, OperationExecute,
		[]ConfigurationValue{{Name: "attempts", Integer: &attempts}, {Name: "mode", Enum: "strict"}}, nil,
		InvocationPolicy{MaxOutputArtifacts: 1, MaxOutputBytes: 5, OutputPrivacy: PrivacyContentMinimized, Replay: ReplayUnsafe})
	if err != nil || invoke.Invoke.Inputs == nil {
		t.Fatalf("invoke inputs=%v err=%v", invoke.Invoke.Inputs, err)
	}
	emptyResult, err := NewResult(invoke, nil)
	if err != nil || emptyResult.Result.Outputs == nil {
		t.Fatalf("result outputs=%v err=%v", emptyResult.Result.Outputs, err)
	}
	encodedResult, err := EncodeFrame(emptyResult)
	if err != nil || !bytes.Contains(encodedResult, []byte(`"outputs":[]`)) {
		t.Fatalf("empty result encoding=%s err=%v", encodedResult, err)
	}
	nullResultData := bytes.Replace(encodedResult, []byte(`"outputs":[]`), []byte(`"outputs":null`), 1)
	if _, err := DecodeFrame(nullResultData); err == nil {
		t.Fatal("frame decoder accepted null result outputs")
	}

	encodedInvoke, err := EncodeFrame(invoke)
	if err != nil || !bytes.Contains(encodedInvoke, []byte(`"inputs":[]`)) {
		t.Fatalf("empty invoke encoding=%s err=%v", encodedInvoke, err)
	}
	nullInvokeData := bytes.Replace(encodedInvoke, []byte(`"inputs":[]`), []byte(`"inputs":null`), 1)
	if _, err := DecodeFrame(nullInvokeData); err == nil {
		t.Fatal("frame decoder accepted null invoke inputs")
	}
}

func TestExtensionProtocolCodecRejectsNoncanonicalAndOversizedFrames(t *testing.T) {
	manifest := validManifest(RoleReporter)
	initialize, err := NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeFrame(initialize)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"unknown":      bytes.Replace(data, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1),
		"duplicate":    bytes.Replace(data, []byte(`{"schema":`), []byte(`{"schema":"agent-eval/adapter-message","schema":`), 1),
		"future":       bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"noncanonical": bytes.Replace(data, []byte(`{"schema":`), []byte(`{ "schema":`), 1),
		"trailing":     append(append([]byte(nil), data...), []byte(`{}`)...),
		"newline":      append(append([]byte(nil), data...), '\n'),
		"oversized":    bytes.Repeat([]byte("x"), MaxFrameBytes+1),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFrame(mutation); err == nil {
				t.Fatal("invalid frame passed")
			}
		})
	}
}

func validArtifact(id string, size uint64) ArtifactReference {
	return ArtifactReference{
		ID: id, Schema: "agent-eval/synthetic-artifact", SchemaVersion: 1,
		SHA256: strings.Repeat("d", 64), SizeBytes: size, Privacy: PrivacyContentMinimized,
	}
}
