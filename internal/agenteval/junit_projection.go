package agenteval

// This file owns the deliberately small JUnit projection.  Canonical result
// and analysis JSON remain the source of truth; this package only renders a
// bounded CI-facing view after those artifacts have already been validated.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	JUnitSchema          = "agent-eval/junit"
	JUnitSchemaVersion   = 1
	JUnitContractVersion = StandaloneContractVersion
	JUnitProducer        = "atl-agent-eval"
	JUnitMaxBytes        = 4 << 20
	JUnitMaxTestCases    = 4096
	JUnitMaxProperties   = 4
)

// ErrInvalidJUnitInput is the content-free failure class for a malformed or
// noncanonical source artifact. Callers can classify it without exposing a
// source identity, path, provider message, or validation detail.
var ErrInvalidJUnitInput = errors.New("invalid_junit_input")

// JUnitState is the closed JUnit interpretation.  It is a projection state,
// not a replacement for a canonical evaluator status.
type JUnitState string

const (
	JUnitSuccess JUnitState = "success"
	JUnitFailure JUnitState = "failure"
	JUnitError   JUnitState = "error"
	JUnitSkipped JUnitState = "skipped"
)

// JUnitResultInput is the content-minimized portion of one already validated
// canonical Result needed for projection. Identity is used only for stable
// ordering and is never emitted. Callers should populate it from Result after
// DecodeResult/Validate has succeeded.
type JUnitResultInput struct {
	Identity        string
	SchemaVersion   int
	Status          string
	Eligibility     string
	Violations      []JUnitViolationInput
	EvidenceCovered bool
	EvidenceState   string
}

// JUnitViolationInput carries only the closed violation vocabulary and safe
// subject identity. Neither field is emitted in the XML projection.
type JUnitViolationInput struct {
	Code    string
	Subject string
}

// JUnitPairedDecisionInput is one canonical paired dimension decision. The
// Regression bit and inference status are copied, never recomputed. Pair
// coverage counts are used only to prevent incomplete evidence from becoming
// a passing test.
type JUnitPairedDecisionInput struct {
	Identity         string
	InferenceStatus  string
	Regression       bool
	CompletePairs    uint32
	ExcludedPairs    uint32
	UnsupportedPairs uint32
}

// JUnitProjectionInput is the complete bounded input to ProjectJUnit. It has
// no filesystem, process, provider, backend, network, credential, or upload
// authority.
type JUnitProjectionInput struct {
	Results   []JUnitResultInput
	Decisions []JUnitPairedDecisionInput
}

// JUnitReport is a JUnit XML document. The XML wire shape intentionally uses
// standard testsuites/testsuite/testcase elements; producer and schema
// identity live in a closed properties block so common JUnit readers remain
// compatible with the document.
type JUnitReport struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Suites   []JUnitSuite `xml:"testsuite"`
}

type JUnitSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Properties JUnitProperties `xml:"properties"`
	Testcases  []JUnitTestcase `xml:"testcase"`
}

type JUnitProperties struct {
	Properties []JUnitProperty `xml:"property"`
}

type JUnitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type JUnitTestcase struct {
	XMLName   xml.Name         `xml:"testcase"`
	Classname string           `xml:"classname,attr"`
	Name      string           `xml:"name,attr"`
	Failure   *JUnitDiagnostic `xml:"failure,omitempty"`
	Error     *JUnitDiagnostic `xml:"error,omitempty"`
	Skipped   *JUnitDiagnostic `xml:"skipped,omitempty"`
}

type JUnitDiagnostic struct {
	Message string `xml:"message,attr"`
}

type junitEntry struct {
	kind     byte
	identity string
	state    JUnitState
	message  string
}

