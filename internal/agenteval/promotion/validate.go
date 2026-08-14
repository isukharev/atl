package promotion

func validateReview(review ComponentReview, reference, candidate Identity) error {
	if componentOrdinal(review.Component) < 0 || !review.Reviewed ||
		!validDigest(review.ReferenceSHA256) || !validDigest(review.CandidateSHA256) || !validDigest(review.ReviewSHA256) ||
		review.ReferenceSHA256 != componentDigest(reference, review.Component) ||
		review.CandidateSHA256 != componentDigest(candidate, review.Component) {
		return fail(ErrorInvalidReview)
	}
	return nil
}

func validateReviews(reviews []ComponentReview, reference, candidate Identity) error {
	if len(reviews) != MaxComponents {
		return fail(ErrorInvalidReview)
	}
	seen := make(map[Component]bool, len(reviews))
	for index, review := range reviews {
		if index > 0 && componentOrdinal(reviews[index-1].Component) >= componentOrdinal(review.Component) {
			return fail(ErrorInvalidReview)
		}
		if seen[review.Component] || validateReview(review, reference, candidate) != nil {
			return fail(ErrorInvalidReview)
		}
		seen[review.Component] = true
	}
	for _, component := range components {
		if !seen[component] {
			return fail(ErrorInvalidReview)
		}
	}
	return nil
}

func expectedAxisReason(axis Axis) Reason {
	switch axis {
	case AxisSafety:
		return ReasonSafetyRegression
	case AxisCoverage:
		return ReasonCoverageMissing
	case AxisRuntime:
		return ReasonRuntimeIncompatible
	case AxisQuality:
		return ReasonQualityRegression
	case AxisNegativeLift:
		return ReasonNegativeLift
	case AxisResource:
		return ReasonResourceExhausted
	default:
		return ""
	}
}

func validateAxis(value AxisResult) error {
	if axisOrdinal(value.Axis) < 0 || (value.State != AxisPass && value.State != AxisFail && value.State != AxisUnknown) {
		return fail(ErrorInvalidAxis)
	}
	if !value.Blocking && value.State != AxisPass {
		return fail(ErrorInvalidAxis)
	}
	if value.Blocking && value.State == AxisPass {
		return fail(ErrorInvalidAxis)
	}
	if value.State != AxisPass && value.Reason != expectedAxisReason(value.Axis) {
		return fail(ErrorInvalidAxis)
	}
	if value.State == AxisPass && value.Reason != "" {
		return fail(ErrorInvalidAxis)
	}
	if value.EvidenceSHA256 != "" && !validDigest(value.EvidenceSHA256) {
		return fail(ErrorInvalidAxis)
	}
	return nil
}

func validateAxes(values []AxisResult) error {
	if len(values) != MaxAxes {
		return fail(ErrorInvalidAxis)
	}
	seen := make(map[Axis]bool, len(values))
	for index, value := range values {
		if index > 0 && axisOrdinal(values[index-1].Axis) >= axisOrdinal(value.Axis) {
			return fail(ErrorInvalidAxis)
		}
		if seen[value.Axis] || validateAxis(value) != nil {
			return fail(ErrorInvalidAxis)
		}
		seen[value.Axis] = true
	}
	for _, axis := range axes {
		if !seen[axis] {
			return fail(ErrorInvalidAxis)
		}
	}
	return nil
}

func validateReasons(reasons []Reason) error {
	if len(reasons) > MaxReasons {
		return fail(ErrorLimitExceeded)
	}
	seen := make(map[Reason]bool, len(reasons))
	for index, reason := range reasons {
		if index > 0 && reasonOrdinal(reasons[index-1]) >= reasonOrdinal(reason) {
			return fail(ErrorInvalidReceipt)
		}
		if reasonOrdinal(reason) < 0 || seen[reason] {
			return fail(ErrorInvalidReceipt)
		}
		seen[reason] = true
	}
	return nil
}

func validateInput(input ComparisonInput) error {
	if (input.Schema != "" && input.Schema != ComparisonSchema) || (input.SchemaVersion != 0 && input.SchemaVersion != SchemaVersion) ||
		(input.ContractVersion != "" && input.ContractVersion != ContractVersion) {
		return fail(ErrorInvalidReceipt)
	}
	if err := validateIdentity(input.Reference); err != nil {
		return err
	}
	if err := validateIdentity(input.Candidate); err != nil {
		return err
	}
	if identityEqual(input.Reference, input.Candidate) {
		return fail(ErrorInvalidIdentity)
	}
	if err := validateReviews(input.Reviews, input.Reference, input.Candidate); err != nil {
		return err
	}
	return validateAxes(input.Axes)
}

func cloneReviews(input []ComponentReview) []ComponentReview {
	result := append([]ComponentReview(nil), input...)
	sortReviews(result)
	return result
}

func cloneAxes(input []AxisResult) []AxisResult {
	result := append([]AxisResult(nil), input...)
	sortAxes(result)
	return result
}

func cloneReasons(input []Reason) []Reason {
	result := make([]Reason, len(input))
	copy(result, input)
	sortReasons(result)
	return result
}

func receiptWithoutDigest(receipt DecisionReceipt) DecisionReceipt {
	receipt.ReceiptSHA256 = ""
	return receipt
}

func rollbackWithoutDigest(receipt RollbackReceipt) RollbackReceipt {
	receipt.ReceiptSHA256 = ""
	return receipt
}
