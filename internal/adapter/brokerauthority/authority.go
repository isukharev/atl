// Package brokerauthority implements the Broker workload-authentication and
// policy-decision ports against one fixed HTTPS authority.
package brokerauthority

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const (
	authenticatePath            = "/v1/authenticate"
	admissionPath               = "/v1/authorize/admission"
	qualificationPath           = "/v1/authorize/qualification"
	operationPath               = "/v1/authorize/operation"
	maxAuthorityDecisionBytes   = int64(128 << 10)
	maxAuthorityCredentialBytes = 8 << 10
	authorityCallTimeout        = 5 * time.Second
)

type Config struct {
	BaseURL          string
	ServerCredential string
	IssuerSHA256     string
	Version          string
	Scheduler        *httpx.Scheduler
	TLS              httpx.TLSOptions
}

type Authority struct {
	client       *httpx.Client
	issuerSHA256 string
	now          func() time.Time
}

var (
	_ brokertransport.Authenticator = (*Authority)(nil)
	_ domain.BrokerAuthorizer       = (*Authority)(nil)
)

func New(config Config) (*Authority, error) {
	parsed, parseErr := url.Parse(config.BaseURL)
	_, originErr := backendid.OriginSHA256(config.BaseURL)
	if parseErr != nil || originErr != nil || parsed.Scheme != "https" || !parsed.IsAbs() || parsed.Hostname() == "" || strings.TrimSpace(config.BaseURL) != config.BaseURL || !validCredential(config.ServerCredential) || !validDigest(config.IssuerSHA256) {
		return nil, fmt.Errorf("%w: invalid Broker authority configuration", domain.ErrConfig)
	}
	client, err := httpx.NewWithSchedulerTLS(config.BaseURL, config.ServerCredential, config.Version, config.Scheduler, config.TLS)
	if err != nil {
		return nil, authorityError(err)
	}
	return &Authority{client: client, issuerSHA256: config.IssuerSHA256, now: time.Now}, nil
}

func (a *Authority) CloseIdleConnections() {
	if a != nil && a.client != nil {
		a.client.CloseIdleConnections()
	}
}

func (a *Authority) Authenticate(ctx context.Context, credential []byte, challenge brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	if a == nil || a.client == nil || a.now == nil {
		return brokertransport.Authentication{}, authorityError(domain.ErrConfig)
	}
	startedAt := a.now()
	request, err := brokertransport.NewAuthenticationRequest(credential, challenge)
	if err != nil {
		return brokertransport.Authentication{}, err
	}
	defer request.Clear()
	body, err := brokertransport.EncodeAuthenticationRequestV1(request)
	if err != nil {
		return brokertransport.Authentication{}, err
	}
	responseBody, err := a.post(ctx, authenticatePath, body, brokertransport.MaxAuthorityEnvelopeBytes)
	if err != nil {
		return brokertransport.Authentication{}, err
	}
	response, err := brokertransport.DecodeAuthenticationResponseV1(responseBody)
	observedAt := a.now()
	if err != nil || brokertransport.ValidateAuthenticationResponseForV1(response, request, a.issuerSHA256, observedAt) != nil {
		return brokertransport.Authentication{}, authorityError(domain.ErrAuth)
	}
	deadline := observedAt.Add(time.Duration(response.ExpiresAtMillis-observedAt.UnixMilli()) * time.Millisecond)
	leaseDeadline := startedAt.Add(time.Duration(response.ExpiresAtMillis-response.IssuedAtMillis) * time.Millisecond)
	if leaseDeadline.Before(deadline) {
		deadline = leaseDeadline
	}
	return brokertransport.Authentication{Context: response.Context, ReleaseDeadline: deadline}, nil
}

func (a *Authority) Admit(ctx context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	body, err := brokercontract.EncodeAdmissionRequestV1(request)
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	response, err := a.post(ctx, admissionPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	decision, err := brokercontract.DecodeAdmissionDecisionV1(response)
	if err != nil {
		return domain.BrokerAdmissionDecision{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeQualification(ctx context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	body, err := brokercontract.EncodeQualificationRequestV1(request)
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	response, err := a.post(ctx, qualificationPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	decision, err := brokercontract.DecodeQualificationDecisionV1(response)
	if err != nil {
		return domain.BrokerQualificationDecision{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeOperation(ctx context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	body, err := brokercontract.EncodeOperationAuthorizationRequestV1(request)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	response, err := a.post(ctx, operationPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	decision, err := brokercontract.DecodeOperationDecisionV1(response)
	if err != nil {
		return domain.BrokerOperationDecision{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) post(ctx context.Context, path string, body []byte, maximum int64) ([]byte, error) {
	if a == nil || a.client == nil || len(body) == 0 || maximum <= 0 {
		return nil, authorityError(domain.ErrConfig)
	}
	budget, err := domain.NewReadBudget(1, maximum)
	if err != nil {
		return nil, authorityError(err)
	}
	bounded, cancel := context.WithTimeout(ctx, authorityCallTimeout)
	defer cancel()
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	response, err := a.client.DoWithBodyLimit(requestContext, http.MethodPost, path, body, nil, maximum)
	if err != nil {
		return nil, authorityError(err)
	}
	return response, nil
}

type transportError struct {
	cause error
}

func (*transportError) Error() string   { return "Broker authority request failed" }
func (e *transportError) Unwrap() error { return e.cause }

func authorityError(err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return safe
	}
	var causes []error
	for _, sentinel := range []error{
		domain.ErrUsage, domain.ErrAuth, domain.ErrForbidden, domain.ErrNotFound,
		domain.ErrConfig, domain.ErrCheckFailed, domain.ErrReadAttemptBudgetExhausted,
		domain.ErrReadResponseBudgetExhausted, context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	return &transportError{cause: errors.Join(causes...)}
}

func validCredential(value string) bool {
	if len(value) < 8 || len(value) > maxAuthorityCredentialBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return false
		}
	}
	return true
}
