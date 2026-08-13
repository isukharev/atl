package agentskills

import (
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	LifecycleSecurityAdmissionVersion uint32 = 1
	LifecycleSecurityPolicyVersion    uint32 = LifecycleSecurityAdmissionVersion
	LifecycleSecurityRulePackID              = "lifecycle-security/v1"
	LifecycleSecurityRulePackVersion  uint32 = 1

	lifecycleSecurityMaxFindings     = 16384
	lifecycleSecurityMaxSuppressions = 1024
	lifecycleSecurityMaxLines        = 16384
	lifecycleSecurityMaxTokens       = 4096
	lifecycleSecurityMaxLineBytes    = 64 << 10
)

// LifecycleSecurityRuleID identifies one closed v1 static-analysis family.
// Rule IDs are stable report vocabulary, not user-supplied expressions.
type LifecycleSecurityRuleID string

const (
	LifecycleSecurityRuleCredentialLike         LifecycleSecurityRuleID = "lifecycle-security/credential-like" // #nosec G101 -- closed rule vocabulary, not a credential.
	LifecycleSecurityRulePromptCommandInjection LifecycleSecurityRuleID = "lifecycle-security/prompt-command-injection"
	LifecycleSecurityRuleUnsafeInstallUpdate    LifecycleSecurityRuleID = "lifecycle-security/unsafe-install-update"
	LifecycleSecurityRuleShellDownload          LifecycleSecurityRuleID = "lifecycle-security/shell-download"
	LifecycleSecurityRuleEscalation             LifecycleSecurityRuleID = "lifecycle-security/escalation"
	LifecycleSecurityRulePersistence            LifecycleSecurityRuleID = "lifecycle-security/persistence"
	LifecycleSecurityRuleDestructive            LifecycleSecurityRuleID = "lifecycle-security/destructive"
	LifecycleSecurityRuleNetworkDestination     LifecycleSecurityRuleID = "lifecycle-security/network-destination"
	LifecycleSecurityRuleUnboundedAuthority     LifecycleSecurityRuleID = "lifecycle-security/unbounded-authority"
	LifecycleSecurityRuleEncodedPayload         LifecycleSecurityRuleID = "lifecycle-security/encoded-payload"
)

// LifecycleSecurityEvidence is a closed, content-free evidence identity.
type LifecycleSecurityEvidence string

const (
	LifecycleSecurityEvidencePrivateKeyHeader         LifecycleSecurityEvidence = "private-key-header"
	LifecycleSecurityEvidenceTokenAssignment          LifecycleSecurityEvidence = "token-assignment"
	LifecycleSecurityEvidenceInstructionOverride      LifecycleSecurityEvidence = "instruction-override"
	LifecycleSecurityEvidenceDynamicCommandEval       LifecycleSecurityEvidence = "dynamic-command-eval"
	LifecycleSecurityEvidenceDownloadToInstaller      LifecycleSecurityEvidence = "download-to-installer"
	LifecycleSecurityEvidenceUnboundedPackageUpdate   LifecycleSecurityEvidence = "unbounded-package-update"
	LifecycleSecurityEvidenceDownloadCommand          LifecycleSecurityEvidence = "download-command"
	LifecycleSecurityEvidenceNetworkShellDevice       LifecycleSecurityEvidence = "network-shell-device"
	LifecycleSecurityEvidencePrivilegeCommand         LifecycleSecurityEvidence = "privilege-command"
	LifecycleSecurityEvidencePermissionWidening       LifecycleSecurityEvidence = "permission-widening"
	LifecycleSecurityEvidenceStartupRegistration      LifecycleSecurityEvidence = "startup-registration"
	LifecycleSecurityEvidenceSchedulerRegistration    LifecycleSecurityEvidence = "scheduler-registration"
	LifecycleSecurityEvidenceRecursiveRootDelete      LifecycleSecurityEvidence = "recursive-root-delete"
	LifecycleSecurityEvidenceDeviceOrStoreWipe        LifecycleSecurityEvidence = "device-or-store-wipe"
	LifecycleSecurityEvidenceLiteralRemoteDestination LifecycleSecurityEvidence = "literal-remote-destination"
	LifecycleSecurityEvidenceWildcardListener         LifecycleSecurityEvidence = "wildcard-listener"
	LifecycleSecurityEvidenceToolWildcardOrBypass     LifecycleSecurityEvidence = "tool-wildcard-or-bypass"
	LifecycleSecurityEvidencePrivilegedRootMount      LifecycleSecurityEvidence = "privileged-root-mount"
	LifecycleSecurityEvidenceDecodedHighRiskEvidence  LifecycleSecurityEvidence = "decoded-high-risk-evidence"
)

