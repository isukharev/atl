// Package cli builds the cobra command tree. Commands are thin: they parse
// flags, call an app use-case, render the result, and translate domain errors
// into process exit codes. No business logic lives here.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
	"github.com/isukharev/atl/internal/version"
)

// Exit codes (kept in sync with the design spec).
const (
	exitOK           = 0
	exitGeneric      = 1
	exitUsage        = 2
	exitAuth         = 3
	exitNotFound     = 4
	exitVersionConfl = 5
	exitForbidden    = 6
	exitConfig       = 7
	exitCheckFailed  = 8
)

var (
	outputFormat string
	verbose      bool
	readOnly     bool
)

// Execute builds and runs the root command, mapping errors to exit codes.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := newRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		code := codeFor(err)
		writeError(os.Stderr, outputFormat, err, code)
		os.Exit(code)
	}
}

// writeError renders a failed command's error to w. With JSON output (the
// default) it emits a single machine-readable object with error/code plus
// stable local kind/remediation metadata so a
// script can parse stderr the same way it parses stdout; with `-o text` it
// prints the familiar `error: <msg>` line. The exit code is echoed in the JSON
// so a caller that only captured stderr still learns the classification.
func writeError(w io.Writer, format string, err error, code int) {
	if format == "text" {
		fmt.Fprintln(w, "error:", err)
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	body := map[string]any{"error": err.Error(), "code": code}
	kind, remediation := classifyError(err)
	body["kind"] = kind
	if remediation != "" {
		body["remediation"] = remediation
	}
	if command, ok := readOnlyErrorMetadata(err); ok {
		body["policy"] = "read_only"
		body["command"] = command
	}
	// Encode never fails for these plain types; ignore its error.
	_ = enc.Encode(body)
}

func classifyError(err error) (kind, remediation string) {
	if _, ok := accessPolicyInvariantMetadata(err); ok {
		return "internal_error", "report_bug"
	}
	if _, ok := readOnlyErrorMetadata(err); ok {
		return "read_only_policy", "request_human_approval"
	}
	switch {
	case errors.Is(err, domain.ErrAuth):
		return "authentication_failed", "reauthenticate"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found", "verify_identifier_or_access"
	case errors.Is(err, domain.ErrVersionConflict):
		return "version_conflict", "refresh_and_reapply"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden", "request_access"
	case errors.Is(err, domain.ErrConfig):
		return "configuration_error", "complete_configuration"
	case errors.Is(err, domain.ErrCheckFailed):
		return "check_failed", "review_failed_check"
	case errors.Is(err, domain.ErrUsage):
		return "usage_error", "fix_request"
	}
	var transportErr *httpx.TransportError
	if errors.As(err, &transportErr) {
		return "transport_error", "inspect_network_before_retry"
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return "api_error", "inspect_backend_error"
	}
	return "unexpected_error", "inspect_error"
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "atl",
		Short:         "Agent-native CLI for Confluence/Jira (mirror, diff-edit, validate, push)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "json", "output format: json|text|id")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "trace HTTP requests/responses to stderr (token never logged); also ATL_VERBOSE=1")
	root.PersistentFlags().BoolVar(&readOnly, "read-only", false, "block every mutating command before credentials, stdin, or network access; also ATL_READ_ONLY=1")
	_ = root.RegisterFlagCompletionFunc("output", fixedComp("json", "text", "id"))
	// A flag-parse failure (unknown flag, bad value) is a usage error: map it to
	// exit 2, not the generic 1. Inherited by every subcommand.
	root.SetFlagErrorFunc(func(_ *cobra.Command, e error) error {
		return usageErr("%v", e)
	})
	root.AddCommand(newConfCmd(), newJiraCmd(), newAuthCmd(), newConfigCmd(), newProfileCmd(), newManifestCmd(), newVersionCmd())
	// Validate the global output format, then run a best-effort self-update check
	// (never blocks/fails the command).
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		switch outputFormat {
		case "json", "text", "id":
		default:
			return usageErr("invalid --output %q (want json|text|id)", outputFormat)
		}
		if err := enforceOutputContract(cmd); err != nil {
			return err
		}
		policyEnabled, err := resolveReadOnlyPolicy(cmd, readOnly)
		if err != nil {
			return err
		}
		if err := enforceAccessPolicy(cmd, policyEnabled); err != nil {
			return err
		}
		// --verbose (or ATL_VERBOSE) traces every HTTP request to stderr. The
		// bearer token is never written. stdout stays reserved for the result.
		if verbose || os.Getenv("ATL_VERBOSE") != "" {
			httpx.SetTrace(cmd.ErrOrStderr())
		}
		if !policyEnabled {
			runSelfUpdate(cmd)
		}
		return nil
	}
	// Cobra otherwise registers these commands lazily inside ExecuteC, after our
	// access-policy walk. Initialize the public built-ins now; hidden __complete
	// requests are recognized narrowly in enforceAccessPolicy because Cobra has
	// no public initializer for them.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, builtIn := range root.Commands() {
		if builtIn.Name() == "help" {
			// Cobra deliberately accepts a command path of arbitrary depth.
			// Declare that policy explicitly so our no-nil Args invariant still
			// covers every initialized executable command.
			builtIn.Args = cobra.ArbitraryArgs
			break
		}
	}
	normalizeArgs(root)
	classifyCommandTree(root)
	return root
}

