package agentskills

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// MaxTreeDepth bounds the number of relative path components admitted from
	// any selected root.
	MaxTreeDepth = 64

	// StructuralAdmissionVersion identifies the closed structural policy and
	// record vocabulary below.
	StructuralAdmissionVersion = 1
)

// StructuralFindingCode is a content-free, closed refusal identity.
type StructuralFindingCode string

const (
	FindingInvalidRoot           StructuralFindingCode = "invalid_root"
	FindingRootSymlink           StructuralFindingCode = "root_symlink"
	FindingEntrySymlink          StructuralFindingCode = "entry_symlink"
	FindingSpecialFile           StructuralFindingCode = "special_file"
	FindingInvalidLocation       StructuralFindingCode = "invalid_location"
	FindingDuplicateFileIdentity StructuralFindingCode = "duplicate_file_identity"
	FindingEntryCountLimit       StructuralFindingCode = "entry_count_limit"
	FindingEntryDepthLimit       StructuralFindingCode = "entry_depth_limit"
	FindingPathBytesLimit        StructuralFindingCode = "path_bytes_limit"
	FindingFileBytesLimit        StructuralFindingCode = "file_bytes_limit"
	FindingTreeBytesLimit        StructuralFindingCode = "tree_bytes_limit"
	FindingEntryUnreadable       StructuralFindingCode = "entry_unreadable"
	FindingMountBoundary         StructuralFindingCode = "mount_boundary"
	FindingPlatformUnsupported   StructuralFindingCode = "platform_unsupported"
	FindingRootChanged           StructuralFindingCode = "root_changed"
	FindingEntryChanged          StructuralFindingCode = "entry_changed"
	FindingTreeChanged           StructuralFindingCode = "tree_changed"
	FindingSkillManifestMissing  StructuralFindingCode = "skill_manifest_missing"
	FindingSkillManifestInvalid  StructuralFindingCode = "skill_manifest_invalid"
	FindingEvalManifestMissing   StructuralFindingCode = "eval_manifest_missing"
	FindingEvalManifestInvalid   StructuralFindingCode = "eval_manifest_invalid"
	FindingEvalReferenceMissing  StructuralFindingCode = "eval_reference_missing"
)

// StructuralFindingClass separates stable policy/input refusal from observed
// source instability without exposing an ambient error.
type StructuralFindingClass string

const (
	FindingPolicyRefusal     StructuralFindingClass = "policy_refusal"
	FindingSourceInstability StructuralFindingClass = "source_instability"
)

// StructuralFinding contains only a closed code and a safe logical location.
// Location is rooted in skill/, evals/, or previous/ and never contains a host
// path.
type StructuralFinding struct {
	Code     StructuralFindingCode
	Class    StructuralFindingClass
	Location string
}

// StructuralEntryKind is the closed set of admitted filesystem entry types.
type StructuralEntryKind string

const (
	StructuralEntryDirectory StructuralEntryKind = "directory"
	StructuralEntryRegular   StructuralEntryKind = "regular"
)

// StructuralModeClass classifies observed portable mode bits. It grants no
// authority and does not prove that a file can execute on the host.
type StructuralModeClass string

const (
	StructuralModeDirectory         StructuralModeClass = "directory"
	StructuralModeRegular           StructuralModeClass = "regular"
	StructuralModeExecutableRegular StructuralModeClass = "executable_regular"
)

// StructuralLimits are the exact bounds bound into PolicySHA256.
type StructuralLimits struct {
	MaxEntries   uint32
	MaxDepth     uint32
	MaxPathBytes uint32
	MaxFileBytes uint64
	MaxTreeBytes uint64
}

// StructuralEntry is a content-minimized canonical projection. EntrySHA256
// binds every field, while ContentSHA256 is present only for regular files.
type StructuralEntry struct {
	Location          string
	EntrySHA256       string
	ContentSHA256     string
	Kind              StructuralEntryKind
	ModeClass         StructuralModeClass
	SizeBytes         uint64
	Executable        bool
	ReferencedBySkill bool
	ReferencedByEvals bool
}

