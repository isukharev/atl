package agentskills

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestAdmitStructureProducesCanonicalContentMinimizedRecord(t *testing.T) {
	requireStructuralAdmissionSupported(t)
	root := writeAdmissionFixture(t)
	script := filepath.Join(root, "scripts", "unlisted.sh")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatalf("Chmod(): %v", err)
	}
	marker := filepath.Join(root, "executed")

	request := admissionRequest(root)
	first, err := AdmitStructure(request)
	if err != nil {
		t.Fatalf("AdmitStructure() error = %v", err)
	}
	second, err := AdmitStructure(request)
	if err != nil {
		t.Fatalf("second AdmitStructure() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated structural admission was not deterministic")
	}
	if !first.Admitted || first.BlocksExecution() || first.RuntimeSafetyProven || len(first.Findings) != 0 {
		t.Fatalf("admission decision = %#v", first)
	}
	if first.Version != StructuralAdmissionVersion || !validDigest(first.PolicySHA256) || !validDigest(first.TreeSHA256) {
		t.Fatalf("admission identities = %#v", first)
	}
	if first.Limits != (StructuralLimits{
		MaxEntries: MaxTreeEntries, MaxDepth: MaxTreeDepth, MaxPathBytes: MaxPathBytes,
		MaxFileBytes: MaxFileBytes, MaxTreeBytes: MaxTreeBytes,
	}) {
		t.Fatalf("admission limits = %#v", first.Limits)
	}

	entries := structuralEntriesByLocation(t, first.Entries)
	for _, location := range []string{
		"skill/SKILL.md", "skill/evals", "skill/evals/evals.json",
		"skill/fixtures", "skill/fixtures/input.txt", "skill/scripts", "skill/scripts/unlisted.sh",
	} {
		if _, ok := entries[location]; !ok {
			t.Fatalf("missing classified entry %q in %#v", location, first.Entries)
		}
	}
	if entry := entries["skill/SKILL.md"]; !entry.ReferencedBySkill || entry.ReferencedByEvals {
		t.Fatalf("skill manifest references = %#v", entry)
	}
	if entry := entries["skill/evals/evals.json"]; entry.ReferencedBySkill || !entry.ReferencedByEvals {
		t.Fatalf("eval manifest references = %#v", entry)
	}
	if entry := entries["skill/fixtures/input.txt"]; entry.ReferencedBySkill || !entry.ReferencedByEvals {
		t.Fatalf("eval input references = %#v", entry)
	}
	if entry := entries["skill/scripts/unlisted.sh"]; entry.ReferencedBySkill || entry.ReferencedByEvals {
		t.Fatalf("prose-only script was treated as an exact reference: %#v", entry)
	}
	if runtime.GOOS != "windows" {
		entry := entries["skill/scripts/unlisted.sh"]
		if !entry.Executable || entry.ModeClass != StructuralModeExecutableRegular {
			t.Fatalf("executable classification = %#v", entry)
		}
	}
	for _, entry := range first.Entries {
		if !validDigest(entry.EntrySHA256) {
			t.Fatalf("entry identity = %#v", entry)
		}
		if entry.Kind == StructuralEntryDirectory && (entry.ContentSHA256 != "" || entry.SizeBytes != 0) {
			t.Fatalf("directory exposed file identity = %#v", entry)
		}
	}
	rendered := fmt.Sprintf("%#v", first)
	if strings.Contains(rendered, root) || strings.Contains(rendered, "do-not-expose-source-text") {
		t.Fatalf("admission leaked host path or source content: %s", rendered)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admission executed bundled script: %v", err)
	}
}

func TestStructuralAdmissionDoesNotChangeImportCompatibility(t *testing.T) {
	t.Run("hard links remain importable", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		if err := os.Link(filepath.Join(root, "fixtures", "input.txt"),
			filepath.Join(root, "fixtures", "linked.txt")); err != nil {
			t.Skipf("hard link unavailable: %v", err)
		}
		if _, err := Import(admissionRequest(root)); err != nil {
			t.Fatalf("Import() compatibility error = %v", err)
		}
	})

	t.Run("deep trees remain importable", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		parts := make([]string, MaxTreeDepth+1)
		for index := range parts {
			parts[index] = "d"
		}
		if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0o700); err != nil {
			t.Fatalf("MkdirAll(): %v", err)
		}
		if _, err := Import(admissionRequest(root)); err != nil {
			t.Fatalf("Import() compatibility error = %v", err)
		}
	})

	t.Run("ambient causes remain inspectable", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		_, err := Import(admissionRequest(missing))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Import() error = %v, want inspectable os.ErrNotExist", err)
		}
	})
}

