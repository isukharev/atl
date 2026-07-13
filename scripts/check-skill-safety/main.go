// Command check-skill-safety validates explicitly designated read-only shell
// examples in skills-src without executing them.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const readOnlyShellMarker = "<!-- atl:read-only-shell -->"

var requiredReadOnlySkillBlocks = map[string]int{
	"skills-src/status-report/SKILL.md":    4,
	"skills-src/search-knowledge/SKILL.md": 5,
}

type skillSafetyReport struct {
	Files  int
	Blocks int
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	report, err := validateReadOnlySkillBlocks(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Skill safety: %d read-only shell blocks in %d files\n", report.Blocks, report.Files)
}

func validateReadOnlySkillBlocks(root string) (skillSafetyReport, error) {
	counts := map[string]int{}
	var problems []string
	skillsRoot := filepath.Join(root, "skills-src")
	skillFiles, err := os.OpenRoot(skillsRoot)
	if err != nil {
		return skillSafetyReport{}, err
	}
	defer func() { _ = skillFiles.Close() }()
	err = fs.WalkDir(skillFiles.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		data, err := skillFiles.ReadFile(path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(filepath.Join("skills-src", path))
		count, fileProblems := validateReadOnlyShellFile(rel, string(data))
		counts[rel] = count
		problems = append(problems, fileProblems...)
		return nil
	})
	if err != nil {
		return skillSafetyReport{}, err
	}
	for path, minimum := range requiredReadOnlySkillBlocks {
		if counts[path] < minimum {
			problems = append(problems, fmt.Sprintf("%s has %d designated read-only shell blocks; require at least %d", path, counts[path], minimum))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return skillSafetyReport{}, fmt.Errorf("read-only skill contract failed:\n- %s", strings.Join(problems, "\n- "))
	}
	report := skillSafetyReport{}
	for _, count := range counts {
		if count > 0 {
			report.Files++
			report.Blocks += count
		}
	}
	return report, nil
}

func validateReadOnlyShellFile(path, content string) (int, []string) {
	lines := strings.Split(content, "\n")
	count := 0
	var problems []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != readOnlyShellMarker {
			continue
		}
		count++
		markerLine := i + 1
		fence := i + 1
		for fence < len(lines) && strings.TrimSpace(lines[fence]) == "" {
			fence++
		}
		if fence >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[fence]), "```") {
			problems = append(problems, fmt.Sprintf("%s:%d marker must be followed by a shell fence", path, markerLine))
			continue
		}
		language := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[fence]), "```"))
		if language != "sh" && language != "bash" && language != "shell" {
			problems = append(problems, fmt.Sprintf("%s:%d marker fence language %q is not sh/bash/shell", path, markerLine, language))
			continue
		}
		end := fence + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
			end++
		}
		if end >= len(lines) {
			problems = append(problems, fmt.Sprintf("%s:%d read-only shell fence is not closed", path, markerLine))
			continue
		}
		first := ""
		for _, line := range lines[fence+1 : end] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			first = trimmed
			break
		}
		if first != "export ATL_READ_ONLY=1" {
			problems = append(problems, fmt.Sprintf("%s:%d first executable statement is %q; require export ATL_READ_ONLY=1", path, markerLine, first))
		}
		i = end
	}
	return count, problems
}
