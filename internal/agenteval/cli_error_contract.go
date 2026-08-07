package agenteval

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
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
	"content_policy":        {"request_human_approval", []int{8}},
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
// copy. Compatibility tests bind it to the versioned wire fixture without
// exposing the mutable table to evaluator callers.
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
	Error       string          `json:"error"`
	Code        int             `json:"code"`
	Kind        string          `json:"kind"`
	Remediation string          `json:"remediation"`
	Policy      *string         `json:"policy"`
	Command     *string         `json:"command"`
	Denial      json.RawMessage `json:"denial"`
	Recovery    json.RawMessage `json:"recovery"`
}

type cliPolicyDenial struct {
	SchemaVersion    int                     `json:"schema_version"`
	Phase            string                  `json:"phase"`
	Verbs            []string                `json:"verbs"`
	Target           cliPolicyDenialTarget   `json:"target"`
	DecidedBy        cliPolicyDenialDecision `json:"decided_by"`
	Reason           string                  `json:"reason"`
	AllowedVerbsHere []string                `json:"allowed_verbs_here"`
	Advice           string                  `json:"advice"`
	PolicyDigest     cliPolicyDenialDigest   `json:"policy_digest"`
	PolicySource     string                  `json:"policy_source"`
	Attribute        string                  `json:"attribute,omitempty"`
	RetrySafe        bool                    `json:"retry_safe"`
}

type cliPolicyDenialTarget struct {
	Service     string   `json:"service"`
	Kind        string   `json:"kind"`
	ID          string   `json:"id,omitempty"`
	Project     string   `json:"project,omitempty"`
	Key         string   `json:"key,omitempty"`
	Space       string   `json:"space,omitempty"`
	AncestorIDs []string `json:"ancestor_ids,omitempty"`
}

type cliPolicyDenialDecision struct {
	Layer  string  `json:"layer"`
	RuleID *string `json:"rule_id"`
	Effect string  `json:"effect"`
}

type cliPolicyDenialDigest struct {
	Managed *string `json:"managed"`
	User    *string `json:"user"`
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
	// Recovery was added to the CLI error line after the frozen three-field
	// benchmark record. Admit legacy lines that lack it; whenever it is present,
	// validate the entire closed object before discarding it.
	if body.Recovery != nil {
		if !validCLIErrorRecoveryJSON(body.Recovery) {
			return CLIErrorContract{}, false
		}
	}
	// The read-only policy members travel together with their own kind. A line
	// that carries one of them in any other combination is not the reviewed CLI
	// error body and is refused before classification.
	if body.Kind == "read_only_policy" {
		if body.Policy == nil || *body.Policy != "read_only" || body.Command == nil || *body.Command == "" {
			return CLIErrorContract{}, false
		}
		if body.Denial != nil {
			return CLIErrorContract{}, false
		}
	} else if body.Kind == "content_policy" {
		if body.Policy == nil || *body.Policy != "content" || body.Denial == nil || body.Command == nil || *body.Command == "" {
			return CLIErrorContract{}, false
		}
		if !validCLIPolicyDenialJSON(body.Denial) {
			return CLIErrorContract{}, false
		}
	} else if body.Policy != nil || body.Command != nil || body.Denial != nil {
		return CLIErrorContract{}, false
	}
	return ValidateCLIErrorContract(exitCode, body.Kind, body.Remediation)
}

func validCLIPolicyDenialJSON(data []byte) bool {
	if validateJSONNoDuplicateKeys(data) != nil || !jsonObjectHasMembers(data,
		"schema_version", "phase", "verbs", "target", "decided_by", "reason", "allowed_verbs_here", "advice", "policy_digest", "policy_source", "retry_safe") {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var denial cliPolicyDenial
	if decoder.Decode(&denial) != nil || decoder.Decode(new(any)) != io.EOF || denial.SchemaVersion != 1 {
		return false
	}
	if denial.Phase != "preflight" && denial.Phase != "resolved" {
		return false
	}
	reasons := map[string]bool{
		"no_matching_allow": true, "explicit_deny": true, "scope_unresolved": true,
		"scope_unavailable": true, "scope_contradiction": true, "protected_subtree_detached": true,
		"contained_content_denied": true, "policy_required_but_absent": true,
		"policy_digest_mismatch": true, "backend_mismatch": true,
	}
	adviceByReason := map[string]string{
		"no_matching_allow": "out_of_scope", "explicit_deny": "out_of_scope",
		"scope_unresolved": "no_retry", "scope_unavailable": "wait_then_retry", "scope_contradiction": "no_retry",
		"protected_subtree_detached": "narrow_scope", "contained_content_denied": "narrow_scope",
		"policy_required_but_absent": "no_retry", "policy_digest_mismatch": "no_retry", "backend_mismatch": "no_retry",
	}
	sources := map[string]bool{"env_inline": true, "env_file": true, "config_dir": true, "required": true}
	if !reasons[denial.Reason] || denial.Advice != adviceByReason[denial.Reason] || !sources[denial.PolicySource] {
		return false
	}
	if denial.RetrySafe != (denial.Reason == "scope_unavailable") {
		return false
	}
	if !validCLIPolicyVerbSet(denial.Verbs, true) || !validCLIPolicyVerbSet(denial.AllowedVerbsHere, true) {
		return false
	}
	var nested struct {
		Target       json.RawMessage `json:"target"`
		DecidedBy    json.RawMessage `json:"decided_by"`
		PolicyDigest json.RawMessage `json:"policy_digest"`
	}
	if json.Unmarshal(data, &nested) != nil ||
		!jsonObjectHasMembers(nested.Target, "service", "kind") ||
		!jsonObjectHasMembers(nested.DecidedBy, "layer", "rule_id", "effect") ||
		!jsonObjectHasMembers(nested.PolicyDigest, "managed", "user") {
		return false
	}
	if !map[string]bool{"managed": true, "user": true, "required": true}[denial.DecidedBy.Layer] ||
		!map[string]bool{"deny": true, "default_deny": true, "scope_error": true, "source_error": true}[denial.DecidedBy.Effect] ||
		!validOptionalPolicyDigest(denial.PolicyDigest.Managed) || !validOptionalPolicyDigest(denial.PolicyDigest.User) {
		return false
	}
	return true
}

func validOptionalPolicyDigest(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) != len("sha256:")+64 || !strings.HasPrefix(*value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(*value, "sha256:") {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func jsonObjectHasMembers(data []byte, required ...string) bool {
	var members map[string]json.RawMessage
	if json.Unmarshal(data, &members) != nil {
		return false
	}
	for _, name := range required {
		if _, ok := members[name]; !ok {
			return false
		}
	}
	return true
}

func validCLIPolicyVerbSet(values []string, allowEmpty bool) bool {
	if len(values) == 0 {
		return allowEmpty
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !map[string]bool{"create": true, "update": true, "comment": true, "transition": true, "move": true, "delete": true}[value] {
			return false
		}
		seen[value] = true
	}
	return true
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
