package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPremergeContractRejectsProtectionGaps(t *testing.T) {
	tests := []struct {
		name, path, old, replacement string
	}{
		{"missing manual head", "ci.yml", "      head_sha:\n", "      other_sha:\n"},
		{"automatic trigger reintroduced", "ci.yml", "  workflow_dispatch:\n", "  pull_request:\n    branches: [main]\n  workflow_dispatch:\n"},
		{"wrong binding checkout", "ci.yml", bindingJobContract, strings.Replace(bindingJobContract, "ref: ${{ github.sha }}", "ref: main", 1)},
		{"conditional binding", "ci.yml", bindingJobContract, strings.Replace(bindingJobContract, "    runs-on:", "    continue-on-error: true\n    runs-on:", 1)},
		{"missing aggregate dependency", "ci.yml", "lint, govulncheck, codeql]", "lint, govulncheck]"},
		{"aggregate skipped after failure", "ci.yml", "if: always() && ", "if: success() && "},
		{"aggregate allowed failure", "ci.yml", readyJobContract, strings.Replace(readyJobContract, "    runs-on:", "    continue-on-error: true\n    runs-on:", 1)},
		{"aggregate bypasses validation", "ci.yml", "go run ./scripts/check-premerge ready", "true"},
		{"aggregate no final API permission", "ci.yml", readyJobContract, strings.Replace(readyJobContract, "      pull-requests: read\n", "", 1)},
		{"aggregate wrong results", "ci.yml", "ATL_PREMERGE_NEEDS: ${{ toJSON(needs) }}", "ATL_PREMERGE_NEEDS: '{}'"},
		{"codeql dependency replaced", "ci.yml", "uses: ./.github/workflows/codeql.yml", "uses: ./.github/workflows/other.yml"},
		{"manual docs head absent", "ci.yml", "ATL_DOCS_HEAD: ${{ inputs.head_sha }}", "ATL_DOCS_HEAD: ${{ github.event.pull_request.head.sha }}"},
		{"automatic codeql reintroduced", "codeql.yml", "  workflow_dispatch:\n", "  pull_request:\n    branches: [main]\n  workflow_dispatch:\n"},
		{"weekly codeql removed", "codeql.yml", "  schedule:\n    - cron: '27 3 * * 1'\n", ""},
		{"reusable codeql removed", "codeql.yml", "  workflow_call:", "  other_event:"},
		{"codeql upload bypassed", "codeql.yml", "github/codeql-action/analyze@", "other/action@"},
		{"codeql scan identity collision", "codeql.yml", "category: ${{ inputs.analysis_category }}", "category: legacy"},
		{"codeql wrong ref", "codeql.yml", "ref: ${{ github.sha }}", "ref: main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeFixture(t)
			path := filepath.Join(root, ".github", "workflows", tt.path)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tt.old) {
				t.Fatal("mutation target missing")
			}
			body = []byte(strings.Replace(string(body), tt.old, tt.replacement, 1))
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := validateRepository(root, "go"+fixtureGoVersion); err == nil {
				t.Fatal("accepted weakened premerge contract")
			}
		})
	}
}
