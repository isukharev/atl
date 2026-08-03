// Command check-maintainer-contract verifies the repository's exact Go toolchain contract.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	goFeatureID             = "ghcr.io/devcontainers/features/go:1"
	goFeaturePackageVersion = "1.3.4"
	goFeatureDigest         = "sha256:d85e921f91b41340055bb12b325d9d551170ed04b3b832e33530bf42f167c032"
	// verifiedBaseImage pins the OCI digest that
	// mcr.microsoft.com/devcontainers/base:bookworm resolved to on 2026-07-31.
	verifiedBaseImage = "mcr.microsoft.com/devcontainers/base:bookworm@sha256:73d85a96694a2cadca1ba3fcb5721f2312a64f1d571dd86f6c77e10a708931dc"
)

var exactGoVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

const windowsCompileMakeContract = `.PHONY: check-windows-compile
check-windows-compile:
	GOROOT= GOTOOLCHAIN=auto GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
`

const coreCoverageMakeContract = `.PHONY: check-core-race-coverage
check-core-race-coverage:
	@core_packages="$$(go run ./scripts/list-go-packages --class core)" && \
		core_cover="$$(go run ./scripts/list-go-packages --class core --scope internal --format csv)" && \
		test -n "$$core_packages" && test -n "$$core_cover" && \
		go test -race -covermode=atomic -coverprofile=cover.out -coverpkg="$$core_cover" -count=1 -timeout=10m $$core_packages
	@go run ./scripts/check-coverage --profile cover.out --minimum "84.0"
`

const packageBoundaryMakeContract = `.PHONY: check-package-boundary
check-package-boundary:
	@core="$$(go run ./scripts/list-go-packages --class core)" && \
		heavy="$$(go run ./scripts/list-go-packages --class heavy)" && \
		test -n "$$core" && test -n "$$heavy"
`

const pluginsMakeContract = `.PHONY: check-plugins
check-plugins: gen-plugins check-skill-safety check-skill-routing
	@test -z "$$(git status --porcelain -- skills plugins/atl/skills plugins/atl/.mcp.json)" || { \
		git status --porcelain -- skills plugins/atl/skills plugins/atl/.mcp.json; \
		echo "generated plugin trees are stale or hand-edited: edit skills-src/, run 'make gen-plugins', commit all three trees"; exit 1; }
`

const context7MakeContract = `.PHONY: check-context7-docs
check-context7-docs:
	go run ./scripts/check-context7-docs
`

const docsCatalogMakeContract = `.PHONY: check-docs-catalog
check-docs-catalog:
	go run ./scripts/check-docs-catalog -root .
`

const docsFreshnessMakeContract = `.PHONY: check-docs-freshness
check-docs-freshness:
	go run ./scripts/check-docs-freshness -root .
`

const repositorySkillsMakeContract = `.PHONY: check-repository-skills
check-repository-skills:
	go run ./scripts/check-repository-skills -root .
`

const referenceSplitMakeContract = `.PHONY: check-reference-split
check-reference-split:
	go run ./scripts/check-reference-split -root .
`

const onboardingMakeContract = `.PHONY: check-onboarding-docs
check-onboarding-docs: build
	ATL_NO_UPDATE=1 go run ./scripts/check-onboarding-docs -root . -atl ./atl
`

const agentEvalRaceMakeContract = `.PHONY: agent-eval-race
agent-eval-race: agent-eval-compat
	go test -race ./internal/agenteval ./scripts/agent-eval -count=1 -timeout=10m
`

const (
	checkoutStepContract     = `      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1`
	lintCheckoutStepContract = `      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          fetch-depth: 0`
	setupGoStepContract = `      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true`
	buildStepContract = `      - name: Build
        run: make build`
	coreGateStepContract = `      - name: Core race and coverage gate
        run: make check-core-race-coverage`
	ciProvenanceStepContract = `      - name: Verify stamped build provenance
        run: |
          ATL_NO_UPDATE=1 ./atl version > "$RUNNER_TEMP/atl-version.json"
          grep -F "\"commit\": \"$(git rev-parse HEAD)\"" "$RUNNER_TEMP/atl-version.json"
          grep -F '"build_state": "clean"' "$RUNNER_TEMP/atl-version.json"`
	windowsCompileStepContract = `      - name: Windows source cross-compile
        if: matrix.os == 'ubuntu-latest'
        run: make check-windows-compile`
	maintainerStepContract = `      - name: Maintainer toolchain contract
        run: GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract`
	packageBoundaryStepContract = `      - name: Core/heavy package boundary
        run: make check-package-boundary`
	pluginsStepContract = `      - name: Generated plugin trees are current
        run: make check-plugins`
	docsCatalogStepContract = `      - name: Documentation catalog
        run: make check-docs-catalog`
	docsFreshnessStepContract = `      - name: Documentation freshness
        env:
          ATL_DOCS_BASE: ${{ github.event.pull_request.base.sha }}
          ATL_DOCS_HEAD: ${{ github.event.pull_request.head.sha }}
        run: make check-docs-freshness`
	releaseDocsFreshnessStepContract = `      - name: Documentation freshness
        run: make check-docs-freshness`
	repositorySkillsStepContract = `      - name: Repository maintainer skills
        run: make check-repository-skills`
	referenceSplitStepContract = `      - name: Reference split compatibility
        run: make check-reference-split`
	context7StepContract = `      - name: Indexed documentation contract
        run: make check-context7-docs`
	onboardingStepContract = `      - name: Onboarding documentation rehearsal
        run: make check-onboarding-docs`
	vetStepContract = `      - name: Vet
        run: go vet ./...`
	lintStepContract = `      - name: golangci-lint
        uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a
        with:
          version: v2.12.2`
	govulncheckStepContract = `      - name: govulncheck
        run: |
          go install golang.org/x/vuln/cmd/govulncheck@v1.4.0
          govulncheck ./...`
	agentEvalStepContract = `      - name: Agent evaluation race gate
        run: make agent-eval-race`
)

