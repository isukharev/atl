package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	capabilitydef "github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

const (
	accessAnnotation          = "atl.access"
	commandRoleAnnotation     = "atl.command.role"
	effectProfileAnnotation   = "atl.effect.profile"
	mutationProfileAnnotation = "atl.mutation.profile"
	commandRoleGroup          = "group"
	commandRoleLeaf           = "leaf"
	commandRoleHybrid         = "hybrid"
)

type commandTrait uint8

const (
	commandGroup commandTrait = 1 << iota
	commandLeaf
	commandMutating
)

type commandOutputMode uint8

const (
	commandOutputJSON commandOutputMode = 1 << iota
	commandOutputText
	commandOutputID
)

type mutationProfile string

const (
	mutationNone           mutationProfile = ""
	mutationLocalDirect    mutationProfile = "local-direct"
	mutationRemoteDirect   mutationProfile = "remote-direct"
	mutationPreviewApply   mutationProfile = "preview-apply"
	mutationDedicatedApply mutationProfile = "dedicated-apply"
	mutationPlan           mutationProfile = "plan"
)

type mutationGuardRequirement uint8

const (
	mutationGuardApply mutationGuardRequirement = iota + 1
	mutationGuardConfirm
	mutationGuardExpectedProposalHash
	mutationGuardExpectedVersion
	mutationGuardExpectedParent
	mutationGuardExpectedUpdated
	mutationGuardExpectedBackendSHA256
	mutationGuardFromFile
	mutationGuardSuggestionHash
	mutationGuardCandidateHash
	mutationGuardExpectedCurrentHash
	mutationGuardExpectedPlanDigest
)

type mutationGuardPresence uint8

const (
	mutationGuardPresenceTrue mutationGuardPresence = iota + 1
	mutationGuardPresenceNonBlank
	mutationGuardPresenceExplicit
)

type mutationGuardPhase uint8

const (
	mutationGuardCommandOwned mutationGuardPhase = iota + 1
	mutationGuardPreConfig
	mutationGuardPreConfigOnApply
)

type mutationGuardFamily uint8

const (
	mutationGuardGeneric mutationGuardFamily = iota + 1
	mutationGuardConfluenceAttachmentDelete
	mutationGuardConfluencePageCopy
	mutationGuardConfluencePageDelete
	mutationGuardJiraIssueDelete
	mutationGuardJiraDescriptionEdit
	mutationGuardJiraGuardedLink
	mutationGuardJiraGuardedCreate
	mutationGuardJiraGuardedLabels
	mutationGuardJiraGuardedComment
	mutationGuardJiraGuardedField
	mutationGuardJiraPlan
)

type mutationGuardSpec struct {
	requirements []mutationGuardRequirement
	phase        mutationGuardPhase
	family       mutationGuardFamily
}

type commandRegistration struct {
	traits         commandTrait
	effectProfile  string
	profile        mutationProfile
	guard          mutationGuardSpec
	outputModes    commandOutputMode
	policyVerbs    []policyVerb
	policyIdentity policyIdentitySource
}

type policyIdentitySource string

const (
	policyIdentityNone               policyIdentitySource = "none"
	policyIdentityJiraIssueArg       policyIdentitySource = "jira-issue-arg"
	policyIdentityJiraProjectFlag    policyIdentitySource = "jira-project-flag"
	policyIdentityJiraTwoIssueArgs   policyIdentitySource = "jira-two-issue-args"
	policyIdentityJiraLinkID         policyIdentitySource = "jira-link-id"
	policyIdentityJiraLinkEndpoints  policyIdentitySource = "jira-link-endpoints"
	policyIdentityJiraPlan           policyIdentitySource = "jira-plan"
	policyIdentityJiraSprintIssues   policyIdentitySource = "jira-sprint-issues"
	policyIdentityJiraMirror         policyIdentitySource = "jira-mirror"
	policyIdentityConfluencePageFlag policyIdentitySource = "confluence-page-flag"
	policyIdentityConfluencePageArg  policyIdentitySource = "confluence-page-arg"
	policyIdentityConfluenceSpace    policyIdentitySource = "confluence-space"
	policyIdentityConfluencePlan     policyIdentitySource = "confluence-plan"
	policyIdentityConfluenceMirror   policyIdentitySource = "confluence-mirror"
)

type policyVerb struct {
	verb        domain.WriteVerb
	conditional bool
}

type commandRegistryState struct {
	nodes map[string]commandRegistration
}

type readOnlyPolicyError struct{ Command string }

func (e *readOnlyPolicyError) Error() string {
	return fmt.Sprintf("read-only policy blocks mutating command %q; remove the policy only after explicit human approval", e.Command)
}

func (e *readOnlyPolicyError) Unwrap() error { return domain.ErrCheckFailed }

func (e *readOnlyPolicyError) DiagnosticReadOnlyPolicy() bool { return e != nil }

func readOnlyErrorMetadata(err error) (string, bool) {
	var policyErr *readOnlyPolicyError
	if errors.As(err, &policyErr) {
		return policyErr.Command, true
	}
	return "", false
}

type accessPolicyInvariantError struct{ Command string }

func (e *accessPolicyInvariantError) Error() string {
	return fmt.Sprintf("command %q has no access-policy classification", e.Command)
}

