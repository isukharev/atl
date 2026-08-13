package corpus

import (
	"bytes"
	"context"
	"strings"
)

const (
	// CacheBindingSchemaV1 is the sealed Confluence cache-eligibility contract.
	CacheBindingSchemaV1 = 1

	cacheBindingStableID = "cache-binding-v1"
	cacheBindingPath     = "confluence/cache-binding.v1.json"

	cacheBindingDigestDomain    = "atl.corpus.cache-binding.v1"
	generatorIdentityDomain     = "atl.corpus.generator-identity.v1"
	cacheCaptureReceiptStableID = "capture-v1-receipt"
	cacheCaptureReceiptPath     = "capture/confluence/receipt.capture-v1.json"
	maxCacheBindingBytes        = int64(64 << 10)
)

// CacheBindingIneligibleReason is the closed reason why sealed evidence must
// not be selected for cache reuse. The binding remains useful as retained
// evidence even when one of the reusable-only identities is unavailable.
type CacheBindingIneligibleReason string

const (
	CacheIneligibleBuildNotClean                  CacheBindingIneligibleReason = "build_not_clean"
	CacheIneligibleGeneratorUnbound               CacheBindingIneligibleReason = "generator_unbound"
	CacheIneligibleTrustUnbound                   CacheBindingIneligibleReason = "trust_unbound"
	CacheIneligibleMetadataUnbound                CacheBindingIneligibleReason = "metadata_unbound"
	CacheIneligibleUserReferencesNondeterministic CacheBindingIneligibleReason = "user_references_nondeterministic"
	CacheIneligibleEvidenceIncomplete             CacheBindingIneligibleReason = "evidence_incomplete"
)

// CacheBindingV1 is a content-free, self-digested compatibility boundary for
// one exact Confluence capture. It is a private sealed member: digests may
// still correlate private configurations and therefore are not status output.
type CacheBindingV1 struct {
	SchemaVersion               int                          `json:"schema_version"`
	Service                     Service                      `json:"service"`
	ScopeDigest                 string                       `json:"scope_digest"`
	SelectorDigest              string                       `json:"selector_digest"`
	OptionsDigest               string                       `json:"options_digest"`
	TrustDigest                 string                       `json:"trust_digest,omitempty"`
	GeneratorDigest             string                       `json:"generator_digest,omitempty"`
	BuildState                  BuildState                   `json:"build_state"`
	ManifestSchema              int                          `json:"manifest_schema"`
	ReceiptSchema               int                          `json:"receipt_schema"`
	ProjectionSchema            int                          `json:"projection_schema"`
	CaptureSchema               int                          `json:"capture_schema"`
	SelectionDigest             string                       `json:"selection_digest"`
	MetadataDigest              string                       `json:"metadata_digest,omitempty"`
	Total                       int                          `json:"total"`
	UserReferencesDeterministic bool                         `json:"user_references_deterministic"`
	Reusable                    bool                         `json:"reusable"`
	IneligibleReason            CacheBindingIneligibleReason `json:"ineligible_reason,omitempty"`
	BindingDigest               string                       `json:"binding_digest"`
}

// CacheBindingInput omits codec-owned schema and self-digest fields.
type CacheBindingInput struct {
	Service                     Service
	ScopeDigest                 string
	SelectorDigest              string
	OptionsDigest               string
	TrustDigest                 string
	GeneratorDigest             string
	BuildState                  BuildState
	ManifestSchema              int
	ReceiptSchema               int
	ProjectionSchema            int
	CaptureSchema               int
	SelectionDigest             string
	MetadataDigest              string
	Total                       int
	UserReferencesDeterministic bool
	Reusable                    bool
	IneligibleReason            CacheBindingIneligibleReason
}

// CacheBindingMemberSpec returns the one exact tuple and path reserved for the
// schema-v1 binding in a Confluence-only generation.
func CacheBindingMemberSpec() MemberSpec {
	return MemberSpec{
		Service: ServiceConfluence, StableID: cacheBindingStableID,
		Role: RoleMetadata, Path: cacheBindingPath,
	}
}

