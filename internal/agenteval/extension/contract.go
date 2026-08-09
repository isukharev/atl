// Package extension defines the domain-neutral, versioned process wire used by
// evaluator extensions. It deliberately owns no process launch or product
// policy; the compatibility facade retains those authorities.
package extension

import (
	"errors"
	"sort"
	"strings"
)

const (
	ManifestSchema           = "agent-eval/adapter-manifest"
	MessageSchema            = "agent-eval/adapter-message"
	ContractVersion          = "0.1.0-pre-release"
	ManifestSchemaVersion    = 1
	MessageSchemaVersion     = 1
	ProtocolVersion          = 1
	MaxManifestBytes         = 64 << 10
	MaxFrameBytes            = 1 << 20
	MaxSessionBytes          = 4 << 20
	MaxStderrBytes           = 64 << 10
	MaxDeadlineMilliseconds  = 15 * 60 * 1000
	MaxCollectionEntries     = 256
	MaxConfigurationEntries  = 32
	MaxPlatformEntries       = 16
	MaxIdentifierBytes       = 128
	MaxComponentVersionBytes = 64
	MaxInvocationOutputBytes = 1 << 40
	SHA256HexCharacters      = 64
)

// ErrorCode is the closed public classification emitted by this package.
type ErrorCode string

const (
	ErrorInvalidManifest ErrorCode = "invalid_manifest"
	ErrorInvalidMessage  ErrorCode = "invalid_message"
	ErrorInvalidState    ErrorCode = "invalid_state"
	ErrorLimitExceeded   ErrorCode = "limit_exceeded"
)

// Error renders only a closed classification. It never includes wire bytes or
// caller-controlled component identities.
type Error struct {
	code  ErrorCode
	cause error
}

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return e.cause }
func (e *Error) Code() ErrorCode { return e.code }

func contractError(code ErrorCode, cause error) error { return &Error{code: code, cause: cause} }

// CodeOf extracts an extension contract error classification.
func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

// Role is the closed component responsibility selected for one session.
type Role string

const (
	RoleProfile          Role = "profile"
	RoleAgentAdapter     Role = "agent-adapter"
	RoleExecutionBackend Role = "execution-backend"
	RoleGrader           Role = "grader"
	RoleReporter         Role = "reporter"
)

// Operation is one role-scoped primary operation.
type Operation string

const (
	OperationValidate     Operation = "validate"
	OperationCapabilities Operation = "capabilities"
	OperationPrepare      Operation = "prepare"
	OperationExecute      Operation = "execute"
	OperationNormalize    Operation = "normalize"
	OperationGrade        Operation = "grade"
	OperationReport       Operation = "report"
)

var operationsByRole = map[Role][]Operation{
	RoleProfile:          {OperationCapabilities, OperationValidate},
	RoleAgentAdapter:     {OperationExecute, OperationNormalize, OperationPrepare},
	RoleExecutionBackend: {OperationExecute, OperationPrepare},
	RoleGrader:           {OperationGrade, OperationValidate},
	RoleReporter:         {OperationReport, OperationValidate},
}

// OperationsForRole returns the canonical operation set for role.
func OperationsForRole(role Role) []Operation {
	return append([]Operation(nil), operationsByRole[role]...)
}

// CapabilityID is a closed role-qualified operation capability.
type CapabilityID string

// CapabilityState is the closed negotiation state.
type CapabilityState string

const (
	CapabilitySupported     CapabilityState = "supported"
	CapabilityUnsupported   CapabilityState = "unsupported"
	CapabilityUnknown       CapabilityState = "unknown"
	CapabilityNotApplicable CapabilityState = "not_applicable"
)

// CapabilityClaim declares one state for one considered capability.
type CapabilityClaim struct {
	ID    CapabilityID    `json:"id"`
	State CapabilityState `json:"state"`
}

// Descriptor is the process-independent component policy projection. Built-in
// components use this same vocabulary without pretending to have an executable
// manifest.
type Descriptor struct {
	ID           string            `json:"id"`
	Version      string            `json:"version"`
	Role         Role              `json:"role"`
	Operations   []Operation       `json:"operations"`
	Capabilities []CapabilityClaim `json:"capabilities"`
}

// ConfigurationKind deliberately excludes free-form strings and paths.
type ConfigurationKind string

const (
	ConfigurationBoolean ConfigurationKind = "boolean"
	ConfigurationInteger ConfigurationKind = "integer"
	ConfigurationEnum    ConfigurationKind = "enum"
)

