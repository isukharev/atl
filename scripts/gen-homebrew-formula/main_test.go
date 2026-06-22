package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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

// collectBuilds must read only the atl-<os>-<arch> binaries, skip sidecars and
// non-binaries, and compute each SHA-256 correctly.
func TestCollectBuildsSkipsSidecarsAndHashes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("atl-darwin-arm64", "DARWIN-ARM")
	write("atl-darwin-arm64.sha256", "deadbeef  atl-darwin-arm64") // sidecar: skipped (has a dot)
	write("atl-linux-amd64", "LINUX-AMD")
	write("manifest.json", "{}") // not atl-: skipped
	write("VERSION", "1.0.0")    // not atl-: skipped
	write("atl.rb", "class Atl") // not atl-<...>: skipped

	builds, err := collectBuilds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 2 {
		t.Fatalf("got %d builds, want 2 (sidecars/non-binaries skipped): %+v", len(builds), builds)
	}
	want := map[string]string{
		"darwin/arm64": sha256Hex("DARWIN-ARM"),
		"linux/amd64":  sha256Hex("LINUX-AMD"),
	}
	for _, b := range builds {
		key := b.os + "/" + b.arch
		if want[key] == "" {
			t.Errorf("unexpected build %s", key)
		}
		if b.sha256 != want[key] {
			t.Errorf("%s sha256 = %s, want %s", key, b.sha256, want[key])
		}
	}
}

// A binary whose name does not resolve to a known os/arch must error, not be
// silently dropped (which would emit a formula missing a platform).
func TestCollectBuildsRejectsUnknownArch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "atl-linux-arm64-static"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectBuilds(dir); err == nil {
		t.Error("expected an error for an unrecognized os/arch in the binary name")
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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