type devcontainerConfig struct {
	Image        string                       `json:"image"`
	RemoteUser   string                       `json:"remoteUser"`
	Features     map[string]map[string]string `json:"features"`
	ContainerEnv map[string]string            `json:"containerEnv"`
}

type featureLock struct {
	Features map[string]lockedFeature `json:"features"`
}

type lockedFeature struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
}

type workflowField struct {
	key   string
	value string
}

const anyWorkflowValue = "\x00"

type report struct {
	Status         string `json:"status"`
	GoVersion      string `json:"go_version"`
	RuntimeVersion string `json:"runtime_version"`
}

func main() {
	if err := run(".", runtime.Version(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-maintainer-contract:", err)
		os.Exit(1)
	}
}

func run(root, runtimeVersion string, output io.Writer) error {
	goVersion, err := validateRepository(root, runtimeVersion)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report{Status: "ok", GoVersion: goVersion, RuntimeVersion: runtimeVersion})
}

func validateRepository(root, runtimeVersion string) (string, error) {
	goVersion, err := readGoVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	if err := validateRuntime(goVersion, runtimeVersion); err != nil {
		return "", err
	}
	if err := validateDevcontainer(root, goVersion); err != nil {
		return "", err
	}
	if err := validateBootstrap(root); err != nil {
		return "", err
	}
	for _, workflow := range []string{"ci.yml", "codeql.yml", "release.yml"} {
		path := filepath.Join(root, ".github", "workflows", workflow)
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		if err := validateSetupGoWorkflow(contents); err != nil {
			return "", fmt.Errorf("%s: %w", workflow, err)
		}
	}
	if err := validateDeliveryContracts(root); err != nil {
		return "", err
	}
	return goVersion, nil
}

func readGoVersion(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var versions []string
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			versions = append(versions, fields[1])
		}
	}
	if len(versions) != 1 || !exactGoVersion.MatchString(versions[0]) {
		return "", fmt.Errorf("go.mod must contain exactly one exact go MAJOR.MINOR.PATCH directive")
	}
	return versions[0], nil
}

func validateRuntime(goVersion, runtimeVersion string) error {
	want := "go" + goVersion
	if runtimeVersion != want {
		return fmt.Errorf("runtime.Version() = %q, want %q from go.mod", runtimeVersion, want)
	}
	return nil
}

func validateDevcontainer(root, goVersion string) error {
	configPath := filepath.Join(root, ".devcontainer", "devcontainer.json")
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	var config devcontainerConfig
	if err := decodeJSONC(contents, &config); err != nil {
		return fmt.Errorf("decode %s: %w", configPath, err)
	}
	if config.Image != verifiedBaseImage {
		return fmt.Errorf("devcontainer image = %q, want verified base image %q", config.Image, verifiedBaseImage)
	}
	if config.RemoteUser != "vscode" {
		return fmt.Errorf("devcontainer remoteUser = %q, want %q", config.RemoteUser, "vscode")
	}
	if config.ContainerEnv["GOTOOLCHAIN"] != "local" {
		return fmt.Errorf("devcontainer GOTOOLCHAIN = %q, want %q", config.ContainerEnv["GOTOOLCHAIN"], "local")
	}
	feature, ok := config.Features[goFeatureID]
	if !ok {
		return fmt.Errorf("devcontainer is missing feature %q", goFeatureID)
	}
	if feature["version"] != goVersion {
		return fmt.Errorf("devcontainer Go feature version = %q, want %q from go.mod", feature["version"], goVersion)
	}

	lockPath := filepath.Join(root, ".devcontainer", "devcontainer-lock.json")
	lockContents, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", lockPath, err)
	}
	var lock featureLock
	if err := json.Unmarshal(lockContents, &lock); err != nil {
		return fmt.Errorf("decode %s: %w", lockPath, err)
	}
	locked, ok := lock.Features[goFeatureID]
	if !ok {
		return fmt.Errorf("devcontainer lock is missing feature %q", goFeatureID)
	}
	if locked.Version != goFeaturePackageVersion {
		return fmt.Errorf("devcontainer Go feature package version = %q, want reviewed %q", locked.Version, goFeaturePackageVersion)
	}
	const resolvedPrefix = "ghcr.io/devcontainers/features/go@"
	if locked.Resolved != resolvedPrefix+goFeatureDigest || locked.Integrity != goFeatureDigest {
		return errors.New("devcontainer Go feature lock does not match the reviewed package digest")
	}
	return nil
}

