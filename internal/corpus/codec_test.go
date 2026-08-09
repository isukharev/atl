package corpus

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestCanonicalCodecExactBytesAndDigests(t *testing.T) {
	manifest := validManifest()
	wantManifest := `{"schema_version":1,"projection_schema":7,"generator_version":"v1.2.3+clean","build_state":"clean","predecessor_digest":"` + digestByte('1') + `","qualifications":[{"service":"confluence","receipt_schema":1,"scope_digest":"` + digestByte('3') + `","selector_digest":"` + digestByte('4') + `","projection_digest":"` + digestByte('5') + `","receipt_digest":"` + digestByte('6') + `"},{"service":"jira","receipt_schema":1,"scope_digest":"` + digestByte('7') + `","selector_digest":"` + digestByte('8') + `","projection_digest":"` + digestByte('9') + `","receipt_digest":"` + digestByte('a') + `"}],"tombstone_digest":"` + digestByte('2') + `","members":[{"service":"confluence","stable_id":"100","role":"document","path":"confluence/100/document.csf","size":3,"mode":384,"sha256":"` + digestByte('b') + `"},{"service":"jira","stable_id":"PROJ-1","role":"native","path":"jira/PROJ-1/native.wiki","size":4,"mode":384,"sha256":"` + digestByte('c') + `"}],"totals":{"members":2,"bytes":7}}` + "\n"

	manifestBytes, err := canonicalManifest(manifest, Limits{})
	if err != nil {
		t.Fatalf("canonicalManifest: %v", err)
	}
	if diff := firstByteDifference(manifestBytes, []byte(wantManifest)); diff != "" {
		t.Fatalf("manifest bytes differ: %s\ngot:  %s\nwant: %s", diff, manifestBytes, wantManifest)
	}
	const wantManifestDigest = "14135a625eb45d9994570810b5fdb794f9c78c13239439a25f83946481c61c7b"
	if got := manifestDigest(manifestBytes); got != wantManifestDigest {
		t.Fatalf("manifest digest = %q, want %q", got, wantManifestDigest)
	}

	gotInventory, err := inventoryDigest(manifest.Members)
	if err != nil {
		t.Fatalf("inventoryDigest: %v", err)
	}
	const wantInventoryDigest = "f4b9c34ed8b9d2e2d6fe97e5b4b2ac6013e8142f747fa8f66f481916c7ee5f49"
	if gotInventory != wantInventoryDigest {
		t.Fatalf("inventory digest = %q, want %q", gotInventory, wantInventoryDigest)
	}

	receipt := validReceipt(t, manifest)
	const wantGenerationDigest = "4b4a95e2e0332e48a4f0e02a363e65b7882df4f25c99a77c82c1ed286c414cd7"
	if receipt.GenerationDigest != wantGenerationDigest {
		t.Fatalf("generation digest = %q, want %q", receipt.GenerationDigest, wantGenerationDigest)
	}
	wantReceipt := `{"schema_version":1,"manifest_schema":1,"projection_schema":7,"generator_version":"v1.2.3+clean","build_state":"clean","predecessor_digest":"` + digestByte('1') + `","qualifications":[{"service":"confluence","receipt_schema":1,"scope_digest":"` + digestByte('3') + `","selector_digest":"` + digestByte('4') + `","projection_digest":"` + digestByte('5') + `","receipt_digest":"` + digestByte('6') + `"},{"service":"jira","receipt_schema":1,"scope_digest":"` + digestByte('7') + `","selector_digest":"` + digestByte('8') + `","projection_digest":"` + digestByte('9') + `","receipt_digest":"` + digestByte('a') + `"}],"tombstone_digest":"` + digestByte('2') + `","totals":{"members":2,"bytes":7},"manifest_digest":"` + wantManifestDigest + `","inventory_digest":"` + wantInventoryDigest + `","generation_digest":"` + wantGenerationDigest + `"}` + "\n"
	receiptBytes, err := canonicalReceipt(receipt, Limits{})
	if err != nil {
		t.Fatalf("canonicalReceipt: %v", err)
	}
	if diff := firstByteDifference(receiptBytes, []byte(wantReceipt)); diff != "" {
		t.Fatalf("receipt bytes differ: %s\ngot:  %s\nwant: %s", diff, receiptBytes, wantReceipt)
	}

	pointer := Pointer{
		SchemaVersion:    PointerSchemaV1,
		GenerationID:     "0123456789abcdef0123456789abcdef",
		GenerationDigest: receipt.GenerationDigest,
	}
	pointerBytes, err := canonicalPointer(pointer)
	if err != nil {
		t.Fatalf("canonicalPointer: %v", err)
	}
	wantPointer := `{"schema_version":1,"generation_id":"0123456789abcdef0123456789abcdef","generation_digest":"` + wantGenerationDigest + `"}` + "\n"
	if !bytes.Equal(pointerBytes, []byte(wantPointer)) {
		t.Fatalf("pointer bytes = %q, want %q", pointerBytes, wantPointer)
	}

	parsedManifest, err := parseManifest(manifestBytes, Limits{})
	if err != nil || parsedManifest.Totals != manifest.Totals {
		t.Fatalf("parseManifest round trip = %#v, %v", parsedManifest.Totals, err)
	}
	parsedReceipt, err := parseReceipt(receiptBytes, Limits{})
	if err != nil || parsedReceipt.GenerationDigest != receipt.GenerationDigest {
		t.Fatalf("parseReceipt round trip = %q, %v", parsedReceipt.GenerationDigest, err)
	}
	parsedPointer, err := parsePointer(pointerBytes)
	if err != nil || parsedPointer != pointer {
		t.Fatalf("parsePointer round trip = %#v, %v", parsedPointer, err)
	}
}

