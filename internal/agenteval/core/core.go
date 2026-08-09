// Package core defines the in-memory, domain-neutral evaluator contracts.
package core

import (
	"context"
	"errors"
	"sort"
)

const (
	maxCollectionEntries = 256
	maxAttempts          = 1024
	maxCheckWeight       = 1_000_000
)

// ProfileID identifies one closed evaluator profile.
type ProfileID string

// PlanID identifies one admitted evaluation plan.
type PlanID string

// TaskID identifies the work evaluated by a plan.
type TaskID string

// FixtureID identifies the in-memory fixture selected by a plan.
type FixtureID string

// TreatmentID identifies one treatment applied to a task.
type TreatmentID string

// SkillID identifies one skill in a treatment.
type SkillID string

// CapabilityID identifies one capability negotiated at admission.
type CapabilityID string

// CheckID identifies one independently graded condition.
type CheckID string

// ResourceID identifies one non-negative resource measurement.
type ResourceID string

// EvidenceID identifies one evidence condition observed during execution.
type EvidenceID string

// Support is the closed capability-negotiation state.
type Support uint8

const (
	SupportUnknown Support = iota
	SupportSupported
	SupportUnsupported
	SupportNotApplicable
)

func (s Support) valid() bool {
	return s <= SupportNotApplicable
}

// Presence is the closed state of one observation or grade.
type Presence uint8

const (
	PresenceUnknown Presence = iota
	PresenceObserved
	PresenceUnsupported
	PresenceNotApplicable
)

func (p Presence) valid() bool {
	return p <= PresenceNotApplicable
}

// Outcome is the closed terminal interpretation of one attempt.
type Outcome uint8

const (
	OutcomeUnknown Outcome = iota
	OutcomeSucceeded
	OutcomeFailed
	OutcomeNotApplicable
)

// ErrorCode is the closed failure classification returned by this package.
type ErrorCode string

const (
	ErrorInvalidRegistry         ErrorCode = "invalid_registry"
	ErrorInvalidPlan             ErrorCode = "invalid_plan"
	ErrorInvalidTask             ErrorCode = "invalid_task"
	ErrorProfileNotFound         ErrorCode = "profile_not_found"
	ErrorCapabilityUndeclared    ErrorCode = "capability_undeclared"
	ErrorCapabilityUnknown       ErrorCode = "capability_unknown"
	ErrorCapabilityUnsupported   ErrorCode = "capability_unsupported"
	ErrorCapabilityNotApplicable ErrorCode = "capability_not_applicable"
	ErrorInterrupted             ErrorCode = "interrupted"
	ErrorProfileOpenFailed       ErrorCode = "profile_open_failed"
	ErrorExecutionFailed         ErrorCode = "execution_failed"
	ErrorInvalidObservation      ErrorCode = "invalid_observation"
	ErrorGradingFailed           ErrorCode = "grading_failed"
	ErrorInvalidGrade            ErrorCode = "invalid_grade"
	ErrorAggregationFailed       ErrorCode = "aggregation_failed"
)

// Error exposes a closed classification while retaining an inspectable cause.
// Its rendered form never includes the cause or caller-supplied identifiers.
type Error struct {
	code  ErrorCode
	cause error
}

func newError(code ErrorCode, cause error) error {
	return &Error{code: code, cause: cause}
}

func (e *Error) Error() string {
	return string(e.code)
}

// Code returns the closed classification.
func (e *Error) Code() ErrorCode {
	return e.code
}

// Unwrap retains programmatic access to a stage failure without rendering it.
func (e *Error) Unwrap() error {
	return e.cause
}

// CodeOf extracts a core error classification.
func CodeOf(err error) (ErrorCode, bool) {
	var coreError *Error
	if !errors.As(err, &coreError) {
		return "", false
	}
	return coreError.Code(), true
}

// Capability describes one profile capability and its negotiated support.
type Capability struct {
	ID      CapabilityID
	Support Support
}

// ProfileDescriptor is the immutable registry projection of one profile.
type ProfileDescriptor struct {
	ID           ProfileID
	Capabilities []Capability
}

// Check declares one positive grading weight.
type Check struct {
	ID     CheckID
	Weight uint32
}

// Task declares the complete observation vocabulary for one unit of work.
// Every declared check, resource, and evidence identity must occur exactly once
// in an observation; absence is represented explicitly with PresenceUnknown.
type Task struct {
	ID                   TaskID
	RequiredCapabilities []CapabilityID
	Checks               []Check
	Resources            []ResourceID
	Evidence             []EvidenceID
}