func validateBootstrap(root string) error {
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	const makeContract = "check-maintainer-contract:\n\tGOTOOLCHAIN=local go run ./scripts/check-maintainer-contract\n"
	if countMakeTargetDeclarations(makefile, "check-maintainer-contract") != 1 ||
		bytes.Count(makefile, []byte(makeContract)) != 1 {
		return errors.New("makefile maintainer check must start with GOTOOLCHAIN=local")
	}
	if countMakeTargetDeclarations(makefile, "check-windows-compile") != 1 ||
		bytes.Count(makefile, []byte(windowsCompileMakeContract)) != 1 {
		return errors.New("makefile must provide the exact Windows source cross-compile target")
	}
	if err := validateMakeExecutionControls(makefile); err != nil {
		return err
	}
	if countMakeTargetDeclarations(makefile, "check-core-race-coverage") != 1 ||
		bytes.Count(makefile, []byte(coreCoverageMakeContract)) != 1 {
		return errors.New("makefile must retain the exact core race/coverage command and reviewed 84.0% floor")
	}
	for _, required := range []struct {
		target, contract, diagnostic string
	}{
		{"check-package-boundary", packageBoundaryMakeContract, "makefile must retain the exact package-boundary gate"},
		{"check-plugins", pluginsMakeContract, "makefile must retain the exact generated-plugin gate"},
		{"check-docs-catalog", docsCatalogMakeContract, "makefile must retain the exact documentation-catalog gate"},
		{"check-docs-freshness", docsFreshnessMakeContract, "makefile must retain the exact documentation-freshness gate"},
		{"check-repository-skills", repositorySkillsMakeContract, "makefile must retain the exact repository-skills gate"},
		{"check-reference-split", referenceSplitMakeContract, "makefile must retain the exact reference-split compatibility gate"},
		{"check-context7-docs", context7MakeContract, "makefile must retain the exact indexed-documentation gate"},
		{"check-onboarding-docs", onboardingMakeContract, "makefile onboarding binary assertion must set ATL_NO_UPDATE=1"},
		{"agent-eval-race", agentEvalRaceMakeContract, "makefile must retain the exact agent-evaluation race gate"},
	} {
		if countMakeTargetDeclarations(makefile, required.target) != 1 ||
			bytes.Count(makefile, []byte(required.contract)) != 1 {
			return errors.New(required.diagnostic)
		}
	}
	postCreate, err := os.ReadFile(filepath.Join(root, ".devcontainer", "post-create.sh"))
	if err != nil {
		return fmt.Errorf("read devcontainer post-create: %w", err)
	}
	if !bytes.Contains(postCreate, []byte("go run ./scripts/check-maintainer-contract")) ||
		bytes.Contains(postCreate, []byte("GOTOOLCHAIN=auto")) {
		return errors.New("devcontainer post-create must run the local maintainer contract")
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return fmt.Errorf("read ci workflow: %w", err)
	}
	if !bytes.Contains(ci, []byte("run: GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract")) {
		return errors.New("ci must run the maintainer contract directly with the local toolchain")
	}
	if err := validateWorkflowHeader(ci, "ci", "ci"); err != nil {
		return err
	}
	if err := validateWindowsCompileWorkflow(ci); err != nil {
		return err
	}
	testJob, err := workflowJob(ci, "test")
	if err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(testJob, "ci test",
		checkoutStepContract, setupGoStepContract, buildStepContract,
		ciProvenanceStepContract, vetStepContract, coreGateStepContract,
	); err != nil {
		return err
	}
	if err := requireWorkflowStep(testJob, "Core race and coverage gate", coreGateStepContract); err != nil {
		return fmt.Errorf("ci: %w", err)
	}
	if err := requireWorkflowStep(testJob, "Verify stamped build provenance", ciProvenanceStepContract); err != nil {
		return fmt.Errorf("ci: %w", err)
	}
	lintJob, err := workflowJob(ci, "lint")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(lintJob, "ci lint",
		workflowField{"if", "github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'"},
		workflowField{"runs-on", "ubuntu-latest"},
		workflowField{"steps", ""},
	); err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(lintJob, "ci lint",
		lintCheckoutStepContract, setupGoStepContract, maintainerStepContract,
		packageBoundaryStepContract, pluginsStepContract, docsCatalogStepContract, docsFreshnessStepContract, repositorySkillsStepContract, referenceSplitStepContract, context7StepContract,
		onboardingStepContract, lintStepContract,
	); err != nil {
		return err
	}
	for _, required := range []struct {
		name, contract string
	}{
		{"Maintainer toolchain contract", maintainerStepContract},
		{"Core/heavy package boundary", packageBoundaryStepContract},
		{"Generated plugin trees are current", pluginsStepContract},
		{"Documentation catalog", docsCatalogStepContract},
		{"Documentation freshness", docsFreshnessStepContract},
		{"Repository maintainer skills", repositorySkillsStepContract},
		{"Reference split compatibility", referenceSplitStepContract},
		{"Indexed documentation contract", context7StepContract},
		{"Onboarding documentation rehearsal", onboardingStepContract},
		{"golangci-lint", lintStepContract},
	} {
		if err := requireWorkflowStep(lintJob, required.name, required.contract); err != nil {
			return fmt.Errorf("ci: %w", err)
		}
	}
	return nil
}