func (e *accessPolicyInvariantError) Unwrap() error { return domain.ErrCheckFailed }

func accessPolicyInvariantMetadata(err error) (string, bool) {
	var invariantErr *accessPolicyInvariantError
	if errors.As(err, &invariantErr) {
		return invariantErr.Command, true
	}
	return "", false
}

// commandRegistry is the single reviewed command contract. Read-only rows use
// "R <effect-profile> <output-modes> <path>". Mutating rows declare
// "M <effect-profile> <mutation-profile> <verbs> <identity-source>" before
// their mutation guard. Local
// commands use the explicit "none none" pair. Unguarded rows then use "-";
// guarded rows declare requirements, phase, and family. Output modes are
// explicit and canonical: json, json,text, json,id, or
// json,text,id. Parent groups are derived from path prefixes, so the finalized
// Cobra tree is checked bidirectionally for groups, leaves, and the two
// intentional hybrids.
var commandRegistry, commandRegistryErr = parseCommandRegistry(`
M setup local-direct none none - json,text auth login
M credential-write local-direct none none - json auth logout
R credential-read json,text auth status
R pure json,text,id capabilities
M local-write-updatable local-direct none none - json,text conf apply
M remote-write preview-apply delete confluence-page-flag apply,confirm,expected-proposal-hash,expected-version pre-config confluence-attachment-delete json conf attachment delete
R remote-download json,text conf attachment get
R remote-read json,text,id conf attachment list
R remote-read-caller-bounded json,text,id conf attachment search
M remote-write-with-local remote-direct create confluence-page-flag - json conf attachment upload
M remote-write-with-local remote-direct create confluence-space - json,text,id conf blog create
M remote-write-with-local preview-apply comment confluence-page-arg apply,expected-proposal-hash command generic json,text conf comment add
R remote-read-capped json,text conf comment list
M remote-write-with-local dedicated-apply comment confluence-page-flag apply,expected-proposal-hash pre-config generic json conf comment mutation apply
R remote-read-with-local json conf comment mutation preview
R remote-read-with-local json,text conf comment preview
R remote-read-capped json,text conf comment thread
R local-read-updatable json,text conf diff
M local-write-updatable local-direct none none - json,text conf edit
R remote-read json,text conf me
M remote-write-local preview-apply create confluence-space apply,expected-proposal-hash,expected-version pre-config confluence-page-copy json,id conf page copy
M remote-write-local remote-direct create confluence-space - json conf page create
M remote-write preview-apply delete confluence-page-flag apply,confirm,expected-proposal-hash,expected-version pre-config confluence-page-delete json conf page delete
R remote-read json,text conf page get
R remote-read json,text conf page history
M remote-write preview-apply update confluence-page-arg apply,expected-proposal-hash command generic json,text conf page labels add
R remote-read json,text conf page labels list
M remote-write preview-apply update confluence-page-arg apply,expected-proposal-hash command generic json,text conf page labels remove
R remote-read json,text,id conf page list
R remote-read json,text conf page meta
M remote-write preview-apply update,move confluence-page-arg apply,expected-proposal-hash,expected-version,expected-parent command generic json,text conf page move
R remote-open json,text conf page open
R remote-read-fixed json,text conf page outline
R remote-read json,text,id conf page resolve
R remote-read-fixed json,text conf page section
R remote-read-fixed json,text conf page sections
M remote-write-with-local preview-apply update confluence-page-arg apply,expected-proposal-hash,expected-version command generic json,text conf page title set
R remote-read json,text conf page view
M remote-write-local plan update confluence-plan confirm,expected-proposal-hash command generic json,text conf plan apply
R local-write-updatable json,text conf plan create
R remote-read-with-local json,text conf plan preview
R remote-pull json,text conf pull
M remote-write-local remote-direct update confluence-mirror - json,text conf push
R remote-read-with-local json,text conf reconcile preview
M local-write-updatable local-direct none none - json,text conf reconcile stage
R local-write-updatable json,text conf render
R remote-read-fixed json,text,id conf search
R optional-remote-read json,text conf snapshot
R remote-read-caller-bounded json,text conf space tree
R optional-remote-read json,text conf status
R remote-read-local json,text conf table extract
R remote-read json,text conf table summary
R local-read-updatable json conf validate
M local-write local-direct none none - json compatibility clear
M local-write local-direct none none - json compatibility pin
R diagnostic json,text compatibility status
R generator json,text completion bash
R generator json,text completion fish
R generator json,text completion powershell
R generator json,text completion zsh
M config-write local-direct none none - json config set
R config-read json,text config show
R corpus-build json,text corpus build
R local-artifact json,text corpus cache retention preview
M local-write dedicated-apply none none apply,expected-plan-digest pre-config generic json,text corpus cache retention apply
R local-read json,text corpus cache status
R local-read-optional-artifact json,text corpus diff
R local-artifact json,text corpus export
R local-read-optional-artifact json,text corpus handoff
R diagnostic json,text doctor
R diagnostic json,text environment inspect
R prose json,text help
M local-write-updatable local-direct none none - json,text jira apply
R remote-pull json,text jira attachment-bodies
R remote-read json,text,id jira board backlog
R remote-read json,text,id jira board config
R remote-read-local json,text jira board export
R remote-read json,text,id jira board get
R remote-read json,text,id jira board issues
R remote-read json,text,id jira board list
R remote-read-capped json,text,id jira board view
R remote-read-capped json,text jira epic digest
R remote-read-local json,text jira export
R local-read-updatable json,text jira export diff
R remote-read json,text jira field-options
R remote-read json,text jira fields
M remote-write remote-direct update jira-issue-arg - json,text jira issue assign
R remote-download json,text jira issue attachment get
R remote-read json,text,id jira issue attachment list
M remote-write-with-local remote-direct create jira-issue-arg - json jira issue attachment upload
R remote-read json,text jira issue check
R remote-read json,text,id jira issue children
M remote-write-with-local preview-apply comment jira-issue-arg apply,expected-proposal-hash pre-config jira-guarded-comment json,text jira issue comment add
M remote-write remote-direct delete jira-issue-arg - json jira issue comment delete
R remote-read json,text,id jira issue comment list
R remote-read-with-local json,text jira issue comment preview
M guarded-create-apply preview-apply create? jira-project-flag apply,expected-proposal-hash pre-config jira-guarded-create json,id jira issue create
R guarded-create-preview json jira issue create preview
R remote-read json,text jira issue create-check
R remote-read json,text jira issue create-metadata
M remote-write preview-apply delete jira-issue-arg apply,confirm,expected-proposal-hash,expected-updated pre-config jira-issue-delete json jira issue delete
M remote-write-with-local preview-apply update,move? jira-issue-arg apply,expected-proposal-hash pre-config jira-description-edit json,text jira issue edit
R remote-read-with-local json,text jira issue edit preview
R remote-read json,text jira issue field get
R guarded-field-preview json,text jira issue field preview
M guarded-field-apply preview-apply update jira-issue-arg apply,expected-proposal-hash,expected-updated pre-config jira-guarded-field json,text jira issue field set
R remote-read json,text jira issue fields
R remote-read json,text jira issue get
R remote-read-caller-bounded json,text jira issue graph
R remote-read json,text jira issue history
R remote-download json jira issue images
M remote-write preview-apply update jira-issue-arg apply,expected-proposal-hash pre-config jira-guarded-labels json jira issue labels
R remote-read-fixed json jira issue labels preview
M remote-write preview-apply update jira-link-endpoints apply,expected-proposal-hash pre-config jira-guarded-link json jira issue link add
R remote-read json jira issue link add preview
M remote-write preview-apply delete jira-link-endpoints apply,expected-proposal-hash pre-config jira-guarded-link json jira issue link delete
R remote-read json jira issue link delete preview
R remote-read json,text,id jira issue link list
R remote-read-with-local json,text jira issue link suggest
M remote-write remote-direct update jira-two-issue-args - json jira issue link-epic
M guarded-plan-apply dedicated-apply update,comment jira-plan confirm,expected-proposal-hash pre-config jira-plan json,text jira issue plan apply
R guarded-plan-preview json,text jira-plan jira issue plan preview
R remote-read json,text jira issue refs
R remote-read-caller-bounded json,text jira issue reference search
R remote-read-fixed json,text,id jira issue search
M remote-write preview-apply transition,comment? jira-issue-arg apply,expected-proposal-hash command generic json,text jira issue transition
R remote-read json,text jira issue transition preview
R remote-read json,text jira issue tree
R remote-read json,text,id jira issue types
M remote-write-with-local remote-direct update,move? jira-issue-arg - json jira issue update
R remote-read json,text jira issue view
M remote-write preview-apply update jira-issue-arg apply,expected-proposal-hash command generic json,text jira issue watchers add
R remote-read json,text jira issue watchers list
M remote-write preview-apply update jira-issue-arg apply,expected-proposal-hash command generic json,text jira issue watchers remove
M remote-write-with-local preview-apply update jira-issue-arg apply,expected-proposal-hash command generic json,text jira issue worklog add
R remote-read json,text,id jira issue worklog list
R remote-read json,text jira link-types
R remote-read json,text,id jira me
R remote-read-local json,text jira planning report
R remote-read json,text,id jira project list
R remote-pull json,text jira pull
M remote-write-local preview-apply update jira-mirror apply command generic json,text jira push
R remote-read-with-local json,text jira reconcile preview
M local-write-updatable local-direct none none - json,text jira reconcile stage
R remote-read-local json,text jira quality-report
R local-write-updatable json,text jira render
M remote-write remote-direct update jira-sprint-issues - json jira sprint add
R remote-read json,text,id jira sprint current
R remote-read json,text,id jira sprint get
R remote-read json,text,id jira sprint issues
R remote-read json,text,id jira sprint list
M remote-write remote-direct update jira-sprint-issues - json jira sprint remove
R optional-remote-read json,text jira snapshot
R optional-remote-read json,text jira status
R remote-read-local json,text jira structure export
R remote-read json,text,id jira structure folders
R remote-read json,text jira structure forest
R remote-read json,text,id jira structure get
R remote-read json,text,id jira structure pull-issues
R remote-read json,text,id jira structure rows
R remote-read json jira structure values
R remote-read json,text,id jira structure view
R remote-read json,text jira transitions
R remote-read json,text,id jira user get
R remote-read json,text,id jira user search
R local-write-updatable json,text manifest create
R stdio-server json mcp serve
M local-write preview-apply none none apply,expected-backend-sha256,confirm pre-config-on-apply generic json,text mirror backend bind
R local-read json,text mirror backend status
M local-write dedicated-apply none none from-file,candidate-hash,expected-current-hash pre-config generic json,text profile apply
R local-prose json,text profile guidance
R local-read json,text profile preview
M local-artifact-config-read local-direct none none - json,text profile revalidate
R local-read json,text profile revalidation status
R local-read json,text profile show
M local-artifact-config-read local-direct none none - json,text profile suggest
M local-write dedicated-apply none none from-file,suggestion-hash,candidate-hash,expected-current-hash pre-config generic json,text profile suggestion apply
M local-write local-direct none none - json,text profile suggestion reject
R local-read json,text profile suggestion review
R config-read json,text policy show
R config-read json,text policy explain
R pure json,text version
`)

