package core_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
)

type fakeProfile struct {
	descriptor core.ProfileDescriptor
	opens      int
	executions *int
	observe    func(uint64) core.Observation
	grade      func(core.Observation) core.Grade
}

func (p *fakeProfile) Descriptor() core.ProfileDescriptor {
	return p.descriptor
}

func (p *fakeProfile) Open(_ context.Context, _ core.AdmittedPlan, _ core.AttemptIdentity) (core.AttemptRuntime, error) {
	p.opens++
	return core.AttemptRuntime{
		Adapter: &counterAdapter{executions: p.executions, observe: p.observe},
		Backend: directBackend{},
		Grader:  fakeGrader{grade: p.grade},
	}, nil
}

type counterAdapter struct {
	count      uint64
	executions *int
	observe    func(uint64) core.Observation
}

func (a *counterAdapter) Execute(context.Context, core.AttemptInput) (core.Observation, error) {
	a.count++
	if a.executions != nil {
		*a.executions++
	}
	return a.observe(a.count), nil
}

type directBackend struct{}

func (directBackend) Run(ctx context.Context, input core.AttemptInput, adapter core.AgentAdapter) (core.Observation, error) {
	return adapter.Execute(ctx, input)
}

type fakeGrader struct {
	grade func(core.Observation) core.Grade
}

func (g fakeGrader) Grade(_ context.Context, _ core.AttemptInput, observation core.Observation) (core.Grade, error) {
	return g.grade(observation), nil
}

func TestCoreRegistryIsExplicitImmutableAndClosed(t *testing.T) {
	first := validFakeProfile()
	first.descriptor.ID = "profile-z"
	first.descriptor.Capabilities = []core.Capability{{ID: "cap-z", Support: core.SupportUnknown}}
	second := validFakeProfile()
	second.descriptor.ID = "profile-a"

	registry, err := core.NewRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	profiles := registry.Profiles()
	if len(profiles) != 2 || profiles[0].ID != "profile-a" || profiles[1].ID != "profile-z" {
		t.Fatalf("profiles=%+v", profiles)
	}

	first.descriptor.Capabilities[0].Support = core.SupportSupported
	profiles[1].Capabilities[0].Support = core.SupportSupported
	descriptor, ok := registry.Describe("profile-z")
	if !ok || descriptor.Capabilities[0].Support != core.SupportUnknown {
		t.Fatalf("registry descriptor mutated: %+v", descriptor)
	}

	if _, err := core.NewRegistry(first, first); errorCode(err) != core.ErrorInvalidRegistry {
		t.Fatalf("duplicate profile error=%v", err)
	}
	invalid := validFakeProfile()
	invalid.descriptor.Capabilities = append(invalid.descriptor.Capabilities, invalid.descriptor.Capabilities[0])
	if _, err := core.NewRegistry(invalid); errorCode(err) != core.ErrorInvalidRegistry {
		t.Fatalf("duplicate capability error=%v", err)
	}
	var nilProfile *fakeProfile
	if _, err := core.NewRegistry(nilProfile); errorCode(err) != core.ErrorInvalidRegistry {
		t.Fatalf("typed nil profile error=%v", err)
	}
}

func TestCoreEngineAdmitsOnlySupportedDeclaredCapabilitiesBeforeOpen(t *testing.T) {
	tests := []struct {
		name       string
		capability core.CapabilityID
		want       core.ErrorCode
	}{
		{name: "undeclared", capability: "cap-missing", want: core.ErrorCapabilityUndeclared},
		{name: "unknown", capability: "cap-unknown", want: core.ErrorCapabilityUnknown},
		{name: "unsupported", capability: "cap-unsupported", want: core.ErrorCapabilityUnsupported},
		{name: "not applicable", capability: "cap-na", want: core.ErrorCapabilityNotApplicable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executions := 0
			profile := validFakeProfile()
			profile.executions = &executions
			profile.descriptor.Capabilities = []core.Capability{
				{ID: "cap-supported", Support: core.SupportSupported},
				{ID: "cap-unknown", Support: core.SupportUnknown},
				{ID: "cap-unsupported", Support: core.SupportUnsupported},
				{ID: "cap-na", Support: core.SupportNotApplicable},
			}
			engine := mustEngine(t, profile)
			plan := validPlan()
			plan.Task.RequiredCapabilities = []core.CapabilityID{test.capability}
			if _, err := engine.Run(context.Background(), plan); errorCode(err) != test.want {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
			if profile.opens != 0 || executions != 0 {
				t.Fatalf("admission opened=%d executed=%d", profile.opens, executions)
			}
		})
	}
}