func TestAdmitStructureBindsObservedExecutableModeWithoutGrantingSafety(t *testing.T) {
	requireStructuralAdmissionSupported(t)
	if runtime.GOOS == "windows" {
		t.Skip("portable executable mode bits are not exposed on Windows")
	}
	root := writeAdmissionFixture(t)
	script := filepath.Join(root, "scripts", "unlisted.sh")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatalf("Chmod executable: %v", err)
	}
	executable, err := AdmitStructure(admissionRequest(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o600); err != nil {
		t.Fatalf("Chmod regular: %v", err)
	}
	regular, err := AdmitStructure(admissionRequest(root))
	if err != nil {
		t.Fatal(err)
	}
	first := structuralEntriesByLocation(t, executable.Entries)["skill/scripts/unlisted.sh"]
	second := structuralEntriesByLocation(t, regular.Entries)["skill/scripts/unlisted.sh"]
	if !first.Executable || second.Executable || first.ModeClass == second.ModeClass ||
		first.ContentSHA256 != second.ContentSHA256 || executable.TreeSHA256 == regular.TreeSHA256 ||
		executable.PolicySHA256 != regular.PolicySHA256 || executable.RuntimeSafetyProven || regular.RuntimeSafetyProven {
		t.Fatalf("mode-bound admissions = %#v / %#v", executable, regular)
	}
}

func TestAdmitStructureClassifiesExternalEvalsAndPreviousSkillOnce(t *testing.T) {
	requireStructuralAdmissionSupported(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), admissionSkillDocument())
	writeFile(t, filepath.Join(root, "fixtures", "input.txt"), "synthetic\n")
	evals := t.TempDir()
	writeFile(t, filepath.Join(evals, "evals.json"), admissionEvalsDocument())
	previous := t.TempDir()
	writeFile(t, filepath.Join(previous, "SKILL.md"), admissionSkillDocument())
	writeFile(t, filepath.Join(previous, "notes.txt"), "previous\n")

	result, err := AdmitStructure(ImportRequest{
		SkillRoot: root, EvalRoot: evals, PreviousSkillRoot: previous,
		Format: FormatAgentSkillsGuideV1, Baseline: BaselinePreviousSkill,
	})
	if err != nil {
		t.Fatalf("AdmitStructure() error = %v", err)
	}
	if !result.Admitted {
		t.Fatalf("admission = %#v", result)
	}
	entries := structuralEntriesByLocation(t, result.Entries)
	for _, location := range []string{
		"skill/SKILL.md", "skill/fixtures/input.txt", "evals/evals.json",
		"previous/SKILL.md", "previous/notes.txt",
	} {
		if _, ok := entries[location]; !ok {
			t.Fatalf("missing namespaced entry %q", location)
		}
	}
	if !entries["evals/evals.json"].ReferencedByEvals || !entries["previous/SKILL.md"].ReferencedBySkill {
		t.Fatalf("namespaced structural roles = %#v", entries)
	}
}