func parseCommandRegistry(value string) (commandRegistryState, error) {
	registry := commandRegistryState{nodes: map[string]commandRegistration{"": {traits: commandGroup}}}
	for lineNumber, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		var registration commandRegistration
		registration.policyIdentity = policyIdentityNone
		var pathFields []string
		switch {
		case len(fields) >= 4 && fields[0] == "R":
			registration.traits = commandLeaf
			registration.effectProfile = fields[1]
			if _, ok := capabilitydef.EffectProfileByID(registration.effectProfile); !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid effect profile %q", lineNumber+1, fields[1])
			}
			var ok bool
			registration.outputModes, ok = parseCommandOutputModes(fields[2])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid output modes %q", lineNumber+1, fields[2])
			}
			pathFields = fields[3:]
			if len(fields) >= 5 {
				if identity, identityOK := parsePolicyIdentity(fields[3]); identityOK && identity != policyIdentityNone {
					registration.policyIdentity = identity
					pathFields = fields[4:]
				}
			}
		case len(fields) >= 8 && fields[0] == "M":
			registration.traits = commandLeaf | commandMutating
			registration.effectProfile = fields[1]
			if _, ok := capabilitydef.EffectProfileByID(registration.effectProfile); !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid effect profile %q", lineNumber+1, fields[1])
			}
			registration.profile = mutationProfile(fields[2])
			if !validMutationProfile(registration.profile) {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid mutation profile %q", lineNumber+1, fields[2])
			}
			var ok bool
			registration.policyVerbs, ok = parsePolicyVerbs(fields[3])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid policy verbs %q", lineNumber+1, fields[3])
			}
			registration.policyIdentity, ok = parsePolicyIdentity(fields[4])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid policy identity %q", lineNumber+1, fields[4])
			}
			if (len(registration.policyVerbs) == 0) != (registration.policyIdentity == policyIdentityNone) {
				return commandRegistryState{}, fmt.Errorf("registry line %d must declare policy verbs and identity together, or none none", lineNumber+1)
			}
			outputIndex := 6
			pathIndex := 7
			if fields[5] != "-" {
				if len(fields) < 10 {
					return commandRegistryState{}, fmt.Errorf("registry line %d guarded mutation has invalid shape", lineNumber+1)
				}
				for _, name := range strings.Split(fields[5], ",") {
					requirement, ok := parseMutationGuardRequirement(name)
					if !ok {
						return commandRegistryState{}, fmt.Errorf("registry line %d has invalid guard requirement %q", lineNumber+1, name)
					}
					registration.guard.requirements = append(registration.guard.requirements, requirement)
				}
				var ok bool
				registration.guard.phase, ok = parseMutationGuardPhase(fields[6])
				if !ok {
					return commandRegistryState{}, fmt.Errorf("registry line %d has invalid guard phase %q", lineNumber+1, fields[6])
				}
				registration.guard.family, ok = parseMutationGuardFamily(fields[7])
				if !ok {
					return commandRegistryState{}, fmt.Errorf("registry line %d has invalid guard family %q", lineNumber+1, fields[7])
				}
				outputIndex = 8
				pathIndex = 9
			}
			registration.outputModes, ok = parseCommandOutputModes(fields[outputIndex])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid output modes %q", lineNumber+1, fields[outputIndex])
			}
			pathFields = fields[pathIndex:]
		default:
			return commandRegistryState{}, fmt.Errorf("registry line %d has invalid shape", lineNumber+1)
		}
		path := strings.Join(pathFields, " ")
		if path == "" {
			return commandRegistryState{}, fmt.Errorf("registry line %d has an empty command path", lineNumber+1)
		}
		if existing, duplicate := registry.nodes[path]; duplicate && existing.traits&commandLeaf != 0 {
			return commandRegistryState{}, fmt.Errorf("registry line %d duplicates command %q", lineNumber+1, path)
		}
		if registration.traits&commandMutating != 0 {
			hasApply := false
			for _, requirement := range registration.guard.requirements {
				hasApply = hasApply || requirement == mutationGuardApply
			}
			switch registration.profile {
			case mutationPreviewApply:
				if !hasApply {
					return commandRegistryState{}, fmt.Errorf("registry line %d preview-apply profile does not require --apply", lineNumber+1)
				}
			case mutationDedicatedApply, mutationPlan:
				if len(registration.guard.requirements) == 0 {
					return commandRegistryState{}, fmt.Errorf("registry line %d %s profile has no required guard", lineNumber+1, registration.profile)
				}
			case mutationLocalDirect, mutationRemoteDirect:
				if len(registration.guard.requirements) != 0 {
					return commandRegistryState{}, fmt.Errorf("registry line %d %s profile unexpectedly declares a guard", lineNumber+1, registration.profile)
				}
			}
			if len(registration.guard.requirements) != 0 {
				if registration.guard.family != mutationGuardGeneric && registration.guard.phase != mutationGuardPreConfig {
					return commandRegistryState{}, fmt.Errorf("registry line %d specialized guard family must run pre-config", lineNumber+1)
				}
				if registration.guard.phase == mutationGuardPreConfigOnApply && !hasApply {
					return commandRegistryState{}, fmt.Errorf("registry line %d apply-only guard phase has no --apply requirement", lineNumber+1)
				}
			}
		}
		registration.traits |= registry.nodes[path].traits & commandGroup
		registry.nodes[path] = registration
		parts := strings.Fields(path)
		for i := 0; i < len(parts); i++ {
			parent := strings.Join(parts[:i], " ")
			entry := registry.nodes[parent]
			entry.traits |= commandGroup
			registry.nodes[parent] = entry
		}
	}
	return registry, nil
}

