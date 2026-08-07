package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

type policyShowResult struct {
	SchemaVersion   int                            `json:"schema_version"`
	Active          bool                           `json:"active"`
	Enforcement     string                         `json:"enforcement"`
	AdvisoryBecause []string                       `json:"advisory_because"`
	ReadOnly        policyReadOnlyStatus           `json:"read_only"`
	Source          any                            `json:"source"`
	Digest          policyDigestStatus             `json:"digest"`
	Grants          map[string]map[string][]string `json:"grants"`
	Governs         map[string]string              `json:"governs"`
	NotABoundary    string                         `json:"not_a_boundary"`
}

type policyReadOnlyStatus struct {
	Active bool `json:"active"`
	Source any  `json:"source"`
}

type policyDigestStatus struct {
	Managed *string `json:"managed"`
	User    *string `json:"user"`
}

type policyExplainResult struct {
	SchemaVersion int                 `json:"schema_version"`
	Decision      string              `json:"decision"`
	Reason        string              `json:"reason,omitempty"`
	Unresolved    []string            `json:"unresolved,omitempty"`
	Verbs         domain.WriteVerbSet `json:"verbs"`
	Target        domain.WriteTarget  `json:"target"`
}

func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Inspect the frozen scoped write policy"}
	cmd.AddCommand(policyShowCmd(), policyExplainCmd())
	return cmd
}

func policyShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the frozen process policy and its effective grants",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := currentProcessPolicy.resolve()
			if err != nil {
				return classifyProcessPolicyLoadError(err)
			}
			result := buildPolicyShowResult(resolved)
			return emit(cmd, result, func() string { return policyShowText(result) })
		},
	}
}

func policyExplainCmd() *cobra.Command {
	var service, verb, kind, id, project, key, space, under string
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Evaluate one offline write-policy target without credentials or network",
		RunE: func(cmd *cobra.Command, _ []string) error {
			writeVerb := domain.WriteVerb(strings.ToLower(strings.TrimSpace(verb)))
			if !domain.ValidWriteVerb(writeVerb) {
				return usageErr("--verb must be one of create|update|comment|transition|move|delete")
			}
			service = strings.ToLower(strings.TrimSpace(service))
			if service != "jira" && service != "confluence" {
				return usageErr("--service must be jira or confluence")
			}
			if kind == "" {
				if service == "jira" {
					kind = "issue"
				} else {
					kind = "page"
				}
			}
			target := domain.WriteTarget{
				Service: service, Kind: strings.ToLower(kind), ID: strings.TrimSpace(id),
				Project: strings.ToUpper(strings.TrimSpace(project)), Key: strings.ToUpper(strings.TrimSpace(key)),
				Space: strings.ToUpper(strings.TrimSpace(space)),
			}
			if target.Project == "" && domain.ValidJiraIssueKey(target.Key) {
				target.Project = target.Key[:strings.IndexByte(target.Key, '-')]
			}
			if under != "" {
				target.AncestorIDs = splitNonBlank(under)
			}
			if err := validatePolicyExplainTarget(target); err != nil {
				return err
			}
			resolved, err := currentProcessPolicy.resolve()
			if err != nil {
				return classifyProcessPolicyLoadError(err)
			}
			request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{writeVerb}, Targets: []domain.WriteTarget{target}}
			decision := contentpolicy.Decide(resolved.Layers, request)
			result := policyExplainResult{SchemaVersion: 1, Decision: "deny", Verbs: request.Verbs, Target: target}
			if decision.Allowed {
				result.Decision = "allow"
			} else {
				result.Reason = string(decision.Reason)
				if contentpolicy.PreflightDeny(resolved.Layers, request) == nil {
					result.Decision = "conditional"
					result.Reason = string(contentpolicy.ReasonScopeUnresolved)
					result.Unresolved = policyExplainUnresolved(resolved, target, writeVerb)
				}
			}
			return emit(cmd, result, func() string { return policyExplainText(result) })
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "target service: jira|confluence")
	cmd.Flags().StringVar(&verb, "verb", "", "write verb")
	cmd.Flags().StringVar(&kind, "kind", "", "resource kind (defaults to issue or page)")
	cmd.Flags().StringVar(&id, "id", "", "canonical numeric resource id")
	cmd.Flags().StringVar(&project, "project", "", "canonical Jira project key")
	cmd.Flags().StringVar(&key, "key", "", "canonical Jira issue key")
	cmd.Flags().StringVar(&space, "space", "", "canonical Confluence space key")
	cmd.Flags().StringVar(&under, "under", "", "comma-separated Confluence ancestor ids")
	_ = cmd.MarkFlagRequired("service")
	_ = cmd.MarkFlagRequired("verb")
	return cmd
}

