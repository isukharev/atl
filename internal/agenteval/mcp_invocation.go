package agenteval

import (
	"bytes"
	"encoding/json"
	"slices"
)

const (
	maxMCPInvocationExpectations = 100
	maxMCPRouteAlternatives      = 8
)

// MCPInvocation is retained only while one provider run is evaluated. Raw tool
// arguments are deliberately excluded from Observation and Result so private
// identifiers cannot enter stored or aggregated benchmark artifacts.
type MCPInvocation struct {
	Tool      string
	Arguments json.RawMessage
}

type mcpRouteExpectation struct {
	HTTPMethods map[string]int
	Invocations []MCPInvocation
}

func newMCPInvocation(tool string, input any) (MCPInvocation, bool) {
	if !mcpToolNameRE.MatchString(tool) {
		return MCPInvocation{}, false
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return MCPInvocation{}, false
	}
	arguments, err := canonicalJSONObject(raw)
	if err != nil {
		return MCPInvocation{}, false
	}
	return MCPInvocation{Tool: tool, Arguments: arguments}, true
}

func expectedMCPInvocations(raw json.RawMessage) ([]MCPInvocation, bool) {
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return nil, false
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &entries); err != nil ||
		len(entries) < 1 ||
		len(entries) > maxMCPInvocationExpectations {
		return nil, false
	}
	invocations := make([]MCPInvocation, 0, len(entries))
	for _, entry := range entries {
		if len(entry) != 2 {
			return nil, false
		}
		var tool string
		if err := json.Unmarshal(entry["tool"], &tool); err != nil ||
			!mcpToolNameRE.MatchString(tool) {
			return nil, false
		}
		arguments, err := canonicalJSONObject(entry["arguments"])
		if err != nil {
			return nil, false
		}
		invocations = append(invocations, MCPInvocation{Tool: tool, Arguments: arguments})
	}
	return invocations, true
}

func equalMCPInvocations(expected, observed []MCPInvocation) bool {
	return slices.EqualFunc(expected, observed, func(left, right MCPInvocation) bool {
		return left.Tool == right.Tool && bytes.Equal(left.Arguments, right.Arguments)
	})
}

func invocationToolsAllowed(invocations []MCPInvocation, allowed []string) bool {
	for _, invocation := range invocations {
		if !slices.Contains(allowed, invocation.Tool) {
			return false
		}
	}
	return true
}

func expectedMCPRouteAlternatives(raw json.RawMessage) ([]mcpRouteExpectation, bool) {
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return nil, false
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(canonical, &entries); err != nil ||
		len(entries) < 2 ||
		len(entries) > maxMCPRouteAlternatives {
		return nil, false
	}
	alternatives := make([]mcpRouteExpectation, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entryCanonical, err := canonicalJSON(entry)
		if err != nil {
			return nil, false
		}
		if _, duplicate := seen[string(entryCanonical)]; duplicate {
			return nil, false
		}
		seen[string(entryCanonical)] = struct{}{}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entryCanonical, &fields); err != nil ||
			len(fields) != 2 {
			return nil, false
		}
		methods, methodsOK := expectedHTTPMethods(fields["http_methods"])
		invocations, invocationsOK := expectedMCPInvocations(fields["invocations"])
		if !methodsOK || !invocationsOK {
			return nil, false
		}
		alternatives = append(alternatives, mcpRouteExpectation{
			HTTPMethods: methods,
			Invocations: invocations,
		})
	}
	return alternatives, true
}

func mcpRouteMatches(
	alternatives []mcpRouteExpectation,
	httpMethods map[string]int,
	invocations []MCPInvocation,
) bool {
	return slices.ContainsFunc(alternatives, func(alternative mcpRouteExpectation) bool {
		return equalHTTPMethods(alternative.HTTPMethods, httpMethods) &&
			equalMCPInvocations(alternative.Invocations, invocations)
	})
}