func TestParseManifestRejectsStrictJSONViolations(t *testing.T) {
	manifest := validManifest()
	canonical := mustCanonicalManifest(t, manifest)
	canonicalText := string(canonical)

	unsortedQualifications := cloneManifest(manifest)
	unsortedQualifications.Qualifications[0], unsortedQualifications.Qualifications[1] =
		unsortedQualifications.Qualifications[1], unsortedQualifications.Qualifications[0]
	unsortedMembers := cloneManifest(manifest)
	unsortedMembers.Members[0], unsortedMembers.Members[1] =
		unsortedMembers.Members[1], unsortedMembers.Members[0]
	upperHash := cloneManifest(manifest)
	upperHash.Members[0].SHA256 = strings.ToUpper(upperHash.Members[0].SHA256)
	nullMembers := cloneManifest(manifest)
	nullMembers.Members = nil
	nullQualifications := cloneManifest(manifest)
	nullQualifications.Qualifications = nil

	tests := map[string][]byte{
		"duplicate root key":       []byte(strings.Replace(canonicalText, `{"schema_version":1,`, `{"schema_version":1,"schema_version":1,`, 1)),
		"duplicate nested key":     []byte(strings.Replace(canonicalText, `"totals":{"members":2,`, `"totals":{"members":2,"members":2,`, 1)),
		"case alias duplicate key": []byte(strings.Replace(canonicalText, `{"schema_version":1,`, `{"SCHEMA_VERSION":1,"schema_version":1,`, 1)),
		"unknown root member":      []byte(strings.Replace(canonicalText, `{"schema_version":1,`, `{"unexpected":0,"schema_version":1,`, 1)),
		"unknown nested member":    []byte(strings.Replace(canonicalText, `"receipt_schema":1,`, `"unexpected":0,"receipt_schema":1,`, 1)),
		"missing schema":           []byte(strings.Replace(canonicalText, `"schema_version":1,`, ``, 1)),
		"missing generator":        []byte(strings.Replace(canonicalText, `"generator_version":"v1.2.3+clean",`, ``, 1)),
		"missing members":          []byte(strings.Replace(canonicalText, `,"members":[{"service":"confluence","stable_id":"100","role":"document","path":"confluence/100/document.csf","size":3,"mode":384,"sha256":"`+digestByte('b')+`"},{"service":"jira","stable_id":"PROJ-1","role":"native","path":"jira/PROJ-1/native.wiki","size":4,"mode":384,"sha256":"`+digestByte('c')+`"}]`, ``, 1)),
		"missing totals":           []byte(strings.Replace(canonicalText, `,"totals":{"members":2,"bytes":7}`, ``, 1)),
		"null scalar":              []byte(strings.Replace(canonicalText, `"build_state":"clean"`, `"build_state":null`, 1)),
		"null members":             marshalUnchecked(t, nullMembers),
		"null qualifications":      marshalUnchecked(t, nullQualifications),
		"trailing document":        append(append([]byte{}, canonical...), []byte("{}\n")...),
		"future schema":            []byte(strings.Replace(canonicalText, `"schema_version":1`, `"schema_version":2`, 1)),
		"unversioned schema":       []byte(strings.Replace(canonicalText, `"schema_version":1,`, ``, 1)),
		"space":                    []byte(strings.Replace(canonicalText, `"schema_version":1`, `"schema_version": 1`, 1)),
		"field order":              []byte(strings.Replace(canonicalText, `{"schema_version":1,"projection_schema":7`, `{"projection_schema":7,"schema_version":1`, 1)),
		"missing newline":          bytes.TrimSuffix(canonical, []byte("\n")),
		"extra newline":            append(append([]byte{}, canonical...), '\n'),
		"uppercase hash":           marshalUnchecked(t, upperHash),
		"unsorted qualifications":  marshalUnchecked(t, unsortedQualifications),
		"unsorted members":         marshalUnchecked(t, unsortedMembers),
		"invalid UTF-8":            append([]byte(`{"schema_version":"`), 0xff, '}'),
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseManifest(data, Limits{})
			assertRejected(t, err)
		})
	}
}

