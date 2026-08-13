package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

func SealPlan(candidate Plan) (Plan, error) {
	candidate.Schema = PlanSchema
	candidate.SchemaVersion = SchemaVersion
	candidate.ContractVersion = ContractVersion
	candidate.PlanSHA256 = ""
	candidate = clonePlan(candidate)
	if err := validatePlanShape(candidate, false); err != nil {
		return Plan{}, err
	}
	candidate.PlanSHA256 = digest("scheduler-plan-v1", candidate)
	if err := ValidatePlan(candidate); err != nil {
		return Plan{}, err
	}
	return candidate, nil
}

func ValidatePlan(plan Plan) error { return validatePlanShape(plan, true) }

func validatePlanShape(plan Plan, requireDigest bool) error {
	if plan.Schema != PlanSchema || plan.SchemaVersion != SchemaVersion || plan.ContractVersion != ContractVersion ||
		(requireDigest && !validSHA256(plan.PlanSHA256)) || (!requireDigest && plan.PlanSHA256 != "") ||
		plan.Tasks == nil || len(plan.Tasks) == 0 || len(plan.Tasks) > MaxTasks ||
		plan.Limits.Cohorts == nil || len(plan.Limits.Cohorts) > MaxCohorts || !validLimits(plan.Limits) {
		return contractError("plan_shape")
	}
	cohorts := make(map[string]uint32, len(plan.Limits.Cohorts))
	for index, cohort := range plan.Limits.Cohorts {
		if !validSHA256(cohort.CohortSHA256) || cohort.Workers == 0 || cohort.Workers > plan.Limits.Workers ||
			(index > 0 && plan.Limits.Cohorts[index-1].CohortSHA256 >= cohort.CohortSHA256) {
			return contractError("cohort_limits")
		}
		cohorts[cohort.CohortSHA256] = cohort.Workers
	}
	seenTasks := make(map[string]struct{}, len(plan.Tasks))
	seenOrdinals := make([]bool, len(plan.Tasks)+1)
	var previousRound uint32
	var previousOrdinal uint32
	for index, task := range plan.Tasks {
		if !validSHA256(task.TaskSHA256) || task.Ordinal == 0 || int(task.Ordinal) > len(plan.Tasks) || task.Round == 0 ||
			len(task.CohortSHA256s) > MaxTaskCohorts || task.CohortSHA256s == nil || !validResources(task.Resources) ||
			!resourcesFit(task.Resources, plan.Limits) {
			return contractError("task_shape")
		}
		if _, exists := seenTasks[task.TaskSHA256]; exists || seenOrdinals[task.Ordinal] {
			return contractError("task_identity")
		}
		seenTasks[task.TaskSHA256] = struct{}{}
		seenOrdinals[task.Ordinal] = true
		if index == 0 {
			if task.Round != 1 {
				return contractError("task_round")
			}
		} else if task.Round < previousRound || task.Round > previousRound+1 ||
			(task.Round == previousRound && task.Ordinal <= previousOrdinal) {
			return contractError("task_order")
		}
		previousRound, previousOrdinal = task.Round, task.Ordinal
		for cohortIndex, cohort := range task.CohortSHA256s {
			if !validSHA256(cohort) || cohorts[cohort] == 0 ||
				(cohortIndex > 0 && task.CohortSHA256s[cohortIndex-1] >= cohort) {
				return contractError("task_cohorts")
			}
		}
	}
	for ordinal := 1; ordinal <= len(plan.Tasks); ordinal++ {
		if !seenOrdinals[ordinal] {
			return contractError("task_roster")
		}
	}
	if requireDigest {
		candidate := clonePlan(plan)
		candidate.PlanSHA256 = ""
		if digest("scheduler-plan-v1", candidate) != plan.PlanSHA256 {
			return contractError("plan_identity")
		}
	}
	return nil
}

