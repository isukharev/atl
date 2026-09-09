package main

import (
	"fmt"
	"strings"
)

const ciCheckoutStepContract = checkoutStepContract + `
        with:
          ref: ${{ github.sha }}`

const ciTriggerContract = `on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
    types: [ready_for_review]
`

const bindingJobContract = `  binding:
    if: github.event_name == 'pull_request'
    outputs:
      plan: ${{ steps.binding.outputs.plan }}
      product: ${{ steps.binding.outputs.product }}
      evaluator: ${{ steps.binding.outputs.evaluator }}
      platform: ${{ steps.binding.outputs.platform }}
      security: ${{ steps.binding.outputs.security }}
      corpus: ${{ steps.binding.outputs.corpus }}
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: Bind current pull request revision
        id: binding
        env:
          GH_TOKEN: ${{ github.token }}
        run: env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go run ./scripts/check-premerge bind
`

const readyJobContract = `  ci-ready:
    if: always() && github.event_name == 'pull_request'
    needs: [binding, contracts, test, corpus-devcontainer, agent-eval, agent-eval-race, agent-eval-platform, agent-eval-extension-windows, lint, govulncheck, codeql]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: Require complete checks and current pull request revision
        env:
          GH_TOKEN: ${{ github.token }}
          ATL_PREMERGE_NEEDS: ${{ toJSON(needs) }}
        run: env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go run ./scripts/check-premerge ready
`

const codeQLCallJobContract = `  codeql:
    needs: binding
    if: needs.binding.outputs.security == 'true'
    permissions:
      contents: read
      security-events: write
    uses: ./.github/workflows/codeql.yml
    with:
      analysis_category: ci-ready
`

const contractsJobContract = `  contracts:
    needs: binding
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: Maintainer toolchain contract
        run: make check-maintainer-contract
      - name: Agent-eval support policy
        run: make check-agent-eval-support
      - name: Two-module package boundary
        run: make check-package-boundary
      - name: Maintainability ratchets
        run: make check-maintainability
      - name: Generated plugin trees are current
        run: make check-plugins
      - name: Documentation catalog
        run: make check-docs-catalog
      - name: Documentation freshness
        env:
          ATL_DOCS_BASE: ${{ github.event.pull_request.base.sha }}
          ATL_DOCS_HEAD: ${{ github.event.pull_request.head.sha }}
        run: make check-docs-freshness
      - name: Repository maintainer skills
        run: make check-repository-skills
      - name: Reference split compatibility
        run: make check-reference-split
      - name: Indexed documentation contract
        run: make check-context7-docs
      - name: Onboarding documentation rehearsal
        run: make check-onboarding-docs
`

const lintJobContract = `  lint:
    needs: binding
    if: needs.binding.outputs.product == 'true'
    runs-on: ubuntu-latest
    steps:
` + ciCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: golangci-lint
        uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0
        with:
          # Pinned (not "latest") so a compromised/yanked upstream release cannot
          # silently enter CI. Must be a v2 build compiled with Go >= 1.26 (the
          # module's go directive); bump deliberately when the toolchain moves.
          version: v2.12.2
`

const evaluatorJobContract = `  agent-eval:
    needs: binding
    if: needs.binding.outputs.evaluator == 'compat' || needs.binding.outputs.evaluator == 'full'
    timeout-minutes: 75
    runs-on: ubuntu-latest
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: Product compatibility contract
        if: needs.binding.outputs.evaluator == 'compat'
        run: make agent-eval-compat
      - name: Complete non-race agent-evaluation gates
        if: needs.binding.outputs.evaluator == 'full'
        run: make agent-eval-hosted-full-nonrace
`

const evaluatorRaceJobContract = `  agent-eval-race:
    needs: binding
    if: needs.binding.outputs.evaluator == 'full'
    timeout-minutes: 75
    strategy:
      fail-fast: false
      max-parallel: 4
      matrix:
        shard: [0, 1, 2, 3]
    runs-on: ubuntu-latest
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: Complete source-bound evaluator race shard
        env:
          ATL_AGENT_EVAL_RACE_SHARD: ${{ matrix.shard }}
          ATL_AGENT_EVAL_SOURCE_SHA: ${{ github.sha }}
        run: make agent-eval-hosted-race-shard
`

const platformJobContract = `  agent-eval-platform:
    needs: binding
    if: needs.binding.outputs.platform == 'true'
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
` + ciCheckoutStepContract + "\n" + setupGoStepContract + "\n" + extensionProtocolRuntimeStepContract + "\n" + schedulerRuntimeStepContract + "\n"

const windowsRuntimeJobContract = `  agent-eval-extension-windows:
    needs: binding
    if: needs.binding.outputs.platform == 'true'
    runs-on: windows-latest
    steps:
` + ciCheckoutStepContract + "\n" + setupGoStepContract + "\n" + extensionProtocolWindowsRuntimeStepContract + "\n" + schedulerWindowsRuntimeStepContract + "\n"

const vulnerabilityJobContract = `  govulncheck:
    needs: binding
    if: needs.binding.outputs.security == 'true'
    runs-on: ubuntu-latest
    steps:
