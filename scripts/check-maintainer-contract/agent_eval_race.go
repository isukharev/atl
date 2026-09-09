package main

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const agentEvalFacadeMakeContract = `.PHONY: agent-eval-build agent-eval-unit agent-eval-race agent-eval-lint agent-eval-vet agent-eval-vuln agent-eval-tidy-check agent-eval-windows
agent-eval-build:
	$(AGENT_EVAL_MAKE) build

agent-eval-unit:
	$(AGENT_EVAL_MAKE) unit

agent-eval-race:
	$(AGENT_EVAL_MAKE) race

agent-eval-lint:
	$(AGENT_EVAL_MAKE) lint

agent-eval-vet:
	$(AGENT_EVAL_MAKE) vet

agent-eval-vuln:
	$(AGENT_EVAL_MAKE) vuln

agent-eval-tidy-check:
	$(AGENT_EVAL_MAKE) tidy-check

agent-eval-windows:
	$(AGENT_EVAL_MAKE) windows

.PHONY: agent-eval-compat
agent-eval-compat: check-agent-eval-support check-skill-routing
	$(AGENT_EVAL_MAKE) compat

.PHONY: agent-eval-contract
agent-eval-contract: check-skill-routing
	$(AGENT_EVAL_MAKE) contract

.PHONY: agent-eval-product-boundary
agent-eval-product-boundary: check-package-boundary

.PHONY: agent-eval-full
agent-eval-full: $(AGENT_EVAL_FULL_PREREQUISITES)
	$(AGENT_EVAL_MAKE) full

.PHONY: agent-eval-hosted-full-nonrace
agent-eval-hosted-full-nonrace: $(AGENT_EVAL_FULL_PREREQUISITES)
	$(AGENT_EVAL_MAKE) hosted-full-nonrace

.PHONY: agent-eval-hosted-race-shard
agent-eval-hosted-race-shard:
	$(GO_ENV) GOENV=off GOFLAGS= GOOS=linux GOARCH=amd64 GOAMD64=v1 GOEXPERIMENT= CGO_ENABLED=1 go run \
		./scripts/agent-eval-race/main.go \
		./scripts/agent-eval-race/source.go \
		./scripts/agent-eval-race/inventory.go \
		./scripts/agent-eval-race/events.go \
		./scripts/agent-eval-race/process.go \
		./scripts/agent-eval-race/process_linux.go \
		-shard "$${ATL_AGENT_EVAL_RACE_SHARD}" \
		-source "$${ATL_AGENT_EVAL_SOURCE_SHA}"
`

const agentEvalFullPrerequisitesContract = `override AGENT_EVAL_FULL_PREREQUISITES := check-agent-eval-support check-skill-routing check-module-boundary`

func validateAgentEvalRaceRunner(root string) error {
	required := map[string][]string{
		"main.go":          {"shardCount       = 4", "listTimeout      = 5 * time.Minute", "testTimeout      = 47 * time.Minute", `goTestTimeout    = "45m"`, `maxFailureOutput = 64 << 10`, `maxJSONLine      = 1 << 20`, "hosted race bootstrap environment does not bind", "allowed := map[string]bool", "if allowed[key]", `"GOENV=off", "GOFLAGS=", "GOOS=linux"`, `"GOARCH=amd64", "GOAMD64=v1", "GOEXPERIMENT=", "CGO_ENABLED=1"`, "results := make(chan shardResult, 2)", "for range 2"},
		"source.go":        {`"status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"`, `[]string{"-deps", "-json", "./cmd/atl"}`, `[]string{"-race", "-json", "./..."}`, `"GOAMD64", "GOEXPERIMENT", "GOENV", "GOFLAGS"`, `goEnv.GOAMD64 != "v1"`, `goEnv.GOExperiment != ""`, `goEnv.GOENV != ""`, `goEnv.GOFlags != ""`, "verifyActiveFiles(checkCtx, root, tree, active)", "rejectVerboseTests(checkCtx, active)", "gitBlobSHA1(path, info.Size())"},
		"inventory.go":     {`[]string{"list", "-race", "-f", "{{.ImportPath}}", "./..."}`, `"test", "-json", "-race"`, `"-list", "^(Test|Fuzz|Example)", "-count=1"`, "index % shardCount", "root runnable partitions overlap", "root runnable partitions omit inventory"},
		"events.go":        {`[]string{"test", "-json", "-race"}`, `"-count=1", "-timeout="+goTestTimeout`, "type seedState struct", "seed.runs++", "seed.terminals++", "seed.runs != 1 || seed.terminals != 1", "totalBytes > maxCommandOutput", "_ = command.Cancel()", "race execution lacks one runnable run and terminal event", "race execution has incomplete or duplicate observed fuzz seed events"},
		"process_linux.go": {"Setpgid: true", "command.Cancel = func() error", "err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)", "_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)", "command.WaitDelay = 5 * time.Second"},
		"process_other.go": {`errors.New("hosted race process ownership requires linux")`},
		"process.go":       {"cleanupCommand(command)", "command output exceeds its reviewed bound"},
		"main_test.go": {
			"TestPartitionInventoryIsCompleteDisjointAndAutomatic", "TestExecutionEventsRequireEverySelectedTerminalAndObserveSeeds",
			"TestSourceCertificationRejectsInjectedActiveSource", "TestVerboseDependentTestsFailClosed", "TestControlledEnvironmentDropsBackendAndProviderAuthority", "TestControlledEnvironmentOverridesAmbientAndPersistedGoSettings", "TestFuzzSeedRunAndTerminalEvidenceAreDistinct",
		},
		"process_linux_test.go": {"TestLinuxProcessGroupIsKilledOnTimeout", "TestRunJSONProcessKillsDescendantsOnParserRejection", "helper did not publish its child-process handshake"},
	}
	directory := filepath.Join(root, "scripts", "agent-eval-race")
	for name, snippets := range required {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return errors.New("hosted evaluator race runner source is incomplete")
		}
		for _, snippet := range snippets {
			if bytes.Count(body, []byte(snippet)) != 1 {
				return errors.New("hosted evaluator race runner contract has drifted")
			}
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, body, parser.ImportsOnly)
		if err != nil {
			return errors.New("hosted evaluator race runner source is malformed")
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil || strings.HasPrefix(path, "github.com/isukharev/atl/") ||
				strings.Contains(strings.Split(path, "/")[0], ".") {
				return errors.New("hosted evaluator race runner must remain standard-library-only")
			}
		}
	}
	return nil
}