// ProjectJUnit constructs a deterministic, privacy-bounded JUnit document.
// Input order has no effect. The function does not calculate scores or
// thresholds; it copies the state decisions supplied by canonical artifacts.
func ProjectJUnit(input JUnitProjectionInput) (JUnitReport, error) {
	total := len(input.Results) + len(input.Decisions)
	if total == 0 || total > JUnitMaxTestCases {
		return JUnitReport{}, fmt.Errorf("junit projection input exceeds bounds")
	}
	entries := make([]junitEntry, 0, total)
	for index, result := range input.Results {
		state, message, err := classifyJUnitResult(result)
		if err != nil {
			return JUnitReport{}, fmt.Errorf("result %d: %w", index, err)
		}
		entries = append(entries, junitEntry{kind: 'r', identity: result.Identity, state: state, message: message})
	}
	for index, decision := range input.Decisions {
		state, message, err := classifyJUnitDecision(decision)
		if err != nil {
			return JUnitReport{}, fmt.Errorf("paired decision %d: %w", index, err)
		}
		entries = append(entries, junitEntry{kind: 'd', identity: decision.Identity, state: state, message: message})
	}
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].kind != entries[right].kind {
			return entries[left].kind < entries[right].kind
		}
		if entries[left].identity != entries[right].identity {
			return entries[left].identity < entries[right].identity
		}
		if entries[left].state != entries[right].state {
			return entries[left].state < entries[right].state
		}
		return entries[left].message < entries[right].message
	})

	testcases := make([]JUnitTestcase, 0, len(entries))
	counts := [4]int{}
	for index, entry := range entries {
		name := fmt.Sprintf("test-%06d", index+1)
		testcase := JUnitTestcase{Classname: "agent-eval", Name: name}
		diagnostic := &JUnitDiagnostic{Message: entry.message}
		switch entry.state {
		case JUnitSuccess:
			counts[0]++
		case JUnitFailure:
			counts[1]++
			testcase.Failure = diagnostic
		case JUnitError:
			counts[2]++
			testcase.Error = diagnostic
		case JUnitSkipped:
			counts[3]++
			testcase.Skipped = diagnostic
		default:
			return JUnitReport{}, fmt.Errorf("invalid projection state")
		}
		testcases = append(testcases, testcase)
	}
	properties := []JUnitProperty{
		{Name: "agent-eval.schema", Value: JUnitSchema},
		{Name: "agent-eval.schema_version", Value: "1"},
		{Name: "agent-eval.contract_version", Value: JUnitContractVersion},
		{Name: "agent-eval.producer", Value: JUnitProducer},
	}
	suite := JUnitSuite{
		Name: "agent-eval", Tests: len(testcases), Failures: counts[1], Errors: counts[2], Skipped: counts[3],
		Properties: JUnitProperties{Properties: properties}, Testcases: testcases,
	}
	report := JUnitReport{
		Name: "agent-eval", Tests: len(testcases), Failures: counts[1], Errors: counts[2], Skipped: counts[3],
		Suites: []JUnitSuite{suite},
	}
	if err := report.Validate(); err != nil {
		return JUnitReport{}, err
	}
	return report, nil
}

// EncodeJUnit serializes one validated projection with a fixed UTF-8 header
// and no volatile timing, timestamps, paths, or free-form diagnostics.
func EncodeJUnit(report JUnitReport) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	body, err := xml.Marshal(report)
	if err != nil || len(body)+len(junitXMLHeader) > JUnitMaxBytes {
		return nil, fmt.Errorf("junit projection exceeds bounds")
	}
	encoded := append(append([]byte{}, junitXMLHeader...), body...)
	encoded = append(encoded, '\n')
	if len(encoded) > JUnitMaxBytes {
		return nil, fmt.Errorf("junit projection exceeds bounds")
	}
	return encoded, nil
}

