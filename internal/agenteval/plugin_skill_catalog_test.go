package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyCodexSkillPackageReconcilesExactTree(t *testing.T) {
	packageRoot := writeCodexSkillFixture(t)
	catalog, err := VerifyCodexSkillPackage(packageRoot)
	if err != nil {
		t.Fatalf("VerifyCodexSkillPackage() error = %v", err)
	}
	if catalog.SchemaVersion != 1 || len(catalog.Skills) != 11 || len(catalog.Files) != 11 {
		t.Fatalf("VerifyCodexSkillPackage() = %#v", catalog)
	}
	if catalog.Skills[0].Name != "atl" || !catalog.Skills[0].AllowImplicitInvocation {
		t.Fatalf("first skill = %#v", catalog.Skills[0])
	}
	if catalog.Skills[6].Name != "setup" || catalog.Skills[6].AllowImplicitInvocation {
		t.Fatalf("setup skill = %#v", catalog.Skills[6])
	}
}

func TestRepositoryCodexSkillPackageMatchesReleasedSemantics(t *testing.T) {
	catalog, err := VerifyCodexSkillPackage(filepath.Join("..", "..", "plugins", "atl"))
	if err != nil {
		t.Fatalf("VerifyCodexSkillPackage(repository package) error = %v", err)
	}
	if err := VerifyReleasedCodexSkillSemantics(catalog); err != nil {
		t.Fatalf("VerifyReleasedCodexSkillSemantics(repository package) error = %v", err)
	}
}

func TestVerifyReleasedCodexSkillSemanticsRejectsPolicyDrift(t *testing.T) {
	root := writeCodexSkillFixture(t)
	catalog, err := VerifyCodexSkillPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleasedCodexSkillSemantics(catalog); err != nil {
		t.Fatalf("VerifyReleasedCodexSkillSemantics() error = %v", err)
	}
	catalog.Skills[0].AllowImplicitInvocation = !catalog.Skills[0].AllowImplicitInvocation
	if err := VerifyReleasedCodexSkillSemantics(catalog); err == nil {
		t.Fatal("VerifyReleasedCodexSkillSemantics(drift) error = nil")
	}
}