func TestValidateManifestRejectsSemanticViolations(t *testing.T) {
	base := validManifest()

	tests := []struct {
		name   string
		limits Limits
		mutate func(*Manifest)
		reason Reason
	}{
		{name: "future schema", mutate: func(m *Manifest) { m.SchemaVersion = 2 }, reason: ReasonSchema},
		{name: "unversioned schema", mutate: func(m *Manifest) { m.SchemaVersion = 0 }, reason: ReasonSchema},
		{name: "projection zero", mutate: func(m *Manifest) { m.ProjectionSchema = 0 }, reason: ReasonSchema},
		{name: "projection too large", mutate: func(m *Manifest) { m.ProjectionSchema = maxSchemaNumber + 1 }, reason: ReasonSchema},
		{name: "unknown service", mutate: func(m *Manifest) { m.Members[0].Service = "private" }, reason: ReasonType},
		{name: "unknown role", mutate: func(m *Manifest) { m.Members[0].Role = "private" }, reason: ReasonType},
		{name: "unknown build", mutate: func(m *Manifest) { m.BuildState = "private" }, reason: ReasonType},
		{name: "empty generator", mutate: func(m *Manifest) { m.GeneratorVersion = "" }, reason: ReasonType},
		{name: "generator character", mutate: func(m *Manifest) { m.GeneratorVersion = "v1/private" }, reason: ReasonType},
		{name: "generator too long", mutate: func(m *Manifest) { m.GeneratorVersion = strings.Repeat("a", maxGeneratorBytes+1) }, reason: ReasonBounds},
		{name: "stable id empty", mutate: func(m *Manifest) { m.Members[0].StableID = "" }, reason: ReasonType},
		{name: "stable id control", mutate: func(m *Manifest) { m.Members[0].StableID = "id\n" }, reason: ReasonType},
		{name: "stable id too long", mutate: func(m *Manifest) { m.Members[0].StableID = strings.Repeat("a", maxStableIDBytes+1) }, reason: ReasonBounds},
		{name: "member negative size", mutate: func(m *Manifest) { m.Members[0].Size = -1 }, reason: ReasonBounds},
		{name: "member too large", limits: Limits{MaxMemberBytes: 2}, reason: ReasonBounds},
		{name: "total too large", limits: Limits{MaxMemberBytes: 10, MaxTotalBytes: 6}, reason: ReasonBounds},
		{name: "member count", limits: Limits{MaxMembers: 1}, reason: ReasonBounds},
		{name: "unsafe mode", mutate: func(m *Manifest) { m.Members[0].Mode = 0o640 }, reason: ReasonMode},
		{name: "uppercase digest", mutate: func(m *Manifest) { m.Members[0].SHA256 = strings.ToUpper(m.Members[0].SHA256) }, reason: ReasonDigest},
		{name: "short digest", mutate: func(m *Manifest) { m.Members[0].SHA256 = "abc" }, reason: ReasonDigest},
		{name: "bad predecessor", mutate: func(m *Manifest) { m.PredecessorDigest = "abc" }, reason: ReasonDigest},
		{name: "bad tombstone", mutate: func(m *Manifest) { m.TombstoneDigest = "abc" }, reason: ReasonDigest},
		{name: "wrong member total", mutate: func(m *Manifest) { m.Totals.Members++ }, reason: ReasonMembership},
		{name: "wrong byte total", mutate: func(m *Manifest) { m.Totals.Bytes++ }, reason: ReasonMembership},
		{name: "negative member total", mutate: func(m *Manifest) { m.Totals.Members = -1 }, reason: ReasonBounds},
		{name: "negative byte total", mutate: func(m *Manifest) { m.Totals.Bytes = -1 }, reason: ReasonBounds},
		{name: "missing service qualification", mutate: func(m *Manifest) { m.Qualifications = m.Qualifications[:1] }, reason: ReasonMembership},
		{name: "qualification unknown service", mutate: func(m *Manifest) { m.Qualifications[0].Service = "private" }, reason: ReasonType},
		{name: "qualification schema zero", mutate: func(m *Manifest) { m.Qualifications[0].ReceiptSchema = 0 }, reason: ReasonSchema},
		{name: "qualification bad digest", mutate: func(m *Manifest) { m.Qualifications[0].ScopeDigest = "abc" }, reason: ReasonDigest},
		{name: "qualification unsorted", mutate: func(m *Manifest) { m.Qualifications[0], m.Qualifications[1] = m.Qualifications[1], m.Qualifications[0] }, reason: ReasonMembership},
		{name: "qualification duplicate", mutate: func(m *Manifest) { m.Qualifications[1].Service = m.Qualifications[0].Service }, reason: ReasonMembership},
		{name: "member unsorted", mutate: func(m *Manifest) { m.Members[0], m.Members[1] = m.Members[1], m.Members[0] }, reason: ReasonMembership},
		{name: "duplicate tuple", mutate: func(m *Manifest) {
			m.Members[1].Service, m.Members[1].StableID, m.Members[1].Role = m.Members[0].Service, m.Members[0].StableID, m.Members[0].Role
			m.Members[1].Path = "other/file"
		}, reason: ReasonMembership},
		{name: "duplicate path", mutate: func(m *Manifest) { m.Members[1].Path = m.Members[0].Path }, reason: ReasonMembership},
		{name: "case path alias", mutate: func(m *Manifest) { m.Members[1].Path = strings.ToUpper(m.Members[0].Path) }, reason: ReasonPath},
		{name: "unicode case path alias", mutate: func(m *Manifest) { m.Members[0].Path = "data/K/file"; m.Members[1].Path = "data/\u212a/file" }, reason: ReasonPath},
		{name: "path bytes", limits: Limits{MaxPathBytes: 4}, reason: ReasonBounds},
		{name: "path depth", limits: Limits{MaxPathDepth: 2}, reason: ReasonBounds},
		{name: "manifest bytes", limits: Limits{MaxManifestBytes: 32}, reason: ReasonBounds},
		{name: "nil members", mutate: func(m *Manifest) { m.Members = nil }, reason: ReasonFormat},
		{name: "nil qualifications", mutate: func(m *Manifest) { m.Qualifications = nil }, reason: ReasonFormat},
		{name: "empty qualifications", mutate: func(m *Manifest) { m.Qualifications = []Qualification{} }, reason: ReasonMembership},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := cloneManifest(base)
			if test.mutate != nil {
				test.mutate(&manifest)
			}
			err := validateManifest(manifest, test.limits)
			assertReason(t, err, test.reason)
		})
	}

	overflow := cloneManifest(base)
	overflow.Members[0].Size = math.MaxInt64
	overflow.Members[1].Size = 1
	overflow.Totals.Bytes = math.MaxInt64
	err := validateManifest(overflow, Limits{MaxMemberBytes: math.MaxInt64, MaxTotalBytes: math.MaxInt64})
	assertReason(t, err, ReasonBounds)
}