func policyExplainUnresolved(resolved *contentpolicy.Resolved, target domain.WriteTarget, verb domain.WriteVerb) []string {
	if resolved == nil {
		return nil
	}
	unresolved := map[string]bool{}
	for _, layer := range resolved.Layers {
		for _, rule := range layer.Policy.Rules {
			selector := rule.Resource
			if !verbSetContains(rule.Verbs, verb) ||
				!selectorValueCouldMatch(selector.Services, target.Service) ||
				!selectorValueCouldMatch(selector.Kinds, target.Kind) ||
				!selectorValueCouldMatch(selector.Projects, target.Project) ||
				!selectorValueCouldMatch(selector.Keys, target.Key) ||
				!selectorValueCouldMatch(selector.Spaces, target.Space) ||
				!selectorValueCouldMatch(selector.IDs, target.ID) {
				continue
			}
			for _, item := range []struct {
				name    string
				missing bool
			}{
				{"project", len(selector.Projects) > 0 && target.Project == ""},
				{"key", len(selector.Keys) > 0 && target.Key == ""},
				{"space", len(selector.Spaces) > 0 && target.Space == ""},
				{"id", len(selector.IDs) > 0 && target.ID == ""},
				{"under", len(selector.Under) > 0 && target.AncestorIDs == nil && !stringSetContains(selector.Under, target.ID)},
			} {
				if item.missing {
					unresolved[item.name] = true
				}
			}
		}
	}
	ordered := []string{"project", "key", "space", "id", "under"}
	out := make([]string, 0, len(unresolved))
	for _, name := range ordered {
		if unresolved[name] {
			out = append(out, name)
		}
	}
	return out
}

func selectorValueCouldMatch(values []string, actual string) bool {
	return len(values) == 0 || actual == "" || stringSetContains(values, actual)
}

func validatePolicyExplainTarget(target domain.WriteTarget) error {
	if target.Service == "jira" {
		if !stringSetContains([]string{"issue", "sprint", "link", "attachment", "worklog", "watcher"}, target.Kind) {
			return usageErr("--kind is not a governed Jira resource kind")
		}
		if target.Space != "" || target.AncestorIDs != nil {
			return usageErr("--space and --under apply only to Confluence targets")
		}
		if target.ID != "" && target.Kind != "sprint" {
			return usageErr("--id applies only to Jira sprint targets")
		}
		if target.Kind == "sprint" && !domain.ValidConfluenceContentID(target.ID) {
			return usageErr("Jira sprint targets require a positive numeric --id")
		}
		if target.Key != "" && !domain.ValidJiraIssueKey(target.Key) {
			return usageErr("--key must be a canonical Jira issue key")
		}
		if target.Project != "" && !domain.ValidJiraIssueKey(target.Project+"-1") {
			return usageErr("--project must be a canonical Jira project key")
		}
		return nil
	}
	if !stringSetContains([]string{"page", "blogpost", "attachment", "comment"}, target.Kind) {
		return usageErr("--kind is not a governed Confluence resource kind")
	}
	if target.Project != "" || target.Key != "" {
		return usageErr("--project and --key apply only to Jira targets")
	}
	if target.ID != "" && !domain.ValidConfluenceContentID(target.ID) {
		return usageErr("--id must be a positive numeric Confluence content id")
	}
	for _, ancestor := range target.AncestorIDs {
		if !domain.ValidConfluenceContentID(ancestor) {
			return usageErr("--under must contain positive numeric Confluence content ids")
		}
	}
	return nil
}