// StructuralAdmission is a deterministic zero-execution decision. A refusal
// contains no partial entries or tree identity. RuntimeSafetyProven is always
// false: structural admission cannot prove sandbox, interpreter, ACL, tool,
// network, credential, or runner behavior.
type StructuralAdmission struct {
	Version             uint32
	PolicySHA256        string
	TreeSHA256          string
	Limits              StructuralLimits
	Admitted            bool
	RuntimeSafetyProven bool
	Entries             []StructuralEntry
	Findings            []StructuralFinding
}

// BlocksExecution reports the structural decision only. A false result does
// not grant execution authority; later policy and runtime controls still own
// that decision.
func (admission StructuralAdmission) BlocksExecution() bool { return !admission.Admitted }

// AdmitStructure captures and classifies the selected Agent Skills roots. It
// validates only exact structural roles: SKILL.md, the selected evals.json,
// and paths explicitly listed by evals[].files. Mentions in SKILL.md prose are
// intentionally not interpreted as references. Secure capture currently
// requires Linux openat2 and procfs descriptor-bridge support; other platforms
// fail closed with a platform_unsupported finding.
func AdmitStructure(request ImportRequest) (StructuralAdmission, error) {
	return admitStructureWithHooks(request, structuralAdmissionHooks{})
}

type structuralAdmissionHooks struct {
	skill    structuralCaptureHooks
	evals    structuralCaptureHooks
	previous structuralCaptureHooks
	limits   *StructuralLimits
}

func admitStructureWithHooks(request ImportRequest, hooks structuralAdmissionHooks) (StructuralAdmission, error) {
	base := newStructuralAdmissionWithLimits(hooks.limits)
	if !validAdmissionRequest(request) {
		return StructuralAdmission{}, contractError(ErrorInvalidRequest, nil)
	}
	budget := newStructuralCaptureBudget(base.Limits)

	skillTree, refusal := captureStructuralTree(request.SkillRoot, "skill", budget, hooks.skill)
	if refusal != nil {
		return refuseStructuralSource(base, refusal), nil
	}
	skillDocument, ok := skillTree.files["SKILL.md"]
	if !ok {
		return refuseStructuralAdmission(base, FindingSkillManifestMissing, "skill", "SKILL.md"), nil
	}
	metadata, err := parseSkillMetadata(skillDocument.data)
	if err != nil {
		return refuseStructuralAdmission(base, FindingSkillManifestInvalid, "skill", "SKILL.md"), nil
	}

	evalSelection, refusal, err := selectAdmissionEvals(request, skillTree, budget, hooks.evals)
	if refusal != nil {
		return refuseStructuralSource(base, refusal), nil
	}
	if err != nil {
		return StructuralAdmission{}, err
	}
	manifest, ok := evalSelection.tree.files[evalSelection.manifestPath]
	if !ok {
		return refuseStructuralAdmission(base, FindingEvalManifestMissing,
			evalSelection.namespace, evalSelection.manifestPath), nil
	}
	decoded, err := decodeEvals(manifest.data, request.Format)
	if err != nil {
		return refuseStructuralAdmission(base, FindingEvalManifestInvalid,
			evalSelection.namespace, evalSelection.manifestPath), nil
	}
	if decoded.skillName != metadata.name {
		return refuseStructuralAdmission(base, FindingEvalManifestInvalid,
			evalSelection.namespace, evalSelection.manifestPath), nil
	}

	evalReferences := make(map[string]struct{})
	for _, source := range decoded.cases {
		for _, location := range source.files {
			if _, exists := skillTree.files[location]; !exists {
				return refuseStructuralAdmission(base, FindingEvalReferenceMissing, "skill", location), nil
			}
			evalReferences[location] = struct{}{}
		}
	}

	sources := []structuralSource{{
		namespace: "skill", tree: skillTree, skillManifest: "SKILL.md",
		evalManifest: evalSelection.skillManifestPath, evalReferences: evalReferences,
	}}
	if evalSelection.namespace == "evals" {
		sources = append(sources, structuralSource{
			namespace: "evals", tree: evalSelection.tree, evalManifest: evalSelection.manifestPath,
		})
	}
	if request.Baseline == BaselinePreviousSkill {
		previousTree, previousRefusal := captureStructuralTree(
			request.PreviousSkillRoot, "previous", budget, hooks.previous,
		)
		if previousRefusal != nil {
			return refuseStructuralSource(base, previousRefusal), nil
		}
		previousDocument, ok := previousTree.files["SKILL.md"]
		if !ok {
			return refuseStructuralAdmission(base, FindingSkillManifestMissing, "previous", "SKILL.md"), nil
		}
		previousMetadata, err := parseSkillMetadata(previousDocument.data)
		if err != nil || previousMetadata.name != metadata.name {
			return refuseStructuralAdmission(base, FindingSkillManifestInvalid, "previous", "SKILL.md"), nil
		}
		sources = append(sources, structuralSource{
			namespace: "previous", tree: previousTree, skillManifest: "SKILL.md",
		})
	}

	entries, refusal := structuralEntries(sources, base.Limits)
	if refusal != nil {
		return refuseStructuralSource(base, refusal), nil
	}
	base.Admitted = true
	base.Entries = entries
	base.TreeSHA256 = digestStructuralTree(base.PolicySHA256, entries)
	return base, nil
}

