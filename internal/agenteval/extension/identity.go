package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
)

// ProtocolRole is the canonical machine projection of one role contract.
type ProtocolRole struct {
	Role         Role           `json:"role"`
	Operations   []Operation    `json:"operations"`
	Capabilities []CapabilityID `json:"capabilities"`
}

// ProtocolWireType describes the JSON shape and the Go representation which
// produces it. Nullable records representation-level nullability; semantic
// invariants below can close a nil-capable representation to non-null JSON.
type ProtocolWireType struct {
	JSONShape    string            `json:"json_shape"`
	GoKind       string            `json:"go_kind"`
	NumericForm  string            `json:"numeric_form,omitempty"`
	LogicalType  string            `json:"logical_type,omitempty"`
	Nullable     bool              `json:"nullable"`
	Element      *ProtocolWireType `json:"element,omitempty"`
	PointedValue *ProtocolWireType `json:"pointed_value,omitempty"`
}

// ProtocolWireField binds the complete JSON tag, presence rule, and wire type
// for one field in declaration and encoding order.
type ProtocolWireField struct {
	JSONTag  string           `json:"json_tag"`
	Required bool             `json:"required"`
	Type     ProtocolWireType `json:"type"`
}

// ProtocolWireObject binds every durable or frame-local JSON member, including
// tag options, order, JSON shape, Go representation, and nullability.
type ProtocolWireObject struct {
	Name   string              `json:"name"`
	Fields []ProtocolWireField `json:"fields"`
}

// ProtocolNumericBound is one closed static limit used by wire decoding or
// semantic validation.
type ProtocolNumericBound struct {
	ID       string `json:"id"`
	Relation string `json:"relation"`
	Value    uint64 `json:"value"`
	Unit     string `json:"unit"`
}

// ProtocolInvariant is one named semantic rule which is not fully expressed
// by field types, closed vocabularies, numeric bounds, or transition rows.
type ProtocolInvariant struct {
	ID   string `json:"id"`
	Rule string `json:"rule"`
}

// ProtocolTransition closes direction, sequence, payload, and terminal state.
type ProtocolTransition struct {
	ID        string      `json:"id"`
	Type      MessageType `json:"type"`
	Direction Direction   `json:"direction"`
	Sequence  uint32      `json:"sequence"`
	Payload   string      `json:"payload"`
	Terminal  bool        `json:"terminal"`
}

// ProtocolDescriptor is the content-addressed closed v1 vocabulary. It is not
// an extension manifest and grants no execution authority.
type ProtocolDescriptor struct {
	Schema                string                   `json:"schema"`
	SchemaVersion         int                      `json:"schema_version"`
	ContractVersion       string                   `json:"contract_version"`
	ProtocolVersion       int                      `json:"protocol_version"`
	ManifestSchema        string                   `json:"manifest_schema"`
	ManifestSchemaVersion int                      `json:"manifest_schema_version"`
	MessageSchema         string                   `json:"message_schema"`
	MessageSchemaVersion  int                      `json:"message_schema_version"`
	Roles                 []ProtocolRole           `json:"roles"`
	WireObjects           []ProtocolWireObject     `json:"wire_objects"`
	NumericBounds         []ProtocolNumericBound   `json:"numeric_bounds"`
	SemanticInvariants    []ProtocolInvariant      `json:"semantic_invariants"`
	Transitions           []ProtocolTransition     `json:"transitions"`
	MessageTypes          []MessageType            `json:"message_types"`
	CapabilityStates      []CapabilityState        `json:"capability_states"`
	ConfigurationKinds    []ConfigurationKind      `json:"configuration_kinds"`
	InvocationControls    []InvocationControl      `json:"invocation_controls"`
	PrivacyClasses        []Privacy                `json:"privacy_classes"`
	ReplayPolicies        []ReplayPolicy           `json:"replay_policies"`
	ComponentErrorCodes   []ComponentErrorCode     `json:"component_error_codes"`
	EnforcementVocabulary []EnforcementRequirement `json:"enforcement_requirements"`
}

