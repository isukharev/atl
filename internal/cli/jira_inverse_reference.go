package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type jiraInverseReferenceSearcher interface {
	SearchInverseReferences(context.Context, app.JiraInverseReferenceOptions) (*app.JiraInverseReferenceResult, error)
}

type jiraInverseReferenceServiceFactory func() (jiraInverseReferenceSearcher, error)

// jiraIssueInverseReferenceCmd builds the bounded, caller-qualified reverse
// lookup command. The command deliberately validates every caller-controlled
// search policy before jiraService reads configuration or credentials.
func jiraIssueInverseReferenceCmd() *cobra.Command {
	return jiraIssueInverseReferenceCmdWithService(nil)
}

func jiraIssueInverseReferenceCmdWithService(newService jiraInverseReferenceServiceFactory) *cobra.Command {
	var (
		target           string
		targetKind       string
		scopeJQL         string
		mode             string
		sourceValues     []string
		fieldValues      []string
		maxIssues        int
		maxRequests      int
		maxResponseBytes int64
		strict           bool
	)

	options := func(cmd *cobra.Command) (app.JiraInverseReferenceOptions, error) {
		var opts app.JiraInverseReferenceOptions
		if !cmd.Flags().Changed("target") || strings.TrimSpace(target) == "" {
			return opts, usageErr("--target is required and must be non-empty")
		}
		if !cmd.Flags().Changed("target-kind") {
			return opts, usageErr("--target-kind is required (confluence-page or gitlab-project)")
		}
		kind, err := inverseReferenceTargetKind(targetKind)
		if err != nil {
			return opts, err
		}
		if !cmd.Flags().Changed("scope-jql") || strings.TrimSpace(scopeJQL) == "" {
			return opts, usageErr("--scope-jql is required and must be non-empty")
		}
		if !cmd.Flags().Changed("mode") {
			return opts, usageErr("--mode is required (exhaustive or fast)")
		}
		searchMode, err := inverseReferenceMode(mode)
		if err != nil {
			return opts, err
		}
		sources, err := inverseReferenceSources(sourceValues)
		if err != nil {
			return opts, err
		}
		fields, err := inverseReferenceFields(fieldValues)
		if err != nil {
			return opts, err
		}
		if containsInverseReferenceSource(sources, domain.JiraInverseReferenceSourceFields) != (len(fields) != 0) {
			return opts, usageErr("--fields must be non-empty exactly when --sources includes fields")
		}
		for _, bound := range []struct {
			name    string
			changed bool
			value   int64
		}{
			{"max-issues", cmd.Flags().Changed("max-issues"), int64(maxIssues)},
			{"max-requests", cmd.Flags().Changed("max-requests"), int64(maxRequests)},
			{"max-response-bytes", cmd.Flags().Changed("max-response-bytes"), maxResponseBytes},
		} {
			if !bound.changed || bound.value <= 0 {
				return opts, usageErr("--%s is required and must be greater than zero", bound.name)
			}
		}
		opts = app.JiraInverseReferenceOptions{
			Target:           target,
			TargetKind:       kind,
			ScopeJQL:         scopeJQL,
			Mode:             searchMode,
			Sources:          sources,
			Fields:           fields,
			MaxIssues:        maxIssues,
			MaxRequests:      maxRequests,
			MaxResponseBytes: maxResponseBytes,
		}
		return app.NormalizeJiraInverseReferenceOptions(opts)
	}

	search := &cobra.Command{
		Use:   "search",
		Short: "Search a caller-qualified Jira scope for references to one external target",
		Long: "Search a caller-qualified Jira scope for references to one external target without reading GitLab or discovered URLs. " +
			"Exhaustive performs two caller-visible ordered passes and can prove absence only when both reach terminal coverage. " +
			"Fast may stop early and therefore cannot prove absence. Fields, Development, and Properties are opt-in sources.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("positional arguments are not supported")
			}
			_, err := options(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options(cmd)
			if err != nil {
				return err
			}
			var service jiraInverseReferenceSearcher
			if newService != nil {
				service, err = newService()
			} else {
				service, err = jiraService(cmd)
			}
			if err != nil {
				return err
			}
			if service == nil {
				return fmt.Errorf("%w: Jira inverse-reference service is not configured", domain.ErrConfig)
			}
			result, err := service.SearchInverseReferences(cmd.Context(), opts)
			if err != nil {
				return err
			}
			var text func() string
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				rendered, err := app.RenderJiraInverseReferencesText(result)
				if err != nil {
					return err
				}
				rendered = strings.TrimSuffix(rendered, "\n")
				text = func() string { return rendered }
			}
			if err := emit(cmd, result, text); err != nil {
				return err
			}
			if strict && !result.Complete {
				return fmt.Errorf("%w: inverse reference search is incomplete", domain.ErrCheckFailed)
			}
			return nil
		},
	}
	search.Flags().StringVar(&target, "target", "", "exact target identity to find (required)")
	search.Flags().StringVar(&targetKind, "target-kind", "", "target kind: confluence-page|gitlab-project (required)")
	search.Flags().StringVar(&scopeJQL, "scope-jql", "", "caller-qualified Jira JQL scope (required)")
	search.Flags().StringVar(&mode, "mode", "", "search policy: exhaustive|fast (required; fast cannot prove absence)")
	search.Flags().StringArrayVar(&sourceValues, "sources", nil, "evidence source (repeat/comma: description,fields,comments,remote-links,worklogs,development,properties; required)")
	search.Flags().StringArrayVar(&fieldValues, "fields", nil, "exact technical Jira field id (repeat/comma; required exactly when sources includes fields)")
	search.Flags().IntVar(&maxIssues, "max-issues", 0, "positive candidate issue bound (required)")
	search.Flags().IntVar(&maxRequests, "max-requests", 0, "positive physical request bound (required)")
	search.Flags().Int64Var(&maxResponseBytes, "max-response-bytes", 0, "positive buffered response byte bound (required)")
	search.Flags().BoolVar(&strict, "strict", false, "emit the qualified result, then fail if requested coverage is incomplete")
	_ = search.RegisterFlagCompletionFunc("target-kind", fixedComp("confluence-page", "gitlab-project"))
	_ = search.RegisterFlagCompletionFunc("mode", fixedComp("exhaustive", "fast"))

	group := &cobra.Command{Use: "reference", Short: "Reference search operations"}
	group.AddCommand(search)
	return group
}