func TestCoreAdmitsNamespacedIdentifiersWithoutTreatingThemAsPaths(t *testing.T) {
	profile := validFakeProfile()
	profile.descriptor.ID = "counter/profile"
	profile.descriptor.Capabilities[0].ID = "count/value"
	engine := mustEngine(t, profile)
	plan := validPlan()
	plan.ID = "counter/plan"
	plan.Profile = profile.descriptor.ID
	plan.Task.ID = "count/task"
	plan.Task.RequiredCapabilities[0] = "count/value"
	plan.Task.Checks[0].ID = "check/b"
	plan.Task.Checks[1].ID = "check/a"
	plan.Task.Resources[0] = "resource/zero"
	plan.Task.Evidence[0] = "evidence/proof"
	plan.Fixture.ID = "counter/fixture"
	plan.Treatment.ID = "counter/treatment"
	plan.Treatment.Skills[0].ID = "skill/increment"
	if _, err := engine.Admit(plan); err != nil {
		t.Fatalf("admit namespace identities: %v", err)
	}
}

func TestEngineStatefulCounter(t *testing.T) {
	executions := 0
	profile := validFakeProfile()
	profile.executions = &executions
	engine := mustEngine(t, profile)
	plan := validPlan()
	plan.Attempts = 2

	first, err := engine.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if profile.opens != 2 || executions != 2 {
		t.Fatalf("opens=%d executions=%d", profile.opens, executions)
	}
	for _, attempt := range first.Attempts {
		counter := resource(t, attempt.Observation, "counter")
		if counter.Presence != core.PresenceObserved || counter.Value != 1 {
			t.Fatalf("attempt %d reused state: %+v", attempt.Identity.Ordinal, counter)
		}
		if attempt.Outcome != core.OutcomeFailed || attempt.Score != (core.Score{Presence: core.PresenceObserved, BasisPoints: 2500}) {
			t.Fatalf("attempt grade=%+v outcome=%d", attempt.Score, attempt.Outcome)
		}
		if got := []core.CheckID{attempt.Observation.Checks[0].ID, attempt.Observation.Checks[1].ID}; !reflect.DeepEqual(got, []core.CheckID{"check-a", "check-b"}) {
			t.Fatalf("check order=%v", got)
		}
	}
	assertResourceAggregate(t, first.Aggregate, "counter", core.PresenceCounts{Observed: 2}, 2)
	assertResourceAggregate(t, first.Aggregate, "zero", core.PresenceCounts{Observed: 2}, 0)
	assertResourceAggregate(t, first.Aggregate, "missing", core.PresenceCounts{Unknown: 2}, 0)
	assertResourceAggregate(t, first.Aggregate, "unavailable", core.PresenceCounts{Unsupported: 2}, 0)
	assertResourceAggregate(t, first.Aggregate, "irrelevant", core.PresenceCounts{NotApplicable: 2}, 0)
	if first.Aggregate.Outcomes.Failed != 2 || first.Aggregate.Scores.Presence.Observed != 2 ||
		first.Aggregate.Scores.TotalBasisPoints != 5000 || first.Aggregate.Scores.MeanBasisPoints != 2500 {
		t.Fatalf("aggregate=%+v", first.Aggregate)
	}

	second, err := engine.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic result\nfirst=%+v\nsecond=%+v", first, second)
	}
	pure, err := core.Assess(plan.Task, first.Attempts[0].Observation, first.Attempts[0].Grade)
	if err != nil || !reflect.DeepEqual(pure, first.Attempts[0].Assessment) {
		t.Fatalf("pure assessment=%+v err=%v", pure, err)
	}
	reversed := []core.Assessment{first.Attempts[1].Assessment, first.Attempts[0].Assessment}
	summary, err := core.Summarize(plan.Task, reversed)
	if err != nil || !reflect.DeepEqual(summary, first.Aggregate) {
		t.Fatalf("pure summary=%+v err=%v", summary, err)
	}
	many := make([]core.Assessment, 1025)
	for index := range many {
		many[index] = first.Attempts[0].Assessment
	}
	largeSummary, err := core.Summarize(plan.Task, many)
	if err != nil || largeSummary.Attempts != 1025 || largeSummary.Outcomes.Failed != 1025 {
		t.Fatalf("cross-plan summary=%+v err=%v", largeSummary, err)
	}
	reversed[0].Score.BasisPoints++
	if _, err := core.Summarize(plan.Task, reversed); errorCode(err) != core.ErrorAggregationFailed {
		t.Fatalf("tampered assessment error=%v", err)
	}
}