func TestDecodeCodexSkillCatalogRejectsMalformedContracts(t *testing.T) {
	fixture := readCodexSkillCatalogFixture(t)
	tests := map[string]func([]byte) []byte{
		"unknown top-level member": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"schema_version": 1,`, `"schema_version": 1, "extra": true,`, 1))
		},
		"missing top-level member": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"schema_version": 1,`, "", 1))
		},
		"duplicate member": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"schema_version": 1,`, `"schema_version": 1, "schema_version": 1,`, 1))
		},
		"missing nested member": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), ",\n      \"allow_implicit_invocation\": true", "", 1))
		},
		"unknown nested member": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"allow_implicit_invocation": true`, `"allow_implicit_invocation": true, "extra": false`, 1))
		},
		"unsupported schema": func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 2`, 1))
		},
		"unsorted skills": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Skills[0], catalog.Skills[1] = catalog.Skills[1], catalog.Skills[0]
			return mustMarshalJSON(t, catalog)
		},
		"duplicate skills": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Skills[1].Name = catalog.Skills[0].Name
			return mustMarshalJSON(t, catalog)
		},
		"invalid skill name": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Skills[0].Name = "../atl"
			return mustMarshalJSON(t, catalog)
		},
		"unsorted files": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Files[0], catalog.Files[1] = catalog.Files[1], catalog.Files[0]
			return mustMarshalJSON(t, catalog)
		},
		"duplicate files": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Files[1].Path = catalog.Files[0].Path
			return mustMarshalJSON(t, catalog)
		},
		"uppercase digest": func(data []byte) []byte {
			catalog := mustDecodeCodexSkillCatalog(t, data)
			catalog.Files[0].SHA256 = strings.ToUpper(catalog.Files[0].SHA256)
			return mustMarshalJSON(t, catalog)
		},
		"trailing data": func(data []byte) []byte { return append(data, []byte(` true`)...) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCodexSkillCatalog(mutate(fixture)); err == nil {
				t.Fatal("decodeCodexSkillCatalog() error = nil")
			}
		})
	}
}

func TestDecodeCodexSkillCatalogRejectsUnsafePaths(t *testing.T) {
	for _, value := range []string{"../SKILL.md", "/tmp/SKILL.md", `atl\\SKILL.md`, "atl/../SKILL.md", "./atl/SKILL.md", "atl//SKILL.md", "atl/\x00SKILL.md"} {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			catalog := mustDecodeCodexSkillCatalog(t, readCodexSkillCatalogFixture(t))
			catalog.Files[0].Path = value
			if _, err := decodeCodexSkillCatalog(mustMarshalJSON(t, catalog)); err == nil {
				t.Fatalf("decodeCodexSkillCatalog(path %q) error = nil", value)
			}
		})
	}
}

func TestDecodeCodexSkillCatalogEnforcesBounds(t *testing.T) {
	if _, err := decodeCodexSkillCatalog(make([]byte, maxCodexSkillCatalogBytes+1)); err == nil {
		t.Fatal("decodeCodexSkillCatalog(oversize) error = nil")
	}
	catalog := mustDecodeCodexSkillCatalog(t, readCodexSkillCatalogFixture(t))
	catalog.Skills = make([]CodexSkillCatalogSkill, maxCodexSkillCatalogSkills+1)
	if err := catalog.validate(); err == nil {
		t.Fatal("CodexSkillCatalog.validate(too many skills) error = nil")
	}
	catalog = mustDecodeCodexSkillCatalog(t, readCodexSkillCatalogFixture(t))
	catalog.Files[0].Path = strings.Repeat("a", maxCodexSkillFilePathBytes+1)
	if err := catalog.validate(); err == nil {
		t.Fatal("CodexSkillCatalog.validate(long path) error = nil")
	}
}

func TestVerifyCodexSkillPackageRejectsTreeDrift(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"missing": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "skills", "setup", "SKILL.md")); err != nil {
				t.Fatal(err)
			}
		},
		"extra": func(t *testing.T, root string) {
			writeCodexCatalogTestFile(t, filepath.Join(root, "skills", "atl", "EXTRA.md"), "extra\n")
		},
		"changed": func(t *testing.T, root string) {
			writeCodexCatalogTestFile(t, filepath.Join(root, "skills", "atl", "SKILL.md"), "changed\n")
		},
		"symlink": func(t *testing.T, root string) {
			target := filepath.Join(root, "skills", "atl", "SKILL.md")
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../setup/SKILL.md", target); err != nil {
				t.Fatal(err)
			}
		},
		"undeclared semantic directory": func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "skills", "rogue"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := writeCodexSkillFixture(t)
			mutate(t, root)
			if _, err := VerifyCodexSkillPackage(root); err == nil {
				t.Fatal("VerifyCodexSkillPackage() error = nil")
			}
		})
	}
}

func TestVerifyCodexSkillPackageRejectsSymlinkedRootsAndCatalog(t *testing.T) {
	t.Run("package root", func(t *testing.T) {
		realRoot := writeCodexSkillFixture(t)
		linkRoot := filepath.Join(t.TempDir(), "plugin")
		if err := os.Symlink(realRoot, linkRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCodexSkillPackage(linkRoot); err == nil {
			t.Fatal("VerifyCodexSkillPackage() error = nil")
		}
	})
	t.Run("skill root", func(t *testing.T) {
		root := writeCodexSkillFixture(t)
		skills := filepath.Join(root, "skills")
		realSkills := filepath.Join(root, "real-skills")
		if err := os.Rename(skills, realSkills); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real-skills", skills); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCodexSkillPackage(root); err == nil {
			t.Fatal("VerifyCodexSkillPackage() error = nil")
		}
	})
	t.Run("catalog", func(t *testing.T) {
		root := writeCodexSkillFixture(t)
		catalog := filepath.Join(root, CodexSkillCatalogFileName)
		realCatalog := filepath.Join(root, "catalog.json")
		if err := os.Rename(catalog, realCatalog); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("catalog.json", catalog); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCodexSkillPackage(root); err == nil {
			t.Fatal("VerifyCodexSkillPackage() error = nil")
		}
	})
}

func TestVerifyCodexSkillPackageRejectsSpecialFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable")
	}
	root := writeCodexSkillFixture(t)
	socketPath := filepath.Join(root, "skills", "atl", "socket")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	defer listener.Close()
	if _, err := VerifyCodexSkillPackage(root); err == nil {
		t.Fatal("VerifyCodexSkillPackage() error = nil")
	}
}

func TestVerifyCodexSkillPackageRejectsFileReplacementBeforeRead(t *testing.T) {
	root := writeCodexSkillFixture(t)
	replaced := false
	_, err := verifyCodexSkillPackage(root, codexSkillPackageHooks{
		beforeSkillFileRead: func(name string) {
			if replaced || name != "atl/SKILL.md" {
				return
			}
			replaced = true
			file := filepath.Join(root, "skills", filepath.FromSlash(name))
			old := file + ".old"
			if renameErr := os.Rename(file, old); renameErr != nil {
				t.Fatal(renameErr)
			}
			writeCodexCatalogTestFile(t, file, codexFixtureSkillContent("atl"))
		},
	})
	if err == nil {
		t.Fatal("verifyCodexSkillPackage(replaced file) error = nil")
	}
}

func TestVerifyCodexSkillPackageRejectsOversizedFileWithoutPathLeak(t *testing.T) {
	root := writeCodexSkillFixture(t)
	writeCodexCatalogTestFileBytes(t, filepath.Join(root, "skills", "atl", "SKILL.md"), make([]byte, maxCodexSkillFileBytes+1))
	_, err := VerifyCodexSkillPackage(root)
	if err == nil {
		t.Fatal("VerifyCodexSkillPackage() error = nil")
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("error leaks package root: %v", err)
	}
}

func writeCodexSkillFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	semantics, err := decodeReleasedCodexSkillSemantics(releasedCodexSkillSemantics)
	if err != nil {
		t.Fatal(err)
	}
	catalog := CodexSkillCatalog{SchemaVersion: semantics.SchemaVersion, Skills: semantics.Skills}
	for _, skill := range semantics.Skills {
		content := []byte(codexFixtureSkillContent(skill.Name))
		digest := sha256.Sum256(content)
		catalog.Files = append(catalog.Files, CodexSkillCatalogFile{
			Path:   skill.Name + "/SKILL.md",
			SHA256: hex.EncodeToString(digest[:]),
		})
		writeCodexCatalogTestFileBytes(t, filepath.Join(root, "skills", skill.Name, "SKILL.md"), content)
	}
	writeCodexCatalogTestFileBytes(t, filepath.Join(root, CodexSkillCatalogFileName), mustMarshalJSON(t, catalog))
	return root
}

func codexFixtureSkillContent(name string) string {
	return "# " + name + "\n"
}

func readCodexSkillCatalogFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "plugin-skill-catalog.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustDecodeCodexSkillCatalog(t *testing.T, data []byte) CodexSkillCatalog {
	t.Helper()
	catalog, err := decodeCodexSkillCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeCodexCatalogTestFile(t *testing.T, name, content string) {
	t.Helper()
	writeCodexCatalogTestFileBytes(t, name, []byte(content))
}

func writeCodexCatalogTestFileBytes(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0o644); err != nil {
		t.Fatal(err)
	}
}
