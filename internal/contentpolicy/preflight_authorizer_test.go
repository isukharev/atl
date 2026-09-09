package contentpolicy

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestPreflightAuthorizerReturnsNilOnlyWithoutDenial(t *testing.T) {
	request := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", ID: "101"}}}
	for _, test := range []struct {
		name   string
		effect Effect
		id     string
		denied bool
	}{
		{name: "unconfigured"},
		{name: "exact allow", effect: EffectAllow, id: "101"},
		{name: "explicit deny", effect: EffectDeny, id: "101", denied: true},
		{name: "nonmatching allow", effect: EffectAllow, id: "102", denied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved := &Resolved{}
			if test.effect != "" {
				resolved.Layers = []Layer{{Source: "managed", Policy: Policy{Rules: []Rule{{ID: "one-issue", Effect: test.effect, Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Resource: Selector{Services: []string{"jira"}, Kinds: []string{"issue"}, IDs: []string{test.id}}}}}}}
			}
			var authorizer domain.WritePreflightAuthorizer = NewAuthorizer(resolved)
			err := authorizer.Preflight(request)
			if !test.denied {
				if err != nil {
					t.Fatalf("no denial must be a nil error interface, got %T", err)
				}
				return
			}
			var denial *DenialError
			if !errors.As(err, &denial) || denial == nil || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("lost typed denial: %v", err)
			}
		})
	}
}
