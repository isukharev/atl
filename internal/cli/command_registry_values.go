package cli

import (
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// These closed value codecs keep the command-registry grammar and its public
// annotations in one small owner without coupling it to Cobra tree assembly.
func validMutationProfile(profile mutationProfile) bool {
	switch profile {
	case mutationLocalDirect, mutationRemoteDirect, mutationPreviewApply, mutationDedicatedApply, mutationPlan:
		return true
	default:
		return false
	}
}

func parsePolicyVerbs(value string) ([]policyVerb, bool) {
	if value == "none" {
		return nil, true
	}
	parts := strings.Split(value, ",")
	verbs := make([]policyVerb, 0, len(parts))
	seen := map[domain.WriteVerb]bool{}
	for _, part := range parts {
		conditional := strings.HasSuffix(part, "?")
		name := strings.TrimSuffix(part, "?")
		verb := domain.WriteVerb(name)
		if !domain.ValidWriteVerb(verb) || seen[verb] {
			return nil, false
		}
		seen[verb] = true
		verbs = append(verbs, policyVerb{verb: verb, conditional: conditional})
	}
	return verbs, len(verbs) != 0
}

func parsePolicyIdentity(value string) (policyIdentitySource, bool) {
	identity := policyIdentitySource(value)
	switch identity {
	case policyIdentityNone, policyIdentityJiraIssueArg, policyIdentityJiraProjectFlag,
		policyIdentityJiraTwoIssueArgs, policyIdentityJiraLinkID, policyIdentityJiraLinkEndpoints, policyIdentityJiraPlan,
		policyIdentityJiraSprintIssues, policyIdentityJiraMirror,
		policyIdentityConfluencePageFlag, policyIdentityConfluencePageArg,
		policyIdentityConfluenceSpace, policyIdentityConfluencePlan, policyIdentityConfluenceMirror:
		return identity, true
	default:
		return "", false
	}
}

func parseMutationGuardRequirement(value string) (mutationGuardRequirement, bool) {
	switch value {
	case "apply":
		return mutationGuardApply, true
	case "confirm":
		return mutationGuardConfirm, true
	case "expected-proposal-hash":
		return mutationGuardExpectedProposalHash, true
	case "expected-version":
		return mutationGuardExpectedVersion, true
	case "expected-parent":
		return mutationGuardExpectedParent, true
	case "expected-updated":
		return mutationGuardExpectedUpdated, true
	case "expected-backend-sha256":
		return mutationGuardExpectedBackendSHA256, true
	case "from-file":
		return mutationGuardFromFile, true
	case "suggestion-hash":
		return mutationGuardSuggestionHash, true
	case "candidate-hash":
		return mutationGuardCandidateHash, true
	case "expected-current-hash":
		return mutationGuardExpectedCurrentHash, true
	case "expected-plan-digest":
		return mutationGuardExpectedPlanDigest, true
	default:
		return 0, false
	}
}

func mutationGuardRequirementName(requirement mutationGuardRequirement) (string, bool) {
	switch requirement {
	case mutationGuardApply:
		return "apply", true
	case mutationGuardConfirm:
		return "confirm", true
	case mutationGuardExpectedProposalHash:
		return "expected-proposal-hash", true
	case mutationGuardExpectedVersion:
		return "expected-version", true
	case mutationGuardExpectedParent:
		return "expected-parent", true
	case mutationGuardExpectedUpdated:
		return "expected-updated", true
	case mutationGuardExpectedBackendSHA256:
		return "expected-backend-sha256", true
	case mutationGuardFromFile:
		return "from-file", true
	case mutationGuardSuggestionHash:
		return "suggestion-hash", true
	case mutationGuardCandidateHash:
		return "candidate-hash", true
	case mutationGuardExpectedCurrentHash:
		return "expected-current-hash", true
	case mutationGuardExpectedPlanDigest:
		return "expected-plan-digest", true
	default:
		return "", false
	}
}

func mutationGuardRequirementPresence(requirement mutationGuardRequirement) (mutationGuardPresence, bool) {
	switch requirement {
	case mutationGuardApply:
		return mutationGuardPresenceTrue, true
	case mutationGuardExpectedParent:
		return mutationGuardPresenceExplicit, true
	case mutationGuardConfirm,
		mutationGuardExpectedProposalHash,
		mutationGuardExpectedVersion,
		mutationGuardExpectedUpdated,
		mutationGuardExpectedBackendSHA256,
		mutationGuardFromFile,
		mutationGuardSuggestionHash,
		mutationGuardCandidateHash,
		mutationGuardExpectedCurrentHash,
		mutationGuardExpectedPlanDigest:
		return mutationGuardPresenceNonBlank, true
	default:
		return 0, false
	}
}

func mutationGuardRequirementNames(requirements []mutationGuardRequirement) []string {
	if len(requirements) == 0 {
		return nil
	}
	names := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		name, ok := mutationGuardRequirementName(requirement)
		if !ok {
			continue
		}
		names = append(names, name)
	}
	return names
}

func parseMutationGuardPhase(value string) (mutationGuardPhase, bool) {
	switch value {
	case "command":
		return mutationGuardCommandOwned, true
	case "pre-config":
		return mutationGuardPreConfig, true
	case "pre-config-on-apply":
		return mutationGuardPreConfigOnApply, true
	default:
		return 0, false
	}
}

func parseMutationGuardFamily(value string) (mutationGuardFamily, bool) {
	switch value {
	case "generic":
		return mutationGuardGeneric, true
	case "confluence-attachment-delete":
		return mutationGuardConfluenceAttachmentDelete, true
	case "confluence-page-copy":
		return mutationGuardConfluencePageCopy, true
	case "confluence-page-delete":
		return mutationGuardConfluencePageDelete, true
	case "jira-issue-delete":
		return mutationGuardJiraIssueDelete, true
	case "jira-description-edit":
		return mutationGuardJiraDescriptionEdit, true
	case "jira-guarded-link":
		return mutationGuardJiraGuardedLink, true
	case "jira-guarded-create":
		return mutationGuardJiraGuardedCreate, true
	case "jira-guarded-labels":
		return mutationGuardJiraGuardedLabels, true
	case "jira-guarded-comment":
		return mutationGuardJiraGuardedComment, true
	case "jira-guarded-field":
		return mutationGuardJiraGuardedField, true
	case "jira-plan":
		return mutationGuardJiraPlan, true
	default:
		return 0, false
	}
}

func parseCommandOutputModes(value string) (commandOutputMode, bool) {
	switch value {
	case "json":
		return commandOutputJSON, true
	case "json,text":
		return commandOutputJSON | commandOutputText, true
	case "json,id":
		return commandOutputJSON | commandOutputID, true
	case "json,text,id":
		return commandOutputJSON | commandOutputText | commandOutputID, true
	default:
		return 0, false
	}
}

func commandOutputModeNames(modes commandOutputMode) []string {
	var out []string
	if modes&commandOutputJSON != 0 {
		out = append(out, "json")
	}
	if modes&commandOutputText != 0 {
		out = append(out, "text")
	}
	if modes&commandOutputID != 0 {
		out = append(out, "id")
	}
	return out
}
