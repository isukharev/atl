package main

import (
	"slices"
	"testing"

	"github.com/isukharev/atl/scripts/check-docs-freshness/hosted"
)

type needsFixture struct {
	Result  string            `json:"result"`
	Outputs map[string]string `json:"outputs"`
}

func plannedNeeds(t *testing.T, plan hosted.Plan) map[string]needsFixture {
	t.Helper()
	needs := map[string]needsFixture{}
	for name, selected := range plan.Jobs() {
		result := "skipped"
		if selected {
			result = "success"
		}
		needs[name] = needsFixture{Result: result}
	}
	outputs, err := planOutputs(plan)
	if err != nil {
		t.Fatal(err)
	}
	needs["binding"] = needsFixture{Result: "success", Outputs: outputs}
	return needs
}

func TestAggregateAcceptsOnlyTheRecomputedPlan(t *testing.T) {
	for name, lanes := range map[string][]string{
		"docs":      {"contracts"},
		"generated": {"contracts", "eval-compat"},
		"product":   {"contracts", "eval-compat", "product", "security"},
		"evaluator": {"contracts", "eval-full", "platform", "security"},
		"full":      slices.Clone(hosted.Lanes),
	} {
		t.Run(name, func(t *testing.T) {
			plan := hosted.Plan{SchemaVersion: 1, BaseSHA: testBase, HeadSHA: testHead, Lanes: lanes}
			if err := validateNeeds(encode(t, plannedNeeds(t, plan)), plan); err != nil {
				t.Fatal(err)
			}
			for job, selected := range plan.Jobs() {
				for _, result := range []string{"success", "failure", "skipped", "cancelled", "", "neutral"} { //nolint:misspell // GitHub's job-result spelling.
					if selected && result == "success" || !selected && result == "skipped" {
						continue
					}
					needs := plannedNeeds(t, plan)
					changed := needs[job]
					changed.Result = result
					needs[job] = changed
					if err := validateNeeds(encode(t, needs), plan); err == nil {
						t.Fatalf("accepted %s=%s", job, result)
					}
				}
				needs := plannedNeeds(t, plan)
				delete(needs, job)
				if err := validateNeeds(encode(t, needs), plan); err == nil {
					t.Fatalf("accepted missing %s", job)
				}
				needs["other"] = needsFixture{Result: "success"}
				if err := validateNeeds(encode(t, needs), plan); err == nil {
					t.Fatalf("accepted substituted %s", job)
				}
			}
			for _, key := range []string{"plan", "product", "evaluator", "platform", "security", "corpus"} {
				needs := plannedNeeds(t, plan)
				needs["binding"].Outputs[key] = "tampered"
				if err := validateNeeds(encode(t, needs), plan); err == nil {
					t.Fatalf("accepted changed %s output", key)
				}
			}
		})
	}
}

func TestPlanDecoderRejectsUnboundOrWeakenedPlans(t *testing.T) {
	b := binding{Base: testBase, Head: testHead}
	base := hosted.Plan{SchemaVersion: 1, BaseSHA: testBase, HeadSHA: testHead, Lanes: []string{"contracts"}}
	if _, err := decodePlan(encode(t, base), b); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*hosted.Plan){
		"base":                       func(p *hosted.Plan) { p.BaseSHA = testMerge },
		"head":                       func(p *hosted.Plan) { p.HeadSHA = testMerge },
		"version":                    func(p *hosted.Plan) { p.SchemaVersion++ },
		"contracts omitted":          func(p *hosted.Plan) { p.Lanes = nil },
		"unknown lane":               func(p *hosted.Plan) { p.Lanes = []string{"contracts", "unknown"} },
		"repeated lane":              func(p *hosted.Plan) { p.Lanes = []string{"contracts", "contracts"} },
		"unsorted lane":              func(p *hosted.Plan) { p.Lanes = []string{"eval-compat", "contracts"} },
		"false full":                 func(p *hosted.Plan) { p.Full = true },
		"product compat omitted":     func(p *hosted.Plan) { p.Lanes = []string{"contracts", "product", "security"} },
		"evaluator platform omitted": func(p *hosted.Plan) { p.Lanes = []string{"contracts", "eval-full", "security"} },
	} {
		t.Run(name, func(t *testing.T) {
			plan := base
			edit(&plan)
			if _, err := decodePlan(encode(t, plan), b); err == nil {
				t.Fatal("accepted weakened plan")
			}
		})
	}
	b.Full = true
	if _, err := decodePlan(encode(t, base), b); err == nil {
		t.Fatal("full override was narrowed")
	}
	base.Lanes, base.Full = slices.Clone(hosted.Lanes), true
	if _, err := decodePlan(encode(t, base), b); err != nil {
		t.Fatal(err)
	}
}
