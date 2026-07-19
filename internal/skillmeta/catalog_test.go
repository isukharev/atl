package skillmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSkillDocument = `---
name: jira
description: Direct Jira operations. USE WHEN the target is one Jira issue. DO NOT USE WHEN a focused reporting workflow applies.
---

# Jira
`

const validOpenAI = `interface:
  display_name: "atl Jira"
  short_description: "Direct Jira issue operations"
  default_prompt: "Use $jira for this Jira operation."
policy:
  allow_implicit_invocation: true
`

func TestParseSkillFrontmatter(t *testing.T) {
	skill, err := parseSkillFrontmatter([]byte(validSkillDocument))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "jira" || skill.DisableModelInvocation || !strings.Contains(skill.Description, "DO NOT USE WHEN") {
		t.Fatalf("skill=%+v", skill)
	}

	explicit := strings.Replace(validSkillDocument, "description:", "disable-model-invocation: true\nallowed-tools: Bash(atl version)\ndescription:", 1)
	skill, err = parseSkillFrontmatter([]byte(explicit))
	if err != nil || !skill.DisableModelInvocation || skill.AllowedTools != "Bash(atl version)" {
		t.Fatalf("explicit skill=%+v err=%v", skill, err)
	}
}

func TestParseSkillFrontmatterRejectsInvalidMetadata(t *testing.T) {
	longDescription := strings.Repeat("x", maxDescriptionBytes+1) + " USE WHEN x. DO NOT USE WHEN y."
	for name, document := range map[string]string{
		"missing opener":        strings.TrimPrefix(validSkillDocument, "---\n"),
		"missing close":         strings.Replace(validSkillDocument, "\n---\n\n# Jira", "\n# Jira", 1),
		"unknown field":         strings.Replace(validSkillDocument, "name: jira", "name: jira\nextra: x", 1),
		"duplicate field":       strings.Replace(validSkillDocument, "name: jira", "name: jira\nname: jira", 1),
		"nested field":          strings.Replace(validSkillDocument, "name: jira", " name: jira", 1),
		"missing name":          strings.Replace(validSkillDocument, "name: jira\n", "", 1),
		"invalid name":          strings.Replace(validSkillDocument, "name: jira", "name: Jira_bad", 1),
		"missing positive":      strings.Replace(validSkillDocument, "USE WHEN", "Use when", 1),
		"missing negative":      strings.Replace(validSkillDocument, "DO NOT USE WHEN", "Do not use when", 1),
		"oversized description": strings.Replace(validSkillDocument, "Direct Jira operations. USE WHEN the target is one Jira issue. DO NOT USE WHEN a focused reporting workflow applies.", longDescription, 1),
		"invalid boolean":       strings.Replace(validSkillDocument, "description:", "disable-model-invocation: TRUE\ndescription:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSkillFrontmatter([]byte(document)); err == nil {
				t.Fatal("invalid SKILL.md metadata passed")
			}
		})
	}
}

func TestParseOpenAI(t *testing.T) {
	metadata, err := parseOpenAI([]byte(validOpenAI), "jira")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.DisplayName != "atl Jira" || metadata.DefaultPrompt != "Use $jira for this Jira operation." || !metadata.AllowImplicitInvocation {
		t.Fatalf("metadata=%+v", metadata)
	}
}

func TestParseOpenAIRejectsInvalidMetadata(t *testing.T) {
	for name, document := range map[string]string{
		"unknown section":            strings.Replace(validOpenAI, "policy:", "other:", 1),
		"duplicate section":          validOpenAI + "policy:\n  allow_implicit_invocation: true\n",
		"unknown field":              strings.Replace(validOpenAI, "  display_name:", "  extra: \"x\"\n  display_name:", 1),
		"duplicate field":            strings.Replace(validOpenAI, "  display_name: \"atl Jira\"", "  display_name: \"atl Jira\"\n  display_name: \"again\"", 1),
		"missing field":              strings.Replace(validOpenAI, "  short_description: \"Direct Jira issue operations\"\n", "", 1),
		"unquoted scalar":            strings.Replace(validOpenAI, `"atl Jira"`, "atl Jira", 1),
		"invalid boolean":            strings.Replace(validOpenAI, "true", "yes", 1),
		"wrong skill":                strings.Replace(validOpenAI, "$jira", "$confluence", 1),
		"qualified skill":            strings.Replace(validOpenAI, "$jira", "$atl:jira", 1),
		"multiple skills":            strings.Replace(validOpenAI, "$jira", "$jira and $confluence", 1),
		"underscore suffix":          strings.Replace(validOpenAI, "$jira", "$jira_suffix", 1),
		"hyphen suffix":              strings.Replace(validOpenAI, "$jira", "$jira-", 1),
		"invalid namespace":          strings.Replace(validOpenAI, "$jira", "$jira:BAD", 1),
		"bad indentation":            strings.Replace(validOpenAI, "  display_name", "   display_name", 1),
		"short description 24 chars": strings.Replace(validOpenAI, "Direct Jira issue operations", strings.Repeat("x", 24), 1),
		"short description 65 chars": strings.Replace(validOpenAI, "Direct Jira issue operations", strings.Repeat("x", 65), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOpenAI([]byte(document), "jira"); err == nil {
				t.Fatal("invalid openai.yaml metadata passed")
			}
		})
	}
}