// ConfigurationField declares one closed configuration key.
type ConfigurationField struct {
	Name     string            `json:"name"`
	Kind     ConfigurationKind `json:"kind"`
	Required bool              `json:"required"`
	Values   []string          `json:"values,omitempty"`
	Minimum  *int64            `json:"minimum,omitempty"`
	Maximum  *int64            `json:"maximum,omitempty"`
}

// Platform is one exact supported OS/architecture pair.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// EnforcementRequirement is a host control required before process launch.
type EnforcementRequirement string

const (
	EnforcementExactEnvironment        EnforcementRequirement = "exact_environment"
	EnforcementPrivateWorkingDirectory EnforcementRequirement = "private_working_directory"
	EnforcementBoundedIO               EnforcementRequirement = "bounded_io"
	EnforcementDeadline                EnforcementRequirement = "deadline"
	EnforcementBestEffortProcessGroup  EnforcementRequirement = "best_effort_process_group_cleanup"
	EnforcementFilesystemIsolation     EnforcementRequirement = "filesystem_isolation"
	EnforcementNetworkIsolation        EnforcementRequirement = "network_isolation"
	EnforcementResourceLimits          EnforcementRequirement = "resource_limits"
	EnforcementTerminationProof        EnforcementRequirement = "termination_proof"
)

// Manifest binds a descriptor to exact executable bytes and launch
// prerequisites. Paths, arguments, environment, URLs, and credentials are
// intentionally absent.
type Manifest struct {
	Schema              string                   `json:"schema"`
	SchemaVersion       int                      `json:"schema_version"`
	ContractVersion     string                   `json:"contract_version"`
	ProtocolVersions    []int                    `json:"protocol_versions"`
	Component           Descriptor               `json:"component"`
	ExecutableSHA256    string                   `json:"executable_sha256"`
	ConfigurationSchema []ConfigurationField     `json:"configuration_schema"`
	Platforms           []Platform               `json:"platforms"`
	Requirements        []EnforcementRequirement `json:"requirements"`
}

// Direction binds which side may emit a frame.
type Direction string

const (
	DirectionHostToExtension Direction = "host_to_extension"
	DirectionExtensionToHost Direction = "extension_to_host"
)

// MessageType is the closed ephemeral session vocabulary.
type MessageType string

const (
	MessageInitialize  MessageType = "initialize"
	MessageInitialized MessageType = "initialized"
	MessageInvoke      MessageType = "invoke"
	MessageResult      MessageType = "result"
	MessageError       MessageType = "error"
	MessageCancel      MessageType = "cancel"
	MessageCanceled    MessageType = "canceled"
)

// Privacy is a closed artifact classification, not a redaction claim.
type Privacy string

const (
	PrivacyPublic           Privacy = "public"
	PrivacyContentMinimized Privacy = "content_minimized"
	PrivacyOwnerPrivate     Privacy = "owner_private"
)

// ReplayPolicy is caller-owned policy. The protocol never performs a retry.
type ReplayPolicy string

const (
	ReplaySafe   ReplayPolicy = "replay_safe"
	ReplayUnsafe ReplayPolicy = "non_replay_safe"
)

// InvocationControl distinguishes an ordinary operation from the synchronized
// protocol-only cancellation probe used by conformance. A cancellation probe
// must wait for cancel and never execute the selected operation.
type InvocationControl string

const (
	InvocationExecute     InvocationControl = "execute"
	InvocationAwaitCancel InvocationControl = "await_cancel"
)

// ConfigurationValue carries exactly one value matching a manifest field.
type ConfigurationValue struct {
	Name    string `json:"name"`
	Boolean *bool  `json:"boolean,omitempty"`
	Integer *int64 `json:"integer,omitempty"`
	Enum    string `json:"enum,omitempty"`
}

// ArtifactReference contains identity and size only. It grants no filesystem
// handle and carries no artifact body.
type ArtifactReference struct {
	ID            string  `json:"id"`
	Schema        string  `json:"schema"`
	SchemaVersion int     `json:"schema_version"`
	SHA256        string  `json:"sha256"`
	SizeBytes     uint64  `json:"size_bytes"`
	Privacy       Privacy `json:"privacy"`
}

// InvocationPolicy is the complete authority exposed through v1 messages.
type InvocationPolicy struct {
	MaxOutputArtifacts uint32       `json:"max_output_artifacts"`
	MaxOutputBytes     uint64       `json:"max_output_bytes"`
	OutputPrivacy      Privacy      `json:"output_privacy"`
	Replay             ReplayPolicy `json:"replay"`
}

type InitializePayload struct {
	OfferedProtocolVersions []int          `json:"offered_protocol_versions"`
	RequiredCapabilities    []CapabilityID `json:"required_capabilities"`
}

