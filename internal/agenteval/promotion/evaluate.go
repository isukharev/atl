package promotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
)

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func decisionReasons(input ComparisonInput) []Reason {
	reasons := make([]Reason, 0, MaxReasons)
	if input.Interrupted {
		reasons = append(reasons, ReasonInterrupted)
	}
	for _, review := range input.Reviews {
		if !review.Reviewed {
			switch review.Component {
			case ComponentSkill:
				reasons = append(reasons, ReasonUnreviewedSkill)
			case ComponentEvaluation:
				reasons = append(reasons, ReasonUnreviewedEvaluation)
			case ComponentGrader:
				reasons = append(reasons, ReasonUnreviewedGrader)
			case ComponentHoldout:
				reasons = append(reasons, ReasonUnreviewedHoldout)
			}
		}
	}
	for _, axis := range input.Axes {
		if axis.Blocking {
			reasons = append(reasons, axis.Reason)
		}
	}
	return uniqueReasons(reasons)
}

func uniqueReasons(values []Reason) []Reason {
	seen := make(map[Reason]bool, len(values))
	result := make([]Reason, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return cloneReasons(result)
}

// Evaluate creates a deterministic decision. Refusal is the safe result for
// any blocking or unknown axis; it is never converted into a weighted score.
func Evaluate(input ComparisonInput) (DecisionReceipt, error) {
	if err := validateInput(input); err != nil {
		return DecisionReceipt{}, err
	}
	reasons := decisionReasons(input)
	decision := DecisionPromote
	if input.Interrupted || len(reasons) != 0 {
		decision = DecisionRefuse
	}
	receipt := DecisionReceipt{
		Schema: Schema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		Decision: decision, Reference: input.Reference, Candidate: input.Candidate,
		Reviews: cloneReviews(input.Reviews), Axes: cloneAxes(input.Axes), Reasons: reasons,
		Interrupted: input.Interrupted,
	}
	digest, err := digestJSON(receiptWithoutDigest(receipt))
	if err != nil {
		return DecisionReceipt{}, fail(ErrorInvalidReceipt)
	}
	receipt.ReceiptSHA256 = digest
	return receipt, nil
}

func EncodeComparison(input ComparisonInput) ([]byte, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	input.Schema, input.SchemaVersion, input.ContractVersion = ComparisonSchema, SchemaVersion, ContractVersion
	input.Reviews = cloneReviews(input.Reviews)
	input.Axes = cloneAxes(input.Axes)
	data, err := json.Marshal(input)
	if err != nil || len(data) > MaxReceiptBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

func DecodeComparison(reader io.Reader) (ComparisonInput, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxReceiptBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxReceiptBytes {
		return ComparisonInput{}, fail(ErrorLimitExceeded)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input ComparisonInput
	if err := decoder.Decode(&input); err != nil {
		return ComparisonInput{}, fail(ErrorInvalidReceipt)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ComparisonInput{}, fail(ErrorInvalidReceipt)
	}
	if err := validateInput(input); err != nil {
		return ComparisonInput{}, err
	}
	canonical, err := EncodeComparison(input)
	if err != nil || !bytes.Equal(canonical, data) {
		return ComparisonInput{}, fail(ErrorInvalidReceipt)
	}
	return input, nil
}

func EncodeDecision(receipt DecisionReceipt) ([]byte, error) {
	if err := ValidateDecision(receipt); err != nil {
		return nil, err
	}
	data, err := json.Marshal(receipt)
	if err != nil || len(data) > MaxReceiptBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

func DecodeDecision(reader io.Reader) (DecisionReceipt, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxReceiptBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxReceiptBytes {
		return DecisionReceipt{}, fail(ErrorLimitExceeded)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt DecisionReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return DecisionReceipt{}, fail(ErrorInvalidReceipt)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DecisionReceipt{}, fail(ErrorInvalidReceipt)
	}
	canonical, err := EncodeDecision(receipt)
	if err != nil || !bytes.Equal(canonical, data) {
		return DecisionReceipt{}, fail(ErrorInvalidReceipt)
	}
	return receipt, nil
}

func ValidateDecision(receipt DecisionReceipt) error {
	if receipt.Schema != Schema || receipt.SchemaVersion != SchemaVersion || receipt.ContractVersion != ContractVersion ||
		(receipt.Decision != DecisionPromote && receipt.Decision != DecisionRefuse) || validateIdentity(receipt.Reference) != nil ||
		validateIdentity(receipt.Candidate) != nil || identityEqual(receipt.Reference, receipt.Candidate) ||
		validateReviews(receipt.Reviews, receipt.Reference, receipt.Candidate) != nil || validateAxes(receipt.Axes) != nil ||
		receipt.Reasons == nil || validateReasons(receipt.Reasons) != nil || !validDigest(receipt.ReceiptSHA256) {
		return fail(ErrorInvalidReceipt)
	}
	expectedReasons := decisionReasons(ComparisonInput{Reference: receipt.Reference, Candidate: receipt.Candidate, Reviews: receipt.Reviews, Axes: receipt.Axes, Interrupted: receipt.Interrupted})
	if len(expectedReasons) != len(receipt.Reasons) {
		return fail(ErrorInvalidReceipt)
	}
	for index := range expectedReasons {
		if expectedReasons[index] != receipt.Reasons[index] {
			return fail(ErrorInvalidReceipt)
		}
	}
	wantDecision := DecisionPromote
	if len(receipt.Reasons) != 0 {
		wantDecision = DecisionRefuse
	}
	if receipt.Decision != wantDecision {
		return fail(ErrorInvalidReceipt)
	}
	digest, err := digestJSON(receiptWithoutDigest(receipt))
	if err != nil || digest != receipt.ReceiptSHA256 {
		return fail(ErrorInvalidReceipt)
	}
	return nil
}

// PlanRollback creates a deterministic, exact rollback receipt. It does not
// mutate a reference; mutation is performed only by an explicit Store method.
func PlanRollback(request RollbackRequest) (RollbackReceipt, error) {
	if validateIdentity(request.Current) != nil || validateIdentity(request.Restore) != nil ||
		identityEqual(request.Current, request.Restore) || !validDigest(request.AuthorizationSHA256) {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	receipt := RollbackReceipt{Schema: RollbackSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		Decision: DecisionRollback, Current: request.Current, Restore: request.Restore, Restored: false,
		AuthorizationSHA256: request.AuthorizationSHA256}
	digest, err := digestJSON(rollbackWithoutDigest(receipt))
	if err != nil {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	receipt.ReceiptSHA256 = digest
	return receipt, nil
}

func ValidateRollback(receipt RollbackReceipt) error {
	if receipt.Schema != RollbackSchema || receipt.SchemaVersion != SchemaVersion || receipt.ContractVersion != ContractVersion ||
		receipt.Decision != DecisionRollback || validateIdentity(receipt.Current) != nil ||
		validateIdentity(receipt.Restore) != nil || identityEqual(receipt.Current, receipt.Restore) ||
		!validDigest(receipt.AuthorizationSHA256) || !validDigest(receipt.ReceiptSHA256) ||
		(receipt.Restored && !validDigest(receipt.RequestSHA256)) || (!receipt.Restored && receipt.RequestSHA256 != "") {
		return fail(ErrorInvalidRollback)
	}
	digest, err := digestJSON(rollbackWithoutDigest(receipt))
	if err != nil || digest != receipt.ReceiptSHA256 {
		return fail(ErrorInvalidRollback)
	}
	return nil
}

func EncodeRollback(receipt RollbackReceipt) ([]byte, error) {
	if err := ValidateRollback(receipt); err != nil {
		return nil, err
	}
	data, err := json.Marshal(receipt)
	if err != nil || len(data) > MaxReceiptBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

func DecodeRollback(reader io.Reader) (RollbackReceipt, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxReceiptBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxReceiptBytes {
		return RollbackReceipt{}, fail(ErrorLimitExceeded)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt RollbackReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	canonical, err := EncodeRollback(receipt)
	if err != nil || !bytes.Equal(canonical, data) {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	return receipt, nil
}
