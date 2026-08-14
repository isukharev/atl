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
	standaloneUsageError           = standaloneExitClass{2, "usage_error", "command usage rejected", "fix_usage"}
	standaloneConfigurationError   = standaloneExitClass{3, "configuration_error", "configuration rejected", "complete_configuration"}
	standaloneInputError           = standaloneExitClass{4, "input_error", "input rejected", "fix_input"}
	standaloneCompatibilityError   = standaloneExitClass{5, "compatibility_error", "operation unavailable", "select_compatible_component"}
	standalonePolicyDeniedError    = standaloneExitClass{6, "policy_denied", "policy denied", "request_authority"}
	standaloneExecutionFailedError = standaloneExitClass{8, "execution_failed", "execution failed", "inspect_execution"}
	standaloneInternalError        = standaloneExitClass{1, "internal_error", "internal failure", "report_bug"}
	standaloneOutcomeUnknownError  = standaloneExitClass{10, "outcome_unknown", "outcome unknown", "reconcile_outcome"}
	standaloneInterruptedError     = standaloneExitClass{11, "interrupted", "operation interrupted", "resume"}
)

var standaloneExitClassRegistry = [...]standaloneExitClass{
	{0, "success", "", ""},
	standaloneInternalError,
	standaloneUsageError,
	standaloneConfigurationError,
	standaloneInputError,
	standaloneCompatibilityError,
	standalonePolicyDeniedError,
	{7, "authentication_failed", "authentication failed", "reauthenticate"},
	standaloneExecutionFailedError,
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
	Name          string
	Summary       string
	Usage         string
	Modes         []string
	ModeFlag      string
	ReservedModes []string
	Examples      []string
	Options       []standaloneOptionDescriptor
	Available     bool
	ProcessAPI    bool
	Children      []standaloneCommandDescriptor
}

