package agenteval

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

const (
	maxCLIErrorContractStderrBytes = 64 << 10
	maxCLIErrorContractLineBytes   = 8 << 10
	maxCLIErrorContractExitCode    = 255
)

// cliErrorContractVocabulary is the closed kind-to-remediation table the CLI
// emits on a failed command: the transport-neutral classifications plus the two
// CLI-local policy classifications. A benchmark contract can therefore only
// ever hold a pair the CLI itself defines; anything else stays unclassified.
// The table is duplicated here on purpose. It is the reviewed evidence
// vocabulary of this harness, so a future CLI classification must be reviewed
// into the benchmark rather than silently widening a published contract.
var cliErrorContractVocabulary = map[string]struct {
	remediation string
	exitCodes   []int
}{
	"api_error":             {"inspect_backend_error", []int{1}},
	"authentication_failed": {"reauthenticate", []int{3}},
	"check_failed":          {"review_failed_check", []int{8}},
	"configuration_error":   {"complete_configuration", []int{7}},
	"forbidden":             {"request_access", []int{6}},
	"internal_error":        {"report_bug", []int{8}},
	"not_found":             {"verify_identifier_or_access", []int{4}},
	"output_limit_exceeded": {"narrow_or_raise_bound", []int{1, 8}},
	"rate_limited":          {"wait_before_retry", []int{1}},
	"read_only_policy":      {"request_human_approval", []int{8}},
	"transport_error":       {"inspect_network_before_retry", []int{1}},
	"unexpected_error":      {"inspect_error", []int{1}},
	"usage_error":           {"fix_request", []int{2}},
	"version_conflict":      {"refresh_and_reapply", []int{5}},
}

// CLIErrorContract is the whole record of one failed reviewed CLI invocation:
// the audited process exit code and the CLI's own stable kind/remediation
// pair. Error text, command, arguments, and backend prose are never part of
// it, so the record stays publishable and content-free.
type CLIErrorContract struct {
	ExitCode    int    `json:"exit_code"`
	Kind        string `json:"kind"`
	Remediation string `json:"remediation"`
}

// KnownCLIErrorContracts returns the complete stable vocabulary as a sorted
// copy. It lets the CLI's runtime contract test prove that every accepted
// triplet is actually reachable, without exposing the mutable table or
// restating its size.
func KnownCLIErrorContracts() []CLIErrorContract {
	contracts := make([]CLIErrorContract, 0, len(cliErrorContractVocabulary))
	for kind, known := range cliErrorContractVocabulary {
		for _, exitCode := range known.exitCodes {
			contracts = append(contracts, CLIErrorContract{
				ExitCode: exitCode, Kind: kind, Remediation: known.remediation,
			})
		}
	}
	sort.Slice(contracts, func(left, right int) bool {
		if contracts[left].Kind != contracts[right].Kind {
			return contracts[left].Kind < contracts[right].Kind
		}
		return contracts[left].ExitCode < contracts[right].ExitCode
	})
	return contracts
}

// cliErrorBody is the closed member set of the CLI's JSON error object. Every
// member is declared so an unknown one fails closed, and only the exit code,
// kind, and remediation survive: the message, the policy marker, and the
// command are read to prove the shape and are then discarded.
type cliErrorBody struct {
	Error       string  `json:"error"`
	Code        int     `json:"code"`
	Kind        string  `json:"kind"`
	Remediation string  `json:"remediation"`
	Policy      *string `json:"policy"`
	Command     *string `json:"command"`
}

// ValidateCLIErrorContract admits one already-parsed classification. It is the
// single authority for what a contract may contain, so the confined proxy that
// first records one and the runner that revalidates the audit before
// evaluation cannot drift apart.
func ValidateCLIErrorContract(exitCode int, kind, remediation string) (CLIErrorContract, bool) {
	if exitCode < 1 || exitCode > maxCLIErrorContractExitCode {
		return CLIErrorContract{}, false
	}
	known, exists := cliErrorContractVocabulary[kind]
	if !exists || remediation != known.remediation {
		return CLIErrorContract{}, false
	}
	allowedExitCode := false
	for _, code := range known.exitCodes {
		allowedExitCode = allowedExitCode || exitCode == code
	}
	if !allowedExitCode {
		return CLIErrorContract{}, false
	}
	return CLIErrorContract{ExitCode: exitCode, Kind: kind, Remediation: remediation}, true
}

// ParseCLIErrorContract classifies one failed brokered CLI invocation from the
// last non-empty line of its captured stderr. A successful invocation, a
// missing or oversized capture, a non-JSON or human-readable line, a truncated
// or trailing-data line, an unknown member, a code that disagrees with the
// audited exit code, and a pair outside the closed vocabulary all stay
// unclassified. Callers that receive false must record nothing at all rather
// than a partial or guessed classification.
func ParseCLIErrorContract(exitCode int, stderr []byte) (CLIErrorContract, bool) {
	if exitCode == 0 || len(stderr) == 0 || len(stderr) > maxCLIErrorContractStderrBytes {
		return CLIErrorContract{}, false
	}
	line := lastNonEmptyLine(stderr)
	if len(line) == 0 || len(line) > maxCLIErrorContractLineBytes {
		return CLIErrorContract{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var body cliErrorBody
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
		return CLIErrorContract{}, false
	}
	if body.Error == "" || body.Code != exitCode {
		return CLIErrorContract{}, false
	}
	// The read-only policy members travel together with their own kind. A line
	// that carries one of them in any other combination is not the reviewed CLI
	// error body and is refused before classification.
	if body.Kind == "read_only_policy" {
		if body.Policy == nil || *body.Policy != "read_only" || body.Command == nil || *body.Command == "" {
			return CLIErrorContract{}, false
		}
	} else if body.Policy != nil || body.Command != nil {
		return CLIErrorContract{}, false
	}
	return ValidateCLIErrorContract(exitCode, body.Kind, body.Remediation)
}

func lastNonEmptyLine(data []byte) []byte {
	lines := bytes.Split(data, []byte{'\n'})
	for index := len(lines) - 1; index >= 0; index-- {
		if line := bytes.TrimSpace(lines[index]); len(line) != 0 {
			return line
		}
	}
	return nil
}
