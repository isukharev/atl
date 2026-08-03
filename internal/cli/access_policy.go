package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
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

type commandRegistration struct {
	traits        commandTrait
	profile       mutationProfile
	requiredFlags []string
	outputModes   commandOutputMode
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
// "R <output-modes> <path>". Mutating rows use
// "M <profile> <required-flags-or-dash> <output-modes> <path>". Output modes
// are explicit and canonical: json, json,text, json,id, or json,text,id. Parent
// groups are derived from path prefixes, so the finalized Cobra tree is checked
// bidirectionally for groups, leaves, and the two intentional hybrids.
var commandRegistry, commandRegistryErr = parseCommandRegistry(`
M local-direct - json,text auth login
M local-direct - json auth logout
R json,text auth status
R json,text,id capabilities
M local-direct - json,text conf apply
M preview-apply apply,confirm,expected-proposal-hash,expected-version json conf attachment delete
R json,text conf attachment get
R json,text,id conf attachment list
M remote-direct - json conf attachment upload
M remote-direct - json,text,id conf blog create
M preview-apply apply,expected-proposal-hash json,text conf comment add
R json,text conf comment list
M dedicated-apply apply,expected-proposal-hash json conf comment mutation apply
R json conf comment mutation preview
R json,text conf comment preview
R json,text conf comment thread
R json,text conf diff
M local-direct - json,text conf edit
R json,text conf me
M preview-apply apply,expected-proposal-hash,expected-version json,id conf page copy
M remote-direct - json conf page create
M preview-apply apply,confirm,expected-proposal-hash,expected-version json conf page delete
R json,text conf page get
R json,text conf page history
M preview-apply apply,expected-proposal-hash json,text conf page labels add
R json,text conf page labels list
M preview-apply apply,expected-proposal-hash json,text conf page labels remove
R json,text,id conf page list
R json,text conf page meta
M preview-apply apply,expected-proposal-hash,expected-version,expected-parent json,text conf page move
R json,text conf page open
R json,text conf page outline
R json,text,id conf page resolve
R json,text conf page section
R json,text conf page sections
M preview-apply apply,expected-proposal-hash,expected-version json,text conf page title set
R json,text conf page view
M plan confirm,expected-proposal-hash json,text conf plan apply
R json,text conf plan create
R json,text conf plan preview
R json,text conf pull
M remote-direct - json,text conf push
R json,text conf reconcile preview
M local-direct - json,text conf reconcile stage
R json,text conf render
R json,text,id conf search
R json,text conf snapshot
R json,text conf space tree
R json,text conf status
R json,text conf table extract
R json,text conf table summary
R json conf validate
M local-direct - json compatibility clear
M local-direct - json compatibility pin
R json,text compatibility status
R json,text completion bash
R json,text completion fish
R json,text completion powershell
R json,text completion zsh
M local-direct - json config set
R json,text config show
R json,text doctor
R json,text environment inspect
R json,text help
M local-direct - json,text jira apply
R json,text,id jira board backlog
R json,text,id jira board config
R json,text jira board export
R json,text,id jira board get
R json,text,id jira board issues
R json,text,id jira board list
R json,text,id jira board view
R json,text jira epic digest
R json,text jira export
R json,text jira export diff
R json,text jira field-options
R json,text jira fields
M remote-direct - json,text jira issue assign
R json,text jira issue attachment get
R json,text,id jira issue attachment list
M remote-direct - json jira issue attachment upload
R json,text jira issue check
R json,text,id jira issue children
M preview-apply apply,expected-proposal-hash json,text jira issue comment add
M remote-direct - json jira issue comment delete
R json,text,id jira issue comment list
R json,text jira issue comment preview
M remote-direct - json,text,id jira issue create
M preview-apply apply,confirm,expected-proposal-hash,expected-updated json jira issue delete
M remote-direct - json,text jira issue edit
R json,text jira issue field get
R json,text jira issue field preview
M preview-apply apply,expected-proposal-hash,expected-updated json,text jira issue field set
R json,text jira issue fields
R json,text jira issue get
R json,text jira issue graph
R json,text jira issue history
R json jira issue images
M remote-direct - json jira issue labels
M remote-direct - json jira issue link add
M remote-direct - json jira issue link delete
R json,text,id jira issue link list
R json,text jira issue link suggest
M remote-direct - json jira issue link-epic
M plan apply,confirm json,text jira issue plan apply
R json,text jira issue refs
R json,text,id jira issue search
M preview-apply apply,expected-proposal-hash json,text jira issue transition
R json,text jira issue transition preview
R json,text jira issue tree
M remote-direct - json jira issue update
R json,text jira issue view
M preview-apply apply,expected-proposal-hash json,text jira issue watchers add
R json,text jira issue watchers list
M preview-apply apply,expected-proposal-hash json,text jira issue watchers remove
M preview-apply apply,expected-proposal-hash json,text jira issue worklog add
R json,text,id jira issue worklog list
R json,text jira link-types
R json,text,id jira me
R json,text jira planning report
R json,text jira pull
M preview-apply apply json,text jira push
R json,text jira reconcile preview
M local-direct - json,text jira reconcile stage
R json,text jira quality-report
R json,text jira render
M remote-direct - json jira sprint add
R json,text,id jira sprint current
R json,text,id jira sprint get
R json,text,id jira sprint issues
R json,text,id jira sprint list
M remote-direct - json jira sprint remove
R json,text jira snapshot
R json,text jira status
R json,text jira structure export
R json,text,id jira structure folders
R json,text jira structure forest
R json,text,id jira structure get
R json,text,id jira structure pull-issues
R json,text,id jira structure rows
R json jira structure values
R json,text,id jira structure view
R json,text jira transitions
R json,text,id jira user get
R json,text,id jira user search
R json,text manifest create
R json mcp serve
M preview-apply apply,expected-backend-sha256,confirm json,text mirror backend bind
R json,text mirror backend status
M dedicated-apply from-file,candidate-hash,expected-current-hash json,text profile apply
R json,text profile guidance
R json,text profile preview
M local-direct - json,text profile revalidate
R json,text profile revalidation status
R json,text profile show
M local-direct - json,text profile suggest
M dedicated-apply from-file,suggestion-hash,candidate-hash,expected-current-hash json,text profile suggestion apply
M local-direct - json,text profile suggestion reject
R json,text profile suggestion review
R json,text version
`)

func validMutationProfile(profile mutationProfile) bool {
	switch profile {
	case mutationLocalDirect, mutationRemoteDirect, mutationPreviewApply, mutationDedicatedApply, mutationPlan:
		return true
	default:
		return false
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
		case len(fields) >= 3 && fields[0] == "R":
			registration.traits = commandLeaf
			var ok bool
			registration.outputModes, ok = parseCommandOutputModes(fields[1])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid output modes %q", lineNumber+1, fields[1])
			}
			pathFields = fields[2:]
		case len(fields) >= 5 && fields[0] == "M":
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
			var ok bool
			registration.outputModes, ok = parseCommandOutputModes(fields[3])
			if !ok {
				return commandRegistryState{}, fmt.Errorf("registry line %d has invalid output modes %q", lineNumber+1, fields[3])
			}
			pathFields = fields[4:]
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
	if path == "mirror backend bind" || path == "conf attachment delete" || path == "conf page copy" || path == "conf page delete" || path == "jira issue delete" {
		preflightRequired = applyRequested
	}
	if !preflightRequired {
		if path == "conf attachment delete" {
			return validateConfluenceAttachmentDeleteInvocation(cmd, false)
		}
		if path == "conf page copy" {
			return validateConfluencePageCopyInvocation(cmd, false)
		}
		if path == "conf page delete" {
			return validateConfluencePageDeleteInvocation(cmd, false)
		}
		if path == "jira issue delete" {
			return validateJiraIssueDeleteInvocation(cmd, false)
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
	if path == "conf attachment delete" {
		return validateConfluenceAttachmentDeleteInvocation(cmd, applyRequested)
	}
	if path == "conf page copy" {
		return validateConfluencePageCopyInvocation(cmd, applyRequested)
	}
	if path == "jira issue delete" {
		return validateJiraIssueDeleteInvocation(cmd, applyRequested)
	}
	return nil
}

func validateJiraIssueDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	key := cmd.Flags().Arg(0)
	if !canonicalJiraCLIIssueKey(key) {
		return usageErr("issue key must be canonical (for example PROJ-1)")
	}
	if outputFormat == "id" {
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

func canonicalJiraCLIIssueKey(value string) bool {
	dash := strings.LastIndexByte(value, '-')
	if dash < 2 || dash > 32 || dash == len(value)-1 || value[0] < 'A' || value[0] > 'Z' || value[dash+1] == '0' {
		return false
	}
	for _, char := range value[:dash] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	for _, char := range value[dash+1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validateConfluenceAttachmentDeleteInvocation(cmd *cobra.Command, applyRequested bool) error {
	pageID, pageErr := cmd.Flags().GetString("page-id")
	attachmentID, attachmentErr := cmd.Flags().GetString("id")
	if pageErr != nil || attachmentErr != nil || strings.TrimSpace(pageID) == "" || strings.TrimSpace(attachmentID) == "" {
		return usageErr("--page-id and --id are required")
	}
	if !canonicalConfluenceCLIContentID(pageID) {
		return usageErr("--page-id must be a positive numeric content id")
	}
	if !canonicalConfluenceCLIContentID(attachmentID) {
		return usageErr("--id must be a positive numeric attachment id")
	}
	if outputFormat == "id" {
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

func canonicalConfluenceCLIContentID(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
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
		if outputFormat == "id" {
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
