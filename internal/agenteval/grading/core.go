package grading

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/isukharev/atl/internal/agenteval/core"
)

// CoreGrader is the compatibility bridge from a validated terminal receipt to
// the neutral core grading port. It executes no grader code and reads no raw
// evidence during Grade.
type CoreGrader struct {
	identity  core.AttemptIdentity
	task      core.Task
	fixture   core.Fixture
	treatment core.Treatment
	receipt   Receipt
}

func NewCoreGrader(identity core.AttemptIdentity, task core.Task, fixture core.Fixture, treatment core.Treatment,
	plan Plan, receipt Receipt,
) (*CoreGrader, error) {
	inputSHA, inputErr := CoreAttemptInputSHA256(identity, task, fixture, treatment)
	if err := ValidateReceipt(plan, receipt); err != nil || inputErr != nil || plan.InputProjectionSHA256 != inputSHA ||
		len(task.Checks) != len(plan.Checks) {
		return nil, contractError("core_grader")
	}
	ownedTask := task
	ownedTask.RequiredCapabilities = slices.Clone(task.RequiredCapabilities)
	ownedTask.Checks = slices.Clone(task.Checks)
	ownedTask.Resources = slices.Clone(task.Resources)
	ownedTask.Evidence = slices.Clone(task.Evidence)
	for index, check := range plan.Checks {
		if string(task.Checks[index].ID) != check.ID {
			return nil, contractError("core_check_binding")
		}
	}
	ownedTreatment := treatment
	ownedTreatment.Skills = slices.Clone(treatment.Skills)
	return &CoreGrader{identity: identity, task: ownedTask, fixture: fixture, treatment: ownedTreatment,
		receipt: cloneReceipt(receipt)}, nil
}

func (g *CoreGrader) Grade(ctx context.Context, input core.AttemptInput, observation core.Observation) (core.Grade, error) {
	if ctx == nil || g == nil {
		return core.Grade{}, newError(ErrorExecution, ErrExecution)
	}
	if err := contextError(ctx); err != nil {
		return core.Grade{}, err
	}
	task := input.Task()
	if input.Identity() != g.identity || !equalCoreTask(task, g.task) || input.Fixture() != g.fixture ||
		!equalCoreTreatment(input.Treatment(), g.treatment) || len(observation.Checks) != len(g.receipt.Decisions) {
		return core.Grade{}, newError(ErrorExecution, ErrExecution)
	}
	grade := core.Grade{Checks: make([]core.CheckGrade, len(g.receipt.Decisions))}
	for index, decision := range g.receipt.Decisions {
		observed := observation.Checks[index]
		presence, err := corePresence(decision.Presence)
		if err != nil || string(observed.ID) != decision.CheckID || observed.Presence != presence {
			return core.Grade{}, newError(ErrorExecution, ErrExecution)
		}
		grade.Checks[index] = core.CheckGrade{ID: observed.ID, Presence: presence, Passed: decision.Passed}
	}
	return grade, nil
}

// CoreAttemptInputSHA256 binds a receipt-backed grader to one exact core
// attempt, including the fixture omitted from core.AttemptIdentity.
func CoreAttemptInputSHA256(identity core.AttemptIdentity, task core.Task, fixture core.Fixture, treatment core.Treatment) (string, error) {
	if identity.Plan == "" || identity.Task != task.ID || identity.Treatment != treatment.ID || identity.Ordinal == 0 || fixture.ID == "" {
		return "", contractError("core_attempt")
	}
	type checkProjection struct {
		ID     string `json:"id"`
		Weight uint32 `json:"weight"`
	}
	type taskProjection struct {
		ID                   string              `json:"id"`
		RequiredCapabilities []core.CapabilityID `json:"required_capabilities"`
		Checks               []checkProjection   `json:"checks"`
		Resources            []core.ResourceID   `json:"resources"`
		Evidence             []core.EvidenceID   `json:"evidence"`
	}
	checks := make([]checkProjection, len(task.Checks))
	for index, check := range task.Checks {
		checks[index] = checkProjection{ID: string(check.ID), Weight: check.Weight}
	}
	skills := make([]string, len(treatment.Skills))
	for index, skill := range treatment.Skills {
		skills[index] = string(skill.ID)
	}
	projection := struct {
		Plan      string         `json:"plan"`
		Task      taskProjection `json:"task"`
		Fixture   string         `json:"fixture"`
		Treatment string         `json:"treatment"`
		Skills    []string       `json:"skills"`
		Ordinal   uint32         `json:"ordinal"`
	}{string(identity.Plan), taskProjection{string(task.ID), slices.Clone(task.RequiredCapabilities), checks,
		slices.Clone(task.Resources), slices.Clone(task.Evidence)}, string(fixture.ID), string(treatment.ID), skills, identity.Ordinal}
	data, err := json.Marshal(projection)
	if err != nil {
		return "", contractError("core_attempt")
	}
	return hashDomain("core-attempt-input", data), nil
}

func equalCoreTask(left, right core.Task) bool {
	return left.ID == right.ID && slices.Equal(left.RequiredCapabilities, right.RequiredCapabilities) &&
		slices.Equal(left.Checks, right.Checks) && slices.Equal(left.Resources, right.Resources) && slices.Equal(left.Evidence, right.Evidence)
}

func equalCoreTreatment(left, right core.Treatment) bool {
	return left.ID == right.ID && slices.Equal(left.Skills, right.Skills)
}

func (g *CoreGrader) Receipt() Receipt {
	if g == nil {
		return Receipt{}
	}
	return cloneReceipt(g.receipt)
}

func corePresence(presence Presence) (core.Presence, error) {
	switch presence {
	case PresenceUnknown:
		return core.PresenceUnknown, nil
	case PresenceObserved:
		return core.PresenceObserved, nil
	case PresenceUnsupported:
		return core.PresenceUnsupported, nil
	case PresenceNotApplicable:
		return core.PresenceNotApplicable, nil
	default:
		return core.PresenceUnknown, errors.New("invalid presence")
	}
}
