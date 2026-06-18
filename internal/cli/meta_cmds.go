package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the atl version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emit(cmd, map[string]string{"version": version.Version},
				func() string { return version.Version })
		},
	}
}

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{Use: "auth", Short: "Manage Personal Access Tokens (per-user, never in repo)"}

	var service, token, fromFile string
	login := &cobra.Command{
		Use:   "login",
		Short: "Store a PAT for a service (confluence|jira)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := svcOf(service)
			if err != nil {
				return err
			}
			tok := token
			if tok == "" {
				b, err := readBody(fromFile)
				if err != nil {
					return err
				}
				tok = string(b)
			}
			if tok == "" {
				return usageErr("provide --token or --from-file -")
			}
			if err := auth.Login(svc, strings.TrimSpace(tok)); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"service": service, "status": "stored"},
				func() string { return "stored " + service + " PAT" })
		},
	}
	login.Flags().StringVar(&service, "service", "", "confluence|jira")
	login.Flags().StringVar(&token, "token", "", "the PAT (or use --from-file)")
	login.Flags().StringVar(&fromFile, "from-file", "", "read PAT from file or - for stdin")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show where each service's PAT is resolved from (never prints it)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := map[string]string{
				"confluence": auth.Source(auth.Confluence),
				"jira":       auth.Source(auth.Jira),
			}
			return emit(cmd, st, func() string {
				return fmt.Sprintf("confluence: %s\njira: %s", orNone(st["confluence"]), orNone(st["jira"]))
			})
		},
	}

	var logoutSvc string
	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove a stored PAT",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := svcOf(logoutSvc)
			if err != nil {
				return err
			}
			if err := auth.Logout(svc); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"service": logoutSvc, "status": "removed"}, nil)
		},
	}
	logout.Flags().StringVar(&logoutSvc, "service", "", "confluence|jira")

	c.AddCommand(login, status, logout)
	return c
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Get/set non-secret config (backend URLs)"}

	show := &cobra.Command{
		Use:   "show",
		Short: "Show resolved config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return emit(cmd, cfg, func() string {
				return fmt.Sprintf("confluence_url: %s\njira_url: %s\nupdate_base_url: %s",
					cfg.ConfluenceURL, cfg.JiraURL, cfg.UpdateBaseURL)
			})
		},
	}

	var confluenceURL, jiraURL, updateURL string
	set := &cobra.Command{
		Use:   "set",
		Short: "Persist backend URLs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if confluenceURL != "" {
				cfg.ConfluenceURL = confluenceURL
			}
			if jiraURL != "" {
				cfg.JiraURL = jiraURL
			}
			if updateURL != "" {
				cfg.UpdateBaseURL = updateURL
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			return emit(cmd, cfg, nil)
		},
	}
	set.Flags().StringVar(&confluenceURL, "confluence-url", "", "Confluence base URL")
	set.Flags().StringVar(&jiraURL, "jira-url", "", "Jira base URL")
	set.Flags().StringVar(&updateURL, "update-url", "", "atl distribution server base URL (self-update)")

	c.AddCommand(show, set)
	return c
}

func svcOf(s string) (auth.Service, error) {
	switch s {
	case "confluence":
		return auth.Confluence, nil
	case "jira":
		return auth.Jira, nil
	default:
		return "", usageErr("--service must be confluence or jira")
	}
}

func orNone(s string) string {
	if s == "" {
		return "(not configured)"
	}
	return s
}
