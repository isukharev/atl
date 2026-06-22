package main

import (
	"strings"
	"testing"
)

func allBuilds() []build {
	return []build{
		{os: "darwin", arch: "arm64", sha256: "aaaa", asset: "atl-darwin-arm64"},
		{os: "darwin", arch: "amd64", sha256: "bbbb", asset: "atl-darwin-amd64"},
		{os: "linux", arch: "arm64", sha256: "cccc", asset: "atl-linux-arm64"},
		{os: "linux", arch: "amd64", sha256: "dddd", asset: "atl-linux-amd64"},
	}
}

func TestRenderFormulaAllPlatforms(t *testing.T) {
	out, err := renderFormula("1.2.3", "isukharev/atl", allBuilds())
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		"class Atl < Formula",
		`version "1.2.3"`,
		`license "Apache-2.0"`,
		`homepage "https://github.com/isukharev/atl"`,
		"on_macos do",
		"on_linux do",
		"on_arm do",
		"on_intel do",
		// URL is pinned to the v-tag and the exact asset name, with its sha256.
		`url "https://github.com/isukharev/atl/releases/download/v1.2.3/atl-darwin-arm64"`,
		`sha256 "aaaa"`,
		`url "https://github.com/isukharev/atl/releases/download/v1.2.3/atl-linux-amd64"`,
		`sha256 "dddd"`,
		`bin.install Dir["atl-*"].first => "atl"`,
		`shell_output("#{bin}/atl version")`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("formula missing %q\n---\n%s", w, out)
		}
	}
	// macOS block must precede the Linux block (deterministic ordering).
	if strings.Index(out, "on_macos") > strings.Index(out, "on_linux") {
		t.Error("on_macos should be emitted before on_linux")
	}
}

// A partial build set (e.g. macOS only) must emit only the present os block and
// not a dangling/empty on_linux.
func TestRenderFormulaPartialPlatforms(t *testing.T) {
	out, err := renderFormula("0.9.0", "acme/atl", []build{
		{os: "darwin", arch: "arm64", sha256: " feed", asset: "atl-darwin-arm64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "on_linux") {
		t.Error("no linux build provided; on_linux must not appear")
	}
	if !strings.Contains(out, "on_macos") || !strings.Contains(out, "on_arm") {
		t.Error("macOS arm64 build should produce on_macos/on_arm blocks")
	}
	if strings.Contains(out, "on_intel") {
		t.Error("no amd64 build provided; on_intel must not appear")
	}
}

func TestRenderFormulaEmpty(t *testing.T) {
	if _, err := renderFormula("1.0.0", "isukharev/atl", nil); err == nil {
		t.Error("expected an error for an empty build set")
	}
}

// A version or repo containing Ruby/shell metacharacters must be rejected, not
// interpolated into the formula — otherwise a tag like `1.0"; system("id") #`
// would break out of the url/string and inject code.
func TestRenderFormulaRejectsInjection(t *testing.T) {
	bad := []struct {
		name, version, repo string
	}{
		{"quote+code in version", `1.0"; system("id") #`, "isukharev/atl"},
		{"interp in version", `1.0#{system('id')}`, "isukharev/atl"},
		{"space in version", "1.0 0", "isukharev/atl"},
		{"quote in repo", "1.0.0", `evil"; system("id") #/atl`},
		{"no slash in repo", "1.0.0", "isukharevatl"},
		{"space in repo", "1.0.0", "isukharev /atl"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderFormula(tc.version, tc.repo, allBuilds()); err == nil {
				t.Errorf("renderFormula accepted hostile input version=%q repo=%q; want error", tc.version, tc.repo)
			}
		})
	}
}