// LifecycleSecuritySeverity is intentionally not a user-configurable
// threshold. Every v1 finding blocks unless an exact reviewed suppression
// applies.
type LifecycleSecuritySeverity string

const (
	LifecycleSecuritySeverityHigh     LifecycleSecuritySeverity = "high"
	LifecycleSecuritySeverityCritical LifecycleSecuritySeverity = "critical"
)

// LifecycleSecurityConfidence describes matcher confidence for review and
// reporting; it never weakens the v1 blocking decision.
type LifecycleSecurityConfidence string

const (
	LifecycleSecurityConfidenceMedium LifecycleSecurityConfidence = "medium"
	LifecycleSecurityConfidenceHigh   LifecycleSecurityConfidence = "high"
)

// LifecycleSecurityCoverageStatus records one closed status for every admitted
// regular file. Unsupported content is a blocking completeness failure.
type LifecycleSecurityCoverageStatus string

const (
	LifecycleSecurityCoverageScannedText         LifecycleSecurityCoverageStatus = "scanned_text"
	LifecycleSecurityCoverageUnsupportedBinary   LifecycleSecurityCoverageStatus = "unsupported_binary"
	LifecycleSecurityCoverageUnsupportedFileType LifecycleSecurityCoverageStatus = "unsupported_file_type"
	LifecycleSecurityCoverageUnsupportedLimit    LifecycleSecurityCoverageStatus = "unsupported_limit"
)

// LifecycleSecurityFileType limits each rule pack to a closed, reviewed input
// family. Unknown extensions are reported as unsupported rather than silently
// skipped.
type LifecycleSecurityFileType string

const (
	LifecycleSecurityFileMarkdown LifecycleSecurityFileType = "markdown"
	LifecycleSecurityFileJSON     LifecycleSecurityFileType = "json"
	LifecycleSecurityFileYAML     LifecycleSecurityFileType = "yaml"
	LifecycleSecurityFileConfig   LifecycleSecurityFileType = "config"
	LifecycleSecurityFileShell    LifecycleSecurityFileType = "shell"
	LifecycleSecurityFileSource   LifecycleSecurityFileType = "source"
	LifecycleSecurityFileText     LifecycleSecurityFileType = "text"
)

type lifecycleSecurityFileTypeExtension struct {
	extension string
	fileType  LifecycleSecurityFileType
}

var lifecycleSecurityFileTypeExtensions = []lifecycleSecurityFileTypeExtension{
	{extension: ".bat", fileType: LifecycleSecurityFileShell},
	{extension: ".bash", fileType: LifecycleSecurityFileShell},
	{extension: ".c", fileType: LifecycleSecurityFileSource},
	{extension: ".cc", fileType: LifecycleSecurityFileSource},
	{extension: ".cfg", fileType: LifecycleSecurityFileConfig},
	{extension: ".cmd", fileType: LifecycleSecurityFileShell},
	{extension: ".conf", fileType: LifecycleSecurityFileConfig},
	{extension: ".cpp", fileType: LifecycleSecurityFileSource},
	{extension: ".env", fileType: LifecycleSecurityFileConfig},
	{extension: ".fish", fileType: LifecycleSecurityFileShell},
	{extension: ".go", fileType: LifecycleSecurityFileSource},
	{extension: ".h", fileType: LifecycleSecurityFileSource},
	{extension: ".ini", fileType: LifecycleSecurityFileConfig},
	{extension: ".java", fileType: LifecycleSecurityFileSource},
	{extension: ".js", fileType: LifecycleSecurityFileSource},
	{extension: ".jsx", fileType: LifecycleSecurityFileSource},
	{extension: ".json", fileType: LifecycleSecurityFileJSON},
	{extension: ".log", fileType: LifecycleSecurityFileText},
	{extension: ".markdown", fileType: LifecycleSecurityFileMarkdown},
	{extension: ".md", fileType: LifecycleSecurityFileMarkdown},
	{extension: ".pl", fileType: LifecycleSecurityFileSource},
	{extension: ".ps1", fileType: LifecycleSecurityFileShell},
	{extension: ".py", fileType: LifecycleSecurityFileSource},
	{extension: ".rb", fileType: LifecycleSecurityFileSource},
	{extension: ".rs", fileType: LifecycleSecurityFileSource},
	{extension: ".sh", fileType: LifecycleSecurityFileShell},
	{extension: ".toml", fileType: LifecycleSecurityFileConfig},
	{extension: ".ts", fileType: LifecycleSecurityFileSource},
	{extension: ".tsx", fileType: LifecycleSecurityFileSource},
	{extension: ".txt", fileType: LifecycleSecurityFileText},
	{extension: ".yaml", fileType: LifecycleSecurityFileYAML},
	{extension: ".yml", fileType: LifecycleSecurityFileYAML},
	{extension: ".zsh", fileType: LifecycleSecurityFileShell},
}

