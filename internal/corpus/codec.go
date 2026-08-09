package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxStableIDBytes  = 512
	maxGeneratorBytes = 64
	maxSchemaNumber   = 65_535
	maxJSONDepth      = 32
	maxPointerBytes   = 512
	memberMode        = uint32(0o600)

	manifestHashDomain   = "atl.corpus.manifest.v1"
	inventoryHashDomain  = "atl.corpus.inventory.v1"
	generationHashDomain = "atl.corpus.generation.v1"
)

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxMembers < 0 || limits.MaxMemberBytes < 0 ||
		limits.MaxTotalBytes < 0 || limits.MaxManifestBytes < 0 ||
		limits.MaxPathBytes < 0 || limits.MaxPathDepth < 0 {
		return Limits{}, reject(ReasonBounds)
	}

	defaults := DefaultLimits()
	if limits.MaxMembers == 0 {
		limits.MaxMembers = defaults.MaxMembers
	}
	if limits.MaxMemberBytes == 0 {
		limits.MaxMemberBytes = defaults.MaxMemberBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxManifestBytes == 0 {
		limits.MaxManifestBytes = defaults.MaxManifestBytes
	}
	if limits.MaxPathBytes == 0 {
		limits.MaxPathBytes = defaults.MaxPathBytes
	}
	if limits.MaxPathDepth == 0 {
		limits.MaxPathDepth = defaults.MaxPathDepth
	}
	if limits.MaxMembers > defaults.MaxMembers ||
		limits.MaxMemberBytes > defaults.MaxMemberBytes ||
		limits.MaxTotalBytes > defaults.MaxTotalBytes ||
		limits.MaxManifestBytes > defaults.MaxManifestBytes ||
		limits.MaxPathBytes > defaults.MaxPathBytes ||
		limits.MaxPathDepth > defaults.MaxPathDepth {
		return Limits{}, reject(ReasonBounds)
	}
	return limits, nil
}

func validateMemberSpec(spec MemberSpec, limits Limits) error {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if !validMemberService(spec.Service) || !validRole(spec.Role) {
		return reject(ReasonType)
	}
	if len(spec.StableID) == 0 {
		return reject(ReasonType)
	}
	if len(spec.StableID) > maxStableIDBytes {
		return reject(ReasonBounds)
	}
	if !utf8.ValidString(spec.StableID) || containsControl(spec.StableID) {
		return reject(ReasonType)
	}
	return validateMemberPath(spec.Path, limits)
}

