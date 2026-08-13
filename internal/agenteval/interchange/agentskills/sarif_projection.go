package agentskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
)

const (
	// SARIFVersion is the only SARIF generation accepted by this projection.
	SARIFVersion = "2.1.0"
	// SARIFSchema is a fixed public schema identifier, never caller input.
	SARIFSchema                 = "https://json.schemastore.org/sarif-2.1.0.json"
	SARIFMaxBytes               = 4 << 20
	SARIFMaxResults             = lifecycleSecurityMaxFindings + 2
	sarifToolName               = "atl-agent-eval"
	sarifToolVersion            = "1"
	sarifCoverageIncompleteRule = "lifecycle-security/coverage-incomplete"
)

// SARIFProjection is a bounded, read-only SARIF 2.1.0 document. The
// structure and security records remain authoritative; this type contains
// only their closed CI-facing projection.
type SARIFProjection struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []SARIFRun `json:"runs"`
}

// SARIFReport is the descriptive alias used by callers that prefer report
// terminology over projection terminology.
type SARIFReport = SARIFProjection

type SARIFRun struct {
	Tool       SARIFTool      `json:"tool"`
	Results    []SARIFResult  `json:"results,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type SARIFTool struct {
	Driver SARIFDriver `json:"driver"`
}

type SARIFDriver struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Rules   []SARIFRule `json:"rules"`
}

type SARIFRule struct {
	ID               string         `json:"id"`
	ShortDescription SARIFMessage   `json:"shortDescription"`
	Properties       map[string]any `json:"properties,omitempty"`
}

type SARIFResult struct {
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level"`
	Message    SARIFMessage    `json:"message"`
	Locations  []SARIFLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties,omitempty"`
}

type SARIFMessage struct {
	Text string `json:"text"`
}

type SARIFLocation struct {
	PhysicalLocation SARIFPhysicalLocation `json:"physicalLocation"`
}

type SARIFPhysicalLocation struct {
	ArtifactLocation SARIFArtifactLocation `json:"artifactLocation"`
}

type SARIFArtifactLocation struct {
	URI string `json:"uri"`
}

