package contentpolicy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/isukharev/atl/internal/domain"
)

func TestDecideEvaluationSemantics(t *testing.T) {
	issue := domain.WriteTarget{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-7"}
	other := domain.WriteTarget{Service: "jira", Kind: "issue", Project: "OPS", Key: "OPS-2"}
	page := domain.WriteTarget{Service: "confluence", Kind: "page", Space: "DOCS", ID: "42", AncestorIDs: []string{"10"}}
	rules := []Rule{
		{ID: "allow-ml-write", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}},
		{ID: "deny-ml-delete", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}},
		{ID: "allow-docs-tree", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"confluence"}, Under: []string{"10"}}},
	}
	layer := Layer{Source: "config_dir", Policy: Policy{Rules: rules}}
	tests := []struct {
		name   string
		layers []Layer
		target domain.WriteTarget
		verb   domain.WriteVerb
		allow  bool
		reason DenialReason
		rule   string
	}{
		{"absent allows", nil, issue, domain.WriteVerbDelete, true, "", ""},
		{"matching allow", []Layer{layer}, issue, domain.WriteVerbUpdate, true, "", ""},
		{"deny wins", []Layer{layer}, issue, domain.WriteVerbDelete, false, ReasonExplicitDeny, "deny-ml-delete"},
		{"default deny", []Layer{layer}, other, domain.WriteVerbUpdate, false, ReasonNoMatchingAllow, ""},
		{"under ancestor", []Layer{layer}, page, domain.WriteVerbUpdate, true, "", ""},
		{"under root is decidable false", []Layer{layer}, domain.WriteTarget{Service: "confluence", Kind: "page", Space: "DOCS", ID: "42", AncestorIDs: []string{}}, domain.WriteVerbUpdate, false, ReasonNoMatchingAllow, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Decide(test.layers, domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{test.verb}, Targets: []domain.WriteTarget{test.target}})
			if got.Allowed != test.allow || got.Reason != test.reason || got.RuleID != test.rule {
				t.Fatalf("decision = %+v, want allowed=%t reason=%q rule=%q", got, test.allow, test.reason, test.rule)
			}
		})
	}
}

func TestAuthorizerReportsFrozenConfluenceScopeRequirements(t *testing.T) {
	resolved := &Resolved{Layers: []Layer{{Source: "managed", Policy: Policy{Rules: []Rule{
		{ID: "space", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, Spaces: []string{"DOC"}}},
		{ID: "tree", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Resource: Selector{Services: []string{"confluence"}, Under: []string{"10"}}},
		{ID: "jira", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}},
	}}}}}
	authorizer := NewAuthorizer(resolved)
	resolved.Layers[0].Policy.Rules[0].Resource.Spaces = nil
	resolved.Layers[0].Policy.Rules[1].Resource.Under[0] = "99"
	requirements := authorizer.RequiredWriteScope("confluence")
	if !requirements.Kind || !requirements.Space || !requirements.Ancestors {
		t.Fatalf("requirements=%+v", requirements)
	}
	anchors := authorizer.DenyUnderAnchors()
	if len(anchors) != 1 || anchors[0].ID != "10" || anchors[0].RuleID != "tree" {
		t.Fatalf("frozen anchors=%v", anchors)
	}
	anchors[0].ID = "changed"
	if authorizer.DenyUnderAnchors()[0].ID != "10" {
		t.Fatal("caller mutated frozen anchors")
	}
}

func TestUntrustedConfluenceReferenceIsDenyOnly(t *testing.T) {
	authorizer := NewAuthorizer(&Resolved{Layers: []Layer{{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "allow", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, IDs: []string{"10"}},
	}}}}}})
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "confluence", Kind: "page", ID: "10"}}}
	_, err := authorizer.Authorize(domain.WithUntrustedConfluenceReference(context.Background()), request)
	var denial *DenialError
	if !errors.As(err, &denial) || denial.Reason != ReasonScopeUnresolved || denial.Attribute != "id" || denial.RetrySafe {
		t.Fatalf("error=%v denial=%+v", err, denial)
	}
}

