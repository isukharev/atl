package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
)

const maxContractBytes = 1 << 20

func DecodeScenario(r io.Reader) (Scenario, error) {
	var value Scenario
	if err := decodeStrict(r, &value); err != nil {
		return Scenario{}, err
	}
	if err := value.Validate(); err != nil {
		return Scenario{}, err
	}
	return value, nil
}

func DecodeObservation(r io.Reader) (Observation, error) {
	var value Observation
	if err := decodeStrict(r, &value); err != nil {
		return Observation{}, err
	}
	if err := value.Validate(); err != nil {
		return Observation{}, err
	}
	return value, nil
}

func DecodeResult(r io.Reader) (Result, error) {
	var value Result
	if err := decodeStrict(r, &value); err != nil {
		return Result{}, err
	}
	if err := value.Validate(); err != nil {
		return Result{}, err
	}
	return value, nil
}

func decodeStrict(r io.Reader, dst any) error {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(dst)
	if limited.N <= 0 {
		return fmt.Errorf("contract exceeds %d bytes", maxContractBytes)
	}
	if decodeErr != nil {
		return fmt.Errorf("decode contract: %w", decodeErr)
	}
	var extra any
	trailingErr := decoder.Decode(&extra)
	if limited.N <= 0 {
		return fmt.Errorf("contract exceeds %d bytes", maxContractBytes)
	}
	if trailingErr != io.EOF {
		if trailingErr == nil {
			return fmt.Errorf("contract contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing contract data: %w", trailingErr)
	}
	return nil
}
