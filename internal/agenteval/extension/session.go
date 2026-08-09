package extension

import "math"

// DescriptorCopy returns a deep copy suitable for an in-process registry.
func DescriptorCopy(value Descriptor) Descriptor {
	value.Operations = append([]Operation(nil), value.Operations...)
	value.Capabilities = append([]CapabilityClaim(nil), value.Capabilities...)
	return value
}

// NewInitialize constructs the first host frame for one exact attempt-bound
// session.
func NewInitialize(manifest Manifest, sessionID, attemptID string) (Frame, error) {
	if err := ValidateManifest(manifest); err != nil {
		return Frame{}, err
	}
	required := make([]CapabilityID, 0, len(manifest.Component.Capabilities))
	for _, claim := range manifest.Component.Capabilities {
		if claim.State == CapabilitySupported {
			required = append(required, claim.ID)
		}
	}
	frame := baseFrame(manifest, sessionID, attemptID)
	frame.Direction = DirectionHostToExtension
	frame.Sequence = 1
	frame.Type = MessageInitialize
	frame.Initialize = &InitializePayload{
		OfferedProtocolVersions: []int{ProtocolVersion},
		RequiredCapabilities:    required,
	}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// NewInitialized constructs the exact successful handshake response.
func NewInitialized(manifest Manifest, initialize Frame) (Frame, error) {
	if err := validateInitializeForManifest(manifest, initialize); err != nil {
		return Frame{}, err
	}
	frame := responseBase(initialize)
	frame.Sequence = 2
	frame.Type = MessageInitialized
	frame.Initialized = &InitializedPayload{
		SelectedProtocolVersion: ProtocolVersion,
		Capabilities:            append([]CapabilityClaim(nil), manifest.Component.Capabilities...),
	}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// ValidateInitialized binds the component response to the admitted manifest
// and exact initialize frame. Capabilities must be complete and cannot expand
// or change the manifest declaration.
func ValidateInitialized(manifest Manifest, initialize, initialized Frame) error {
	if err := validateInitializeForManifest(manifest, initialize); err != nil {
		return err
	}
	if err := ValidateFrame(initialized); err != nil || initialized.Type != MessageInitialized ||
		!sameSessionIdentity(initialize, initialized) ||
		!equalClaims(initialized.Initialized.Capabilities, manifest.Component.Capabilities) {
		return contractError(ErrorInvalidState, err)
	}
	return nil
}

// NewInvoke constructs the sole primary-operation request for a session.
func NewInvoke(
	manifest Manifest,
	initialize, initialized Frame,
	invocationID string,
	operation Operation,
	configuration []ConfigurationValue,
	inputs []ArtifactReference,
	policy InvocationPolicy,
) (Frame, error) {
	return newInvoke(manifest, initialize, initialized, invocationID, InvocationExecute, operation, configuration, inputs, policy)
}

// NewCancellationProbeInvoke constructs a synchronized protocol-only probe.
// The component must not execute the operation or emit a result/error; it waits
// for the host's cancel frame and may answer only with canceled.
func NewCancellationProbeInvoke(
	manifest Manifest,
	initialize, initialized Frame,
	invocationID string,
	operation Operation,
	configuration []ConfigurationValue,
	inputs []ArtifactReference,
	policy InvocationPolicy,
) (Frame, error) {
	return newInvoke(manifest, initialize, initialized, invocationID, InvocationAwaitCancel, operation, configuration, inputs, policy)
}

func newInvoke(
	manifest Manifest,
	initialize, initialized Frame,
	invocationID string,
	control InvocationControl,
	operation Operation,
	configuration []ConfigurationValue,
	inputs []ArtifactReference,
	policy InvocationPolicy,
) (Frame, error) {
	if err := ValidateInitialized(manifest, initialize, initialized); err != nil ||
		!OperationSupported(manifest, operation) || !configurationMatches(manifest.ConfigurationSchema, configuration) {
		return Frame{}, contractError(ErrorInvalidState, err)
	}
	frame := baseFrame(manifest, initialize.SessionID, initialize.AttemptID)
	frame.Direction = DirectionHostToExtension
	frame.Sequence = 3
	frame.Type = MessageInvoke
	frame.Invoke = &InvokePayload{
		InvocationID:  invocationID,
		Control:       control,
		Operation:     operation,
		Configuration: cloneConfiguration(configuration),
		Inputs:        append([]ArtifactReference{}, inputs...),
		Policy:        policy,
	}
	if err := validateInvokeForManifest(manifest, frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// NewResult constructs a component terminal result. The host still validates
// it against the invocation's exact output policy.
func NewResult(invoke Frame, outputs []ArtifactReference) (Frame, error) {
	if err := ValidateFrame(invoke); err != nil || invoke.Type != MessageInvoke || invoke.Invoke.Control != InvocationExecute {
		return Frame{}, contractError(ErrorInvalidState, err)
	}
	frame := responseBase(invoke)
	frame.Sequence = 4
	frame.Type = MessageResult
	frame.Result = &ResultPayload{
		InvocationID: invoke.Invoke.InvocationID,
		Operation:    invoke.Invoke.Operation,
		Outputs:      append([]ArtifactReference{}, outputs...),
	}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// NewComponentError constructs a structured component failure without text,
// retry, lifecycle, or scoring authority.
func NewComponentError(request Frame, code ComponentErrorCode) (Frame, error) {
	if err := ValidateFrame(request); err != nil || (request.Type != MessageInitialize && request.Type != MessageInvoke) {
		return Frame{}, contractError(ErrorInvalidState, err)
	}
	frame := responseBase(request)
	frame.Type = MessageError
	frame.Error = &ComponentError{Code: code}
	if request.Type == MessageInitialize {
		frame.Sequence = 2
	} else {
		if request.Invoke.Control != InvocationExecute {
			return Frame{}, contractError(ErrorInvalidState, nil)
		}
		frame.Sequence = 4
		frame.Error.InvocationID = request.Invoke.InvocationID
		frame.Error.Operation = request.Invoke.Operation
	}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// ValidateTerminal revalidates a result/error against the exact invocation.
func ValidateTerminal(manifest Manifest, invoke, terminal Frame) error {
	if err := validateInvokeForManifest(manifest, invoke); err != nil ||
		invoke.Invoke.Control != InvocationExecute || ValidateFrame(terminal) != nil || !sameSessionIdentity(invoke, terminal) {
		return contractError(ErrorInvalidState, err)
	}
	switch terminal.Type {
	case MessageError:
		if terminal.Error.InvocationID != invoke.Invoke.InvocationID || terminal.Error.Operation != invoke.Invoke.Operation {
			return contractError(ErrorInvalidState, nil)
		}
		return nil
	case MessageResult:
		if terminal.Result.InvocationID != invoke.Invoke.InvocationID || terminal.Result.Operation != invoke.Invoke.Operation ||
			terminal.Result.Outputs == nil ||
			len(terminal.Result.Outputs) > int(invoke.Invoke.Policy.MaxOutputArtifacts) {
			return contractError(ErrorInvalidState, nil)
		}
		var total uint64
		for _, output := range terminal.Result.Outputs {
			if output.Privacy != invoke.Invoke.Policy.OutputPrivacy || math.MaxUint64-total < output.SizeBytes {
				return contractError(ErrorInvalidState, nil)
			}
			total += output.SizeBytes
		}
		if total > invoke.Invoke.Policy.MaxOutputBytes {
			return contractError(ErrorInvalidState, nil)
		}
		return nil
	default:
		return contractError(ErrorInvalidState, nil)
	}
}

func validateInvokeForManifest(manifest Manifest, invoke Frame) error {
	if err := ValidateManifest(manifest); err != nil || ValidateFrame(invoke) != nil || invoke.Type != MessageInvoke ||
		invoke.ComponentID != manifest.Component.ID || invoke.ComponentVersion != manifest.Component.Version ||
		invoke.Role != manifest.Component.Role || invoke.ExecutableSHA256 != manifest.ExecutableSHA256 ||
		!OperationSupported(manifest, invoke.Invoke.Operation) ||
		invoke.Invoke.Configuration == nil || invoke.Invoke.Inputs == nil ||
		!configurationMatches(manifest.ConfigurationSchema, invoke.Invoke.Configuration) {
		return contractError(ErrorInvalidState, err)
	}
	return nil
}

// NewCancel constructs the only cancellation request. Receipt of Canceled is
// protocol evidence only, never process termination or lifecycle proof.
func NewCancel(invoke Frame) (Frame, error) {
	if err := ValidateFrame(invoke); err != nil || invoke.Type != MessageInvoke {
		return Frame{}, contractError(ErrorInvalidState, err)
	}
	frame := invoke
	frame.Sequence = 4
	frame.Type = MessageCancel
	frame.Invoke = nil
	frame.Cancel = &CancelPayload{InvocationID: invoke.Invoke.InvocationID, Operation: invoke.Invoke.Operation}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// NewCanceled constructs the protocol acknowledgment for a cancel frame.
func NewCanceled(cancel Frame) (Frame, error) {
	if err := ValidateFrame(cancel); err != nil || cancel.Type != MessageCancel {
		return Frame{}, contractError(ErrorInvalidState, err)
	}
	frame := responseBase(cancel)
	frame.Sequence = 5
	frame.Type = MessageCanceled
	frame.Canceled = &CanceledPayload{InvocationID: cancel.Cancel.InvocationID, Operation: cancel.Cancel.Operation}
	if err := ValidateFrame(frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// ValidateCanceled binds an acknowledgment to the exact cancel request.
func ValidateCanceled(cancel, canceled Frame) error {
	if ValidateFrame(cancel) != nil || cancel.Type != MessageCancel || ValidateFrame(canceled) != nil ||
		canceled.Type != MessageCanceled || !sameSessionIdentity(cancel, canceled) ||
		cancel.Cancel.InvocationID != canceled.Canceled.InvocationID ||
		cancel.Cancel.Operation != canceled.Canceled.Operation {
		return contractError(ErrorInvalidState, nil)
	}
	return nil
}

// OperationSupported reports the admitted manifest state for one operation.
func OperationSupported(manifest Manifest, operation Operation) bool {
	capability := CapabilityFor(manifest.Component.Role, operation)
	if capability == "" {
		return false
	}
	for _, claim := range manifest.Component.Capabilities {
		if claim.ID == capability {
			return claim.State == CapabilitySupported
		}
	}
	return false
}

func validateInitializeForManifest(manifest Manifest, initialize Frame) error {
	if err := ValidateManifest(manifest); err != nil || ValidateFrame(initialize) != nil || initialize.Type != MessageInitialize ||
		initialize.ComponentID != manifest.Component.ID || initialize.ComponentVersion != manifest.Component.Version ||
		initialize.Role != manifest.Component.Role || initialize.ExecutableSHA256 != manifest.ExecutableSHA256 {
		return contractError(ErrorInvalidState, err)
	}
	wantRequired := make([]CapabilityID, 0, len(manifest.Component.Capabilities))
	for _, claim := range manifest.Component.Capabilities {
		if claim.State == CapabilitySupported {
			wantRequired = append(wantRequired, claim.ID)
		}
	}
	if len(initialize.Initialize.RequiredCapabilities) != len(wantRequired) {
		return contractError(ErrorInvalidState, nil)
	}
	for index, capability := range wantRequired {
		if initialize.Initialize.RequiredCapabilities[index] != capability {
			return contractError(ErrorInvalidState, nil)
		}
	}
	return nil
}

func baseFrame(manifest Manifest, sessionID, attemptID string) Frame {
	return Frame{
		Schema: MessageSchema, SchemaVersion: MessageSchemaVersion, ProtocolVersion: ProtocolVersion,
		SessionID: sessionID, AttemptID: attemptID, Role: manifest.Component.Role,
		ComponentID: manifest.Component.ID, ComponentVersion: manifest.Component.Version,
		ExecutableSHA256: manifest.ExecutableSHA256,
	}
}

func responseBase(request Frame) Frame {
	return Frame{
		Schema: request.Schema, SchemaVersion: request.SchemaVersion, ProtocolVersion: request.ProtocolVersion,
		Direction: DirectionExtensionToHost, SessionID: request.SessionID, AttemptID: request.AttemptID,
		Role: request.Role, ComponentID: request.ComponentID, ComponentVersion: request.ComponentVersion,
		ExecutableSHA256: request.ExecutableSHA256,
	}
}

func sameSessionIdentity(left, right Frame) bool {
	return left.Schema == right.Schema && left.SchemaVersion == right.SchemaVersion &&
		left.ProtocolVersion == right.ProtocolVersion && left.SessionID == right.SessionID &&
		left.AttemptID == right.AttemptID && left.Role == right.Role && left.ComponentID == right.ComponentID &&
		left.ComponentVersion == right.ComponentVersion && left.ExecutableSHA256 == right.ExecutableSHA256
}

func equalClaims(left, right []CapabilityClaim) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func configurationMatches(schema []ConfigurationField, values []ConfigurationValue) bool {
	if !validConfigurationValues(values) {
		return false
	}
	valueIndex := 0
	for _, field := range schema {
		if valueIndex >= len(values) || values[valueIndex].Name != field.Name {
			if field.Required {
				return false
			}
			continue
		}
		value := values[valueIndex]
		switch field.Kind {
		case ConfigurationBoolean:
			if value.Boolean == nil {
				return false
			}
		case ConfigurationInteger:
			if value.Integer == nil || *value.Integer < *field.Minimum || *value.Integer > *field.Maximum {
				return false
			}
		case ConfigurationEnum:
			if value.Enum == "" || !containsString(field.Values, value.Enum) {
				return false
			}
		default:
			return false
		}
		valueIndex++
	}
	return valueIndex == len(values)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneConfiguration(values []ConfigurationValue) []ConfigurationValue {
	result := make([]ConfigurationValue, len(values))
	copy(result, values)
	for index := range result {
		if result[index].Boolean != nil {
			value := *result[index].Boolean
			result[index].Boolean = &value
		}
		if result[index].Integer != nil {
			value := *result[index].Integer
			result[index].Integer = &value
		}
	}
	return result
}