` + ciCheckoutStepContract + "\n" + setupGoStepContract + "\n" + `      - name: govulncheck
        run: |
          # Pinned (not "@latest") so a yanked/compromised upstream release cannot
          # silently enter CI; bump deliberately.
          go install golang.org/x/vuln/cmd/govulncheck@v1.4.0
          govulncheck ./...
`

const codeQLTriggerContract = `on:
  schedule:
    - cron: '27 3 * * 1'
  workflow_dispatch:
  workflow_call:
    inputs:
      analysis_category:
        description: Separate the aggregate scan from the legacy standalone scan
        required: true
        type: string
`

const codeQLAnalyzeJobContract = `  analyze:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      security-events: write
    steps:
` + ciCheckoutStepContract + "\n" + setupGoStepContract + `
      - name: Initialize CodeQL
        uses: github/codeql-action/init@e4fba868fa4b1b91e1fdab776edc8cfbe6e9fb81
        with:
          languages: go
          build-mode: manual
` + codeQLProductBuildStepContract + "\n" + codeQLEvaluatorBuildStepContract + `
      - name: Analyze
        uses: github/codeql-action/analyze@e4fba868fa4b1b91e1fdab776edc8cfbe6e9fb81
        with:
          category: ${{ inputs.analysis_category }}
`

const smokeJobContract = `  smoke:
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6.5.0
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Build
        run: make build
      - name: Verify merged build provenance
        run: |
          ATL_NO_UPDATE=1 ./atl version > "$RUNNER_TEMP/atl-version.json"
          grep -F "\"commit\": \"$GITHUB_SHA\"" "$RUNNER_TEMP/atl-version.json"
          grep -F '"build_state": "clean"' "$RUNNER_TEMP/atl-version.json"
      - name: CLI startup smoke
        run: ATL_NO_UPDATE=1 ./atl --help > /dev/null
`

func requireExactJob(contents []byte, name, contract string) error {
	actual, err := workflowJob(contents, name)
	if err != nil {
		return err
	}
	// workflowJob includes the job key. Compare semantic lines so harmless
	// comments do not weaken or invalidate the executable workflow contract.
	if normalizeWorkflowBlock(string(actual)) != normalizeWorkflowBlock(contract) {
		return fmt.Errorf("%s job must retain its exact premerge workflow contract", name)
	}
	return nil
}

func validatePremergeWorkflow(contents []byte) error {
	if err := validateWorkflowHeader(contents, "ci", "ci"); err != nil {
		return err
	}
	if err := validateWorkflowJobSet(contents, "ci", "binding", "contracts", "test", "corpus-devcontainer", "agent-eval", "agent-eval-race", "agent-eval-platform", "agent-eval-extension-windows", "lint", "govulncheck", "codeql", "ci-ready", "smoke"); err != nil {
		return err
	}
	if err := validateWindowsCompileWorkflow(contents); err != nil {
		return err
	}
	for _, required := range []struct{ name, contract string }{
		{"binding", bindingJobContract}, {"ci-ready", readyJobContract}, {"codeql", codeQLCallJobContract},
		{"contracts", contractsJobContract}, {"lint", lintJobContract}, {"govulncheck", vulnerabilityJobContract},
		{"agent-eval", evaluatorJobContract}, {"agent-eval-race", evaluatorRaceJobContract}, {"agent-eval-platform", platformJobContract},
		{"agent-eval-extension-windows", windowsRuntimeJobContract},
		{"smoke", smokeJobContract},
	} {
		if err := requireExactJob(contents, required.name, required.contract); err != nil {
			return err
		}
	}
	testJob, err := workflowJob(contents, "test")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(testJob, "ci test",
		workflowField{"needs", "binding"}, workflowField{"if", "needs.binding.outputs.product == 'true'"},
		workflowField{"strategy", ""}, workflowField{"runs-on", "${{ matrix.os }}"}, workflowField{"steps", ""},
	); err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(testJob, "ci test", ciCheckoutStepContract, setupGoStepContract, buildStepContract, ciProvenanceStepContract, vetStepContract, coreGateStepContract); err != nil {
		return err
	}
	corpus, err := workflowJob(contents, "corpus-devcontainer")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(corpus, "ci corpus",
		workflowField{"needs", "binding"}, workflowField{"if", "needs.binding.outputs.corpus == 'true'"},
		workflowField{"runs-on", "ubuntu-latest"}, workflowField{"steps", ""},
	); err != nil {
		return err
	}
	return requireWorkflowStepPrefix(corpus, "ci corpus", ciCheckoutStepContract, setupGoStepContract)
}

func validateBootstrapCodeQL(contents []byte) error {
	trigger, err := workflowTopLevelBlock(contents, "on")
	if err != nil {
		return err
	}
	if normalizeWorkflowBlock(string(trigger)) != normalizeWorkflowBlock(codeQLTriggerContract) {
		return fmt.Errorf("CodeQL must retain its exact standalone and reusable triggers")
	}
	return requireExactJob(contents, "analyze", codeQLAnalyzeJobContract)
}

func validateCITriggers(contents []byte) error {
	trigger, err := workflowTopLevelBlock(contents, "on")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(trigger)) != strings.TrimSpace(ciTriggerContract) {
		return fmt.Errorf("ci workflow must retain the exact reviewed ready-event trigger contract")
	}
	return nil
}
