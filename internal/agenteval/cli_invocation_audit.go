package agenteval

import "fmt"

// CLIInvocationAuditRecord is the shared, content-free record exchanged by
// the evaluator's CLI wrapper and runner. Field order is part of the JSONL ABI:
// historical records remain readable and new records must retain their exact
// serialized byte order.
type CLIInvocationAuditRecord struct {
	CommandFamily                string `json:"command_family,omitempty"`
	CalibrationObservationSHA256 string `json:"calibration_observation_sha256,omitempty"`
	ErrorKind                    string `json:"error_kind,omitempty"`
	ErrorRemediation             string `json:"error_remediation,omitempty"`
	Denied                       bool   `json:"denied,omitempty"`
	StdoutBytes                  int64  `json:"stdout_bytes"`
	StderrBytes                  int64  `json:"stderr_bytes"`
	ExitCode                     int    `json:"exit_code"`
}

// errorContract revalidates a recorded classification against this record's
// own audited exit code and the closed CLI vocabulary before it can reach an
// oracle. An absent classification is ordinary: a successful, denied, or
// unclassified invocation simply contributes nothing. A present but
// inconsistent one is a corrupt audit and fails the run closed.
func (r CLIInvocationAuditRecord) errorContract() (CLIErrorContract, bool, error) {
	if r.ErrorKind == "" && r.ErrorRemediation == "" {
		return CLIErrorContract{}, false, nil
	}
	contract, ok := ValidateCLIErrorContract(r.ExitCode, r.ErrorKind, r.ErrorRemediation)
	if !ok || r.Denied {
		return CLIErrorContract{}, false, fmt.Errorf("atl proxy record carries an invalid CLI error contract")
	}
	return contract, true, nil
}

// atlProxyRecord preserves the package-local historical name while the writer
// in cmd/agent-eval consumes the canonical exported ABI type directly.
type atlProxyRecord = CLIInvocationAuditRecord
