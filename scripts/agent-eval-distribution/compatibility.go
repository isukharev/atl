package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	maxCompatibilityEntries = 1024
	compatibilitySchemaV1   = 1
)

type compatibilityGoldenBundle struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type compatibilityReadability struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Versions  []int  `json:"versions"`
}

type compatibilityForwardRejection struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
}

type compatibilityMetricVector struct {
	ID             string          `json:"id"`
	Representation string          `json:"representation"`
	Present        bool            `json:"present"`
	Required       bool            `json:"required"`
	State          *string         `json:"state"`
	Coverage       bool            `json:"coverage"`
	Value          json.RawMessage `json:"value"`
	Valid          bool            `json:"valid"`
}

type compatibilityBundle struct {
	SchemaVersion   int                             `json:"schema_version"`
	ContractVersion string                          `json:"contract_version"`
	GoldenBundle    compatibilityGoldenBundle       `json:"golden_bundle"`
	Readability     []compatibilityReadability      `json:"readability"`
	Forward         []compatibilityForwardRejection `json:"forward_rejection"`
	MetricVectors   []compatibilityMetricVector     `json:"metric_vectors"`
}

func decodeCompatibilityBundle(data []byte) (compatibilityBundle, error) {
	var members map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&members); err != nil {
		return compatibilityBundle{}, errors.New("compatibility bundle is not a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return compatibilityBundle{}, errors.New("compatibility bundle has trailing data")
	}
	want := map[string]bool{
		"schema_version": true, "contract_version": true, "golden_bundle": true,
		"readability": true, "forward_rejection": true, "metric_vectors": true,
	}
	if len(members) != len(want) {
		return compatibilityBundle{}, errors.New("compatibility bundle has unknown or missing members")
	}
	for name := range members {
		if !want[name] {
			return compatibilityBundle{}, errors.New("compatibility bundle has unknown or non-canonical member")
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle compatibilityBundle
	if err := decoder.Decode(&bundle); err != nil {
		return compatibilityBundle{}, errors.New("compatibility bundle has invalid member types")
	}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return compatibilityBundle{}, errors.New("compatibility bundle has trailing data")
	}
	return bundle, nil
}

func validateCompatibilityBundle(data []byte, contractVersion string) error {
	bundle, err := decodeCompatibilityBundle(data)
	if err != nil {
		return err
	}
	if bundle.SchemaVersion != compatibilitySchemaV1 || bundle.ContractVersion != contractVersion ||
		!safeName(bundle.GoldenBundle.Path) || !validDigest(bundle.GoldenBundle.SHA256) ||
		len(bundle.Readability) == 0 || len(bundle.Readability) > maxCompatibilityEntries ||
		len(bundle.Forward) == 0 || len(bundle.Forward) > maxCompatibilityEntries ||
		len(bundle.MetricVectors) == 0 || len(bundle.MetricVectors) > maxCompatibilityEntries {
		return errors.New("compatibility bundle metadata is invalid")
	}
	for _, entry := range bundle.Readability {
		if !safeName(entry.Namespace) || !safeName(entry.Kind) || len(entry.Versions) == 0 || len(entry.Versions) > maxCompatibilityEntries {
			return errors.New("compatibility readability entry is invalid")
		}
		previous := 0
		for _, version := range entry.Versions {
			if version <= 0 || version <= previous {
				return errors.New("compatibility readability versions are not canonical")
			}
			previous = version
		}
	}
	for _, entry := range bundle.Forward {
		if !safeName(entry.Namespace) || !safeName(entry.Kind) || entry.Version <= 0 {
			return errors.New("compatibility forward-rejection entry is invalid")
		}
	}
	for _, vector := range bundle.MetricVectors {
		if !safeName(vector.ID) || !safeName(vector.Representation) {
			return errors.New("compatibility metric vector identity is invalid")
		}
		if !vector.Present && len(vector.Value) != 0 {
			return errors.New("missing compatibility metric cannot carry a value")
		}
		if vector.State != nil && !safeName(*vector.State) {
			return errors.New("compatibility metric state is invalid")
		}
	}
	return nil
}
