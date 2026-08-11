package agenteval

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"testing"
)

func TestPrivateActivationReportCodecIsBoundedCanonicalAndHistorical(t *testing.T) {
	for _, version := range []int{LegacyPrivateActivationReportSchemaVersion, PrivateActivationReportSchemaVersion} {
		report := privateActivationReportCodecFixture(version)
		encoded, err := EncodePrivateActivationReport(report)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodePrivateActivationReport(bytes.NewReader(encoded))
		if err != nil || decoded.SchemaVersion != version {
			t.Fatalf("version=%d decoded=%+v err=%v", version, decoded, err)
		}
		for name, mutation := range map[string][]byte{
			"future":    bytes.Replace(encoded, []byte(`"schema_version":`+strconv.Itoa(version)), []byte(`"schema_version":3`), 1),
			"unknown":   bytes.Replace(encoded, []byte(`"schema_version":`), []byte(`"unknown":true,"schema_version":`), 1),
			"duplicate": bytes.Replace(encoded, []byte(`"schema_version":`), []byte(`"schema_version":1,"schema_version":`), 1),
			"trailing":  append(slices.Clone(encoded), []byte(`{}`)...),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := DecodePrivateActivationReport(bytes.NewReader(mutation)); !errors.Is(err, ErrPrivateActivationReport) {
					t.Fatalf("error=%v, want decode rejection", err)
				}
			})
		}
	}
}

func privateActivationReportCodecFixture(version int) PrivateActivationReport {
	gates := PrivateActivationGates{CaptureEligible: true}
	report := PrivateActivationReport{SchemaVersion: version, Gates: gates, Treatments: make([]PrivateActivationTreatmentReport, 0, 4), Contrasts: []PrivateActivationContrast{}}
	for _, treatment := range privateActivationTreatments {
		metrics := []PrivateActivationMetric{}
		if version == PrivateActivationReportSchemaVersion {
			metrics = []PrivateActivationMetric{
				{Name: privateActivationMetricEvidenceAttemptedBPS, Value: 0},
				{Name: privateActivationMetricEvidenceBlockedBPS, Value: 0},
				{Name: privateActivationMetricReportedNoneBPS, Value: 10000},
				{Name: privateActivationMetricReportedUnavailableBPS, Value: 0},
				{Name: privateActivationMetricEvidenceSucceededBPS, Value: 0},
				{Name: privateActivationMetricEvidenceUnavailableBPS, Value: 0},
			}
		}
		report.Treatments = append(report.Treatments, PrivateActivationTreatmentReport{Treatment: treatment, Gates: gates, Metrics: metrics})
	}
	return report
}
