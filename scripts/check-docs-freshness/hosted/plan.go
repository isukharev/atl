// Package hosted defines the closed plan exchanged by the maintained impact
// classifier and the hosted premerge binding/aggregate checker.
package hosted

import "slices"

// Lanes is the complete module-level contour. Full evaluator includes compat.
var Lanes = []string{"contracts", "corpus", "eval-compat", "eval-full", "platform", "product", "security"}

// Plan binds a canonical, conservative selection to two committed endpoints.
type Plan struct {
	SchemaVersion int      `json:"schema_version"`
	BaseSHA       string   `json:"base_sha"`
	HeadSHA       string   `json:"head_sha"`
	Lanes         []string `json:"lanes"`
	Full          bool     `json:"full"`
}

// Has reports whether the plan selects a lane.
func (p Plan) Has(lane string) bool { return slices.Contains(p.Lanes, lane) }

// Evaluator returns the strongest selected evaluator facade.
func (p Plan) Evaluator() string {
	if p.Has("eval-full") {
		return "full"
	}
	if p.Has("eval-compat") {
		return "compat"
	}
	return "none"
}

// Jobs is the complete aggregate inventory, including intentionally absent work.
func (p Plan) Jobs() map[string]bool {
	return map[string]bool{
		"binding": true, "contracts": true,
		"test": p.Has("product"), "lint": p.Has("product"),
		"agent-eval": p.Evaluator() != "none", "agent-eval-race": p.Has("eval-full"),
		"agent-eval-platform": p.Has("platform"), "agent-eval-extension-windows": p.Has("platform"),
		"govulncheck": p.Has("security"), "codeql": p.Has("security"),
		"corpus-devcontainer": p.Has("corpus"),
	}
}
