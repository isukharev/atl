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

func runVerifyGrader(args []string) error {
	flags := flag.NewFlagSet("verify-grader", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var manifestPath, graderPath, bundlePath, contractPath, ledgerPath singleStringFlag
	flags.Var(&manifestPath, "manifest", "extension manifest")
	flags.Var(&graderPath, "grader", "grader executable")
	flags.Var(&bundlePath, "bundle", "extension conformance bundle")
	flags.Var(&contractPath, "contract", "grader semantic contract")
	flags.Var(&ledgerPath, "ledger", "durable attempt ledger")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("verify-grader has invalid flags")
	}
	if manifestPath.duplicate || graderPath.duplicate || bundlePath.duplicate || contractPath.duplicate || ledgerPath.duplicate {
		return fmt.Errorf("verify-grader flags may be specified only once")
	}
	if flags.NArg() != 0 || manifestPath.value == "" || graderPath.value == "" || bundlePath.value == "" ||
		contractPath.value == "" || ledgerPath.value == "" {
		return fmt.Errorf("verify-grader requires --manifest, --grader, --bundle, --contract, and --ledger")
	}
	report, err := agenteval.VerifyGraderProtocolFiles(context.Background(), manifestPath.value, graderPath.value,
		bundlePath.value, contractPath.value, ledgerPath.value)
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
