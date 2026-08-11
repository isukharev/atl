package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/isukharev/atl/internal/agenteval"
)

const (
	standaloneContractVersion = agenteval.StandaloneContractVersion
	standaloneResultSchema    = "agent-eval/command-result"
	standaloneErrorSchema     = "agent-eval/command-error"
)

// These values are intentionally useful without linker injection. Release
// packaging may replace them with content-free build identity.
var (
	standaloneBuildVersion = "devel"
	standaloneBuildCommit  = "unknown"
	standaloneBuildDate    = "unknown"
)

type standaloneExitStatus struct {
	code int
}

func (status standaloneExitStatus) Error() string {
	return "standalone command exited"
}

type standaloneExitClass struct {
	code     int
	id       string
	message  string
	recovery string
}

var (
	standaloneUsageError          = standaloneExitClass{2, "usage_error", "command usage rejected", "fix_usage"}
	standaloneConfigurationError  = standaloneExitClass{3, "configuration_error", "configuration rejected", "complete_configuration"}
	standaloneInputError          = standaloneExitClass{4, "input_error", "input rejected", "fix_input"}
	standaloneCompatibilityError  = standaloneExitClass{5, "compatibility_error", "operation unavailable", "select_compatible_component"}
	standaloneInternalError       = standaloneExitClass{1, "internal_error", "internal failure", "report_bug"}
	standaloneOutcomeUnknownError = standaloneExitClass{10, "outcome_unknown", "outcome unknown", "reconcile_outcome"}
	standaloneInterruptedError    = standaloneExitClass{11, "interrupted", "operation interrupted", "resume"}
)

var standaloneExitClassRegistry = [...]standaloneExitClass{
	{0, "success", "", ""},
	standaloneInternalError,
	standaloneUsageError,
	standaloneConfigurationError,
	standaloneInputError,
	standaloneCompatibilityError,
	{6, "policy_denied", "policy denied", "request_authority"},
	{7, "authentication_failed", "authentication failed", "reauthenticate"},
	{8, "execution_failed", "execution failed", "inspect_execution"},
	{9, "check_failed", "check failed", "review_failed_check"},
	standaloneOutcomeUnknownError,
	standaloneInterruptedError,
}

type standaloneFailure struct {
	class     standaloneExitClass
	kind      string
	retrySafe bool
}

func standaloneFail(class standaloneExitClass, kind string) *standaloneFailure {
	return &standaloneFailure{class: class, kind: kind}
}

type standaloneRecovery struct {
	SchemaVersion int    `json:"schema_version"`
	Action        string `json:"action"`
}

type standaloneErrorEnvelope struct {
	Schema          string             `json:"schema"`
	SchemaVersion   int                `json:"schema_version"`
	ContractVersion string             `json:"contract_version"`
	Error           string             `json:"error"`
	ExitClass       string             `json:"exit_class"`
	Kind            string             `json:"kind"`
	RetrySafe       bool               `json:"retry_safe"`
	Recovery        standaloneRecovery `json:"recovery"`
}

type standaloneResultEnvelope struct {
	Schema          string `json:"schema"`
	SchemaVersion   int    `json:"schema_version"`
	ContractVersion string `json:"contract_version"`
	Command         string `json:"command"`
	Status          string `json:"status"`
	Result          any    `json:"result"`
}

type standaloneOutcome struct {
	command    string
	status     string
	result     any
	outputMode string
	text       string
}

type standaloneOptionDescriptor struct {
	Name        string
	Value       string
	Description string
}

type standaloneCommandDescriptor struct {
	Name       string
	Summary    string
	Usage      string
	Modes      []string
	Examples   []string
	Options    []standaloneOptionDescriptor
	Available  bool
	ProcessAPI bool
	Children   []standaloneCommandDescriptor
}