func commandRegistryPath(root, cmd *cobra.Command) string {
	if cmd == root {
		return ""
	}
	return strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
}

func actualCommandTraits(cmd *cobra.Command) commandTrait {
	var traits commandTrait
	if len(cmd.Commands()) > 0 {
		traits |= commandGroup
	}
	if cmd.Run != nil || cmd.RunE != nil {
		traits |= commandLeaf
	}
	return traits
}

func finalizeCommandTree(root *cobra.Command) error {
	if commandRegistryErr != nil {
		return commandRegistryErr
	}
	seen := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		path := commandRegistryPath(root, cmd)
		registration, exists := commandRegistry.nodes[path]
		if !exists {
			return
		}
		seen[path] = true
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		switch registration.traits & (commandGroup | commandLeaf) {
		case commandGroup:
			cmd.Annotations[commandRoleAnnotation] = commandRoleGroup
		case commandLeaf:
			cmd.Annotations[commandRoleAnnotation] = commandRoleLeaf
		case commandGroup | commandLeaf:
			cmd.Annotations[commandRoleAnnotation] = commandRoleHybrid
		}
		if registration.traits&commandLeaf != 0 {
			cmd.Annotations[effectProfileAnnotation] = registration.effectProfile
			classifyOutputModes(cmd, registration.outputModes)
			if registration.traits&commandMutating != 0 {
				cmd.Annotations[accessAnnotation] = "mutating"
				cmd.Annotations[mutationProfileAnnotation] = string(registration.profile)
			} else {
				cmd.Annotations[accessAnnotation] = "read-only"
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	var validate func(*cobra.Command) error
	validate = func(cmd *cobra.Command) error {
		path := commandRegistryPath(root, cmd)
		registration, exists := commandRegistry.nodes[path]
		if !exists {
			return fmt.Errorf("command %q is missing from the registry", cmd.CommandPath())
		}
		want := registration.traits & (commandGroup | commandLeaf)
		if got := actualCommandTraits(cmd); got != want {
			return fmt.Errorf("command %q topology is %d, registry requires %d", cmd.CommandPath(), got, want)
		}
		if registration.traits&commandMutating != 0 {
			if registration.profile == mutationNone {
				return fmt.Errorf("mutating command %q has no mutation profile", cmd.CommandPath())
			}
			for _, flag := range mutationGuardRequirementNames(registration.guard.requirements) {
				if cmd.Flags().Lookup(flag) == nil {
					return fmt.Errorf("mutating command %q profile %q requires missing --%s flag", cmd.CommandPath(), registration.profile, flag)
				}
			}
		} else if registration.profile != mutationNone || len(registration.guard.requirements) != 0 {
			return fmt.Errorf("read-only command %q has mutation metadata", cmd.CommandPath())
		}
		if registration.traits&commandLeaf != 0 {
			if _, ok := capabilitydef.EffectProfileByID(registration.effectProfile); !ok {
				return fmt.Errorf("executable command %q has no valid effect profile", cmd.CommandPath())
			}
		}
		for _, child := range cmd.Commands() {
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(root); err != nil {
		return err
	}
	for path := range commandRegistry.nodes {
		if !seen[path] {
			return fmt.Errorf("registry command %q is not constructed", path)
		}
	}
	installPureGroupFallbacks(root)
	return nil
}
func installPureGroupFallbacks(cmd *cobra.Command) {
	if cmd.Annotations[commandRoleAnnotation] == commandRoleGroup {
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return nil
		}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	}
	for _, child := range cmd.Commands() {
		installPureGroupFallbacks(child)
	}
}
func validateMutationInvocation(cmd *cobra.Command) error {
	path := commandRegistryPath(cmd.Root(), cmd)
	if handled, err := validateGuardedPreviewInvocation(cmd, path); handled {
		return err
	}
	registration, ok := commandRegistry.nodes[path]
	if !ok || registration.traits&commandMutating == 0 {
		return nil
	}
	guard := registration.guard
	if len(guard.requirements) == 0 || guard.phase == mutationGuardCommandOwned {
		return nil
	}
	applyFlag := cmd.Flags().Lookup("apply")
	applyRequested := false
	if applyFlag != nil {
		value, err := cmd.Flags().GetBool("apply")
		if err != nil {
			return usageErr("invalid --apply value")
		}
		applyRequested = value
	}
	if guard.phase == mutationGuardPreConfigOnApply && !applyRequested {
		return nil
	}
	if guard.phase == mutationGuardPreConfig && registration.profile == mutationPreviewApply && !applyRequested {
		return validateMutationGuardFamily(cmd, guard.family, false)
	}
	// The guarded-field family owns the reviewed prerequisite ordering and exact
	// diagnostics for its multi-input apply contract.
	if guard.family == mutationGuardJiraGuardedField {
		return validateMutationGuardFamily(cmd, guard.family, applyRequested)
	}
	for _, requirement := range guard.requirements {
		name, ok := mutationGuardRequirementName(requirement)
		if !ok {
			return &accessPolicyInvariantError{Command: fmt.Sprintf("%s has invalid mutation guard requirement", cmd.CommandPath())}
		}
		presence, ok := mutationGuardRequirementPresence(requirement)
		if !ok {
			return &accessPolicyInvariantError{Command: fmt.Sprintf("%s has invalid mutation guard requirement presence", cmd.CommandPath())}
		}
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			return &accessPolicyInvariantError{Command: fmt.Sprintf("%s missing --%s", cmd.CommandPath(), name)}
		}
		if registration.profile == mutationPreviewApply && requirement == mutationGuardApply {
			continue
		}
		missing := !flag.Changed
		switch presence {
		case mutationGuardPresenceTrue:
			value, err := cmd.Flags().GetBool(name)
			missing = err != nil || !value
		case mutationGuardPresenceNonBlank:
			missing = missing || strings.TrimSpace(flag.Value.String()) == ""
		case mutationGuardPresenceExplicit:
			// An explicitly supplied empty value is meaningful for guards such as
			// --expected-parent, so flag presence alone satisfies the contract.
		default:
			return &accessPolicyInvariantError{Command: fmt.Sprintf("%s has invalid mutation guard presence", cmd.CommandPath())}
		}
		if missing {
			if registration.profile == mutationPreviewApply {
				if guard.family == mutationGuardJiraGuardedComment && requirement == mutationGuardExpectedProposalHash {
					return usageErr("--expected-proposal-hash is required with --apply; run the dry-run first")
				}
				return usageErr("--%s is required with --apply", name)
			}
			return usageErr("--%s is required for this apply command", name)
		}
	}
	return validateMutationGuardFamily(cmd, guard.family, applyRequested)
}
func validateMutationGuardFamily(cmd *cobra.Command, family mutationGuardFamily, applyRequested bool) error {
	switch family {
	case mutationGuardGeneric:
		return nil
	case mutationGuardConfluenceAttachmentDelete:
		return validateConfluenceAttachmentDeleteInvocation(cmd, applyRequested)
	case mutationGuardConfluencePageCopy:
		return validateConfluencePageCopyInvocation(cmd, applyRequested)
	case mutationGuardConfluencePageDelete:
		return validateConfluencePageDeleteInvocation(cmd, applyRequested)
	case mutationGuardJiraIssueDelete:
		return validateJiraIssueDeleteInvocation(cmd, applyRequested)
	case mutationGuardJiraDescriptionEdit:
		return validateJiraDescriptionEditInvocation(cmd, applyRequested)
	case mutationGuardJiraGuardedLink:
		return validateJiraGuardedLinkInvocation(cmd, applyRequested)
	case mutationGuardJiraGuardedCreate:
		return validateJiraGuardedCreateInvocation(cmd, applyRequested)
	case mutationGuardJiraGuardedLabels:
		return validateJiraGuardedLabelInvocation(cmd, applyRequested)
	case mutationGuardJiraGuardedComment:
		return validateJiraGuardedCommentInvocation(cmd, applyRequested)
	case mutationGuardJiraGuardedField:
		return validateJiraGuardedFieldInvocation(cmd, applyRequested)
	case mutationGuardJiraPlan:
		return validateJiraPlanApplyInvocation(cmd)
	default:
		return &accessPolicyInvariantError{Command: fmt.Sprintf("%s has invalid mutation guard family", cmd.CommandPath())}
	}
}
func validateJiraDescriptionEditInvocation(cmd *cobra.Command, applyRequested bool) error {
	if dryRun := cmd.Flags().Lookup("dry-run"); dryRun != nil && dryRun.Changed {
		value, err := cmd.Flags().GetBool("dry-run")
		if err != nil || !value {
			return usageErr("--dry-run=false is not supported; omit --dry-run to preview")
		}
		if applyRequested {
			return usageErr("--dry-run cannot be combined with --apply")
		}
	}
	expected := cmd.Flags().Lookup("expected-proposal-hash")
	if !applyRequested {
		if expected != nil && expected.Changed {
			return usageErr("--expected-proposal-hash requires --apply")
		}
		return nil
	}
	if expected == nil {
		return &accessPolicyInvariantError{Command: fmt.Sprintf("%s missing --expected-proposal-hash", cmd.CommandPath())}
	}
	return app.ValidateJiraDescriptionEditReviewHash(expected.Value.String())
}
func validateJiraIssueDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	key := cmd.Flags().Arg(0)
	if !domain.ValidJiraIssueKey(key) {
		return usageErr("issue key must be canonical (for example PROJ-1)")
	}
	if invocationRuntimeFor(cmd).outputFormat == "id" {
		return usageErr("-o id is not supported for this command")
	}
	guardNames := []string{"confirm", "expected-updated", "expected-proposal-hash"}
	if !applyRequested {
		for _, name := range guardNames {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				return usageErr("--confirm, --expected-updated, and --expected-proposal-hash require --apply")
			}
		}
		return nil
	}
	confirm, err := cmd.Flags().GetString("confirm")
	if err != nil || confirm != "DELETE" {
		return usageErr("--confirm must be exactly DELETE with --apply")
	}
	expectedUpdated, updatedErr := cmd.Flags().GetString("expected-updated")
	expectedProposalHash, hashErr := cmd.Flags().GetString("expected-proposal-hash")
	if updatedErr != nil || hashErr != nil {
		return usageErr("invalid reviewed deletion markers")
	}
	return app.ValidateJiraIssueDeleteReviewMarkers(expectedUpdated, expectedProposalHash)
}

func validateConfluenceAttachmentDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	pageID, pageErr := cmd.Flags().GetString("page-id")
	attachmentID, attachmentErr := cmd.Flags().GetString("id")
	if pageErr != nil || attachmentErr != nil || strings.TrimSpace(pageID) == "" || strings.TrimSpace(attachmentID) == "" {
		return usageErr("--page-id and --id are required")
	}
	if !domain.ValidConfluenceContentID(pageID) {
		return usageErr("--page-id must be a positive numeric content id")
	}
	if !domain.ValidConfluenceContentID(attachmentID) {
		return usageErr("--id must be a positive numeric attachment id")
	}
	if invocationRuntimeFor(cmd).outputFormat == "id" {
		return usageErr("-o id is not supported for this command")
	}
	guardNames := []string{"confirm", "expected-version", "expected-proposal-hash"}
	if !applyRequested {
		for _, name := range guardNames {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				return usageErr("--confirm, --expected-version, and --expected-proposal-hash require --apply")
			}
		}
		return nil
	}
	confirm, err := cmd.Flags().GetString("confirm")
	if err != nil || confirm != "DELETE" {
		return usageErr("--confirm must be exactly DELETE with --apply")
	}
	expectedVersion, err := cmd.Flags().GetInt("expected-version")
	if err != nil || expectedVersion <= 0 {
		return usageErr("--expected-version is required with --apply; run the dry-run first")
	}
	return nil
}

