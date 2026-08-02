package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/selfupdate"
	"github.com/isukharev/atl/internal/version"
)

// runSelfUpdate performs a best-effort, signature-verified, throttled
// self-replacement before a command runs. It resolves the distribution server
// from config/env, falling back to the build-time default. It runs
// synchronously within selfupdate's total startup budget, never errors a
// command, honors the command's (signal-aware) context so Ctrl-C can cancel an
// in-flight download, and applies any update for the NEXT invocation rather
// than re-execing the current one. Offline/trivial commands skip it.
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
	if localMirrorStatus(cmd) {
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "version", "capabilities", "compatibility", "doctor", "auth", "config", "profile", "environment", "mcp", "mirror", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return true
		}
	}
	return false
}

// localMirrorStatus reports whether a status command is limited to local
// mirror inspection. The --remote form remains an online read and therefore
// keeps the normal config, policy, and self-update behavior.
func localMirrorStatus(cmd *cobra.Command) bool {
	if cmd == nil || cmd.Name() != "status" || cmd.Parent() == nil {
		return false
	}
	switch cmd.Parent().Name() {
	case "conf", "jira":
	default:
		return false
	}
	remote, err := cmd.Flags().GetBool("remote")
	return err == nil && !remote
}