// Fixture identifies the data selected for an in-memory attempt.
type Fixture struct {
	ID FixtureID
}

// Skill identifies one admitted treatment component.
type Skill struct {
	ID SkillID
}

// Treatment declares a closed, ordered-independent set of skills.
type Treatment struct {
	ID     TreatmentID
	Skills []Skill
}

// Plan is the caller-owned input admitted by an Engine. Admission snapshots
// every slice before any profile method is called.
type Plan struct {
	ID        PlanID
	Profile   ProfileID
	Task      Task
	Fixture   Fixture
	Treatment Treatment
	Attempts  uint32
}

// CheckObservation records whether a declared check was observed and, when it
// was, its boolean value.
type CheckObservation struct {
	ID       CheckID
	Presence Presence
	Passed   bool
}

// ResourceObservation records a non-negative resource value. An observed zero
// remains distinct from unknown, unsupported, and not-applicable states.
type ResourceObservation struct {
	ID       ResourceID
	Presence Presence
	Value    uint64
}

// EvidenceObservation records the presence and acceptance of declared evidence.
type EvidenceObservation struct {
	ID       EvidenceID
	Presence Presence
	Accepted bool
}

// Observation is the complete normalized result returned by an adapter.
type Observation struct {
	Checks    []CheckObservation
	Resources []ResourceObservation
	Evidence  []EvidenceObservation
}

// CheckGrade is one grader decision. Its presence must match the corresponding
// check observation, so grading cannot invent missing coverage.
type CheckGrade struct {
	ID       CheckID
	Presence Presence
	Passed   bool
}

// Grade contains one decision for every task check. Outcome and score are
// derived by the engine rather than trusted from a profile implementation.
type Grade struct {
	Checks []CheckGrade
}

// Score is a deterministic integer score and its explicit presence state.
type Score struct {
	Presence    Presence
	BasisPoints uint16
}

// AttemptIdentity is the deterministic in-memory identity of one attempt.
type AttemptIdentity struct {
	Plan      PlanID
	Task      TaskID
	Treatment TreatmentID
	Ordinal   uint32
}

// AdmittedPlan is an immutable plan snapshot. Accessors return fresh copies.
type AdmittedPlan struct {
	plan       Plan
	descriptor ProfileDescriptor
}

// Plan returns a copy of the canonical admitted plan.
func (a AdmittedPlan) Plan() Plan {
	return clonePlan(a.plan)
}

// Profile returns a copy of the profile descriptor used for admission.
func (a AdmittedPlan) Profile() ProfileDescriptor {
	return cloneDescriptor(a.descriptor)
}

// AttemptInput is the immutable input supplied to execution and grading.
type AttemptInput struct {
	identity  AttemptIdentity
	task      Task
	fixture   Fixture
	treatment Treatment
}

// Identity returns the attempt identity.
func (i AttemptInput) Identity() AttemptIdentity {
	return i.identity
}

// Task returns a copy of the admitted task.
func (i AttemptInput) Task() Task {
	return cloneTask(i.task)
}

// Fixture returns the admitted fixture.
func (i AttemptInput) Fixture() Fixture {
	return i.fixture
}

// Treatment returns a copy of the admitted treatment.
func (i AttemptInput) Treatment() Treatment {
	return cloneTreatment(i.treatment)
}

// AgentAdapter performs one attempt. Implementations are opened per attempt by
// a Profile and may therefore keep attempt-local in-memory state.
type AgentAdapter interface {
	Execute(context.Context, AttemptInput) (Observation, error)
}

// ExecutionBackend applies execution policy around one adapter call.
type ExecutionBackend interface {
	Run(context.Context, AttemptInput, AgentAdapter) (Observation, error)
}

// Grader converts a validated observation into one decision per task check.
type Grader interface {
	Grade(context.Context, AttemptInput, Observation) (Grade, error)
}

// AttemptRuntime is a freshly opened per-attempt component set.
type AttemptRuntime struct {
	Adapter AgentAdapter
	Backend ExecutionBackend
	Grader  Grader
}

// Profile declares capabilities and opens fresh state for each admitted attempt.
type Profile interface {
	Descriptor() ProfileDescriptor
	Open(context.Context, AdmittedPlan, AttemptIdentity) (AttemptRuntime, error)
}

