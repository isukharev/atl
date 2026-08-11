package lifecycle

const (
	ErrorNone                 = "none"
	ErrorCanceled             = "canceled"
	ErrorDeadline             = "deadline"
	ErrorPolicyDenied         = "policy_denied"
	ErrorUnsupported          = "unsupported"
	ErrorSpawnFailure         = "spawn_failure"
	ErrorComponentFailure     = "component_failure"
	ErrorProtocolFailure      = "protocol_failure"
	ErrorCleanupAmbiguous     = "cleanup_ambiguous"
	ErrorTerminationAmbiguous = "termination_ambiguous"
	ErrorInternal             = "internal"
)

var errorClasses = map[string]bool{
	ErrorNone: true, ErrorCanceled: true, ErrorDeadline: true, ErrorPolicyDenied: true,
	ErrorUnsupported: true, ErrorSpawnFailure: true, ErrorComponentFailure: true,
	ErrorProtocolFailure: true, ErrorCleanupAmbiguous: true, ErrorTerminationAmbiguous: true, ErrorInternal: true,
}

func InitialProjection(plan Plan) (Projection, error) {
	if err := ValidatePlan(plan); err != nil {
		return Projection{}, err
	}
	return Projection{State: StatePlanned, Usage: UnknownUsage()}, nil
}

func NewEvent(plan Plan, current Projection, to State, proofs []Proof, evidence Evidence) (Event, error) {
	if err := ValidatePlan(plan); err != nil || validateProjection(current) != nil || current.Terminal ||
		!sortedUniqueProofs(proofs) || !validTransitionProofs(current.State, to, proofs) {
		return Event{}, contractError("transition")
	}
	event := Event{
		Schema: EventSchema, SchemaVersion: SchemaVersion, LedgerID: plan.LedgerID, AttemptID: plan.AttemptID,
		PlanSHA256: plan.PlanSHA256, Sequence: current.Sequence + 1, PreviousSHA256: current.LastSHA256,
		From: current.State, To: to, Proofs: append([]Proof(nil), proofs...), Evidence: evidence,
	}
	if err := validateEventEvidence(event, current); err != nil {
		return Event{}, err
	}
	digest, err := digestEvent(event)
	if err != nil {
		return Event{}, err
	}
	event.EventSHA256 = digest
	return event, nil
}

func ValidateEvent(event Event) error {
	if event.Schema != EventSchema || event.SchemaVersion != SchemaVersion || !validSHA256(event.LedgerID) ||
		!validSHA256(event.AttemptID) || !validSHA256(event.PlanSHA256) || event.Sequence == 0 || event.Sequence > MaxEvents ||
		(event.Sequence == 1 && event.PreviousSHA256 != "") || (event.Sequence > 1 && !validSHA256(event.PreviousSHA256)) ||
		!sortedUniqueProofs(event.Proofs) || !validTransitionProofs(event.From, event.To, event.Proofs) ||
		validateEvidenceShape(event.Evidence) != nil || validateEventEvidenceShape(event) != nil || !validSHA256(event.EventSHA256) {
		return contractError("event")
	}
	digest, err := digestEvent(event)
	if err != nil || digest != event.EventSHA256 {
		return contractError("event_digest", err)
	}
	return nil
}

func Apply(plan Plan, current Projection, event Event) (Projection, error) {
	if err := ValidatePlan(plan); err != nil || validateProjection(current) != nil || ValidateEvent(event) != nil ||
		current.Terminal || event.LedgerID != plan.LedgerID || event.AttemptID != plan.AttemptID ||
		event.PlanSHA256 != plan.PlanSHA256 || event.Sequence != current.Sequence+1 || event.PreviousSHA256 != current.LastSHA256 ||
		event.From != current.State || validateEventEvidence(event, current) != nil {
		return Projection{}, contractError("apply")
	}
	next := current
	next.State = event.To
	next.Terminal = IsTerminal(event.To)
	next.Sequence = event.Sequence
	next.LastSHA256 = event.EventSHA256
	next.Usage = mergeUsage(current.Usage, event.Evidence.Usage)
	if event.Evidence.ProcessIdentitySHA256 != "" {
		next.ProcessSHA256 = event.Evidence.ProcessIdentitySHA256
	}
	if event.Evidence.ReceiptSHA256 != "" {
		next.ReceiptSHA256 = event.Evidence.ReceiptSHA256
	}
	return next, nil
}

func Project(plan Plan, events []Event) (Projection, error) {
	if len(events) > MaxEvents {
		return Projection{}, contractError("events_limit")
	}
	projection, err := InitialProjection(plan)
	if err != nil {
		return Projection{}, err
	}
	for _, event := range events {
		projection, err = Apply(plan, projection, event)
		if err != nil {
			return Projection{}, err
		}
	}
	return projection, nil
}

func validateProjection(value Projection) error {
	if value.Sequence > MaxEvents || value.Terminal != IsTerminal(value.State) ||
		(value.Sequence == 0 && (value.State != StatePlanned || value.LastSHA256 != "")) ||
		(value.Sequence > 0 && !validSHA256(value.LastSHA256)) || validateUsage(value.Usage) != nil ||
		(value.ProcessSHA256 != "" && !validSHA256(value.ProcessSHA256)) ||
		(value.ReceiptSHA256 != "" && !validSHA256(value.ReceiptSHA256)) {
		return contractError("projection")
	}
	return nil
}

