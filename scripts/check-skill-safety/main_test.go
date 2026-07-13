package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryReadOnlySkillBlocks(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	report, err := validateReadOnlySkillBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Files < 2 || report.Blocks < 9 {
		t.Fatalf("unexpectedly sparse safety coverage: %+v", report)
	}
}

func TestReadOnlyShellMarkerRequiresInheritedGuardFirst(t *testing.T) {
	valid := readOnlyShellMarker + "\n```bash\n# comment\nexport ATL_READ_ONLY=1\natl jira issue get PROJ-1\n```\n"
	if count, problems := validateReadOnlyShellFile("skill.md", valid); count != 1 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v", count, problems)
	}
	for name, content := range map[string]string{
		"missing export": readOnlyShellMarker + "\n```sh\natl jira issue get PROJ-1\n```\n",
		"prefix only":    readOnlyShellMarker + "\n```sh\nATL_READ_ONLY=1 atl jira issue get PROJ-1\n```\n",
		"wrong language": readOnlyShellMarker + "\n```text\nexport ATL_READ_ONLY=1\n```\n",
		"missing fence":  readOnlyShellMarker + "\nprose\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, problems := validateReadOnlyShellFile("skill.md", content); len(problems) == 0 {
				t.Fatal("invalid block passed")
			}
		})
	}
}

func TestRepositoryCheckRejectsRemovedRequiredMarkers(t *testing.T) {
	root := t.TempDir()
	for path, minimum := range requiredReadOnlySkillBlocks {
		content := strings.Repeat(readOnlyShellMarker+"\n```sh\nexport ATL_READ_ONLY=1\natl version\n```\n", minimum)
		if strings.Contains(path, "status-report") {
			content = strings.Replace(content, readOnlyShellMarker, "<!-- removed -->", 1)
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := validateReadOnlySkillBlocks(root); err == nil || !strings.Contains(err.Error(), "require at least") {
		t.Fatalf("error = %v", err)
	}
}