func TestManifestAllowsQualifiedServiceWithoutMembers(t *testing.T) {
	manifest := validManifest()
	manifest.Qualifications = append([]Qualification(nil), manifest.Qualifications[:1]...)
	manifest.Members = []Member{}
	manifest.Totals = Totals{}

	canonical, err := canonicalManifest(manifest, Limits{})
	if err != nil {
		t.Fatalf("qualified empty manifest rejected: %v", err)
	}
	parsed, err := parseManifest(canonical, Limits{})
	if err != nil {
		t.Fatalf("qualified empty manifest did not parse: %v", err)
	}
	if parsed.Members == nil || len(parsed.Members) != 0 || len(parsed.Qualifications) != 1 {
		t.Fatalf("qualified empty manifest changed shape: %#v", parsed)
	}
}

func TestNormalizeLimitsEnforcesHardMaxima(t *testing.T) {
	defaults := DefaultLimits()
	tests := []Limits{
		{MaxMembers: defaults.MaxMembers + 1},
		{MaxMemberBytes: defaults.MaxMemberBytes + 1},
		{MaxTotalBytes: defaults.MaxTotalBytes + 1},
		{MaxManifestBytes: defaults.MaxManifestBytes + 1},
		{MaxPathBytes: defaults.MaxPathBytes + 1},
		{MaxPathDepth: defaults.MaxPathDepth + 1},
	}
	for _, limits := range tests {
		if _, err := normalizeLimits(limits); err == nil {
			t.Fatalf("limits above hard maximum accepted: %#v", limits)
		} else {
			assertReason(t, err, ReasonBounds)
		}
	}
	if got, err := normalizeLimits(Limits{}); err != nil || got != defaults {
		t.Fatalf("zero limits = %#v, %v; want %#v", got, err, defaults)
	}
}