func validateEvidenceShape(evidence Evidence) error {
	if evidence.ProcessIdentitySHA256 != "" && !validSHA256(evidence.ProcessIdentitySHA256) {
		return contractError("process_identity")
	}
	if evidence.ReceiptSHA256 != "" && !validSHA256(evidence.ReceiptSHA256) {
		return contractError("receipt")
	}
	if !errorClasses[evidence.ErrorClass] || validateUsage(evidence.Usage) != nil {
		return contractError("evidence")
	}
	return nil
}

func validateEventEvidence(event Event, current Projection) error {
	if err := validateEvidenceShape(event.Evidence); err != nil {
		return err
	}
	if err := validateEventEvidenceShape(event); err != nil {
		return err
	}
	if current.ProcessSHA256 != "" && event.Evidence.ProcessIdentitySHA256 != "" &&
		current.ProcessSHA256 != event.Evidence.ProcessIdentitySHA256 {
		return contractError("process_identity_drift")
	}
	if current.ReceiptSHA256 != "" && event.Evidence.ReceiptSHA256 != "" && current.ReceiptSHA256 != event.Evidence.ReceiptSHA256 {
		return contractError("receipt_drift")
	}
	if !usageMonotonic(current.Usage, event.Evidence.Usage) {
		return contractError("usage_regression")
	}
	return nil
}

func validateEventEvidenceShape(event Event) error {
	if event.To == StateRunning && event.Evidence.ProcessIdentitySHA256 == "" {
		return contractError("running_identity")
	}
	if containsProof(event.Proofs, ProofTerminalReceipt) && event.Evidence.ReceiptSHA256 == "" {
		return contractError("terminal_receipt")
	}
	errorClassValid := false
	switch event.To {
	case StateCommitted, StateSpawning, StateRunning, StateSucceeded:
		errorClassValid = event.Evidence.ErrorClass == ErrorNone
	case StatePolicyDenied:
		errorClassValid = event.Evidence.ErrorClass == ErrorPolicyDenied
	case StateUnsupported:
		errorClassValid = event.Evidence.ErrorClass == ErrorUnsupported
	case StateCanceled:
		errorClassValid = event.Evidence.ErrorClass == ErrorCanceled
	case StateTimedOut:
		errorClassValid = event.Evidence.ErrorClass == ErrorDeadline
	case StateFailed:
		if containsProof(event.Proofs, ProofDefinitiveSpawnFailure) {
			errorClassValid = event.Evidence.ErrorClass == ErrorSpawnFailure
		} else {
			errorClassValid = event.Evidence.ErrorClass == ErrorComponentFailure || event.Evidence.ErrorClass == ErrorProtocolFailure
		}
	case StateUnknown:
		errorClassValid = event.Evidence.ErrorClass == ErrorProtocolFailure || event.Evidence.ErrorClass == ErrorCleanupAmbiguous ||
			event.Evidence.ErrorClass == ErrorTerminationAmbiguous || event.Evidence.ErrorClass == ErrorInternal
	}
	if !errorClassValid {
		return contractError("error_class")
	}
	return nil
}

func validateUsage(usage Usage) error {
	for _, metric := range []Metric{usage.EstimatedCostMicroUSD, usage.InputTokens, usage.OutputTokens} {
		if metric.State != MetricUnknown && metric.State != MetricObserved {
			return contractError("metric_state")
		}
		if (metric.State == MetricUnknown && metric.Value != nil) || (metric.State == MetricObserved && metric.Value == nil) {
			return contractError("metric_unknown_value")
		}
	}
	return nil
}

func usageMonotonic(previous, next Usage) bool {
	return metricMonotonic(previous.EstimatedCostMicroUSD, next.EstimatedCostMicroUSD) &&
		metricMonotonic(previous.InputTokens, next.InputTokens) && metricMonotonic(previous.OutputTokens, next.OutputTokens)
}

func metricMonotonic(previous, next Metric) bool {
	if next.State == MetricUnknown {
		return next.Value == nil
	}
	return next.State == MetricObserved && next.Value != nil &&
		(previous.State != MetricObserved || previous.Value != nil && *next.Value >= *previous.Value)
}

func mergeUsage(previous, next Usage) Usage {
	return Usage{
		EstimatedCostMicroUSD: mergeMetric(previous.EstimatedCostMicroUSD, next.EstimatedCostMicroUSD),
		InputTokens:           mergeMetric(previous.InputTokens, next.InputTokens),
		OutputTokens:          mergeMetric(previous.OutputTokens, next.OutputTokens),
	}
}

func mergeMetric(previous, next Metric) Metric {
	if next.State == MetricObserved {
		return next
	}
	return previous
}

func containsProof(proofs []Proof, want Proof) bool {
	for _, proof := range proofs {
		if proof == want {
			return true
		}
	}
	return false
}