// LifecycleSecuritySuppressionRationale is a closed reason for a reviewed
// exact suppression. It intentionally has no free-form explanation field.
type LifecycleSecuritySuppressionRationale string

const (
	LifecycleSecuritySuppressionSyntheticFixture       LifecycleSecuritySuppressionRationale = "synthetic_fixture"
	LifecycleSecuritySuppressionDocumentedExample      LifecycleSecuritySuppressionRationale = "documented_example"
	LifecycleSecuritySuppressionRequiredReviewed       LifecycleSecuritySuppressionRationale = "required_reviewed_behavior"
	LifecycleSecuritySuppressionConfirmedFalsePositive LifecycleSecuritySuppressionRationale = "confirmed_false_positive"
)

// LifecycleSecurityPolicy selects the fixed v1 rule pack and supplies an
// explicit caller-owned date. No ambient clock is consulted.
type LifecycleSecurityPolicy struct {
	Version      uint32
	EffectiveOn  string
	Suppressions []LifecycleSecuritySuppression
}

// LifecycleSecuritySuppression applies to one exact logical file and one
// exact rule/evidence pair in one exact admitted bundle digest.
type LifecycleSecuritySuppression struct {
	Version         uint32
	RulePackVersion uint32
	BundleSHA256    string
	RuleID          LifecycleSecurityRuleID
	Evidence        LifecycleSecurityEvidence
	Scope           string
	Rationale       LifecycleSecuritySuppressionRationale
	ExpiresOn       string
}

// LifecycleSecurityCoverage contains no source bytes, snippets, host paths,
// URLs, or decoded payloads.
type LifecycleSecurityCoverage struct {
	Location      string
	FileType      LifecycleSecurityFileType
	Status        LifecycleSecurityCoverageStatus
	ContentSHA256 string
	SizeBytes     uint64
}

// LifecycleSecurityFinding contains only closed identities and a logical
// location. Suppressed findings remain visible for auditability.
type LifecycleSecurityFinding struct {
	RuleID     LifecycleSecurityRuleID
	Severity   LifecycleSecuritySeverity
	Confidence LifecycleSecurityConfidence
	Evidence   LifecycleSecurityEvidence
	Location   string
	Suppressed bool
}

// LifecycleSecurityReport is independently versioned from structural
// admission. BundleSHA256 equals the admitted structural TreeSHA256.
type LifecycleSecurityReport struct {
	Version         uint32
	RulePackID      string
	RulePackVersion uint32
	RulePackSHA256  string
	PolicySHA256    string
	BundleSHA256    string
	Complete        bool
	Coverage        []LifecycleSecurityCoverage
	Findings        []LifecycleSecurityFinding
}

// BlocksExecution is true for incomplete coverage or any unsuppressed
// finding. A false value means only that this static layer found no block; it
// is not runtime safety proof.
func (report LifecycleSecurityReport) BlocksExecution() bool {
	if report.Version != LifecycleSecurityAdmissionVersion || !report.Complete {
		return true
	}
	for _, finding := range report.Findings {
		if !finding.Suppressed {
			return true
		}
	}
	return false
}

// LifecycleSecurityAdmission combines structural admission with the separate
// static security report. RuntimeSafetyProven is always false by contract.
type LifecycleSecurityAdmission struct {
	Structure           StructuralAdmission
	Security            LifecycleSecurityReport
	RuntimeSafetyProven bool
}

