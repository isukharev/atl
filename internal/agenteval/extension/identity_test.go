package extension

import (
	"reflect"
	"strings"
	"testing"
)

func TestProtocolIdentityMutationOracle(t *testing.T) {
	baseline := ProtocolSHA256()
	tests := []struct {
		name   string
		mutate func(*ProtocolDescriptor)
	}{
		{
			name: "json tag option",
			mutate: func(value *ProtocolDescriptor) {
				field := protocolFieldForTest(t, value, "frame", "initialize,omitempty")
				field.JSONTag = strings.TrimSuffix(field.JSONTag, ",omitempty")
			},
		},
		{
			name: "wire field type",
			mutate: func(value *ProtocolDescriptor) {
				field := protocolFieldForTest(t, value, "manifest", "schema_version")
				field.Type = protocolWireType(reflect.TypeOf(int64(0)))
			},
		},
		{
			name: "numeric bound",
			mutate: func(value *ProtocolDescriptor) {
				bound := protocolBoundForTest(t, value, "frame_body_bytes.maximum")
				bound.Value++
			},
		},
		{
			name: "semantic invariant",
			mutate: func(value *ProtocolDescriptor) {
				invariant := protocolInvariantForTest(t, value, "invocation_nonce_binding")
				invariant.Rule += "; altered"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := CurrentProtocolDescriptor()
			test.mutate(&descriptor)
			if got := protocolDescriptorSHA256(descriptor); got == baseline {
				t.Fatalf("mutation retained protocol digest %s", got)
			}
		})
	}
}

func TestProtocolWireProjectionIncludesOptionsTypesAndNullability(t *testing.T) {
	descriptor := CurrentProtocolDescriptor()
	optional := protocolFieldForTest(t, &descriptor, "frame", "initialize,omitempty")
	if optional.Required || optional.Type.GoKind != "pointer" || !optional.Type.Nullable ||
		optional.Type.JSONShape != "object" || optional.Type.PointedValue == nil ||
		optional.Type.PointedValue.LogicalType != "initialize" {
		t.Fatalf("optional frame payload projection=%+v", *optional)
	}

	invocationID := protocolFieldForTest(t, &descriptor, "invoke", "invocation_id")
	if !invocationID.Required || invocationID.Type.JSONShape != "string" ||
		invocationID.Type.GoKind != "string" || invocationID.Type.Nullable {
		t.Fatalf("invocation identity projection=%+v", *invocationID)
	}
	control := protocolFieldForTest(t, &descriptor, "invoke", "control")
	if !control.Required || control.Type.JSONShape != "string" ||
		control.Type.LogicalType != "invocation_control" || control.Type.Nullable {
		t.Fatalf("invocation control projection=%+v", *control)
	}

	inputs := protocolFieldForTest(t, &descriptor, "invoke", "inputs")
	if !inputs.Required || inputs.Type.JSONShape != "array" || inputs.Type.GoKind != "slice" ||
		!inputs.Type.Nullable || inputs.Type.Element == nil || inputs.Type.Element.LogicalType != "artifact_reference" {
		t.Fatalf("input collection projection=%+v", *inputs)
	}
}

func TestProtocolDescriptorProjectionIsDeterministic(t *testing.T) {
	first := CurrentProtocolDescriptor()
	second := CurrentProtocolDescriptor()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fresh protocol descriptors differ")
	}
	if got, want := protocolDescriptorSHA256(first), ProtocolSHA256(); got != want {
		t.Fatalf("descriptor digest=%s, exported digest=%s", got, want)
	}
	contract := ProtocolContractBytes()
	if len(contract) == 0 || contract[len(contract)-1] != '\n' || strings.ContainsRune(string(contract[:len(contract)-1]), '\n') {
		t.Fatal("protocol descriptor is not one canonical JSONL record")
	}
	for index := 1; index < len(first.WireObjects); index++ {
		if first.WireObjects[index-1].Name >= first.WireObjects[index].Name {
			t.Fatal("wire objects are not strictly sorted")
		}
	}
	for index := 1; index < len(first.NumericBounds); index++ {
		if first.NumericBounds[index-1].ID >= first.NumericBounds[index].ID {
			t.Fatal("numeric bounds are not strictly sorted")
		}
	}
	for index := 1; index < len(first.SemanticInvariants); index++ {
		if first.SemanticInvariants[index-1].ID >= first.SemanticInvariants[index].ID {
			t.Fatal("semantic invariants are not strictly sorted")
		}
	}
}

func protocolFieldForTest(t *testing.T, descriptor *ProtocolDescriptor, objectName, jsonTag string) *ProtocolWireField {
	t.Helper()
	for objectIndex := range descriptor.WireObjects {
		object := &descriptor.WireObjects[objectIndex]
		if object.Name != objectName {
			continue
		}
		for fieldIndex := range object.Fields {
			if object.Fields[fieldIndex].JSONTag == jsonTag {
				return &object.Fields[fieldIndex]
			}
		}
		t.Fatalf("wire field %s.%s is absent", objectName, jsonTag)
	}
	t.Fatalf("wire object %s is absent", objectName)
	return nil
}

func protocolBoundForTest(t *testing.T, descriptor *ProtocolDescriptor, id string) *ProtocolNumericBound {
	t.Helper()
	for index := range descriptor.NumericBounds {
		if descriptor.NumericBounds[index].ID == id {
			return &descriptor.NumericBounds[index]
		}
	}
	t.Fatalf("numeric bound %s is absent", id)
	return nil
}

func protocolInvariantForTest(t *testing.T, descriptor *ProtocolDescriptor, id string) *ProtocolInvariant {
	t.Helper()
	for index := range descriptor.SemanticInvariants {
		if descriptor.SemanticInvariants[index].ID == id {
			return &descriptor.SemanticInvariants[index]
		}
	}
	t.Fatalf("semantic invariant %s is absent", id)
	return nil
}
