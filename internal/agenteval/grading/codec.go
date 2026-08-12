package grading

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

func EncodeContract(contract Contract) ([]byte, error) {
	if err := ValidateContract(contract); err != nil {
		return nil, err
	}
	return encodeCanonical(contract, MaxContractBytes)
}

func DecodeContract(reader io.Reader) (Contract, error) {
	var contract Contract
	data, err := readCanonical(reader, MaxContractBytes)
	if err != nil || decodeClosed(data, &contract) != nil || ValidateContract(contract) != nil {
		return Contract{}, contractError("decode_contract")
	}
	canonical, err := encodeCanonical(contract, MaxContractBytes)
	if err != nil || !bytes.Equal(data, canonical) {
		return Contract{}, contractError("canonical_contract")
	}
	return cloneContract(contract), nil
}

func EncodePlan(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	return encodeCanonical(plan, MaxPlanBytes)
}

func DecodePlan(reader io.Reader) (Plan, error) {
	var plan Plan
	data, err := readCanonical(reader, MaxPlanBytes)
	if err != nil || decodeClosed(data, &plan) != nil || ValidatePlan(plan) != nil {
		return Plan{}, contractError("decode_plan")
	}
	canonical, err := encodeCanonical(plan, MaxPlanBytes)
	if err != nil || !bytes.Equal(data, canonical) {
		return Plan{}, contractError("canonical_plan")
	}
	return clonePlan(plan), nil
}

func EncodeReceipt(plan Plan, receipt Receipt) ([]byte, error) {
	if err := ValidateReceipt(plan, receipt); err != nil {
		return nil, err
	}
	return encodeCanonical(receipt, MaxReceiptBytes)
}

func DecodeReceipt(reader io.Reader, plan Plan) (Receipt, error) {
	var receipt Receipt
	data, err := readCanonical(reader, MaxReceiptBytes)
	if err != nil || decodeClosed(data, &receipt) != nil || ValidateReceipt(plan, receipt) != nil {
		return Receipt{}, contractError("decode_receipt")
	}
	canonical, err := encodeCanonical(receipt, MaxReceiptBytes)
	if err != nil || !bytes.Equal(data, canonical) {
		return Receipt{}, contractError("canonical_receipt")
	}
	return cloneReceipt(receipt), nil
}

func ContractSHA256(contract Contract) (string, error) {
	data, err := EncodeContract(contract)
	if err != nil {
		return "", err
	}
	return hashDomain("grader-contract", data), nil
}

func PlanSHA256(plan Plan) (string, error) {
	data, err := EncodePlan(plan)
	if err != nil {
		return "", err
	}
	return hashDomain("grading-plan", data), nil
}

func ReceiptSHA256(plan Plan, receipt Receipt) (string, error) {
	data, err := EncodeReceipt(plan, receipt)
	if err != nil {
		return "", err
	}
	return hashDomain("grade-receipt", data), nil
}

func ReviewSHA256(plan Plan, review Review) (string, error) {
	if err := validateReview(plan, review, ""); err != nil {
		return "", err
	}
	data, err := encodeCanonical(review, MaxReceiptBytes)
	if err != nil {
		return "", err
	}
	return hashDomain("review", data), nil
}

func encodeCanonical(value any, limit int64) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || int64(len(data))+1 > limit {
		return nil, contractError("encode")
	}
	return append(data, '\n'), nil
}

func readCanonical(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, contractError("reader")
	}
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) == 0 || int64(len(data)) > limit || !utf8.Valid(data) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 ||
		validateJSONShape(data) != nil {
		return nil, contractError("bounds")
	}
	return data, nil
}

func decodeClosed(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return contractError("trailing")
	}
	return nil
}