func (admission LifecycleSecurityAdmission) BlocksExecution() bool {
	return !admission.Structure.Admitted || admission.Security.BlocksExecution()
}

// AdmitStructureWithLifecycleSecurity performs structural admission and then
// scans the same private captured bytes before releasing them. It never
// executes, resolves, downloads, invokes a provider, discovers credentials,
// or reopens a bundle-controlled path.
func AdmitStructureWithLifecycleSecurity(request ImportRequest, policy LifecycleSecurityPolicy) (LifecycleSecurityAdmission, error) {
	normalized, err := normalizeLifecycleSecurityPolicy(policy)
	if err != nil {
		return LifecycleSecurityAdmission{}, contractError(ErrorInvalidSecurityPolicy, nil)
	}
	capture, err := admitStructureCaptureWithHooks(request, structuralAdmissionHooks{})
	if err != nil {
		return LifecycleSecurityAdmission{}, err
	}
	result := LifecycleSecurityAdmission{
		Structure:           capture.admission,
		RuntimeSafetyProven: false,
		Security: LifecycleSecurityReport{
			Version:         LifecycleSecurityAdmissionVersion,
			RulePackID:      LifecycleSecurityRulePackID,
			RulePackVersion: LifecycleSecurityRulePackVersion,
			RulePackSHA256:  lifecycleSecurityRulePackSHA256(),
			PolicySHA256:    normalized.digest,
		},
	}
	if !capture.admission.Admitted {
		if len(normalized.suppressions) != 0 {
			return LifecycleSecurityAdmission{}, contractError(ErrorInvalidSecurityPolicy, nil)
		}
		return result, nil
	}
	if err := validateLifecycleSecurityBundle(normalized.suppressions, capture.admission.TreeSHA256); err != nil {
		return LifecycleSecurityAdmission{}, contractError(ErrorInvalidSecurityPolicy, nil)
	}
	result.Security.BundleSHA256 = capture.admission.TreeSHA256
	result.Security.Coverage, result.Security.Findings, result.Security.Complete = scanLifecycleSecuritySources(capture.sources)
	if err := applyLifecycleSecuritySuppressions(&result.Security, normalized.suppressions); err != nil {
		return LifecycleSecurityAdmission{}, contractError(ErrorInvalidSecurityPolicy, nil)
	}
	return result, nil
}

type normalizedLifecycleSecurityPolicy struct {
	suppressions []LifecycleSecuritySuppression
	digest       string
}

func normalizeLifecycleSecurityPolicy(policy LifecycleSecurityPolicy) (normalizedLifecycleSecurityPolicy, error) {
	if policy.Version != LifecycleSecurityPolicyVersion || !validLifecycleSecurityDate(policy.EffectiveOn) {
		return normalizedLifecycleSecurityPolicy{}, contractError(ErrorInvalidSecurityPolicy, nil)
	}
	suppressions := append([]LifecycleSecuritySuppression(nil), policy.Suppressions...)
	if len(suppressions) > lifecycleSecurityMaxSuppressions {
		return normalizedLifecycleSecurityPolicy{}, contractError(ErrorInvalidSecurityPolicy, nil)
	}
	for _, suppression := range suppressions {
		if suppression.Version != LifecycleSecurityPolicyVersion ||
			suppression.RulePackVersion != LifecycleSecurityRulePackVersion ||
			!validDigest(suppression.BundleSHA256) ||
			!validLifecycleSecurityScope(suppression.Scope) ||
			!validLifecycleSecurityDate(suppression.ExpiresOn) ||
			lifecycleSecurityDateCompare(policy.EffectiveOn, suppression.ExpiresOn) >= 0 ||
			!validLifecycleSecurityRationale(suppression.Rationale) {
			return normalizedLifecycleSecurityPolicy{}, contractError(ErrorInvalidSecurityPolicy, nil)
		}
		if _, ok := lifecycleSecurityRuleDescriptor(suppression.RuleID, suppression.Evidence); !ok {
			return normalizedLifecycleSecurityPolicy{}, contractError(ErrorInvalidSecurityPolicy, nil)
		}
	}
	sort.Slice(suppressions, func(i, j int) bool {
		return lifecycleSecuritySuppressionKey(suppressions[i]) < lifecycleSecuritySuppressionKey(suppressions[j])
	})
	for index := 1; index < len(suppressions); index++ {
		if lifecycleSecuritySuppressionMatchKey(suppressions[index-1]) == lifecycleSecuritySuppressionMatchKey(suppressions[index]) {
			return normalizedLifecycleSecurityPolicy{}, contractError(ErrorInvalidSecurityPolicy, nil)
		}
	}
	builder := newDigestBuilder("lifecycle-security-policy")
	builder.addString(strconv.FormatUint(uint64(LifecycleSecurityAdmissionVersion), 10))
	builder.addString(lifecycleSecurityRulePackSHA256())
	builder.addString(policy.EffectiveOn)
	for _, suppression := range suppressions {
		builder.addString(lifecycleSecuritySuppressionKey(suppression))
	}
	return normalizedLifecycleSecurityPolicy{suppressions: suppressions, digest: builder.sum()}, nil
}

