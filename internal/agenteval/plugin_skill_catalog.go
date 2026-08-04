package agenteval

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	CodexSkillCatalogFileName = "skill-catalog.v1.json"

	codexSkillCatalogSchemaVersion = 1
	maxCodexSkillCatalogBytes      = 1 << 20
	maxCodexSkillCatalogSkills     = 256
	maxCodexSkillCatalogFiles      = 4096
	maxCodexSkillNameBytes         = 64
	maxCodexSkillFilePathBytes     = 512
	maxCodexSkillFileBytes         = 8 << 20
	maxCodexSkillTreeBytes         = 64 << 20
)

var codexSkillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

//go:embed testdata/released-codex-skill-semantics.v1.json
var releasedCodexSkillSemantics []byte

type CodexSkillCatalog struct {
	SchemaVersion int                      `json:"schema_version"`
	Skills        []CodexSkillCatalogSkill `json:"skills"`
	Files         []CodexSkillCatalogFile  `json:"files"`
}

type CodexSkillCatalogSkill struct {
	Name                    string `json:"name"`
	AllowImplicitInvocation bool   `json:"allow_implicit_invocation"`
}

type CodexSkillCatalogFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type codexSkillSemantics struct {
	SchemaVersion int                      `json:"schema_version"`
	Skills        []CodexSkillCatalogSkill `json:"skills"`
}

type codexSkillPackageHooks struct {
	beforeSkillFileRead func(string)
}

// VerifyCodexSkillPackage decodes the generated companion contract and
// reconciles it against the exact regular-file tree under packageRoot/skills.
// Both the package and skill roots remain pinned while their files are read.
func VerifyCodexSkillPackage(packageRoot string) (CodexSkillCatalog, error) {
	return verifyCodexSkillPackage(packageRoot, codexSkillPackageHooks{})
}

