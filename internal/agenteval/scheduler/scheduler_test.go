package scheduler

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSchedulerCodecIsClosedCanonicalBoundedAndContentAddressed(t *testing.T) {
	plan := testPlan(t, 2, 2, 2, 100)
	data, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePlan(bytes.NewReader(data))
	if err != nil || !reflect.DeepEqual(decoded, plan) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}

	mutated := plan
	mutated.Limits.Workers++
	if err := ValidatePlan(mutated); !errors.Is(err, ErrContract) {
		t.Fatalf("identity mutation accepted: %v", err)
	}
	for name, candidate := range map[string][]byte{
		"unknown":    bytes.Replace(data, []byte(`"schema":`), []byte(`"unknown":0,"schema":`), 1),
		"duplicate":  bytes.Replace(data, []byte(`{"schema":`), []byte(`{"schema":"agent-eval/scheduler-plan","schema":`), 1),
		"case alias": bytes.Replace(data, []byte(`"tasks":`), []byte(`"Tasks":`), 1),
		"whitespace": append([]byte(" "), data...),
		"trailing":   append(append([]byte{}, data...), []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePlan(bytes.NewReader(candidate)); !errors.Is(err, ErrContract) {
				t.Fatalf("candidate accepted: %v", err)
			}
		})
	}

	report, err := SealReport(plan, Report{Started: 2, Completed: 2, Succeeded: 1, Failed: 1, Stop: StopNone})
	if err != nil {
		t.Fatal(err)
	}
	reportData, err := EncodeReport(plan, report)
	if err != nil {
		t.Fatal(err)
	}
	wantReport, err := DecodeReport(bytes.NewReader(reportData), plan)
	if err != nil || !reflect.DeepEqual(wantReport, report) {
		t.Fatalf("report=%+v err=%v", wantReport, err)
	}
	for name, candidate := range map[string]Report{
		"in flight":        {Started: 2, Completed: 1, Succeeded: 1, Stop: StopCanceled},
		"spent no cost":    {Started: 2, Completed: 2, Succeeded: 2, Stop: StopCostExhausted},
		"no start error":   {Started: 1, Completed: 1, Succeeded: 1, Stop: StopStartFailed},
		"counter overflow": {Succeeded: ^uint32(0), Failed: 1, Stop: StopCanceled},
	} {
		t.Run("report "+name, func(t *testing.T) {
			if _, err := SealReport(plan, candidate); !errors.Is(err, ErrContract) {
				t.Fatalf("report accepted: %v", err)
			}
		})
	}
}