type admissionEvalSelection struct {
	namespace         string
	tree              capturedTree
	manifestPath      string
	skillManifestPath string
}

func selectAdmissionEvals(request ImportRequest, skillTree capturedTree, budget *structuralCaptureBudget,
	hooks structuralCaptureHooks,
) (admissionEvalSelection, *structuralSourceRefusal, error) {
	evalRoot := request.EvalRoot
	if evalRoot == "" {
		evalRoot = filepath.Join(request.SkillRoot, "evals")
	}
	skillAbsolute, skillErr := filepath.Abs(request.SkillRoot)
	evalAbsolute, evalErr := filepath.Abs(evalRoot)
	if skillErr != nil || evalErr != nil {
		return admissionEvalSelection{}, nil, contractError(ErrorInvalidRequest, nil)
	}
	relative, relErr := filepath.Rel(skillAbsolute, evalAbsolute)
	insideSkill := relErr == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if insideSkill {
		prefix := filepath.ToSlash(relative)
		if !validSourcePath(prefix) {
			return admissionEvalSelection{}, nil, contractError(ErrorInvalidRequest, nil)
		}
		manifest := prefix + "/evals.json"
		return admissionEvalSelection{
			namespace: "skill", tree: skillTree, manifestPath: manifest, skillManifestPath: manifest,
		}, nil, nil
	}
	fromEval, fromEvalErr := filepath.Rel(evalAbsolute, skillAbsolute)
	evalContainsSkill := fromEvalErr == nil && fromEval != "." && fromEval != ".." &&
		!strings.HasPrefix(fromEval, ".."+string(filepath.Separator))
	if relative == "." || evalContainsSkill {
		return admissionEvalSelection{}, nil, contractError(ErrorInvalidRequest, nil)
	}
	evalTree, refusal := captureStructuralTree(evalRoot, "evals", budget, hooks)
	if refusal != nil {
		return admissionEvalSelection{}, refusal, nil
	}
	return admissionEvalSelection{namespace: "evals", tree: evalTree, manifestPath: "evals.json"}, nil, nil
}

type structuralSource struct {
	namespace      string
	tree           capturedTree
	skillManifest  string
	evalManifest   string
	evalReferences map[string]struct{}
}

type structuralEntrySource struct {
	namespace string
	entry     capturedEntry
	location  string
}

type structuralSourceRefusal struct {
	code      StructuralFindingCode
	class     StructuralFindingClass
	namespace string
	location  string
}

func structuralEntries(sources []structuralSource, limits StructuralLimits) ([]StructuralEntry, *structuralSourceRefusal) {
	var raw []structuralEntrySource
	var regular []structuralEntrySource
	var totalBytes uint64
	for _, source := range sources {
		for _, entry := range source.tree.entries {
			if uint64(len(raw)) >= uint64(limits.MaxEntries) {
				return nil, &structuralSourceRefusal{
					code: FindingEntryCountLimit, class: FindingPolicyRefusal,
					namespace: source.namespace, location: entry.path,
				}
			}
			current := structuralEntrySource{
				namespace: source.namespace, entry: entry,
				location: qualifyStructuralLocation(source.namespace, entry.path),
			}
			if !entry.isDir {
				if totalBytes > limits.MaxTreeBytes || uint64(len(entry.data)) > limits.MaxTreeBytes-totalBytes {
					return nil, &structuralSourceRefusal{
						code: FindingTreeBytesLimit, class: FindingPolicyRefusal,
						namespace: source.namespace, location: entry.path,
					}
				}
				for _, previous := range regular {
					if os.SameFile(previous.entry.info, entry.info) {
						return nil, &structuralSourceRefusal{
							code: FindingDuplicateFileIdentity, class: FindingPolicyRefusal,
							namespace: source.namespace, location: entry.path,
						}
					}
				}
				regular = append(regular, current)
				totalBytes += uint64(len(entry.data))
			}
			raw = append(raw, current)
		}
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].location < raw[j].location })

	entries := make([]StructuralEntry, 0, len(raw))
	for _, item := range raw {
		source := findStructuralSource(sources, item.namespace)
		entry := projectStructuralEntry(item, source)
		entry.EntrySHA256 = digestStructuralEntry(entry)
		entries = append(entries, entry)
	}
	return entries, nil
}

