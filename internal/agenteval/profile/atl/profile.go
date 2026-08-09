// Package atl adapts ATL-owned policy and execution components to the neutral
// evaluator core. The composition root supplies the authoritative capability
// snapshot and runtime factory; this package does not duplicate those owners.
package atl

import (
	"context"
	"reflect"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/core"
)

// ProfileID is the closed identity of the built-in ATL compatibility profile.
const ProfileID core.ProfileID = "atl"

// RuntimeFactory supplies the ATL-owned per-attempt adapter, execution, and
// grader components. Implementations remain in the compatibility facade until
// their byte contracts can move independently.
type RuntimeFactory interface {
	Open(context.Context, core.AdmittedPlan, core.AttemptIdentity) (core.AttemptRuntime, error)
}

// RuntimeFactoryFunc adapts a function to RuntimeFactory.
type RuntimeFactoryFunc func(context.Context, core.AdmittedPlan, core.AttemptIdentity) (core.AttemptRuntime, error)

// Open implements RuntimeFactory.
func (f RuntimeFactoryFunc) Open(ctx context.Context, plan core.AdmittedPlan, attempt core.AttemptIdentity) (core.AttemptRuntime, error) {
	if f == nil {
		return core.AttemptRuntime{}, errRuntimeFactoryUnavailable
	}
	return f(ctx, plan, attempt)
}

// Profile is the explicit built-in ATL profile. It snapshots the capability
// projection at construction and has no mutable registration surface.
type Profile struct {
	descriptor core.ProfileDescriptor
	factory    RuntimeFactory
}

// New constructs an ATL profile from the compatibility facade's authoritative
// capability projection and runtime factory. The neutral registry performs the
// final closed-vocabulary validation when this profile is composed.
func New(capabilities []core.Capability, factory RuntimeFactory) *Profile {
	snapshot := append([]core.Capability(nil), capabilities...)
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].ID < snapshot[j].ID })
	return &Profile{
		descriptor: core.ProfileDescriptor{ID: ProfileID, Capabilities: snapshot},
		factory:    factory,
	}
}

// Descriptor returns a defensive copy of the closed ATL profile projection.
func (p *Profile) Descriptor() core.ProfileDescriptor {
	if p == nil {
		return core.ProfileDescriptor{}
	}
	descriptor := p.descriptor
	descriptor.Capabilities = append([]core.Capability(nil), p.descriptor.Capabilities...)
	return descriptor
}

// Open delegates one already-admitted attempt to the compatibility facade.
func (p *Profile) Open(ctx context.Context, plan core.AdmittedPlan, attempt core.AttemptIdentity) (core.AttemptRuntime, error) {
	if p == nil || nilRuntimeFactory(p.factory) {
		return core.AttemptRuntime{}, errRuntimeFactoryUnavailable
	}
	return p.factory.Open(ctx, plan, attempt)
}

func nilRuntimeFactory(factory RuntimeFactory) bool {
	if factory == nil {
		return true
	}
	value := reflect.ValueOf(factory)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
