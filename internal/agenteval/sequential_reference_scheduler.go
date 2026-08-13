package agenteval

import (
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

func validateSequentialReferenceRunOptions(options SequentialReferenceRunOptions) error {
	if options.Workers == 0 || options.Workers > scheduler.MaxWorkers {
		return sequentialReferenceError("scheduler_options", nil)
	}
	return nil
}

func sequentialReferenceMarker(manifestSHA256 string, workers uint32) []byte {
	return []byte(fmt.Sprintf("manifest_sha256=%s\nworkers=%d\n", manifestSHA256, workers))
}

func (prepared *sequentialReferencePrepared) schedulerPlan(options SequentialReferenceRunOptions, plans []lifecycle.Plan,
	assignments []sequentialReferenceAssignment,
) (scheduler.Plan, error) {
	executionPlans := make([]executionbackend.Plan, len(assignments))
	for index, assignment := range assignments {
		treatment := prepared.treatments[assignment.TreatmentID]
		if treatment == nil {
			return scheduler.Plan{}, sequentialReferenceError("scheduler_treatment", nil)
		}
		executionPlans[index] = treatment.plan
	}
	return sequentialReferenceSchedulerPlan(prepared.manifest, options, plans, assignments, executionPlans)
}

func sequentialReferenceSchedulerPlan(manifest experiment.Manifest, options SequentialReferenceRunOptions, plans []lifecycle.Plan,
	assignments []sequentialReferenceAssignment, executionPlans []executionbackend.Plan,
) (scheduler.Plan, error) {
	if options.Workers == 0 || options.Workers > scheduler.MaxWorkers || len(plans) == 0 || len(plans) != len(assignments) {
		return scheduler.Plan{}, sequentialReferenceError("scheduler_options", nil)
	}
	if len(executionPlans) != len(assignments) {
		return scheduler.Plan{}, sequentialReferenceError("scheduler_execution_plans", nil)
	}
	runtimeBinding := manifest.CapabilityContract.Runtime
	providerIdentity, err := contentMinimizedAttemptDigest("sequential-reference-provider", "not_applicable")
	if err != nil {
		return scheduler.Plan{}, sequentialReferenceError("scheduler_provider", err)
	}
	cohorts := make([]string, 0, 3)
	for _, input := range []struct {
		Kind     string `json:"kind"`
		Identity string `json:"identity"`
	}{
		{Kind: "execution", Identity: runtimeBinding.ExecutionBackendSHA256},
		{Kind: "model", Identity: runtimeBinding.ModelSHA256},
		{Kind: "provider", Identity: providerIdentity},
	} {
		cohort, digestErr := contentMinimizedAttemptDigest("scheduler-cohort", input)
		if digestErr != nil {
			return scheduler.Plan{}, sequentialReferenceError("scheduler_cohort", digestErr)
		}
		cohorts = append(cohorts, cohort)
	}
	sort.Strings(cohorts)
	limits := scheduler.Limits{Workers: options.Workers, Cohorts: make([]scheduler.CohortLimit, len(cohorts))}
	for index, cohort := range cohorts {
		limits.Cohorts[index] = scheduler.CohortLimit{CohortSHA256: cohort, Workers: options.Workers}
	}
	tasks := make([]scheduler.Task, 0, len(plans))
	var maximum scheduler.Resources
	appendTask := func(index int, round uint32) error {
		executionPlan := executionPlans[index]
		if executionPlan.Resources.MaxInputBytes > ^uint64(0)-executionPlan.Resources.MaxOutputBytes {
			return sequentialReferenceError("scheduler_resources", nil)
		}
		resources := scheduler.Resources{
			CPUTimeMillis: executionPlan.Resources.CPUTimeMillis,
			MemoryBytes:   executionPlan.Resources.MemoryBytes,
			StorageBytes:  executionPlan.Resources.MaxInputBytes + executionPlan.Resources.MaxOutputBytes,
			Processes:     executionPlan.Resources.ProcessLimit,
		}
		maximum.CPUTimeMillis = max(maximum.CPUTimeMillis, resources.CPUTimeMillis)
		maximum.MemoryBytes = max(maximum.MemoryBytes, resources.MemoryBytes)
		maximum.StorageBytes = max(maximum.StorageBytes, resources.StorageBytes)
		maximum.Processes = max(maximum.Processes, resources.Processes)
		tasks = append(tasks, scheduler.Task{TaskSHA256: plans[index].PlanSHA256, Ordinal: uint32(index + 1), // #nosec G115 -- roster is bounded.
			Round: round, Resources: resources, CohortSHA256s: append([]string{}, cohorts...)})
		return nil
	}
	if options.Workers == 1 {
		for index := range assignments {
			if err := appendTask(index, uint32(index+1)); err != nil { // #nosec G115 -- roster is bounded.
				return scheduler.Plan{}, err
			}
		}
	} else {
		for round := uint32(1); len(tasks) < len(plans); round++ {
			found := false
			for index, assignment := range assignments {
				if assignment.Round != round {
					continue
				}
				found = true
				if err := appendTask(index, round); err != nil {
					return scheduler.Plan{}, err
				}
			}
			if !found {
				return scheduler.Plan{}, sequentialReferenceError("scheduler_rounds", nil)
			}
		}
	}
	limits.CPUTimeMillis, err = sequentialReferenceSchedulerCapacity(maximum.CPUTimeMillis, options.Workers, scheduler.MaxCPUTimeMillis)
	if err == nil {
		limits.MemoryBytes, err = sequentialReferenceSchedulerCapacity(maximum.MemoryBytes, options.Workers, scheduler.MaxMemoryBytes)
	}
	if err == nil {
		limits.StorageBytes, err = sequentialReferenceSchedulerCapacity(maximum.StorageBytes, options.Workers, scheduler.MaxStorageBytes)
	}
	if err == nil {
		var processes uint64
		processes, err = sequentialReferenceSchedulerCapacity(uint64(maximum.Processes), options.Workers, uint64(scheduler.MaxProcesses))
		limits.Processes = uint32(processes) // #nosec G115 -- capacity is bounded by MaxProcesses.
	}
	if err != nil {
		return scheduler.Plan{}, sequentialReferenceError("scheduler_capacity", err)
	}
	plan, err := scheduler.SealPlan(scheduler.Plan{Limits: limits, Tasks: tasks})
	if err != nil {
		return scheduler.Plan{}, sequentialReferenceError("scheduler_plan", err)
	}
	return plan, nil
}

func sequentialReferenceSchedulerCapacity(value uint64, workers uint32, maximum uint64) (uint64, error) {
	if value == 0 {
		return 0, nil
	}
	if uint64(workers) > maximum/value {
		return 0, scheduler.ErrContract
	}
	return value * uint64(workers), nil
}

func sequentialReferenceSchedulerOutcome(artifacts SequentialReferenceTrialArtifacts) scheduler.Outcome {
	switch artifacts.TrialRecord.LifecycleState {
	case experiment.LifecycleSucceeded:
		return scheduler.OutcomeSucceeded
	case experiment.LifecycleFailed:
		return scheduler.OutcomeFailed
	case experiment.LifecycleCanceled, experiment.LifecycleTimedOut:
		return scheduler.OutcomeCanceled
	default:
		return scheduler.OutcomeUnknown
	}
}

func sequentialReferenceStartedAttemptsAreSchedulePrefix(roster sequentialReferenceScheduledRoster,
	inspections []AttemptLedgerInspection,
) bool {
	if len(roster.plans) != len(inspections) || len(roster.schedule.Tasks) != len(inspections) {
		return false
	}
	seenPlanned := false
	for _, task := range roster.schedule.Tasks {
		if task.Ordinal == 0 || int(task.Ordinal) > len(inspections) {
			return false
		}
		inspection := inspections[task.Ordinal-1]
		if inspection.Plan.PlanSHA256 != task.TaskSHA256 || inspection.Plan.PlanSHA256 != roster.plans[task.Ordinal-1].PlanSHA256 {
			return false
		}
		if inspection.Projection.State == lifecycle.StatePlanned {
			seenPlanned = true
		} else if seenPlanned {
			return false
		}
	}
	return true
}