// GeneratorIdentityDigest binds a reusable cache to one exact known clean
// generator build. Unknown, abbreviated, uppercase, or dirty identities fail
// closed instead of producing a weaker digest.
func GeneratorIdentityDigest(version, commit string, state BuildState) (string, error) {
	if err := validateGenerator(version); err != nil {
		return "", err
	}
	if state != BuildStateClean || !validExactCommit(commit) {
		return "", reject(ReasonLineage)
	}
	return domainHash(generatorIdentityDomain, []byte(version), []byte(commit), []byte(state)), nil
}

// BuildCacheBindingV1 constructs and self-digests a strict schema-v1 binding.
func BuildCacheBindingV1(input CacheBindingInput, limits Limits) (CacheBindingV1, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return CacheBindingV1{}, err
	}
	binding := CacheBindingV1{
		SchemaVersion: CacheBindingSchemaV1,
		Service:       input.Service, ScopeDigest: input.ScopeDigest,
		SelectorDigest: input.SelectorDigest, OptionsDigest: input.OptionsDigest,
		TrustDigest: input.TrustDigest, GeneratorDigest: input.GeneratorDigest,
		BuildState: input.BuildState, ManifestSchema: input.ManifestSchema,
		ReceiptSchema: input.ReceiptSchema, ProjectionSchema: input.ProjectionSchema,
		CaptureSchema: input.CaptureSchema, SelectionDigest: input.SelectionDigest,
		MetadataDigest: input.MetadataDigest, Total: input.Total,
		UserReferencesDeterministic: input.UserReferencesDeterministic,
		Reusable:                    input.Reusable, IneligibleReason: input.IneligibleReason,
	}
	if err := validateCacheBindingFields(binding, limits, false); err != nil {
		return CacheBindingV1{}, err
	}
	binding.BindingDigest, err = cacheBindingDigest(binding)
	if err != nil {
		return CacheBindingV1{}, err
	}
	if err := validateCacheBindingFields(binding, limits, true); err != nil {
		return CacheBindingV1{}, err
	}
	return binding, nil
}

// CanonicalCacheBindingV1 returns the exact sealed member bytes.
func CanonicalCacheBindingV1(binding CacheBindingV1, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateCacheBindingFields(binding, limits, true); err != nil {
		return nil, err
	}
	want, err := cacheBindingDigest(binding)
	if err != nil {
		return nil, err
	}
	if binding.BindingDigest != want {
		return nil, reject(ReasonDigest)
	}
	data, err := marshalCanonical(binding)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxCacheBindingBytes || int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseCacheBindingV1 accepts only exact canonical schema-v1 bytes.
func ParseCacheBindingV1(data []byte, limits Limits) (CacheBindingV1, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return CacheBindingV1{}, err
	}
	if len(data) == 0 || int64(len(data)) > maxCacheBindingBytes || int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return CacheBindingV1{}, reject(ReasonBounds)
	}
	var binding CacheBindingV1
	if err := decodeStrictObject(data, &binding); err != nil {
		return CacheBindingV1{}, err
	}
	canonical, err := CanonicalCacheBindingV1(binding, limits)
	if err != nil {
		return CacheBindingV1{}, err
	}
	if !bytes.Equal(data, canonical) {
		return CacheBindingV1{}, reject(ReasonFormat)
	}
	return binding, nil
}

// LoadCacheBindingV1 loads the one exact binding member from a pinned fully
// verified generation. A lookalike tuple at another path fails closed.
func LoadCacheBindingV1(ctx context.Context, generation *Generation) (CacheBindingV1, error) {
	if generation == nil {
		return CacheBindingV1{}, reject(ReasonType)
	}
	spec := CacheBindingMemberSpec()
	if err := requireExactGenerationMember(generation, spec, maxCacheBindingBytes); err != nil {
		return CacheBindingV1{}, err
	}
	var data bytes.Buffer
	if _, err := generation.CopyMember(ctx, spec.Service, spec.StableID, spec.Role, &data); err != nil {
		return CacheBindingV1{}, err
	}
	return ParseCacheBindingV1(data.Bytes(), generation.limits)
}