func TestMemberPathValidation(t *testing.T) {
	unsafe := []string{
		"", ".", "..", "/absolute", "../escape", "dir/../escape", "./file",
		"dir//file", "dir/", "dir\\file", "dir:name/file", "dir\nfile",
		string([]byte{'d', 'i', 'r', '/', 0xff}),
	}
	for _, value := range unsafe {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			err := validateMemberSpec(MemberSpec{
				Service: ServiceJira, StableID: "id", Role: RoleNative, Path: value,
			}, Limits{})
			assertRejected(t, err)
		})
	}

	if err := validateMemberSpec(MemberSpec{
		Service: ServiceConfluence, StableID: "id-\u03c0", Role: RoleAsset, Path: "safe/\u03c0.bin",
	}, Limits{}); err != nil {
		t.Fatalf("safe Unicode member spec rejected: %v", err)
	}
	if _, err := normalizeLimits(Limits{MaxMembers: -1}); err == nil {
		t.Fatal("negative limits accepted")
	}
}

func TestReceiptSelfDigestAndLineageBinding(t *testing.T) {
	manifest := validManifest()
	receipt := validReceipt(t, manifest)
	if err := validateReceipt(receipt, Limits{}); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}

	tampered := cloneReceipt(receipt)
	tampered.GenerationDigest = digestByte('f')
	assertReason(t, validateReceipt(tampered, Limits{}), ReasonDigest)

	tampered = cloneReceipt(receipt)
	tampered.PredecessorDigest = digestByte('d')
	predecessorDigest, err := generationDigest(tampered)
	if err != nil {
		t.Fatalf("digest changed predecessor: %v", err)
	}
	if predecessorDigest == receipt.GenerationDigest {
		t.Fatal("predecessor change did not change generation digest")
	}

	tampered = cloneReceipt(receipt)
	tampered.Qualifications[0].ReceiptDigest, tampered.Qualifications[1].ReceiptDigest =
		tampered.Qualifications[1].ReceiptDigest, tampered.Qualifications[0].ReceiptDigest
	serviceSwapDigest, err := generationDigest(tampered)
	if err != nil {
		t.Fatalf("digest changed service lineage: %v", err)
	}
	if serviceSwapDigest == receipt.GenerationDigest {
		t.Fatal("service lineage swap did not change generation digest")
	}

	withoutSelf := cloneReceipt(receipt)
	withoutSelf.GenerationDigest = ""
	got, err := generationDigest(withoutSelf)
	if err != nil || got != receipt.GenerationDigest {
		t.Fatalf("generationDigest depends on self field: got %q, err %v", got, err)
	}
}

