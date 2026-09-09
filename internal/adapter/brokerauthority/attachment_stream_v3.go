package brokerauthority

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BrokerAttachmentAuthorizerV3 = (*Authority)(nil)

func (a *Authority) AdmitAttachment(ctx context.Context, request domain.BrokerAttachmentAdmissionRequestV3) (domain.BrokerAttachmentAdmissionDecisionV3, error) {
	body, err := brokercontract.EncodeAttachmentAdmissionRequestV3(request)
	if err != nil {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, err
	}
	response, err := a.postAttachmentV3(ctx, brokertransport.AuthorizeAttachmentAdmissionPathV3, body, brokercontract.MaxAttachmentAuthorityCallBytesV3)
	if err != nil {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, err
	}
	decision, err := brokercontract.DecodeAttachmentAdmissionDecisionV3(response)
	if err != nil {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeAttachmentQualification(ctx context.Context, request domain.BrokerAttachmentQualificationRequestV3) (domain.BrokerAttachmentQualificationDecisionV3, error) {
	body, err := brokercontract.EncodeAttachmentQualificationRequestV3(request)
	if err != nil {
		return domain.BrokerAttachmentQualificationDecisionV3{}, err
	}
	response, err := a.postAttachmentV3(ctx, brokertransport.AuthorizeAttachmentQualificationPathV3, body, brokercontract.MaxAttachmentAuthorityCallBytesV3)
	if err != nil {
		return domain.BrokerAttachmentQualificationDecisionV3{}, err
	}
	decision, err := brokercontract.DecodeAttachmentQualificationDecisionV3(response)
	if err != nil {
		return domain.BrokerAttachmentQualificationDecisionV3{}, authorityError(err)
	}
	return decision, nil
}

func (a *Authority) AuthorizeAttachmentOperation(ctx context.Context, request domain.BrokerAttachmentOperationAuthorizationRequestV3) (domain.BrokerAttachmentOperationDecisionV3, error) {
	body, err := brokercontract.EncodeAttachmentOperationAuthorizationRequestV3(request)
	if err != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, err
	}
	response, err := a.postAttachmentV3(ctx, brokertransport.AuthorizeAttachmentOperationPathV3, body, brokercontract.MaxAttachmentAuthorityCallBytesV3)
	if err != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, err
	}
	decision, err := brokercontract.DecodeAttachmentOperationDecisionV3(response)
	if err != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, authorityError(err)
	}
	return decision, nil
}
