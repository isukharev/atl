package brokerauthority

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	projectPageAdmissionPath     = "/v2/authorize/project-page/admission"
	projectPageQualificationPath = "/v2/authorize/project-page/qualification"
	projectPageOperationPath     = "/v2/authorize/project-page/operation"
)

var _ domain.BrokerProjectPageAuthorizerV2 = (*Authority)(nil)

func (a *Authority) AdmitProjectPage(ctx context.Context, request domain.BrokerProjectPageAdmissionRequestV2) (domain.BrokerProjectPageAdmissionDecisionV2, error) {
	body, err := brokercontract.EncodeProjectPageAdmissionRequestV2(request)
	if err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, err
	}
	response, err := a.post(ctx, projectPageAdmissionPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, err
	}
	decision, err := brokercontract.DecodeProjectPageAdmissionDecisionV2(response)
	if err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeProjectPageQualification(ctx context.Context, request domain.BrokerProjectPageQualificationRequestV2) (domain.BrokerProjectPageQualificationDecisionV2, error) {
	body, err := brokercontract.EncodeProjectPageQualificationRequestV2(request)
	if err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, err
	}
	response, err := a.post(ctx, projectPageQualificationPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, err
	}
	decision, err := brokercontract.DecodeProjectPageQualificationDecisionV2(response)
	if err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeProjectPage(ctx context.Context, request domain.BrokerProjectPageOperationAuthorizationRequestV2) (domain.BrokerProjectPageOperationDecisionV2, error) {
	body, err := brokercontract.EncodeProjectPageOperationAuthorizationRequestV2(request)
	if err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, err
	}
	response, err := a.post(ctx, projectPageOperationPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, err
	}
	decision, err := brokercontract.DecodeProjectPageOperationDecisionV2(response)
	if err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, authorityError(err)
	}
	return decision, nil
}