func TestSchedulerDispatchIsRoundOrderedBoundedAndCompletionOrderIndependent(t *testing.T) {
	plan := testPlan(t, 4, 2, 2, 100)
	var startMu sync.Mutex
	starts := []uint32{}
	var running atomic.Int32
	var peak atomic.Int32
	roundBarriers := map[uint32]*sync.WaitGroup{1: {}, 2: {}}
	roundBarriers[1].Add(2)
	roundBarriers[2].Add(2)

	report, err := Run(context.Background(), plan, func(_ context.Context, task Task) (RunFunc, error) {
		startMu.Lock()
		starts = append(starts, task.Ordinal)
		startMu.Unlock()
		return func(context.Context) (Outcome, error) {
			current := running.Add(1)
			for current > peak.Load() && !peak.CompareAndSwap(peak.Load(), current) {
			}
			roundBarriers[task.Round].Done()
			roundBarriers[task.Round].Wait()
			running.Add(-1)
			if task.Ordinal%2 == 0 {
				return OutcomeFailed, nil
			}
			return OutcomeSucceeded, nil
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(starts, []uint32{1, 2, 3, 4}) || peak.Load() != 2 || report.Started != 4 || report.Completed != 4 ||
		report.Succeeded != 2 || report.Failed != 2 || report.Stop != StopNone || report.NeverStarted != 0 {
		t.Fatalf("starts=%v peak=%d report=%+v", starts, peak.Load(), report)
	}
}

func TestSchedulerCohortAndResourceLimitsBoundEachBatch(t *testing.T) {
	plan := testPlan(t, 4, 1, 4, 100)
	for name, constrain := range map[string]func(*Limits){
		"cpu":       func(limits *Limits) { limits.CPUTimeMillis = 1 },
		"memory":    func(limits *Limits) { limits.MemoryBytes = 1 },
		"storage":   func(limits *Limits) { limits.StorageBytes = 1 },
		"processes": func(limits *Limits) { limits.Processes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			limits := plan.Limits
			limits.Cohorts = append([]CohortLimit(nil), limits.Cohorts...)
			limits.Cohorts[0].Workers = limits.Workers
			constrain(&limits)
			batch, next, _, blocked := nextBatch(limits, plan.Tasks, 0, len(plan.Tasks), 0)
			if blocked || len(batch) != 1 || next != 1 {
				t.Fatalf("batch=%d next=%d blocked=%t", len(batch), next, blocked)
			}
		})
	}
	var err error
	plan, err = SealPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var running atomic.Int32
	var peak atomic.Int32
	report, err := Run(context.Background(), plan, func(_ context.Context, _ Task) (RunFunc, error) {
		return func(context.Context) (Outcome, error) {
			current := running.Add(1)
			for current > peak.Load() && !peak.CompareAndSwap(peak.Load(), current) {
			}
			running.Add(-1)
			return OutcomeSucceeded, nil
		}, nil
	})
	if err != nil || peak.Load() != 1 || report.Succeeded != 4 {
		t.Fatalf("peak=%d report=%+v err=%v", peak.Load(), report, err)
	}
}

func TestSchedulerCostExhaustionStopsBeforeTheNextReservation(t *testing.T) {
	plan := testPlan(t, 3, 3, 3, 5)
	for index := range plan.Tasks {
		plan.Tasks[index].Resources.CostMicroUSD = 2
	}
	var err error
	plan, err = SealPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	started := []uint32{}
	report, runErr := Run(context.Background(), plan, func(_ context.Context, task Task) (RunFunc, error) {
		started = append(started, task.Ordinal)
		return func(context.Context) (Outcome, error) { return OutcomeSucceeded, nil }, nil
	})
	if !errors.Is(runErr, ErrCostExhausted) || !reflect.DeepEqual(started, []uint32{1, 2}) || report.Started != 2 ||
		report.Completed != 2 || report.NeverStarted != 1 || report.Stop != StopCostExhausted {
		t.Fatalf("started=%v report=%+v err=%v", started, report, runErr)
	}
}

func TestSchedulerFailFastCancelsTheCurrentBatchAndNeverStartsLaterRounds(t *testing.T) {
	plan := testPlan(t, 4, 2, 2, 100)
	starts := []uint32{}
	report, runErr := Run(context.Background(), plan, func(_ context.Context, task Task) (RunFunc, error) {
		starts = append(starts, task.Ordinal)
		return func(ctx context.Context) (Outcome, error) {
			if task.Ordinal == 1 {
				return OutcomeUnknown, errors.New("worker lost")
			}
			<-ctx.Done()
			return OutcomeCanceled, ctx.Err()
		}, nil
	})
	if !errors.Is(runErr, ErrExecution) || !reflect.DeepEqual(starts, []uint32{1, 2}) || report.Unknown != 1 ||
		report.Canceled != 1 || report.NeverStarted != 2 || report.Stop != StopTaskFailed {
		t.Fatalf("starts=%v report=%+v err=%v", starts, report, runErr)
	}
	panicPlan := testPlan(t, 1, 1, 1, 100)
	panicReport, panicErr := Run(context.Background(), panicPlan, func(context.Context, Task) (RunFunc, error) {
		return func(context.Context) (Outcome, error) { panic("worker lost") }, nil
	})
	if !errors.Is(panicErr, ErrExecution) || panicReport.Unknown != 1 || panicReport.Stop != StopTaskFailed {
		t.Fatalf("panic report=%+v err=%v", panicReport, panicErr)
	}
}

func TestSchedulerCanceledContextStartsNothing(t *testing.T) {
	plan := testPlan(t, 1, 1, 1, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, plan, func(context.Context, Task) (RunFunc, error) {
		t.Fatal("started after cancellation")
		return nil, nil
	})
	if !errors.Is(err, ErrInterrupted) || report.Started != 0 || report.NeverStarted != 1 || report.Stop != StopCanceled {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	report, err = RunRemaining(ctx, plan, []TerminalTask{{TaskSHA256: plan.Tasks[0].TaskSHA256, Outcome: OutcomeSucceeded}},
		func(context.Context, Task) (RunFunc, error) {
			t.Fatal("complete terminal snapshot was replayed")
			return nil, nil
		})
	if !errors.Is(err, ErrInterrupted) || report.Succeeded != 1 || report.Stop != StopCanceled {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestSchedulerResumeCountsTerminalTasksAndDispatchesOnlyPlannedComplement(t *testing.T) {
	plan := testPlan(t, 4, 2, 2, 100)
	terminal := []TerminalTask{
		{TaskSHA256: plan.Tasks[0].TaskSHA256, Outcome: OutcomeSucceeded},
		{TaskSHA256: plan.Tasks[2].TaskSHA256, Outcome: OutcomeUnknown},
	}
	started := []uint32{}
	report, err := RunRemaining(context.Background(), plan, terminal, func(_ context.Context, task Task) (RunFunc, error) {
		started = append(started, task.Ordinal)
		return func(context.Context) (Outcome, error) { return OutcomeFailed, nil }, nil
	})
	if err != nil || !reflect.DeepEqual(started, []uint32{2, 4}) || report.Started != 4 || report.Completed != 4 ||
		report.Succeeded != 1 || report.Failed != 2 || report.Unknown != 1 || report.Stop != StopNone {
		t.Fatalf("started=%v report=%+v err=%v", started, report, err)
	}
	if _, err := RunRemaining(context.Background(), plan, []TerminalTask{terminal[1], terminal[0]}, func(context.Context, Task) (RunFunc, error) {
		return nil, nil
	}); !errors.Is(err, ErrContract) {
		t.Fatalf("out-of-order terminal snapshot accepted: %v", err)
	}
}

func TestSchedulerPlanRejectsRosterOrderCohortAndResourceDrift(t *testing.T) {
	base := testPlan(t, 4, 2, 2, 100)
	tests := map[string]func(Plan) Plan{
		"ordinal duplicate": func(plan Plan) Plan { plan.Tasks[1].Ordinal = plan.Tasks[0].Ordinal; return plan },
		"same-round reorder": func(plan Plan) Plan {
			plan.Tasks[0], plan.Tasks[1] = plan.Tasks[1], plan.Tasks[0]
			return plan
		},
		"round gap":      func(plan Plan) Plan { plan.Tasks[1].Round = 3; return plan },
		"task duplicate": func(plan Plan) Plan { plan.Tasks[1].TaskSHA256 = plan.Tasks[0].TaskSHA256; return plan },
		"uppercase identity": func(plan Plan) Plan {
			plan.Tasks[0].TaskSHA256 = strings.ToUpper(plan.Tasks[0].TaskSHA256)
			return plan
		},
		"unknown cohort":    func(plan Plan) Plan { plan.Tasks[0].CohortSHA256s[0] = testSHA("f"); return plan },
		"resource overflow": func(plan Plan) Plan { plan.Tasks[0].Resources.MemoryBytes = MaxMemoryBytes + 1; return plan },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := clonePlan(base)
			candidate.PlanSHA256 = ""
			if _, err := SealPlan(mutate(candidate)); !errors.Is(err, ErrContract) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func testPlan(t *testing.T, tasks, cohortWorkers, workers int, totalCost uint64) Plan {
	t.Helper()
	cohort := testSHA("a")
	candidate := Plan{
		Limits: Limits{Workers: uint32(workers), CPUTimeMillis: uint64(workers), MemoryBytes: uint64(workers),
			StorageBytes: uint64(workers), Processes: uint32(workers), TotalCostMicroUSD: totalCost,
			Cohorts: []CohortLimit{{CohortSHA256: cohort, Workers: uint32(cohortWorkers)}}},
		Tasks: make([]Task, tasks),
	}
	for index := range candidate.Tasks {
		candidate.Tasks[index] = Task{TaskSHA256: testSHA(string(rune('b' + index))), Ordinal: uint32(index + 1),
			Round: uint32(index/2 + 1), Resources: Resources{CPUTimeMillis: 1, MemoryBytes: 1, StorageBytes: 1, Processes: 1},
			CohortSHA256s: []string{cohort}}
	}
	plan, err := SealPlan(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testSHA(character string) string { return strings.Repeat(character, 64) }