func countMakeTargetDeclarations(makefile []byte, target string) int {
	count := 0
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.HasPrefix(line, "\t") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		separator := strings.IndexByte(trimmed, ':')
		if separator < 0 {
			continue
		}
		for _, declared := range strings.Fields(trimmed[:separator]) {
			declared = strings.TrimSuffix(declared, "&")
			if declared == target {
				count++
			}
		}
	}
	return count
}

func validateMakeExecutionControls(makefile []byte) error {
	for lineNumber, line := range strings.Split(string(makefile), "\n") {
		if strings.HasPrefix(line, "\t") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			return fmt.Errorf("makefile line %d must not continue hidden top-level build controls", lineNumber+1)
		}
		lower := strings.ToLower(trimmed)
		fields := strings.Fields(lower)
		if len(fields) > 0 {
			switch fields[0] {
			case "ifeq", "ifneq", "ifdef", "ifndef", "else", "endif":
				return fmt.Errorf("makefile line %d must not conditionally disable reviewed build controls", lineNumber+1)
			}
		}
		includeDirective := len(fields) > 0 && (fields[0] == "include" || fields[0] == "-include" || fields[0] == "sinclude")
		if includeDirective || strings.Contains(trimmed, "$(eval") ||
			strings.Contains(trimmed, "${eval") {
			return fmt.Errorf("makefile line %d must not import or evaluate hidden build rules", lineNumber+1)
		}
		for _, field := range fields {
			if field == "define" || field == "endef" {
				return fmt.Errorf("makefile line %d must not define hidden multiline build controls", lineNumber+1)
			}
		}
		if strings.HasPrefix(trimmed, "$") {
			return fmt.Errorf("makefile line %d must not expand hidden top-level directives", lineNumber+1)
		}
		if variable := dangerousMakeAssignment(trimmed); variable != "" {
			return fmt.Errorf("makefile line %d must not override %s", lineNumber+1, variable)
		}
		if separator := strings.IndexByte(trimmed, ':'); separator >= 0 {
			declaration := strings.TrimSpace(trimmed[:separator])
			if strings.Contains(declaration, "$") {
				return fmt.Errorf("makefile line %d must not declare dynamically expanded targets", lineNumber+1)
			}
			for _, target := range strings.Fields(declaration) {
				target = strings.TrimSuffix(target, "&")
				if target == ".IGNORE" || target == ".ONESHELL" {
					return fmt.Errorf("makefile line %d must not weaken recipe failure propagation", lineNumber+1)
				}
			}
			if variable := dangerousMakeAssignment(strings.TrimSpace(trimmed[separator+1:])); variable != "" {
				return fmt.Errorf("makefile line %d must not set target-specific %s", lineNumber+1, variable)
			}
		}
	}
	return nil
}

func dangerousMakeAssignment(value string) string {
	for {
		previous := value
		for _, modifier := range []string{"override", "export", "private", "unexport"} {
			if strings.HasPrefix(value, modifier) && len(value) > len(modifier) &&
				(value[len(modifier)] == ' ' || value[len(modifier)] == '\t') {
				value = strings.TrimLeft(value[len(modifier):], " \t")
			}
		}
		if value == previous {
			break
		}
	}
	if assignment := strings.IndexByte(value, '='); assignment >= 0 {
		name := value[:assignment]
		if strings.Contains(name, "$") {
			return "computed variable name"
		}
	}
	for _, variable := range []string{"MAKEFLAGS", "GNUMAKEFLAGS", "MAKEFILES", "SHELL", ".SHELLFLAGS", ".RECIPEPREFIX"} {
		if !strings.HasPrefix(value, variable) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(value, variable))
		if rest == "" || strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":=") ||
			strings.HasPrefix(rest, "::=") || strings.HasPrefix(rest, ":::=") ||
			strings.HasPrefix(rest, "+=") || strings.HasPrefix(rest, "?=") ||
			strings.HasPrefix(rest, "!=") {
			return variable
		}
	}
	return ""
}