func inverseReferenceTargetKind(value string) (domain.JiraInverseReferenceTargetKind, error) {
	switch strings.TrimSpace(value) {
	case "confluence-page":
		return domain.JiraInverseReferenceTargetConfluencePage, nil
	case "gitlab-project":
		return domain.JiraInverseReferenceTargetGitLabProject, nil
	default:
		return "", usageErr("--target-kind must be confluence-page or gitlab-project")
	}
}

func inverseReferenceMode(value string) (domain.JiraInverseReferenceMode, error) {
	switch strings.TrimSpace(value) {
	case "exhaustive":
		return domain.JiraInverseReferenceModeExhaustive, nil
	case "fast":
		return domain.JiraInverseReferenceModeFast, nil
	default:
		return "", usageErr("--mode must be exhaustive or fast")
	}
}

func inverseReferenceSources(values []string) ([]domain.JiraInverseReferenceSource, error) {
	if len(values) == 0 {
		return nil, usageErr("--sources is required")
	}
	seen := make(map[domain.JiraInverseReferenceSource]struct{})
	out := make([]domain.JiraInverseReferenceSource, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			source, err := inverseReferenceSource(part)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[source]; duplicate {
				return nil, usageErr("--sources must not contain duplicates")
			}
			seen[source] = struct{}{}
			out = append(out, source)
		}
	}
	return out, nil
}

func inverseReferenceSource(value string) (domain.JiraInverseReferenceSource, error) {
	switch strings.TrimSpace(value) {
	case "description":
		return domain.JiraInverseReferenceSourceDescription, nil
	case "fields":
		return domain.JiraInverseReferenceSourceFields, nil
	case "comments":
		return domain.JiraInverseReferenceSourceComments, nil
	case "remote-links":
		return domain.JiraInverseReferenceSourceRemoteLinks, nil
	case "worklogs":
		return domain.JiraInverseReferenceSourceWorklogs, nil
	case "development":
		return domain.JiraInverseReferenceSourceDevelopment, nil
	case "properties":
		return domain.JiraInverseReferenceSourceProperties, nil
	default:
		if strings.TrimSpace(value) == "" {
			return "", usageErr("--sources must not contain empty values")
		}
		return "", usageErr("--sources contains an unknown value")
	}
}

func inverseReferenceFields(values []string) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			field := strings.TrimSpace(part)
			if field == "" {
				return nil, usageErr("--fields must not contain empty values")
			}
			if _, duplicate := seen[field]; duplicate {
				return nil, usageErr("--fields must not contain duplicates")
			}
			seen[field] = struct{}{}
			out = append(out, field)
		}
	}
	return out, nil
}

func containsInverseReferenceSource(sources []domain.JiraInverseReferenceSource, want domain.JiraInverseReferenceSource) bool {
	for _, source := range sources {
		if source == want {
			return true
		}
	}
	return false
}