// DecodeJUnit accepts only bytes emitted by EncodeJUnit. Canonical-byte
// comparison prevents alternate whitespace, attribute order, unknown
// attributes, and legacy partial documents from being silently accepted.
func DecodeJUnit(reader io.Reader) (JUnitReport, error) {
	if reader == nil {
		return JUnitReport{}, errors.New("invalid junit projection")
	}
	limited := &io.LimitedReader{R: reader, N: JUnitMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < len(junitXMLHeader)+2 || len(data) > JUnitMaxBytes ||
		!bytes.HasPrefix(data, junitXMLHeader) || data[len(data)-1] != '\n' {
		return JUnitReport{}, errors.New("invalid junit projection")
	}
	var report JUnitReport
	decoder := xml.NewDecoder(bytes.NewReader(data[len(junitXMLHeader):]))
	if err := decoder.Decode(&report); err != nil {
		return JUnitReport{}, errors.New("invalid junit projection")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return JUnitReport{}, errors.New("invalid junit projection")
	}
	canonical, err := EncodeJUnit(report)
	if err != nil || !bytes.Equal(data, canonical) {
		return JUnitReport{}, errors.New("noncanonical junit projection")
	}
	return report, nil
}

var junitXMLHeader = []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")

func (report JUnitReport) Validate() error {
	if (report.XMLName.Local != "" && report.XMLName.Local != "testsuites") || report.Name != "agent-eval" || len(report.Suites) != 1 || report.Tests <= 0 || report.Tests > JUnitMaxTestCases ||
		report.Failures < 0 || report.Errors < 0 || report.Skipped < 0 || report.Failures+report.Errors+report.Skipped > report.Tests {
		return errors.New("invalid junit report shape")
	}
	suite := report.Suites[0]
	if (suite.XMLName.Local != "" && suite.XMLName.Local != "testsuite") || suite.Name != "agent-eval" || suite.Tests != report.Tests || suite.Failures != report.Failures ||
		suite.Errors != report.Errors || suite.Skipped != report.Skipped || len(suite.Testcases) != report.Tests ||
		len(suite.Properties.Properties) != JUnitMaxProperties {
		return errors.New("invalid junit report shape")
	}
	wantProperties := []JUnitProperty{
		{Name: "agent-eval.schema", Value: JUnitSchema},
		{Name: "agent-eval.schema_version", Value: "1"},
		{Name: "agent-eval.contract_version", Value: JUnitContractVersion},
		{Name: "agent-eval.producer", Value: JUnitProducer},
	}
	if !equalJUnitProperties(suite.Properties.Properties, wantProperties) {
		return errors.New("invalid junit producer properties")
	}
	counts := [4]int{}
	for index, testcase := range suite.Testcases {
		if (testcase.XMLName.Local != "" && testcase.XMLName.Local != "testcase") || testcase.Classname != "agent-eval" || testcase.Name != fmt.Sprintf("test-%06d", index+1) {
			return errors.New("invalid junit testcase")
		}
		present := 0
		if testcase.Failure != nil {
			present++
			counts[1]++
			if testcase.Failure.Message != "task regression" && testcase.Failure.Message != "paired regression" {
				return errors.New("invalid junit failure diagnostic")
			}
		}
		if testcase.Error != nil {
			present++
			counts[2]++
			if testcase.Error.Message != "infrastructure or evidence unavailable" && testcase.Error.Message != "paired evidence unavailable" {
				return errors.New("invalid junit error diagnostic")
			}
		}
		if testcase.Skipped != nil {
			present++
			counts[3]++
			if testcase.Skipped.Message != "unsupported capability" && testcase.Skipped.Message != "paired capability unsupported" {
				return errors.New("invalid junit skipped diagnostic")
			}
		}
		if present > 1 {
			return errors.New("junit testcase has multiple states")
		}
		if present == 0 {
			counts[0]++
		}
	}
	if counts[1] != report.Failures || counts[2] != report.Errors || counts[3] != report.Skipped ||
		counts[0]+counts[1]+counts[2]+counts[3] != report.Tests {
		return errors.New("junit summary disagrees with testcases")
	}
	return nil
}

func equalJUnitProperties(left, right []JUnitProperty) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func classifyJUnitResult(result JUnitResultInput) (JUnitState, string, error) {
	if !validJUnitInputIdentity(result.Identity) || !validJUnitResultSchema(result.SchemaVersion) ||
		(result.Status != "pass" && result.Status != "fail" && result.Status != "ineligible") {
		return "", "", errors.New("invalid canonical result projection")
	}
	eligibility := result.Eligibility
	if eligibility == "" {
		eligibility = EligibilitySupported
	}
	if eligibility != EligibilitySupported && eligibility != EligibilityUnsupportedCapability && eligibility != EligibilityInvalidatedDrift {
		return "", "", errors.New("invalid canonical result eligibility")
	}
	if eligibility == EligibilityUnsupportedCapability {
		if result.Status != "ineligible" {
			return "", "", errors.New("unsupported result status is inconsistent")
		}
		return JUnitSkipped, "unsupported capability", nil
	}
	if eligibility != EligibilitySupported {
		return JUnitError, "infrastructure or evidence unavailable", nil
	}
	if result.Status == "pass" && !result.EvidenceCovered {
		return JUnitError, "infrastructure or evidence unavailable", nil
	}
	if result.EvidenceCovered && !validJUnitEvidenceState(result.EvidenceState) {
		return JUnitError, "infrastructure or evidence unavailable", nil
	}
	if result.EvidenceCovered && junitEvidenceFailure(result.EvidenceState) {
		return JUnitError, "infrastructure or evidence unavailable", nil
	}
	if result.Status == "pass" {
		if len(result.Violations) != 0 {
			return "", "", errors.New("passing result contains violations")
		}
		return JUnitSuccess, "", nil
	}
	if result.Status == "ineligible" || len(result.Violations) == 0 {
		return JUnitError, "infrastructure or evidence unavailable", nil
	}
	for _, violation := range result.Violations {
		if !validJUnitViolation(violation) {
			return "", "", errors.New("invalid canonical result violation")
		}
		if junitInfrastructureViolation(violation.Code) {
			return JUnitError, "infrastructure or evidence unavailable", nil
		}
	}
	return JUnitFailure, "task regression", nil
}

func classifyJUnitDecision(decision JUnitPairedDecisionInput) (JUnitState, string, error) {
	if !validJUnitInputIdentity(decision.Identity) ||
		(decision.InferenceStatus != "insufficient" && decision.InferenceStatus != "descriptive" && decision.InferenceStatus != "inferential") ||
		decision.UnsupportedPairs > decision.ExcludedPairs || decision.CompletePairs > JUnitMaxTestCases || decision.ExcludedPairs > JUnitMaxTestCases {
		return "", "", errors.New("invalid canonical paired decision")
	}
	if decision.CompletePairs == 0 && decision.ExcludedPairs > 0 && decision.UnsupportedPairs == decision.ExcludedPairs {
		return JUnitSkipped, "paired capability unsupported", nil
	}
	if decision.InferenceStatus != "inferential" || decision.CompletePairs == 0 || decision.ExcludedPairs != 0 {
		return JUnitError, "paired evidence unavailable", nil
	}
	if decision.Regression {
		return JUnitFailure, "paired regression", nil
	}
	return JUnitSuccess, "", nil
}

func junitEvidenceFailure(state string) bool {
	switch state {
	case string(EvidenceAttemptStateUnavailable), string(EvidenceAttemptStateBlocked),
		string(EvidenceAttemptStateFailed), string(EvidenceAttemptStatePartial):
		return true
	default:
		return false
	}
}

func validJUnitEvidenceState(state string) bool {
	switch state {
	case string(EvidenceAttemptStateNone), string(EvidenceAttemptStateUnavailable),
		string(EvidenceAttemptStateBlocked), string(EvidenceAttemptStateFailed),
		string(EvidenceAttemptStatePartial), string(EvidenceAttemptStateSucceeded):
		return true
	default:
		return false
	}
}

func junitInfrastructureViolation(code string) bool {
	switch code {
	case "metric_not_observed", "run_check_failed":
		return true
	default:
		return false
	}
}

func validJUnitViolation(violation JUnitViolationInput) bool {
	if !validJUnitInputIdentity(violation.Code) || !validJUnitSubject(violation.Subject) {
		return false
	}
	switch violation.Code {
	case "budget_exceeded", "http_method_not_allowed", "metric_not_observed", "required_check_failed",
		"qualitative_review_disagreement", "qualitative_review_failed", "oracle_failed", "run_check_failed", "run_cost_cap_exceeded":
		return true
	default:
		return false
	}
}

func validJUnitSubject(value string) bool {
	if validJUnitInputIdentity(value) {
		return true
	}
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validJUnitResultSchema(version int) bool {
	switch version {
	case LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion,
		LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion:
		return true
	default:
		return false
	}
}

func validJUnitInputIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n<>\"'") {
		return false
	}
	for index, char := range value {
		if index == 0 && (char < 'a' || char > 'z') {
			return false
		}
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '/' && char != '-' {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