type InitializedPayload struct {
	SelectedProtocolVersion int               `json:"selected_protocol_version"`
	Capabilities            []CapabilityClaim `json:"capabilities"`
}

type InvokePayload struct {
	InvocationID  string               `json:"invocation_id"`
	Control       InvocationControl    `json:"control"`
	Operation     Operation            `json:"operation"`
	Configuration []ConfigurationValue `json:"configuration"`
	Inputs        []ArtifactReference  `json:"inputs"`
	Policy        InvocationPolicy     `json:"policy"`
}

type ResultPayload struct {
	InvocationID string              `json:"invocation_id"`
	Operation    Operation           `json:"operation"`
	Outputs      []ArtifactReference `json:"outputs"`
}

// ComponentErrorCode cannot select lifecycle, retry, or scoring authority.
type ComponentErrorCode string

const (
	ComponentInvalidInput ComponentErrorCode = "invalid_input"
	ComponentUnsupported  ComponentErrorCode = "unsupported"
	ComponentPolicyDenied ComponentErrorCode = "policy_denied"
	ComponentFailure      ComponentErrorCode = "component_failure"
)

type ComponentError struct {
	InvocationID string             `json:"invocation_id,omitempty"`
	Operation    Operation          `json:"operation,omitempty"`
	Code         ComponentErrorCode `json:"code"`
}

type CancelPayload struct {
	InvocationID string    `json:"invocation_id"`
	Operation    Operation `json:"operation"`
}
type CanceledPayload struct {
	InvocationID string    `json:"invocation_id"`
	Operation    Operation `json:"operation"`
}

// Frame uses typed conditional payloads. Exactly one payload matching Type is
// required, so unknown role bodies cannot hide in an extension object.
type Frame struct {
	Schema           string              `json:"schema"`
	SchemaVersion    int                 `json:"schema_version"`
	ProtocolVersion  int                 `json:"protocol_version"`
	Direction        Direction           `json:"direction"`
	SessionID        string              `json:"session_id"`
	AttemptID        string              `json:"attempt_id"`
	Sequence         uint32              `json:"sequence"`
	Role             Role                `json:"role"`
	ComponentID      string              `json:"component_id"`
	ComponentVersion string              `json:"component_version"`
	ExecutableSHA256 string              `json:"executable_sha256"`
	Type             MessageType         `json:"type"`
	Initialize       *InitializePayload  `json:"initialize,omitempty"`
	Initialized      *InitializedPayload `json:"initialized,omitempty"`
	Invoke           *InvokePayload      `json:"invoke,omitempty"`
	Result           *ResultPayload      `json:"result,omitempty"`
	Error            *ComponentError     `json:"error,omitempty"`
	Cancel           *CancelPayload      `json:"cancel,omitempty"`
	Canceled         *CanceledPayload    `json:"canceled,omitempty"`
}

// ValidateDescriptor closes role, operation, and capability membership.
func ValidateDescriptor(value Descriptor) error {
	wantOperations, ok := operationsByRole[value.Role]
	if !ok || !validIdentifier(value.ID) || !validVersion(value.Version) ||
		!equalOperations(value.Operations, wantOperations) || len(value.Capabilities) != len(wantOperations) {
		return contractError(ErrorInvalidManifest, nil)
	}
	supported := 0
	for index, operation := range wantOperations {
		claim := value.Capabilities[index]
		if claim.ID != CapabilityFor(value.Role, operation) || !validCapabilityState(claim.State) {
			return contractError(ErrorInvalidManifest, nil)
		}
		if claim.State == CapabilitySupported {
			supported++
		}
	}
	if supported == 0 {
		return contractError(ErrorInvalidManifest, nil)
	}
	return nil
}

// CapabilityFor returns the one closed capability corresponding to an allowed
// role operation.
func CapabilityFor(role Role, operation Operation) CapabilityID {
	for _, allowed := range operationsByRole[role] {
		if allowed == operation {
			return CapabilityID(string(role) + "." + string(operation))
		}
	}
	return ""
}

// ValidateManifest validates semantic closure independently from JSON syntax.
func ValidateManifest(value Manifest) error {
	if value.Schema != ManifestSchema || value.SchemaVersion != ManifestSchemaVersion ||
		value.ContractVersion != ContractVersion || len(value.ProtocolVersions) != 1 ||
		value.ProtocolVersions[0] != ProtocolVersion || !validSHA256(value.ExecutableSHA256) ||
		ValidateDescriptor(value.Component) != nil || value.ConfigurationSchema == nil || len(value.ConfigurationSchema) > MaxConfigurationEntries ||
		len(value.Platforms) == 0 || len(value.Platforms) > MaxPlatformEntries || len(value.Requirements) == 0 {
		return contractError(ErrorInvalidManifest, nil)
	}
	if !validConfigurationSchema(value.ConfigurationSchema) || !validPlatforms(value.Platforms) ||
		!validRequirements(value.Requirements) {
		return contractError(ErrorInvalidManifest, nil)
	}
	return nil
}

