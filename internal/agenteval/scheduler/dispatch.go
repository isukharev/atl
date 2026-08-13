package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

type taskResult struct {
	task    Task
	outcome Outcome
	err     error
}

type batchReservation struct {
	resources Resources
	cohorts   map[string]uint32
}

// Run executes an admitted plan in deterministic round/batch order. The
// caller-owned StartFunc crosses each durable start boundary serially; the
// returned closures are the only concurrently invoked work.
func Run(ctx context.Context, plan Plan, start StartFunc) (Report, error) {
	return RunRemaining(ctx, plan, nil, start)
}

// RunRemaining preserves durable terminal tasks and dispatches only their
// planned complement. The terminal slice must follow static plan order.
func RunRemaining(ctx context.Context, plan Plan, terminal []TerminalTask, start StartFunc) (Report, error) {
	if ctx == nil || start == nil || ValidatePlan(plan) != nil {
		return Report{}, contractError("run")
	}
	plan = clonePlan(plan)
	terminalByTask, report, reservedCost, err := admitTerminalTasks(plan, terminal)
	if err != nil {
		return Report{}, err
	}
	runContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var runErr error
	pending := make([]Task, 0, len(plan.Tasks)-len(terminal))
	for _, task := range plan.Tasks {
		if _, done := terminalByTask[task.TaskSHA256]; !done {
			pending = append(pending, task)
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		report.Stop, runErr = StopCanceled, fmt.Errorf("%w: %w", ErrInterrupted, cause)
	}
	for index := 0; index < len(pending) && runErr == nil; {
		if err := context.Cause(ctx); err != nil {
			report.Stop, runErr = StopCanceled, fmt.Errorf("%w: %w", ErrInterrupted, err)
			break
		}
		round := pending[index].Round
		roundEnd := index
		for roundEnd < len(pending) && pending[roundEnd].Round == round {
			roundEnd++
		}
		for index < roundEnd && runErr == nil {
			batch, next, batchCost, costBlocked := nextBatch(plan.Limits, pending, index, roundEnd, reservedCost)
			if costBlocked && len(batch) == 0 {
				report.Stop, runErr = StopCostExhausted, ErrCostExhausted
				break
			}
			if len(batch) == 0 {
				report.Stop, runErr = StopStartFailed, contractError("empty_batch")
				break
			}
			reservedCost += batchCost
			batchErr := runBatch(runContext, cancel, ctx, batch, start, &report)
			if batchErr != nil {
				runErr = batchErr
				break
			}
			index = next
			if costBlocked {
				report.Stop, runErr = StopCostExhausted, ErrCostExhausted
			}
		}
	}
	if runErr == nil && report.Completed != uint32(len(plan.Tasks)) { // #nosec G115 -- validated plans cap tasks at MaxTasks.
		report.Stop, runErr = StopTaskFailed, ErrExecution
	}
	sealed, sealErr := SealReport(plan, report)
	if sealErr != nil {
		return Report{}, errors.Join(runErr, sealErr)
	}
	return sealed, runErr
}

func admitTerminalTasks(plan Plan, terminal []TerminalTask) (map[string]Outcome, Report, uint64, error) {
	result := make(map[string]Outcome, len(terminal))
	report := Report{Stop: StopNone}
	reservedCost := uint64(0)
	terminalIndex := 0
	for _, task := range plan.Tasks {
		if terminalIndex >= len(terminal) || terminal[terminalIndex].TaskSHA256 != task.TaskSHA256 {
			continue
		}
		item := terminal[terminalIndex]
		if !item.Outcome.valid() || task.Resources.CostMicroUSD > plan.Limits.TotalCostMicroUSD-reservedCost {
			return nil, Report{}, 0, contractError("terminal_tasks")
		}
		result[item.TaskSHA256] = item.Outcome
		reservedCost += task.Resources.CostMicroUSD
		report.Started++
		report.Completed++
		countOutcome(&report, item.Outcome)
		terminalIndex++
	}
	if terminalIndex != len(terminal) {
		return nil, Report{}, 0, contractError("terminal_order")
	}
	return result, report, reservedCost, nil
}

func nextBatch(limits Limits, tasks []Task, start, end int, reservedCost uint64) ([]Task, int, uint64, bool) {
	reservation := batchReservation{cohorts: map[string]uint32{}}
	batch := make([]Task, 0, limits.Workers)
	batchCost := uint64(0)
	index := start
	for index < end && len(batch) < int(limits.Workers) {
		task := tasks[index]
		if task.Resources.CostMicroUSD > limits.TotalCostMicroUSD-reservedCost-batchCost {
			return batch, index, batchCost, true
		}
		if !reservation.canAdd(task, limits) {
			break
		}
		reservation.add(task)
		batchCost += task.Resources.CostMicroUSD
		batch = append(batch, task)
		index++
	}
	return batch, index, batchCost, false
}

func (reservation batchReservation) canAdd(task Task, limits Limits) bool {
	if task.Resources.CPUTimeMillis > limits.CPUTimeMillis-reservation.resources.CPUTimeMillis ||
		task.Resources.MemoryBytes > limits.MemoryBytes-reservation.resources.MemoryBytes ||
		task.Resources.StorageBytes > limits.StorageBytes-reservation.resources.StorageBytes ||
		task.Resources.Processes > limits.Processes-reservation.resources.Processes {
		return false
	}
	for _, cohort := range task.CohortSHA256s {
		limit := cohortWorkers(limits.Cohorts, cohort)
		if limit == 0 || reservation.cohorts[cohort] >= limit {
			return false
		}
	}
	return true
}

func (reservation *batchReservation) add(task Task) {
	reservation.resources.CPUTimeMillis += task.Resources.CPUTimeMillis
	reservation.resources.MemoryBytes += task.Resources.MemoryBytes
	reservation.resources.StorageBytes += task.Resources.StorageBytes
	reservation.resources.Processes += task.Resources.Processes
	for _, cohort := range task.CohortSHA256s {
		reservation.cohorts[cohort]++
	}
}

func cohortWorkers(cohorts []CohortLimit, sha string) uint32 {
	index := sort.Search(len(cohorts), func(index int) bool { return cohorts[index].CohortSHA256 >= sha })
	if index == len(cohorts) || cohorts[index].CohortSHA256 != sha {
		return 0
	}
	return cohorts[index].Workers
}

func runBatch(runContext context.Context, cancel context.CancelCauseFunc, parent context.Context, tasks []Task,
	start StartFunc, report *Report,
) error {
	results := make(chan taskResult, len(tasks))
	started := 0
	var startFailure *taskResult
	for _, task := range tasks {
		if err := context.Cause(parent); err != nil {
			cancel(err)
			break
		}
		run, err := start(runContext, task)
		if err != nil || run == nil {
			failure := taskResult{task: task, outcome: OutcomeUnknown, err: errors.Join(ErrStart, err)}
			startFailure = &failure
			cancel(failure.err)
			break
		}
		report.Started++
		started++
		go executeTask(runContext, task, run, results)
	}

	collected := make([]taskResult, 0, started+1)
	for index := 0; index < started; index++ {
		result := <-results
		if result.err != nil || result.outcome == OutcomeCanceled || result.outcome == OutcomeUnknown {
			cause := result.err
			if cause == nil {
				cause = ErrExecution
			}
			cancel(cause)
		}
		collected = append(collected, result)
	}
	if startFailure != nil {
		report.Started++
		report.Completed++
		report.Unknown++
		collected = append(collected, *startFailure)
	}
	for _, result := range collected {
		if startFailure != nil && result.task.TaskSHA256 == startFailure.task.TaskSHA256 {
			continue
		}
		report.Completed++
		countOutcome(report, result.outcome)
	}

	if parentErr := context.Cause(parent); parentErr != nil {
		report.Stop = StopCanceled
		return fmt.Errorf("%w: %w", ErrInterrupted, parentErr)
	}
	if startFailure != nil {
		report.Stop = StopStartFailed
		return startFailure.err
	}
	sort.Slice(collected, func(left, right int) bool { return collected[left].task.Ordinal < collected[right].task.Ordinal })
	for _, result := range collected {
		if result.outcome == OutcomeCanceled {
			report.Stop = StopCanceled
			cause := result.err
			if cause == nil {
				cause = ErrInterrupted
			}
			return fmt.Errorf("%w: task %d: %w", ErrInterrupted, result.task.Ordinal, cause)
		}
		if result.err != nil || result.outcome == OutcomeUnknown {
			report.Stop = StopTaskFailed
			return fmt.Errorf("%w: task %d: %v", ErrExecution, result.task.Ordinal, result.err)
		}
	}
	return nil
}

func executeTask(ctx context.Context, task Task, execute RunFunc, results chan<- taskResult) {
	result := taskResult{task: task, outcome: OutcomeUnknown}
	defer func() {
		if recover() != nil {
			result.outcome, result.err = OutcomeUnknown, errors.Join(ErrExecution, contractError("worker_panic"))
		}
		results <- result
	}()
	result.outcome, result.err = execute(ctx)
	if !result.outcome.valid() {
		result.outcome = OutcomeUnknown
		result.err = errors.Join(ErrExecution, result.err, contractError("outcome"))
	}
}

func countOutcome(report *Report, outcome Outcome) {
	switch outcome {
	case OutcomeSucceeded:
		report.Succeeded++
	case OutcomeFailed:
		report.Failed++
	case OutcomeCanceled:
		report.Canceled++
	case OutcomeUnknown:
		report.Unknown++
	}
}