// CurrentProtocolDescriptor returns a fresh canonical descriptor.
func CurrentProtocolDescriptor() ProtocolDescriptor {
	return ProtocolDescriptor{
		Schema: "agent-eval/extension-protocol", SchemaVersion: 1,
		ContractVersion: ContractVersion, ProtocolVersion: ProtocolVersion,
		ManifestSchema: ManifestSchema, ManifestSchemaVersion: ManifestSchemaVersion,
		MessageSchema: MessageSchema, MessageSchemaVersion: MessageSchemaVersion,
		Roles: roleProtocolDescriptors(), WireObjects: protocolWireObjects(),
		NumericBounds: protocolNumericBounds(), SemanticInvariants: protocolSemanticInvariants(),
		Transitions:         protocolTransitions(),
		MessageTypes:        []MessageType{MessageCancel, MessageCanceled, MessageError, MessageInitialize, MessageInitialized, MessageInvoke, MessageResult},
		CapabilityStates:    []CapabilityState{CapabilityNotApplicable, CapabilitySupported, CapabilityUnknown, CapabilityUnsupported},
		ConfigurationKinds:  []ConfigurationKind{ConfigurationBoolean, ConfigurationEnum, ConfigurationInteger},
		InvocationControls:  []InvocationControl{InvocationAwaitCancel, InvocationExecute},
		PrivacyClasses:      []Privacy{PrivacyContentMinimized, PrivacyOwnerPrivate, PrivacyPublic},
		ReplayPolicies:      []ReplayPolicy{ReplayUnsafe, ReplaySafe},
		ComponentErrorCodes: []ComponentErrorCode{ComponentFailure, ComponentInvalidInput, ComponentPolicyDenied, ComponentUnsupported},
		EnforcementVocabulary: []EnforcementRequirement{
			EnforcementBestEffortProcessGroup, EnforcementBoundedIO, EnforcementDeadline, EnforcementExactEnvironment,
			EnforcementFilesystemIsolation, EnforcementNetworkIsolation, EnforcementPrivateWorkingDirectory,
			EnforcementResourceLimits, EnforcementTerminationProof,
		},
	}
}

func roleProtocolDescriptors() []ProtocolRole {
	roles := []Role{RoleAgentAdapter, RoleExecutionBackend, RoleGrader, RoleProfile, RoleReporter}
	result := make([]ProtocolRole, len(roles))
	for index, role := range roles {
		operations := OperationsForRole(role)
		capabilities := make([]CapabilityID, len(operations))
		for operationIndex, operation := range operations {
			capabilities[operationIndex] = CapabilityFor(role, operation)
		}
		result[index] = ProtocolRole{Role: role, Operations: operations, Capabilities: capabilities}
	}
	return result
}

func protocolWireObjects() []ProtocolWireObject {
	return []ProtocolWireObject{
		protocolWireObject("artifact_reference", ArtifactReference{}),
		protocolWireObject("cancel", CancelPayload{}),
		protocolWireObject("canceled", CanceledPayload{}),
		protocolWireObject("capability_claim", CapabilityClaim{}),
		protocolWireObject("component_descriptor", Descriptor{}),
		protocolWireObject("component_error", ComponentError{}),
		protocolWireObject("configuration_field", ConfigurationField{}),
		protocolWireObject("configuration_value", ConfigurationValue{}),
		protocolWireObject("frame", Frame{}),
		protocolWireObject("initialize", InitializePayload{}),
		protocolWireObject("initialized", InitializedPayload{}),
		protocolWireObject("invocation_policy", InvocationPolicy{}),
		protocolWireObject("invoke", InvokePayload{}),
		protocolWireObject("manifest", Manifest{}),
		protocolWireObject("platform", Platform{}),
		protocolWireObject("result", ResultPayload{}),
	}
}

