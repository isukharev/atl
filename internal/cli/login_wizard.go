package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	verify func(ctx context.Context, url, token string) (string, error)
}

func wizardSpecs() []svcSpec {
	return []svcSpec{
		{
			svc:    auth.Confluence,
			label:  "Confluence",
			getURL: func(c *config.Config) string { return c.ConfluenceURL },
			setURL: func(c *config.Config, u string) { c.ConfluenceURL = u },
			verify: func(ctx context.Context, u, t string) (string, error) {
				return app.VerifyConfluence(ctx, u, t, version.Version)
			},
		},
		{
			svc:    auth.Jira,
			label:  "Jira",
			getURL: func(c *config.Config) string { return c.JiraURL },
			setURL: func(c *config.Config, u string) { c.JiraURL = u },
			verify: func(ctx context.Context, u, t string) (string, error) {
				return app.VerifyJira(ctx, u, t, version.Version)
			},
		},
	}
}

// runLoginWizard runs the interactive multi-service setup and returns a summary
// for emit(). Prompts go to wz.out; the caller emits the summary.
func runLoginWizard(ctx context.Context, wz wizardIO) (loginSummary, error) {
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
		if err := configureService(ctx, wz, sp, res); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// configureService walks one service: URL -> PAT -> validate -> persist.
// Nothing is persisted until validation succeeds, so a bad token never
// overwrites a working stored one. Persist is two atomic writes (credentials,
// then config); the PAT is written first so a failure of the second leaves the
// stored config URL unchanged rather than recording a new URL paired with a
// stale/absent PAT.
func configureService(ctx context.Context, wz wizardIO, sp svcSpec, res *svcResult) error {
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
		name, verr := sp.verify(ctx, url, token)
		if verr == nil {
			if serr := auth.Login(sp.svc, token); serr != nil {
				return serr
			}
			sp.setURL(cfg, url)
			if serr := config.Save(cfg); serr != nil {
				return serr
			}
			res.Status = "configured"
			res.User = name
			fmt.Fprintf(wz.out, "  ✓ %s: %s\n", sp.label, authedAs(name))
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
// until it passes the https check. On EOF (Ctrl+D / exhausted stdin) it accepts
// a stored default only when that default is itself secure; an empty or insecure
// default aborts rather than re-prompting forever with an unusable value (a
// non-TTY caller cannot supply more input, so re-prompting would busy-loop).
func promptURL(wz wizardIO, sp svcSpec, cfg *config.Config) (string, error) {
	cur := sp.getURL(cfg)
	for {
		u, err := promptLine(wz, fmt.Sprintf("    %s base URL", sp.label), cur)
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			return "", err
		}
		if u == "" {
			if eof {
				return "", usageErr("no URL provided")
			}
			fmt.Fprintln(wz.out, "  ! a URL is required")
			continue
		}
		if verr := config.CheckSecureURL(u); verr != nil {
			if eof {
				return "", usageErr("%v", verr)
			}
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

// promptYesNo asks a yes/no question with a default, reading one line. A blank
// line (real Enter) yields the shown default. EOF with no input returns false
// (decline) so a Ctrl+D / exhausted stdin never spins the retry loop.
func promptYesNo(wz wizardIO, q string, def bool) (bool, error) {
	suffix := "(Y/n)"
	if !def {
		suffix = "(y/N)"
	}
	fmt.Fprintf(wz.out, "? %s %s ", q, suffix)
	line, err := wz.in.ReadString('\n')
	eof := errors.Is(err, io.EOF)
	if err != nil && !eof {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	case "":
		if eof {
			return false, nil // EOF (Ctrl+D): decline, never loop on the default
		}
		return def, nil // blank line: accept the shown default
	default:
		return def, nil
	}
}

// promptLine reads one line from wz.in.
//
// Behavior:
//   - Non-empty trimmed input → return it (even when EOF arrives with the line).
//   - Blank line (Enter, no error) → return the default (may be "").
//   - EOF with no buffered input → return the default *and* io.EOF, so the
//     caller can decide whether the default is usable or it must abort. Returning
//     io.EOF even with a non-empty default is what lets promptURL break out
//     instead of busy-looping on a default that fails a later check.
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
	if eof {
		return def, io.EOF // stdin exhausted: surface EOF; caller decides on def
	}
	if def != "" {
		return def, nil // blank line (Enter): keep the default
	}
	return "", nil // blank line, no default: caller re-prompts
}

// runInteractiveLogin runs the wizard against the real terminal. It requires a
// TTY; a non-interactive invocation gets a usage error pointing at the scriptable
// paths.
func runInteractiveLogin(cmd *cobra.Command) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return usageErr("interactive setup needs a terminal; use `--service` with piped stdin or --from-file, or `atl config set` for non-interactive setup")
	}
	wz := wizardIO{
		in:         bufio.NewReader(os.Stdin),
		out:        os.Stderr,
		readSecret: func() (string, error) { return readSecretNoEcho(fd) },
	}
	sum, err := runLoginWizard(cmd.Context(), wz)
	if err != nil {
		return err
	}
	return emit(cmd, sum, func() string { return wizardText(sum) })
}

// authedAs renders the success line, tolerating a backend whose whoami returns
// an empty display name (a 200 with no name still means the PAT is valid).
func authedAs(name string) string {
	if name == "" {
		return "authenticated"
	}
	return "authenticated as " + name
}

// wizardText renders the summary for `-o text`.
func wizardText(s loginSummary) string {
	render := func(label string, r svcResult) string {
		if r.Status == "configured" {
			if r.User == "" {
				return label + ": configured"
			}
			return fmt.Sprintf("%s: configured (%s)", label, r.User)
		}
		return fmt.Sprintf("%s: %s", label, r.Status)
	}
	return render("confluence", s.Confluence) + "\n" + render("jira", s.Jira)
}