// ValidateFrame checks a frame's closed shape. Session-to-session binding is
// performed by ValidateInitialized and ValidateTerminal.
func ValidateFrame(value Frame) error {
	if value.Schema != MessageSchema || value.SchemaVersion != MessageSchemaVersion ||
		value.ProtocolVersion != ProtocolVersion || !validSHA256(value.SessionID) || !validSHA256(value.AttemptID) ||
		!validIdentifier(value.ComponentID) || !validVersion(value.ComponentVersion) ||
		!validSHA256(value.ExecutableSHA256) || operationsByRole[value.Role] == nil {
		return contractError(ErrorInvalidMessage, nil)
	}
	payloads := 0
	for _, present := range []bool{value.Initialize != nil, value.Initialized != nil, value.Invoke != nil,
		value.Result != nil, value.Error != nil, value.Cancel != nil, value.Canceled != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return contractError(ErrorInvalidMessage, nil)
	}
	switch value.Type {
	case MessageInitialize:
		if value.Direction != DirectionHostToExtension || value.Sequence != 1 || value.Initialize == nil ||
			len(value.Initialize.OfferedProtocolVersions) != 1 || value.Initialize.OfferedProtocolVersions[0] != ProtocolVersion ||
			!validCapabilityIDs(value.Role, value.Initialize.RequiredCapabilities) {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageInitialized:
		if value.Direction != DirectionExtensionToHost || value.Sequence != 2 || value.Initialized == nil ||
			value.Initialized.SelectedProtocolVersion != ProtocolVersion || !validClaims(value.Role, value.Initialized.Capabilities) {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageInvoke:
		if value.Direction != DirectionHostToExtension || value.Sequence != 3 || value.Invoke == nil ||
			!validSHA256(value.Invoke.InvocationID) || CapabilityFor(value.Role, value.Invoke.Operation) == "" ||
			(value.Invoke.Control != InvocationExecute && value.Invoke.Control != InvocationAwaitCancel) ||
			value.Invoke.Configuration == nil || value.Invoke.Inputs == nil || !validConfigurationValues(value.Invoke.Configuration) ||
			!validArtifactReferences(value.Invoke.Inputs) || !validInvocationPolicy(value.Invoke.Policy) {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageResult:
		if value.Direction != DirectionExtensionToHost || value.Sequence != 4 || value.Result == nil ||
			!validSHA256(value.Result.InvocationID) || CapabilityFor(value.Role, value.Result.Operation) == "" ||
			value.Result.Outputs == nil || !validArtifactReferences(value.Result.Outputs) {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageError:
		if value.Direction != DirectionExtensionToHost || value.Error == nil || !validComponentError(value.Role, value.Sequence, *value.Error) {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageCancel:
		if value.Direction != DirectionHostToExtension || value.Sequence != 4 || value.Cancel == nil ||
			!validSHA256(value.Cancel.InvocationID) || CapabilityFor(value.Role, value.Cancel.Operation) == "" {
			return contractError(ErrorInvalidMessage, nil)
		}
	case MessageCanceled:
		if value.Direction != DirectionExtensionToHost || value.Sequence != 5 || value.Canceled == nil ||
			!validSHA256(value.Canceled.InvocationID) || CapabilityFor(value.Role, value.Canceled.Operation) == "" {
			return contractError(ErrorInvalidMessage, nil)
		}
	default:
		return contractError(ErrorInvalidMessage, nil)
	}
	return nil
}

func validComponentError(role Role, sequence uint32, value ComponentError) bool {
	if value.Code != ComponentInvalidInput && value.Code != ComponentUnsupported &&
		value.Code != ComponentPolicyDenied && value.Code != ComponentFailure {
		return false
	}
	if sequence == 2 {
		return value.InvocationID == "" && value.Operation == ""
	}
	return sequence == 4 && validSHA256(value.InvocationID) && CapabilityFor(role, value.Operation) != ""
}

func validClaims(role Role, values []CapabilityClaim) bool {
	operations := operationsByRole[role]
	if len(values) != len(operations) {
		return false
	}
	for index, operation := range operations {
		if values[index].ID != CapabilityFor(role, operation) || !validCapabilityState(values[index].State) {
			return false
		}
	}
	return true
}

func validCapabilityState(value CapabilityState) bool {
	return value == CapabilitySupported || value == CapabilityUnsupported || value == CapabilityUnknown || value == CapabilityNotApplicable
}

func validCapabilityIDs(role Role, values []CapabilityID) bool {
	if len(values) == 0 || len(values) > MaxCollectionEntries {
		return false
	}
	allowed := make(map[CapabilityID]bool, len(operationsByRole[role]))
	for _, operation := range operationsByRole[role] {
		allowed[CapabilityFor(role, operation)] = true
	}
	for index, value := range values {
		if !allowed[value] || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func validConfigurationSchema(values []ConfigurationField) bool {
	for index, value := range values {
		if !validIdentifier(value.Name) || (index > 0 && values[index-1].Name >= value.Name) {
			return false
		}
		switch value.Kind {
		case ConfigurationBoolean:
			if len(value.Values) != 0 || value.Minimum != nil || value.Maximum != nil {
				return false
			}
		case ConfigurationInteger:
			if len(value.Values) != 0 || value.Minimum == nil || value.Maximum == nil || *value.Minimum > *value.Maximum {
				return false
			}
		case ConfigurationEnum:
			if value.Minimum != nil || value.Maximum != nil || len(value.Values) == 0 ||
				!sortedUniqueStrings(value.Values) || !allValidIdentifiers(value.Values) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validPlatforms(values []Platform) bool {
	previous := ""
	for _, value := range values {
		if (value.OS != "linux" && value.OS != "darwin" && value.OS != "windows") ||
			(value.Architecture != "amd64" && value.Architecture != "arm64") {
			return false
		}
		key := value.OS + "/" + value.Architecture
		if key <= previous {
			return false
		}
		previous = key
	}
	return true
}

func validRequirements(values []EnforcementRequirement) bool {
	for index, value := range values {
		if !validRequirement(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func validRequirement(value EnforcementRequirement) bool {
	switch value {
	case EnforcementExactEnvironment, EnforcementPrivateWorkingDirectory, EnforcementBoundedIO, EnforcementDeadline,
		EnforcementBestEffortProcessGroup, EnforcementFilesystemIsolation, EnforcementNetworkIsolation,
		EnforcementResourceLimits, EnforcementTerminationProof:
		return true
	default:
		return false
	}
}

func validConfigurationValues(values []ConfigurationValue) bool {
	if len(values) > MaxConfigurationEntries {
		return false
	}
	for index, value := range values {
		if !validIdentifier(value.Name) || (index > 0 && values[index-1].Name >= value.Name) {
			return false
		}
		count := 0
		if value.Boolean != nil {
			count++
		}
		if value.Integer != nil {
			count++
		}
		if value.Enum != "" {
			count++
		}
		if count != 1 || (value.Enum != "" && !validIdentifier(value.Enum)) {
			return false
		}
	}
	return true
}

func validArtifactReferences(values []ArtifactReference) bool {
	if len(values) > MaxCollectionEntries {
		return false
	}
	for index, value := range values {
		if !validIdentifier(value.ID) || !validSchema(value.Schema) || value.SchemaVersion < 1 || !validSHA256(value.SHA256) ||
			!validPrivacy(value.Privacy) || (index > 0 && values[index-1].ID >= value.ID) {
			return false
		}
	}
	return true
}

func validInvocationPolicy(value InvocationPolicy) bool {
	return value.MaxOutputArtifacts <= MaxCollectionEntries && value.MaxOutputBytes <= MaxInvocationOutputBytes &&
		validPrivacy(value.OutputPrivacy) && (value.Replay == ReplaySafe || value.Replay == ReplayUnsafe)
}

func validPrivacy(value Privacy) bool {
	return value == PrivacyPublic || value == PrivacyContentMinimized || value == PrivacyOwnerPrivate
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifierBytes || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
		for index, character := range segment {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
				(index > 0 && (character == '-' || character == '_' || character == '.')) {
				continue
			}
			return false
		}
	}
	return true
}

func validVersion(value string) bool {
	if value == "" || len(value) > MaxComponentVersionBytes {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '-' || character == '+' {
			continue
		}
		return false
	}
	return true
}

func validSchema(value string) bool { return strings.Contains(value, "/") && validIdentifier(value) }

func validSHA256(value string) bool {
	if len(value) != SHA256HexCharacters {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func equalOperations(got, want []Operation) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sortedUniqueStrings(values []string) bool {
	return sort.StringsAreSorted(values) && !hasDuplicateString(values)
}

func allValidIdentifiers(values []string) bool {
	for _, value := range values {
		if !validIdentifier(value) {
			return false
		}
	}
	return true
}

func hasDuplicateString(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return true
		}
	}
	return false
}
