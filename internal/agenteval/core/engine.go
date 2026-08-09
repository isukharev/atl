package core

import (
	"context"
	"math"
	"reflect"
	"sort"
)

// Assessment is the pure canonical interpretation of one observation and grade.
type Assessment struct {
	Observation Observation
	Grade       Grade
	Outcome     Outcome
	Score       Score
}

// AttemptResult binds one pure assessment to its engine attempt identity.
type AttemptResult struct {
	Identity AttemptIdentity
	Assessment
}

// PresenceCounts preserves coverage independently from numeric totals.
type PresenceCounts struct {
	Unknown       uint32
	Observed      uint32
	Unsupported   uint32
	NotApplicable uint32
}

// OutcomeCounts is the deterministic terminal-outcome projection.
type OutcomeCounts struct {
	Unknown       uint32
	Succeeded     uint32
	Failed        uint32
	NotApplicable uint32
}

// ResourceAggregate is one sorted resource projection across attempts.
type ResourceAggregate struct {
	ID       ResourceID
	Presence PresenceCounts
	Total    uint64
}

// ScoreAggregate retains integer-only score statistics.
type ScoreAggregate struct {
	Presence         PresenceCounts
	TotalBasisPoints uint64
	MeanBasisPoints  uint16
}

// Aggregate is the deterministic projection of all attempt results.
type Aggregate struct {
	Attempts  uint32
	Outcomes  OutcomeCounts
	Scores    ScoreAggregate
	Resources []ResourceAggregate
}

// RunResult contains attempts in ordinal order and one canonical aggregate.
type RunResult struct {
	Plan      PlanID
	Profile   ProfileID
	Attempts  []AttemptResult
	Aggregate Aggregate
}

// Engine validates and executes plans sequentially against one immutable registry.
type Engine struct {
	registry *Registry
}

// NewEngine constructs an engine from an explicit immutable registry.
func NewEngine(registry *Registry) (*Engine, error) {
	if registry == nil || len(registry.profiles) == 0 {
		return nil, newError(ErrorInvalidRegistry, nil)
	}
	return &Engine{registry: registry}, nil
}

// Admit validates and canonicalizes a plan without opening its profile.
func (e *Engine) Admit(plan Plan) (AdmittedPlan, error) {
	admitted, _, err := e.admit(plan)
	return admitted, err
}

func (e *Engine) admit(plan Plan) (AdmittedPlan, registeredProfile, error) {
	if e == nil || e.registry == nil {
		return AdmittedPlan{}, registeredProfile{}, newError(ErrorInvalidRegistry, nil)
	}
	canonical, err := validatePlan(plan)
	if err != nil {
		return AdmittedPlan{}, registeredProfile{}, err
	}
	profile, ok := e.registry.resolve(canonical.Profile)
	if !ok {
		return AdmittedPlan{}, registeredProfile{}, newError(ErrorProfileNotFound, nil)
	}
	for _, required := range canonical.Task.RequiredCapabilities {
		support, declared := profile.capabilities[required]
		if !declared {
			return AdmittedPlan{}, registeredProfile{}, newError(ErrorCapabilityUndeclared, nil)
		}
		switch support {
		case SupportSupported:
		case SupportUnknown:
			return AdmittedPlan{}, registeredProfile{}, newError(ErrorCapabilityUnknown, nil)
		case SupportUnsupported:
			return AdmittedPlan{}, registeredProfile{}, newError(ErrorCapabilityUnsupported, nil)
		case SupportNotApplicable:
			return AdmittedPlan{}, registeredProfile{}, newError(ErrorCapabilityNotApplicable, nil)
		default:
			return AdmittedPlan{}, registeredProfile{}, newError(ErrorInvalidRegistry, nil)
		}
	}
	return AdmittedPlan{plan: canonical, descriptor: cloneDescriptor(profile.descriptor)}, profile, nil
}