func TestDecideMismatchPrecedesUnresolvedAndLayersConjoin(t *testing.T) {
	target := domain.WriteTarget{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-1"}
	foreignDeny := Rule{ID: "foreign", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"confluence"}, Spaces: []string{"SECRET"}, Under: []string{"99"}}}
	allowML := Rule{ID: "allow-ml", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}}
	allowOPS := Rule{ID: "allow-ops", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"OPS"}}}
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{target}}
	decision := Decide([]Layer{{Source: "managed", Policy: Policy{Rules: []Rule{foreignDeny, allowML}}}, {Source: "user", Policy: Policy{Rules: []Rule{allowOPS}}}}, request)
	if decision.Reason != ReasonNoMatchingAllow || decision.Layer != "user" {
		t.Fatalf("decision = %+v, want user-layer default denial", decision)
	}
}

func TestLayerConjunctionNeverWidensEitherRandomPolicy(t *testing.T) {
	property := func(leftMask, rightMask uint8, useOPS bool) bool {
		request := domain.WriteAuthorizationRequest{
			Verbs:   domain.WriteVerbSet{domain.WriteVerbUpdate},
			Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-1"}},
		}
		if useOPS {
			request.Targets[0].Project, request.Targets[0].Key = "OPS", "OPS-1"
		}
		layer := func(mask uint8, source string) Layer {
			var rules []Rule
			for bit, project := range []string{"ML", "OPS"} {
				if mask&(1<<bit) != 0 {
					rules = append(rules, Rule{ID: source + "-allow-" + strings.ToLower(project), Effect: EffectAllow,
						Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{project}}})
				}
				if mask&(1<<(bit+2)) != 0 {
					rules = append(rules, Rule{ID: source + "-deny-" + strings.ToLower(project), Effect: EffectDeny,
						Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{project}}})
				}
			}
			return Layer{Source: source, Policy: Policy{Rules: rules}}
		}
		left, right := layer(leftMask, "managed"), layer(rightMask, "user")
		effective := Decide([]Layer{left, right}, request)
		return !effective.Allowed || (Decide([]Layer{left}, request).Allowed && Decide([]Layer{right}, request).Allowed)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

func TestDecideExplicitDenyWinsAcrossCompoundLayer(t *testing.T) {
	request := domain.WriteAuthorizationRequest{
		Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Targets: []domain.WriteTarget{
			{Service: "jira", Kind: "issue", Project: "OPS", Key: "OPS-1"},
			{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-1"},
		},
	}
	layer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "deny-ml", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate},
		Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}},
	}}}}
	decision := Decide([]Layer{layer}, request)
	if decision.Reason != ReasonExplicitDeny || decision.RuleID != "deny-ml" || decision.Target != 1 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideAbsentPolicyDoesNotValidateDormantRequest(t *testing.T) {
	decision := Decide(nil, domain.WriteAuthorizationRequest{ScopeProblem: domain.WriteScopeContradiction})
	if !decision.Allowed {
		t.Fatalf("absent policy decision = %+v", decision)
	}
}

func TestDecideUnresolvedDenyFailsClosedButUnresolvedAllowDoesNotMatch(t *testing.T) {
	target := domain.WriteTarget{Service: "jira", Kind: "issue", Key: "ML-1"}
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{target}}
	for _, effect := range []Effect{EffectAllow, EffectDeny} {
		rule := Rule{ID: "project-rule", Effect: effect, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}}
		decision := Decide([]Layer{{Source: "managed", Policy: Policy{Rules: []Rule{rule}}}}, request)
		want := ReasonNoMatchingAllow
		if effect == EffectDeny {
			want = ReasonScopeUnresolved
		}
		if decision.Reason != want || (effect == EffectDeny && decision.RuleID != rule.ID) {
			t.Fatalf("effect %q decision = %+v, want %q", effect, decision, want)
		}
	}
}