type registeredProfile struct {
	implementation Profile
	descriptor     ProfileDescriptor
	capabilities   map[CapabilityID]Support
}

// Registry is a closed immutable snapshot. It has no registration method;
// construct a replacement registry to use a different profile set.
type Registry struct {
	profiles map[ProfileID]registeredProfile
}

// NewRegistry validates and snapshots an explicit profile set.
func NewRegistry(profiles ...Profile) (*Registry, error) {
	if len(profiles) == 0 || len(profiles) > maxCollectionEntries {
		return nil, newError(ErrorInvalidRegistry, nil)
	}
	registered := make(map[ProfileID]registeredProfile, len(profiles))
	for _, profile := range profiles {
		if nilInterface(profile) {
			return nil, newError(ErrorInvalidRegistry, nil)
		}
		descriptor, capabilities, err := validateDescriptor(profile.Descriptor())
		if err != nil {
			return nil, err
		}
		if _, exists := registered[descriptor.ID]; exists {
			return nil, newError(ErrorInvalidRegistry, nil)
		}
		registered[descriptor.ID] = registeredProfile{
			implementation: profile,
			descriptor:     descriptor,
			capabilities:   capabilities,
		}
	}
	return &Registry{profiles: registered}, nil
}

// Profiles returns sorted copies of every registered descriptor.
func (r *Registry) Profiles() []ProfileDescriptor {
	if r == nil {
		return nil
	}
	descriptors := make([]ProfileDescriptor, 0, len(r.profiles))
	for _, profile := range r.profiles {
		descriptors = append(descriptors, cloneDescriptor(profile.descriptor))
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].ID < descriptors[j].ID
	})
	return descriptors
}

// Describe returns an immutable copy of one descriptor.
func (r *Registry) Describe(id ProfileID) (ProfileDescriptor, bool) {
	if r == nil {
		return ProfileDescriptor{}, false
	}
	profile, ok := r.profiles[id]
	if !ok {
		return ProfileDescriptor{}, false
	}
	return cloneDescriptor(profile.descriptor), true
}

func (r *Registry) resolve(id ProfileID) (registeredProfile, bool) {
	if r == nil {
		return registeredProfile{}, false
	}
	profile, ok := r.profiles[id]
	return profile, ok
}

func validateDescriptor(input ProfileDescriptor) (ProfileDescriptor, map[CapabilityID]Support, error) {
	if !validIdentifier(string(input.ID)) || len(input.Capabilities) > maxCollectionEntries {
		return ProfileDescriptor{}, nil, newError(ErrorInvalidRegistry, nil)
	}
	descriptor := cloneDescriptor(input)
	capabilities := make(map[CapabilityID]Support, len(descriptor.Capabilities))
	for _, capability := range descriptor.Capabilities {
		if !validIdentifier(string(capability.ID)) || !capability.Support.valid() {
			return ProfileDescriptor{}, nil, newError(ErrorInvalidRegistry, nil)
		}
		if _, duplicate := capabilities[capability.ID]; duplicate {
			return ProfileDescriptor{}, nil, newError(ErrorInvalidRegistry, nil)
		}
		capabilities[capability.ID] = capability.Support
	}
	sort.Slice(descriptor.Capabilities, func(i, j int) bool {
		return descriptor.Capabilities[i].ID < descriptor.Capabilities[j].ID
	})
	return descriptor, capabilities, nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 || !identifierFirst(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !identifierFirst(character) && character != '.' && character != '_' && character != '-' && character != '/' {
			return false
		}
	}
	return true
}

func identifierFirst(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func cloneDescriptor(input ProfileDescriptor) ProfileDescriptor {
	result := input
	result.Capabilities = append([]Capability(nil), input.Capabilities...)
	return result
}

func cloneTask(input Task) Task {
	result := input
	result.RequiredCapabilities = append([]CapabilityID(nil), input.RequiredCapabilities...)
	result.Checks = append([]Check(nil), input.Checks...)
	result.Resources = append([]ResourceID(nil), input.Resources...)
	result.Evidence = append([]EvidenceID(nil), input.Evidence...)
	return result
}

func cloneTreatment(input Treatment) Treatment {
	result := input
	result.Skills = append([]Skill(nil), input.Skills...)
	return result
}

func clonePlan(input Plan) Plan {
	result := input
	result.Task = cloneTask(input.Task)
	result.Treatment = cloneTreatment(input.Treatment)
	return result
}