func validateDeliveryContracts(root string) error {
	releasePath := filepath.Join(root, ".github", "workflows", "release.yml")
	release, err := os.ReadFile(releasePath)
	if err != nil {
		return fmt.Errorf("read release workflow: %w", err)
	}
	if err := validateWorkflowHeader(release, "release", "release"); err != nil {
		return err
	}
	trigger, err := workflowTopLevelBlock(release, "on")
	if err != nil {
		return err
	}
	const releaseTrigger = `on:
  push:
    tags: ['v*']
`
	if strings.TrimSpace(string(trigger)) != strings.TrimSpace(releaseTrigger) {
		return errors.New("release workflow must retain the exact v* tag trigger")
	}
	if err := validateWorkflowJobSet(release, "release", "test", "quality", "agent-eval", "release", "refresh-context7"); err != nil {
		return err
	}

	testJob, err := workflowJob(release, "test")
	if err != nil {
		return err
	}
	if err := validateReleaseMatrix(testJob); err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(testJob, "release test",
		checkoutStepContract, setupGoStepContract, coreGateStepContract,
	); err != nil {
		return err
	}
	if err := requireWorkflowStep(testJob, "Core race and coverage gate", coreGateStepContract); err != nil {
		return fmt.Errorf("release: %w", err)
	}

	qualityJob, err := workflowJob(release, "quality")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(qualityJob, "release quality",
		workflowField{"runs-on", "ubuntu-latest"},
		workflowField{"steps", ""},
	); err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(qualityJob, "release quality",
		checkoutStepContract, setupGoStepContract, maintainerStepContract,
		packageBoundaryStepContract, pluginsStepContract, docsCatalogStepContract, releaseDocsFreshnessStepContract, repositorySkillsStepContract, referenceSplitStepContract, context7StepContract,
		onboardingStepContract, vetStepContract, lintStepContract, govulncheckStepContract,
	); err != nil {
		return err
	}
	qualitySteps := []struct {
		name, contract string
	}{
		{"Maintainer toolchain contract", maintainerStepContract},
		{"Core/heavy package boundary", packageBoundaryStepContract},
		{"Generated plugin trees are current", pluginsStepContract},
		{"Documentation catalog", docsCatalogStepContract},
		{"Documentation freshness", releaseDocsFreshnessStepContract},
		{"Repository maintainer skills", repositorySkillsStepContract},
		{"Reference split compatibility", referenceSplitStepContract},
		{"Indexed documentation contract", context7StepContract},
		{"Onboarding documentation rehearsal", onboardingStepContract},
		{"Vet", vetStepContract},
		{"golangci-lint", lintStepContract},
		{"govulncheck", govulncheckStepContract},
	}
	for _, required := range qualitySteps {
		if err := requireWorkflowStep(qualityJob, required.name, required.contract); err != nil {
			return fmt.Errorf("release: %w", err)
		}
	}

	agentEvalJob, err := workflowJob(release, "agent-eval")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(agentEvalJob, "release agent-eval",
		workflowField{"runs-on", "ubuntu-latest"},
		workflowField{"steps", ""},
	); err != nil {
		return err
	}
	if err := requireWorkflowStepPrefix(agentEvalJob, "release agent-eval",
		checkoutStepContract, setupGoStepContract, agentEvalStepContract,
	); err != nil {
		return err
	}
	if err := requireWorkflowStep(agentEvalJob, "Agent evaluation race gate", agentEvalStepContract); err != nil {
		return fmt.Errorf("release: %w", err)
	}

	publication, err := workflowJob(release, "release")
	if err != nil {
		return err
	}
	if err := validateRequiredJob(publication, "release publication",
		workflowField{"needs", anyWorkflowValue},
		workflowField{"runs-on", "ubuntu-latest"},
		workflowField{"environment", "release"},
		workflowField{"permissions", ""},
		workflowField{"env", ""},
		workflowField{"steps", ""},
	); err != nil {
		return err
	}
	if err := requireInlineNeeds(publication, "test", "quality", "agent-eval"); err != nil {
		return err
	}

	followup, err := workflowJob(release, "refresh-context7")
	if err != nil {
		return err
	}
	if err := validateJobFields(followup, "release documentation follow-up", true,
		workflowField{"name", "Refresh Context7 stable docs (non-blocking)"},
		workflowField{"needs", anyWorkflowValue},
		workflowField{"runs-on", "ubuntu-latest"},
		workflowField{"continue-on-error", "true"},
		workflowField{"environment", "context7"},
		workflowField{"permissions", ""},
		workflowField{"concurrency", ""},
		workflowField{"env", ""},
		workflowField{"steps", ""},
	); err != nil {
		return err
	}
	return requireInlineNeeds(followup, "release")
}

func validateReleaseMatrix(job []byte) error {
	lines := strings.Split(string(job), "\n")
	if err := validateRequiredJob(job, "release test matrix",
		workflowField{"strategy", ""},
		workflowField{"runs-on", "${{ matrix.os }}"},
		workflowField{"steps", ""},
	); err != nil {
		return fmt.Errorf("release test job must retain the exact Ubuntu/macOS matrix runners: %w", err)
	}
	matrixLine, matrixCount, runnerCount := -1, 0, 0
	for index, line := range lines {
		if indentation(line) == 6 && strings.TrimSpace(line) == "matrix:" {
			matrixLine = index
			matrixCount++
		}
		if indentation(line) == 4 && strings.TrimSpace(line) == "runs-on: ${{ matrix.os }}" {
			runnerCount++
		}
	}
	if matrixCount != 1 || runnerCount != 1 {
		return errors.New("release test job must retain the exact Ubuntu/macOS matrix runners")
	}
	hasOS := false
	for _, line := range lines[matrixLine+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indentation(line) <= 6 {
			break
		}
		if indentation(line) != 8 || trimmed != "os: [ubuntu-latest, macos-latest]" || hasOS {
			return errors.New("release test job must retain the exact Ubuntu/macOS matrix runners")
		}
		hasOS = true
	}
	if !hasOS {
		return errors.New("release test job must retain the exact Ubuntu/macOS matrix runners")
	}
	return nil
}

func validateRequiredJob(job []byte, description string, expected ...workflowField) error {
	return validateJobFields(job, description, false, expected...)
}