func canonicalManifest(manifest Manifest, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateManifest(manifest, limits); err != nil {
		return nil, err
	}
	canonical, err := marshalCanonical(manifest)
	if err != nil {
		return nil, err
	}
	if int64(len(canonical)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return canonical, nil
}

func canonicalReceipt(receipt Receipt, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateReceipt(receipt, limits); err != nil {
		return nil, err
	}
	canonical, err := marshalCanonical(receipt)
	if err != nil {
		return nil, err
	}
	if int64(len(canonical)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return canonical, nil
}

func canonicalPointer(pointer Pointer) ([]byte, error) {
	if err := validatePointer(pointer); err != nil {
		return nil, err
	}
	canonical, err := marshalCanonical(pointer)
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxPointerBytes {
		return nil, reject(ReasonBounds)
	}
	return canonical, nil
}

func parseManifest(data []byte, limits Limits) (Manifest, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return Manifest{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxManifestBytes {
		return Manifest{}, reject(ReasonBounds)
	}
	var manifest Manifest
	if err := decodeStrictObject(data, &manifest); err != nil {
		return Manifest{}, err
	}
	canonical, err := canonicalManifest(manifest, limits)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(data, canonical) {
		return Manifest{}, reject(ReasonFormat)
	}
	return manifest, nil
}

func parseReceipt(data []byte, limits Limits) (Receipt, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return Receipt{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxManifestBytes {
		return Receipt{}, reject(ReasonBounds)
	}
	var receipt Receipt
	if err := decodeStrictObject(data, &receipt); err != nil {
		return Receipt{}, err
	}
	canonical, err := canonicalReceipt(receipt, limits)
	if err != nil {
		return Receipt{}, err
	}
	if !bytes.Equal(data, canonical) {
		return Receipt{}, reject(ReasonFormat)
	}
	return receipt, nil
}

func parsePointer(data []byte) (Pointer, error) {
	if len(data) == 0 || len(data) > maxPointerBytes {
		return Pointer{}, reject(ReasonBounds)
	}
	var pointer Pointer
	if err := decodeStrictObject(data, &pointer); err != nil {
		return Pointer{}, err
	}
	canonical, err := canonicalPointer(pointer)
	if err != nil {
		return Pointer{}, err
	}
	if !bytes.Equal(data, canonical) {
		return Pointer{}, reject(ReasonFormat)
	}
	return pointer, nil
}

// manifestDigest binds the exact canonical manifest bytes, including its
// required trailing newline.
func manifestDigest(canonical []byte) string {
	return domainHash(manifestHashDomain, canonical)
}

// inventoryDigest hashes each already sorted canonical member as a separate
// length-delimited part. The member artifact hashes themselves remain raw
// SHA-256 values.
func inventoryDigest(members []Member) (string, error) {
	if _, _, err := validateMembers(members, DefaultLimits()); err != nil {
		return "", err
	}
	hash := sha256.New()
	writeHashPart(hash, []byte(inventoryHashDomain))
	for _, member := range members {
		entry, err := json.Marshal(member)
		if err != nil {
			return "", reject(ReasonFormat)
		}
		writeHashPart(hash, entry)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// generationDigest binds the canonical receipt preimage after excluding only
// GenerationDigest.
func generationDigest(receipt Receipt) (string, error) {
	return generationDigestWithLimits(receipt, DefaultLimits())
}

func generationDigestWithLimits(receipt Receipt, limits Limits) (string, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return "", err
	}
	if err := validateReceiptFields(receipt, limits, false); err != nil {
		return "", err
	}
	preimage, err := marshalCanonical(receiptDigestPreimage{
		SchemaVersion:     receipt.SchemaVersion,
		ManifestSchema:    receipt.ManifestSchema,
		ProjectionSchema:  receipt.ProjectionSchema,
		GeneratorVersion:  receipt.GeneratorVersion,
		BuildState:        receipt.BuildState,
		PredecessorDigest: receipt.PredecessorDigest,
		Qualifications:    receipt.Qualifications,
		TombstoneDigest:   receipt.TombstoneDigest,
		Totals:            receipt.Totals,
		ManifestDigest:    receipt.ManifestDigest,
		InventoryDigest:   receipt.InventoryDigest,
	})
	if err != nil {
		return "", err
	}
	if int64(len(preimage)) > limits.MaxManifestBytes {
		return "", reject(ReasonBounds)
	}
	return domainHash(generationHashDomain, preimage), nil
}

type receiptDigestPreimage struct {
	SchemaVersion     int             `json:"schema_version"`
	ManifestSchema    int             `json:"manifest_schema"`
	ProjectionSchema  int             `json:"projection_schema"`
	GeneratorVersion  string          `json:"generator_version"`
	BuildState        BuildState      `json:"build_state"`
	PredecessorDigest string          `json:"predecessor_digest,omitempty"`
	Qualifications    []Qualification `json:"qualifications"`
	TombstoneDigest   string          `json:"tombstone_digest,omitempty"`
	Totals            Totals          `json:"totals"`
	ManifestDigest    string          `json:"manifest_digest"`
	InventoryDigest   string          `json:"inventory_digest"`
}

func validateManifest(manifest Manifest, limits Limits) error {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if manifest.SchemaVersion != ManifestSchemaV1 {
		return reject(ReasonSchema)
	}
	if !validSchemaNumber(manifest.ProjectionSchema) {
		return reject(ReasonSchema)
	}
	if err := validateGenerator(manifest.GeneratorVersion); err != nil {
		return err
	}
	if !validBuildState(manifest.BuildState) {
		return reject(ReasonType)
	}
	if manifest.PredecessorDigest != "" && !isLowerSHA256(manifest.PredecessorDigest) {
		return reject(ReasonDigest)
	}
	if manifest.TombstoneDigest != "" && !isLowerSHA256(manifest.TombstoneDigest) {
		return reject(ReasonDigest)
	}
	if err := validateQualifications(manifest.Qualifications); err != nil {
		return err
	}
	totalBytes, services, err := validateMembers(manifest.Members, limits)
	if err != nil {
		return err
	}
	if err := validateTotals(manifest.Totals, limits); err != nil {
		return err
	}
	if manifest.Totals.Members != len(manifest.Members) ||
		manifest.Totals.Bytes != totalBytes {
		return reject(ReasonMembership)
	}
	qualified := make(map[Service]struct{}, len(manifest.Qualifications))
	for _, qualification := range manifest.Qualifications {
		qualified[qualification.Service] = struct{}{}
	}
	for _, service := range services {
		if service == ServiceAggregate {
			if _, ok := qualified[ServiceConfluence]; !ok {
				return reject(ReasonMembership)
			}
			if _, ok := qualified[ServiceJira]; !ok {
				return reject(ReasonMembership)
			}
			continue
		}
		if _, ok := qualified[service]; !ok {
			return reject(ReasonMembership)
		}
	}
	return validateEncodedBound(manifest, limits.MaxManifestBytes)
}

func validateReceipt(receipt Receipt, limits Limits) error {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if err := validateReceiptFields(receipt, limits, true); err != nil {
		return err
	}
	want, err := generationDigestWithLimits(receipt, limits)
	if err != nil {
		return err
	}
	if receipt.GenerationDigest != want {
		return reject(ReasonDigest)
	}
	return validateEncodedBound(receipt, limits.MaxManifestBytes)
}

func validateReceiptFields(receipt Receipt, limits Limits, requireGeneration bool) error {
	if receipt.SchemaVersion != ReceiptSchemaV1 || receipt.ManifestSchema != ManifestSchemaV1 {
		return reject(ReasonSchema)
	}
	if !validSchemaNumber(receipt.ProjectionSchema) {
		return reject(ReasonSchema)
	}
	if err := validateGenerator(receipt.GeneratorVersion); err != nil {
		return err
	}
	if !validBuildState(receipt.BuildState) {
		return reject(ReasonType)
	}
	if receipt.PredecessorDigest != "" && !isLowerSHA256(receipt.PredecessorDigest) {
		return reject(ReasonDigest)
	}
	if receipt.TombstoneDigest != "" && !isLowerSHA256(receipt.TombstoneDigest) {
		return reject(ReasonDigest)
	}
	if err := validateQualifications(receipt.Qualifications); err != nil {
		return err
	}
	if err := validateTotals(receipt.Totals, limits); err != nil {
		return err
	}
	if !isLowerSHA256(receipt.ManifestDigest) || !isLowerSHA256(receipt.InventoryDigest) {
		return reject(ReasonDigest)
	}
	if requireGeneration && !isLowerSHA256(receipt.GenerationDigest) {
		return reject(ReasonDigest)
	}
	return nil
}

func validatePointer(pointer Pointer) error {
	if pointer.SchemaVersion != PointerSchemaV1 {
		return reject(ReasonSchema)
	}
	if err := validateGenerationID(pointer.GenerationID); err != nil {
		return err
	}
	if !isLowerSHA256(pointer.GenerationDigest) {
		return reject(ReasonDigest)
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if (value[i] < '0' || value[i] > '9') &&
			(value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}

func validateQualifications(qualifications []Qualification) error {
	if qualifications == nil {
		return reject(ReasonFormat)
	}
	if len(qualifications) == 0 {
		return reject(ReasonMembership)
	}
	var previous Service
	for i, qualification := range qualifications {
		if !validQualificationService(qualification.Service) {
			return reject(ReasonType)
		}
		if i > 0 && string(previous) >= string(qualification.Service) {
			return reject(ReasonMembership)
		}
		previous = qualification.Service
		if !validSchemaNumber(qualification.ReceiptSchema) {
			return reject(ReasonSchema)
		}
		if !isLowerSHA256(qualification.ScopeDigest) ||
			!isLowerSHA256(qualification.SelectorDigest) ||
			!isLowerSHA256(qualification.ProjectionDigest) ||
			!isLowerSHA256(qualification.ReceiptDigest) {
			return reject(ReasonDigest)
		}
	}
	return nil
}

func validateMembers(members []Member, limits Limits) (int64, []Service, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return 0, nil, err
	}
	if members == nil {
		return 0, nil, reject(ReasonFormat)
	}
	if len(members) > limits.MaxMembers {
		return 0, nil, reject(ReasonBounds)
	}

	paths := make(map[string]struct{}, len(members))
	foldedPaths := make(map[string]struct{}, len(members))
	services := make([]Service, 0, 2)
	var total int64
	for i, member := range members {
		spec := MemberSpec{
			Service:  member.Service,
			StableID: member.StableID,
			Role:     member.Role,
			Path:     member.Path,
		}
		if err := validateMemberSpec(spec, limits); err != nil {
			return 0, nil, err
		}
		if member.Size < 0 || member.Size > limits.MaxMemberBytes {
			return 0, nil, reject(ReasonBounds)
		}
		if member.Mode != memberMode {
			return 0, nil, reject(ReasonMode)
		}
		if !isLowerSHA256(member.SHA256) {
			return 0, nil, reject(ReasonDigest)
		}
		if i > 0 && compareMemberTuple(members[i-1], member) >= 0 {
			return 0, nil, reject(ReasonMembership)
		}
		if _, exists := paths[member.Path]; exists {
			return 0, nil, reject(ReasonMembership)
		}
		paths[member.Path] = struct{}{}
		folded := foldPath(member.Path)
		if _, exists := foldedPaths[folded]; exists {
			return 0, nil, reject(ReasonPath)
		}
		foldedPaths[folded] = struct{}{}
		if member.Size > limits.MaxTotalBytes-total {
			return 0, nil, reject(ReasonBounds)
		}
		total += member.Size
		if len(services) == 0 || services[len(services)-1] != member.Service {
			services = append(services, member.Service)
		}
	}
	return total, services, nil
}

func validateTotals(totals Totals, limits Limits) error {
	if totals.Members < 0 || totals.Members > limits.MaxMembers ||
		totals.Bytes < 0 || totals.Bytes > limits.MaxTotalBytes {
		return reject(ReasonBounds)
	}
	return nil
}

func validateMemberPath(value string, limits Limits) error {
	if len(value) == 0 {
		return reject(ReasonPath)
	}
	if len(value) > limits.MaxPathBytes {
		return reject(ReasonBounds)
	}
	if !utf8.ValidString(value) || containsControl(value) ||
		strings.ContainsRune(value, ':') ||
		strings.ContainsRune(value, '\\') || strings.HasPrefix(value, "/") ||
		path.IsAbs(value) || path.Clean(value) != value {
		return reject(ReasonPath)
	}
	parts := strings.Split(value, "/")
	if len(parts) > limits.MaxPathDepth {
		return reject(ReasonBounds)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return reject(ReasonPath)
		}
	}
	return nil
}

func validateGenerator(value string) error {
	if len(value) == 0 {
		return reject(ReasonType)
	}
	if len(value) > maxGeneratorBytes {
		return reject(ReasonBounds)
	}
	for i := 0; i < len(value); i++ {
		if !safeTokenByte(value[i]) {
			return reject(ReasonType)
		}
	}
	return nil
}

func validateGenerationID(value string) error {
	if len(value) != 32 {
		return reject(ReasonFormat)
	}
	for i := 0; i < len(value); i++ {
		if (value[i] < '0' || value[i] > '9') &&
			(value[i] < 'a' || value[i] > 'f') {
			return reject(ReasonFormat)
		}
	}
	return nil
}

func validMemberService(service Service) bool {
	return service == ServiceAggregate || validQualificationService(service)
}

func validQualificationService(service Service) bool {
	return service == ServiceConfluence || service == ServiceJira
}

func validRole(role Role) bool {
	switch role {
	case RoleNative, RoleMetadata, RoleDocument, RoleEdges, RoleAsset, RoleTombstone:
		return true
	default:
		return false
	}
}

func validBuildState(state BuildState) bool {
	return state == BuildStateClean || state == BuildStateModified || state == BuildStateUnknown
}

func validSchemaNumber(value int) bool {
	return value > 0 && value <= maxSchemaNumber
}

func safeTokenByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '.' || value == '_' || value == '+' || value == '-'
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func compareMemberTuple(left, right Member) int {
	if left.Service != right.Service {
		return strings.Compare(string(left.Service), string(right.Service))
	}
	if left.StableID != right.StableID {
		return strings.Compare(left.StableID, right.StableID)
	}
	return strings.Compare(string(left.Role), string(right.Role))
}

func foldPath(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, r := range value {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}

func marshalCanonical(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, reject(ReasonFormat)
	}
	return append(data, '\n'), nil
}

func validateEncodedBound(value any, maximum int64) error {
	data, err := marshalCanonical(value)
	if err != nil {
		return err
	}
	if int64(len(data)) > maximum {
		return reject(ReasonBounds)
	}
	return nil
}

func decodeStrictObject(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return reject(ReasonFormat)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return reject(ReasonFormat)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return reject(ReasonFormat)
	}
	return nil
}

func validateJSONNoDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return reject(ReasonFormat)
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return reject(ReasonBounds)
	}
	token, err := decoder.Token()
	if err != nil {
		return reject(ReasonFormat)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return reject(ReasonFormat)
			}
			key, ok := keyToken.(string)
			if !ok {
				return reject(ReasonFormat)
			}
			key = strings.ToLower(key)
			if _, duplicate := seen[key]; duplicate {
				return reject(ReasonFormat)
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return reject(ReasonFormat)
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return reject(ReasonFormat)
		}
	default:
		return reject(ReasonFormat)
	}
	return nil
}

func domainHash(domain string, parts ...[]byte) string {
	hash := sha256.New()
	writeHashPart(hash, []byte(domain))
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(destination hashWriter, part []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write(part)
}