// ProjectSARIF converts one independently validated structural/security
// admission into a deterministic SARIF document. It does not scan, execute,
// upload, or reinterpret a policy decision.
func ProjectSARIF(admission LifecycleSecurityAdmission) (SARIFProjection, error) {
	if err := validateSARIFAdmission(admission); err != nil {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	results := make([]SARIFResult, 0, len(admission.Structure.Findings)+len(admission.Security.Findings)+1)
	ruleKinds := make(map[string]sarifRuleKind)
	for _, finding := range admission.Structure.Findings {
		ruleID := "structural/" + string(finding.Code)
		results = append(results, sarifStructuralResult(finding, ruleID))
		ruleKinds[ruleID] = sarifRuleKind{description: "structural admission refusal", level: "error", confidence: "deterministic"}
	}
	for _, finding := range admission.Security.Findings {
		results = append(results, sarifSecurityResult(finding))
		ruleKinds[string(finding.RuleID)] = sarifRuleKind{
			description: "lifecycle security finding", level: "error", confidence: string(finding.Confidence),
		}
	}
	if !admission.Security.Complete {
		results = append(results, SARIFResult{
			RuleID:  sarifCoverageIncompleteRule,
			Level:   "error",
			Message: SARIFMessage{Text: "lifecycle security coverage incomplete"},
			Properties: map[string]any{
				"agent-eval.blocks_execution": true,
				"agent-eval.confidence":       "deterministic",
				"agent-eval.suppressed":       false,
			},
		})
		ruleKinds[sarifCoverageIncompleteRule] = sarifRuleKind{
			description: "lifecycle security coverage incomplete", level: "error", confidence: "deterministic",
		}
	}
	if len(results) > SARIFMaxResults {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	sort.Slice(results, func(left, right int) bool {
		return sarifResultKey(results[left]) < sarifResultKey(results[right])
	})
	rules := make([]SARIFRule, 0, len(ruleKinds))
	for ruleID, kind := range ruleKinds {
		rules = append(rules, SARIFRule{
			ID: ruleID, ShortDescription: SARIFMessage{Text: kind.description},
			Properties: map[string]any{
				"agent-eval.confidence": kind.confidence,
				"agent-eval.level":      kind.level,
			},
		})
	}
	sort.Slice(rules, func(left, right int) bool { return rules[left].ID < rules[right].ID })
	run := SARIFRun{
		Tool:    SARIFTool{Driver: SARIFDriver{Name: sarifToolName, Version: sarifToolVersion, Rules: rules}},
		Results: results,
		Properties: map[string]any{
			"agent-eval.blocks_execution":      admission.BlocksExecution(),
			"agent-eval.bundle_sha256":         admission.Security.BundleSHA256,
			"agent-eval.policy_sha256":         admission.Structure.PolicySHA256,
			"agent-eval.rule_pack_id":          admission.Security.RulePackID,
			"agent-eval.rule_pack_sha256":      admission.Security.RulePackSHA256,
			"agent-eval.rule_pack_version":     admission.Security.RulePackVersion,
			"agent-eval.security_complete":     admission.Security.Complete,
			"agent-eval.security_version":      admission.Security.Version,
			"agent-eval.structure_tree_sha256": admission.Structure.TreeSHA256,
			"agent-eval.structure_version":     admission.Structure.Version,
			"agent-eval.runtime_safety_proven": admission.RuntimeSafetyProven,
		},
	}
	return SARIFProjection{Schema: SARIFSchema, Version: SARIFVersion, Runs: []SARIFRun{run}}, nil
}

// EncodeSARIF serializes a validated projection with stable field order,
// fixed UTF-8 JSON, and a single trailing newline.
func EncodeSARIF(report SARIFProjection) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, contractError(ErrorInvalidProjection, nil)
	}
	data, err := json.Marshal(report)
	if err != nil || len(data)+1 > SARIFMaxBytes {
		return nil, contractError(ErrorInvalidProjection, nil)
	}
	return append(data, '\n'), nil
}