func validateJobFields(job []byte, description string, allowFailure bool, expected ...workflowField) error {
	wanted := make(map[string]string, len(expected))
	for _, field := range expected {
		if _, duplicate := wanted[field.key]; duplicate {
			return fmt.Errorf("%s contract repeats job-level key %q", description, field.key)
		}
		wanted[field.key] = field.value
	}
	seen := make(map[string]bool, len(expected))
	for _, line := range strings.Split(string(job), "\n") {
		if indentation(line) != 4 {
			continue
		}
		trimmed := strings.TrimSpace(stripYAMLComment(line))
		if trimmed == "" {
			continue
		}
		key, value, ok := simpleYAMLKeyValue(line)
		if !ok {
			return fmt.Errorf("%s job has an unrecognized job-level field", description)
		}
		if key == "continue-on-error" && !allowFailure {
			return fmt.Errorf("%s job must be unconditional and failure-blocking", description)
		}
		want, allowed := wanted[key]
		if key == "if" && !allowed {
			return fmt.Errorf("%s job must be unconditional and failure-blocking", description)
		}
		if !allowed {
			return fmt.Errorf("%s job has unexpected job-level key %q", description, key)
		}
		if seen[key] {
			return fmt.Errorf("%s job repeats job-level key %q", description, key)
		}
		if want != anyWorkflowValue && value != want {
			return fmt.Errorf("%s job must retain %s: %s", description, key, want)
		}
		seen[key] = true
	}
	for _, field := range expected {
		if !seen[field.key] {
			return fmt.Errorf("%s job is missing required job-level key %q", description, field.key)
		}
	}
	return nil
}

func validateWorkflowJobSet(contents []byte, description string, expected ...string) error {
	jobs, err := workflowTopLevelBlock(contents, "jobs")
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(expected))
	for _, name := range expected {
		wanted[name] = true
	}
	seen := make(map[string]bool, len(expected))
	for _, line := range strings.Split(string(jobs), "\n") {
		if indentation(line) != 2 || strings.TrimSpace(stripYAMLComment(line)) == "" {
			continue
		}
		name, _, ok := simpleYAMLKeyValue(line)
		if !ok {
			return fmt.Errorf("%s workflow has an unrecognized job identifier", description)
		}
		if !wanted[name] {
			return fmt.Errorf("%s workflow has unexpected job %q", description, name)
		}
		if seen[name] {
			return fmt.Errorf("%s workflow defines %q job more than once", description, name)
		}
		seen[name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			return fmt.Errorf("%s workflow is missing required job %q", description, name)
		}
	}
	return nil
}

func validateWorkflowHeader(contents []byte, description, name string) error {
	expected := []workflowField{
		{"name", name},
		{"on", ""},
		{"permissions", ""},
		{"concurrency", ""},
		{"jobs", ""},
	}
	wanted := make(map[string]string, len(expected))
	for _, field := range expected {
		wanted[field.key] = field.value
	}
	seen := make(map[string]bool, len(expected))
	for _, line := range strings.Split(string(contents), "\n") {
		if indentation(line) != 0 {
			continue
		}
		trimmed := strings.TrimSpace(stripYAMLComment(line))
		if trimmed == "" {
			continue
		}
		key, value, ok := simpleYAMLKeyValue(line)
		if !ok {
			return fmt.Errorf("%s workflow has an unrecognized top-level field", description)
		}
		want, allowed := wanted[key]
		if !allowed {
			return fmt.Errorf("%s workflow has unexpected top-level key %q", description, key)
		}
		if seen[key] || value != want {
			return fmt.Errorf("%s workflow must retain exactly one %s: %s field", description, key, want)
		}
		seen[key] = true
	}
	for _, field := range expected {
		if !seen[field.key] {
			return fmt.Errorf("%s workflow is missing top-level key %q", description, field.key)
		}
	}
	return nil
}