// normalizeArgs walks the built tree and (1) gives every leaf command that
// declares no Args policy and no positional placeholder in its Use line a
// cobra.NoArgs policy, so a stray positional argument fails instead of being
// silently dropped (`jira issue search --jql … PROJ-1` used to run on the
// full JQL result); (2) wraps every leaf's Args validation so its failure is
// a usage error (exit 2, matching SetFlagErrorFunc) rather than the generic
// exit 1. Positional commands keep their declared arity; new flag-only
// commands are covered by default.
func normalizeArgs(c *cobra.Command) {
	for _, sub := range c.Commands() {
		if len(sub.Commands()) > 0 {
			normalizeArgs(sub)
			continue
		}
		policy := sub.Args
		if policy == nil {
			if strings.ContainsAny(sub.Use, "<[") {
				continue // positional command missing arity: caught by tests
			}
			policy = cobra.NoArgs
		}
		inner := policy
		sub.Args = func(cmd *cobra.Command, args []string) error {
			if err := inner(cmd, args); err != nil {
				return usageErr("%v", err)
			}
			return nil
		}
	}
}

func codeFor(err error) int {
	switch {
	case errors.Is(err, domain.ErrAuth):
		return exitAuth
	case errors.Is(err, domain.ErrNotFound):
		return exitNotFound
	case errors.Is(err, domain.ErrVersionConflict):
		return exitVersionConfl
	case errors.Is(err, domain.ErrForbidden):
		return exitForbidden
	case errors.Is(err, domain.ErrConfig):
		return exitConfig
	case errors.Is(err, domain.ErrCheckFailed):
		return exitCheckFailed
	case errors.Is(err, domain.ErrUsage):
		return exitUsage
	default:
		return exitGeneric
	}
}

// emit renders v as JSON (default) or, when -o text and a texter is given, text.
// `-o id` is only meaningful for commands that emit identifiers; a command that
// has no id projection routes through here and reports the unsupported format
// rather than silently dumping JSON.
func emit(cmd *cobra.Command, v any, text func() string) error {
	w := cmd.OutOrStdout()
	switch outputFormat {
	case "text":
		if text != nil {
			fmt.Fprintln(w, text())
			return nil
		}
		return usageErr("-o text is not supported for this command; use -o json")
	case "id":
		return usageErr("-o id is not supported for this command")
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// emitID is emit plus an `-o id` projection: when the output format is `id` it
// prints just the primary identifier(s), one per line, for safe piping
// (`atl jira issue search --jql … -o id | xargs …`). For json/text it defers to
// emit. ids must be non-nil for a command to advertise id support.
func emitID(cmd *cobra.Command, v any, text func() string, ids func() []string) error {
	if outputFormat == "id" {
		if ids == nil {
			return usageErr("-o id is not supported for this command")
		}
		w := cmd.OutOrStdout()
		for _, id := range ids() {
			fmt.Fprintln(w, id)
		}
		return nil
	}
	return emit(cmd, v, text)
}

// loadConfig loads non-secret config (URLs).
func loadConfig() (*config.Config, error) {
	return config.Load()
}

// mirrorRootDefault resolves the default mirror root for pull/status commands.
// ATL_MIRROR_ROOT lets a workspace fix one mirror location (per the setup
// skill's `~/.atl/<workspace>/` convention) so pull and a later push/status
// agree without the caller re-passing --into every time; when it is unset the
// command's own fallback ("mirror" / "mirror-jira") is used. An explicit --into
// flag still wins, since cobra only applies this default when the flag is
// absent.
func mirrorRootDefault(fallback string) string {
	if v := strings.TrimSpace(os.Getenv("ATL_MIRROR_ROOT")); v != "" {
		return v
	}
	return fallback
}

// stdinBodyCap bounds a stdin body so a stray binary/firehose can't exhaust
// memory. Exceeding it is a loud usage error, never a silent truncation — a
// truncated Jira body would be sent as-is (no validation gate catches it).
const stdinBodyCap = 64 << 20 // 64 MiB

// stdinIsTerminal reports whether stdin is an interactive terminal. A var so
// tests can simulate a TTY without one.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// readBody reads a body from a file path, or stdin when path is "-". Empty path
// yields nil (no body). Reading stdin on an interactive terminal is refused:
// body-required commands default `--from-file` to "-", so a user who forgot
// the flag would otherwise hang forever waiting on the terminal.
func readBody(path string) ([]byte, error) {
	switch path {
	case "":
		return nil, nil
	case "-":
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no body was piped; pass --from-file FILE (or --from-md FILE where supported), or pipe the body")
		}
		return readBounded(os.Stdin, stdinBodyCap)
	default:
		return os.ReadFile(path)
	}
}

func readFileBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, usageErr("input file %q exceeds the %d MiB limit", path, max>>20)
	}
	return data, nil
}

// readBounded reads up to max bytes from r, returning a usage error when the
// input is larger rather than silently truncating it.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, usageErr("stdin body exceeds the %d MiB limit; pass a file path instead", max>>20)
	}
	return data, nil
}

func usageErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{domain.ErrUsage}, a...)...)
}