func splitNonBlank(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func buildPolicyShowResult(resolved *contentpolicy.Resolved) policyShowResult {
	result := policyShowResult{
		SchemaVersion: 1, Active: resolved != nil && len(resolved.Layers) != 0, Enforcement: "advisory",
		ReadOnly: policyReadOnlyStatus{Active: currentReadOnlyPolicy}, Digest: policyDigestStatus{},
		Grants:       summarizePolicyGrants(resolved),
		Governs:      map[string]string{"jira": "guarded", "confluence": "guarded", "local_commands": "not_governed", "local_mirror": "not_governed", "reads": "not_governed"},
		NotABoundary: "atl enforces these rules on the atl code path only; a process that can run atl can read the credential and call the REST API directly",
	}
	if result.ReadOnly.Active {
		source := "configuration"
		switch {
		case readOnly:
			source = "flag"
		case envReadOnly():
			source = "environment"
		}
		result.ReadOnly.Source = source
		for service := range result.Grants {
			for verb := range result.Grants[service] {
				result.Grants[service][verb] = []string{}
			}
		}
	}
	if resolved != nil {
		var sources []string
		for _, layer := range resolved.Layers {
			sources = append(sources, layer.Source)
			digest := layer.Digest
			if layer.Source == "config_dir" {
				result.Digest.User = &digest
			} else {
				result.Digest.Managed = &digest
			}
		}
		if len(sources) == 1 {
			result.Source = sources[0]
		} else if len(sources) > 1 {
			result.Source = "layered"
		}
	}
	result.AdvisoryBecause = []string{"credential_readable_by_uid", "policy_writable_by_uid"}
	if os.Getenv("ATL_POLICY_SHA256") == "" {
		result.AdvisoryBecause = append(result.AdvisoryBecause, "no_digest_pin")
	}
	if !policyHasCompleteBackendBinding(resolved) {
		result.AdvisoryBecause = append(result.AdvisoryBecause, "no_backend_binding")
	}
	if os.Getenv("ATL_NO_UPDATE") == "" {
		result.AdvisoryBecause = append(result.AdvisoryBecause, "self_update_armed")
	}
	if currentProcessPolicy != nil && currentProcessPolicy.required && os.Getenv("ATL_POLICY_FILE") != "" && os.Getenv("ATL_POLICY_SHA256") != "" && policyHasCompleteBackendBinding(resolved) {
		result.Enforcement = "sealed_unverified"
	}
	return result
}

func policyHasCompleteBackendBinding(resolved *contentpolicy.Resolved) bool {
	if resolved == nil || len(resolved.Layers) == 0 {
		return false
	}
	for _, layer := range resolved.Layers {
		if layer.Policy.Backend.JiraSHA256 == "" || layer.Policy.Backend.ConfluenceSHA256 == "" {
			return false
		}
	}
	return true
}

func summarizePolicyGrants(resolved *contentpolicy.Resolved) map[string]map[string][]string {
	services := map[string][]domain.WriteVerb{
		"jira":       {domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment, domain.WriteVerbTransition, domain.WriteVerbMove, domain.WriteVerbDelete},
		"confluence": {domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment, domain.WriteVerbMove, domain.WriteVerbDelete},
	}
	out := make(map[string]map[string][]string, len(services))
	for service, verbs := range services {
		out[service] = make(map[string][]string, len(verbs))
		for _, verb := range verbs {
			out[service][string(verb)] = effectiveGrantLabels(resolved, service, verb)
		}
	}
	return out
}

func effectiveGrantLabels(resolved *contentpolicy.Resolved, service string, verb domain.WriteVerb) []string {
	if resolved == nil || len(resolved.Layers) == 0 {
		return []string{"service:" + service}
	}
	var intersection map[string]struct{}
	for _, layer := range resolved.Layers {
		current := map[string]struct{}{}
		hasApplicableDeny := false
		for _, rule := range layer.Policy.Rules {
			if !verbSetContains(rule.Verbs, verb) || !stringSetContains(rule.Resource.Services, service) {
				continue
			}
			if rule.Effect == contentpolicy.EffectDeny {
				hasApplicableDeny = true
				continue
			}
			for _, label := range selectorGrantLabels(rule.Resource, service) {
				current[label] = struct{}{}
			}
		}
		// The compact discovery schema cannot express allow-minus-deny. Emptying
		// this verb is conservative; retaining an allow label would overstate the
		// effective scope and could make an agent plan a write that is forbidden.
		if hasApplicableDeny {
			clear(current)
		}
		if intersection == nil {
			intersection = current
			continue
		}
		for value := range intersection {
			if _, ok := current[value]; !ok {
				delete(intersection, value)
			}
		}
	}
	out := make([]string, 0, len(intersection))
	for value := range intersection {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func selectorGrantLabels(selector contentpolicy.Selector, service string) []string {
	var out []string
	for _, pair := range []struct {
		prefix string
		values []string
	}{
		{"project:", selector.Projects}, {"key:", selector.Keys}, {"space:", selector.Spaces}, {"id:", selector.IDs}, {"under:", selector.Under}, {"kind:", selector.Kinds},
	} {
		for _, value := range pair.values {
			out = append(out, pair.prefix+value)
		}
	}
	if len(out) == 0 {
		out = append(out, "service:"+service)
	}
	return out
}

func verbSetContains(values domain.WriteVerbSet, wanted domain.WriteVerb) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func stringSetContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func policyShowText(result policyShowResult) string {
	return fmt.Sprintf("active: %t\nenforcement: %s\nsource: %v\n", result.Active, result.Enforcement, result.Source)
}

func policyExplainText(result policyExplainResult) string {
	line := "decision: " + result.Decision
	if result.Reason != "" {
		line += "\nreason: " + result.Reason
	}
	if len(result.Unresolved) != 0 {
		line += "\nunresolved: " + strings.Join(result.Unresolved, ",")
	}
	return line + "\n"
}
