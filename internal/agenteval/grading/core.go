package grading

import (
	"context"
	"errors"
	"slices"

	"github.com/isukharev/atl/internal/agenteval/core"
)

// CoreGrader is the compatibility bridge from a validated terminal receipt to
// the neutral core grading port. It executes no grader code and reads no raw
// evidence during Grade.
type CoreGrader struct {
	task    core.Task
	receipt Receipt
}

func NewCoreGrader(task core.Task, plan Plan, receipt Receipt) (*CoreGrader, error) {
	if err := ValidateReceipt(plan, receipt); err != nil || len(task.Checks) != len(plan.Checks) {
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
	return &CoreGrader{task: ownedTask, receipt: cloneReceipt(receipt)}, nil
}

func (g *CoreGrader) Grade(ctx context.Context, input core.AttemptInput, observation core.Observation) (core.Grade, error) {
	if ctx == nil || g == nil {
		return core.Grade{}, newError(ErrorExecution, ErrExecution)
	}
	if err := contextError(ctx); err != nil {
		return core.Grade{}, err
	}
	task := input.Task()
	if task.ID != g.task.ID || !slices.Equal(task.Checks, g.task.Checks) || len(observation.Checks) != len(g.receipt.Decisions) {
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
