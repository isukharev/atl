package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// policyPreflightTargets is the path-aware, syntax-only identity extractor.
// It never resolves configuration, credentials, or backend identities.
func policyPreflightTargets(cmd *cobra.Command, args []string, identity policyIdentitySource) ([]domain.WriteTarget, error) {
	switch identity {
	case policyIdentityNone, policyIdentityJiraMirror, policyIdentityConfluenceMirror, policyIdentityJiraPlan:
		return nil, nil
	case policyIdentityConfluencePlan:
		return app.ConfluencePlanPolicyTargets(firstArg(args))
	case policyIdentityJiraIssueArg:
		kind := "issue"
		switch commandRegistryPath(cmd.Root(), cmd) {
		case "jira issue attachment upload":
			kind = "attachment"
		case "jira issue watchers add", "jira issue watchers remove":
			kind = "watcher"
		case "jira issue worklog add":
			kind = "worklog"
		}
		return jiraPreflightTargets(kind, firstArg(args)), nil
	case policyIdentityJiraProjectFlag:
		project := strings.ToUpper(policyFlagValue(cmd, "project"))
		if project == "" {
			return nil, nil
		}
		return []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: project}}, nil
	case policyIdentityJiraTwoIssueArgs:
		refs := []string{firstArg(args), policyFlagValue(cmd, "to", "epic")}
		if len(args) > 1 {
			refs = append(refs, args[1])
		}
		var out []domain.WriteTarget
		for _, ref := range refs {
			out = append(out, jiraPreflightTargets("issue", ref)...)
		}
		return uniqueWriteTargets(out), nil
	case policyIdentityJiraLinkID:
		return []domain.WriteTarget{{Service: "jira", Kind: "link"}}, nil
	case policyIdentityJiraLinkEndpoints:
		from := firstArg(args)
		if strings.HasSuffix(commandRegistryPath(cmd.Root(), cmd), "link delete") {
			from = policyFlagValue(cmd, "from")
		}
		var out []domain.WriteTarget
		for _, ref := range []string{from, policyFlagValue(cmd, "to")} {
			out = append(out, jiraPreflightTargets("link", ref)...)
		}
		return uniqueWriteTargets(out), nil
	case policyIdentityJiraSprintIssues:
		path, start := commandRegistryPath(cmd.Root(), cmd), 0
		var out []domain.WriteTarget
		if path == "jira sprint add" && len(args) > 0 {
			start = 1
			out = append(out, domain.WriteTarget{Service: "jira", Kind: "sprint", ID: args[0]})
		}
		for _, ref := range args[start:] {
			out = append(out, jiraPreflightTargets("issue", ref)...)
		}
		return out, nil
	case policyIdentityConfluencePageFlag:
		switch commandRegistryPath(cmd.Root(), cmd) {
		case "conf attachment delete":
			return confluencePreflightTarget("attachment", policyFlagValue(cmd, "id"))
		case "conf attachment upload":
			pageID := policyFlagValue(cmd, "id")
			if pageID == "" {
				return nil, nil
			}
			if _, err := confluencePreflightTarget("page", pageID); err != nil {
				return nil, err
			}
			return []domain.WriteTarget{{Service: "confluence", Kind: "attachment"}}, nil
		case "conf comment mutation apply":
			pageID := policyFlagValue(cmd, "id")
			if pageID == "" {
				return nil, nil
			}
			if _, err := confluencePreflightTarget("page", pageID); err != nil {
				return nil, err
			}
			return confluencePreflightTarget("comment", policyFlagValue(cmd, "thread-id"))
		default:
			return confluencePreflightTarget("page", policyFlagValue(cmd, "id", "page-id", "page"))
		}
	case policyIdentityConfluencePageArg:
		if commandRegistryPath(cmd.Root(), cmd) == "conf comment add" {
			if _, err := confluencePreflightTarget("page", firstArg(args)); err != nil {
				return nil, err
			}
			return []domain.WriteTarget{{Service: "confluence", Kind: "comment"}}, nil
		}
		return confluencePreflightTarget("page", firstArg(args))
	case policyIdentityConfluenceSpace:
		space := strings.ToUpper(policyFlagValue(cmd, "space"))
		if space == "" {
			return nil, nil
		}
		kind := "page"
		if commandRegistryPath(cmd.Root(), cmd) == "conf blog create" {
			kind = "blogpost"
		}
		return []domain.WriteTarget{{Service: "confluence", Kind: kind, Space: space}}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported content-policy identity source %q", domain.ErrCheckFailed, identity)
	}
}
