package extension

import (
	"bytes"
	"strings"
	"testing"
)

func TestExtensionManifestV1IsClosed(t *testing.T) {
	for _, role := range []Role{RoleAgentAdapter, RoleExecutionBackend, RoleGrader, RoleProfile, RoleReporter} {
		t.Run(string(role), func(t *testing.T) {
			manifest := validManifest(role)
			encoded, err := EncodeManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeManifest(encoded)
			if err != nil || decoded.Component.Role != role {
				t.Fatalf("decode role=%q err=%v", decoded.Component.Role, err)
			}
			if again, err := EncodeManifest(decoded); err != nil || !bytes.Equal(again, encoded) {
				t.Fatalf("manifest is not canonical: %v", err)
			}
		})
	}

	baseline, err := EncodeManifest(validManifest(RoleAgentAdapter))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"unknown member":   bytes.Replace(baseline, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1),
		"duplicate member": bytes.Replace(baseline, []byte(`{"schema":`), []byte(`{"schema":"agent-eval/adapter-manifest","schema":`), 1),
		"future schema":    bytes.Replace(baseline, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"trailing value":   append(append([]byte(nil), baseline...), []byte("{}\n")...),
		"noncanonical":     bytes.Replace(baseline, []byte(`{"schema":`), []byte(`{ "schema":`), 1),
		"invalid utf8":     append([]byte{0xff}, baseline...),
	}
	for name, data := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeManifest(data); err == nil {
				t.Fatal("invalid manifest passed")
			}
		})
	}

	semantic := []func(*Manifest){
		func(value *Manifest) { value.ConfigurationSchema = nil },
		func(value *Manifest) { value.Component.Role = "other" },
		func(value *Manifest) {
			value.Component.Operations[0], value.Component.Operations[1] = value.Component.Operations[1], value.Component.Operations[0]
		},
		func(value *Manifest) { value.Component.Capabilities[0].ID = "agent-adapter.other" },
		func(value *Manifest) { value.Component.Capabilities[0].State = "available" },
		func(value *Manifest) {
			value.Component.Capabilities[0].State = CapabilityUnsupported
			value.Component.Capabilities[1].State = CapabilityUnsupported
			value.Component.Capabilities[2].State = CapabilityUnsupported
		},
		func(value *Manifest) { value.Requirements[0] = "ambient" },
		func(value *Manifest) { value.Platforms[0].OS = "freebsd" },
		func(value *Manifest) { value.ConfigurationSchema[0].Kind = "string" },
		func(value *Manifest) { value.ConfigurationSchema[1].Values = []string{"../escape"} },
	}
	for index, mutate := range semantic {
		value := validManifest(RoleAgentAdapter)
		mutate(&value)
		if err := ValidateManifest(value); err == nil {
			t.Fatalf("semantic mutation %d passed", index)
		}
	}
}

func TestExtensionProtocolIdentityIsContentAddressed(t *testing.T) {
	if got, want := ContractSHA256(), "940097369477a21ebbd81f3833fd560ca8e474ec9113f50ffbf56cf5b07468f7"; got != want {
		t.Fatalf("contract digest=%s, want %s", got, want)
	}
	if got, want := ProtocolSHA256(), "1b77f4edf83eed0394e8421311ea48874c0526ab40fbdf9626f4947840a65867"; got != want {
		t.Fatalf("protocol digest=%s, want %s", got, want)
	}
}

func TestExtensionProtocolSharedBoundsMatchValidation(t *testing.T) {
	descriptor := CurrentProtocolDescriptor()
	for id, want := range map[string]uint64{
		"configuration_schema_entries.maximum":      MaxConfigurationEntries,
		"conformance_deadline_milliseconds.maximum": MaxDeadlineMilliseconds,
		"identifier_bytes.maximum":                  MaxIdentifierBytes,
		"invocation_output_bytes.maximum":           MaxInvocationOutputBytes,
		"manifest_platforms.maximum":                MaxPlatformEntries,
		"session_stdin_bytes.maximum":               MaxSessionBytes,
		"session_stdout_bytes.maximum":              MaxSessionBytes,
		"stderr_bytes.maximum":                      MaxStderrBytes,
	} {
		bound := protocolBoundForTest(t, &descriptor, id)
		if bound.Value != want {
			t.Fatalf("bound %s=%d, want %d", id, bound.Value, want)
		}
	}
	if !validIdentifier(strings.Repeat("a", MaxIdentifierBytes)) ||
		validIdentifier(strings.Repeat("a", MaxIdentifierBytes+1)) {
		t.Fatal("identifier boundary drifted")
	}
	if !validVersion(strings.Repeat("v", MaxComponentVersionBytes)) ||
		validVersion(strings.Repeat("v", MaxComponentVersionBytes+1)) {
		t.Fatal("component version boundary drifted")
	}
	policy := InvocationPolicy{
		MaxOutputArtifacts: MaxCollectionEntries, MaxOutputBytes: MaxInvocationOutputBytes,
		OutputPrivacy: PrivacyPublic, Replay: ReplayUnsafe,
	}
	if !validInvocationPolicy(policy) {
		t.Fatal("maximum invocation policy was rejected")
	}
	policy.MaxOutputBytes++
	if validInvocationPolicy(policy) {
		t.Fatal("over-maximum invocation policy was accepted")
	}
}

func validManifest(role Role) Manifest {
	operations := OperationsForRole(role)
	capabilities := make([]CapabilityClaim, len(operations))
	for index, operation := range operations {
		capabilities[index] = CapabilityClaim{ID: CapabilityFor(role, operation), State: CapabilitySupported}
	}
	minimum, maximum := int64(1), int64(10)
	return Manifest{
		Schema: ManifestSchema, SchemaVersion: ManifestSchemaVersion, ContractVersion: ContractVersion,
		ProtocolVersions: []int{ProtocolVersion},
		Component:        Descriptor{ID: "synthetic-component", Version: "1.0.0", Role: role, Operations: operations, Capabilities: capabilities},
		ExecutableSHA256: strings.Repeat("a", 64),
		ConfigurationSchema: []ConfigurationField{
			{Name: "attempts", Kind: ConfigurationInteger, Required: true, Minimum: &minimum, Maximum: &maximum},
			{Name: "mode", Kind: ConfigurationEnum, Required: true, Values: []string{"strict", "synthetic"}},
		},
		Platforms: []Platform{{OS: "linux", Architecture: "amd64"}},
		Requirements: []EnforcementRequirement{
			EnforcementBestEffortProcessGroup, EnforcementBoundedIO, EnforcementDeadline,
			EnforcementExactEnvironment, EnforcementPrivateWorkingDirectory,
		},
	}
}