// Run admits and executes every attempt exactly once in ordinal order.
func (e *Engine) Run(ctx context.Context, plan Plan) (RunResult, error) {
	admitted, profile, err := e.admit(plan)
	if err != nil {
		return RunResult{}, err
	}
	canonical := admitted.plan
	result := RunResult{
		Plan:     canonical.ID,
		Profile:  canonical.Profile,
		Attempts: make([]AttemptResult, 0, canonical.Attempts),
	}
	for ordinal := uint32(1); ordinal <= canonical.Attempts; ordinal++ {
		if err := contextError(ctx); err != nil {
			return RunResult{}, err
		}
		identity := AttemptIdentity{
			Plan: canonical.ID, Task: canonical.Task.ID,
			Treatment: canonical.Treatment.ID, Ordinal: ordinal,
		}
		runtime, err := profile.implementation.Open(ctx, admitted, identity)
		if err != nil {
			return RunResult{}, newError(ErrorProfileOpenFailed, err)
		}
		if nilInterface(runtime.Adapter) || nilInterface(runtime.Backend) || nilInterface(runtime.Grader) {
			return RunResult{}, newError(ErrorProfileOpenFailed, nil)
		}
		input := AttemptInput{
			identity: identity, task: cloneTask(canonical.Task), fixture: canonical.Fixture,
			treatment: cloneTreatment(canonical.Treatment),
		}
		observation, err := runtime.Backend.Run(ctx, input, runtime.Adapter)
		if err != nil {
			return RunResult{}, stageError(ctx, ErrorExecutionFailed, err)
		}
		observation, err = normalizeObservation(canonical.Task, observation)
		if err != nil {
			return RunResult{}, err
		}
		grade, err := runtime.Grader.Grade(ctx, input, cloneObservation(observation))
		if err != nil {
			return RunResult{}, stageError(ctx, ErrorGradingFailed, err)
		}
		assessment, err := Assess(canonical.Task, observation, grade)
		if err != nil {
			return RunResult{}, err
		}
		result.Attempts = append(result.Attempts, AttemptResult{
			Identity: identity, Assessment: assessment,
		})
	}
	assessments := make([]Assessment, len(result.Attempts))
	for index := range result.Attempts {
		assessments[index] = result.Attempts[index].Assessment
	}
	result.Aggregate, err = Summarize(canonical.Task, assessments)
	if err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func validatePlan(input Plan) (Plan, error) {
	if !validIdentifier(string(input.ID)) || !validIdentifier(string(input.Profile)) ||
		!validIdentifier(string(input.Fixture.ID)) || !validIdentifier(string(input.Treatment.ID)) ||
		input.Attempts == 0 || input.Attempts > maxAttempts ||
		len(input.Treatment.Skills) > maxCollectionEntries {
		return Plan{}, newError(ErrorInvalidPlan, nil)
	}
	plan := clonePlan(input)
	task, err := validateTask(plan.Task, ErrorInvalidPlan)
	if err != nil || !validSkills(plan.Treatment.Skills) {
		return Plan{}, newError(ErrorInvalidPlan, nil)
	}
	plan.Task = task
	sort.Slice(plan.Treatment.Skills, func(i, j int) bool { return plan.Treatment.Skills[i].ID < plan.Treatment.Skills[j].ID })
	return plan, nil
}

func validateTask(input Task, code ErrorCode) (Task, error) {
	if !validIdentifier(string(input.ID)) || len(input.RequiredCapabilities) > maxCollectionEntries ||
		len(input.Checks) == 0 || len(input.Checks) > maxCollectionEntries ||
		len(input.Resources) > maxCollectionEntries || len(input.Evidence) > maxCollectionEntries {
		return Task{}, newError(code, nil)
	}
	task := cloneTask(input)
	if !uniqueCapabilityIDs(task.RequiredCapabilities) || !validChecks(task.Checks) ||
		!uniqueResourceIDs(task.Resources) || !uniqueEvidenceIDs(task.Evidence) {
		return Task{}, newError(code, nil)
	}
	sort.Slice(task.RequiredCapabilities, func(i, j int) bool {
		return task.RequiredCapabilities[i] < task.RequiredCapabilities[j]
	})
	sort.Slice(task.Checks, func(i, j int) bool { return task.Checks[i].ID < task.Checks[j].ID })
	sort.Slice(task.Resources, func(i, j int) bool { return task.Resources[i] < task.Resources[j] })
	sort.Slice(task.Evidence, func(i, j int) bool { return task.Evidence[i] < task.Evidence[j] })
	return task, nil
}

func uniqueCapabilityIDs(values []CapabilityID) bool {
	seen := make(map[CapabilityID]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(string(value)) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validChecks(values []Check) bool {
	seen := make(map[CheckID]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(string(value.ID)) || value.Weight == 0 || value.Weight > maxCheckWeight {
			return false
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return false
		}
		seen[value.ID] = struct{}{}
	}
	return true
}

func uniqueResourceIDs(values []ResourceID) bool {
	seen := make(map[ResourceID]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(string(value)) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueEvidenceIDs(values []EvidenceID) bool {
	seen := make(map[EvidenceID]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(string(value)) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validSkills(values []Skill) bool {
	seen := make(map[SkillID]struct{}, len(values))
	for _, value := range values {
		if !validIdentifier(string(value.ID)) {
			return false
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return false
		}
		seen[value.ID] = struct{}{}
	}
	return true
}

func normalizeObservation(task Task, input Observation) (Observation, error) {
	if len(input.Checks) != len(task.Checks) || len(input.Resources) != len(task.Resources) || len(input.Evidence) != len(task.Evidence) {
		return Observation{}, newError(ErrorInvalidObservation, nil)
	}
	checks := make(map[CheckID]struct{}, len(task.Checks))
	for _, check := range task.Checks {
		checks[check.ID] = struct{}{}
	}
	resources := make(map[ResourceID]struct{}, len(task.Resources))
	for _, resource := range task.Resources {
		resources[resource] = struct{}{}
	}
	evidence := make(map[EvidenceID]struct{}, len(task.Evidence))
	for _, item := range task.Evidence {
		evidence[item] = struct{}{}
	}

	result := cloneObservation(input)
	seenChecks := make(map[CheckID]struct{}, len(input.Checks))
	for _, observation := range result.Checks {
		if _, declared := checks[observation.ID]; !declared || !observation.Presence.valid() ||
			observation.Presence != PresenceObserved && observation.Passed {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		if _, duplicate := seenChecks[observation.ID]; duplicate {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		seenChecks[observation.ID] = struct{}{}
	}
	seenResources := make(map[ResourceID]struct{}, len(input.Resources))
	for _, observation := range result.Resources {
		if _, declared := resources[observation.ID]; !declared || !observation.Presence.valid() ||
			observation.Presence != PresenceObserved && observation.Value != 0 {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		if _, duplicate := seenResources[observation.ID]; duplicate {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		seenResources[observation.ID] = struct{}{}
	}
	seenEvidence := make(map[EvidenceID]struct{}, len(input.Evidence))
	for _, observation := range result.Evidence {
		if _, declared := evidence[observation.ID]; !declared || !observation.Presence.valid() ||
			observation.Presence != PresenceObserved && observation.Accepted {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		if _, duplicate := seenEvidence[observation.ID]; duplicate {
			return Observation{}, newError(ErrorInvalidObservation, nil)
		}
		seenEvidence[observation.ID] = struct{}{}
	}
	sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].ID < result.Checks[j].ID })
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].ID < result.Evidence[j].ID })
	return result, nil
}

func normalizeGrade(task Task, observation Observation, input Grade) (Grade, error) {
	if len(input.Checks) != len(task.Checks) {
		return Grade{}, newError(ErrorInvalidGrade, nil)
	}
	observed := make(map[CheckID]CheckObservation, len(observation.Checks))
	for _, check := range observation.Checks {
		observed[check.ID] = check
	}
	result := cloneGrade(input)
	seen := make(map[CheckID]struct{}, len(input.Checks))
	for _, grade := range result.Checks {
		check, declared := observed[grade.ID]
		if !declared || !grade.Presence.valid() || grade.Presence != check.Presence ||
			grade.Presence != PresenceObserved && grade.Passed {
			return Grade{}, newError(ErrorInvalidGrade, nil)
		}
		if _, duplicate := seen[grade.ID]; duplicate {
			return Grade{}, newError(ErrorInvalidGrade, nil)
		}
		seen[grade.ID] = struct{}{}
	}
	sort.Slice(result.Checks, func(i, j int) bool { return result.Checks[i].ID < result.Checks[j].ID })
	return result, nil
}

// Assess validates and canonicalizes one observation and grade, then derives
// its outcome and integer score without opening a profile or executing work.
func Assess(task Task, observation Observation, grade Grade) (Assessment, error) {
	canonical, err := validateTask(task, ErrorInvalidTask)
	if err != nil {
		return Assessment{}, err
	}
	observation, err = normalizeObservation(canonical, observation)
	if err != nil {
		return Assessment{}, err
	}
	grade, err = normalizeGrade(canonical, observation, grade)
	if err != nil {
		return Assessment{}, err
	}
	outcome, score := deriveGrade(canonical, grade)
	return Assessment{
		Observation: observation,
		Grade:       grade,
		Outcome:     outcome,
		Score:       score,
	}, nil
}

func deriveGrade(task Task, grade Grade) (Outcome, Score) {
	weights := make(map[CheckID]uint64, len(task.Checks))
	for _, check := range task.Checks {
		weights[check.ID] = uint64(check.Weight)
	}
	failed, incomplete, anyObserved := false, false, false
	var observedWeight, passedWeight uint64
	for _, check := range grade.Checks {
		switch check.Presence {
		case PresenceObserved:
			anyObserved = true
			observedWeight += weights[check.ID]
			if check.Passed {
				passedWeight += weights[check.ID]
			} else {
				failed = true
			}
		case PresenceUnknown, PresenceUnsupported:
			incomplete = true
		case PresenceNotApplicable:
		}
	}
	outcome := OutcomeNotApplicable
	switch {
	case failed:
		outcome = OutcomeFailed
	case incomplete:
		outcome = OutcomeUnknown
	case anyObserved:
		outcome = OutcomeSucceeded
	}
	if incomplete {
		return outcome, Score{Presence: PresenceUnknown}
	}
	if observedWeight == 0 {
		return outcome, Score{Presence: PresenceNotApplicable}
	}
	return outcome, Score{
		Presence:    PresenceObserved,
		BasisPoints: uint16(passedWeight * 10_000 / observedWeight),
	}
}

// Summarize validates assessments again and deterministically aggregates them.
// It uses integer arithmetic and emits resources in canonical identity order.
func Summarize(task Task, input []Assessment) (Aggregate, error) {
	if len(input) == 0 || uint64(len(input)) > math.MaxUint32 {
		return Aggregate{}, newError(ErrorAggregationFailed, nil)
	}
	canonical, err := validateTask(task, ErrorInvalidTask)
	if err != nil {
		return Aggregate{}, err
	}
	assessments := make([]Assessment, 0, len(input))
	for _, assessment := range input {
		validated, err := Assess(canonical, assessment.Observation, assessment.Grade)
		if err != nil {
			return Aggregate{}, err
		}
		if assessment.Outcome != validated.Outcome || assessment.Score != validated.Score {
			return Aggregate{}, newError(ErrorAggregationFailed, nil)
		}
		assessments = append(assessments, validated)
	}
	aggregate := Aggregate{
		Attempts:  uint32(len(assessments)),
		Resources: make([]ResourceAggregate, len(canonical.Resources)),
	}
	resourceIndex := make(map[ResourceID]int, len(canonical.Resources))
	for index, id := range canonical.Resources {
		aggregate.Resources[index].ID = id
		resourceIndex[id] = index
	}
	for _, assessment := range assessments {
		switch assessment.Outcome {
		case OutcomeUnknown:
			aggregate.Outcomes.Unknown++
		case OutcomeSucceeded:
			aggregate.Outcomes.Succeeded++
		case OutcomeFailed:
			aggregate.Outcomes.Failed++
		case OutcomeNotApplicable:
			aggregate.Outcomes.NotApplicable++
		default:
			return Aggregate{}, newError(ErrorAggregationFailed, nil)
		}
		incrementPresence(&aggregate.Scores.Presence, assessment.Score.Presence)
		if assessment.Score.Presence == PresenceObserved {
			if math.MaxUint64-aggregate.Scores.TotalBasisPoints < uint64(assessment.Score.BasisPoints) {
				return Aggregate{}, newError(ErrorAggregationFailed, nil)
			}
			aggregate.Scores.TotalBasisPoints += uint64(assessment.Score.BasisPoints)
		}
		for _, observation := range assessment.Observation.Resources {
			index, exists := resourceIndex[observation.ID]
			if !exists {
				return Aggregate{}, newError(ErrorAggregationFailed, nil)
			}
			item := &aggregate.Resources[index]
			incrementPresence(&item.Presence, observation.Presence)
			if observation.Presence == PresenceObserved {
				if math.MaxUint64-item.Total < observation.Value {
					return Aggregate{}, newError(ErrorAggregationFailed, nil)
				}
				item.Total += observation.Value
			}
		}
	}
	if aggregate.Scores.Presence.Observed > 0 {
		aggregate.Scores.MeanBasisPoints = uint16(
			aggregate.Scores.TotalBasisPoints / uint64(aggregate.Scores.Presence.Observed),
		)
	}
	return aggregate, nil
}

func incrementPresence(counts *PresenceCounts, presence Presence) {
	switch presence {
	case PresenceUnknown:
		counts.Unknown++
	case PresenceObserved:
		counts.Observed++
	case PresenceUnsupported:
		counts.Unsupported++
	case PresenceNotApplicable:
		counts.NotApplicable++
	}
}

func cloneObservation(input Observation) Observation {
	result := input
	result.Checks = append([]CheckObservation(nil), input.Checks...)
	result.Resources = append([]ResourceObservation(nil), input.Resources...)
	result.Evidence = append([]EvidenceObservation(nil), input.Evidence...)
	return result
}

func cloneGrade(input Grade) Grade {
	result := input
	result.Checks = append([]CheckGrade(nil), input.Checks...)
	return result
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return newError(ErrorInterrupted, err)
	}
	return nil
}

func stageError(ctx context.Context, fallback ErrorCode, cause error) error {
	if err := ctx.Err(); err != nil {
		return newError(ErrorInterrupted, err)
	}
	return newError(fallback, cause)
}
