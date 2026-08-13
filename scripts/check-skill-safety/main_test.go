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
	if report.Files < 6 || report.Blocks < 16 {
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

func TestATLToJQPipelineRequiresPipefail(t *testing.T) {
	valid := "```bash\nexport ATL_READ_ONLY=1\nset -o pipefail\natl jira issue search \\\n  --jql 'project = PROJ' |\n  jq -c '{page,rows}'\n```\n"
	if count, problems := validateJQPipelines("skill.md", valid); count != 1 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v", count, problems)
	}

	invalid := "```bash\natl config show | jq -c '{jira_url}'\n```\n"
	if count, problems := validateJQPipelines("skill.md", invalid); count != 1 || len(problems) != 1 {
		t.Fatalf("count=%d problems=%v", count, problems)
	}
	after := "```bash\natl config show | jq -c '{jira_url}'\nset -o pipefail\n```\n"
	if count, problems := validateJQPipelines("skill.md", after); count != 1 || len(problems) != 1 {
		t.Fatalf("late pipefail: count=%d problems=%v", count, problems)
	}
	sameStatementAfter := "```bash\natl config show | jq -c '{jira_url}'; set -o pipefail\n```\n"
	if count, problems := validateJQPipelines("skill.md", sameStatementAfter); count != 1 || len(problems) != 1 {
		t.Fatalf("same-statement late pipefail: count=%d problems=%v", count, problems)
	}
	nonBash := "```sh\nset -o pipefail\natl config show | jq -c '{jira_url}'\n```\n"
	if count, problems := validateJQPipelines("skill.md", nonBash); count != 1 || len(problems) != 1 || !strings.Contains(problems[0], "bash fence") {
		t.Fatalf("non-bash pipefail: count=%d problems=%v", count, problems)
	}
}

func TestJQCheckIgnoresNonPipelineAndDownstreamATL(t *testing.T) {
	content := "```sh\natl jira export --out selected.json\njq -c 'map(.key)' selected.json\nprintf '%s' \"$body\" | atl jira issue comment preview PROJ-1 --from-md -\nprintf '%s' atl | jq -R .\n```\n"
	if count, problems := validateJQPipelines("skill.md", content); count != 0 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v", count, problems)
	}
}

func TestJQCheckJoinsPipelineBeforeComment(t *testing.T) {
	content := "```bash\nset -o pipefail\nATL_READ_ONLY=1 atl config show | # keep only the URL\n  jq -c '{jira_url}'\n```\n"
	if count, problems := validateJQPipelines("skill.md", content); count != 1 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v", count, problems)
	}
}

func TestATLToJQPipelineUsesCommandPosition(t *testing.T) {
	for name, statement := range map[string]string{
		"direct":        "atl config show | jq -c '{jira_url}'",
		"assignment":    "ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'",
		"chain":         "printf ignored; atl config show | sed -n 1p | jq -R .",
		"stderr":        "atl config show 2>&1 | jq -R .",
		"prefix stderr": "2>&1 atl config show | jq -R .",
		"prefix file":   "2> /dev/null atl config show | jq -R .",
		"stderr pipe":   "atl config show |& jq -R .",
		"env":           "env ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'",
		"env redirect":  "env 2>/dev/null ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'",
		"env unset":     "/usr/bin/env -u UNUSED atl config show | jq -c '{jira_url}'",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := atlToJQPipelinePosition(statement); !ok {
				t.Fatalf("pipeline not detected: %q", statement)
			}
		})
	}
	for name, statement := range map[string]string{
		"argument":    "printf '%s' atl | jq -R .",
		"quoted pipe": "printf '%s' 'atl | jq'",
		"new chain":   "atl version | sed -n 1p; printf result | jq -R .",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := atlToJQPipelinePosition(statement); ok {
				t.Fatalf("false positive: %q", statement)
			}
		})
	}
}

func TestJQCheckRejectsUnprotectedRedirectionsAndEnvWrapper(t *testing.T) {
	for name, pipeline := range map[string]string{
		"stderr":        "atl config show 2>&1 | jq -R .",
		"prefix stderr": "2>&1 atl config show | jq -R .",
		"prefix file":   "2> /dev/null atl config show | jq -R .",
		"stderr pipe":   "atl config show |& jq -R .",
		"env":           "env ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'",
		"env redirect":  "env 2>/dev/null ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'",
	} {
		t.Run(name, func(t *testing.T) {
			content := "```bash\n" + pipeline + "\n```\n"
			if count, problems := validateJQPipelines("skill.md", content); count != 1 || len(problems) != 1 {
				t.Fatalf("count=%d problems=%v", count, problems)
			}
		})
	}

	valid := "```bash\nset -o pipefail\n2>&1 atl config show | jq -R .\natl config show |& jq -R .\nenv 2>/dev/null ATL_READ_ONLY=1 atl config show | jq -c '{jira_url}'\n```\n"
	if count, problems := validateJQPipelines("skill.md", valid); count != 3 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v", count, problems)
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

func TestRepositoryCheckRejectsUnsafeJQPipeline(t *testing.T) {
	root := t.TempDir()
	for path, minimum := range requiredReadOnlySkillBlocks {
		content := strings.Repeat(readOnlyShellMarker+"\n```sh\nexport ATL_READ_ONLY=1\natl version\n```\n", minimum)
		if strings.Contains(path, "meeting-tasks") {
			content += "```sh\natl config show | jq -c '{jira_url}'\n```\n"
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := validateReadOnlySkillBlocks(root); err == nil || !strings.Contains(err.Error(), "atl-to-jq pipeline") {
		t.Fatalf("error = %v", err)
	}
}
