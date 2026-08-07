package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/domain"
)

// processPolicy owns the one immutable policy resolution used by a command
// tree. Production creates one tree per process; tests may create more than one
// tree so each test invocation can project an isolated environment.
type processPolicy struct {
	resolver *contentpolicy.Resolver
	required bool

	once       sync.Once
	resolved   *contentpolicy.Resolved
	authorizer *contentpolicy.Authorizer
	err        error
}

func newProcessPolicy() *processPolicy {
	return &processPolicy{
		resolver: contentpolicy.NewResolver(config.Dir(), contentpolicy.Environment{
			Inline:           os.Getenv("ATL_POLICY"),
			File:             os.Getenv("ATL_POLICY_FILE"),
			FileSHA256:       os.Getenv("ATL_POLICY_SHA256"),
			ExpectedOwnerUID: processPolicyOwnerUID(),
		}),
		required: envBool("ATL_POLICY_REQUIRED"),
	}
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (p *processPolicy) resolve() (*contentpolicy.Resolved, error) {
	if p == nil {
		return &contentpolicy.Resolved{}, nil
	}
	p.once.Do(func() {
		p.resolved, p.err = p.resolver.Resolve()
		if p.err != nil {
			return
		}
		if len(p.resolved.Layers) != 0 {
			p.authorizer = contentpolicy.NewAuthorizer(p.resolved)
		}
	})
	return p.resolved, p.err
}

func (p *processPolicy) active() bool {
	resolved, err := p.resolve()
	return err == nil && resolved != nil && len(resolved.Layers) != 0
}

func (p *processPolicy) requireActiveFor(registration commandRegistration) error {
	resolved, err := p.resolve()
	if err != nil {
		return fmt.Errorf("%w: load content policy: %v", domain.ErrConfig, err)
	}
	if p.required && registration.policyIdentity != policyIdentityNone && len(resolved.Layers) == 0 {
		return contentpolicy.NewSourceDenial(
			contentpolicy.ReasonPolicyRequiredAbsent,
			"content policy is required for governed writes but no policy is active",
			"required",
			resolved,
		)
	}
	return nil
}

func (p *processPolicy) authorizerFor(service, rawURL string) (domain.WriteAuthorizer, error) {
	resolved, err := p.resolve()
	if err != nil {
		return nil, fmt.Errorf("%w: load content policy: %v", domain.ErrConfig, err)
	}
	if len(resolved.Layers) == 0 {
		if p.required {
			return nil, contentpolicy.NewSourceDenial(
				contentpolicy.ReasonPolicyRequiredAbsent,
				"content policy is required for governed writes but no policy is active",
				"required",
				resolved,
			)
		}
		return nil, nil
	}
	digest, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		return nil, err
	}
	for _, layer := range resolved.Layers {
		var want string
		switch service {
		case "jira":
			want = layer.Policy.Backend.JiraSHA256
		case "confluence":
			want = layer.Policy.Backend.ConfluenceSHA256
		default:
			return nil, fmt.Errorf("%w: unsupported policy backend %q", domain.ErrConfig, service)
		}
		if want == "" && !p.required {
			continue
		}
		if want == "" || want != digest {
			return nil, contentpolicy.NewSourceDenial(
				contentpolicy.ReasonBackendMismatch,
				"content policy backend binding does not match the configured backend",
				layer.Source,
				resolved,
			)
		}
	}
	return p.authorizer, nil
}

func policyAuthorizerFor(service, rawURL string) (domain.WriteAuthorizer, error) {
	if currentProcessPolicy == nil {
		return nil, nil
	}
	return currentProcessPolicy.authorizerFor(service, rawURL)
}