func protocolWireObject(name string, value any) ProtocolWireObject {
	typeOf := reflect.TypeOf(value)
	if typeOf.Kind() != reflect.Struct {
		panic("extension wire object projection requires a struct")
	}
	fields := make([]ProtocolWireField, 0, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		tag, required := protocolJSONTag(field)
		fields = append(fields, ProtocolWireField{
			JSONTag: tag, Required: required, Type: protocolWireType(field.Type),
		})
	}
	return ProtocolWireObject{Name: name, Fields: fields}
}

func protocolJSONTag(field reflect.StructField) (string, bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		panic("extension wire field is missing a JSON tag")
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" || parts[0] == "-" {
		panic("extension wire field has an invalid JSON tag")
	}
	required := true
	for _, option := range parts[1:] {
		if option != "omitempty" {
			panic("extension wire field has an unsupported JSON tag option")
		}
		required = false
	}
	return tag, required
}

func protocolWireType(typeOf reflect.Type) ProtocolWireType {
	switch typeOf.Kind() {
	case reflect.Pointer:
		pointed := protocolWireType(typeOf.Elem())
		return ProtocolWireType{
			JSONShape: pointed.JSONShape, GoKind: "pointer", Nullable: true, PointedValue: &pointed,
		}
	case reflect.Slice:
		element := protocolWireType(typeOf.Elem())
		return ProtocolWireType{JSONShape: "array", GoKind: "slice", Nullable: true, Element: &element}
	case reflect.Struct:
		return ProtocolWireType{
			JSONShape: "object", GoKind: "struct", LogicalType: protocolLogicalType(typeOf), Nullable: false,
		}
	case reflect.String:
		return ProtocolWireType{
			JSONShape: "string", GoKind: "string", LogicalType: protocolLogicalType(typeOf), Nullable: false,
		}
	case reflect.Bool:
		return ProtocolWireType{JSONShape: "boolean", GoKind: "bool", Nullable: false}
	case reflect.Int:
		return ProtocolWireType{JSONShape: "number", GoKind: "int", NumericForm: "signed-native", Nullable: false}
	case reflect.Int64:
		return ProtocolWireType{JSONShape: "number", GoKind: "int64", NumericForm: "signed-64", Nullable: false}
	case reflect.Uint32:
		return ProtocolWireType{JSONShape: "number", GoKind: "uint32", NumericForm: "unsigned-32", Nullable: false}
	case reflect.Uint64:
		return ProtocolWireType{JSONShape: "number", GoKind: "uint64", NumericForm: "unsigned-64", Nullable: false}
	default:
		panic("extension wire field has an unsupported Go type")
	}
}

func protocolLogicalType(typeOf reflect.Type) string {
	switch typeOf {
	case reflect.TypeOf(Role("")):
		return "role"
	case reflect.TypeOf(Operation("")):
		return "operation"
	case reflect.TypeOf(CapabilityID("")):
		return "capability_id"
	case reflect.TypeOf(CapabilityState("")):
		return "capability_state"
	case reflect.TypeOf(ConfigurationKind("")):
		return "configuration_kind"
	case reflect.TypeOf(InvocationControl("")):
		return "invocation_control"
	case reflect.TypeOf(EnforcementRequirement("")):
		return "enforcement_requirement"
	case reflect.TypeOf(Direction("")):
		return "direction"
	case reflect.TypeOf(MessageType("")):
		return "message_type"
	case reflect.TypeOf(Privacy("")):
		return "privacy"
	case reflect.TypeOf(ReplayPolicy("")):
		return "replay_policy"
	case reflect.TypeOf(ComponentErrorCode("")):
		return "component_error_code"
	case reflect.TypeOf(ArtifactReference{}):
		return "artifact_reference"
	case reflect.TypeOf(CancelPayload{}):
		return "cancel"
	case reflect.TypeOf(CanceledPayload{}):
		return "canceled"
	case reflect.TypeOf(CapabilityClaim{}):
		return "capability_claim"
	case reflect.TypeOf(Descriptor{}):
		return "component_descriptor"
	case reflect.TypeOf(ComponentError{}):
		return "component_error"
	case reflect.TypeOf(ConfigurationField{}):
		return "configuration_field"
	case reflect.TypeOf(ConfigurationValue{}):
		return "configuration_value"
	case reflect.TypeOf(Frame{}):
		return "frame"
	case reflect.TypeOf(InitializePayload{}):
		return "initialize"
	case reflect.TypeOf(InitializedPayload{}):
		return "initialized"
	case reflect.TypeOf(InvocationPolicy{}):
		return "invocation_policy"
	case reflect.TypeOf(InvokePayload{}):
		return "invoke"
	case reflect.TypeOf(Manifest{}):
		return "manifest"
	case reflect.TypeOf(Platform{}):
		return "platform"
	case reflect.TypeOf(ResultPayload{}):
		return "result"
	default:
		if typeOf.PkgPath() != "" {
			panic("extension wire field has an unregistered logical type")
		}
		return ""
	}
}

