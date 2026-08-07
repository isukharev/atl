package cli

import (
	"errors"
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

// policyRelevantEnvironment is the launcher allowlist contract. It includes
// direct product reads plus Go transport variables that can redirect or trust
// a peer without changing the configured backend origin.
var policyRelevantEnvironment = []string{
	"PATH", "HOME", "USERPROFILE", "XDG_CONFIG_HOME",
	"ATL_CONFIG_DIR", "ATL_JIRA_URL", "JIRA_URL", "ATL_CONFLUENCE_URL", "CONFLUENCE_URL", "ATL_UPDATE_URL",
	"ATL_JIRA_CA_BUNDLE", "ATL_CONFLUENCE_CA_BUNDLE",
	"ATL_JIRA_PAT", "JIRA_PAT", "ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_INTEGRATION", "TEST_JIRA_PAT", "TEST_CONFLUENCE_PAT",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"ATL_ALLOW_INSECURE", "ATL_READ_ONLY", "ATL_MIRROR_ROOT", "ATL_NO_UPDATE", "ATL_UPDATE_DEBUG", "ATL_VERBOSE",
	"ATL_POLICY", "ATL_POLICY_FILE", "ATL_POLICY_SHA256", "ATL_POLICY_REQUIRED",
}

func newProcessPolicy() *processPolicy {
	policy := &processPolicy{
		resolver: contentpolicy.NewResolver(config.Dir(), contentpolicy.Environment{
			Inline:           os.Getenv("ATL_POLICY"),
			File:             os.Getenv("ATL_POLICY_FILE"),
			FileSHA256:       os.Getenv("ATL_POLICY_SHA256"),
			ExpectedOwnerUID: processPolicyOwnerUID(),
		}),
		required: envBool("ATL_POLICY_REQUIRED"),
	}
	// Snapshot source bytes and failures while constructing the process command
	// tree. Ungoverned reads ignore the frozen result; governed writes and policy
	// diagnostics consume it without giving later code control of the timing.
	_, _ = policy.resolve()
	return policy
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

func (p *processPolicy) requireActiveFor(registration commandRegistration) error {
	if registration.policyIdentity == policyIdentityNone {
		return nil
	}
	resolved, err := p.resolve()
	if err != nil {
		return classifyProcessPolicyLoadError(err)
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
		return nil, classifyProcessPolicyLoadError(err)
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
	if strings.TrimSpace(rawURL) == "" {
		// Preserve the app constructor's established missing-backend config
		// classification; no backend exists to bind or write.
		return p.authorizer, nil
	}
	digest, err := backendid.OriginSHA256(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s backend origin", domain.ErrConfig, service)
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

func classifyProcessPolicyLoadError(err error) error {
	var denial interface{ DiagnosticPolicyDenial() bool }
	if errors.As(err, &denial) && denial.DiagnosticPolicyDenial() {
		return err
	}
	return fmt.Errorf("%w: load content policy: %w", domain.ErrConfig, err)
}

func policyAuthorizerFor(service, rawURL string) (domain.WriteAuthorizer, error) {
	if currentProcessPolicy == nil || !currentCommandPolicyWrite {
		return nil, nil
	}
	return currentProcessPolicy.authorizerFor(service, rawURL)
}
