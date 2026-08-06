package contentpolicy

import (
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type matchState uint8

const (
	matchNo matchState = iota
	matchYes
	matchUnresolved
)

// Decide evaluates every target/verb pair. Each configured layer defaults to
// deny and all layers must admit the request. Within a layer, deny wins
// independently of rule order or specificity.
func Decide(layers []Layer, request domain.WriteAuthorizationRequest) Decision {
	if len(layers) == 0 {
		return Decision{Allowed: true}
	}
	if request.ScopeProblem != domain.WriteScopeResolved {
		reason := reasonInvalidRequest
		switch request.ScopeProblem {
		case domain.WriteScopeUnresolved:
			reason = ReasonScopeUnresolved
		case domain.WriteScopeUnavailable:
			reason = ReasonScopeUnavailable
		case domain.WriteScopeContradiction:
			reason = ReasonScopeContradiction
		}
		return Decision{Reason: reason, Attribute: request.ScopeAttribute}
	}
	if !domain.ValidWriteVerbSet(request.Verbs) || len(request.Targets) == 0 {
		return Decision{Reason: reasonInvalidRequest}
	}
	for index, target := range request.Targets {
		if !validTarget(target) {
			return Decision{Reason: reasonInvalidRequest, Target: index}
		}
	}
	for _, layer := range layers {
		decision := decideLayer(layer, request)
		if !decision.Allowed {
			return decision
		}
	}
	return Decision{Allowed: true}
}

func decideLayer(layer Layer, request domain.WriteAuthorizationRequest) Decision {
	// First scan every pair for deny evidence. A default denial on an earlier
	// pair must not hide a deciding explicit deny elsewhere in the request.
	for targetIndex, target := range request.Targets {
		for _, verb := range request.Verbs {
			for _, rule := range layer.Policy.Rules {
				if rule.Effect != EffectDeny || !containsVerb(rule.Verbs, verb) {
					continue
				}
				state, attribute := matchSelector(rule.Resource, target)
				if state == matchYes {
					return Decision{Reason: ReasonExplicitDeny, RuleID: rule.ID, Layer: layer.Source, Target: targetIndex, Verb: verb}
				}
				if state == matchUnresolved {
					return Decision{Reason: ReasonScopeUnresolved, RuleID: rule.ID, Layer: layer.Source, Target: targetIndex, Verb: verb, Attribute: attribute}
				}
			}
		}
	}
	for targetIndex, target := range request.Targets {
		for _, verb := range request.Verbs {
			allowed := false
			for _, rule := range layer.Policy.Rules {
				if rule.Effect != EffectAllow || !containsVerb(rule.Verbs, verb) {
					continue
				}
				state, _ := matchSelector(rule.Resource, target)
				if state == matchYes {
					allowed = true
					break
				}
			}
			if !allowed {
				reason := ReasonNoMatchingAllow
				if request.ScopeAttribute != "" {
					reason = ReasonScopeUnresolved
				}
				return Decision{Reason: reason, Layer: layer.Source, Target: targetIndex, Verb: verb, Attribute: request.ScopeAttribute}
			}
		}
	}
	return Decision{Allowed: true}
}

func matchSelector(selector Selector, target domain.WriteTarget) (matchState, string) {
	// Supplied mismatches are decided before unresolved attributes. This keeps a
	// deny for another service or kind from poisoning an unrelated target.
	checks := []struct {
		name   string
		values []string
		actual string
	}{
		{"service", selector.Services, target.Service},
		{"kind", selector.Kinds, target.Kind},
		{"project", selector.Projects, target.Project},
		{"key", selector.Keys, target.Key},
		{"space", selector.Spaces, target.Space},
		{"id", selector.IDs, target.ID},
	}
	for _, check := range checks {
		if len(check.values) > 0 && check.actual != "" && !containsString(check.values, check.actual) {
			return matchNo, ""
		}
	}
	for _, check := range checks {
		if len(check.values) > 0 && check.actual == "" {
			return matchUnresolved, check.name
		}
	}
	if len(selector.Under) > 0 {
		if target.ID != "" && containsString(selector.Under, target.ID) {
			return matchYes, ""
		}
		if target.AncestorIDs == nil {
			return matchUnresolved, "under"
		}
		for _, ancestor := range target.AncestorIDs {
			if containsString(selector.Under, ancestor) {
				return matchYes, ""
			}
		}
		return matchNo, ""
	}
	return matchYes, ""
}

func validTarget(target domain.WriteTarget) bool {
	if target.Service != "jira" && target.Service != "confluence" {
		return false
	}
	if target.Kind == "" {
		return false
	}
	if target.Service == "jira" {
		if !containsString([]string{"issue", "sprint", "link", "attachment", "worklog", "watcher"}, target.Kind) ||
			target.Space != "" || target.AncestorIDs != nil || (target.ID != "" && target.Kind != "sprint") {
			return false
		}
		if target.Kind == "sprint" && !domain.ValidConfluenceContentID(target.ID) {
			return false
		}
	} else if !containsString([]string{"page", "blogpost", "attachment", "comment"}, target.Kind) || target.Project != "" || target.Key != "" {
		return false
	}
	if target.Key != "" && !domain.ValidJiraIssueKey(target.Key) {
		return false
	}
	if target.Project != "" && !domain.ValidJiraIssueKey(target.Project+"-1") {
		return false
	}
	if target.ID != "" && target.Service == "confluence" && !domain.ValidConfluenceContentID(target.ID) {
		return false
	}
	for _, ancestor := range target.AncestorIDs {
		if !domain.ValidConfluenceContentID(ancestor) {
			return false
		}
	}
	return target.Project == strings.ToUpper(target.Project) && target.Space == strings.ToUpper(target.Space)
}

func containsVerb(values domain.WriteVerbSet, wanted domain.WriteVerb) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