// VerifyCacheBindingV1 proves that the supplied binding is the exact sealed
// member and reconciles every generation/capture field that has a sealed owner.
func VerifyCacheBindingV1(binding CacheBindingV1, generation *Generation) error {
	if generation == nil {
		return reject(ReasonType)
	}
	loaded, err := LoadCacheBindingV1(context.Background(), generation)
	if err != nil {
		return err
	}
	wantBytes, err := CanonicalCacheBindingV1(binding, generation.limits)
	if err != nil {
		return err
	}
	loadedBytes, err := CanonicalCacheBindingV1(loaded, generation.limits)
	if err != nil || !bytes.Equal(wantBytes, loadedBytes) {
		return reject(ReasonDigest)
	}

	manifest := generation.Manifest()
	receipt := generation.Receipt()
	if len(manifest.Qualifications) != 1 || len(receipt.Qualifications) != 1 {
		return reject(ReasonMembership)
	}
	qualification := manifest.Qualifications[0]
	if binding.Service != ServiceConfluence || qualification.Service != ServiceConfluence ||
		binding.ManifestSchema != manifest.SchemaVersion || binding.ManifestSchema != receipt.ManifestSchema ||
		binding.ReceiptSchema != receipt.SchemaVersion || binding.ProjectionSchema != manifest.ProjectionSchema ||
		binding.ProjectionSchema != receipt.ProjectionSchema || binding.CaptureSchema != qualification.ReceiptSchema ||
		binding.ScopeDigest != qualification.ScopeDigest || binding.SelectorDigest != qualification.SelectorDigest ||
		binding.BuildState != manifest.BuildState || binding.BuildState != receipt.BuildState {
		return reject(ReasonLineage)
	}

	capture, err := loadCacheCaptureReceipt(generation)
	if err != nil {
		return err
	}
	if capture.SchemaVersion != binding.CaptureSchema || capture.Service != binding.Service ||
		capture.ScopeDigest != binding.ScopeDigest || capture.SelectorDigest != binding.SelectorDigest ||
		capture.OptionsDigest != binding.OptionsDigest || capture.SelectionDigest != binding.SelectionDigest ||
		capture.Total != binding.Total || capture.ReceiptDigest != qualification.ReceiptDigest {
		return reject(ReasonLineage)
	}
	if binding.Reusable {
		if capture.Completed != capture.Total {
			return reject(ReasonLineage)
		}
		for _, dimension := range capture.Dimensions {
			if dimension.State == CapturePartial {
				return reject(ReasonLineage)
			}
		}
	}
	return nil
}

func loadCacheCaptureReceipt(generation *Generation) (CaptureReceipt, error) {
	spec := MemberSpec{
		Service: ServiceConfluence, StableID: cacheCaptureReceiptStableID,
		Role: RoleMetadata, Path: cacheCaptureReceiptPath,
	}
	if err := requireExactGenerationMember(generation, spec, maxCaptureReceiptBytes); err != nil {
		return CaptureReceipt{}, err
	}
	var data bytes.Buffer
	if _, err := generation.CopyMember(context.Background(), spec.Service, spec.StableID, spec.Role, &data); err != nil {
		return CaptureReceipt{}, err
	}
	receipt, err := ParseCaptureReceipt(data.Bytes(), generation.limits)
	if err != nil || VerifyCaptureReceipt(receipt, generation.limits) != nil {
		return CaptureReceipt{}, reject(ReasonDigest)
	}
	return receipt, nil
}

