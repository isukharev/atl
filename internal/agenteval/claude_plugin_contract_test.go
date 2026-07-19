package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

type repositoryClaudePluginManifest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"author"`
	Homepage   string   `json:"homepage"`
	Repository string   `json:"repository"`
	License    string   `json:"license"`
	Keywords   []string `json:"keywords"`
	MCPServers string   `json:"mcpServers"`
}

type repositoryClaudeMarketplace struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Description string `json:"description"`
	Plugins     []struct {
		Name        string `json:"name"`
		Source      string `json:"source"`
		Description string `json:"description"`
	} `json:"plugins"`
}

func TestRepositoryClaudePluginContract(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	pluginDirectory := filepath.Join(repositoryRoot, ".claude-plugin")
	requireRepositoryDirectory(t, pluginDirectory)

	var manifest repositoryClaudePluginManifest
	decodeRepositoryJSON(t, filepath.Join(pluginDirectory, "plugin.json"), &manifest)
	var marketplace repositoryClaudeMarketplace
	decodeRepositoryJSON(t, filepath.Join(pluginDirectory, "marketplace.json"), &marketplace)

	if manifest.Name != "atl" {
		t.Fatalf("Claude plugin name = %q, want atl", manifest.Name)
	}
	if marketplace.Name != manifest.Name {
		t.Fatalf("Claude marketplace name = %q, want plugin name %q", marketplace.Name, manifest.Name)
	}
	if len(marketplace.Plugins) != 1 {
		t.Fatalf("Claude marketplace plugin count = %d, want 1", len(marketplace.Plugins))
	}
	entry := marketplace.Plugins[0]
	if entry.Name != manifest.Name || entry.Source != "./" {
		t.Fatalf("Claude marketplace entry = {name:%q source:%q}, want {name:%q source:%q}", entry.Name, entry.Source, manifest.Name, "./")
	}

	wantSkills := []string{
		"atl",
		"confluence",
		"jira",
		"meeting-tasks",
		"onboarding",
		"search-knowledge",
		"setup",
		"spec-to-backlog",
		"sprint-dashboard",
		"status-report",
		"triage-issue",
	}
	generatedSkills := repositorySkillInventory(t, filepath.Join(repositoryRoot, "skills"))
	sourceSkills := repositorySkillInventory(t, filepath.Join(repositoryRoot, "skills-src"))
	if !slices.Equal(generatedSkills, wantSkills) {
		t.Fatalf("shipped Claude skill inventory = %v, want %v", generatedSkills, wantSkills)
	}
	if !slices.Equal(sourceSkills, wantSkills) {
		t.Fatalf("source skill inventory = %v, want %v", sourceSkills, wantSkills)
	}
}

func decodeRepositoryJSON(t *testing.T, path string, target any) {
	t.Helper()
	requireRepositoryRegularFile(t, path)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("decode %s: trailing JSON: %v", path, err)
	}
}

func repositorySkillInventory(t *testing.T, root string) []string {
	t.Helper()
	requireRepositoryDirectory(t, root)
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository skill path %s must not be a symlink", path)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("repository skill path %s must be a regular file, mode=%s", path, info.Mode())
			}
		}
		if entry.Name() != "SKILL.md" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.Dir(relative)
		if name == "." || filepath.Dir(name) != "." {
			return fmt.Errorf("repository skill manifest %s is not directly below the skill root", path)
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func requireRepositoryDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("repository path %s must be a real directory, mode=%s", path, info.Mode())
	}
}

func requireRepositoryRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("repository path %s must be a regular file, mode=%s", path, info.Mode())
	}
}
