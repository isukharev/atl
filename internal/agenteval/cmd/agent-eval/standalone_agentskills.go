package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/agenteval"
)

const (
	standaloneAgentSkillsFormat           = "agent-skills"
	standaloneAgentSkillsVariantAuto      = "auto"
	standaloneAgentSkillsVariantGuide     = "agentskills-guide-v1"
	standaloneAgentSkillsVariantAnthropic = "anthropic-skill-creator-v1"
	standaloneAgentSkillsBaselineNone     = "no-skill"
	standaloneAgentSkillsBaselinePrevious = "previous-skill"
)

type standaloneAgentSkillsOptions struct {
	parsed          standaloneParsedFlags
	variant         string
	importOptions   agenteval.AgentSkillsImportOptions
	workspaceRoot   string
	destination     string
	caseDirectories []agenteval.AgentSkillsCaseDirectory
}

func standaloneExecuteAgentSkillsImport(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	options, failure := parseStandaloneAgentSkillsOptions(args, false)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneAgentSkillsAuthority("import", "agent-skills"); failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	report, err := agenteval.InspectAgentSkillsImport(options.importOptions)
	if err != nil {
		return standaloneOutcome{}, standaloneAgentSkillsFailure(err)
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	return standaloneOutcome{
		command: "import agent-skills", status: "completed", result: report,
		outputMode: options.parsed.outputModeValue(), text: "Agent Skills import inspected\n",
	}, nil
}

func standaloneExecuteAgentSkillsExport(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	options, failure := parseStandaloneAgentSkillsOptions(args, true)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneAgentSkillsAuthority("export", "agent-skills"); failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := validateStandaloneAgentSkillsDestination(options.destination); failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	report, err := agenteval.ExportAgentSkillsWorkspace(agenteval.AgentSkillsExportOptions{
		Import: options.importOptions, WorkspaceRoot: options.workspaceRoot,
		Destination: options.destination, CaseDirectories: options.caseDirectories,
	})
	if err != nil {
		return standaloneOutcome{}, standaloneAgentSkillsFailure(err)
	}
	return standaloneOutcome{
		command: "export agent-skills", status: "completed", result: report,
		outputMode: options.parsed.outputModeValue(), text: "Agent Skills export completed\n",
	}, nil
}

func parseStandaloneAgentSkillsOptions(args []string, export bool) (standaloneAgentSkillsOptions, *standaloneFailure) {
	specs := map[string]standaloneFlagSpec{
		"format":              {takesValue: true},
		"variant":             {takesValue: true},
		"skill-root":          {takesValue: true},
		"eval-root":           {takesValue: true},
		"baseline":            {takesValue: true},
		"previous-skill-root": {takesValue: true},
		"output":              {takesValue: true},
	}
	if export {
		specs["workspace-root"] = standaloneFlagSpec{takesValue: true}
		specs["destination"] = standaloneFlagSpec{takesValue: true}
		specs["case-directory"] = standaloneFlagSpec{takesValue: true, repeatable: true}
	}
	parsed, failure := parseStandaloneFlags(args, specs)
	if failure != nil {
		return standaloneAgentSkillsOptions{}, failure
	}
	variant := parsed.one("variant")
	if len(parsed.positionals) != 0 || parsed.one("format") != standaloneAgentSkillsFormat ||
		parsed.one("skill-root") == "" || parsed.one("baseline") == "" || variant == "" {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "invalid_agent_skills_options")
	}
	if !standaloneOneOf(variant, standaloneAgentSkillsVariantAuto, standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic) ||
		(export && variant == standaloneAgentSkillsVariantAuto) {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "invalid_agent_skills_variant")
	}
	baseline, previousRoot := parsed.one("baseline"), parsed.one("previous-skill-root")
	if !standaloneOneOf(baseline, standaloneAgentSkillsBaselineNone, standaloneAgentSkillsBaselinePrevious) ||
		(baseline == standaloneAgentSkillsBaselineNone && previousRoot != "") ||
		(baseline == standaloneAgentSkillsBaselinePrevious && previousRoot == "") {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "invalid_agent_skills_baseline")
	}
	options := standaloneAgentSkillsOptions{
		parsed: parsed, variant: variant,
		importOptions: agenteval.AgentSkillsImportOptions{
			SkillRoot: parsed.one("skill-root"), EvalRoot: parsed.one("eval-root"),
			PreviousSkillRoot: previousRoot, Format: variant, Baseline: baseline,
		},
	}
	if !export {
		return options, nil
	}
	options.workspaceRoot, options.destination = parsed.one("workspace-root"), parsed.one("destination")
	if options.workspaceRoot == "" || options.destination == "" {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "invalid_agent_skills_export_options")
	}
	caseDirectories, failure := parseStandaloneAgentSkillsCaseDirectories(parsed.many("case-directory"))
	if failure != nil {
		return standaloneAgentSkillsOptions{}, failure
	}
	if variant == standaloneAgentSkillsVariantAnthropic && len(caseDirectories) != 0 {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "agent_skills_case_directories_not_allowed")
	}
	if variant == standaloneAgentSkillsVariantGuide && len(caseDirectories) == 0 {
		return standaloneAgentSkillsOptions{}, standaloneFail(standaloneUsageError, "agent_skills_case_directories_required")
	}
	options.caseDirectories = caseDirectories
	return options, nil
}

