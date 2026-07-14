package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/selfupdate"
	"github.com/isukharev/atl/internal/version"
)

// runSelfUpdate performs a best-effort, signature-verified, throttled
// self-replacement before a command runs. It resolves the distribution server
// from config/env, falling back to the build-time default. It never blocks or
// errors a command, honors the command's (signal-aware) context so Ctrl-C can
// cancel an in-flight download, and applies any update for the NEXT invocation
// rather than re-execing the current one. Offline/trivial commands skip it.
func runSelfUpdate(cmd *cobra.Command) {
	if skipSelfUpdate(cmd) {
		return
	}
	base := version.DefaultUpdateURL
	if cfg, err := config.Load(); err == nil && cfg.UpdateBaseURL != "" {
		base = cfg.UpdateBaseURL
	}
	selfupdate.Run(cmd.Context(), base, version.Version, config.Dir())
}

// skipSelfUpdate disables the update check for offline/trivial commands and for
// the explicitly bounded environment diagnostic, where an unrelated update
// request would violate the reviewed request inventory.
func skipSelfUpdate(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "version", "auth", "config", "profile", "environment", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return true
		}
	}
	return false
}
