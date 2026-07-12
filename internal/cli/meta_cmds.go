package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/isukharev/atl/internal/app"
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
				if fromFile != "" {
					return usageErr("--from-file requires --service")
				}
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
	_ = login.RegisterFlagCompletionFunc("service", fixedComp("confluence", "jira"))

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
	_ = logout.RegisterFlagCompletionFunc("service", fixedComp("confluence", "jira"))

	c.AddCommand(login, status, logout)
	return c
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Get/set non-secret config (backend URLs, render.*)"}

	show := &cobra.Command{
		Use:   "show",
		Short: "Show resolved config (effective render + provenance)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			// Resolve the local (per-mirror) render layer from cwd. Warnings from a
			// shared/hostile local file go to stderr and never influence output.
			local, localPath := loadLocalFromCwd(cmd.ErrOrStderr())
			render, prov := config.EffectiveRender(cfg, local)
			out := configShowResult{
				ConfluenceURL:    cfg.ConfluenceURL,
				JiraURL:          cfg.JiraURL,
				UpdateBaseURL:    cfg.UpdateBaseURL,
				Render:           render,
				JiraListViews:    cfg.JiraListViews,
				RenderProvenance: nonDefaultProvenance(prov),
				LocalConfigPath:  localPath,
				Mirror:           mirrorHints(),
			}
			return emit(cmd, out, func() string { return configShowText(out) })
		},
	}

	var confluenceURL, jiraURL, updateURL string
	var local bool
	var into string
	set := &cobra.Command{
		Use:   "set [<key> <value>]",
		Short: "Persist backend URLs or a render.* key",
		Long: "Persist backend URLs (via --confluence-url/--jira-url/--update-url) or a\n" +
			"dotted render key positionally, e.g. `config set render.jira.profile full`.\n" +
			"Valid render keys: " + strings.Join(config.ValidRenderKeys(), ", ") + ".\n" +
			"include/exclude/custom_fields take a comma-separated value.\n\n" +
			"--local writes the per-mirror <root>/.atl/config.json (render.* only; a\n" +
			"credential-adjacent key is refused). The mirror root is the nearest .atl\n" +
			"walking up from cwd, or --into ROOT.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, hasKV, err := parseSetArgs(args)
			if err != nil {
				return err
			}
			if local {
				return runSetLocal(cmd, key, value, hasKV, into, confluenceURL, jiraURL, updateURL)
			}
			return runSetGlobal(cmd, key, value, hasKV, confluenceURL, jiraURL, updateURL)
		},
	}
	set.Flags().StringVar(&confluenceURL, "confluence-url", "", "Confluence base URL")
	set.Flags().StringVar(&jiraURL, "jira-url", "", "Jira base URL")
	set.Flags().StringVar(&updateURL, "update-url", "", "atl distribution server base URL (self-update)")
	set.Flags().BoolVar(&local, "local", false, "write the per-mirror .atl/config.json (render.* only)")
	set.Flags().StringVar(&into, "into", "", "mirror root for --local (defaults to nearest .atl)")

	c.AddCommand(show, set)
	return c
}

// parseSetArgs splits the optional positional key/value. Exactly zero or two
// positionals are accepted; a lone key is a usage error.
func parseSetArgs(args []string) (key, value string, hasKV bool, err error) {
	switch len(args) {
	case 0:
		return "", "", false, nil
	case 2:
		return args[0], args[1], true, nil
	default:
		return "", "", false, usageErr("config set takes a key AND a value (e.g. `config set render.jira.profile full`)")
	}
}

