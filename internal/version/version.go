// Package version carries deterministic build identity, set via -ldflags:
//
//	go build -ldflags "-X github.com/isukharev/atl/internal/version.Version=1.2.3 -X github.com/isukharev/atl/internal/version.Commit=<sha> -X github.com/isukharev/atl/internal/version.BuildState=clean"
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the CLI version. "dev" for local/unstamped builds.
var Version = "dev"

// Commit is the source revision stamped by supported build paths. Current also
// consults compiler VCS metadata when this remains unknown.
var Commit = "unknown"

// BuildState is clean, dirty, or unknown. The supported Makefile build defines
// dirty as any tracked or non-ignored untracked workspace change at build time.
var BuildState = "unknown"

// BuildInfo is the stable JSON identity returned by `atl version`.
type BuildInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	BuildState string `json:"build_state"`
}

// Current returns informational provenance only. Self-update and signature
// verification continue to use Version and never trust Commit/BuildState.
func Current() BuildInfo {
	settings := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
	}
	return resolveBuildInfo(Version, Commit, BuildState, settings)
}

func resolveBuildInfo(version, commit, state string, settings map[string]string) BuildInfo {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	commit = normalizedCommit(commit)
	if commit == "unknown" {
		commit = normalizedCommit(settings["vcs.revision"])
	}
	state = normalizedBuildState(state)
	if state == "unknown" {
		switch strings.ToLower(strings.TrimSpace(settings["vcs.modified"])) {
		case "false":
			state = "clean"
		case "true":
			state = "dirty"
		}
	}
	return BuildInfo{Version: version, Commit: commit, BuildState: state}
}

func normalizedCommit(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "unknown") || value == "(devel)" {
		return "unknown"
	}
	return value
}

func normalizedBuildState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "clean":
		return "clean"
	case "dirty":
		return "dirty"
	default:
		return "unknown"
	}
}

// DefaultUpdateURL is the GitHub Releases download base the CLI self-updates
// from. "latest/download" always resolves to the newest release's assets
// (manifest.json, manifest.json.sig, atl-<os>-<arch>). Overridable via config
// or the ATL_UPDATE_URL env var; auto-update additionally requires a compiled-in
// signing key (see internal/selfupdate/pubkey.go) and a non-dev Version.
var DefaultUpdateURL = "https://github.com/isukharev/atl/releases/latest/download"
