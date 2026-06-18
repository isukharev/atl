package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/selfupdate"
	"github.com/isukharev/atl/internal/version"
)

// runSelfUpdate performs a best-effort, checksum-verified, throttled
// self-replacement before a command runs. It resolves the distribution server
// from config/env, falling back to the build-time default. It never blocks or
// errors a command; on a successful update it re-execs and does not return.
func runSelfUpdate(_ *cobra.Command) {
	base := version.DefaultUpdateURL
	if cfg, err := config.Load(); err == nil && cfg.UpdateBaseURL != "" {
		base = cfg.UpdateBaseURL
	}
	selfupdate.Run(base, version.Version, config.Dir())
}