func SealReport(plan Plan, candidate Report) (Report, error) {
	if err := ValidatePlan(plan); err != nil {
		return Report{}, err
	}
	candidate.Schema = ReportSchema
	candidate.SchemaVersion = SchemaVersion
	candidate.ContractVersion = ContractVersion
	candidate.PlanSHA256 = plan.PlanSHA256
	candidate.Queued = uint32(len(plan.Tasks)) // #nosec G115 -- plan task count is bounded.
	candidate.NeverStarted = candidate.Queued - candidate.Started
	candidate.ReportSHA256 = ""
	if err := validateReportShape(plan, candidate, false); err != nil {
		return Report{}, err
	}
	candidate.ReportSHA256 = digest("scheduler-report-v1", candidate)
	if err := ValidateReport(plan, candidate); err != nil {
		return Report{}, err
	}
	return candidate, nil
}

func ValidateReport(plan Plan, report Report) error { return validateReportShape(plan, report, true) }

func validateReportShape(plan Plan, report Report, requireDigest bool) error {
	totalOutcomes := uint64(report.Succeeded) + uint64(report.Failed) + uint64(report.Canceled) + uint64(report.Unknown)
	if ValidatePlan(plan) != nil || report.Schema != ReportSchema || report.SchemaVersion != SchemaVersion ||
		report.ContractVersion != ContractVersion || report.PlanSHA256 != plan.PlanSHA256 ||
		(requireDigest && !validSHA256(report.ReportSHA256)) || (!requireDigest && report.ReportSHA256 != "") ||
		report.Queued != uint32(len(plan.Tasks)) || // #nosec G115 -- ValidatePlan caps tasks at MaxTasks.
		report.Started > report.Queued || report.Completed != report.Started ||
		totalOutcomes != uint64(report.Completed) ||
		report.NeverStarted != report.Queued-report.Started || !report.Stop.valid() ||
		(report.Stop == StopNone && report.Completed != report.Queued) ||
		(report.Stop == StopCostExhausted && report.NeverStarted == 0) ||
		(report.Stop == StopStartFailed && report.Unknown == 0) {
		return contractError("report_shape")
	}
	if requireDigest {
		candidate := report
		candidate.ReportSHA256 = ""
		if digest("scheduler-report-v1", candidate) != report.ReportSHA256 {
			return contractError("report_identity")
		}
	}
	return nil
}

func validLimits(limits Limits) bool {
	return limits.Workers > 0 && limits.Workers <= MaxWorkers &&
		limits.CPUTimeMillis <= MaxCPUTimeMillis && limits.MemoryBytes <= MaxMemoryBytes &&
		limits.StorageBytes <= MaxStorageBytes && limits.Processes <= MaxProcesses &&
		limits.TotalCostMicroUSD <= MaxCostMicroUSD
}

func validResources(resources Resources) bool {
	return resources.CPUTimeMillis <= MaxCPUTimeMillis && resources.MemoryBytes <= MaxMemoryBytes &&
		resources.StorageBytes <= MaxStorageBytes && resources.Processes <= MaxProcesses &&
		resources.CostMicroUSD <= MaxCostMicroUSD
}

func resourcesFit(resources Resources, limits Limits) bool {
	return resources.CPUTimeMillis <= limits.CPUTimeMillis && resources.MemoryBytes <= limits.MemoryBytes &&
		resources.StorageBytes <= limits.StorageBytes && resources.Processes <= limits.Processes &&
		resources.CostMicroUSD <= limits.TotalCostMicroUSD
}

func (reason StopReason) valid() bool {
	return reason == StopNone || reason == StopCanceled || reason == StopCostExhausted ||
		reason == StopStartFailed || reason == StopTaskFailed
}

func (outcome Outcome) valid() bool {
	return outcome == OutcomeSucceeded || outcome == OutcomeFailed || outcome == OutcomeCanceled || outcome == OutcomeUnknown
}

func clonePlan(plan Plan) Plan {
	plan.Limits.Cohorts = slices.Clone(plan.Limits.Cohorts)
	plan.Tasks = slices.Clone(plan.Tasks)
	for index := range plan.Tasks {
		plan.Tasks[index].CohortSHA256s = slices.Clone(plan.Tasks[index].CohortSHA256s)
	}
	return plan
}

func digest(domain string, value any) string {
	data, err := json.Marshal(struct {
		Domain string `json:"domain"`
		Value  any    `json:"value"`
	}{Domain: domain, Value: value})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func contractError(code string) error { return fmt.Errorf("%w: %s", ErrContract, code) }