func TestJiraIssueIDPreflightAndAuthoritativeMatching(t *testing.T) {
	selector := Selector{Services: []string{"jira"}, Kinds: []string{"issue"}, IDs: []string{"101"}, Projects: []string{"OPS"}, Keys: []string{"OPS-1"}}
	request := func(id string) domain.WriteAuthorizationRequest {
		return domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{
			Service: "jira", Kind: "issue", ID: id, Project: "OPS", Key: "OPS-1",
		}}}
	}
	allowLayer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "allow-exact", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Resource: selector,
	}}}}
	if denial := PreflightDeny([]Layer{allowLayer}, request("")); denial != nil {
		t.Fatalf("partial allow preflight was authoritative: %+v", denial)
	}
	if decision := Decide([]Layer{allowLayer}, request("")); decision.Allowed || decision.Reason != ReasonNoMatchingAllow {
		t.Fatalf("missing id authoritative decision=%+v", decision)
	}
	if denial := PreflightDeny([]Layer{allowLayer}, request("102")); denial == nil || denial.Reason != ReasonNoMatchingAllow {
		t.Fatalf("drifted id preflight=%+v", denial)
	}
	allowed, err := NewAuthorizer(&Resolved{Layers: []Layer{allowLayer}}).Authorize(t.Context(), request("101"))
	if err != nil || !domain.HasWriteClearance(allowed) {
		t.Fatalf("exact authoritative clearance=%t err=%v", domain.HasWriteClearance(allowed), err)
	}

	denyLayer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{
		{ID: "allow-comments", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Resource: Selector{Services: []string{"jira"}, Kinds: []string{"issue"}}},
		{ID: "deny-exact", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Resource: selector},
	}}}
	if denial := PreflightDeny([]Layer{denyLayer}, request("")); denial != nil {
		t.Fatalf("partial deny preflight was authoritative: %+v", denial)
	}
	if decision := Decide([]Layer{denyLayer}, request("")); decision.Reason != ReasonScopeUnresolved || decision.Attribute != "id" || decision.RuleID != "deny-exact" {
		t.Fatalf("missing id authoritative deny decision=%+v", decision)
	}
	if denial := PreflightDeny([]Layer{denyLayer}, request("101")); denial == nil || denial.Reason != ReasonExplicitDeny || denial.RuleID != "deny-exact" {
		t.Fatalf("exact deny preflight=%+v", denial)
	}
	_, err = NewAuthorizer(&Resolved{Layers: []Layer{denyLayer}}).Authorize(t.Context(), request("101"))
	var denial *DenialError
	if !errors.As(err, &denial) || denial.Reason != ReasonExplicitDeny || denial.Details.Target.ID != "101" {
		t.Fatalf("exact authoritative denial=%+v err=%v", denial, err)
	}
}

func TestMixedJiraAndConfluenceIDSelectorMatchesCompatibleTargets(t *testing.T) {
	layer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "mixed", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbComment},
		Resource: Selector{Services: []string{"jira", "confluence"}, Kinds: []string{"issue", "page"}, IDs: []string{"101"}},
	}}}}
	for _, target := range []domain.WriteTarget{
		{Service: "jira", Kind: "issue", ID: "101", Project: "OPS", Key: "OPS-1"},
		{Service: "confluence", Kind: "page", ID: "101", Space: "DOC", AncestorIDs: []string{}},
	} {
		request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{target}}
		if decision := Decide([]Layer{layer}, request); !decision.Allowed {
			t.Fatalf("target=%+v decision=%+v", target, decision)
		}
	}
}

func TestJiraSprintAndConfluenceIDTargetFormsRemainUnchanged(t *testing.T) {
	tests := []struct {
		name     string
		selector Selector
		target   domain.WriteTarget
	}{
		{name: "Jira sprint", selector: Selector{Services: []string{"jira"}, Kinds: []string{"sprint"}, IDs: []string{"42"}}, target: domain.WriteTarget{Service: "jira", Kind: "sprint", ID: "42"}},
		{name: "Confluence page", selector: Selector{Services: []string{"confluence"}, Kinds: []string{"page"}, IDs: []string{"42"}}, target: domain.WriteTarget{Service: "confluence", Kind: "page", ID: "42", Space: "DOC", AncestorIDs: []string{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{{
				ID: "allow", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: test.selector,
			}}}}
			request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{test.target}}
			if decision := Decide([]Layer{layer}, request); !decision.Allowed {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
	emptySprint := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "sprint"}}}
	if decision := Decide([]Layer{{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "allow", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}},
	}}}}}, emptySprint); decision.Reason != reasonInvalidRequest {
		t.Fatalf("sprint without required id decision=%+v", decision)
	}
}