func TestParseReceiptAndPointerAreStrict(t *testing.T) {
	manifest := validManifest()
	receipt := validReceipt(t, manifest)
	receiptBytes := mustCanonicalReceipt(t, receipt)
	receiptText := string(receiptBytes)

	receiptCases := map[string][]byte{
		"duplicate nested": []byte(strings.Replace(receiptText, `"totals":{"members":2,`, `"totals":{"members":2,"members":2,`, 1)),
		"unknown":          []byte(strings.Replace(receiptText, `{"schema_version":1,`, `{"private":1,"schema_version":1,`, 1)),
		"missing digest":   []byte(strings.Replace(receiptText, `,"generation_digest":"`+receipt.GenerationDigest+`"`, ``, 1)),
		"null digest":      []byte(strings.Replace(receiptText, `"generation_digest":"`+receipt.GenerationDigest+`"`, `"generation_digest":null`, 1)),
		"tampered digest":  []byte(strings.Replace(receiptText, receipt.GenerationDigest, digestByte('f'), 1)),
		"future schema":    []byte(strings.Replace(receiptText, `"schema_version":1`, `"schema_version":2`, 1)),
		"missing newline":  bytes.TrimSuffix(receiptBytes, []byte("\n")),
		"whitespace":       []byte(strings.Replace(receiptText, `"schema_version":1`, `"schema_version": 1`, 1)),
		"trailing":         append(append([]byte{}, receiptBytes...), []byte("null\n")...),
	}
	for name, data := range receiptCases {
		t.Run("receipt "+name, func(t *testing.T) {
			_, err := parseReceipt(data, Limits{})
			assertRejected(t, err)
		})
	}

	pointer := Pointer{SchemaVersion: 1, GenerationID: "0123456789abcdef0123456789abcdef", GenerationDigest: receipt.GenerationDigest}
	pointerBytes, err := canonicalPointer(pointer)
	if err != nil {
		t.Fatalf("canonicalPointer: %v", err)
	}
	pointerText := string(pointerBytes)
	pointerCases := map[string][]byte{
		"duplicate":        []byte(strings.Replace(pointerText, `{"schema_version":1,`, `{"schema_version":1,"schema_version":1,`, 1)),
		"unknown":          []byte(strings.Replace(pointerText, `{"schema_version":1,`, `{"private":1,"schema_version":1,`, 1)),
		"missing":          []byte(strings.Replace(pointerText, `"generation_id":"0123456789abcdef0123456789abcdef",`, ``, 1)),
		"null":             []byte(strings.Replace(pointerText, `"generation_id":"0123456789abcdef0123456789abcdef"`, `"generation_id":null`, 1)),
		"future":           []byte(strings.Replace(pointerText, `"schema_version":1`, `"schema_version":2`, 1)),
		"unsafe id":        []byte(strings.Replace(pointerText, `0123456789abcdef0123456789abcdef`, `../private`, 1)),
		"uppercase id":     []byte(strings.Replace(pointerText, `0123456789abcdef0123456789abcdef`, `0123456789ABCDEF0123456789ABCDEF`, 1)),
		"short id":         []byte(strings.Replace(pointerText, `0123456789abcdef0123456789abcdef`, `0123456789abcdef`, 1)),
		"safe nonhex id":   []byte(strings.Replace(pointerText, `0123456789abcdef0123456789abcdef`, `generation_id_is_safe_ascii_0001`, 1)),
		"uppercase digest": []byte(strings.Replace(pointerText, receipt.GenerationDigest, strings.ToUpper(receipt.GenerationDigest), 1)),
		"whitespace":       []byte(strings.Replace(pointerText, `"schema_version":1`, `"schema_version": 1`, 1)),
		"no newline":       bytes.TrimSuffix(pointerBytes, []byte("\n")),
		"trailing":         append(append([]byte{}, pointerBytes...), []byte("{}\n")...),
	}
	for name, data := range pointerCases {
		t.Run("pointer "+name, func(t *testing.T) {
			_, err := parsePointer(data)
			assertRejected(t, err)
		})
	}
}

