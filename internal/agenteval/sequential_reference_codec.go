package agenteval

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/grading"
)

const (
	SequentialReferenceBundleSchema    = "agent-eval/sequential-reference-bundle"
	SequentialReferenceSchemaVersion   = 1
	SequentialReferenceContractVersion = "0.1.0-pre-release"
	SequentialReferenceBundleMaxBytes  = 64 << 20
)

func NewSequentialReferenceBundle(manifest ExperimentManifest, gradingPlan GradingPlan,
	treatments []SequentialReferenceTreatment,
) (SequentialReferenceBundle, error) {
	bundle := SequentialReferenceBundle{
		Schema: SequentialReferenceBundleSchema, SchemaVersion: SequentialReferenceSchemaVersion,
		ContractVersion: SequentialReferenceContractVersion, ManifestSHA256: manifest.ManifestSHA256,
		GradingPlan: gradingPlan, Treatments: cloneSequentialReferenceTreatments(treatments),
	}
	if err := validateSequentialReferenceBundleShape(bundle); err != nil {
		return SequentialReferenceBundle{}, err
	}
	return bundle, nil
}

func EncodeSequentialReferenceBundle(bundle SequentialReferenceBundle) ([]byte, error) {
	if err := validateSequentialReferenceBundleShape(bundle); err != nil {
		return nil, err
	}
	data, err := json.Marshal(bundle)
	if err != nil || len(data)+1 > SequentialReferenceBundleMaxBytes {
		return nil, sequentialReferenceError("bundle_encode", err)
	}
	return append(data, '\n'), nil
}

func DecodeSequentialReferenceBundle(reader io.Reader) (SequentialReferenceBundle, error) {
	if reader == nil {
		return SequentialReferenceBundle{}, sequentialReferenceError("bundle_reader", nil)
	}
	data, err := io.ReadAll(io.LimitReader(reader, SequentialReferenceBundleMaxBytes+1))
	if err != nil || len(data) < 3 || len(data) > SequentialReferenceBundleMaxBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 || !utf8.Valid(data) {
		return SequentialReferenceBundle{}, sequentialReferenceError("bundle_encoding", err)
	}
	body := data[:len(data)-1]
	if err := validateJSONNoDuplicateKeys(body); err != nil {
		return SequentialReferenceBundle{}, sequentialReferenceError("bundle_encoding", err)
	}
	var bundle SequentialReferenceBundle
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil || decoder.Decode(new(any)) != io.EOF {
		return SequentialReferenceBundle{}, sequentialReferenceError("bundle_decode", err)
	}
	if err := validateSequentialReferenceBundleShape(bundle); err != nil {
		return SequentialReferenceBundle{}, err
	}
	canonical, err := json.Marshal(bundle)
	if err != nil || !bytes.Equal(canonical, body) {
		return SequentialReferenceBundle{}, sequentialReferenceError("bundle_canonical", err)
	}
	return cloneSequentialReferenceBundle(bundle), nil
}

func validateSequentialReferenceBundleShape(bundle SequentialReferenceBundle) error {
	if bundle.Schema != SequentialReferenceBundleSchema || bundle.SchemaVersion != SequentialReferenceSchemaVersion ||
		bundle.ContractVersion != SequentialReferenceContractVersion || !validSHA256(bundle.ManifestSHA256) ||
		bundle.Treatments == nil || len(bundle.Treatments) == 0 || len(bundle.Treatments) > experiment.MaxTreatments {
		return sequentialReferenceError("bundle_shape", nil)
	}
	if _, err := grading.EncodePlan(bundle.GradingPlan); err != nil {
		return sequentialReferenceError("bundle_grading_plan", err)
	}
	var inputBytes uint64
	for index, treatment := range bundle.Treatments {
		if treatment.TreatmentID == "" || index > 0 && bundle.Treatments[index-1].TreatmentID >= treatment.TreatmentID ||
			treatment.Inputs.Definitions == nil || treatment.Inputs.Fixture == nil || treatment.Inputs.Skill == nil {
			return sequentialReferenceError("bundle_treatments", nil)
		}
		if _, err := executionbackend.EncodePlan(treatment.Plan); err != nil {
			return sequentialReferenceError("bundle_execution_plan", err)
		}
		for _, input := range [][]byte{treatment.Inputs.Definitions, treatment.Inputs.Fixture, treatment.Inputs.Skill} {
			if uint64(len(input)) > uint64(SequentialReferenceBundleMaxBytes)-inputBytes {
				return sequentialReferenceError("bundle_input_bytes", nil)
			}
			inputBytes += uint64(len(input))
		}
	}
	return nil
}

func cloneSequentialReferenceBundle(bundle SequentialReferenceBundle) SequentialReferenceBundle {
	bundle.Treatments = cloneSequentialReferenceTreatments(bundle.Treatments)
	return bundle
}

func cloneSequentialReferenceTreatments(input []SequentialReferenceTreatment) []SequentialReferenceTreatment {
	result := make([]SequentialReferenceTreatment, len(input))
	for index, treatment := range input {
		result[index] = treatment
		result[index].Inputs = executionbackend.ReferenceInputs{
			Definitions: slices.Clone(treatment.Inputs.Definitions),
			Fixture:     slices.Clone(treatment.Inputs.Fixture),
			Skill:       slices.Clone(treatment.Inputs.Skill),
		}
	}
	return result
}