func validateLifecycleSecurityBundle(suppressions []LifecycleSecuritySuppression, bundleSHA256 string) error {
	for _, suppression := range suppressions {
		if suppression.BundleSHA256 != bundleSHA256 {
			return contractError(ErrorInvalidSecurityPolicy, nil)
		}
	}
	return nil
}

func scanLifecycleSecuritySources(sources []structuralSource) ([]LifecycleSecurityCoverage, []LifecycleSecurityFinding, bool) {
	coverage := make([]LifecycleSecurityCoverage, 0)
	findings := make([]LifecycleSecurityFinding, 0)
	complete := true
	for _, source := range sources {
		for _, entry := range source.tree.entries {
			if entry.isDir {
				continue
			}
			location := qualifyStructuralLocation(source.namespace, entry.path)
			item := LifecycleSecurityCoverage{
				Location: location, ContentSHA256: entry.digest, SizeBytes: uint64(len(entry.data)),
			}
			if !lifecycleSecuritySupportedText(entry.data) {
				item.Status = LifecycleSecurityCoverageUnsupportedBinary
				complete = false
				coverage = append(coverage, item)
				continue
			}
			fileType, supported := classifyLifecycleSecurityFile(location)
			item.FileType = fileType
			if !supported {
				item.Status = LifecycleSecurityCoverageUnsupportedFileType
				complete = false
				coverage = append(coverage, item)
				continue
			}
			item.Status = LifecycleSecurityCoverageScannedText
			coverage = append(coverage, item)
			matches, scanComplete := scanLifecycleSecurityTextForTypeBounded(entry.data, fileType)
			if !scanComplete {
				item.Status = LifecycleSecurityCoverageUnsupportedLimit
				coverage[len(coverage)-1] = item
				complete = false
			}
			if len(findings)+len(matches) > lifecycleSecurityMaxFindings {
				item.Status = LifecycleSecurityCoverageUnsupportedLimit
				coverage[len(coverage)-1] = item
				complete = false
				continue
			}
			for _, match := range matches {
				descriptor, ok := lifecycleSecurityRuleDescriptor(match.ruleID, match.evidence)
				if !ok {
					complete = false
					continue
				}
				findings = append(findings, LifecycleSecurityFinding{
					RuleID: match.ruleID, Severity: descriptor.severity,
					Confidence: descriptor.confidence, Evidence: match.evidence, Location: location,
				})
			}
		}
	}
	sort.Slice(coverage, func(i, j int) bool { return coverage[i].Location < coverage[j].Location })
	sort.Slice(findings, func(i, j int) bool {
		return lifecycleSecurityFindingKey(findings[i]) < lifecycleSecurityFindingKey(findings[j])
	})
	return coverage, findings, complete
}

func classifyLifecycleSecurityFile(location string) (LifecycleSecurityFileType, bool) {
	name := strings.ToLower(path.Base(location))
	extension := strings.ToLower(path.Ext(name))
	for _, candidate := range lifecycleSecurityFileTypeExtensions {
		if extension == candidate.extension {
			return candidate.fileType, true
		}
	}
	return "", false
}

func lifecycleSecuritySupportedText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, value := range data {
		if value == 0 || value < 0x09 || value == 0x0b || value == 0x0c || value == 0x0e || value == 0x0f {
			return false
		}
	}
	return true
}