func verifyCodexSkillPackage(packageRoot string, hooks codexSkillPackageHooks) (CodexSkillCatalog, error) {
	var zero CodexSkillCatalog
	packageInfo, err := os.Lstat(packageRoot)
	if err != nil {
		return zero, fmt.Errorf("verify Codex skill package: %w", err)
	}
	if !packageInfo.IsDir() || packageInfo.Mode()&fs.ModeSymlink != 0 {
		return zero, fmt.Errorf("verify Codex skill package: package root is not a regular directory")
	}
	packageHandle, err := os.OpenRoot(packageRoot)
	if err != nil {
		return zero, fmt.Errorf("verify Codex skill package: %w", err)
	}
	defer func() { _ = packageHandle.Close() }()
	openedPackageInfo, err := packageHandle.Stat(".")
	if err != nil || !os.SameFile(packageInfo, openedPackageInfo) || !sameSyntheticRootInfo(packageInfo, openedPackageInfo) {
		return zero, fmt.Errorf("verify Codex skill package: package root changed")
	}

	manifestPath := filepath.Join(packageRoot, CodexSkillCatalogFileName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return zero, fmt.Errorf("verify Codex skill package: catalog unavailable: %w", err)
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&fs.ModeSymlink != 0 {
		return zero, fmt.Errorf("verify Codex skill package: catalog is not a regular file")
	}
	manifestData, err := readStableRootFile(packageHandle, CodexSkillCatalogFileName, manifestInfo, maxCodexSkillCatalogBytes)
	if err != nil {
		return zero, fmt.Errorf("verify Codex skill package: read catalog: %w", err)
	}
	catalog, err := decodeCodexSkillCatalog(manifestData)
	if err != nil {
		return zero, fmt.Errorf("verify Codex skill package: decode catalog: %w", err)
	}

	skillsPath := filepath.Join(packageRoot, "skills")
	if err := reconcileCodexSkillTree(catalog, skillsPath, hooks); err != nil {
		return zero, fmt.Errorf("verify Codex skill package: %w", err)
	}
	finalPackageInfo, err := os.Lstat(packageRoot)
	if err != nil || !os.SameFile(packageInfo, finalPackageInfo) || !sameSyntheticRootInfo(packageInfo, finalPackageInfo) {
		return zero, fmt.Errorf("verify Codex skill package: package root changed")
	}
	return catalog, nil
}

func decodeCodexSkillCatalog(data []byte) (CodexSkillCatalog, error) {
	var catalog CodexSkillCatalog
	if len(data) == 0 || len(data) > maxCodexSkillCatalogBytes {
		return catalog, fmt.Errorf("catalog size is outside the supported bound")
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return catalog, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return catalog, err
	}
	if err := requireExactJSONMembers(document, "skill catalog", []string{"schema_version", "skills", "files"}); err != nil {
		return catalog, err
	}
	var skillDocuments []map[string]json.RawMessage
	if err := json.Unmarshal(document["skills"], &skillDocuments); err != nil {
		return catalog, fmt.Errorf("skills: %w", err)
	}
	for index, skill := range skillDocuments {
		if err := requireExactJSONMembers(skill, fmt.Sprintf("skill[%d]", index), []string{"name", "allow_implicit_invocation"}); err != nil {
			return catalog, err
		}
	}
	var fileDocuments []map[string]json.RawMessage
	if err := json.Unmarshal(document["files"], &fileDocuments); err != nil {
		return catalog, fmt.Errorf("files: %w", err)
	}
	for index, file := range fileDocuments {
		if err := requireExactJSONMembers(file, fmt.Sprintf("file[%d]", index), []string{"path", "sha256"}); err != nil {
			return catalog, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return catalog, err
	}
	if err := catalog.validate(); err != nil {
		return CodexSkillCatalog{}, err
	}
	return catalog, nil
}

func (catalog CodexSkillCatalog) validate() error {
	if catalog.SchemaVersion != codexSkillCatalogSchemaVersion {
		return fmt.Errorf("schema_version must be %d", codexSkillCatalogSchemaVersion)
	}
	if err := validateCodexSkillSemantics(catalog.Skills); err != nil {
		return err
	}
	if len(catalog.Files) == 0 || len(catalog.Files) > maxCodexSkillCatalogFiles {
		return fmt.Errorf("files count is outside the supported bound")
	}
	for index, file := range catalog.Files {
		if err := validateCodexSkillFilePath(file.Path); err != nil {
			return fmt.Errorf("file[%d] path: %w", index, err)
		}
		if !isLowercaseSHA256(file.SHA256) {
			return fmt.Errorf("file[%d] sha256 is invalid", index)
		}
		if index > 0 && catalog.Files[index-1].Path >= file.Path {
			return fmt.Errorf("files must be sorted and unique")
		}
	}
	semanticDirectories := make(map[string]struct{}, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		semanticDirectories[skill.Name] = struct{}{}
		if _, ok := fileByPath(catalog.Files, skill.Name+"/SKILL.md"); !ok {
			return fmt.Errorf("skill %q has no SKILL.md", skill.Name)
		}
	}
	fileDirectories := make(map[string]struct{}, len(catalog.Skills))
	for _, file := range catalog.Files {
		directory, _, found := strings.Cut(file.Path, "/")
		if !found {
			return fmt.Errorf("file path must belong to a skill directory")
		}
		fileDirectories[directory] = struct{}{}
	}
	if !sameStringSet(semanticDirectories, fileDirectories) {
		return fmt.Errorf("skill names and file directories differ")
	}
	return nil
}

func validateCodexSkillSemantics(skills []CodexSkillCatalogSkill) error {
	if len(skills) == 0 || len(skills) > maxCodexSkillCatalogSkills {
		return fmt.Errorf("skills count is outside the supported bound")
	}
	for index, skill := range skills {
		if len(skill.Name) == 0 || len(skill.Name) > maxCodexSkillNameBytes || !codexSkillNamePattern.MatchString(skill.Name) {
			return fmt.Errorf("skill[%d] name is invalid", index)
		}
		if index > 0 && skills[index-1].Name >= skill.Name {
			return fmt.Errorf("skills must be sorted and unique")
		}
	}
	return nil
}

func fileByPath(files []CodexSkillCatalogFile, name string) (CodexSkillCatalogFile, bool) {
	for _, file := range files {
		if file.Path == name {
			return file, true
		}
	}
	return CodexSkillCatalogFile{}, false
}

func sameStringSet(first, second map[string]struct{}) bool {
	if len(first) != len(second) {
		return false
	}
	for value := range first {
		if _, ok := second[value]; !ok {
			return false
		}
	}
	return true
}

// VerifyReleasedCodexSkillSemantics gates an already verified package against
// the frozen schema-v1 skill names and implicit-invocation policy. Callers can
// apply this release-compatibility decision independently of structural checks.
func VerifyReleasedCodexSkillSemantics(catalog CodexSkillCatalog) error {
	pinned, err := decodeReleasedCodexSkillSemantics(releasedCodexSkillSemantics)
	if err != nil {
		return fmt.Errorf("embedded released projection is invalid: %w", err)
	}
	if catalog.SchemaVersion != pinned.SchemaVersion || len(catalog.Skills) != len(pinned.Skills) {
		return fmt.Errorf("catalog differs from released schema-v1 semantics")
	}
	for index := range catalog.Skills {
		if catalog.Skills[index] != pinned.Skills[index] {
			return fmt.Errorf("catalog differs from released schema-v1 semantics")
		}
	}
	return nil
}

func decodeReleasedCodexSkillSemantics(data []byte) (codexSkillSemantics, error) {
	var semantics codexSkillSemantics
	if len(data) == 0 || len(data) > maxCodexSkillCatalogBytes {
		return semantics, fmt.Errorf("semantic projection size is outside the supported bound")
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return semantics, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return semantics, err
	}
	if err := requireExactJSONMembers(document, "released skill semantics", []string{"schema_version", "skills"}); err != nil {
		return semantics, err
	}
	var skillDocuments []map[string]json.RawMessage
	if err := json.Unmarshal(document["skills"], &skillDocuments); err != nil {
		return semantics, fmt.Errorf("skills: %w", err)
	}
	for index, skill := range skillDocuments {
		if err := requireExactJSONMembers(skill, fmt.Sprintf("skill[%d]", index), []string{"name", "allow_implicit_invocation"}); err != nil {
			return semantics, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&semantics); err != nil {
		return semantics, err
	}
	if semantics.SchemaVersion != codexSkillCatalogSchemaVersion {
		return semantics, fmt.Errorf("schema_version must be %d", codexSkillCatalogSchemaVersion)
	}
	if err := validateCodexSkillSemantics(semantics.Skills); err != nil {
		return semantics, err
	}
	return semantics, nil
}

func validateCodexSkillFilePath(value string) error {
	if len(value) == 0 || len(value) > maxCodexSkillFilePathBytes {
		return fmt.Errorf("size is outside the supported bound")
	}
	if strings.Contains(value, `\`) || strings.ContainsRune(value, 0) || path.IsAbs(value) || value == "." || path.Clean(value) != value {
		return fmt.Errorf("must be a clean slash-relative path")
	}
	if first, _, _ := strings.Cut(value, "/"); first == ".." {
		return fmt.Errorf("must not traverse outside the skill root")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func isLowercaseSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func reconcileCodexSkillTree(catalog CodexSkillCatalog, skillsRoot string, hooks codexSkillPackageHooks) error {
	if err := catalog.validate(); err != nil {
		return fmt.Errorf("catalog contract: %w", err)
	}
	rootInfo, err := os.Lstat(skillsRoot)
	if err != nil {
		return fmt.Errorf("skill root unavailable: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("skill root is not a regular directory")
	}
	root, err := os.OpenRoot(skillsRoot)
	if err != nil {
		return fmt.Errorf("open skill root: %w", err)
	}
	defer func() { _ = root.Close() }()
	openedRootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRootInfo) || !sameSyntheticRootInfo(rootInfo, openedRootInfo) {
		return fmt.Errorf("skill root changed")
	}

	expected := make(map[string]CodexSkillCatalogFile, len(catalog.Files))
	for _, file := range catalog.Files {
		expected[file.Path] = file
	}
	seen := make(map[string]struct{}, len(catalog.Files))
	semanticDirectories := make(map[string]struct{}, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		semanticDirectories[skill.Name] = struct{}{}
	}
	observedDirectories := make(map[string]struct{}, len(catalog.Skills))
	var totalBytes int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if err := validateCodexSkillFilePath(name); err != nil {
			return fmt.Errorf("skill tree contains an invalid path")
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("skill tree contains a symbolic link")
		}
		if entry.IsDir() {
			if !strings.Contains(name, "/") {
				observedDirectories[name] = struct{}{}
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill tree contains a special file")
		}
		want, ok := expected[name]
		if !ok {
			return fmt.Errorf("skill tree contains an unexpected file")
		}
		if hooks.beforeSkillFileRead != nil {
			hooks.beforeSkillFileRead(name)
		}
		data, err := readStableRootFile(root, filepath.FromSlash(name), info, maxCodexSkillFileBytes)
		if err != nil {
			return fmt.Errorf("read skill file: %w", err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxCodexSkillTreeBytes {
			return fmt.Errorf("skill tree exceeds the supported byte bound")
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != want.SHA256 {
			return fmt.Errorf("skill tree contains a changed file")
		}
		seen[name] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile skill tree: %w", err)
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("reconcile skill tree: catalog file is missing")
	}
	if !sameStringSet(semanticDirectories, observedDirectories) {
		return fmt.Errorf("reconcile skill tree: top-level skill directories differ from catalog semantics")
	}
	finalOpenedRootInfo, openedErr := root.Stat(".")
	finalRootInfo, pathErr := os.Lstat(skillsRoot)
	if openedErr != nil || pathErr != nil || !os.SameFile(rootInfo, finalOpenedRootInfo) ||
		!os.SameFile(rootInfo, finalRootInfo) || !sameSyntheticRootInfo(rootInfo, finalOpenedRootInfo) ||
		!sameSyntheticRootInfo(rootInfo, finalRootInfo) {
		return fmt.Errorf("reconcile skill tree: skill root changed")
	}
	return nil
}