func requireWorkflowStep(job []byte, name, contract string) error {
	lines := strings.Split(string(job), "\n")
	start, count := -1, 0
	for index, line := range lines {
		if indentation(line) == 6 && strings.TrimSpace(stripYAMLComment(line)) == "- name: "+name {
			start = index
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("required %q step must appear exactly once", name)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if indentation(lines[index]) == 6 && (trimmed == "-" || strings.HasPrefix(trimmed, "- ")) {
			end = index
			break
		}
	}
	actual := normalizeWorkflowBlock(strings.Join(lines[start:end], "\n"))
	want := normalizeWorkflowBlock(contract)
	if actual != want {
		return fmt.Errorf("required %q step must retain its exact workflow block", name)
	}
	return nil
}

func requireWorkflowStepPrefix(job []byte, description string, contracts ...string) error {
	steps, err := workflowStepBlocks(job)
	if err != nil {
		return err
	}
	if len(steps) < len(contracts) {
		return fmt.Errorf("%s job must retain its required step prefix", description)
	}
	for index, contract := range contracts {
		if normalizeWorkflowBlock(steps[index]) != normalizeWorkflowBlock(contract) {
			return fmt.Errorf("%s job step %d must retain its exact required workflow block", description, index+1)
		}
	}
	return nil
}

func workflowStepBlocks(job []byte) ([]string, error) {
	lines := strings.Split(string(job), "\n")
	stepsLine := -1
	for index, line := range lines {
		if indentation(line) == 4 && strings.TrimSpace(stripYAMLComment(line)) == "steps:" {
			if stepsLine >= 0 {
				return nil, errors.New("required workflow job defines steps more than once")
			}
			stepsLine = index
		}
	}
	if stepsLine < 0 {
		return nil, errors.New("required workflow job has no steps")
	}
	var starts []int
	end := len(lines)
	for index := stepsLine + 1; index < len(lines); index++ {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed != "" && indentation(lines[index]) <= 4 {
			end = index
			break
		}
		if indentation(lines[index]) == 6 && (trimmed == "-" || strings.HasPrefix(trimmed, "- ")) {
			starts = append(starts, index)
		}
	}
	if len(starts) == 0 {
		return nil, errors.New("required workflow job has no step entries")
	}
	blocks := make([]string, 0, len(starts))
	for index, start := range starts {
		blockEnd := end
		if index+1 < len(starts) {
			blockEnd = starts[index+1]
		}
		blocks = append(blocks, strings.Join(lines[start:blockEnd], "\n"))
	}
	return blocks, nil
}

func normalizeWorkflowBlock(block string) string {
	var normalized []string
	for _, line := range strings.Split(block, "\n") {
		semantic := strings.TrimSpace(stripYAMLComment(line))
		if semantic == "" || strings.HasPrefix(semantic, "#") {
			continue
		}
		normalized = append(normalized, fmt.Sprintf("%d:%s", indentation(line), semantic))
	}
	return strings.Join(normalized, "\n")
}

func stripYAMLComment(line string) string {
	singleQuoted, doubleQuoted, escaped := false, false, false
	for index, current := range line {
		if escaped {
			escaped = false
			continue
		}
		if doubleQuoted && current == '\\' {
			escaped = true
			continue
		}
		switch current {
		case '\'':
			if !doubleQuoted {
				singleQuoted = !singleQuoted
			}
		case '"':
			if !singleQuoted {
				doubleQuoted = !doubleQuoted
			}
		case '#':
			if !singleQuoted && !doubleQuoted && (index == 0 || line[index-1] == ' ' || line[index-1] == '\t') {
				return strings.TrimSpace(line[:index])
			}
		}
	}
	return line
}

func simpleYAMLKeyValue(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(stripYAMLComment(line))
	separator := strings.IndexByte(trimmed, ':')
	if separator <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:separator])
	value = strings.TrimSpace(trimmed[separator+1:])
	if len(key) >= 2 && key[0] == key[len(key)-1] {
		switch key[0] {
		case '"':
			decoded, err := strconv.Unquote(key)
			if err != nil {
				return "", "", false
			}
			key = decoded
		case '\'':
			key = strings.ReplaceAll(key[1:len(key)-1], "''", "'")
		}
	}
	if key == "" || strings.ContainsAny(key, "'\"") {
		return "", "", false
	}
	return key, value, true
}

func requireInlineNeeds(job []byte, required ...string) error {
	var needs []string
	for _, line := range strings.Split(string(job), "\n") {
		if indentation(line) != 4 {
			continue
		}
		key, value, ok := simpleYAMLKeyValue(line)
		if !ok || key != "needs" {
			continue
		}
		if needs != nil {
			return errors.New("release publication job must define one required needs list")
		}
		if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
			return errors.New("release publication job must use an explicit inline needs list")
		}
		for _, item := range strings.Split(value[1:len(value)-1], ",") {
			needs = append(needs, strings.TrimSpace(item))
		}
	}
	if needs == nil {
		return errors.New("release publication job is missing required prerequisites")
	}
	for _, want := range required {
		found := false
		for _, got := range needs {
			found = found || got == want
		}
		if !found {
			return fmt.Errorf("release publication job must need %q", want)
		}
	}
	return nil
}

func validateWindowsCompileWorkflow(contents []byte) error {
	const requiredTriggers = `on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_dispatch:
`
	triggerBlock, err := workflowTopLevelBlock(contents, "on")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(triggerBlock)) != strings.TrimSpace(requiredTriggers) {
		return errors.New("ci workflow must retain the exact pull-request trigger contract")
	}
	testJob, err := workflowJob(contents, "test")
	if err != nil {
		return err
	}
	lines := strings.Split(string(testJob), "\n")
	requiredIfCount := 0
	requiredRunnerCount := 0
	matrixLine := -1
	for index, line := range lines {
		indent := indentation(line)
		trimmed := strings.TrimSpace(line)
		if indent == 6 && trimmed == "matrix:" {
			if matrixLine >= 0 {
				return errors.New("ci test job must define exactly one matrix")
			}
			matrixLine = index
		}
		if indent != 4 {
			continue
		}
		key, value, ok := simpleYAMLKeyValue(line)
		if !ok {
			continue
		}
		if key == "if" {
			if value != "github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'" {
				return errors.New("ci test job must retain the exact required event condition")
			}
			requiredIfCount++
		}
		if key == "runs-on" {
			if value != "${{ matrix.os }}" {
				return errors.New("ci test job must retain the exact matrix runner")
			}
			requiredRunnerCount++
		}
		if key == "continue-on-error" {
			return errors.New("ci test job must not allow job-level failure")
		}
		if key == "needs" {
			return errors.New("ci test job must not depend on a potentially skipped job")
		}
		if key != "if" && key != "strategy" && key != "runs-on" && key != "steps" {
			return fmt.Errorf("ci test job has unexpected job-level key %q", key)
		}
	}
	if requiredIfCount != 1 || requiredRunnerCount != 1 || matrixLine < 0 {
		return errors.New("ci test job must retain the exact required Ubuntu/macOS matrix")
	}
	hasRequiredOS := false
	for _, line := range lines[matrixLine+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indentation(line) <= 6 {
			break
		}
		if indentation(line) != 8 || trimmed != "os: [ubuntu-latest, macos-latest]" || hasRequiredOS {
			return errors.New("ci test job must retain the exact required Ubuntu/macOS matrix")
		}
		hasRequiredOS = true
	}
	if !hasRequiredOS {
		return errors.New("ci test job must retain the exact required Ubuntu/macOS matrix")
	}
	if err := requireWorkflowStep(testJob, "Windows source cross-compile", windowsCompileStepContract); err != nil {
		return fmt.Errorf("ci Ubuntu test job must run the exact Windows source cross-compile workflow block: %w", err)
	}
	return nil
}