func requireExactGenerationMember(generation *Generation, spec MemberSpec, maximum int64) error {
	matches := 0
	for _, member := range generation.Manifest().Members {
		if member.Service == spec.Service && member.StableID == spec.StableID && member.Role == spec.Role {
			matches++
			if member.Path != spec.Path || member.Size < 0 || member.Size > maximum {
				return reject(ReasonMembership)
			}
		}
	}
	if matches != 1 {
		return reject(ReasonMembership)
	}
	return nil
}

func validateCacheBindingFields(binding CacheBindingV1, limits Limits, requireDigest bool) error {
	if binding.SchemaVersion != CacheBindingSchemaV1 || binding.ManifestSchema != ManifestSchemaV1 ||
		binding.ReceiptSchema != ReceiptSchemaV1 || !validSchemaNumber(binding.ProjectionSchema) ||
		binding.CaptureSchema != CaptureReceiptSchemaV1 {
		return reject(ReasonSchema)
	}
	if binding.Service != ServiceConfluence || !validBuildState(binding.BuildState) {
		return reject(ReasonType)
	}
	for _, digest := range []string{binding.ScopeDigest, binding.SelectorDigest, binding.OptionsDigest, binding.SelectionDigest} {
		if !isLowerSHA256(digest) {
			return reject(ReasonDigest)
		}
	}
	for _, optional := range []string{binding.TrustDigest, binding.GeneratorDigest, binding.MetadataDigest} {
		if optional != "" && !isLowerSHA256(optional) {
			return reject(ReasonDigest)
		}
	}
	if binding.Total < 0 || binding.Total > limits.MaxMembers {
		return reject(ReasonBounds)
	}
	if binding.Reusable {
		if binding.IneligibleReason != "" || binding.BuildState != BuildStateClean ||
			binding.TrustDigest == "" || binding.GeneratorDigest == "" || binding.MetadataDigest == "" ||
			!binding.UserReferencesDeterministic {
			return reject(ReasonLineage)
		}
	} else {
		if !validCacheIneligibleReason(binding.IneligibleReason) {
			return reject(ReasonType)
		}
		if !cacheIneligibleReasonMatches(binding) {
			return reject(ReasonLineage)
		}
	}
	if requireDigest {
		if !isLowerSHA256(binding.BindingDigest) {
			return reject(ReasonDigest)
		}
	} else if binding.BindingDigest != "" {
		return reject(ReasonDigest)
	}
	return nil
}

func validCacheIneligibleReason(reason CacheBindingIneligibleReason) bool {
	switch reason {
	case CacheIneligibleBuildNotClean, CacheIneligibleGeneratorUnbound,
		CacheIneligibleTrustUnbound, CacheIneligibleMetadataUnbound,
		CacheIneligibleUserReferencesNondeterministic, CacheIneligibleEvidenceIncomplete:
		return true
	default:
		return false
	}
}

func cacheIneligibleReasonMatches(binding CacheBindingV1) bool {
	switch binding.IneligibleReason {
	case CacheIneligibleBuildNotClean:
		return binding.BuildState != BuildStateClean
	case CacheIneligibleGeneratorUnbound:
		return binding.GeneratorDigest == ""
	case CacheIneligibleTrustUnbound:
		return binding.TrustDigest == ""
	case CacheIneligibleMetadataUnbound:
		return binding.MetadataDigest == ""
	case CacheIneligibleUserReferencesNondeterministic:
		return !binding.UserReferencesDeterministic
	case CacheIneligibleEvidenceIncomplete:
		return true
	default:
		return false
	}
}

func cacheBindingDigest(binding CacheBindingV1) (string, error) {
	projection := binding
	projection.BindingDigest = ""
	data, err := marshalCanonical(projection)
	if err != nil {
		return "", err
	}
	return domainHash(cacheBindingDigestDomain, data), nil
}

func validExactCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 || commit != strings.ToLower(commit) {
		return false
	}
	for index := range len(commit) {
		if (commit[index] < '0' || commit[index] > '9') && (commit[index] < 'a' || commit[index] > 'f') {
			return false
		}
	}
	return true
}
