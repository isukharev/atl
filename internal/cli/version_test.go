package cli

import (
	"encoding/json"
	"testing"

	"github.com/isukharev/atl/internal/version"
)

func TestVersionOutputIncludesBuildProvenance(t *testing.T) {
	oldVersion, oldCommit, oldState := version.Version, version.Commit, version.BuildState
	version.Version = "1.2.3"
	version.Commit = "0123456789abcdef0123456789abcdef01234567"
	version.BuildState = "dirty"
	t.Cleanup(func() {
		version.Version, version.Commit, version.BuildState = oldVersion, oldCommit, oldState
	})

	out, code := runCLI(t, nil, "version")
	if code != exitOK {
		t.Fatalf("version exit = %d, output=%s", code, out)
	}
	var got version.BuildInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode version output: %v\n%s", err, out)
	}
	if got.Version != "1.2.3" || got.Commit != version.Commit || got.BuildState != "dirty" {
		t.Fatalf("version output = %+v", got)
	}

	out, code = runCLI(t, nil, "version", "-o", "text")
	if code != exitOK || out != "1.2.3\n" {
		t.Fatalf("version text = %q, exit=%d", out, code)
	}

	out, code = runCLI(t, nil, "--version")
	if code != exitOK || out != "atl version 1.2.3\n" {
		t.Fatalf("root --version = %q, exit=%d", out, code)
	}
}