func protocolNumericBounds() []ProtocolNumericBound {
	return []ProtocolNumericBound{
		{ID: "artifact_reference_entries.maximum", Relation: "maximum", Value: MaxCollectionEntries, Unit: "entries"},
		{ID: "artifact_schema_version.minimum", Relation: "minimum", Value: 1, Unit: "integer"},
		{ID: "component_version_bytes.maximum", Relation: "maximum", Value: MaxComponentVersionBytes, Unit: "bytes"},
		{ID: "configuration_schema_entries.maximum", Relation: "maximum", Value: MaxConfigurationEntries, Unit: "entries"},
		{ID: "configuration_value_entries.maximum", Relation: "maximum", Value: MaxConfigurationEntries, Unit: "entries"},
		{ID: "conformance_deadline_milliseconds.maximum", Relation: "maximum", Value: MaxDeadlineMilliseconds, Unit: "milliseconds"},
		{ID: "descriptor_supported_capabilities.minimum", Relation: "minimum", Value: 1, Unit: "entries"},
		{ID: "frame_body_bytes.maximum", Relation: "maximum", Value: MaxFrameBytes, Unit: "bytes"},
		{ID: "frame_body_bytes.minimum", Relation: "minimum", Value: 2, Unit: "bytes"},
		{ID: "frame_line_bytes.maximum", Relation: "maximum", Value: MaxFrameBytes + 1, Unit: "bytes"},
		{ID: "frame_line_bytes.minimum", Relation: "minimum", Value: 3, Unit: "bytes"},
		{ID: "frame_payloads.exact", Relation: "exact", Value: 1, Unit: "payloads"},
		{ID: "identifier_bytes.maximum", Relation: "maximum", Value: MaxIdentifierBytes, Unit: "bytes"},
		{ID: "initialize_offered_protocol_versions.exact", Relation: "exact", Value: 1, Unit: "entries"},
		{ID: "initialize_required_capabilities.maximum", Relation: "maximum", Value: MaxCollectionEntries, Unit: "entries"},
		{ID: "initialize_required_capabilities.minimum", Relation: "minimum", Value: 1, Unit: "entries"},
		{ID: "invocation_output_artifacts.maximum", Relation: "maximum", Value: MaxCollectionEntries, Unit: "entries"},
		{ID: "invocation_output_bytes.maximum", Relation: "maximum", Value: MaxInvocationOutputBytes, Unit: "bytes"},
		{ID: "json_nesting_depth.maximum", Relation: "maximum", Value: maxJSONDepth, Unit: "levels"},
		{ID: "manifest_bytes.maximum", Relation: "maximum", Value: MaxManifestBytes, Unit: "bytes"},
		{ID: "manifest_bytes.minimum", Relation: "minimum", Value: 3, Unit: "bytes"},
		{ID: "manifest_platforms.maximum", Relation: "maximum", Value: MaxPlatformEntries, Unit: "entries"},
		{ID: "manifest_platforms.minimum", Relation: "minimum", Value: 1, Unit: "entries"},
		{ID: "manifest_protocol_versions.exact", Relation: "exact", Value: 1, Unit: "entries"},
		{ID: "manifest_requirements.minimum", Relation: "minimum", Value: 1, Unit: "entries"},
		{ID: "session_stdin_bytes.maximum", Relation: "maximum", Value: MaxSessionBytes, Unit: "bytes"},
		{ID: "session_stdout_bytes.maximum", Relation: "maximum", Value: MaxSessionBytes, Unit: "bytes"},
		{ID: "sha256_lower_hex_characters.exact", Relation: "exact", Value: SHA256HexCharacters, Unit: "characters"},
		{ID: "stderr_bytes.maximum", Relation: "maximum", Value: MaxStderrBytes, Unit: "bytes"},
	}
}