// standaloneCommandTree returns a fresh value tree. Callers cannot mutate the
// canonical command registry, while help, completion, routing, and
// capabilities all consume the same descriptors.
func standaloneCommandTree() standaloneCommandDescriptor {
	common := []standaloneOptionDescriptor{
		{Name: "--config", Value: "FILE", Description: "read exactly one project config (maximum 64 KiB)"},
		{Name: "--project", Value: "DIR", Description: "read DIR/.agent-eval/config.json without parent walking"},
		{Name: "--environment", Value: "none|portable-v1", Description: "enable a closed environment projection"},
		{Name: "--profile", Value: "ID", Description: "override the generic evaluation profile identity"},
		{Name: "--model", Value: "ID", Description: "override the generic model identity"},
		{Name: "--repetitions", Value: "N", Description: "override the bounded repetition count"},
		{Name: "--dry-run", Description: "resolve and validate without executing the operation"},
		{Name: "--explain", Description: "emit authority and privacy-safe provenance"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	agentSkillsImportOptions := []standaloneOptionDescriptor{
		{Name: "--format", Value: "agent-skills", Description: "select the Agent Skills interchange"},
		{Name: "--variant", Value: "auto|agentskills-guide-v1|anthropic-skill-creator-v1", Description: "select or safely detect the documented source variant"},
		{Name: "--skill-root", Value: "DIR", Description: "read one exact bounded skill tree"},
		{Name: "--eval-root", Value: "DIR", Description: "optionally select the exact evaluation source root"},
		{Name: "--baseline", Value: "no-skill|previous-skill", Description: "select the explicit comparison baseline"},
		{Name: "--previous-skill-root", Value: "DIR", Description: "required only for the previous-skill baseline"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	agentSkillsExportOptions := append([]standaloneOptionDescriptor(nil), agentSkillsImportOptions...)
	for index := range agentSkillsExportOptions {
		if agentSkillsExportOptions[index].Name == "--variant" {
			agentSkillsExportOptions[index].Value = "agentskills-guide-v1|anthropic-skill-creator-v1"
			agentSkillsExportOptions[index].Description = "select the exact documented workspace variant"
		}
	}
	agentSkillsExportOptions = append(agentSkillsExportOptions,
		standaloneOptionDescriptor{Name: "--workspace-root", Value: "DIR", Description: "read one exact bounded iteration workspace"},
		standaloneOptionDescriptor{Name: "--destination", Value: "ABSOLUTE_DIR", Description: "write one exact clean and previously nonexistent destination"},
		standaloneOptionDescriptor{Name: "--case-directory", Value: "ID=iteration-N/eval-slug", Description: "bind each Guide case to one exact workspace directory; repeat as needed"},
	)
	return standaloneCommandDescriptor{
		Name:    "agent-eval",
		Summary: "validate, execute, and compare bounded agent evaluations",
		Usage:   "agent-eval <command> [options]",
		Examples: []string{
			"agent-eval capabilities",
			"agent-eval validate --kind scenario --input scenario.json",
			"agent-eval inspect --kind configuration --project . --explain",
		},
		Available: true,
		Children: []standaloneCommandDescriptor{
			{Name: "capabilities", Summary: "report supported standalone operations without reading configuration", Usage: "agent-eval capabilities [--output json|text]", Available: true, ProcessAPI: true},
			{Name: "version", Summary: "report build, schema, and protocol identity without reading configuration", Usage: "agent-eval version [--output json|text]", Available: true, ProcessAPI: true},
			{Name: "init", Summary: "initialize a standalone evaluation project", Usage: "agent-eval init [options]", Options: common},
			{Name: "import", Summary: "inspect a selected external evaluation representation without writing", Usage: "agent-eval import <command>", Children: []standaloneCommandDescriptor{
				{Name: "agent-skills", Summary: "inspect a bounded Agent Skills source without execution or writes", Usage: "agent-eval import agent-skills --format agent-skills --variant VARIANT --skill-root DIR --baseline BASELINE [options]", Modes: []string{"auto", "agentskills-guide-v1", "anthropic-skill-creator-v1"}, Examples: []string{"agent-eval import agent-skills --format agent-skills --variant auto --skill-root ./skill --baseline no-skill"}, Options: agentSkillsImportOptions, Available: true},
			}},
			{Name: "export", Summary: "write a non-authoritative compatibility view to one new destination", Usage: "agent-eval export <command>", Children: []standaloneCommandDescriptor{
				{Name: "agent-skills", Summary: "export a captured Agent Skills workspace without executing it", Usage: "agent-eval export agent-skills --format agent-skills --variant VARIANT --skill-root DIR --baseline BASELINE --workspace-root DIR --destination ABSOLUTE_DIR [options]", Modes: []string{"agentskills-guide-v1", "anthropic-skill-creator-v1"}, Examples: []string{"agent-eval export agent-skills --format agent-skills --variant agentskills-guide-v1 --skill-root ./skill --baseline no-skill --workspace-root ./workspace --destination /absolute/new-output --case-directory 1=iteration-1/eval-example"}, Options: agentSkillsExportOptions, Available: true},
			}},
			{Name: "validate", Summary: "validate project, scenario, or run-spec inputs without network access", Usage: "agent-eval validate --kind scenario|run-spec --input FILE [--input FILE ...] [options]", Examples: []string{"agent-eval validate --kind scenario --input scenario.json"}, Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: "scenario|run-spec", Description: "input contract"}, {Name: "--input", Value: "FILE", Description: "bounded input; repeat for additional inputs"}}, common...), Available: true, ProcessAPI: true},
			{Name: "plan", Summary: "create an immutable execution plan", Usage: "agent-eval plan [options]", Options: common},
			{Name: "run", Summary: "execute a reviewed standalone plan", Usage: "agent-eval run --plan FILE [options]", Options: append([]standaloneOptionDescriptor{{Name: "--plan", Value: "FILE", Description: "reviewed immutable plan"}}, common...)},
			{Name: "resume", Summary: "resume only an attempt whose durable evidence permits it", Usage: "agent-eval resume [options]", Options: common},
			{Name: "reconcile", Summary: "append evidence without replaying an ambiguous identity", Usage: "agent-eval reconcile [options]", Options: common},
			{Name: "grade", Summary: "grade an observation with a deterministic evaluator", Usage: "agent-eval grade --mode deterministic --scenario FILE --observation FILE [options]", Examples: []string{"agent-eval grade --mode deterministic --scenario scenario.json --observation observation.json"}, Options: append([]standaloneOptionDescriptor{{Name: "--mode", Value: "deterministic|judge", Description: "grading authority"}, {Name: "--scenario", Value: "FILE", Description: "scenario contract"}, {Name: "--observation", Value: "FILE", Description: "observation contract"}}, common...), Available: true},
			{Name: "compare", Summary: "compare or aggregate content-minimized result artifacts", Usage: "agent-eval compare --kind results|root [options]", Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: "results|root|pair|set", Description: "comparison contract"}, {Name: "--input", Value: "FILE", Description: "result input; repeat for additional inputs"}, {Name: "--root", Value: "DIR", Description: "marked synthetic result root"}}, common...), Available: true, ProcessAPI: true},
			{Name: "report", Summary: "render a read-only standalone report", Usage: "agent-eval report --format FORMAT [options]", Options: common},
			{Name: "inspect", Summary: "inspect configuration provenance or a benchmark corpus", Usage: "agent-eval inspect --kind configuration|corpus [options]", Examples: []string{"agent-eval inspect --kind configuration --project . --explain"}, Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: "configuration|corpus|artifact", Description: "inspection target"}, {Name: "--root", Value: "DIR", Description: "corpus root"}}, common...), Available: true, ProcessAPI: true},
			{Name: "schema", Summary: "inspect standalone artifact schema support", Usage: "agent-eval schema <command>", Children: []standaloneCommandDescriptor{
				{Name: "inspect", Summary: "inspect a versioned artifact schema", Usage: "agent-eval schema inspect [options]", Options: common},
			}},
			{Name: "migrate", Summary: "preview or apply an explicit artifact migration", Usage: "agent-eval migrate <command>", Children: []standaloneCommandDescriptor{
				{Name: "preview", Summary: "produce a reviewed migration preview without changing source bytes", Usage: "agent-eval migrate preview [options]", Options: common},
				{Name: "apply", Summary: "apply an exactly reviewed migration", Usage: "agent-eval migrate apply [options]", Options: common},
			}},
			{Name: "compat", Summary: "verify provider-free component compatibility", Usage: "agent-eval compat <command>", Children: []standaloneCommandDescriptor{
				{Name: "verify", Summary: "verify a selected local component", Usage: "agent-eval compat verify --target TARGET [options]", Options: append([]standaloneOptionDescriptor{{Name: "--target", Value: "atl|codex-skill-package|extension-protocol", Description: "selected compatibility target"}}, common...)},
			}},
			{Name: "completion", Summary: "generate shell completion from the public command descriptors", Usage: "agent-eval completion <bash|fish|powershell|zsh>", Available: true, Children: []standaloneCommandDescriptor{
				{Name: "bash", Summary: "generate Bash completion", Usage: "agent-eval completion bash", Available: true},
				{Name: "fish", Summary: "generate fish completion", Usage: "agent-eval completion fish", Available: true},
				{Name: "powershell", Summary: "generate PowerShell completion", Usage: "agent-eval completion powershell", Available: true},
				{Name: "zsh", Summary: "generate Zsh completion", Usage: "agent-eval completion zsh", Available: true},
			}},
			{Name: "process", Summary: "execute exactly one bounded, strictly decoded JSON request", Usage: "agent-eval process", Available: true},
			{Name: "help", Summary: "show root, parent, or leaf help", Usage: "agent-eval help [command [subcommand]]", Available: true},
		},
	}
}

func runStandaloneCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (bool, error) {
	if !standaloneInvocation(args) {
		return false, nil
	}
	if path, requested := standaloneRequestedHelp(args); requested {
		if !writeStandaloneHelp(stdout, path) {
			failure := standaloneFail(standaloneUsageError, "unknown_help_topic")
			standaloneWriteFailure(stderr, failure)
			return true, standaloneExitStatus{failure.class.code}
		}
		return true, nil
	}
	if len(args) == 1 && args[0] == "--version" {
		args = []string{"version"}
	}
	if args[0] == "completion" {
		if len(args) != 2 || !writeStandaloneCompletion(stdout, args[1]) {
			failure := standaloneFail(standaloneUsageError, "invalid_completion_shell")
			standaloneWriteFailure(stderr, failure)
			return true, standaloneExitStatus{failure.class.code}
		}
		return true, nil
	}
	if args[0] == "process" {
		failure := runStandaloneProcess(stdin, stdout, stderr, args[1:])
		if failure != nil {
			return true, standaloneExitStatus{failure.class.code}
		}
		return true, nil
	}
	outcome, failure := executeStandalone(args)
	if failure != nil {
		standaloneWriteFailure(stderr, failure)
		return true, standaloneExitStatus{failure.class.code}
	}
	if err := standaloneWriteOutcome(stdout, outcome); err != nil {
		failure = standaloneFail(standaloneInternalError, "output_failed")
		standaloneWriteFailure(stderr, failure)
		return true, standaloneExitStatus{failure.class.code}
	}
	return true, nil
}

func standaloneInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return !standaloneLegacyInvocation(args)
}

func standaloneLegacyInvocation(args []string) bool {
	switch args[0] {
	case "aggregate", "aggregate-root", "assess", "attempt-ledger", "evaluate", "inventory", "private",
		"review-template", "validate-comparison-set", "validate-pair", "validate-run",
		"verify-atl-capabilities", "verify-codex-skill-package", "verify-extension-protocol":
		return true
	case "validate":
		if len(args) < 2 {
			return false
		}
		for _, argument := range args[1:] {
			if argument == "" || strings.HasPrefix(argument, "-") {
				return false
			}
		}
		return true
	case "run":
		return standaloneLegacyRunInvocation(args[1:])
	default:
		return false
	}
}

func standaloneLegacyRunInvocation(args []string) bool {
	flags := flag.NewFlagSet("legacy-run-routing", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var spec, outputRoot, repositoryRoot, agentBinary, atlBinary, pluginRoot, liveConfigDir, externalMCPProfile, model string
	var repetitions int
	var dryRun bool
	flags.StringVar(&spec, "spec", "", "")
	flags.StringVar(&outputRoot, "output-root", "", "")
	flags.StringVar(&repositoryRoot, "repository-root", ".", "")
	flags.StringVar(&agentBinary, "agent-binary", "", "")
	flags.StringVar(&atlBinary, "atl-binary", "", "")
	flags.StringVar(&pluginRoot, "plugin-root", ".", "")
	flags.StringVar(&liveConfigDir, "live-config-dir", "", "")
	flags.StringVar(&externalMCPProfile, "external-mcp-profile", "", "")
	flags.StringVar(&model, "model", "", "")
	flags.IntVar(&repetitions, "repetitions", 0, "")
	flags.BoolVar(&dryRun, "dry-run", false, "")
	return flags.Parse(args) == nil && flags.NArg() == 0 && spec != ""
}

func standaloneHasHelp(args []string) bool {
	for _, argument := range args {
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return false
}

func standaloneRequestedHelp(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, true
	}
	if args[0] == "--help" || args[0] == "-h" {
		return nil, true
	}
	if args[0] == "help" {
		return args[1:], true
	}
	if !standaloneHasHelp(args) {
		return nil, false
	}
	if _, ok := standaloneDescriptorChild(standaloneCommandTree(), args[0]); !ok {
		return []string{args[0]}, true
	}
	path := make([]string, 0, 2)
	descriptor := standaloneCommandTree()
	for _, argument := range args {
		if strings.HasPrefix(argument, "-") {
			break
		}
		child, ok := standaloneDescriptorChild(descriptor, argument)
		if !ok {
			break
		}
		path = append(path, argument)
		descriptor = child
	}
	return path, true
}

func writeStandaloneHelp(writer io.Writer, path []string) bool {
	descriptor := standaloneCommandTree()
	for _, name := range path {
		child, ok := standaloneDescriptorChild(descriptor, name)
		if !ok {
			return false
		}
		descriptor = child
	}
	fmt.Fprintln(writer, descriptor.Summary)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Usage:")
	fmt.Fprintln(writer, "  "+descriptor.Usage)
	if len(descriptor.Children) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Commands:")
		for _, child := range descriptor.Children {
			status := ""
			if len(child.Children) == 0 && !child.Available {
				status = " (reserved)"
			}
			fmt.Fprintf(writer, "  %-14s %s%s\n", child.Name, child.Summary, status)
		}
	}
	if len(descriptor.Options) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Options:")
		for _, option := range descriptor.Options {
			name := option.Name
			if option.Value != "" {
				name += " " + option.Value
			}
			fmt.Fprintf(writer, "  %-28s %s\n", name, option.Description)
		}
	}
	if len(descriptor.Modes) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Modes:")
		for _, mode := range descriptor.Modes {
			fmt.Fprintln(writer, "  "+mode)
		}
	}
	if len(descriptor.Examples) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Examples:")
		for _, example := range descriptor.Examples {
			fmt.Fprintln(writer, "  "+example)
		}
	}
	if len(path) == 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "JSON is the default command output. Help and completion are explicit text modes.")
		fmt.Fprintln(writer, "Maintainer compatibility commands remain supported but are intentionally hidden.")
	}
	return true
}