func findStructuralSource(sources []structuralSource, namespace string) structuralSource {
	for _, source := range sources {
		if source.namespace == namespace {
			return source
		}
	}
	return structuralSource{}
}

func projectStructuralEntry(source structuralEntrySource, owner structuralSource) StructuralEntry {
	entry := StructuralEntry{
		Location:          source.location,
		ReferencedBySkill: source.entry.path == owner.skillManifest,
		ReferencedByEvals: source.entry.path == owner.evalManifest,
	}
	if _, referenced := owner.evalReferences[source.entry.path]; referenced {
		entry.ReferencedByEvals = true
	}
	if source.entry.isDir {
		entry.Kind = StructuralEntryDirectory
		entry.ModeClass = StructuralModeDirectory
		return entry
	}
	entry.Kind = StructuralEntryRegular
	entry.SizeBytes = uint64(len(source.entry.data))
	entry.ContentSHA256 = source.entry.digest
	entry.Executable = source.entry.info.Mode().Perm()&0o111 != 0
	entry.ModeClass = StructuralModeRegular
	if entry.Executable {
		entry.ModeClass = StructuralModeExecutableRegular
	}
	return entry
}

func newStructuralAdmission() StructuralAdmission {
	return newStructuralAdmissionWithLimits(nil)
}

func newStructuralAdmissionWithLimits(override *StructuralLimits) StructuralAdmission {
	limits := StructuralLimits{
		MaxEntries: MaxTreeEntries, MaxDepth: MaxTreeDepth, MaxPathBytes: MaxPathBytes,
		MaxFileBytes: MaxFileBytes, MaxTreeBytes: MaxTreeBytes,
	}
	if override != nil {
		limits = *override
	}
	return StructuralAdmission{
		Version: StructuralAdmissionVersion, PolicySHA256: digestStructuralPolicy(limits),
		Limits: limits, RuntimeSafetyProven: false,
	}
}

func refuseStructuralAdmission(base StructuralAdmission, code StructuralFindingCode, namespace, location string) StructuralAdmission {
	return refuseStructuralAdmissionWithClass(base, code, structuralFindingClass(code), namespace, location)
}

func refuseStructuralSource(base StructuralAdmission, refusal *structuralSourceRefusal) StructuralAdmission {
	if refusal == nil {
		return refuseStructuralAdmission(base, FindingEntryUnreadable, "", ".")
	}
	return refuseStructuralAdmissionWithClass(
		base, refusal.code, refusal.class, refusal.namespace, refusal.location,
	)
}

func refuseStructuralAdmissionWithClass(base StructuralAdmission, code StructuralFindingCode,
	class StructuralFindingClass, namespace, location string,
) StructuralAdmission {
	base.Admitted = false
	base.TreeSHA256 = ""
	base.Entries = nil
	base.Findings = []StructuralFinding{{
		Code: code, Class: class,
		Location: qualifyStructuralLocation(namespace, location),
	}}
	return base
}

func structuralFindingClass(code StructuralFindingCode) StructuralFindingClass {
	switch code {
	case FindingRootChanged, FindingEntryChanged, FindingTreeChanged:
		return FindingSourceInstability
	default:
		return FindingPolicyRefusal
	}
}

func qualifyStructuralLocation(namespace, location string) string {
	if namespace != "skill" && namespace != "evals" && namespace != "previous" {
		return "."
	}
	if location == "." || !validSourcePath(location) {
		return namespace
	}
	return namespace + "/" + location
}