func validateConfluencePageCopyInvocation(cmd *cobra.Command, applyRequested bool) error {
	id, idErr := cmd.Flags().GetString("id")
	title, titleErr := cmd.Flags().GetString("title")
	if idErr != nil || titleErr != nil || strings.TrimSpace(id) == "" || strings.TrimSpace(title) == "" {
		return usageErr("--id and --title are required")
	}
	register, registerErr := cmd.Flags().GetBool("register")
	into, intoErr := cmd.Flags().GetString("into")
	if registerErr != nil || intoErr != nil || register != (strings.TrimSpace(into) != "") {
		return usageErr("--register and a non-empty --into must be used together")
	}
	if !applyRequested {
		for _, name := range []string{"expected-version", "expected-proposal-hash"} {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				return usageErr("--expected-version and --expected-proposal-hash require --apply")
			}
		}
		if invocationRuntimeFor(cmd).outputFormat == "id" {
			return usageErr("-o id is available only with --apply after the created page id is known")
		}
		return nil
	}
	expectedVersion, err := cmd.Flags().GetInt("expected-version")
	if err != nil || expectedVersion <= 0 {
		return usageErr("--expected-version is required with --apply; run the dry-run first")
	}
	return nil
}

func validateConfluencePageDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	id, err := cmd.Flags().GetString("id")
	if err != nil || strings.TrimSpace(id) == "" {
		return usageErr("--id is required")
	}
	if !domain.ValidConfluenceContentID(id) {
		return usageErr("--id must be a positive numeric content id")
	}
	guardNames := []string{"confirm", "expected-version", "expected-proposal-hash"}
	if !applyRequested {
		for _, name := range guardNames {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				return usageErr("--confirm, --expected-version, and --expected-proposal-hash require --apply")
			}
		}
		return nil
	}
	confirm, err := cmd.Flags().GetString("confirm")
	if err != nil || confirm != "TRASH" {
		return usageErr("--confirm must be exactly TRASH with --apply")
	}
	expectedVersion, err := cmd.Flags().GetInt("expected-version")
	if err != nil || expectedVersion <= 0 {
		return usageErr("--expected-version is required with --apply; run the dry-run first")
	}
	return nil
}

