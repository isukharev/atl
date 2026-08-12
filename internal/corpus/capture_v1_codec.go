package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	captureReceiptDigestDomain = "atl.corpus.capture-receipt.v1"
	principalScopeDigestDomain = "atl.corpus.principal-scope.v1"
	maxCaptureReceiptBytes     = 64 << 10
	maxCapturePrincipalBytes   = 512
	maxCaptureAttempts         = 10_000_000
	maxCaptureResponseBytes    = 64 << 30
)

// PrincipalScopeDigest binds a capture to a configured backend origin and the
// authenticated stable principal without persisting either cleartext value.
func PrincipalScopeDigest(service Service, originDigest, principalID string) (string, error) {
	if !validQualificationService(service) {
		return "", reject(ReasonType)
	}
	if !validOriginDigest(originDigest) {
		return "", reject(ReasonDigest)
	}
	if principalID == "" || len(principalID) > maxCapturePrincipalBytes ||
		strings.TrimSpace(principalID) != principalID || !utf8.ValidString(principalID) || containsControl(principalID) {
		return "", reject(ReasonType)
	}
	return domainHash(principalScopeDigestDomain, []byte(service), []byte(originDigest), []byte(principalID)), nil
}

// CaptureSelectionDigest reproduces the qualified complete-pull selection
// identity from a full provider-ID inventory. Jira uses numeric order while
// Confluence preserves its established lexical ordering contract.
func CaptureSelectionDigest(service Service, providerIDs []string) (string, error) {
	if !validQualificationService(service) || providerIDs == nil {
		return "", reject(ReasonType)
	}
	ids := append([]string(nil), providerIDs...)
	for _, id := range ids {
		if err := validateProviderID(id); err != nil {
			return "", err
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if service == ServiceJira && len(ids[i]) != len(ids[j]) {
			return len(ids[i]) < len(ids[j])
		}
		return ids[i] < ids[j]
	})
	for index := 1; index < len(ids); index++ {
		if ids[index-1] == ids[index] {
			return "", reject(ReasonMembership)
		}
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return "", reject(ReasonFormat)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// BuildCaptureReceipt validates and canonicalizes one capture proof.
func BuildCaptureReceipt(input CaptureReceiptInput, limits Limits) (CaptureReceipt, error) {
	dimensions := append([]CaptureDimensionEvidence(nil), input.Dimensions...)
	sort.Slice(dimensions, func(i, j int) bool { return dimensions[i].Dimension < dimensions[j].Dimension })
	receipt := CaptureReceipt{
		SchemaVersion: CaptureReceiptSchemaV1,
		Service:       input.Service, ScopeDigest: input.ScopeDigest,
		SelectorDigest: input.SelectorDigest, OptionsDigest: input.OptionsDigest,
		SelectionDigest: input.SelectionDigest, SnapshotDigest: input.SnapshotDigest,
		StartedAt: canonicalCaptureTime(input.StartedAt), CompletedAt: canonicalCaptureTime(input.CompletedAt),
		Total: input.Total, Completed: input.Completed, Usage: input.Usage, Dimensions: dimensions,
	}
	var err error
	receipt.ReceiptDigest, err = captureReceiptDigest(receipt, limits)
	if err != nil {
		return CaptureReceipt{}, err
	}
	if err := validateCaptureReceipt(receipt, limits); err != nil {
		return CaptureReceipt{}, err
	}
	return receipt, nil
}

// CanonicalCaptureReceipt returns the exact persisted receipt bytes.
func CanonicalCaptureReceipt(receipt CaptureReceipt, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateCaptureReceipt(receipt, limits); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(receipt)
	if err != nil {
		return nil, err
	}
	if len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseCaptureReceipt accepts only strict canonical schema-v1 bytes.
func ParseCaptureReceipt(data []byte, limits Limits) (CaptureReceipt, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return CaptureReceipt{}, err
	}
	if len(data) == 0 || len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return CaptureReceipt{}, reject(ReasonBounds)
	}
	var receipt CaptureReceipt
	if err := decodeStrictObject(data, &receipt); err != nil {
		return CaptureReceipt{}, err
	}
	canonical, err := CanonicalCaptureReceipt(receipt, limits)
	if err != nil {
		return CaptureReceipt{}, err
	}
	if !bytes.Equal(data, canonical) {
		return CaptureReceipt{}, reject(ReasonFormat)
	}
	return receipt, nil
}

// VerifyCaptureReceipt revalidates a decoded receipt and its self digest.
func VerifyCaptureReceipt(receipt CaptureReceipt, limits Limits) error {
	_, err := CanonicalCaptureReceipt(receipt, limits)
	return err
}

func validateCaptureReceipt(receipt CaptureReceipt, limits Limits) error {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if receipt.SchemaVersion != CaptureReceiptSchemaV1 {
		return reject(ReasonSchema)
	}
	if !validQualificationService(receipt.Service) {
		return reject(ReasonType)
	}
	for _, digest := range []string{receipt.ScopeDigest, receipt.SelectorDigest, receipt.OptionsDigest,
		receipt.SelectionDigest, receipt.SnapshotDigest, receipt.ReceiptDigest} {
		if !isLowerSHA256(digest) {
			return reject(ReasonDigest)
		}
	}
	started, err := parseCanonicalCaptureTime(receipt.StartedAt)
	if err != nil {
		return err
	}
	completed, err := parseCanonicalCaptureTime(receipt.CompletedAt)
	if err != nil || completed.Before(started) {
		return reject(ReasonLineage)
	}
	if receipt.Total < 0 || receipt.Total > limits.MaxMembers || receipt.Completed != receipt.Total {
		return reject(ReasonMembership)
	}
	if receipt.Usage.Attempts < 0 || receipt.Usage.Attempts > maxCaptureAttempts ||
		receipt.Usage.ResponseBytes < 0 || receipt.Usage.ResponseBytes > maxCaptureResponseBytes {
		return reject(ReasonBounds)
	}
	if err := validateCaptureDimensions(receipt.Dimensions); err != nil {
		return err
	}
	want, err := captureReceiptDigest(receipt, limits)
	if err != nil {
		return err
	}
	if receipt.ReceiptDigest != want {
		return reject(ReasonDigest)
	}
	return nil
}

func validateCaptureDimensions(dimensions []CaptureDimensionEvidence) error {
	if dimensions == nil || len(dimensions) != 4 {
		return reject(ReasonMembership)
	}
	want := []CaptureDimension{CaptureAttachments, CaptureComments, CaptureMetadata, CaptureNative}
	for index, evidence := range dimensions {
		if evidence.Dimension != want[index] {
			return reject(ReasonMembership)
		}
		switch evidence.State {
		case CaptureComplete, CapturePartial, CaptureNotRequested:
		default:
			return reject(ReasonType)
		}
	}
	return nil
}

func captureReceiptDigest(receipt CaptureReceipt, limits Limits) (string, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return "", err
	}
	preimage := struct {
		SchemaVersion   int                        `json:"schema_version"`
		Service         Service                    `json:"service"`
		ScopeDigest     string                     `json:"scope_digest"`
		SelectorDigest  string                     `json:"selector_digest"`
		OptionsDigest   string                     `json:"options_digest"`
		SelectionDigest string                     `json:"selection_digest"`
		SnapshotDigest  string                     `json:"snapshot_digest"`
		StartedAt       string                     `json:"started_at"`
		CompletedAt     string                     `json:"completed_at"`
		Total           int                        `json:"total"`
		Completed       int                        `json:"completed"`
		Usage           CaptureUsage               `json:"usage"`
		Dimensions      []CaptureDimensionEvidence `json:"dimensions"`
	}{
		receipt.SchemaVersion, receipt.Service, receipt.ScopeDigest, receipt.SelectorDigest,
		receipt.OptionsDigest, receipt.SelectionDigest, receipt.SnapshotDigest,
		receipt.StartedAt, receipt.CompletedAt, receipt.Total, receipt.Completed,
		receipt.Usage, receipt.Dimensions,
	}
	data, err := marshalCanonical(preimage)
	if err != nil {
		return "", err
	}
	if len(data) > maxCaptureReceiptBytes || int64(len(data)) > limits.MaxManifestBytes {
		return "", reject(ReasonBounds)
	}
	return domainHash(captureReceiptDigestDomain, data), nil
}

func canonicalCaptureTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseCanonicalCaptureTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || value != parsed.UTC().Format(time.RFC3339Nano) || parsed.Year() < 1970 {
		return time.Time{}, reject(ReasonFormat)
	}
	return parsed, nil
}