// runSetGlobal persists URL flags and/or a render key to the global config.
func runSetGlobal(cmd *cobra.Command, key, value string, hasKV bool, confluenceURL, jiraURL, updateURL string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// Reject an insecure (cleartext) backend URL at set time, not only at run
	// time, so a PAT-leaking URL is never silently persisted.
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
	if hasKV {
		if key == "jira.list_views" || strings.HasPrefix(key, "jira.list_views.") {
			views, setErr := config.SetJiraListViewsJSON(cfg.JiraListViews, key, value)
			if setErr != nil {
				return usageErr("%v", setErr)
			}
			cfg.JiraListViews = views
		} else {
			if cfg.Render == nil {
				cfg.Render = &config.RenderConfig{}
			}
			if err := applyRenderKey(cfg.Render, key, value); err != nil {
				return err
			}
		}
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	return emit(cmd, cfg, nil)
}

// runSetLocal writes a render.* key to the per-mirror local config. It refuses
// any credential-adjacent flag and any non-render key — a mirror-local file must
// never redirect where the PAT is sent.
func runSetLocal(cmd *cobra.Command, key, value string, hasKV bool, into, confluenceURL, jiraURL, updateURL string) error {
	if confluenceURL != "" || jiraURL != "" || updateURL != "" {
		return usageErr("--local accepts render.* keys only; backend/update URLs are global-only (a shared mirror file must not redirect the PAT)")
	}
	if !hasKV {
		return usageErr("config set --local needs a render key and value, e.g. `config set --local render.jira.profile full`")
	}
	if key == "jira.list_views" || strings.HasPrefix(key, "jira.list_views.") {
		return usageErr("%s is global-only; omit --local", key)
	}
	if key == "render.confluence.jira_macros" {
		return usageErr("%s is global-only because it controls authenticated Jira reads; omit --local", key)
	}
	root, err := resolveLocalRoot(into)
	if err != nil {
		return err
	}
	// Start from the existing (sanitized) local render so repeated sets
	// accumulate; forbidden/unknown keys already in the file are dropped.
	lc, warnings, err := config.LoadLocal(root)
	if err != nil {
		return err
	}
	emitWarnings(cmd.ErrOrStderr(), warnings)
	rc := &config.RenderConfig{}
	if lc != nil && lc.Render != nil {
		rc = lc.Render
	}
	if err := applyRenderKey(rc, key, value); err != nil {
		return err
	}
	if err := config.SaveLocal(root, &config.LocalConfig{Render: rc}); err != nil {
		return err
	}
	return emit(cmd, map[string]any{
		"local_config_path": config.LocalConfigPath(root),
		"render":            rc,
	}, nil)
}

// applyRenderKey wraps config.SetRenderKey, mapping validation failures to a
// usage error (exit 2) with the valid-keys list for the not-a-render-key case.
func applyRenderKey(rc *config.RenderConfig, key, value string) error {
	err := config.SetRenderKey(rc, key, value)
	if err == nil {
		return nil
	}
	if errors.Is(err, config.ErrNotRenderKey) {
		return usageErr("unknown config key %q; valid render keys: %s", key, strings.Join(config.ValidRenderKeys(), ", "))
	}
	return usageErr("%v", err)
}

// resolveLocalRoot picks the mirror root for --local: an explicit --into, else
// the nearest .atl walking up from cwd. No mirror found is a usage error.
func resolveLocalRoot(into string) (string, error) {
	if into != "" {
		return into, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, ok := app.MirrorRootOf(cwd)
	if !ok {
		return "", usageErr("no mirror found from the current directory (looked for an .atl dir walking up); run from inside a mirror or pass --into ROOT")
	}
	return root, nil
}

// loadLocalFromCwd resolves the per-mirror local config from cwd for `config
// show`, emitting any warnings to w. It returns the parsed local config (nil
// when none) and the file path (empty unless a local file was actually loaded).
func loadLocalFromCwd(w io.Writer) (*config.LocalConfig, string) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, ""
	}
	root, ok := app.MirrorRootOf(cwd)
	if !ok {
		return nil, ""
	}
	local, warnings, err := config.LoadLocal(root)
	emitWarnings(w, warnings)
	if err != nil || local == nil {
		return nil, ""
	}
	return local, config.LocalConfigPath(root)
}

func emitWarnings(w io.Writer, warnings []string) {
	for _, warn := range warnings {
		fmt.Fprintln(w, "warning: "+warn)
	}
}

// nonDefaultProvenance keeps only keys whose value did not come from the built-in
// default, so `config show` stays lean (an all-default mirror emits nothing).
func nonDefaultProvenance(prov config.Provenance) map[string]string {
	out := map[string]string{}
	for k, v := range prov {
		if v != "default" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func configShowText(out configShowResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "confluence_url: %s\njira_url: %s\nupdate_base_url: %s\n",
		out.ConfluenceURL, out.JiraURL, out.UpdateBaseURL)
	fmt.Fprintf(&b, "render_jira_profile: %s\nrender_confluence_profile: %s\n",
		out.Render.Jira.Profile, out.Render.Confluence.Profile)
	viewNames := make([]string, 0, len(out.JiraListViews))
	for name := range out.JiraListViews {
		viewNames = append(viewNames, name)
	}
	sort.Strings(viewNames)
	fmt.Fprintf(&b, "jira_list_views: %s\n", strings.Join(viewNames, ","))
	if len(out.RenderProvenance) > 0 {
		keys := make([]string, 0, len(out.RenderProvenance))
		for k := range out.RenderProvenance {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "provenance %s: %s\n", k, out.RenderProvenance[k])
		}
	}
	if out.LocalConfigPath != "" {
		fmt.Fprintf(&b, "local_config_path: %s\n", out.LocalConfigPath)
	}
	fmt.Fprintf(&b, "mirror_recommended_root: %s\nmirror_active_root: %s",
		out.Mirror.RecommendedRoot, orNone(out.Mirror.ActiveRoot))
	return b.String()
}

type configShowResult struct {
	ConfluenceURL    string                         `json:"confluence_url,omitempty"`
	JiraURL          string                         `json:"jira_url,omitempty"`
	UpdateBaseURL    string                         `json:"update_base_url,omitempty"`
	Render           config.RenderConfig            `json:"render"`
	JiraListViews    map[string]config.JiraListView `json:"jira_list_views"`
	RenderProvenance map[string]string              `json:"render_provenance,omitempty"`
	LocalConfigPath  string                         `json:"local_config_path,omitempty"`
	Mirror           mirrorHint                     `json:"mirror"`
}

type mirrorHint struct {
	RecommendedRoot string `json:"recommended_root"`
	ActiveRoot      string `json:"active_root,omitempty"`
	ActiveSource    string `json:"active_source,omitempty"`
}

func mirrorHints() mirrorHint {
	h := mirrorHint{RecommendedRoot: "~/.atl/<workspace>/"}
	if v := strings.TrimSpace(os.Getenv("ATL_MIRROR_ROOT")); v != "" {
		h.ActiveRoot = v
		h.ActiveSource = "ATL_MIRROR_ROOT"
	}
	return h
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