func validAdmissionRequest(request ImportRequest) bool {
	return request.SkillRoot != "" &&
		(request.Format == FormatAuto || request.Format == FormatAgentSkillsGuideV1 || request.Format == FormatAnthropicSkillCreatorV1) &&
		(request.Baseline == BaselineNoSkill || request.Baseline == BaselinePreviousSkill) &&
		(request.Baseline != BaselinePreviousSkill || request.PreviousSkillRoot != "") &&
		(request.Baseline != BaselineNoSkill || request.PreviousSkillRoot == "")
}

func digestStructuralEntry(entry StructuralEntry) string {
	builder := newDigestBuilder("structural-entry")
	builder.addString(strconv.FormatUint(StructuralAdmissionVersion, 10))
	builder.addString(entry.Location)
	builder.addString(entry.ContentSHA256)
	builder.addString(string(entry.Kind))
	builder.addString(string(entry.ModeClass))
	builder.addString(strconv.FormatUint(entry.SizeBytes, 10))
	builder.addString(strconv.FormatBool(entry.Executable))
	builder.addString(strconv.FormatBool(entry.ReferencedBySkill))
	builder.addString(strconv.FormatBool(entry.ReferencedByEvals))
	return builder.sum()
}

func digestStructuralTree(policySHA256 string, entries []StructuralEntry) string {
	builder := newDigestBuilder("structural-tree")
	builder.addString(strconv.FormatUint(StructuralAdmissionVersion, 10))
	builder.addString(policySHA256)
	for _, entry := range entries {
		builder.addString(entry.EntrySHA256)
	}
	return builder.sum()
}

func digestStructuralPolicy(limits StructuralLimits) string {
	builder := newDigestBuilder("structural-policy")
	builder.addString(strconv.FormatUint(StructuralAdmissionVersion, 10))
	for _, value := range []uint64{
		uint64(limits.MaxEntries), uint64(limits.MaxDepth), uint64(limits.MaxPathBytes),
		limits.MaxFileBytes, limits.MaxTreeBytes,
	} {
		builder.addString(strconv.FormatUint(value, 10))
	}
	for _, value := range structuralPolicyVocabulary() {
		builder.addString(value)
	}
	return builder.sum()
}

func structuralPolicyVocabulary() []string {
	values := []string{
		string(StructuralEntryDirectory), string(StructuralEntryRegular),
		string(StructuralModeDirectory), string(StructuralModeRegular), string(StructuralModeExecutableRegular),
		string(FindingPolicyRefusal), string(FindingSourceInstability),
	}
	for _, code := range []StructuralFindingCode{
		FindingInvalidRoot, FindingRootSymlink, FindingEntrySymlink, FindingSpecialFile,
		FindingInvalidLocation, FindingDuplicateFileIdentity, FindingEntryCountLimit,
		FindingEntryDepthLimit, FindingPathBytesLimit, FindingFileBytesLimit,
		FindingTreeBytesLimit, FindingEntryUnreadable, FindingMountBoundary,
		FindingPlatformUnsupported, FindingRootChanged,
		FindingEntryChanged, FindingTreeChanged, FindingSkillManifestMissing,
		FindingSkillManifestInvalid, FindingEvalManifestMissing, FindingEvalManifestInvalid,
		FindingEvalReferenceMissing,
	} {
		values = append(values, string(code))
	}
	sort.Strings(values)
	return values
}

type structuralRefusal struct {
	code     StructuralFindingCode
	location string
}

func (refusal *structuralRefusal) Error() string { return string(refusal.code) }

func newStructuralRefusal(code StructuralFindingCode, location string) error {
	if location != "." && !validSourcePath(location) {
		location = "."
	}
	return &structuralRefusal{code: code, location: location}
}

func structuralCaptureError(err error) (StructuralFindingCode, string) {
	var refusal *structuralRefusal
	if !errors.As(err, &refusal) {
		return FindingEntryUnreadable, "."
	}
	return refusal.code, refusal.location
}

func structuralPathBytes(location string) uint32 {
	var size uint32
	for range []byte(location) {
		size++
	}
	return size
}

func structuralEntryDepth(location string) uint32 {
	if location == "." {
		return 0
	}
	depth := uint32(1)
	for _, character := range []byte(location) {
		if character == '/' {
			depth++
		}
	}
	return depth
}