func TestCoreScoringBoundsStayWithinBasisPoints(t *testing.T) {
	task := core.Task{ID: "maximum-weight-task", Checks: make([]core.Check, 256)}
	observation := core.Observation{Checks: make([]core.CheckObservation, len(task.Checks))}
	grade := core.Grade{Checks: make([]core.CheckGrade, len(task.Checks))}
	for index := range task.Checks {
		id := core.CheckID(fmt.Sprintf("maximum-weight-%03d", index))
		task.Checks[index] = core.Check{ID: id, Weight: 1_000_000}
		observation.Checks[index] = core.CheckObservation{ID: id, Presence: core.PresenceObserved, Passed: true}
		grade.Checks[index] = core.CheckGrade{ID: id, Presence: core.PresenceObserved, Passed: true}
	}
	assessment, err := core.Assess(task, observation, grade)
	if err != nil || assessment.Score.BasisPoints != 10_000 {
		t.Fatalf("maximum-weight assessment=%+v err=%v", assessment, err)
	}
	aggregate, err := core.Summarize(task, []core.Assessment{assessment})
	if err != nil || aggregate.Scores.MeanBasisPoints != 10_000 {
		t.Fatalf("maximum-weight aggregate=%+v err=%v", aggregate, err)
	}

	tooMany := task
	tooMany.Checks = append(append([]core.Check(nil), task.Checks...), core.Check{ID: "one-too-many", Weight: 1})
	if _, err := core.Assess(tooMany, core.Observation{}, core.Grade{}); errorCode(err) != core.ErrorInvalidTask {
		t.Fatalf("too-many-checks error=%v", err)
	}
	overweight := task
	overweight.Checks = append([]core.Check(nil), task.Checks...)
	overweight.Checks[0].Weight = 1_000_001
	if _, err := core.Assess(overweight, observation, grade); errorCode(err) != core.ErrorInvalidTask {
		t.Fatalf("overweight-check error=%v", err)
	}
}