func enforceContentPolicyPreflight(cmd *cobra.Command, args []string, registration commandRegistration) error {
	if len(registration.policyVerbs) == 0 {
		return nil
	}
	verbs := policyPreflightVerbs(cmd, registration.policyVerbs)
	if len(verbs) == 0 {
		return nil
	}
	resolved, err := invocationRuntimeFor(cmd).processPolicy.resolve()
	if err != nil || resolved == nil || len(resolved.Layers) == 0 {
		return err
	}
	if registration.policyIdentity == policyIdentityJiraPlan {
		runtime := invocationRuntimeFor(cmd)
		requests, err := app.JiraPlanDocumentPolicyRequests(runtime.jiraPlanDocument, runtime.jiraPlanCommand)
		if err != nil {
			return err
		}
		for _, request := range requests {
			if denial := contentpolicy.PreflightDeny(resolved.Layers, request); denial != nil {
				return denial
			}
		}
		return nil
	}
	targets, err := policyPreflightTargets(cmd, args, registration.policyIdentity)
	if err != nil || len(targets) == 0 {
		return err
	}
	request := domain.WriteAuthorizationRequest{Verbs: verbs, Targets: targets}
	denial := contentpolicy.PreflightDeny(resolved.Layers, request)
	if denial == nil {
		return nil
	}
	return denial
}

