// Package scheduler owns a neutral, deterministic, bounded local dispatch
// contract. It has no filesystem, process, provider, or lifecycle authority.
package scheduler

import (
	"context"
	"errors"
)

const (
	PlanSchema      = "agent-eval/scheduler-plan"
	ReportSchema    = "agent-eval/scheduler-report"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxPlanBytes     = 4 << 20
	MaxReportBytes   = 64 << 10
	MaxTasks         = 4096
	MaxWorkers       = 256
	MaxCohorts       = 4096
	MaxTaskCohorts   = 8
	MaxCPUTimeMillis = 1 << 50
	MaxMemoryBytes   = 1 << 50
	MaxStorageBytes  = 1 << 50
	MaxProcesses     = 1 << 20
	MaxCostMicroUSD  = 1 << 50
	MaxJSONDepth     = 32
)

var (
	ErrContract      = errors.New("scheduler_contract_invalid")
	ErrInterrupted   = errors.New("scheduler_interrupted")
	ErrCostExhausted = errors.New("scheduler_cost_exhausted")
	ErrStart         = errors.New("scheduler_start_failed")
	ErrExecution     = errors.New("scheduler_execution_failed")
)

// Resources is the maximum share reserved for one task. Cost is cumulative
// once dispatch starts; every other field is an in-flight batch reservation.
type Resources struct {
	CPUTimeMillis uint64 `json:"cpu_time_millis"`
	MemoryBytes   uint64 `json:"memory_bytes"`
	StorageBytes  uint64 `json:"storage_bytes"`
	Processes     uint32 `json:"processes"`
	CostMicroUSD  uint64 `json:"cost_microusd"`
}

// CohortLimit bounds one opaque identity class. The producer owns the meaning
// of the digest; the neutral scheduler compares only exact identities.
type CohortLimit struct {
	CohortSHA256 string `json:"cohort_sha256"`
	Workers      uint32 `json:"workers"`
}

type Limits struct {
	Workers           uint32        `json:"workers"`
	CPUTimeMillis     uint64        `json:"cpu_time_millis"`
	MemoryBytes       uint64        `json:"memory_bytes"`
	StorageBytes      uint64        `json:"storage_bytes"`
	Processes         uint32        `json:"processes"`
	TotalCostMicroUSD uint64        `json:"total_cost_microusd"`
	Cohorts           []CohortLimit `json:"cohorts"`
}

// Task binds one already-authored immutable attempt. Round is a one-based
// barrier: no task in a later round may start until the current round is done.
type Task struct {
	TaskSHA256    string    `json:"task_sha256"`
	Ordinal       uint32    `json:"ordinal"`
	Round         uint32    `json:"round"`
	Resources     Resources `json:"resources"`
	CohortSHA256s []string  `json:"cohort_sha256s"`
}

type Plan struct {
	Schema          string `json:"schema"`
	SchemaVersion   int    `json:"schema_version"`
	ContractVersion string `json:"contract_version"`
	Limits          Limits `json:"limits"`
	Tasks           []Task `json:"tasks"`
	PlanSHA256      string `json:"plan_sha256"`
}

type StopReason string

const (
	StopNone          StopReason = "none"
	StopCanceled      StopReason = "canceled"
	StopCostExhausted StopReason = "cost_exhausted"
	StopStartFailed   StopReason = "start_failed"
	StopTaskFailed    StopReason = "task_failed"
)

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeCanceled  Outcome = "canceled"
	OutcomeUnknown   Outcome = "unknown"
)

// TerminalTask is a previously started absorbing result supplied by a durable
// owner during resume. It is counted but never dispatched again.
type TerminalTask struct {
	TaskSHA256 string
	Outcome    Outcome
}

// Report contains content-free counters only. It intentionally excludes
// timestamps, worker identifiers, and completion order.
type Report struct {
	Schema          string     `json:"schema"`
	SchemaVersion   int        `json:"schema_version"`
	ContractVersion string     `json:"contract_version"`
	PlanSHA256      string     `json:"plan_sha256"`
	Queued          uint32     `json:"queued"`
	Started         uint32     `json:"started"`
	Completed       uint32     `json:"completed"`
	Succeeded       uint32     `json:"succeeded"`
	Failed          uint32     `json:"failed"`
	Canceled        uint32     `json:"canceled"`
	Unknown         uint32     `json:"unknown"`
	NeverStarted    uint32     `json:"never_started"`
	Stop            StopReason `json:"stop"`
	ReportSHA256    string     `json:"report_sha256"`
}

// RunFunc owns one task after its durable start boundary has succeeded.
type RunFunc func(context.Context) (Outcome, error)

// StartFunc crosses the producer-owned durable start boundary in deterministic
// plan order and returns the single-use execution closure.
type StartFunc func(context.Context, Task) (RunFunc, error)