func parseStandaloneAgentSkillsCaseDirectories(values []string) ([]agenteval.AgentSkillsCaseDirectory, *standaloneFailure) {
	result := make([]agenteval.AgentSkillsCaseDirectory, 0, len(values))
	seenIDs := make(map[uint32]struct{}, len(values))
	seenPaths := make(map[string]struct{}, len(values))
	for _, value := range values {
		idText, directory, ok := strings.Cut(value, "=")
		parsedID, err := strconv.ParseUint(idText, 10, 32)
		if !ok || err != nil || parsedID == 0 || idText != strconv.FormatUint(parsedID, 10) ||
			strings.Contains(directory, "=") || !validStandaloneAgentSkillsGuideDirectory(directory) {
			return nil, standaloneFail(standaloneUsageError, "invalid_agent_skills_case_directory")
		}
		// ParseUint with bitSize 32 proves that the conversion cannot truncate.
		caseID := uint32(parsedID) // #nosec G115
		if _, duplicate := seenIDs[caseID]; duplicate {
			return nil, standaloneFail(standaloneUsageError, "duplicate_agent_skills_case_directory")
		}
		if _, duplicate := seenPaths[directory]; duplicate {
			return nil, standaloneFail(standaloneUsageError, "duplicate_agent_skills_case_directory")
		}
		seenIDs[caseID] = struct{}{}
		seenPaths[directory] = struct{}{}
		result = append(result, agenteval.AgentSkillsCaseDirectory{CaseID: caseID, Path: directory})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CaseID != result[j].CaseID {
			return result[i].CaseID < result[j].CaseID
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func validStandaloneAgentSkillsGuideDirectory(value string) bool {
	components := strings.Split(value, "/")
	if len(components) != 2 || strings.Contains(value, "\\") ||
		!strings.HasPrefix(components[0], "iteration-") || !strings.HasPrefix(components[1], "eval-") {
		return false
	}
	iterationText := strings.TrimPrefix(components[0], "iteration-")
	iteration, err := strconv.ParseUint(iterationText, 10, 32)
	slug := strings.TrimPrefix(components[1], "eval-")
	if err != nil || iteration == 0 || iterationText != strconv.FormatUint(iteration, 10) ||
		slug == "" || len(slug) > 128 || slug[0] == '-' || slug[len(slug)-1] == '-' || strings.Contains(slug, "--") {
		return false
	}
	for _, character := range []byte(slug) {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validateStandaloneAgentSkillsDestination(destination string) *standaloneFailure {
	if destination == "" || strings.IndexByte(destination, 0) >= 0 || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return standaloneFail(standaloneUsageError, "invalid_destination")
	}
	_, err := os.Lstat(destination)
	switch {
	case err == nil:
		return standaloneFail(standaloneInputError, "destination_exists")
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return standaloneFail(standaloneInputError, "invalid_destination")
	}
}

func standaloneAgentSkillsAuthority(operation, mode string) *standaloneFailure {
	profile, ok := standaloneAuthorityProfileFor(operation, mode)
	expectWrite := operation == "export"
	if !ok || !profile.LocalRead || profile.LocalWrite != expectWrite || profile.ProcessSpawn ||
		profile.ProviderContact || profile.BackendContact || profile.Network || profile.CredentialAccess ||
		profile.PrivateWorkspaceAccess {
		return standaloneFail(standaloneInternalError, "authority_profile_invalid")
	}
	return nil
}

func standaloneAgentSkillsFailure(err error) *standaloneFailure {
	code, ok := agenteval.AgentSkillsErrorCode(err)
	if !ok {
		return standaloneFail(standaloneInternalError, "agent_skills_internal")
	}
	switch code {
	case "publication_failed":
		return standaloneFail(standaloneOutcomeUnknownError, "agent_skills_publication_failed")
	case "invalid_destination":
		return standaloneFail(standaloneInputError, "invalid_destination")
	case "invalid_request", "invalid_root", "unstable_source", "limit_exceeded", "invalid_skill", "invalid_evals",
		"invalid_workspace", "invalid_projection", "invalid_export", "invalid_publication":
		return standaloneFail(standaloneInputError, "agent_skills_"+code)
	default:
		return standaloneFail(standaloneInternalError, "agent_skills_internal")
	}
}
