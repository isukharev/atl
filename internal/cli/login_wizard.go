package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/version"
)

// wizardIO abstracts the wizard's I/O so tests drive it without a TTY. in
// carries visible prompt answers (Y/n, URL) line by line; out receives the
// prompts (stderr in production); readSecret reads a PAT without echo.
type wizardIO struct {
	in         *bufio.Reader
	out        io.Writer
	readSecret func() (string, error)
}

// svcResult is the per-service outcome reported by the wizard.
type svcResult struct {
	Status string `json:"status"`         // "configured" | "skipped"
	User   string `json:"user,omitempty"` // display name when configured
}

// loginSummary is the wizard's machine-readable result, emitted as JSON.
type loginSummary struct {
	Confluence svcResult `json:"confluence"`
	Jira       svcResult `json:"jira"`
}

// authLogin is an alias so tests in this package can seed a PAT without
// importing internal/auth directly.
func authLogin(s auth.Service, token string) error { return auth.Login(s, token) }

// svcSpec describes one configurable service so Confluence and Jira share the
// same flow.
type svcSpec struct {
	svc    auth.Service
	label  string
	getURL func(*config.Config) string
	setURL func(*config.Config, string)
	verify func(url, token string) (string, error)
}

func wizardSpecs() []svcSpec {
	return []svcSpec{
		{
			svc:    auth.Confluence,
			label:  "Confluence",
			getURL: func(c *config.Config) string { return c.ConfluenceURL },
			setURL: func(c *config.Config, u string) { c.ConfluenceURL = u },
			verify: func(u, t string) (string, error) { return app.VerifyConfluence(u, t, version.Version) },
		},
		{
			svc:    auth.Jira,
			label:  "Jira",
			getURL: func(c *config.Config) string { return c.JiraURL },
			setURL: func(c *config.Config, u string) { c.JiraURL = u },
			verify: func(u, t string) (string, error) { return app.VerifyJira(u, t, version.Version) },
		},
	}
}

// runLoginWizard runs the interactive multi-service setup and returns a summary
// for emit(). Prompts go to wz.out; the caller emits the summary.
func runLoginWizard(wz wizardIO) (loginSummary, error) {
	var sum loginSummary
	results := map[auth.Service]*svcResult{
		auth.Confluence: &sum.Confluence,
		auth.Jira:       &sum.Jira,
	}
	for _, sp := range wizardSpecs() {
		res := results[sp.svc]
		ok, err := promptYesNo(wz, fmt.Sprintf("Configure %s?", sp.label), true)
		if err != nil {
			return sum, err
		}
		if !ok {
			res.Status = "skipped"
			fmt.Fprintf(wz.out, "  - %s: skipped\n", sp.label)
			continue
		}
		if err := configureService(wz, sp, res); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// configureService walks one service: URL -> PAT -> validate -> persist.
// Nothing is written until validation succeeds, so a bad token never overwrites
// a working stored one.
func configureService(wz wizardIO, sp svcSpec, res *svcResult) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	url, err := promptURL(wz, sp, cfg)
	if err != nil {
		return err
	}
	token, err := resolveToken(wz, sp.svc)
	if err != nil {
		return err
	}
	for {
		name, verr := sp.verify(url, token)
		if verr == nil {
			sp.setURL(cfg, url)
			if serr := config.Save(cfg); serr != nil {
				return serr
			}
			if serr := auth.Login(sp.svc, token); serr != nil {
				return serr
			}
			res.Status = "configured"
			res.User = name
			fmt.Fprintf(wz.out, "  ✓ %s: authenticated as %s\n", sp.label, name)
			return nil
		}
		fmt.Fprintf(wz.out, "  ! %s validation failed: %v\n", sp.label, verr)
		retry, perr := promptYesNo(wz, "Retry?", true)
		if perr != nil {
			return perr
		}
		if !retry {
			res.Status = "skipped"
			fmt.Fprintf(wz.out, "  - %s: skipped\n", sp.label)
			return nil
		}
		if token, err = readNewToken(wz); err != nil {
			return err
		}
	}
}

// promptURL asks for the base URL (defaulting to the stored one) and loops
// until it passes the https check.
func promptURL(wz wizardIO, sp svcSpec, cfg *config.Config) (string, error) {
	cur := sp.getURL(cfg)
	for {
		u, err := promptLine(wz, fmt.Sprintf("    %s base URL", sp.label), cur)
		if errors.Is(err, io.EOF) {
			return "", usageErr("no URL provided")
		}
		if err != nil {
			return "", err
		}
		if u == "" {
			fmt.Fprintln(wz.out, "  ! a URL is required")
			continue
		}
		if verr := config.CheckSecureURL(u); verr != nil {
			fmt.Fprintf(wz.out, "  ! %v\n", verr)
			continue
		}
		return u, nil
	}
}

// resolveToken keeps an already-resolvable PAT when the user declines to
// replace it; otherwise reads a new one.
func resolveToken(wz wizardIO, svc auth.Service) (string, error) {
	if existing, err := auth.Token(svc); err == nil && existing != "" {
		replace, perr := promptYesNo(wz, "    Replace stored PAT?", false)
		if perr != nil {
			return "", perr
		}
		if !replace {
			return existing, nil
		}
	}
	return readNewToken(wz)
}

func readNewToken(wz wizardIO) (string, error) {
	fmt.Fprint(wz.out, "    Enter PAT (input hidden): ")
	tok, err := wz.readSecret()
	if err != nil {
		return "", err
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", usageErr("no PAT provided")
	}
	return tok, nil
}

// promptYesNo asks a yes/no question with a default, reading one line. EOF or a
// blank line yields the default.
func promptYesNo(wz wizardIO, q string, def bool) (bool, error) {
	suffix := "(Y/n)"
	if !def {
		suffix = "(y/N)"
	}
	fmt.Fprintf(wz.out, "? %s %s ", q, suffix)
	line, err := wz.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// promptLine reads one line from wz.in.
//
// Behavior:
//   - Non-empty trimmed input → return it.
//   - Blank line (no error) → return "" so the caller can re-prompt.
//   - EOF with a non-empty default → return the default (Ctrl+D keeps stored value).
//   - EOF with no default and no input → return io.EOF so the caller can abort.
//   - Any other read error → propagated unchanged.
func promptLine(wz wizardIO, label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(wz.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(wz.out, "%s: ", label)
	}
	line, err := wz.in.ReadString('\n')
	eof := errors.Is(err, io.EOF)
	if err != nil && !eof {
		return "", err
	}
	if v := strings.TrimSpace(line); v != "" {
		return v, nil
	}
	if def != "" {
		return def, nil // Enter (or EOF) keeps the default
	}
	if eof {
		return "", io.EOF // no default and stdin exhausted: cannot proceed
	}
	return "", nil // blank line, no default: caller re-prompts
}