func protocolSemanticInvariants() []ProtocolInvariant {
	return []ProtocolInvariant{
		{ID: "artifact_reference_closure", Rule: "references=strictly_id_sorted(identity_only(id,valid_schema,schema_version>=1,lower_hex_sha256,size_bytes,closed_privacy)); bodies,paths,handles=absent"},
		{ID: "canonical_jsonl", Rule: "manifest_line,frame_line=utf8_compact_struct_order_json+exactly_one_lf; frame_body=canonical_json_without_cr_or_lf; reject=invalid_utf8|unknown_member|duplicate_member|trailing_value|noncanonical_reencode|depth_over_bound"},
		{ID: "capability_claim_completeness", Rule: "descriptor.operations=exact_ordered_role_operations; descriptor.capabilities=exact_ordered_role_operation_claims; supported_count>=1; initialize.required_capabilities=ordered_supported_subset; initialized.capabilities=manifest.capabilities"},
		{ID: "closed_vocabulary", Rule: "roles,operations,capability_states,configuration_kinds,invocation_controls,directions,message_types,privacy,replay,error_codes,enforcement_requirements=descriptor_sets_only"},
		{ID: "component_error_closure", Rule: "error=closed_code_without_text_retry_lifecycle_or_scoring_authority; sequence_2_error_has_no_invocation_or_operation; sequence_4_error_echoes_invoke_invocation_and_operation"},
		{ID: "configuration_schema_closure", Rule: "fields=strictly_name_sorted; boolean_has_no_values_or_bounds; integer_has_both_ordered_bounds; enum_has_nonempty_sorted_unique_identifier_values_and_no_bounds"},
		{ID: "frame_identity_binding", Rule: "initialize_and_invoke(role,component_id,component_version,executable_sha256)=manifest; invoke(session_id,attempt_id)=initialize; every_response(schema,schema_version,protocol_version,session_id,attempt_id,role,component_id,component_version,executable_sha256)=request"},
		{ID: "frame_payload_closure", Rule: "frame_has_exactly_one_nonnull_payload_matching_message_type; direction,sequence,payload,terminal=one_transition_row"},
		{ID: "invocation_configuration_binding", Rule: "invoke.operation=manifest_supported_operation; invoke.configuration=strictly_name_sorted_exact_manifest_schema_match(required,type,integer_range,enum_membership)"},
		{ID: "invocation_control_binding", Rule: "execute=operation_may_run_with_only_result_or_error_terminal; await_cancel=operation_must_not_run_and_result_or_error_forbidden_while_waiting_for_host_cancel; canceled_is_protocol_acknowledgment_not_lifecycle_or_termination_proof"},
		{ID: "invocation_nonce_binding", Rule: "invocation_id=absent_from_initialize_initialized_and_sequence_2_error; first_wire_disclosure=invoke_sequence_3_lower_hex_sha256; result_sequence_4_error_cancel_canceled=exact_invoke_echo"},
		{ID: "nonnull_collections", Rule: "nonnull=descriptor.operations,descriptor.capabilities,manifest.protocol_versions,manifest.configuration_schema,manifest.platforms,manifest.requirements,initialize.offered_protocol_versions,initialize.required_capabilities,initialized.capabilities,invoke.configuration,invoke.inputs,result.outputs; empty_allowed_collections_encode=[]"},
		{ID: "output_policy_enforcement", Rule: "result.outputs_count<=invoke.policy.max_output_artifacts; overflow_safe_sum(result.output.size_bytes)<=invoke.policy.max_output_bytes; every_result_output.privacy=invoke.policy.output_privacy"},
		{ID: "platform_closure", Rule: "manifest.platforms=strictly_sorted_unique_pairs(os in [darwin,linux,windows],architecture in [amd64,arm64])"},
		{ID: "requirement_closure", Rule: "manifest.requirements=strictly_sorted_unique_closed_enforcement_requirements"},
		{ID: "schema_identity", Rule: "manifest_schema,manifest_schema_version,contract_version,message_schema,message_schema_version,protocol_version=descriptor_exact_values"},
		{ID: "terminal_binding", Rule: "initialize_error=terminal_sequence_2; invoke_result_or_error=terminal_sequence_4_with_exact_invocation_and_operation; canceled=terminal_sequence_5_only_for_exact_cancel; no_other_terminal"},
		{ID: "transition_closure", Rule: "allowed_transitions=descriptor.transitions_exactly; no_implicit_transition_retry_or_lifecycle_authority"},
	}
}

