// Package version carries the build version, set via -ldflags at build time:
//
//	go build -ldflags "-X github.com/isukharev/atl/internal/version.Version=1.2.3"
package version

// Version is the CLI version. "dev" for local/unstamped builds.
var Version = "dev"

// DefaultUpdateURL is the GitHub Releases download base the CLI self-updates
// from. "latest/download" always resolves to the newest release's assets
// (manifest.json, manifest.json.sig, atl-<os>-<arch>). Overridable via config
// or the ATL_UPDATE_URL env var; auto-update additionally requires a compiled-in
// signing key (see internal/selfupdate/pubkey.go) and a non-dev Version.
var DefaultUpdateURL = "https://github.com/isukharev/atl/releases/latest/download"
