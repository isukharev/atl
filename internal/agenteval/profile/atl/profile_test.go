package atl_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
	profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"
)

type unavailableFactory struct{}

func (unavailableFactory) Open(context.Context, core.AdmittedPlan, core.AttemptIdentity) (core.AttemptRuntime, error) {
	return core.AttemptRuntime{}, errors.New("not executed")
}

func TestProfileSnapshotsAuthoritativeCapabilityProjection(t *testing.T) {
	capabilities := []core.Capability{
		{ID: "work.write", Support: core.SupportUnsupported},
		{ID: "work.read", Support: core.SupportSupported},
	}
	profile := profileatl.New(capabilities, unavailableFactory{})
	capabilities[0].Support = core.SupportSupported

	descriptor := profile.Descriptor()
	want := core.ProfileDescriptor{
		ID: profileatl.ProfileID,
		Capabilities: []core.Capability{
			{ID: "work.read", Support: core.SupportSupported},
			{ID: "work.write", Support: core.SupportUnsupported},
		},
	}
	if !reflect.DeepEqual(descriptor, want) {
		t.Fatalf("descriptor=%+v, want %+v", descriptor, want)
	}
	descriptor.Capabilities[0].Support = core.SupportUnknown
	if got := profile.Descriptor(); !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor was mutable: %+v", got)
	}
	if _, err := core.NewRegistry(profile); err != nil {
		t.Fatalf("compose ATL profile: %v", err)
	}
}

func TestProfileLeavesCapabilityClosureToNeutralRegistry(t *testing.T) {
	profile := profileatl.New([]core.Capability{
		{ID: "duplicate", Support: core.SupportSupported},
		{ID: "duplicate", Support: core.SupportSupported},
	}, unavailableFactory{})
	if _, err := core.NewRegistry(profile); err == nil {
		t.Fatal("duplicate capability unexpectedly composed")
	}
}

func TestProfileRejectsTypedNilRuntimeFactory(t *testing.T) {
	var factory profileatl.RuntimeFactoryFunc
	profile := profileatl.New(nil, factory)
	if _, err := profile.Open(context.Background(), core.AdmittedPlan{}, core.AttemptIdentity{}); err == nil {
		t.Fatal("typed-nil runtime factory unexpectedly opened")
	}
}