func protocolTransitions() []ProtocolTransition {
	return []ProtocolTransition{
		{ID: "cancel", Type: MessageCancel, Direction: DirectionHostToExtension, Sequence: 4, Payload: "cancel"},
		{ID: "canceled", Type: MessageCanceled, Direction: DirectionExtensionToHost, Sequence: 5, Payload: "canceled", Terminal: true},
		{ID: "initialization-error", Type: MessageError, Direction: DirectionExtensionToHost, Sequence: 2, Payload: "component_error", Terminal: true},
		{ID: "initialize", Type: MessageInitialize, Direction: DirectionHostToExtension, Sequence: 1, Payload: "initialize"},
		{ID: "initialized", Type: MessageInitialized, Direction: DirectionExtensionToHost, Sequence: 2, Payload: "initialized"},
		{ID: "invoke", Type: MessageInvoke, Direction: DirectionHostToExtension, Sequence: 3, Payload: "invoke"},
		{ID: "invoke-error", Type: MessageError, Direction: DirectionExtensionToHost, Sequence: 4, Payload: "component_error", Terminal: true},
		{ID: "result", Type: MessageResult, Direction: DirectionExtensionToHost, Sequence: 4, Payload: "result", Terminal: true},
	}
}

func protocolDescriptorBytes(descriptor ProtocolDescriptor) []byte {
	data, err := json.Marshal(descriptor)
	if err != nil {
		panic("static extension protocol descriptor is not encodable")
	}
	return append(data, '\n')
}

func protocolDescriptorSHA256(descriptor ProtocolDescriptor) string {
	digest := sha256.Sum256(protocolDescriptorBytes(descriptor))
	return hex.EncodeToString(digest[:])
}

// ProtocolContractBytes returns the canonical content bound by conformance
// bundles and reports.
func ProtocolContractBytes() []byte {
	return protocolDescriptorBytes(CurrentProtocolDescriptor())
}

// ProtocolSHA256 identifies the complete closed v1 vocabulary projection.
func ProtocolSHA256() string {
	return protocolDescriptorSHA256(CurrentProtocolDescriptor())
}

// ContractSHA256 binds the pre-release standalone contract identity separately
// from the process-protocol vocabulary.
func ContractSHA256() string {
	digest := sha256.Sum256([]byte("agent-eval/standalone-contract/v1\x00" + ContractVersion))
	return hex.EncodeToString(digest[:])
}