func TestParseOpenAIShortDescriptionCountsCharacters(t *testing.T) {
	for _, count := range []int{25, 64} {
		document := strings.Replace(validOpenAI, "Direct Jira issue operations", strings.Repeat("界", count), 1)
		if _, err := parseOpenAI([]byte(document), "jira"); err != nil {
			t.Fatalf("%d-character multibyte description rejected: %v", count, err)
		}
	}
}

func TestLoadSourceValidatesCompleteCatalogAndSkipsRoutingMetadata(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "jira", validSkillDocument, validOpenAI)
	confluenceSkill := strings.ReplaceAll(validSkillDocument, "jira", "confluence")
	confluenceSkill = strings.ReplaceAll(confluenceSkill, "Jira", "Confluence")
	confluenceOpenAI := strings.ReplaceAll(validOpenAI, "jira", "confluence")
	confluenceOpenAI = strings.ReplaceAll(confluenceOpenAI, "Jira", "Confluence")
	writeSkillFixture(t, root, "confluence", confluenceSkill, confluenceOpenAI)
	if err := os.WriteFile(filepath.Join(root, RoutingFileName), []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := LoadSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 || catalog.Skills[0].Name != "confluence" || catalog.Skills[1].Name != "jira" {
		t.Fatalf("catalog=%+v", catalog)
	}
}

func TestLoadSourceRejectsCatalogIdentityAndPolicyDrift(t *testing.T) {
	for name, arrange := range map[string]func(*testing.T, string){
		"unexpected root file": func(t *testing.T, root string) {
			writeSkillFixture(t, root, "jira", validSkillDocument, validOpenAI)
			if err := os.WriteFile(filepath.Join(root, "unexpected.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"directory mismatch": func(t *testing.T, root string) {
			writeSkillFixture(t, root, "other", validSkillDocument, validOpenAI)
		},
		"duplicate logical id": func(t *testing.T, root string) {
			writeSkillFixture(t, root, "a", strings.Replace(validSkillDocument, "name: jira", "name: a", 1), strings.Replace(validOpenAI, "$jira", "$a", 1))
			writeSkillFixture(t, root, "z", strings.Replace(validSkillDocument, "name: jira", "name: a", 1), strings.Replace(validOpenAI, "$jira", "$a", 1))
		},
		"missing openai metadata": func(t *testing.T, root string) {
			directory := filepath.Join(root, "jira")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(validSkillDocument), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"implicit policy mismatch": func(t *testing.T, root string) {
			skill := strings.Replace(validSkillDocument, "description:", "disable-model-invocation: true\ndescription:", 1)
			writeSkillFixture(t, root, "jira", skill, validOpenAI)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			arrange(t, root)
			if _, err := LoadSource(root); err == nil {
				t.Fatal("invalid source catalog passed")
			}
		})
	}
}

func TestLoadSourceRejectsSymlinkedMetadataDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkillFixture(t, root, "jira", validSkillDocument, validOpenAI)
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "openai.yaml"), []byte(validOpenAI), 0o600); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "jira", "agents")
	if err := os.RemoveAll(agents); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, agents); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := LoadSource(root); err == nil || !strings.Contains(err.Error(), "plain directory") {
		t.Fatalf("symlinked metadata directory passed: %v", err)
	}
}

func writeSkillFixture(t *testing.T, root, directory, skill, openAI string) {
	t.Helper()
	path := filepath.Join(root, directory)
	if err := os.MkdirAll(filepath.Join(path, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "agents", "openai.yaml"), []byte(openAI), 0o600); err != nil {
		t.Fatal(err)
	}
}
