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

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
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
)

var outputFormat string

// Execute builds and runs the root command, mapping errors to exit codes.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := newRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(codeFor(err))
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "atl",
		Short:         "Agent-native CLI for Confluence/Jira (mirror, diff-edit, validate, push)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Version,
	}
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "json", "output format: json|text")
	// A flag-parse failure (unknown flag, bad value) is a usage error: map it to
	// exit 2, not the generic 1. Inherited by every subcommand.
	root.SetFlagErrorFunc(func(_ *cobra.Command, e error) error {
		return usageErr("%v", e)
	})
	root.AddCommand(newConfCmd(), newJiraCmd(), newAuthCmd(), newConfigCmd(), newVersionCmd())
	// Validate the global output format, then run a best-effort self-update check
	// (never blocks/fails the command).
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if outputFormat != "json" && outputFormat != "text" {
			return usageErr("invalid --output %q (want json|text)", outputFormat)
		}
		runSelfUpdate(cmd)
		return nil
	}
	return root
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
	case errors.Is(err, domain.ErrUsage):
		return exitUsage
	default:
		return exitGeneric
	}
}

// emit renders v as JSON (default) or, when -o text and a texter is given, text.
func emit(cmd *cobra.Command, v any, text func() string) error {
	w := cmd.OutOrStdout()
	if outputFormat == "text" && text != nil {
		fmt.Fprintln(w, text())
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// loadConfig loads non-secret config (URLs).
func loadConfig() (*config.Config, error) {
	return config.Load()
}

// readBody reads a body from a file path, or stdin when path is "-". Empty path
// yields nil (no body).
func readBody(path string) ([]byte, error) {
	switch path {
	case "":
		return nil, nil
	case "-":
		// Bound stdin so a stray binary/firehose can't exhaust memory.
		return io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	default:
		return os.ReadFile(path)
	}
}

func usageErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{domain.ErrUsage}, a...)...)
}