// DecodeSARIF accepts only the canonical bytes emitted by EncodeSARIF. It is
// intentionally strict so a future/extended SARIF document cannot be treated
// as an evaluator-owned security report without an explicit schema revision.
func DecodeSARIF(reader io.Reader) (SARIFProjection, error) {
	if reader == nil {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	limited := &io.LimitedReader{R: reader, N: SARIFMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < 2 || len(data) > SARIFMaxBytes || data[len(data)-1] != '\n' {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	var report SARIFProjection
	decoder := json.NewDecoder(bytes.NewReader(data[:len(data)-1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	normalizeSARIFNumericProperties(&report)
	canonical, err := EncodeSARIF(report)
	if err != nil || !bytes.Equal(data, canonical) {
		return SARIFProjection{}, contractError(ErrorInvalidProjection, nil)
	}
	return report, nil
}

func normalizeSARIFNumericProperties(report *SARIFProjection) {
	if report == nil || len(report.Runs) != 1 {
		return
	}
	run := &report.Runs[0]
	for key, value := range run.Properties {
		if _, ok := sarifRunPropertyNames[key]; !ok {
			continue
		}
		if number, ok := value.(float64); ok && number >= 0 && number <= float64(^uint32(0)) && number == float64(uint32(number)) {
			run.Properties[key] = uint32(number)
		}
	}
}

// Validate checks the closed projection vocabulary and canonical ordering.
// It intentionally does not accept arbitrary SARIF extensions or diagnostics.
func (report SARIFProjection) Validate() error {
	if report.Schema != SARIFSchema || report.Version != SARIFVersion || len(report.Runs) != 1 {
		return errors.New("invalid sarif envelope")
	}
	run := report.Runs[0]
	if run.Tool.Driver.Name != sarifToolName || run.Tool.Driver.Version != sarifToolVersion ||
		len(run.Results) > SARIFMaxResults || len(run.Results) != len(uniqueSARIFResults(run.Results)) {
		return errors.New("invalid sarif run")
	}
	if err := validateSARIFProperties(run.Properties, sarifRunPropertyNames); err != nil {
		return err
	}
	if err := requireSARIFProperties(run.Properties, sarifRunPropertyNames); err != nil {
		return err
	}
	if !sameSARIFProperty(run.Properties, "agent-eval.rule_pack_id", LifecycleSecurityRulePackID) ||
		!sameSARIFProperty(run.Properties, "agent-eval.rule_pack_version", uint32(LifecycleSecurityRulePackVersion)) ||
		!sameSARIFProperty(run.Properties, "agent-eval.security_version", uint32(LifecycleSecurityAdmissionVersion)) ||
		!sameSARIFProperty(run.Properties, "agent-eval.structure_version", uint32(StructuralAdmissionVersion)) ||
		!sameSARIFProperty(run.Properties, "agent-eval.runtime_safety_proven", false) {
		return errors.New("invalid sarif run identities")
	}
	bundle, bundleOK := run.Properties["agent-eval.bundle_sha256"].(string)
	tree, treeOK := run.Properties["agent-eval.structure_tree_sha256"].(string)
	if bundleOK && treeOK && bundle != "" && tree != "" && bundle != tree {
		return errors.New("sarif bundle and structure identities disagree")
	}
	for _, key := range []string{"agent-eval.policy_sha256", "agent-eval.rule_pack_sha256"} {
		value, ok := run.Properties[key].(string)
		if !ok || !validDigest(value) {
			return errors.New("invalid sarif required digest")
		}
	}
	if len(run.Tool.Driver.Rules) != len(uniqueSARIFRules(run.Tool.Driver.Rules)) {
		return errors.New("duplicate sarif rules")
	}
	for index, rule := range run.Tool.Driver.Rules {
		if index > 0 && run.Tool.Driver.Rules[index-1].ID >= rule.ID {
			return errors.New("sarif rules are not sorted")
		}
		if !validSARIFRuleID(rule.ID) || rule.ShortDescription.Text == "" || strings.ContainsAny(rule.ShortDescription.Text, "\r\n") {
			return errors.New("invalid sarif rule")
		}
		if err := validateSARIFProperties(rule.Properties, sarifRulePropertyNames); err != nil {
			return err
		}
		if err := requireSARIFProperties(rule.Properties, sarifRulePropertyNames); err != nil {
			return err
		}
		if err := validateSARIFRuleDefinition(rule); err != nil {
			return err
		}
	}
	rules := make(map[string]struct{}, len(run.Tool.Driver.Rules))
	for _, rule := range run.Tool.Driver.Rules {
		rules[rule.ID] = struct{}{}
	}
	for index, result := range run.Results {
		if index > 0 && sarifResultKey(run.Results[index-1]) >= sarifResultKey(result) {
			return errors.New("sarif results are not sorted")
		}
		if _, ok := rules[result.RuleID]; !ok || !validSARIFRuleID(result.RuleID) ||
			(result.Level != "error" && result.Level != "warning" && result.Level != "note") ||
			result.Message.Text == "" || strings.ContainsAny(result.Message.Text, "\r\n") {
			return errors.New("invalid sarif result")
		}
		if len(result.Locations) > 1 {
			return errors.New("too many sarif locations")
		}
		for _, location := range result.Locations {
			if !validSARIFLocation(location.PhysicalLocation.ArtifactLocation.URI) {
				return errors.New("invalid sarif location")
			}
		}
		if err := validateSARIFProperties(result.Properties, sarifResultPropertyNames); err != nil {
			return err
		}
		if err := validateSARIFResultDefinition(result); err != nil {
			return err
		}
	}
	return nil
}

type sarifRuleKind struct {
	description string
	level       string
	confidence  string
}

var sarifRunPropertyNames = map[string]sarifPropertyType{
	"agent-eval.blocks_execution":      sarifBoolProperty,
	"agent-eval.bundle_sha256":         sarifDigestProperty,
	"agent-eval.policy_sha256":         sarifDigestProperty,
	"agent-eval.rule_pack_id":          sarifStringProperty,
	"agent-eval.rule_pack_sha256":      sarifDigestProperty,
	"agent-eval.rule_pack_version":     sarifUintProperty,
	"agent-eval.runtime_safety_proven": sarifBoolProperty,
	"agent-eval.security_complete":     sarifBoolProperty,
	"agent-eval.security_version":      sarifUintProperty,
	"agent-eval.structure_tree_sha256": sarifDigestProperty,
	"agent-eval.structure_version":     sarifUintProperty,
}

var sarifRulePropertyNames = map[string]sarifPropertyType{
	"agent-eval.confidence": sarifStringProperty,
	"agent-eval.level":      sarifStringProperty,
}

var sarifResultPropertyNames = map[string]sarifPropertyType{
	"agent-eval.blocks_execution": sarifBoolProperty,
	"agent-eval.class":            sarifStringProperty,
	"agent-eval.confidence":       sarifStringProperty,
	"agent-eval.evidence":         sarifStringProperty,
	"agent-eval.severity":         sarifStringProperty,
	"agent-eval.suppressed":       sarifBoolProperty,
}

type sarifPropertyType uint8

const (
	sarifStringProperty sarifPropertyType = iota
	sarifBoolProperty
	sarifUintProperty
	sarifDigestProperty
)

func validateSARIFProperties(properties map[string]any, allowed map[string]sarifPropertyType) error {
	for key, value := range properties {
		kind, ok := allowed[key]
		if !ok {
			return errors.New("unknown sarif property")
		}
		switch kind {
		case sarifStringProperty:
			text, ok := value.(string)
			if !ok || text == "" || strings.ContainsAny(text, "\r\n") {
				return errors.New("invalid sarif string property")
			}
		case sarifBoolProperty:
			if _, ok := value.(bool); !ok {
				return errors.New("invalid sarif boolean property")
			}
		case sarifUintProperty:
			switch value.(type) {
			case uint32, uint64:
			case int:
				if value.(int) < 0 {
					return errors.New("invalid sarif integer property")
				}
			default:
				return errors.New("invalid sarif integer property")
			}
		case sarifDigestProperty:
			text, ok := value.(string)
			if !ok || (text != "" && !validDigest(text)) {
				return errors.New("invalid sarif digest property")
			}
		}
	}
	return nil
}

func requireSARIFProperties(properties map[string]any, allowed map[string]sarifPropertyType) error {
	for key := range allowed {
		if _, ok := properties[key]; !ok {
			return errors.New("missing sarif property")
		}
	}
	return nil
}

func validateSARIFAdmission(admission LifecycleSecurityAdmission) error {
	if err := validateStructuralForSARIF(admission.Structure); err != nil || admission.RuntimeSafetyProven {
		return errors.New("invalid structural admission")
	}
	security := admission.Security
	if security.Version != LifecycleSecurityAdmissionVersion || security.RulePackID != LifecycleSecurityRulePackID ||
		security.RulePackVersion != LifecycleSecurityRulePackVersion || !validDigest(security.RulePackSHA256) ||
		!validDigest(security.PolicySHA256) || security.RulePackSHA256 != lifecycleSecurityRulePackSHA256() {
		return errors.New("invalid security generation")
	}
	if admission.Structure.Admitted {
		if !validDigest(security.BundleSHA256) || security.BundleSHA256 != admission.Structure.TreeSHA256 {
			return errors.New("security bundle is not structure-bound")
		}
	} else if security.BundleSHA256 != "" || len(security.Coverage) != 0 || len(security.Findings) != 0 || security.Complete {
		return errors.New("refused structure has security data")
	}
	if len(security.Coverage) > MaxTreeEntries || len(security.Findings) > lifecycleSecurityMaxFindings {
		return errors.New("security bounds exceeded")
	}
	if err := validateSARIFCoverage(security.Coverage, security.Complete); err != nil {
		return err
	}
	if err := validateSARIFSecurityCoverageBinding(admission.Structure, security.Coverage); err != nil {
		return err
	}
	if err := validateSARIFFindings(security.Findings); err != nil {
		return err
	}
	coverageLocations := make(map[string]struct{}, len(security.Coverage))
	for _, item := range security.Coverage {
		coverageLocations[item.Location] = struct{}{}
	}
	for _, finding := range security.Findings {
		if _, ok := coverageLocations[finding.Location]; !ok {
			return errors.New("security finding is not coverage-bound")
		}
	}
	return nil
}

func validateStructuralForSARIF(admission StructuralAdmission) error {
	if admission.Version != StructuralAdmissionVersion || admission.PolicySHA256 != digestStructuralPolicy(admission.Limits) ||
		admission.RuntimeSafetyProven || !validDigest(admission.PolicySHA256) {
		return errors.New("invalid structural generation")
	}
	if !admission.Admitted {
		if admission.TreeSHA256 != "" || len(admission.Entries) != 0 || len(admission.Findings) != 1 {
			return errors.New("invalid structural refusal")
		}
		return validateStructuralFinding(admission.Findings[0])
	}
	if !validDigest(admission.TreeSHA256) || len(admission.Findings) != 0 || len(admission.Entries) > int(admission.Limits.MaxEntries) || len(admission.Entries) > MaxTreeEntries ||
		admission.BlocksExecution() {
		return errors.New("invalid structural admission")
	}
	previous := ""
	var totalBytes uint64
	for _, entry := range admission.Entries {
		if previous != "" && previous >= entry.Location {
			return errors.New("structural entries are not sorted")
		}
		previous = entry.Location
		if !validSARIFLocation(entry.Location) || !validDigest(entry.EntrySHA256) || entry.EntrySHA256 != digestStructuralEntry(entry) {
			return errors.New("invalid structural entry")
		}
		relative := sarifRelativeLocation(entry.Location)
		if structuralPathBytes(relative) > admission.Limits.MaxPathBytes || structuralEntryDepth(relative) > admission.Limits.MaxDepth {
			return errors.New("structural entry exceeds limits")
		}
		switch entry.Kind {
		case StructuralEntryDirectory:
			if entry.ModeClass != StructuralModeDirectory || entry.ContentSHA256 != "" || entry.SizeBytes != 0 || entry.Executable {
				return errors.New("invalid structural directory")
			}
		case StructuralEntryRegular:
			if !validDigest(entry.ContentSHA256) || entry.SizeBytes > admission.Limits.MaxFileBytes ||
				(entry.ModeClass != StructuralModeRegular && entry.ModeClass != StructuralModeExecutableRegular) ||
				entry.Executable != (entry.ModeClass == StructuralModeExecutableRegular) {
				return errors.New("invalid structural file")
			}
			if totalBytes > admission.Limits.MaxTreeBytes || entry.SizeBytes > admission.Limits.MaxTreeBytes-totalBytes {
				return errors.New("structural tree exceeds limits")
			}
			totalBytes += entry.SizeBytes
		default:
			return errors.New("unknown structural entry")
		}
	}
	if digestStructuralTree(admission.PolicySHA256, admission.Entries) != admission.TreeSHA256 {
		return errors.New("structural tree is not canonical")
	}
	return nil
}

func validateStructuralFinding(finding StructuralFinding) error {
	if !validSARIFLocation(finding.Location) {
		return errors.New("invalid structural finding location")
	}
	switch finding.Code {
	case FindingInvalidRoot, FindingRootSymlink, FindingEntrySymlink, FindingSpecialFile,
		FindingInvalidLocation, FindingDuplicateFileIdentity, FindingEntryCountLimit,
		FindingEntryDepthLimit, FindingPathBytesLimit, FindingFileBytesLimit, FindingTreeBytesLimit,
		FindingEntryUnreadable, FindingMountBoundary, FindingPlatformUnsupported, FindingRootChanged,
		FindingEntryChanged, FindingTreeChanged, FindingSkillManifestMissing, FindingSkillManifestInvalid,
		FindingEvalManifestMissing, FindingEvalManifestInvalid, FindingEvalReferenceMissing:
	default:
		return errors.New("unknown structural finding")
	}
	if finding.Class != FindingPolicyRefusal && finding.Class != FindingSourceInstability {
		return errors.New("unknown structural finding class")
	}
	if structuralFindingClass(finding.Code) != finding.Class {
		return errors.New("structural finding class mismatch")
	}
	return nil
}

func validateSARIFCoverage(coverage []LifecycleSecurityCoverage, complete bool) error {
	previous := ""
	for _, item := range coverage {
		if previous != "" && previous >= item.Location {
			return errors.New("security coverage is not sorted")
		}
		previous = item.Location
		if !validSARIFLocation(item.Location) || !validDigest(item.ContentSHA256) || item.SizeBytes > MaxFileBytes {
			return errors.New("invalid security coverage")
		}
		switch item.Status {
		case LifecycleSecurityCoverageScannedText:
			want, ok := classifyLifecycleSecurityFile(item.Location)
			if !ok || item.FileType != want {
				return errors.New("invalid scanned coverage")
			}
		case LifecycleSecurityCoverageUnsupportedBinary:
			if item.FileType != "" || complete {
				return errors.New("invalid binary coverage")
			}
		case LifecycleSecurityCoverageUnsupportedFileType:
			if item.FileType != "" || complete {
				return errors.New("invalid file-type coverage")
			}
		case LifecycleSecurityCoverageUnsupportedLimit:
			if item.FileType == "" || complete {
				return errors.New("invalid limited coverage")
			}
		default:
			return errors.New("unknown security coverage")
		}
	}
	if complete {
		for _, item := range coverage {
			if item.Status != LifecycleSecurityCoverageScannedText {
				return errors.New("complete security report has unsupported coverage")
			}
		}
	}
	return nil
}

func validateSARIFSecurityCoverageBinding(structure StructuralAdmission, coverage []LifecycleSecurityCoverage) error {
	expected := make(map[string]StructuralEntry)
	for _, entry := range structure.Entries {
		if entry.Kind == StructuralEntryRegular {
			expected[entry.Location] = entry
		}
	}
	if len(coverage) != len(expected) {
		return errors.New("security coverage does not cover structural files")
	}
	for _, item := range coverage {
		entry, ok := expected[item.Location]
		if !ok || item.ContentSHA256 != entry.ContentSHA256 || item.SizeBytes != entry.SizeBytes {
			return errors.New("security coverage is not structure-bound")
		}
		delete(expected, item.Location)
	}
	if len(expected) != 0 {
		return errors.New("security coverage omitted structural files")
	}
	return nil
}

func validateSARIFFindings(findings []LifecycleSecurityFinding) error {
	previous := ""
	for _, finding := range findings {
		key := lifecycleSecurityFindingKey(finding)
		if previous != "" && previous >= key {
			return errors.New("security findings are not sorted")
		}
		previous = key
		descriptor, ok := lifecycleSecurityRuleDescriptor(finding.RuleID, finding.Evidence)
		if !ok || descriptor.severity != finding.Severity || descriptor.confidence != finding.Confidence ||
			!validSARIFLocation(finding.Location) {
			return errors.New("invalid security finding")
		}
	}
	return nil
}

func validSARIFRuleID(value string) bool {
	if value == sarifCoverageIncompleteRule {
		return true
	}
	if strings.HasPrefix(value, "structural/") {
		value = strings.TrimPrefix(value, "structural/")
		switch StructuralFindingCode(value) {
		case FindingInvalidRoot, FindingRootSymlink, FindingEntrySymlink, FindingSpecialFile,
			FindingInvalidLocation, FindingDuplicateFileIdentity, FindingEntryCountLimit,
			FindingEntryDepthLimit, FindingPathBytesLimit, FindingFileBytesLimit, FindingTreeBytesLimit,
			FindingEntryUnreadable, FindingMountBoundary, FindingPlatformUnsupported, FindingRootChanged,
			FindingEntryChanged, FindingTreeChanged, FindingSkillManifestMissing, FindingSkillManifestInvalid,
			FindingEvalManifestMissing, FindingEvalManifestInvalid, FindingEvalReferenceMissing:
			return true
		default:
			return false
		}
	}
	for _, spec := range lifecycleSecurityRuleSpecs {
		if value == string(spec.id) {
			return true
		}
	}
	return false
}

func validSARIFLocation(value string) bool {
	for _, namespace := range []string{"skill", "evals", "previous"} {
		if value == namespace {
			return true
		}
		prefix := namespace + "/"
		if strings.HasPrefix(value, prefix) {
			return validSourcePath(strings.TrimPrefix(value, prefix))
		}
	}
	return false
}

func sarifRelativeLocation(value string) string {
	for _, namespace := range []string{"skill", "evals", "previous"} {
		if value == namespace {
			return "."
		}
		prefix := namespace + "/"
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func validateSARIFRuleDefinition(rule SARIFRule) error {
	want := sarifRuleKindForID(rule.ID)
	if want.description == "" || rule.ShortDescription.Text != want.description ||
		!sameSARIFProperty(rule.Properties, "agent-eval.level", "error") ||
		!sameSARIFProperty(rule.Properties, "agent-eval.level", want.level) ||
		!sameSARIFProperty(rule.Properties, "agent-eval.confidence", want.confidence) {
		return errors.New("invalid sarif rule definition")
	}
	return nil
}

func validateSARIFResultDefinition(result SARIFResult) error {
	if result.RuleID == sarifCoverageIncompleteRule {
		if result.Level != "error" || result.Message.Text != "lifecycle security coverage incomplete" || len(result.Locations) != 0 ||
			!sameSARIFProperty(result.Properties, "agent-eval.blocks_execution", true) ||
			!sameSARIFProperty(result.Properties, "agent-eval.confidence", "deterministic") ||
			!sameSARIFProperty(result.Properties, "agent-eval.suppressed", false) {
			return errors.New("invalid sarif incomplete result")
		}
		return nil
	}
	if strings.HasPrefix(result.RuleID, "structural/") {
		if result.Level != "error" || result.Message.Text != "structural admission refusal" || len(result.Locations) != 1 ||
			!sameSARIFProperty(result.Properties, "agent-eval.blocks_execution", true) ||
			!sameSARIFProperty(result.Properties, "agent-eval.confidence", "deterministic") ||
			!sameSARIFProperty(result.Properties, "agent-eval.severity", "error") ||
			!sameSARIFProperty(result.Properties, "agent-eval.suppressed", false) {
			return errors.New("invalid sarif structural result")
		}
		class, ok := result.Properties["agent-eval.class"].(string)
		if !ok || (class != string(FindingPolicyRefusal) && class != string(FindingSourceInstability)) {
			return errors.New("invalid sarif structural class")
		}
		return nil
	}
	if result.Level != "error" || result.Message.Text != "lifecycle security finding" || len(result.Locations) != 1 ||
		!sameSARIFProperty(result.Properties, "agent-eval.class", "lifecycle_security") {
		return errors.New("invalid sarif security result")
	}
	severity, severityOK := result.Properties["agent-eval.severity"].(string)
	confidence, confidenceOK := result.Properties["agent-eval.confidence"].(string)
	evidence, evidenceOK := result.Properties["agent-eval.evidence"].(string)
	suppressed, suppressedOK := result.Properties["agent-eval.suppressed"].(bool)
	if !severityOK || !confidenceOK || !evidenceOK || !suppressedOK ||
		!sameSARIFProperty(result.Properties, "agent-eval.blocks_execution", !suppressed) {
		return errors.New("invalid sarif security properties")
	}
	descriptor, ok := lifecycleSecurityRuleDescriptor(LifecycleSecurityRuleID(result.RuleID), LifecycleSecurityEvidence(evidence))
	if !ok || severity != string(descriptor.severity) || confidence != string(descriptor.confidence) {
		return errors.New("invalid sarif security vocabulary")
	}
	return nil
}

func sarifRuleKindForID(ruleID string) sarifRuleKind {
	if ruleID == sarifCoverageIncompleteRule {
		return sarifRuleKind{description: "lifecycle security coverage incomplete", level: "error", confidence: "deterministic"}
	}
	if strings.HasPrefix(ruleID, "structural/") {
		return sarifRuleKind{description: "structural admission refusal", level: "error", confidence: "deterministic"}
	}
	for _, spec := range lifecycleSecurityRuleSpecs {
		if ruleID == string(spec.id) {
			return sarifRuleKind{description: "lifecycle security finding", level: "error", confidence: string(spec.confidence)}
		}
	}
	return sarifRuleKind{}
}

func sameSARIFProperty(properties map[string]any, key string, want any) bool {
	value, ok := properties[key]
	return ok && reflect.DeepEqual(value, want)
}

func sarifStructuralResult(finding StructuralFinding, ruleID string) SARIFResult {
	return SARIFResult{
		RuleID: ruleID, Level: "error", Message: SARIFMessage{Text: "structural admission refusal"},
		Locations: []SARIFLocation{{PhysicalLocation: SARIFPhysicalLocation{ArtifactLocation: SARIFArtifactLocation{URI: finding.Location}}}},
		Properties: map[string]any{
			"agent-eval.blocks_execution": true,
			"agent-eval.class":            string(finding.Class),
			"agent-eval.confidence":       "deterministic",
			"agent-eval.severity":         "error",
			"agent-eval.suppressed":       false,
		},
	}
}

func sarifSecurityResult(finding LifecycleSecurityFinding) SARIFResult {
	return SARIFResult{
		RuleID: string(finding.RuleID), Level: "error", Message: SARIFMessage{Text: "lifecycle security finding"},
		Locations: []SARIFLocation{{PhysicalLocation: SARIFPhysicalLocation{ArtifactLocation: SARIFArtifactLocation{URI: finding.Location}}}},
		Properties: map[string]any{
			"agent-eval.blocks_execution": !finding.Suppressed,
			"agent-eval.class":            "lifecycle_security",
			"agent-eval.confidence":       string(finding.Confidence),
			"agent-eval.evidence":         string(finding.Evidence),
			"agent-eval.severity":         string(finding.Severity),
			"agent-eval.suppressed":       finding.Suppressed,
		},
	}
}

func sarifResultKey(result SARIFResult) string {
	location := ""
	if len(result.Locations) != 0 {
		location = result.Locations[0].PhysicalLocation.ArtifactLocation.URI
	}
	evidence := ""
	if value, ok := result.Properties["agent-eval.evidence"].(string); ok {
		evidence = value
	}
	return strings.Join([]string{result.RuleID, location, evidence}, "\x00")
}

func uniqueSARIFResults(results []SARIFResult) []SARIFResult {
	seen := make(map[string]struct{}, len(results))
	unique := make([]SARIFResult, 0, len(results))
	for _, result := range results {
		key := sarifResultKey(result)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			unique = append(unique, result)
		}
	}
	return unique
}

func uniqueSARIFRules(rules []SARIFRule) []SARIFRule {
	seen := make(map[string]struct{}, len(rules))
	unique := make([]SARIFRule, 0, len(rules))
	for _, rule := range rules {
		if _, ok := seen[rule.ID]; !ok {
			seen[rule.ID] = struct{}{}
			unique = append(unique, rule)
		}
	}
	return unique
}