func TestAdmitStructureRefusesLinksInvalidReferencesAndBounds(t *testing.T) {
	requireStructuralAdmissionSupported(t)
	t.Run("root symlink", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		link := filepath.Join(t.TempDir(), "skill-link")
		if err := os.Symlink(root, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		result, err := AdmitStructure(admissionRequest(link))
		requireStructuralFinding(t, result, err, FindingRootSymlink, FindingPolicyRefusal, "skill")
	})

	t.Run("entry symlink", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		if err := os.Symlink("SKILL.md", filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingEntrySymlink, FindingPolicyRefusal, "skill/linked")
	})

	t.Run("hard link", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		if err := os.Link(filepath.Join(root, "fixtures", "input.txt"), filepath.Join(root, "fixtures", "linked.txt")); err != nil {
			t.Skipf("hard link unavailable: %v", err)
		}
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingDuplicateFileIdentity,
			FindingPolicyRefusal, "skill/fixtures/linked.txt")
	})

	t.Run("invalid eval location", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		writeFile(t, filepath.Join(root, "evals", "evals.json"), strings.Replace(
			admissionEvalsDocument(), `"fixtures/input.txt"`, `"../escape"`, 1))
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingEvalManifestInvalid,
			FindingPolicyRefusal, "skill/evals/evals.json")
	})

	t.Run("missing eval reference", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		writeFile(t, filepath.Join(root, "evals", "evals.json"), strings.Replace(
			admissionEvalsDocument(), `"fixtures/input.txt"`, `"fixtures/missing.txt"`, 1))
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingEvalReferenceMissing,
			FindingPolicyRefusal, "skill/fixtures/missing.txt")
	})

	t.Run("depth", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		parts := make([]string, MaxTreeDepth+1)
		for index := range parts {
			parts[index] = "d"
		}
		if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0o700); err != nil {
			t.Fatalf("MkdirAll(): %v", err)
		}
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingEntryDepthLimit, FindingPolicyRefusal,
			"skill/"+strings.Join(parts, "/"))
	})

	t.Run("path bytes", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		component := strings.Repeat("p", 180)
		if err := os.MkdirAll(filepath.Join(root, component, component, component), 0o700); err != nil {
			t.Skipf("long path unavailable: %v", err)
		}
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingPathBytesLimit, FindingPolicyRefusal, "skill")
	})

	t.Run("file bytes", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		name := filepath.Join(root, "oversized.bin")
		file, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(MaxFileBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		result, err := AdmitStructure(admissionRequest(root))
		requireStructuralFinding(t, result, err, FindingFileBytesLimit,
			FindingPolicyRefusal, "skill/oversized.bin")
	})
}

