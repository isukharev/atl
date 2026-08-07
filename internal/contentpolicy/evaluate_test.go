package contentpolicy

import (
	"context"
	"errors"
	"strings"
	"testing"

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
		{ID: "space", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"confluence"}, Spaces: []string{"DOC"}}},
		{ID: "tree", Effect: EffectDeny, Verbs: domain.WriteVerbSet{domain.WriteVerbDelete}, Resource: Selector{Services: []string{"confluence"}, Under: []string{"10"}}},
		{ID: "jira", Effect: EffectAllow, Verbs: domain.WriteVerbSet{domain.WriteVerbUpdate}, Resource: Selector{Services: []string{"jira"}, Projects: []string{"ML"}}},
	}}}}}
	authorizer := NewAuthorizer(resolved)
	resolved.Layers[0].Policy.Rules[0].Resource.Spaces = nil
	resolved.Layers[0].Policy.Rules[1].Resource.Under[0] = "99"
	requirements := authorizer.RequiredWriteScope("confluence")
	if !requirements.Space || !requirements.Ancestors {
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
