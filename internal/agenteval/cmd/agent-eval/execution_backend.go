package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/isukharev/atl/internal/agenteval"
)

func runVerifyExecutionBackend(args []string) error {
	flags := flag.NewFlagSet("verify-execution-backend", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var manifestPath, backendPath, bundlePath, contractPath, planPath, ledgerPath singleStringFlag
	flags.Var(&manifestPath, "manifest", "extension manifest")
	flags.Var(&backendPath, "backend", "execution backend executable")
	flags.Var(&bundlePath, "bundle", "extension conformance bundle")
	flags.Var(&contractPath, "contract", "execution backend semantic contract")
	flags.Var(&planPath, "plan", "execution backend trial plan")
	flags.Var(&ledgerPath, "ledger", "durable attempt ledger")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("verify-execution-backend has invalid flags")
	}
	if manifestPath.duplicate || backendPath.duplicate || bundlePath.duplicate || contractPath.duplicate || planPath.duplicate || ledgerPath.duplicate {
		return fmt.Errorf("verify-execution-backend flags may be specified only once")
	}
	if flags.NArg() != 0 || manifestPath.value == "" || backendPath.value == "" || bundlePath.value == "" || contractPath.value == "" || planPath.value == "" || ledgerPath.value == "" {
		return fmt.Errorf("verify-execution-backend requires --manifest, --backend, --bundle, --contract, --plan, and --ledger")
	}
	report, err := agenteval.VerifyExecutionBackendProtocolFiles(context.Background(), manifestPath.value, backendPath.value,
		bundlePath.value, contractPath.value, planPath.value, ledgerPath.value)
	if err != nil {
		return err
	}
	encoded, err := agenteval.EncodeExtensionConformanceReport(report)
	if err != nil {
		return err
	}
	_, err = io.Copy(os.Stdout, bytes.NewReader(encoded))
	return err
}