func standaloneDescriptorChild(parent standaloneCommandDescriptor, name string) (standaloneCommandDescriptor, bool) {
	for _, child := range parent.Children {
		if child.Name == name {
			return child, true
		}
	}
	return standaloneCommandDescriptor{}, false
}

func standaloneDescriptorForInvocation(args []string) (standaloneCommandDescriptor, int, bool) {
	descriptor := standaloneCommandTree()
	consumed := 0
	for consumed < len(args) {
		child, ok := standaloneDescriptorChild(descriptor, args[consumed])
		if !ok {
			break
		}
		descriptor = child
		consumed++
	}
	return descriptor, consumed, consumed > 0
}

func executeStandalone(args []string) (standaloneOutcome, *standaloneFailure) {
	return executeStandaloneContext(context.Background(), args)
}

func executeStandaloneContext(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	if ctx == nil {
		ctx = context.Background()
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	descriptor, consumed, ok := standaloneDescriptorForInvocation(args)
	if !ok {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "unknown_command")
	}
	if len(descriptor.Children) > 0 {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "subcommand_required")
	}
	command := strings.Join(args[:consumed], " ")
	if !descriptor.Available {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	commandArgs := args[consumed:]
	switch command {
	case "version":
		return standaloneExecuteVersion(commandArgs)
	case "capabilities":
		return standaloneExecuteCapabilities(commandArgs)
	case "validate":
		return standaloneExecuteValidate(ctx, commandArgs)
	case "grade":
		return standaloneExecuteGrade(ctx, commandArgs)
	case "compare":
		return standaloneExecuteCompare(ctx, commandArgs)
	case "inspect":
		return standaloneExecuteInspect(ctx, commandArgs)
	case "import agent-skills":
		return standaloneExecuteAgentSkillsImport(ctx, commandArgs)
	case "export agent-skills":
		return standaloneExecuteAgentSkillsExport(ctx, commandArgs)
	default:
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
}

func standaloneContextFailure(ctx context.Context) *standaloneFailure {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	failure := standaloneFail(standaloneInterruptedError, "execution_canceled")
	failure.retrySafe = true
	return failure
}

type standaloneBuildIdentity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type standaloneSupportedVersion struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type standaloneVersionResult struct {
	Build           standaloneBuildIdentity      `json:"build"`
	ContractVersion string                       `json:"contract_version"`
	Schemas         []standaloneSupportedVersion `json:"schemas"`
	Protocols       []standaloneSupportedVersion `json:"protocols"`
}

func standaloneExecuteVersion(args []string) (standaloneOutcome, *standaloneFailure) {
	output, failure := standaloneParseOutputOnly(args)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	result := standaloneVersionResult{
		Build:           standaloneBuildIdentity{Version: standaloneBuildVersion, Commit: standaloneBuildCommit, Date: standaloneBuildDate},
		ContractVersion: standaloneContractVersion,
		Schemas: []standaloneSupportedVersion{
			{ID: "agent-skills-export-report", Version: agenteval.AgentSkillsExportReportVersion},
			{ID: "agent-skills-import-report", Version: agenteval.AgentSkillsImportReportVersion},
			{ID: "command-error", Version: 1},
			{ID: "command-result", Version: 1},
			{ID: "process-request", Version: 1},
			{ID: "project-config", Version: 1},
		},
		Protocols: []standaloneSupportedVersion{
			{ID: "extension", Version: 1},
			{ID: "process", Version: 1},
		},
	}
	return standaloneOutcome{command: "version", status: "completed", result: result, outputMode: output, text: standaloneBuildVersion + "\n"}, nil
}

type standaloneCapability struct {
	Command    string                        `json:"command"`
	Mode       string                        `json:"mode"`
	Status     string                        `json:"status"`
	Authority  string                        `json:"authority"`
	Dimensions standaloneAuthorityDimensions `json:"authority_dimensions"`
	Formats    []string                      `json:"formats,omitempty"`
	ProcessAPI bool                          `json:"process_api"`
}

type standaloneCapabilitiesResult struct {
	ContractVersion string                  `json:"contract_version"`
	Configuration   string                  `json:"configuration"`
	Environment     []string                `json:"environment_projections"`
	ExitClasses     []standaloneExitClassID `json:"exit_classes"`
	Capabilities    []standaloneCapability  `json:"capabilities"`
}

type standaloneExitClassID struct {
	Code int    `json:"code"`
	ID   string `json:"id"`
}

func standaloneExecuteCapabilities(args []string) (standaloneOutcome, *standaloneFailure) {
	output, failure := standaloneParseOutputOnly(args)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	capabilities := standaloneProductCapabilities()
	result := standaloneCapabilitiesResult{
		ContractVersion: standaloneContractVersion,
		Configuration:   "flags>project_file>opt_in_environment",
		Environment:     []string{"none", "portable-v1"},
		ExitClasses:     make([]standaloneExitClassID, 0, len(standaloneExitClassRegistry)),
		Capabilities:    capabilities,
	}
	for _, class := range standaloneExitClassRegistry {
		result.ExitClasses = append(result.ExitClasses, standaloneExitClassID{Code: class.code, ID: class.id})
	}
	return standaloneOutcome{command: "capabilities", status: "completed", result: result, outputMode: output, text: fmt.Sprintf("%d public leaf capabilities\n", len(capabilities))}, nil
}

func standaloneProductCapabilities() []standaloneCapability {
	type availability struct {
		processAPI bool
		formats    []string
	}
	implemented := map[string]availability{
		"capabilities/default": {processAPI: true},
		"compare/default":      {processAPI: true},
		"export/agent-skills": {
			formats: []string{standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic},
		},
		"grade/deterministic": {},
		"import/agent-skills": {
			formats: []string{standaloneAgentSkillsVariantAuto, standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic},
		},
		"inspect/default":  {processAPI: true},
		"validate/default": {processAPI: true},
		"version/default":  {processAPI: true},
	}
	profiles := standaloneAuthorityProfiles()
	capabilities := make([]standaloneCapability, 0, len(profiles))
	for _, profile := range profiles {
		key := profile.Operation + "/" + profile.Mode
		available, ok := implemented[key]
		status := "unsupported"
		if ok {
			status = "supported"
		}
		capabilities = append(capabilities, standaloneCapability{
			Command: profile.Operation, Mode: profile.Mode, Status: status,
			Authority: profile.Authority, Dimensions: profile.standaloneAuthorityDimensions,
			Formats: append([]string(nil), available.formats...), ProcessAPI: ok && available.processAPI,
		})
	}
	return capabilities
}

func standaloneWalkDescriptors(descriptor standaloneCommandDescriptor, path []string, visit func([]string, standaloneCommandDescriptor)) {
	for _, child := range descriptor.Children {
		childPath := append(append([]string(nil), path...), child.Name)
		visit(childPath, child)
		standaloneWalkDescriptors(child, childPath, visit)
	}
}

func standaloneParseOutputOnly(args []string) (string, *standaloneFailure) {
	parsed, failure := parseStandaloneFlags(args, map[string]standaloneFlagSpec{
		"output": {takesValue: true},
	})
	if failure != nil {
		return "", failure
	}
	if len(parsed.positionals) != 0 {
		return "", standaloneFail(standaloneUsageError, "unexpected_argument")
	}
	return parsed.outputMode()
}

func standalonePeekFlag(args []string, name string) (string, *standaloneFailure) {
	var value string
	seen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, "--"+name+"=") {
			if seen {
				return "", standaloneFail(standaloneUsageError, "duplicate_flag")
			}
			seen = true
			value = strings.TrimPrefix(argument, "--"+name+"=")
			continue
		}
		if argument != "--"+name {
			continue
		}
		if seen || index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
			return "", standaloneFail(standaloneUsageError, "invalid_flag")
		}
		seen = true
		value = args[index+1]
		index++
	}
	return value, nil
}

func standaloneOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func standaloneWriteOutcome(writer io.Writer, outcome standaloneOutcome) error {
	if outcome.outputMode == "text" {
		_, err := io.WriteString(writer, outcome.text)
		return err
	}
	return standaloneEncodeJSON(writer, standaloneResultEnvelope{
		Schema:          standaloneResultSchema,
		SchemaVersion:   1,
		ContractVersion: standaloneContractVersion,
		Command:         outcome.command,
		Status:          outcome.status,
		Result:          outcome.result,
	})
}

func standaloneWriteFailure(writer io.Writer, failure *standaloneFailure) {
	if failure == nil {
		return
	}
	_ = standaloneEncodeJSON(writer, standaloneErrorEnvelope{
		Schema:          standaloneErrorSchema,
		SchemaVersion:   1,
		ContractVersion: standaloneContractVersion,
		Error:           failure.class.message,
		ExitClass:       failure.class.id,
		Kind:            failure.kind,
		RetrySafe:       failure.retrySafe,
		Recovery:        standaloneRecovery{SchemaVersion: 1, Action: failure.class.recovery},
	})
}

func standaloneEncodeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