// standaloneCommandTree returns a fresh value tree. Callers cannot mutate the
// canonical command registry, while help, completion, routing, and
// capabilities all consume the same descriptors.
func standaloneCommandTree() standaloneCommandDescriptor {
	gradeModes := standaloneOperationModes("grade", true)
	reservedGradeModes := standaloneOperationModes("grade", false)
	runModes := standaloneOperationModes("run", true)
	compareKinds := []string{"experiment", "results", "root"}
	importFormats := standaloneOperationFormats("import", "agent-skills")
	exportFormats := standaloneOperationFormats("export", "agent-skills")
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
		{Name: "--variant", Value: strings.Join(importFormats, "|"), Description: "select or safely detect the documented source variant"},
		{Name: "--skill-root", Value: "DIR", Description: "read one exact bounded skill tree"},
		{Name: "--eval-root", Value: "DIR", Description: "optionally select the exact evaluation source root"},
		{Name: "--baseline", Value: "no-skill|previous-skill", Description: "select the explicit comparison baseline"},
		{Name: "--previous-skill-root", Value: "DIR", Description: "required only for the previous-skill baseline"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	agentSkillsExportOptions := append([]standaloneOptionDescriptor(nil), agentSkillsImportOptions...)
	for index := range agentSkillsExportOptions {
		if agentSkillsExportOptions[index].Name == "--variant" {
			agentSkillsExportOptions[index].Value = strings.Join(exportFormats, "|")
			agentSkillsExportOptions[index].Description = "select the exact documented workspace variant"
		}
	}
	agentSkillsExportOptions = append(agentSkillsExportOptions,
		standaloneOptionDescriptor{Name: "--workspace-root", Value: "DIR", Description: "read one exact bounded iteration workspace"},
		standaloneOptionDescriptor{Name: "--destination", Value: "ABSOLUTE_DIR", Description: "write one exact clean and previously nonexistent destination"},
		standaloneOptionDescriptor{Name: "--case-directory", Value: "ID=iteration-N/eval-slug", Description: "bind each Guide case to one exact workspace directory; repeat as needed"},
	)
	schemaInspectOptions := []standaloneOptionDescriptor{
		{Name: "--namespace", Value: "ID", Description: "select the exact schema namespace"},
		{Name: "--kind", Value: "ID", Description: "select the exact artifact kind"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	migrationOptions := []standaloneOptionDescriptor{
		{Name: "--namespace", Value: "ID", Description: "select the exact schema namespace"},
		{Name: "--kind", Value: "ID", Description: "select the exact artifact kind"},
		{Name: "--from", Value: "VERSION", Description: "select the exact source generation"},
		{Name: "--to", Value: "VERSION", Description: "select the exact target generation"},
		{Name: "--root", Value: "DIR", Description: "select the exact owner-private workspace root"},
		{Name: "--repository-root", Value: "DIR", Description: "bind the exact repository root (default .)"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	migrationApplyOptions := append([]standaloneOptionDescriptor(nil), migrationOptions...)
	migrationApplyOptions = append(migrationApplyOptions,
		standaloneOptionDescriptor{Name: "--expected-preview-sha256", Value: "SHA256", Description: "bind the exact reviewed preview"},
		standaloneOptionDescriptor{Name: "--confirm", Value: "MIGRATE", Description: "authorize the reviewed local mutation"},
	)
	promotionOptions := []standaloneOptionDescriptor{
		{Name: "--comparison", Value: "FILE", Description: "read one bounded immutable candidate comparison"},
		{Name: "--store", Value: "DIR", Description: "write the exact owner-only promotion reference store"},
		{Name: "--expected-comparison-sha256", Value: "SHA256", Description: "bind the exact reviewed comparison bytes"},
		{Name: "--confirm", Value: "PROMOTE", Description: "authorize the reviewed promotion mutation"},
		{Name: "--dry-run", Description: "evaluate and validate without writing the store"},
		{Name: "--explain", Description: "emit identity-only decision provenance"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	rollbackOptions := []standaloneOptionDescriptor{
		{Name: "--receipt", Value: "FILE", Description: "read one exact rollback receipt"},
		{Name: "--store", Value: "DIR", Description: "write the exact owner-only promotion reference store"},
		{Name: "--expected-rollback-sha256", Value: "SHA256", Description: "bind the exact reviewed rollback request"},
		{Name: "--confirm", Value: "ROLLBACK", Description: "authorize the exact rollback mutation"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
	root := standaloneCommandDescriptor{
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
			{Name: "capabilities", Summary: "report supported standalone operations without reading configuration", Usage: "agent-eval capabilities [--output json|text]"},
			{Name: "version", Summary: "report build, schema, and protocol identity without reading configuration", Usage: "agent-eval version [--output json|text]"},
			{Name: "init", Summary: "initialize a standalone evaluation project", Usage: "agent-eval init [options]", Options: common},
			{Name: "import", Summary: "inspect a selected external evaluation representation without writing", Usage: "agent-eval import <command>", Children: []standaloneCommandDescriptor{
				{Name: "agent-skills", Summary: "inspect a bounded Agent Skills source without execution or writes", Usage: "agent-eval import agent-skills --format agent-skills --variant VARIANT --skill-root DIR --baseline BASELINE [options]", Modes: importFormats, ModeFlag: "--variant", Examples: []string{"agent-eval import agent-skills --format agent-skills --variant auto --skill-root ./skill --baseline no-skill"}, Options: agentSkillsImportOptions},
			}},
			{Name: "export", Summary: "write a non-authoritative compatibility view to one new destination", Usage: "agent-eval export <command>", Children: []standaloneCommandDescriptor{
				{Name: "agent-skills", Summary: "export a captured Agent Skills workspace without executing it", Usage: "agent-eval export agent-skills --format agent-skills --variant VARIANT --skill-root DIR --baseline BASELINE --workspace-root DIR --destination ABSOLUTE_DIR [options]", Modes: exportFormats, ModeFlag: "--variant", Examples: []string{"agent-eval export agent-skills --format agent-skills --variant agentskills-guide-v1 --skill-root ./skill --baseline no-skill --workspace-root ./workspace --destination /absolute/new-output --case-directory 1=iteration-1/eval-example"}, Options: agentSkillsExportOptions},
			}},
			{Name: "validate", Summary: "validate project, scenario, or run-spec inputs without network access", Usage: "agent-eval validate --kind scenario|run-spec --input FILE [--input FILE ...] [options]", Examples: []string{"agent-eval validate --kind scenario --input scenario.json"}, Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: "scenario|run-spec", Description: "input contract"}, {Name: "--input", Value: "FILE", Description: "bounded input; repeat for additional inputs"}}, common...)},
			{Name: "plan", Summary: "create an immutable execution plan", Usage: "agent-eval plan [options]", Options: common},
			{Name: "promote", Summary: "apply a guarded immutable candidate decision", Usage: "agent-eval promote --comparison FILE --store DIR --expected-comparison-sha256 SHA256 --confirm PROMOTE [options]", Examples: []string{"agent-eval promote --comparison comparison.json --store /absolute/reference-store --expected-comparison-sha256 SHA256 --confirm PROMOTE"}, Options: promotionOptions},
			{Name: "run", Summary: "execute one admitted bounded reference profile", Usage: "agent-eval run --mode reference --manifest FILE --bundle FILE --destination ABSOLUTE_DIR [--workers N|--sequential] [--output json|text]", Modes: runModes, ModeFlag: "--mode", Examples: []string{"agent-eval run --mode reference --manifest manifest.json --bundle reference-bundle.json --destination /absolute/new-output --workers 4"}, Options: standaloneReferenceOptions(runModes, true)},
			{Name: "resume", Summary: "resume the never-started complement of an incomplete reference publication", Usage: "agent-eval resume --mode reference --manifest FILE --bundle FILE --destination ABSOLUTE_DIR [--workers N|--sequential] [--output json|text]", Modes: standaloneOperationModes("resume", true), ModeFlag: "--mode", Examples: []string{"agent-eval resume --mode reference --manifest manifest.json --bundle reference-bundle.json --destination /absolute/incomplete-output --workers 4"}, Options: standaloneReferenceOptions(standaloneOperationModes("resume", true), false)},
			{Name: "rollback", Summary: "restore one exact immutable prior identity", Usage: "agent-eval rollback --receipt FILE --store DIR --expected-rollback-sha256 SHA256 --confirm ROLLBACK [options]", Examples: []string{"agent-eval rollback --receipt rollback.json --store /absolute/reference-store --expected-rollback-sha256 SHA256 --confirm ROLLBACK"}, Options: rollbackOptions},
			{Name: "reconcile", Summary: "append evidence without replaying an ambiguous identity", Usage: "agent-eval reconcile [options]", Options: common},
			{Name: "grade", Summary: "grade an observation with a deterministic evaluator", Usage: "agent-eval grade --mode deterministic --scenario FILE --observation FILE [options]", ReservedModes: reservedGradeModes, Examples: []string{"agent-eval grade --mode deterministic --scenario scenario.json --observation observation.json"}, Options: append([]standaloneOptionDescriptor{{Name: "--mode", Value: strings.Join(gradeModes, "|"), Description: "supported grading authority"}, {Name: "--scenario", Value: "FILE", Description: "scenario contract"}, {Name: "--observation", Value: "FILE", Description: "observation contract"}}, common...)},
			{Name: "compare", Summary: "compare results or analyze one complete reference experiment publication", Usage: "agent-eval compare --kind experiment|results|root [options]", Modes: compareKinds, ModeFlag: "--kind", Examples: []string{"agent-eval compare --kind experiment --root /absolute/completed-reference-publication"}, Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: strings.Join(compareKinds, "|"), Description: "supported comparison contract"}, {Name: "--input", Value: "FILE", Description: "result input; repeat for additional inputs"}, {Name: "--root", Value: "DIR", Description: "bounded result or completed reference experiment root"}}, common...)},
			{Name: "report", Summary: "render a read-only standalone report", Usage: "agent-eval report --format FORMAT [options]", Options: common},
			{Name: "inspect", Summary: "inspect configuration provenance or a benchmark corpus", Usage: "agent-eval inspect --kind configuration|corpus [options]", Examples: []string{"agent-eval inspect --kind configuration --project . --explain"}, Options: append([]standaloneOptionDescriptor{{Name: "--kind", Value: "configuration|corpus|artifact", Description: "inspection target"}, {Name: "--root", Value: "DIR", Description: "corpus root"}}, common...)},
			{Name: "schema", Summary: "inspect standalone artifact schema support", Usage: "agent-eval schema <command>", Children: []standaloneCommandDescriptor{
				{Name: "inspect", Summary: "inspect a versioned artifact schema", Usage: "agent-eval schema inspect --namespace ID --kind ID [--output json|text]", Options: schemaInspectOptions},
			}},
			{Name: "migrate", Summary: "preview or apply an explicit artifact migration", Usage: "agent-eval migrate <command>", Children: []standaloneCommandDescriptor{
				{Name: "preview", Summary: "produce a reviewed migration preview without changing source bytes", Usage: "agent-eval migrate preview --namespace ID --kind ID --from VERSION --to VERSION --root DIR [options]", Options: migrationOptions},
				{Name: "apply", Summary: "apply an exactly reviewed migration", Usage: "agent-eval migrate apply --namespace ID --kind ID --from VERSION --to VERSION --root DIR --expected-preview-sha256 SHA256 --confirm MIGRATE [options]", Options: migrationApplyOptions},
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
	standaloneBindCommandRegistry(&root, nil)
	return root
}

func standaloneBindCommandRegistry(descriptor *standaloneCommandDescriptor, path []string) {
	for index := range descriptor.Children {
		child := &descriptor.Children[index]
		childPath := append(append([]string(nil), path...), child.Name)
		if len(child.Children) == 0 {
			if available, processAPI, found := standaloneCommandRegistryState(strings.Join(childPath, " ")); found {
				child.Available = available
				child.ProcessAPI = processAPI
			}
		}
		standaloneBindCommandRegistry(child, childPath)
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
		"verify-agent-adapter", "verify-atl-capabilities", "verify-codex-skill-package", "verify-execution-backend", "verify-extension-protocol", "verify-grader":
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
	if _, _, registered := standaloneCommandRegistryState(strings.Join(path, " ")); registered {
		status := "reserved (unavailable)"
		if descriptor.Available {
			status = "pre-release (supported)"
		}
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Status:")
		fmt.Fprintln(writer, "  "+status)
	}
	if len(descriptor.Children) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Commands:")
		for _, child := range descriptor.Children {
			status := ""
			if !standaloneDescriptorCompletable(child) {
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
	if len(descriptor.ReservedModes) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Reserved modes (unavailable):")
		for _, mode := range descriptor.ReservedModes {
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
	case "run":
		return standaloneExecuteReferenceRun(ctx, commandArgs)
	case "resume":
		return standaloneExecuteReferenceResume(ctx, commandArgs)
	case "import agent-skills":
		return standaloneExecuteAgentSkillsImport(ctx, commandArgs)
	case "export agent-skills":
		return standaloneExecuteAgentSkillsExport(ctx, commandArgs)
	case "schema inspect":
		return standaloneExecuteSchemaInspect(ctx, commandArgs)
	case "migrate preview":
		return standaloneExecuteMigrationPreview(ctx, commandArgs)
	case "migrate apply":
		return standaloneExecuteMigrationApply(ctx, commandArgs)
	case "promote":
		return standaloneExecutePromotion(ctx, commandArgs)
	case "rollback":
		return standaloneExecuteRollback(ctx, commandArgs)
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
			{ID: "analysis-report", Version: agenteval.AnalysisReportSchemaVersion},
			{ID: "command-error", Version: 1},
			{ID: "command-result", Version: 1},
			{ID: "migration-preview", Version: agenteval.StandaloneMigrationArtifactVersion},
			{ID: "migration-result", Version: agenteval.StandaloneMigrationArtifactVersion},
			{ID: "process-request", Version: 1},
			{ID: "project-config", Version: 1},
			{ID: "promotion-comparison", Version: agenteval.PromotionSchemaVersion},
			{ID: "promotion-decision", Version: agenteval.PromotionSchemaVersion},
			{ID: "promotion-rollback", Version: agenteval.PromotionSchemaVersion},
			{ID: "scheduler-plan", Version: agenteval.SchedulerSchemaVersion},
			{ID: "scheduler-report", Version: agenteval.SchedulerSchemaVersion},
			{ID: "schema-registry", Version: agenteval.StandaloneSchemaRegistryVersion},
			{ID: "sequential-reference-bundle", Version: agenteval.SequentialReferenceSchemaVersion},
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
	profiles := standaloneAuthorityProfiles()
	capabilities := make([]standaloneCapability, 0, len(profiles))
	for _, profile := range profiles {
		status := "unsupported"
		if profile.Supported {
			status = "supported"
		}
		capabilities = append(capabilities, standaloneCapability{
			Command: profile.Operation, Mode: profile.Mode, Status: status,
			Authority: profile.Authority, Dimensions: profile.standaloneAuthorityDimensions,
			Formats: append([]string(nil), profile.Formats...), ProcessAPI: profile.Supported && profile.ProcessAPI,
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
