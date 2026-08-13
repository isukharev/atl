package agenteval

import (
	"context"
	"io"

	"github.com/isukharev/atl/internal/agenteval/analysis"
)

const (
	AnalysisReportSchema          = analysis.ReportSchema
	AnalysisReportSchemaVersion   = analysis.SchemaVersion
	AnalysisReportContractVersion = analysis.ContractVersion
	AnalysisReportMaxBytes        = analysis.MaxReportBytes
	AnalysisErrorInvalidInput     = analysis.ErrorInvalidInput
	AnalysisErrorInvalidReport    = analysis.ErrorInvalidReport
	AnalysisErrorLimitExceeded    = analysis.ErrorLimitExceeded
	AnalysisErrorInterrupted      = analysis.ErrorInterrupted
)

type AnalysisErrorCode = analysis.ErrorCode
type AnalysisReport = analysis.Report

func AnalysisErrorCodeOf(err error) (AnalysisErrorCode, bool) { return analysis.CodeOf(err) }

func DecodeAnalysisReport(reader io.Reader, manifest ExperimentManifest) (AnalysisReport, error) {
	return analysis.DecodeReport(reader, manifest)
}

func EncodeAnalysisReport(report AnalysisReport, manifest ExperimentManifest) ([]byte, error) {
	return analysis.EncodeReport(report, manifest)
}

// AnalyzeSequentialReferencePublication admits the manifest before reading
// trial bytes, completes the exact-tree and transitive artifact inspection,
// then analyzes only its canonical membership and records. It does not read a
// second path snapshot, reconstruct pairs, execute, or write an artifact.
func AnalyzeSequentialReferencePublication(destination string) (AnalysisReport, error) {
	return AnalyzeSequentialReferencePublicationContext(context.Background(), destination)
}

func AnalyzeSequentialReferencePublicationContext(ctx context.Context, destination string) (AnalysisReport, error) {
	if err := analysis.ContextError(ctx); err != nil {
		return AnalysisReport{}, err
	}
	manifest, inspected, err := inspectSequentialReferencePublicationWithAdmissionContext(ctx, destination, analysis.ValidateManifest)
	if err != nil {
		if interrupted := analysis.ContextError(ctx); interrupted != nil {
			return AnalysisReport{}, interrupted
		}
		return AnalysisReport{}, err
	}
	if err := analysis.ContextError(ctx); err != nil {
		return AnalysisReport{}, err
	}
	records := make([]ExperimentTrialRecord, len(inspected.Trials))
	for index := range inspected.Trials {
		records[index] = inspected.Trials[index].TrialRecord
	}
	return analysis.AnalyzeContext(ctx, manifest, records)
}