func TestCoreEngineClosesObservationsAndGrades(t *testing.T) {
	validObservation := validFakeProfile().observe(1)
	observationCases := []struct {
		name   string
		mutate func(*core.Observation)
	}{
		{name: "duplicate check", mutate: func(value *core.Observation) { value.Checks[1].ID = value.Checks[0].ID }},
		{name: "unknown check", mutate: func(value *core.Observation) { value.Checks[1].ID = "check-unknown" }},
		{name: "missing check", mutate: func(value *core.Observation) { value.Checks = value.Checks[:1] }},
		{name: "duplicate resource", mutate: func(value *core.Observation) { value.Resources[1].ID = value.Resources[0].ID }},
		{name: "unknown resource", mutate: func(value *core.Observation) { value.Resources[1].ID = "resource-unknown" }},
		{name: "duplicate evidence", mutate: func(value *core.Observation) { value.Evidence = append(value.Evidence, value.Evidence[0]) }},
		{name: "unknown evidence", mutate: func(value *core.Observation) { value.Evidence[0].ID = "evidence-unknown" }},
		{name: "unobserved value", mutate: func(value *core.Observation) {
			value.Resources[0].Presence = core.PresenceUnknown
			value.Resources[0].Value = 1
		}},
	}
	for _, test := range observationCases {
		t.Run("observation "+test.name, func(t *testing.T) {
			profile := validFakeProfile()
			profile.observe = func(uint64) core.Observation {
				value := cloneObservation(validObservation)
				test.mutate(&value)
				return value
			}
			engine := mustEngine(t, profile)
			if _, err := engine.Run(context.Background(), validPlan()); errorCode(err) != core.ErrorInvalidObservation {
				t.Fatalf("error=%v", err)
			}
		})
	}

	gradeCases := []struct {
		name   string
		mutate func(*core.Grade)
	}{
		{name: "duplicate", mutate: func(value *core.Grade) { value.Checks[1].ID = value.Checks[0].ID }},
		{name: "unknown", mutate: func(value *core.Grade) { value.Checks[1].ID = "check-unknown" }},
		{name: "coverage invention", mutate: func(value *core.Grade) {
			value.Checks[0].Presence = core.PresenceUnknown
		}},
	}
	for _, test := range gradeCases {
		t.Run("grade "+test.name, func(t *testing.T) {
			profile := validFakeProfile()
			originalGrade := profile.grade
			profile.grade = func(observation core.Observation) core.Grade {
				value := originalGrade(observation)
				test.mutate(&value)
				return value
			}
			engine := mustEngine(t, profile)
			if _, err := engine.Run(context.Background(), validPlan()); errorCode(err) != core.ErrorInvalidGrade {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func validFakeProfile() *fakeProfile {
	profile := &fakeProfile{
		descriptor: core.ProfileDescriptor{
			ID: "counter-profile",
			Capabilities: []core.Capability{
				{ID: "count", Support: core.SupportSupported},
			},
		},
	}
	profile.observe = func(counter uint64) core.Observation {
		return core.Observation{
			Checks: []core.CheckObservation{
				{ID: "check-b", Presence: core.PresenceObserved, Passed: false},
				{ID: "check-a", Presence: core.PresenceObserved, Passed: true},
			},
			Resources: []core.ResourceObservation{
				{ID: "zero", Presence: core.PresenceObserved, Value: 0},
				{ID: "irrelevant", Presence: core.PresenceNotApplicable},
				{ID: "counter", Presence: core.PresenceObserved, Value: counter},
				{ID: "unavailable", Presence: core.PresenceUnsupported},
				{ID: "missing", Presence: core.PresenceUnknown},
			},
			Evidence: []core.EvidenceObservation{
				{ID: "proof", Presence: core.PresenceObserved, Accepted: true},
			},
		}
	}
	profile.grade = func(observation core.Observation) core.Grade {
		return core.Grade{Checks: []core.CheckGrade{
			{ID: observation.Checks[1].ID, Presence: observation.Checks[1].Presence, Passed: observation.Checks[1].Passed},
			{ID: observation.Checks[0].ID, Presence: observation.Checks[0].Presence, Passed: observation.Checks[0].Passed},
		}}
	}
	return profile
}

func validPlan() core.Plan {
	return core.Plan{
		ID: "counter-plan", Profile: "counter-profile", Attempts: 1,
		Task: core.Task{
			ID:                   "count-task",
			RequiredCapabilities: []core.CapabilityID{"count"},
			Checks: []core.Check{
				{ID: "check-b", Weight: 3},
				{ID: "check-a", Weight: 1},
			},
			Resources: []core.ResourceID{"zero", "counter", "missing", "unavailable", "irrelevant"},
			Evidence:  []core.EvidenceID{"proof"},
		},
		Fixture: core.Fixture{ID: "counter-fixture"},
		Treatment: core.Treatment{
			ID:     "counter-treatment",
			Skills: []core.Skill{{ID: "increment"}},
		},
	}
}

func mustEngine(t *testing.T, profile core.Profile) *core.Engine {
	t.Helper()
	registry, err := core.NewRegistry(profile)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(registry)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func errorCode(err error) core.ErrorCode {
	code, ok := core.CodeOf(err)
	if !ok {
		return ""
	}
	return code
}

func resource(t *testing.T, observation core.Observation, id core.ResourceID) core.ResourceObservation {
	t.Helper()
	for _, item := range observation.Resources {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("resource %q missing", id)
	return core.ResourceObservation{}
}

func assertResourceAggregate(t *testing.T, aggregate core.Aggregate, id core.ResourceID, presence core.PresenceCounts, total uint64) {
	t.Helper()
	for _, item := range aggregate.Resources {
		if item.ID == id {
			if item.Presence != presence || item.Total != total {
				t.Fatalf("resource aggregate %q=%+v", id, item)
			}
			return
		}
	}
	t.Fatalf("resource aggregate %q missing", id)
}

func cloneObservation(input core.Observation) core.Observation {
	result := input
	result.Checks = append([]core.CheckObservation(nil), input.Checks...)
	result.Resources = append([]core.ResourceObservation(nil), input.Resources...)
	result.Evidence = append([]core.EvidenceObservation(nil), input.Evidence...)
	return result
}