func FuzzJiraIssueIDTargetCanonicality(f *testing.F) {
	for _, seed := range []string{"", "101", "102", "0", "01", "not-numeric", "18446744073709551616", "18446744073709551615"} {
		f.Add(seed)
	}
	layer := Layer{Source: "managed", Policy: Policy{Rules: []Rule{{
		ID: "exact", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbComment},
		Resource: Selector{Services: []string{"jira"}, Kinds: []string{"issue"}, IDs: []string{"101"}},
	}}}}
	f.Fuzz(func(t *testing.T, id string) {
		request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{
			Service: "jira", Kind: "issue", ID: id, Project: "OPS", Key: "OPS-1",
		}}}
		decision := Decide([]Layer{layer}, request)
		switch {
		case id == "101":
			if !decision.Allowed {
				t.Fatalf("canonical exact id decision=%+v", decision)
			}
		case id == "" || domain.ValidConfluenceContentID(id):
			if decision.Allowed || decision.Reason != ReasonNoMatchingAllow {
				t.Fatalf("canonical nonmatch id=%q decision=%+v", id, decision)
			}
		default:
			if decision.Allowed || decision.Reason != reasonInvalidRequest {
				t.Fatalf("noncanonical id=%q decision=%+v", id, decision)
			}
		}
	})
}

func TestAuthorizerClearanceAndStableDenial(t *testing.T) {
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-1"}}}
	allowed, err := NewAuthorizer(nil).Authorize(context.Background(), request)
	if err != nil || !domain.HasWriteClearance(allowed) {
		t.Fatalf("allowed context clearance=%t error=%v", domain.HasWriteClearance(allowed), err)
	}
	resolved := &Resolved{Layers: []Layer{{
		Source: "managed",
		Policy: Policy{Rules: []Rule{{
			ID: "deny", Effect: EffectDeny,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: Selector{Services: []string{"jira"}},
		}}},
	}}}
	_, err = NewAuthorizer(resolved).Authorize(context.Background(), request)
	var denial *DenialError
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &denial) || denial.Reason != ReasonExplicitDeny || denial.RuleID != "deny" {
		t.Fatalf("denial = %#v, error = %v", denial, err)
	}
}

func TestAuthorizerFreezesResolvedPolicy(t *testing.T) {
	resolved := &Resolved{Layers: []Layer{{
		Source: "managed",
		Policy: Policy{Rules: []Rule{{
			ID: "allow", Effect: EffectAllow,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}},
		}}},
	}}}
	authorizer := NewAuthorizer(resolved)
	resolved.Layers[0].Policy.Rules[0].Effect = EffectDeny
	resolved.Layers[0].Policy.Rules[0].Resource.Projects[0] = "OPS"
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-1"}}}
	if _, err := authorizer.Authorize(context.Background(), request); err != nil {
		t.Fatalf("frozen authorizer changed with source value: %v", err)
	}
}

func TestDefaultDenialDetailsKeepRuleNullAndAllowedVerbsExact(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resolved := &Resolved{Layers: []Layer{{
		Source: "config_dir", Digest: digest,
		Policy: Policy{Rules: []Rule{{
			ID: "allow-update", Effect: EffectAllow,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbUpdate},
			Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}},
		}}},
	}}}
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-3"}}}
	_, err := NewAuthorizer(resolved).Authorize(context.Background(), request)
	var denial *DenialError
	if !errors.As(err, &denial) {
		t.Fatal(err)
	}
	details := denial.Details
	if details.DecidedBy.RuleID != nil || details.DecidedBy.Effect != "default_deny" || details.DecidedBy.Layer != "user" ||
		details.PolicySource != "config_dir" || details.PolicyDigest.User == nil || *details.PolicyDigest.User != digest ||
		len(details.AllowedVerbsHere) != 1 || details.AllowedVerbsHere[0] != domain.WriteVerbUpdate || details.RetrySafe ||
		strings.Contains(denial.Error(), "allow-update") {
		t.Fatalf("denial=%+v details=%+v", denial, details)
	}
}
