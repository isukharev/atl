package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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

	var service, fromFile string
	login := &cobra.Command{
		Use:   "login",
		Short: "Store PATs interactively, or for one service with --service",
		Long: "Run without flags for an interactive wizard that configures each\n" +
			"service's URL and PAT (any service can be skipped). With --service,\n" +
			"store a single PAT read from --from-file, piped stdin, or a no-echo\n" +
			"prompt — the token is never accepted on the command line.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if service == "" {
				return runInteractiveLogin(cmd)
			}
			svc, err := svcOf(service)
			if err != nil {
				return err
			}
			tok, err := readPAT(fromFile)
			if err != nil {
				return err
			}
			tok = strings.TrimSpace(tok)
			if tok == "" {
				return usageErr("no PAT provided (use --from-file, pipe via stdin, or type it at the prompt)")
			}
			if err := auth.Login(svc, tok); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"service": service, "status": "stored"},
				func() string { return "stored " + service + " PAT" })
		},
	}
	login.Flags().StringVar(&service, "service", "", "confluence|jira")
	login.Flags().StringVar(&fromFile, "from-file", "", "read PAT from a file, or - for stdin")

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
			// Reject an insecure (cleartext) backend URL at set time, not only at
			// run time, so a PAT-leaking URL is never silently persisted.
			if confluenceURL != "" {
				if err := config.CheckSecureURL(confluenceURL); err != nil {
					return usageErr("%v", err)
				}
				cfg.ConfluenceURL = confluenceURL
			}
			if jiraURL != "" {
				if err := config.CheckSecureURL(jiraURL); err != nil {
					return usageErr("%v", err)
				}
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

// readPAT obtains a PAT without ever placing it on the command line. With
// --from-file it reads that file (or stdin for "-"); otherwise it prompts on a
// TTY without echo, or reads piped stdin when not attached to a terminal.
func readPAT(fromFile string) (string, error) {
	if fromFile != "" {
		b, err := readBody(fromFile)
		return string(b), err
	}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Enter PAT (input hidden): ")
		return readSecretNoEcho(fd)
	}
	b, err := readBody("-")
	return string(b), err
}

// readSecretNoEcho reads one line from the TTY fd without echo. term.ReadPassword
// restores the terminal on a normal return, but a signal mid-read would leave the
// shell with hidden input; restore and exit on interrupt/terminate. The caller
// prints the prompt; this prints the trailing newline ReadPassword suppresses.
func readSecretNoEcho(fd int) (string, error) {
	if prev, err := term.GetState(fd); err == nil {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		done := make(chan struct{})
		defer func() { signal.Stop(sig); close(done) }()
		go func() {
			select {
			case <-sig:
				_ = term.Restore(fd, prev)
				os.Exit(130) // 128 + SIGINT
			case <-done:
			}
		}()
	}
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
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
