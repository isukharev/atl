package agentadapter

import (
	"bytes"
	"encoding/json"
	"io"
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
		return Contract{}, contractError("decode")
	}
	canonical, err := encodeCanonical(contract, MaxContractBytes)
	if err != nil || !bytes.Equal(data, canonical) {
		return Contract{}, contractError("canonical")
	}
	return contract, nil
}

func EncodeObservation(contract Contract, observation Observation) ([]byte, error) {
	if err := ValidateObservation(contract, observation); err != nil {
		return nil, err
	}
	return encodeCanonical(observation, MaxObservationBytes)
}

func DecodeObservation(reader io.Reader, contract Contract) (Observation, error) {
	var observation Observation
	data, err := readCanonical(reader, MaxObservationBytes)
	if err != nil || decodeClosed(data, &observation) != nil || ValidateObservation(contract, observation) != nil {
		return Observation{}, contractError("decode_observation")
	}
	canonical, err := encodeCanonical(observation, MaxObservationBytes)
	if err != nil || !bytes.Equal(data, canonical) {
		return Observation{}, contractError("canonical_observation")
	}
	return observation, nil
}

// ObservationSHA256 returns the content identity of one canonical normalized
// observation under its exact adapter contract.
func ObservationSHA256(contract Contract, observation Observation) (string, error) {
	data, err := EncodeObservation(contract, observation)
	if err != nil {
		return "", err
	}
	return hashDomain("observation", data), nil
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
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || len(data) == 0 || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 {
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
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return contractError("trailing")
	}
	return nil
}
