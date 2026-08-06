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
	if !decision.Allowed {
		if decision.Reason == reasonInvalidRequest {
			return ctx, fmt.Errorf("%w: invalid write authorization request", domain.ErrCheckFailed)
		}
		return ctx, denialFromDecision(decision, request, layers)
	}
	return domain.WithWriteClearance(ctx), nil
}
