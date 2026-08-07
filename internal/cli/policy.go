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
				return err
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
			if under != "" {
				target.AncestorIDs = splitNonBlank(under)
			}
			resolved, err := currentProcessPolicy.resolve()
			if err != nil {
				return err
			}
			request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{writeVerb}, Targets: []domain.WriteTarget{target}}
			decision := contentpolicy.Decide(resolved.Layers, request)
			result := policyExplainResult{SchemaVersion: 1, Decision: "deny", Verbs: request.Verbs, Target: target}
			if decision.Allowed {
				result.Decision = "allow"
			} else {
				result.Reason = string(decision.Reason)
				if decision.Reason == contentpolicy.ReasonScopeUnresolved || decision.Attribute != "" {
					result.Decision = "conditional"
					attribute := decision.Attribute
					if attribute == "key" {
						attribute = "project"
					}
					if attribute != "" {
						result.Unresolved = []string{attribute}
					}
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
		ReadOnly: policyReadOnlyStatus{Active: readOnly || envReadOnly()}, Digest: policyDigestStatus{},
		Grants:       summarizePolicyGrants(resolved),
		Governs:      map[string]string{"jira": "guarded", "confluence": "guarded", "local_commands": "not_governed", "local_mirror": "not_governed", "reads": "not_governed"},
		NotABoundary: "atl enforces these rules on the atl code path only; a process that can run atl can read the credential and call the REST API directly",
	}
	if result.ReadOnly.Active {
		source := "flag"
		if envReadOnly() {
			source = "environment"
		}
		result.ReadOnly.Source = source
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
	result.AdvisoryBecause = append(result.AdvisoryBecause, "self_update_armed")
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
		"jira":       {domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment, domain.WriteVerbTransition, domain.WriteVerbDelete},
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
		for _, rule := range layer.Policy.Rules {
			if rule.Effect != contentpolicy.EffectAllow || !verbSetContains(rule.Verbs, verb) || !stringSetContains(rule.Resource.Services, service) {
				continue
			}
			for _, label := range selectorGrantLabels(rule.Resource, service) {
				current[label] = struct{}{}
			}
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
