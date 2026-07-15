package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectPredecessorUsesMaximumSemverAcrossPages(t *testing.T) {
	input := `[[{"tag_name":"v1.9.0","draft":false,"prerelease":false}],` +
		`[{"tag_name":"v2.0.0","draft":false,"prerelease":false},` +
		`{"tag_name":"v9.0.0-rc.1","draft":false,"prerelease":true}]]`
	got, err := selectPredecessor(strings.NewReader(input), "v2.1.0", "")
	if err != nil || got != "v2.0.0" {
		t.Fatalf("predecessor=%q err=%v", got, err)
	}
}

func TestReleaseWorkflowPinsFailClosedChainControls(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)
	for _, required := range []string{
		"gh api --paginate --slurp", "./scripts/select-release-predecessor",
		"--bootstrap-tag \"$TRUST_BOOTSTRAP_TAG\"", "--current-source internal/selfupdate/pubkey.go",
		"ATL_RELEASE_TRUST_RESET_TAG", "--allow-trust-reset", "remove a stale reset before releasing",
		"test -s dist/manifest.json.sig", "dist/manifest.json dist/manifest.json.sig",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	for _, unsafe := range []string{"gh release list", "if [ -f dist/manifest.json.sig ]"} {
		if strings.Contains(content, unsafe) {
			t.Errorf("release workflow retains fail-open fragment %q", unsafe)
		}
	}
}

func TestSelectPredecessorRequiresTagBoundBootstrapForEmptyListing(t *testing.T) {
	for name, bootstrap := range map[string]string{"missing": "", "wrong": "v0.1.1"} {
		t.Run(name, func(t *testing.T) {
			if _, err := selectPredecessor(strings.NewReader(`[]`), "v0.1.0", bootstrap); err == nil {
				t.Fatal("expected bootstrap refusal")
			}
		})
	}
	got, err := selectPredecessor(strings.NewReader(`[]`), "v0.1.0", "v0.1.0")
	if err != nil || got != "v0.1.0" {
		t.Fatalf("bootstrap predecessor=%q err=%v", got, err)
	}
}

func TestSelectPredecessorRejectsBootstrapAfterChainStarts(t *testing.T) {
	input := `[{"tag_name":"v1.0.0","draft":false,"prerelease":false}]`
	if _, err := selectPredecessor(strings.NewReader(input), "v1.1.0", "v1.1.0"); err == nil {
		t.Fatal("expected stale bootstrap refusal")
	}
}

func TestSelectPredecessorRequiresMonotonicStableVersion(t *testing.T) {
	input := `[{"tag_name":"v2.0.0","draft":false,"prerelease":false}]`
	for _, current := range []string{"v2.0.0", "v1.99.0", "v2.0.0-rc.1", "2.1.0"} {
		t.Run(current, func(t *testing.T) {
			if _, err := selectPredecessor(strings.NewReader(input), current, ""); err == nil {
				t.Fatal("expected version refusal")
			}
		})
	}
}

func TestSelectPredecessorRejectsAmbiguousListing(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":  `[{"tag_name":"v1.0.0"},{"tag_name":"v1.0.0"}]`,
		"non-semver": `[{"tag_name":"latest"}]`,
		"bad-entry":  `[42]`,
		"malformed":  `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := selectPredecessor(strings.NewReader(input), "v2.0.0", ""); err == nil {
				t.Fatal("expected listing refusal")
			}
		})
	}
}