func policyPreflightVerbs(cmd *cobra.Command, specs []policyVerb) domain.WriteVerbSet {
	verbs := make(domain.WriteVerbSet, 0, len(specs))
	for _, spec := range specs {
		if !spec.conditional || policyVerbConditionPresent(cmd, spec.verb) {
			verbs = append(verbs, spec.verb)
		}
	}
	return verbs
}

func policyVerbConditionPresent(cmd *cobra.Command, verb domain.WriteVerb) bool {
	switch verb {
	case domain.WriteVerbComment:
		return policyFlagValue(cmd, "comment") != ""
	case domain.WriteVerbMove:
		for _, name := range []string{"project", "fields", "field"} {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				value := strings.ToLower(flag.Value.String())
				if name == "project" || strings.Contains(value, "project") {
					return true
				}
			}
		}
	case domain.WriteVerbCreate:
		apply, err := cmd.Flags().GetBool("apply")
		return err == nil && apply
	}
	return false
}

func policyFlagValue(cmd *cobra.Command, names ...string) string {
	for _, name := range names {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			if value := strings.TrimSpace(flag.Value.String()); value != "" {
				return value
			}
		}
	}
	return ""
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

func jiraPreflightTargets(kind, ref string) []domain.WriteTarget {
	ref = strings.ToUpper(strings.TrimSpace(ref))
	if !domain.ValidJiraIssueKey(ref) {
		return nil
	}
	project := ref[:strings.IndexByte(ref, '-')]
	return []domain.WriteTarget{{Service: "jira", Kind: kind, Project: project, Key: ref}}
}

func confluencePreflightTarget(kind, ref string) ([]domain.WriteTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if kind == "page" {
			return nil, nil
		}
		return []domain.WriteTarget{{Service: "confluence", Kind: kind}}, nil
	}
	if !domain.ValidConfluenceContentID(ref) {
		return nil, usageErr("mutating Confluence references must use a canonical numeric content id while a content policy is active")
	}
	return []domain.WriteTarget{{Service: "confluence", Kind: kind, ID: ref}}, nil
}

func uniqueWriteTargets(values []domain.WriteTarget) []domain.WriteTarget {
	seen := map[string]bool{}
	out := make([]domain.WriteTarget, 0, len(values))
	for _, value := range values {
		key := value.Service + "\x00" + value.Kind + "\x00" + value.ID + "\x00" + value.Key
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func resolveReadOnlyPolicy(cmd *cobra.Command, flagEnabled bool) (bool, error) {
	if flagEnabled || envReadOnly() {
		return true, nil
	}
	// Offline/trivial reads are incapable of backend/config mutation and are
	// already guaranteed not to self-update. Keep them usable for diagnosis
	// when config.json itself is malformed; every mutator and online read still
	// decodes the policy strictly below.
	if cmd.Annotations[accessAnnotation] == "read-only" && skipSelfUpdate(cmd) && !policyInspectionCommand(cmd) {
		return false, nil
	}
	cfg, err := config.LoadForEdit()
	if err != nil {
		return false, err
	}
	return cfg.ReadOnly, nil
}

func policyInspectionCommand(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Name() == "policy" {
			return true
		}
	}
	return false
}

func enforceAccessPolicy(cmd *cobra.Command, enabled bool) error {
	access := cmd.Annotations[accessAnnotation]
	if access == "unclassified" || access == "" {
		if cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd {
			return nil
		}
		return &accessPolicyInvariantError{Command: cmd.CommandPath()}
	}
	if access != "mutating" {
		return nil
	}
	if registration, ok := commandRegistry.nodes[commandRegistryPath(cmd.Root(), cmd)]; ok &&
		len(registration.policyVerbs) != 0 && len(policyPreflightVerbs(cmd, registration.policyVerbs)) == 0 {
		return nil
	}
	if enabled {
		return &readOnlyPolicyError{Command: cmd.CommandPath()}
	}
	return nil
}
