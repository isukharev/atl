package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

const (
	accessAnnotation          = "atl.access"
	commandRoleAnnotation     = "atl.command.role"
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

type mutationProfile string

const (
	mutationNone           mutationProfile = ""
	mutationLocalDirect    mutationProfile = "local-direct"
	mutationRemoteDirect   mutationProfile = "remote-direct"
	mutationPreviewApply   mutationProfile = "preview-apply"
	mutationDedicatedApply mutationProfile = "dedicated-apply"
	mutationPlan           mutationProfile = "plan"
)

type commandRegistration struct {
	traits        commandTrait
	profile       mutationProfile
	requiredFlags []string
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
// "R <path>". Mutating rows use "M <profile> <required-flags-or-dash> <path>".
// Parent groups are derived from path prefixes, so the finalized Cobra tree is
// checked bidirectionally for groups, leaves, and the two intentional hybrids.
var commandRegistry, commandRegistryErr = parseCommandRegistry(`
M local-direct - auth login
M local-direct - auth logout
R auth status
R capabilities
M local-direct - conf apply
M remote-direct - conf attachment delete
R conf attachment get
R conf attachment list
M remote-direct - conf attachment upload
M remote-direct - conf blog create
M preview-apply apply,expected-proposal-hash conf comment add
R conf comment list
M dedicated-apply apply,expected-proposal-hash conf comment mutation apply
R conf comment mutation preview
R conf comment preview
R conf comment thread
R conf diff
M local-direct - conf edit
R conf me
M remote-direct - conf page copy
M remote-direct - conf page create
M preview-apply apply,confirm,expected-proposal-hash,expected-version conf page delete
R conf page get
R conf page history
M preview-apply apply,expected-proposal-hash conf page labels add
R conf page labels list
M preview-apply apply,expected-proposal-hash conf page labels remove
R conf page list
R conf page meta
M preview-apply apply,expected-proposal-hash,expected-version,expected-parent conf page move
R conf page open
R conf page outline
R conf page resolve
R conf page section
R conf page sections
M preview-apply apply,expected-proposal-hash,expected-version conf page title set
R conf page view
M plan confirm,expected-proposal-hash conf plan apply
R conf plan create
R conf plan preview
R conf pull
M remote-direct - conf push
R conf reconcile preview
M local-direct - conf reconcile stage
R conf render
R conf search
R conf snapshot
R conf space tree
R conf status
R conf table extract
R conf table summary
R conf validate
M local-direct - compatibility clear
M local-direct - compatibility pin
R compatibility status
R completion bash
R completion fish
R completion powershell
R completion zsh
M local-direct - config set
R config show
R doctor
R environment inspect
R help
M local-direct - jira apply
R jira board backlog
R jira board config
R jira board export
R jira board get
R jira board issues
R jira board list
R jira board view
R jira epic digest
R jira export
R jira export diff
R jira field-options
R jira fields
M remote-direct - jira issue assign
R jira issue attachment get
R jira issue attachment list
M remote-direct - jira issue attachment upload
R jira issue check
R jira issue children
M preview-apply apply,expected-proposal-hash jira issue comment add
M remote-direct - jira issue comment delete
R jira issue comment list
R jira issue comment preview
M remote-direct - jira issue create
M remote-direct - jira issue delete
M remote-direct - jira issue edit
R jira issue field get
R jira issue field preview
M preview-apply apply,expected-proposal-hash,expected-updated jira issue field set
R jira issue fields
R jira issue get
R jira issue graph
R jira issue history
R jira issue images
M remote-direct - jira issue labels
M remote-direct - jira issue link add
M remote-direct - jira issue link delete
R jira issue link list
R jira issue link suggest
M remote-direct - jira issue link-epic
M plan apply,confirm jira issue plan apply
R jira issue refs
R jira issue search
M preview-apply apply,expected-proposal-hash jira issue transition
R jira issue transition preview
R jira issue tree
M remote-direct - jira issue update
R jira issue view
M preview-apply apply,expected-proposal-hash jira issue watchers add
R jira issue watchers list
M preview-apply apply,expected-proposal-hash jira issue watchers remove
M preview-apply apply,expected-proposal-hash jira issue worklog add
R jira issue worklog list
R jira link-types
R jira me
R jira planning report
R jira pull
M preview-apply apply jira push
R jira reconcile preview
M local-direct - jira reconcile stage
R jira quality-report
R jira render
M remote-direct - jira sprint add
R jira sprint current
R jira sprint get
R jira sprint issues
R jira sprint list
M remote-direct - jira sprint remove
R jira snapshot
R jira status
R jira structure export
R jira structure folders
R jira structure forest
R jira structure get
R jira structure pull-issues
R jira structure rows
R jira structure values
R jira structure view
R jira transitions
R jira user get
R jira user search
R manifest create
R mcp serve
M preview-apply apply,expected-backend-sha256,confirm mirror backend bind
R mirror backend status
M dedicated-apply from-file,candidate-hash,expected-current-hash profile apply
R profile guidance
R profile preview
M local-direct - profile revalidate
R profile revalidation status
R profile show
M local-direct - profile suggest
M dedicated-apply from-file,suggestion-hash,candidate-hash,expected-current-hash profile suggestion apply
M local-direct - profile suggestion reject
R profile suggestion review
R version
`)

func stringSetFromLines(value string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

func validMutationProfile(profile mutationProfile) bool {
	switch profile {
	case mutationLocalDirect, mutationRemoteDirect, mutationPreviewApply, mutationDedicatedApply, mutationPlan:
		return true
	default:
		return false
	}
}

func parseCommandRegistry(value string) (commandRegistryState, error) {
	registry := commandRegistryState{nodes: map[string]commandRegistration{"": {traits: commandGroup}}}
	for lineNumber, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		var registration commandRegistration
		var pathFields []string
		switch {
		case len(fields) >= 2 && fields[0] == "R":
			registration.traits = commandLeaf
			pathFields = fields[1:]
		case len(fields) >= 4 && fields[0] == "M":
			registration.traits = commandLeaf | commandMutating
			registration.profile = mutationProfile(fields[1])
			if !validMutationProfile(registration.profile) {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid mutation profile %q", lineNumber+1, fields[1])
			}
			if fields[2] != "-" {
				registration.requiredFlags = strings.Split(fields[2], ",")
				for _, name := range registration.requiredFlags {
					if name == "" {
						return commandRegistryState{}, fmt.Errorf("registry line %d has an empty required flag", lineNumber+1)
					}
				}
			}
			pathFields = fields[3:]
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
			for _, name := range registration.requiredFlags {
				hasApply = hasApply || name == "apply"
			}
			switch registration.profile {
			case mutationPreviewApply:
				if !hasApply {
					return commandRegistryState{}, fmt.Errorf("registry line %d preview-apply profile does not require --apply", lineNumber+1)
				}
			case mutationDedicatedApply, mutationPlan:
				if len(registration.requiredFlags) == 0 {
					return commandRegistryState{}, fmt.Errorf("registry line %d %s profile has no required guard", lineNumber+1, registration.profile)
				}
			case mutationLocalDirect, mutationRemoteDirect:
				if len(registration.requiredFlags) != 0 {
					return commandRegistryState{}, fmt.Errorf("registry line %d %s profile unexpectedly declares a guard", lineNumber+1, registration.profile)
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
			classifyTextOutput(cmd, path)
			classifyIDOutput(cmd, path)
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
			for _, flag := range registration.requiredFlags {
				if cmd.Flags().Lookup(flag) == nil {
					return fmt.Errorf("mutating command %q profile %q requires missing --%s flag", cmd.CommandPath(), registration.profile, flag)
				}
			}
		} else if registration.profile != mutationNone || len(registration.requiredFlags) != 0 {
			return fmt.Errorf("read-only command %q has mutation metadata", cmd.CommandPath())
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
	registration, ok := commandRegistry.nodes[path]
	if !ok || registration.traits&commandMutating == 0 {
		return nil
	}
	profile := registration.profile
	applyFlag := cmd.Flags().Lookup("apply")
	applyRequested := false
	if applyFlag != nil {
		value, err := cmd.Flags().GetBool("apply")
		if err != nil {
			return usageErr("invalid --apply value")
		}
		applyRequested = value
	}
	preflightRequired := profile == mutationDedicatedApply
	if path == "mirror backend bind" || path == "conf page delete" {
		preflightRequired = applyRequested
	}
	if !preflightRequired {
		if path == "conf page delete" {
			return validateConfluencePageDeleteInvocation(cmd, false)
		}
		return nil
	}
	for _, name := range registration.requiredFlags {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			return &accessPolicyInvariantError{Command: fmt.Sprintf("%s missing --%s", cmd.CommandPath(), name)}
		}
		if profile == mutationPreviewApply && name == "apply" {
			continue
		}
		missing := !flag.Changed
		if flag.Value.Type() == "bool" {
			value, err := cmd.Flags().GetBool(name)
			missing = err != nil || !value
		} else if name != "expected-parent" {
			missing = missing || strings.TrimSpace(flag.Value.String()) == ""
		}
		if missing {
			if profile == mutationPreviewApply {
				return usageErr("--%s is required with --apply", name)
			}
			return usageErr("--%s is required for this apply command", name)
		}
	}
	if path == "conf page delete" {
		return validateConfluencePageDeleteInvocation(cmd, applyRequested)
	}
	return nil
}

func validateConfluencePageDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	id, err := cmd.Flags().GetString("id")
	if err != nil || strings.TrimSpace(id) == "" {
		return usageErr("--id is required")
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

func resolveReadOnlyPolicy(cmd *cobra.Command, flagEnabled bool) (bool, error) {
	if flagEnabled || envReadOnly() {
		return true, nil
	}
	// Offline/trivial reads are incapable of backend/config mutation and are
	// already guaranteed not to self-update. Keep them usable for diagnosis
	// when config.json itself is malformed; every mutator and online read still
	// decodes the policy strictly below.
	if cmd.Annotations[accessAnnotation] == "read-only" && skipSelfUpdate(cmd) {
		return false, nil
	}
	cfg, err := config.LoadForEdit()
	if err != nil {
		return false, err
	}
	return cfg.ReadOnly, nil
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
	if enabled {
		return &readOnlyPolicyError{Command: cmd.CommandPath()}
	}
	return nil
}

func envReadOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ATL_READ_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