func TestInventoryDigestRequiresCanonicalMembers(t *testing.T) {
	members := cloneManifest(validManifest()).Members
	baseline, err := inventoryDigest(members)
	if err != nil {
		t.Fatalf("inventoryDigest: %v", err)
	}

	changed := append([]Member(nil), members...)
	changed[0].SHA256 = digestByte('d')
	got, err := inventoryDigest(changed)
	if err != nil {
		t.Fatalf("changed inventoryDigest: %v", err)
	}
	if got == baseline {
		t.Fatal("member change did not change inventory digest")
	}

	changed[0], changed[1] = changed[1], changed[0]
	if _, err := inventoryDigest(changed); err == nil {
		t.Fatal("unsorted member inventory accepted")
	}
	if domainHash("domain-one", []byte("ab"), []byte("c")) ==
		domainHash("domain-two", []byte("ab"), []byte("c")) {
		t.Fatal("domain tag did not separate digest")
	}
	if domainHash("domain", []byte("ab"), []byte("c")) ==
		domainHash("domain", []byte("a"), []byte("bc")) {
		t.Fatal("length prefixes did not separate parts")
	}
}

func TestClosedVocabulariesAndHashes(t *testing.T) {
	for _, service := range []Service{ServiceJira, ServiceConfluence} {
		if !validService(service) {
			t.Fatalf("valid service %q rejected", service)
		}
	}
	for _, role := range []Role{RoleNative, RoleMetadata, RoleDocument, RoleEdges, RoleAsset, RoleTombstone} {
		if !validRole(role) {
			t.Fatalf("valid role %q rejected", role)
		}
	}
	for _, state := range []BuildState{BuildStateClean, BuildStateModified, BuildStateUnknown} {
		if !validBuildState(state) {
			t.Fatalf("valid build state %q rejected", state)
		}
	}
	for _, invalid := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if isLowerSHA256(invalid) {
			t.Fatalf("invalid SHA-256 %q accepted", invalid)
		}
	}
	if !isLowerSHA256(strings.Repeat("0a", 32)) {
		t.Fatal("valid lowercase SHA-256 rejected")
	}
}

func TestErrorsNeverExposeInputContent(t *testing.T) {
	canaries := []string{"secret.example.invalid", "PRIVATE-42", "SensitiveTitle"}
	manifest := validManifest()
	manifest.Members[0].Path = strings.Join(canaries, "/") + "\n"
	manifest.Members[0].StableID = strings.Join(canaries, "-") + "\n"
	err := validateManifest(manifest, Limits{})
	assertPrivateError(t, err, canaries)

	malformed := []byte(`{"schema_version":1,"secret.example.invalid":{"PRIVATE-42":"SensitiveTitle"}}` + "\n")
	_, err = parseManifest(malformed, Limits{})
	assertPrivateError(t, err, canaries)

	pointer := Pointer{SchemaVersion: 1, GenerationID: strings.Join(canaries, "/"), GenerationDigest: digestByte('a')}
	_, err = canonicalPointer(pointer)
	assertPrivateError(t, err, canaries)

	unknown := reject(Reason("SensitiveTitle"))
	assertPrivateError(t, unknown, canaries)
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:     ManifestSchemaV1,
		ProjectionSchema:  7,
		GeneratorVersion:  "v1.2.3+clean",
		BuildState:        BuildStateClean,
		PredecessorDigest: digestByte('1'),
		Qualifications: []Qualification{
			{
				Service: ServiceConfluence, ReceiptSchema: 1,
				ScopeDigest: digestByte('3'), SelectorDigest: digestByte('4'),
				ProjectionDigest: digestByte('5'), ReceiptDigest: digestByte('6'),
			},
			{
				Service: ServiceJira, ReceiptSchema: 1,
				ScopeDigest: digestByte('7'), SelectorDigest: digestByte('8'),
				ProjectionDigest: digestByte('9'), ReceiptDigest: digestByte('a'),
			},
		},
		TombstoneDigest: digestByte('2'),
		Members: []Member{
			{
				Service: ServiceConfluence, StableID: "100", Role: RoleDocument,
				Path: "confluence/100/document.csf", Size: 3, Mode: 0o600, SHA256: digestByte('b'),
			},
			{
				Service: ServiceJira, StableID: "PROJ-1", Role: RoleNative,
				Path: "jira/PROJ-1/native.wiki", Size: 4, Mode: 0o600, SHA256: digestByte('c'),
			},
		},
		Totals: Totals{Members: 2, Bytes: 7},
	}
}

