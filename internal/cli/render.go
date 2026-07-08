package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
)

// renderFlags collects the three per-run markdown-view override flags shared by
// `pull` and `render` on both backends. Empty values mean "no override" — the
// effective settings then come from local + global config + defaults.
type renderFlags struct {
	profile string
	include string
	exclude string
}

// register adds --render-profile/--render-include/--render-exclude to a command.
func (rf *renderFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&rf.profile, "render-profile", "", "markdown view profile: minimal|default|full (overrides config)")
	cmd.Flags().StringVar(&rf.include, "render-include", "", "comma-separated sections to add to the profile")
	cmd.Flags().StringVar(&rf.exclude, "render-exclude", "", "comma-separated sections to remove from the profile")
}

// override builds the config.RenderService the app layer merges over config. It
// validates the profile up front so a bad --render-profile is a usage error
// (exit 2) rather than a silent fallback.
func (rf *renderFlags) override() (config.RenderService, error) {
	if !config.ValidProfile(rf.profile) {
		return config.RenderService{}, usageErr(fmt.Sprintf("--render-profile %q is invalid (want minimal|default|full)", rf.profile))
	}
	return config.RenderService{
		Profile: rf.profile,
		Include: splitCommaList(rf.include),
		Exclude: splitCommaList(rf.exclude),
	}, nil
}

// splitCommaList trims a comma-separated flag value into a clean slice.
func splitCommaList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// warnRender prints render-resolution warnings (unknown section names, malformed
// local config) on stderr — never stdout, which carries the JSON result.
func warnRender(w io.Writer, warnings []string) {
	for _, msg := range warnings {
		fmt.Fprintf(w, "warning: %s\n", msg)
	}
}