func workflowTopLevelBlock(contents []byte, name string) ([]byte, error) {
	lines := strings.SplitAfter(string(contents), "\n")
	start := -1
	for index, line := range lines {
		candidate := strings.TrimSuffix(line, "\n")
		key, _, ok := simpleYAMLKeyValue(candidate)
		if indentation(candidate) != 0 || !ok || key != name {
			continue
		}
		if start >= 0 {
			return nil, fmt.Errorf("ci workflow defines %q more than once", name)
		}
		start = index
	}
	if start < 0 {
		return nil, fmt.Errorf("ci workflow has no %q block", name)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\n")
		if strings.TrimSpace(line) != "" && indentation(line) == 0 {
			end = index
			break
		}
	}
	return []byte(strings.Join(lines[start:end], "")), nil
}

func workflowJob(contents []byte, name string) ([]byte, error) {
	lines := strings.SplitAfter(string(contents), "\n")
	start := -1
	for index, line := range lines {
		candidate := strings.TrimSuffix(line, "\n")
		key, _, ok := simpleYAMLKeyValue(candidate)
		if indentation(candidate) != 2 || !ok || key != name {
			continue
		}
		if start >= 0 {
			return nil, fmt.Errorf("workflow defines %q job more than once", name)
		}
		start = index
	}
	if start < 0 {
		return nil, fmt.Errorf("ci workflow has no %q job", name)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\n")
		if strings.TrimSpace(line) != "" && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			end = index
			break
		}
	}
	return []byte(strings.Join(lines[start:end], "")), nil
}

func validateSetupGoWorkflow(contents []byte) error {
	lines := strings.Split(string(contents), "\n")
	setupCount := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "- uses: actions/setup-go@") {
			continue
		}
		setupCount++
		stepIndent := indentation(line)
		withIndent := -1
		hasVersionFile := false
		for next := index + 1; next < len(lines); next++ {
			nextLine := lines[next]
			nextTrimmed := strings.TrimSpace(nextLine)
			nextIndent := indentation(nextLine)
			if nextTrimmed != "" && nextIndent <= stepIndent {
				break
			}
			if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "#") {
				continue
			}
			if strings.HasPrefix(nextTrimmed, "go-version:") {
				return errors.New("actions/setup-go must not use a literal go-version")
			}
			if withIndent >= 0 && nextIndent <= withIndent {
				withIndent = -1
			}
			if nextTrimmed == "with:" {
				withIndent = nextIndent
				continue
			}
			if withIndent >= 0 && nextIndent == withIndent+2 && nextTrimmed == "go-version-file: go.mod" {
				hasVersionFile = true
			}
		}
		if !hasVersionFile {
			return errors.New("actions/setup-go must use go-version-file: go.mod")
		}
	}
	if setupCount == 0 {
		return errors.New("workflow has no actions/setup-go step")
	}
	return nil
}

func indentation(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func decodeJSONC(contents []byte, destination any) error {
	stripped, err := stripJSONComments(contents)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(stripped))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing content")
	}
	return nil
}

func stripJSONComments(contents []byte) ([]byte, error) {
	result := make([]byte, 0, len(contents))
	inString := false
	escaped := false
	for index := 0; index < len(contents); index++ {
		current := contents[index]
		if inString {
			result = append(result, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			result = append(result, current)
			continue
		}
		if current != '/' || index+1 >= len(contents) {
			result = append(result, current)
			continue
		}
		switch contents[index+1] {
		case '/':
			index += 2
			for index < len(contents) && contents[index] != '\n' {
				index++
			}
			if index < len(contents) {
				result = append(result, '\n')
			}
		case '*':
			index += 2
			for index+1 < len(contents) && (contents[index] != '*' || contents[index+1] != '/') {
				if contents[index] == '\n' {
					result = append(result, '\n')
				}
				index++
			}
			if index+1 >= len(contents) {
				return nil, errors.New("unterminated JSON block comment")
			}
			index++
		default:
			result = append(result, current)
		}
	}
	if inString {
		return nil, errors.New("unterminated JSON string")
	}
	return result, nil
}