func applyLifecycleSecuritySuppressions(report *LifecycleSecurityReport, suppressions []LifecycleSecuritySuppression) error {
	if len(suppressions) == 0 {
		return nil
	}
	used := make([]bool, len(suppressions))
	for index := range report.Findings {
		for suppressionIndex, suppression := range suppressions {
			if report.Findings[index].Location == suppression.Scope &&
				report.Findings[index].RuleID == suppression.RuleID &&
				report.Findings[index].Evidence == suppression.Evidence {
				report.Findings[index].Suppressed = true
				used[suppressionIndex] = true
				break
			}
		}
	}
	for _, value := range used {
		if !value {
			return contractError(ErrorInvalidSecurityPolicy, nil)
		}
	}
	return nil
}

func lifecycleSecurityRulePackSHA256() string {
	builder := newDigestBuilder("lifecycle-security-rule-pack")
	builder.addString(LifecycleSecurityRulePackID)
	builder.addString(strconv.FormatUint(uint64(LifecycleSecurityRulePackVersion), 10))
	builder.addString(lifecycleSecurityMatcherRevision)
	builder.addString(lifecycleSecurityPrecisionCorpusRevision)
	builder.addString(strconv.Itoa(lifecycleSecurityMaxFindings))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxSuppressions))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxLines))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxTokens))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxLineBytes))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxDecodedCandidates))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxDecodedBytes))
	builder.addString(strconv.Itoa(lifecycleSecurityMaxTokenBytes))
	for _, mapping := range lifecycleSecurityFileTypeExtensions {
		builder.addString(mapping.extension)
		builder.addString(string(mapping.fileType))
	}
	for _, spec := range lifecycleSecurityRuleSpecs {
		builder.addString(string(spec.id))
		builder.addString(string(spec.severity))
		builder.addString(string(spec.confidence))
		for _, evidence := range spec.evidence {
			builder.addString(string(evidence))
		}
		for _, fileType := range spec.fileTypes {
			builder.addString(string(fileType))
		}
	}
	return builder.sum()
}

func lifecycleSecuritySuppressionKey(suppression LifecycleSecuritySuppression) string {
	return strings.Join([]string{
		strconv.FormatUint(uint64(suppression.Version), 10),
		strconv.FormatUint(uint64(suppression.RulePackVersion), 10), suppression.BundleSHA256,
		string(suppression.RuleID), string(suppression.Evidence), suppression.Scope,
		string(suppression.Rationale), suppression.ExpiresOn,
	}, "\x00")
}

func lifecycleSecuritySuppressionMatchKey(suppression LifecycleSecuritySuppression) string {
	return strings.Join([]string{suppression.BundleSHA256, string(suppression.RuleID), string(suppression.Evidence), suppression.Scope}, "\x00")
}

func lifecycleSecurityFindingKey(finding LifecycleSecurityFinding) string {
	return strings.Join([]string{finding.Location, string(finding.RuleID), string(finding.Evidence)}, "\x00")
}

func validLifecycleSecurityScope(scope string) bool {
	if strings.ContainsAny(scope, "*?[]") {
		return false
	}
	for _, namespace := range []string{"skill/", "evals/", "previous/"} {
		if strings.HasPrefix(scope, namespace) {
			relative := strings.TrimPrefix(scope, namespace)
			return relative != "." && validSourcePath(relative)
		}
	}
	return false
}

func validLifecycleSecurityRationale(rationale LifecycleSecuritySuppressionRationale) bool {
	switch rationale {
	case LifecycleSecuritySuppressionSyntheticFixture, LifecycleSecuritySuppressionDocumentedExample,
		LifecycleSecuritySuppressionRequiredReviewed, LifecycleSecuritySuppressionConfirmedFalsePositive:
		return true
	default:
		return false
	}
}

func validLifecycleSecurityDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for index, character := range []byte(value) {
		if index == 4 || index == 7 {
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
	}
	month, _ := strconv.Atoi(value[5:7])
	day, _ := strconv.Atoi(value[8:10])
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	days := [...]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	year, _ := strconv.Atoi(value[:4])
	if month == 2 && (year%400 == 0 || year%4 == 0 && year%100 != 0) {
		days[2] = 29
	}
	return day <= days[month]
}

func lifecycleSecurityDateCompare(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
