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

func runVerifyAgentAdapter(args []string) error {
	flags := flag.NewFlagSet("verify-agent-adapter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var manifestPath, adapterPath, bundlePath, contractPath, ledgerPath singleStringFlag
	flags.Var(&manifestPath, "manifest", "extension manifest")
	flags.Var(&adapterPath, "adapter", "agent adapter executable")
	flags.Var(&bundlePath, "bundle", "extension conformance bundle")
	flags.Var(&contractPath, "contract", "agent adapter semantic contract")
	flags.Var(&ledgerPath, "ledger", "durable attempt ledger")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("verify-agent-adapter has invalid flags")
	}
	if manifestPath.duplicate || adapterPath.duplicate || bundlePath.duplicate || contractPath.duplicate || ledgerPath.duplicate {
		return fmt.Errorf("verify-agent-adapter flags may be specified only once")
	}
	if flags.NArg() != 0 || manifestPath.value == "" || adapterPath.value == "" || bundlePath.value == "" ||
		contractPath.value == "" || ledgerPath.value == "" {
		return fmt.Errorf("verify-agent-adapter requires --manifest, --adapter, --bundle, --contract, and --ledger")
	}
	report, err := agenteval.VerifyAgentAdapterProtocolFiles(context.Background(), manifestPath.value, adapterPath.value,
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
