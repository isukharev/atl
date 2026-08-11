package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const PrivateActivationReportMaxBytes = 1 << 20

var ErrPrivateActivationReport = errors.New("private_activation_report_invalid")

func EncodePrivateActivationReport(report PrivateActivationReport) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, ErrPrivateActivationReport
	}
	data, err := json.Marshal(report)
	if err != nil || len(data)+1 > PrivateActivationReportMaxBytes {
		return nil, ErrPrivateActivationReport
	}
	return append(data, '\n'), nil
}

func DecodePrivateActivationReport(reader io.Reader) (PrivateActivationReport, error) {
	if reader == nil {
		return PrivateActivationReport{}, ErrPrivateActivationReport
	}
	limited := &io.LimitedReader{R: reader, N: PrivateActivationReportMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) == 0 || validateJSONNoDuplicateKeys(data) != nil {
		return PrivateActivationReport{}, ErrPrivateActivationReport
	}
	var report PrivateActivationReport
	if decodeStrictJSONObject(data, &report) != nil || report.Validate() != nil {
		return PrivateActivationReport{}, ErrPrivateActivationReport
	}
	canonical, err := EncodePrivateActivationReport(report)
	if err != nil || !bytes.Equal(data, canonical) {
		return PrivateActivationReport{}, ErrPrivateActivationReport
	}
	return report, nil
}
