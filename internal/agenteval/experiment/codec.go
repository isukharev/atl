package experiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const maxJSONDepth = 64

func EncodeCapabilityContract(contract CapabilityContract) ([]byte, error) {
	if err := ValidateCapabilityContract(contract); err != nil {
		return nil, err
	}
	return encodeCanonical(contract, MaxCapabilityBytes, ErrorInvalidCapability)
}

func DecodeCapabilityContract(reader io.Reader) (CapabilityContract, error) {
	var contract CapabilityContract
	data, err := readCanonical(reader, MaxCapabilityBytes, ErrorInvalidCapability)
	if err != nil || decodeClosed(data, &contract) != nil || ValidateCapabilityContract(contract) != nil {
		return CapabilityContract{}, contractError(ErrorInvalidCapability, err)
	}
	canonical, err := EncodeCapabilityContract(contract)
	if err != nil || !bytes.Equal(data, canonical) {
		return CapabilityContract{}, contractError(ErrorInvalidCapability, err)
	}
	return cloneCapabilityContract(contract), nil
}

func EncodeDesign(design Design) ([]byte, error) {
	if err := ValidateDesign(design); err != nil {
		return nil, err
	}
	return encodeCanonical(design, MaxDesignBytes, ErrorInvalidDesign)
}

func DecodeDesign(reader io.Reader) (Design, error) {
	var design Design
	data, err := readCanonical(reader, MaxDesignBytes, ErrorInvalidDesign)
	if err != nil || decodeClosed(data, &design) != nil || ValidateDesign(design) != nil {
		return Design{}, contractError(ErrorInvalidDesign, err)
	}
	canonical, err := EncodeDesign(design)
	if err != nil || !bytes.Equal(data, canonical) {
		return Design{}, contractError(ErrorInvalidDesign, err)
	}
	return cloneDesign(design), nil
}

func EncodeAnalysisPlan(plan AnalysisPlan) ([]byte, error) {
	if err := ValidateAnalysisPlan(plan); err != nil {
		return nil, err
	}
	return encodeCanonical(plan, MaxAnalysisBytes, ErrorInvalidAnalysis)
}

func DecodeAnalysisPlan(reader io.Reader) (AnalysisPlan, error) {
	var plan AnalysisPlan
	data, err := readCanonical(reader, MaxAnalysisBytes, ErrorInvalidAnalysis)
	if err != nil || decodeClosed(data, &plan) != nil || ValidateAnalysisPlan(plan) != nil {
		return AnalysisPlan{}, contractError(ErrorInvalidAnalysis, err)
	}
	canonical, err := EncodeAnalysisPlan(plan)
	if err != nil || !bytes.Equal(data, canonical) {
		return AnalysisPlan{}, contractError(ErrorInvalidAnalysis, err)
	}
	return cloneAnalysisPlan(plan), nil
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return encodeCanonical(manifest, MaxManifestBytes, ErrorInvalidManifest)
}

func DecodeManifest(reader io.Reader) (Manifest, error) {
	var manifest Manifest
	data, err := readCanonical(reader, MaxManifestBytes, ErrorInvalidManifest)
	if err != nil || decodeClosed(data, &manifest) != nil || ValidateManifest(manifest) != nil {
		return Manifest{}, contractError(ErrorInvalidManifest, err)
	}
	canonical, err := EncodeManifest(manifest)
	if err != nil || !bytes.Equal(data, canonical) {
		return Manifest{}, contractError(ErrorInvalidManifest, err)
	}
	return cloneManifest(manifest), nil
}

func EncodeTrialRecord(manifest Manifest, record TrialRecord) ([]byte, error) {
	if err := ValidateTrialRecord(manifest, record); err != nil {
		return nil, err
	}
	return encodeCanonical(record, MaxTrialBytes, ErrorInvalidTrial)
}

func DecodeTrialRecord(reader io.Reader, manifest Manifest) (TrialRecord, error) {
	validator, err := NewTrialRecordValidator(manifest)
	if err != nil {
		return TrialRecord{}, err
	}
	return validator.Decode(reader)
}

// Decode reads one canonical trial record against an already-authenticated
// manifest. Reusing the validator keeps batch consumers from re-deriving the
// manifest registry for every member.
func (validator *TrialRecordValidator) Decode(reader io.Reader) (TrialRecord, error) {
	var record TrialRecord
	data, err := readCanonical(reader, MaxTrialBytes, ErrorInvalidTrial)
	if err != nil || decodeClosed(data, &record) != nil || validator.Validate(record) != nil {
		return TrialRecord{}, contractError(ErrorInvalidTrial, err)
	}
	canonical, err := encodeCanonical(record, MaxTrialBytes, ErrorInvalidTrial)
	if err != nil || !bytes.Equal(data, canonical) {
		return TrialRecord{}, contractError(ErrorInvalidTrial, err)
	}
	return cloneTrialRecord(record), nil
}

func encodeCanonical(value any, maximum int, code ErrorCode) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > maximum {
		return nil, contractError(code, err)
	}
	return append(data, '\n'), nil
}

func readCanonical(reader io.Reader, maximum int, code ErrorCode) ([]byte, error) {
	if reader == nil {
		return nil, contractError(code, errInvalidValue)
	}
	limited := &io.LimitedReader{R: reader, N: int64(maximum) + 1}
	data, err := io.ReadAll(limited)
	if err != nil || len(data) < 3 || len(data) > maximum || limited.N == 0 || !utf8.Valid(data) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 ||
		validateJSONMembers(data[:len(data)-1]) != nil {
		return nil, contractError(code, err)
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
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidValue
	}
	return nil
}

func validateJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidValue
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errInvalidValue
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || seen[name] {
				return errInvalidValue
			}
			seen[name] = true
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errInvalidValue
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errInvalidValue
		}
	default:
		return errInvalidValue
	}
	return nil
}
