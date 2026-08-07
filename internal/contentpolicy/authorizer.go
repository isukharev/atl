package contentpolicy

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

type Authorizer struct {
	layers []Layer
}

func NewAuthorizer(resolved *Resolved) *Authorizer {
	var layers []Layer
	if resolved != nil {
		layers = cloneLayers(resolved.Layers)
	}
	return &Authorizer{layers: layers}
}

func cloneLayers(input []Layer) []Layer {
	layers := make([]Layer, len(input))
	for index, layer := range input {
		layers[index] = layer
		layers[index].Policy.Rules = make([]Rule, len(layer.Policy.Rules))
		for ruleIndex, rule := range layer.Policy.Rules {
			copyRule := rule
			copyRule.Verbs = append(domain.WriteVerbSet(nil), rule.Verbs...)
			copyRule.Resource.Services = append([]string(nil), rule.Resource.Services...)
			copyRule.Resource.Kinds = append([]string(nil), rule.Resource.Kinds...)
			copyRule.Resource.Projects = append([]string(nil), rule.Resource.Projects...)
			copyRule.Resource.Keys = append([]string(nil), rule.Resource.Keys...)
			copyRule.Resource.Spaces = append([]string(nil), rule.Resource.Spaces...)
			copyRule.Resource.IDs = append([]string(nil), rule.Resource.IDs...)
			copyRule.Resource.Under = append([]string(nil), rule.Resource.Under...)
			layers[index].Policy.Rules[ruleIndex] = copyRule
		}
	}
	return layers
}

func cloneResolved(input *Resolved) *Resolved {
	if input == nil {
		return nil
	}
	return &Resolved{Layers: cloneLayers(input.Layers), Warnings: append([]Warning(nil), input.Warnings...)}
}

func (a *Authorizer) Authorize(ctx context.Context, request domain.WriteAuthorizationRequest) (context.Context, error) {
	var layers []Layer
	if a != nil {
		layers = a.layers
	}
	decision := Decide(layers, request)
	if decision.Allowed && domain.UntrustedConfluenceReference(ctx) {
		decision = Decision{Reason: ReasonScopeUnresolved, Attribute: "id"}
	}
	if !decision.Allowed {
		if decision.Reason == reasonInvalidRequest {
			return ctx, fmt.Errorf("%w: invalid write authorization request", domain.ErrCheckFailed)
		}
		return ctx, denialFromDecision(decision, request, layers)
	}
	return domain.WithWriteClearance(ctx), nil
}

// RequiredWriteScope reports the canonical metadata attributes referenced by
// any rule that can apply to service. It is frozen with the authorizer.
func (a *Authorizer) RequiredWriteScope(service string) domain.WriteScopeRequirements {
	var requirements domain.WriteScopeRequirements
	if a == nil {
		return requirements
	}
	for _, layer := range a.layers {
		for _, rule := range layer.Policy.Rules {
			if !containsString(rule.Resource.Services, service) {
				continue
			}
			if len(rule.Resource.Spaces) > 0 {
				requirements.Space = true
			}
			if len(rule.Resource.Under) > 0 {
				requirements.Ancestors = true
			}
		}
	}
	return requirements
}

// DenyUnderAnchors returns the distinct Confluence hierarchy anchors used by
// explicit deny rules. Callers receive a copy of the frozen data.
func (a *Authorizer) DenyUnderAnchors() []domain.WriteHierarchyAnchor {
	if a == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var anchors []domain.WriteHierarchyAnchor
	for _, layer := range a.layers {
		for _, rule := range layer.Policy.Rules {
			if rule.Effect != EffectDeny || !containsString(rule.Resource.Services, "confluence") {
				continue
			}
			for _, anchor := range rule.Resource.Under {
				if _, exists := seen[anchor]; !exists {
					seen[anchor] = struct{}{}
					anchors = append(anchors, domain.WriteHierarchyAnchor{ID: anchor, RuleID: rule.ID})
				}
			}
		}
	}
	return anchors
}

// PageDeleteScopeProblem performs the bounded static contained-content check.
// Child-id selectors cannot be established from a containing page and fail
// closed as unresolved; page-compatible selectors are evaluated directly.
func (a *Authorizer) PageDeleteScopeProblem(target domain.WriteTarget) (domain.WriteScopeProblem, string, string) {
	if a == nil {
		return domain.WriteScopeResolved, "", ""
	}
	for _, layer := range a.layers {
		for _, rule := range layer.Policy.Rules {
			if rule.Effect != EffectDeny || !containsVerb(rule.Verbs, domain.WriteVerbDelete) ||
				!containsString(rule.Resource.Services, "confluence") ||
				(!containsString(rule.Resource.Kinds, "attachment") && !containsString(rule.Resource.Kinds, "comment")) {
				continue
			}
			selector := rule.Resource
			selector.Kinds = nil
			selector.IDs = nil
			state, attribute := matchSelector(selector, target)
			switch state {
			case matchYes:
				if len(rule.Resource.IDs) > 0 {
					return domain.WriteScopeUnresolved, "id", rule.ID
				}
				return domain.WriteScopeContainedContent, "contained_content", rule.ID
			case matchUnresolved:
				return domain.WriteScopeUnresolved, attribute, rule.ID
			}
		}
	}
	return domain.WriteScopeResolved, "", ""
}
