package agenteval

import (
	"io"

	"github.com/isukharev/atl/internal/agenteval/scheduler"
)

const (
	SchedulerPlanSchema       = scheduler.PlanSchema
	SchedulerReportSchema     = scheduler.ReportSchema
	SchedulerSchemaVersion    = scheduler.SchemaVersion
	SchedulerContractVersion  = scheduler.ContractVersion
	SchedulerPlanMaxBytes     = scheduler.MaxPlanBytes
	SchedulerReportMaxBytes   = scheduler.MaxReportBytes
	SchedulerMaximumWorkers   = scheduler.MaxWorkers
	SchedulerMaximumTaskCount = scheduler.MaxTasks
)

type SchedulerResources = scheduler.Resources
type SchedulerCohortLimit = scheduler.CohortLimit
type SchedulerLimits = scheduler.Limits
type SchedulerTask = scheduler.Task
type SchedulerPlan = scheduler.Plan
type SchedulerStopReason = scheduler.StopReason
type SchedulerOutcome = scheduler.Outcome
type SchedulerReport = scheduler.Report

func EncodeSchedulerPlan(plan SchedulerPlan) ([]byte, error) { return scheduler.EncodePlan(plan) }

func DecodeSchedulerPlan(reader io.Reader) (SchedulerPlan, error) {
	return scheduler.DecodePlan(reader)
}

func EncodeSchedulerReport(plan SchedulerPlan, report SchedulerReport) ([]byte, error) {
	return scheduler.EncodeReport(plan, report)
}

func DecodeSchedulerReport(reader io.Reader, plan SchedulerPlan) (SchedulerReport, error) {
	return scheduler.DecodeReport(reader, plan)
}
