package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func newMirrorCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mirror", Short: "Inspect and maintain cross-service mirror invariants", Args: cobra.NoArgs}
	cmd.AddCommand(newMirrorBackendCmd())
	return cmd
}

func newMirrorBackendCmd() *cobra.Command {
	group := &cobra.Command{Use: "backend", Short: "Inspect or explicitly bind content-minimized backend identity", Args: cobra.NoArgs}

	var statusInto string
	status := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Inspect durable backend bindings without loading config, credentials, or network",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && strings.TrimSpace(statusInto) != "" {
				return usageErr("use either [DIR] or --into, not both")
			}
			root := strings.TrimSpace(statusInto)
			if len(args) == 1 {
				root = args[0]
			}
			if root == "" {
				root = mirrorRootDefault("mirror")
			}
			result, err := app.InspectMirrorBackends(root)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return mirrorBackendStatusText(result) })
		},
	}
	status.Flags().StringVar(&statusInto, "into", "", "mirror root (or pass [DIR])")

	var service, into, expected, confirm string
	var apply bool
	bind := &cobra.Command{
		Use:   "bind [DIR]",
		Short: "Preview or apply a local compare-and-set binding to the configured backend",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if service != "confluence" && service != "jira" {
				return usageErr("--service must be confluence or jira")
			}
			if len(args) == 1 && strings.TrimSpace(into) != "" {
				return usageErr("use either [DIR] or --into, not both")
			}
			root := strings.TrimSpace(into)
			if len(args) == 1 {
				root = args[0]
			}
			if root == "" {
				root = mirrorRootDefault("mirror")
			}
			if !apply && (expected != "" || confirm != "") {
				return usageErr("--expected-backend-sha256 and --confirm require --apply")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			rawURL := cfg.ConfluenceURL
			if service == "jira" {
				rawURL = cfg.JiraURL
			}
			if strings.TrimSpace(rawURL) == "" {
				return fmt.Errorf("%w: configured %s backend URL is required", domain.ErrConfig, service)
			}
			var result *app.MirrorBackendBindResult
			if apply {
				result, err = app.ApplyMirrorBackendBind(root, service, rawURL, expected, confirm)
			} else {
				result, err = app.PreviewMirrorBackendBind(root, service, rawURL)
			}
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("%s %s (%s)", result.Service, result.Status, filepath.Clean(result.Root))
			})
		},
	}
	bind.Flags().StringVar(&service, "service", "", "backend service: confluence|jira")
	_ = bind.RegisterFlagCompletionFunc("service", fixedComp("confluence", "jira"))
	bind.Flags().StringVar(&into, "into", "", "mirror root (or pass [DIR])")
	bind.Flags().BoolVar(&apply, "apply", false, "write the reviewed binding locally")
	bind.Flags().StringVar(&expected, "expected-backend-sha256", "", "exact backend hash emitted by preview")
	bind.Flags().StringVar(&confirm, "confirm", "", "must be exactly BIND with --apply")

	group.AddCommand(status, bind)
	return group
}

func mirrorBackendStatusText(result *app.MirrorBackendStatus) string {
	if result == nil || len(result.Bindings) == 0 {
		return "no backend bindings"
	}
	var b strings.Builder
	for _, binding := range result.Bindings {
		fmt.Fprintf(&b, "%s %s\n", binding.Service, binding.OriginSHA256)
	}
	return strings.TrimRight(b.String(), "\n")
}
