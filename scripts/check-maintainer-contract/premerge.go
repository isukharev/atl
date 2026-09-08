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
  workflow_dispatch:
    inputs:
      pr:
        description: Open pull request number (same repository, targeting main)
        required: true
        type: string
      head_sha:
        description: Exact reviewed PR head SHA; dispatch its branch
        required: true
        type: string
      base_sha:
        description: Exact current main SHA, already contained in the PR head
        required: true
        type: string
`

const bindingJobContract = `  binding:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + `
      - name: Bind current pull request revision
        env:
          GH_TOKEN: ${{ github.token }}
        run: env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go run ./scripts/check-premerge bind
`

const readyJobContract = `  ci-ready:
    if: always() && (github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch')
    needs: [binding, test, corpus-devcontainer, agent-eval, agent-eval-extension-windows, lint, govulncheck, codeql]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: read
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + `
      - name: Require complete checks and current pull request revision
        env:
          GH_TOKEN: ${{ github.token }}
          ATL_PREMERGE_NEEDS: ${{ toJSON(needs) }}
        run: env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go run ./scripts/check-premerge ready
`

const codeQLCallJobContract = `  codeql:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    permissions:
      contents: read
      security-events: write
    uses: ./.github/workflows/codeql.yml
    with:
      analysis_category: ci-ready
`

const codeQLTriggerContract = `on:
  pull_request:
    branches: [main]
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

func validateBootstrapCI(contents []byte) error {
	for _, required := range []struct{ name, contract string }{
		{"binding", bindingJobContract}, {"ci-ready", readyJobContract}, {"codeql", codeQLCallJobContract},
	} {
		if err := requireExactJob(contents, required.name, required.contract); err != nil {
			return err
		}
	}
	for _, name := range []string{"corpus-devcontainer", "govulncheck"} {
		job, err := workflowJob(contents, name)
		if err != nil {
			return err
		}
		if err := requireWorkflowStepPrefix(job, name, ciCheckoutStepContract, setupGoStepContract); err != nil {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("ci workflow must retain the exact pull-request and bound manual trigger contract")
	}
	return nil
}