func TestAdmitStructureDistinguishesAggregateLimitsAndInstability(t *testing.T) {
	requireStructuralAdmissionSupported(t)

	t.Run("entry count", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		limits := testStructuralLimits()
		limits.MaxEntries = 2
		result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
			limits: &limits,
		})
		requireStructuralFinding(t, result, err, FindingEntryCountLimit, FindingPolicyRefusal,
			"skill")
	})

	t.Run("tree bytes", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		limits := testStructuralLimits()
		limits.MaxTreeBytes = 32
		result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
			limits: &limits,
		})
		requireStructuralFinding(t, result, err, FindingTreeBytesLimit, FindingPolicyRefusal,
			"skill/SKILL.md")
	})

	t.Run("aggregate tree bytes", func(t *testing.T) {
		skill := t.TempDir()
		skillDocument := admissionSkillDocument()
		inputDocument := "synthetic\n"
		writeFile(t, filepath.Join(skill, "SKILL.md"), skillDocument)
		writeFile(t, filepath.Join(skill, "fixtures", "input.txt"), inputDocument)
		evals := t.TempDir()
		evalDocument := admissionEvalsDocument()
		writeFile(t, filepath.Join(evals, "evals.json"), evalDocument)
		limits := testStructuralLimits()
		limits.MaxTreeBytes = uint64(len(skillDocument) + len(inputDocument) + len(evalDocument) - 1)
		reads := 0
		result, err := admitStructureWithHooks(ImportRequest{
			SkillRoot: skill, EvalRoot: evals,
			Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill,
		}, structuralAdmissionHooks{
			limits: &limits,
			evals:  structuralCaptureHooks{beforeRead: func(_ int, _ string) { reads++ }},
		})
		requireStructuralFinding(t, result, err, FindingTreeBytesLimit,
			FindingPolicyRefusal, "evals/evals.json")
		if reads != 0 {
			t.Fatalf("oversized aggregate source was read %d times", reads)
		}
	})

	t.Run("aggregate entry count", func(t *testing.T) {
		skill := t.TempDir()
		writeFile(t, filepath.Join(skill, "SKILL.md"), admissionSkillDocument())
		writeFile(t, filepath.Join(skill, "fixtures", "input.txt"), "synthetic\n")
		evals := t.TempDir()
		writeFile(t, filepath.Join(evals, "evals.json"), admissionEvalsDocument())
		limits := testStructuralLimits()
		limits.MaxEntries = 3
		opens := 0
		result, err := admitStructureWithHooks(ImportRequest{
			SkillRoot: skill, EvalRoot: evals,
			Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill,
		}, structuralAdmissionHooks{
			limits: &limits,
			evals:  structuralCaptureHooks{beforeOpen: func(_ int, _ string) { opens++ }},
		})
		requireStructuralFinding(t, result, err, FindingEntryCountLimit,
			FindingPolicyRefusal, "evals")
		if opens != 0 {
			t.Fatalf("over-entry aggregate source opened %d entries", opens)
		}
	})

	t.Run("duplicate identity across roots", func(t *testing.T) {
		skill := writeAdmissionFixture(t)
		previous := t.TempDir()
		if err := os.Link(filepath.Join(skill, "SKILL.md"), filepath.Join(previous, "SKILL.md")); err != nil {
			t.Skipf("hard link unavailable: %v", err)
		}
		result, err := AdmitStructure(ImportRequest{
			SkillRoot: skill, PreviousSkillRoot: previous,
			Format: FormatAgentSkillsGuideV1, Baseline: BaselinePreviousSkill,
		})
		requireStructuralFinding(t, result, err, FindingDuplicateFileIdentity,
			FindingPolicyRefusal, "previous/SKILL.md")
	})

	t.Run("tree changed", func(t *testing.T) {
		root := writeAdmissionFixture(t)
		name := filepath.Join(root, "fixtures", "input.txt")
		initial, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		var hookErr error
		result, err := admitStructureWithHooks(admissionRequest(root), structuralAdmissionHooks{
			skill: structuralCaptureHooks{afterFirstInventory: func() {
				hookErr = os.WriteFile(name, []byte("replacement"), 0o600)
				if hookErr == nil {
					hookErr = os.Chtimes(name, initial.ModTime(), initial.ModTime())
				}
			}},
		})
		if hookErr != nil {
			t.Fatalf("mutation hook: %v", hookErr)
		}
		requireStructuralFinding(t, result, err, FindingTreeChanged, FindingSourceInstability, "skill")
	})
}

func TestAgentSkillsProductionHasNoExecutionNetworkOrCredentialDiscovery(t *testing.T) {
	forbiddenImports := map[string]struct{}{
		"net": {}, "net/http": {}, "os/exec": {},
	}
	forbiddenSelectors := map[string]map[string]struct{}{
		"os": {
			"Environ": {}, "Executable": {}, "FindProcess": {}, "Getenv": {},
			"LookupEnv": {}, "StartProcess": {}, "UserHomeDir": {},
		},
		"syscall": {
			"Exec": {}, "ForkExec": {}, "RawSyscall": {}, "RawSyscall6": {},
			"StartProcess": {}, "Syscall": {}, "Syscall6": {},
		},
		"unix": {
			"Accept": {}, "Accept4": {}, "Bind": {}, "Clone": {}, "Connect": {},
			"Exec": {}, "ForkExec": {}, "Listen": {}, "RawSyscall": {}, "RawSyscall6": {},
			"Recvfrom": {}, "Sendto": {}, "Socket": {}, "Socketpair": {},
			"StartProcess": {}, "Syscall": {}, "Syscall6": {},
		},
	}
	directory, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range directory {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(files, entry.Name(), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse production file %s: %v", entry.Name(), parseErr)
		}
		effectAliases := make(map[string]string)
		for _, imported := range parsed.Imports {
			path, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("unquote import in %s: %v", entry.Name(), unquoteErr)
			}
			if _, forbidden := forbiddenImports[path]; forbidden || strings.HasPrefix(path, "net/") {
				t.Fatalf("production package imports forbidden effect package %q in %s", path, entry.Name())
			}
			owner := filepath.Base(path)
			if imported.Name != nil {
				owner = imported.Name.Name
			}
			if owner == "." && (path == "os" || path == "syscall" || path == "golang.org/x/sys/unix") {
				t.Fatalf("production package dot-imports effect package %q in %s", path, entry.Name())
			}
			switch path {
			case "os", "syscall":
				effectAliases[owner] = path
			case "golang.org/x/sys/unix":
				effectAliases[owner] = "unix"
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			owner, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if names := forbiddenSelectors[effectAliases[owner.Name]]; names != nil {
				if _, forbidden := names[selector.Sel.Name]; forbidden {
					t.Errorf("production package uses forbidden effect %s.%s in %s",
						owner.Name, selector.Sel.Name, entry.Name())
				}
			}
			return true
		})
	}
}

func writeAdmissionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), admissionSkillDocument())
	writeFile(t, filepath.Join(root, "evals", "evals.json"), admissionEvalsDocument())
	writeFile(t, filepath.Join(root, "fixtures", "input.txt"), "synthetic\n")
	writeFile(t, filepath.Join(root, "scripts", "unlisted.sh"), "do-not-expose-source-text\ntouch executed\n")
	return root
}

func admissionSkillDocument() string {
	return "---\nname: structural-fixture\ndescription: Synthetic structural admission fixture.\n" +
		"allowed-tools: shell\ncompatibility: synthetic\n---\n" +
		"A prose-only mention of scripts/unlisted.sh grants no authority.\n"
}

func admissionEvalsDocument() string {
	return `{
  "skill_name": "structural-fixture",
  "evals": [
    {
      "id": 1,
      "prompt": "Use the synthetic fixture.",
      "expected_output": "A synthetic result.",
      "files": ["fixtures/input.txt"],
      "assertions": ["The result is synthetic."]
    },
    {
      "id": 2,
      "prompt": "Reuse the synthetic fixture.",
      "expected_output": "Another synthetic result.",
      "files": ["fixtures/input.txt"]
    }
  ]
}`
}

func admissionRequest(root string) ImportRequest {
	return ImportRequest{
		SkillRoot: root, Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill,
	}
}

func structuralEntriesByLocation(t *testing.T, entries []StructuralEntry) map[string]StructuralEntry {
	t.Helper()
	result := make(map[string]StructuralEntry, len(entries))
	previous := ""
	for index, entry := range entries {
		if index > 0 && entry.Location <= previous {
			t.Fatalf("entries are not canonical: %q after %q", entry.Location, previous)
		}
		if _, duplicate := result[entry.Location]; duplicate {
			t.Fatalf("entry %q classified more than once", entry.Location)
		}
		result[entry.Location] = entry
		previous = entry.Location
	}
	return result
}

func requireStructuralFinding(t *testing.T, result StructuralAdmission, err error,
	wantCode StructuralFindingCode, wantClass StructuralFindingClass, wantLocation string,
) {
	t.Helper()
	if err != nil {
		t.Fatalf("AdmitStructure() error = %v", err)
	}
	if result.Admitted || !result.BlocksExecution() || result.RuntimeSafetyProven ||
		result.TreeSHA256 != "" || len(result.Entries) != 0 || len(result.Findings) != 1 {
		t.Fatalf("refusal exposed an invalid partial result: %#v", result)
	}
	finding := result.Findings[0]
	if finding.Code != wantCode || finding.Class != wantClass || finding.Location != wantLocation {
		t.Fatalf("finding = %#v, want %q/%q/%q", finding, wantCode, wantClass, wantLocation)
	}
	if !validDigest(result.PolicySHA256) || strings.Contains(finding.Location, string(filepath.Separator)+"tmp") {
		t.Fatalf("finding identity/location = %#v", result)
	}
}

func testStructuralLimits() StructuralLimits {
	return StructuralLimits{
		MaxEntries: MaxTreeEntries, MaxDepth: MaxTreeDepth, MaxPathBytes: MaxPathBytes,
		MaxFileBytes: MaxFileBytes, MaxTreeBytes: MaxTreeBytes,
	}
}

func requireStructuralAdmissionSupported(t *testing.T) {
	t.Helper()
	if !structuralAdmissionSupported {
		t.Skip("secure structural admission is not supported on this platform")
	}
}
