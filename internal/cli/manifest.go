package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/version"
)

func newManifestCmd() *cobra.Command {
	c := &cobra.Command{Use: "manifest", Short: "Create sanitized local mirror/snapshot manifests"}

	var root, out, command, service, selectors, fields, include string
	create := &cobra.Command{
		Use:   "create",
		Short: "Write a sanitized manifest for a local mirror/snapshot root",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			res, err := app.CreateManifest(app.ManifestOpts{
				Root:      root,
				Out:       out,
				Command:   command,
				Service:   service,
				Selectors: splitFields(selectors),
				Fields:    splitFields(fields),
				Include:   splitFields(include),
				Version:   version.Version,
				BackendURLs: map[string]string{
					"confluence": cfg.ConfluenceURL,
					"jira":       cfg.JiraURL,
				},
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("%s\tfiles=%d\tbytes=%d", res.Path, res.Manifest.Counts.Files, res.Manifest.Counts.Bytes)
			})
		},
	}
	create.Flags().StringVar(&root, "root", "", "local mirror/snapshot root directory")
	create.Flags().StringVar(&out, "out", "", "manifest output path (default: <root>/manifest.json)")
	create.Flags().StringVar(&command, "command", "atl manifest create", "source command recorded in the manifest")
	create.Flags().StringVar(&service, "service", "", "optional backend service: jira, confluence, or generic")
	create.Flags().StringVar(&selectors, "selector", "", "comma-separated selectors to record, for example jql=project=PROJ")
	create.Flags().StringVar(&fields, "fields", "", "comma-separated fields to record")
	create.Flags().StringVar(&include, "include", "", "comma-separated include flags to record")
	c.AddCommand(create)
	return c
}