func validReceipt(t *testing.T, manifest Manifest) Receipt {
	t.Helper()
	manifestBytes := mustCanonicalManifest(t, manifest)
	inventory, err := inventoryDigest(manifest.Members)
	if err != nil {
		t.Fatalf("inventoryDigest: %v", err)
	}
	receipt := Receipt{
		SchemaVersion:     ReceiptSchemaV1,
		ManifestSchema:    manifest.SchemaVersion,
		ProjectionSchema:  manifest.ProjectionSchema,
		GeneratorVersion:  manifest.GeneratorVersion,
		BuildState:        manifest.BuildState,
		PredecessorDigest: manifest.PredecessorDigest,
		Qualifications:    append([]Qualification(nil), manifest.Qualifications...),
		TombstoneDigest:   manifest.TombstoneDigest,
		Totals:            manifest.Totals,
		ManifestDigest:    manifestDigest(manifestBytes),
		InventoryDigest:   inventory,
	}
	digest, err := generationDigest(receipt)
	if err != nil {
		t.Fatalf("generationDigest: %v", err)
	}
	receipt.GenerationDigest = digest
	return receipt
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Qualifications = append([]Qualification(nil), manifest.Qualifications...)
	manifest.Members = append([]Member(nil), manifest.Members...)
	return manifest
}

func cloneReceipt(receipt Receipt) Receipt {
	receipt.Qualifications = append([]Qualification(nil), receipt.Qualifications...)
	return receipt
}

func mustCanonicalManifest(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	data, err := canonicalManifest(manifest, Limits{})
	if err != nil {
		t.Fatalf("canonicalManifest: %v", err)
	}
	return data
}

func mustCanonicalReceipt(t *testing.T, receipt Receipt) []byte {
	t.Helper()
	data, err := canonicalReceipt(receipt, Limits{})
	if err != nil {
		t.Fatalf("canonicalReceipt: %v", err)
	}
	return data
}

func marshalUnchecked(t *testing.T, value any) []byte {
	t.Helper()
	data, err := marshalCanonical(value)
	if err != nil {
		t.Fatalf("marshalCanonical: %v", err)
	}
	return data
}

func digestByte(value byte) string {
	return strings.Repeat(string(value), 64)
}

func assertRejected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("invalid value was accepted")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("error %q does not wrap ErrIntegrity", err)
	}
}

func assertReason(t *testing.T, err error, want Reason) {
	t.Helper()
	assertRejected(t, err)
	var integrity interface{ Reason() Reason }
	if !errors.As(err, &integrity) {
		t.Fatalf("error %q has no integrity reason", err)
	}
	if integrity.Reason() != want {
		t.Fatalf("reason = %q, want %q", integrity.Reason(), want)
	}
}

func assertPrivateError(t *testing.T, err error, canaries []string) {
	t.Helper()
	assertRejected(t, err)
	for _, canary := range canaries {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("error exposed private input")
		}
	}
}

func firstByteDifference(left, right []byte) string {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			return "offset differs"
		}
	}
	if len(left) != len(right) {
		return "length differs"
	}
	return ""
}
