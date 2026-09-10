package app

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	brokerAttachmentControlAttempts = brokercontract.MaxAttachmentHostOutboundAttemptsV3 - 1
	brokerAttachmentControlBytes    = brokercontract.MaxAttachmentHostResponseBytesV3 - brokercontract.MaxAttachmentNativeBodyBytesV3
	brokerAttachmentMaxTailBytes    = 128<<10 - 1
	brokerAttachmentStateLabel      = "Broker attachment stream state"
	brokerAttachmentBudgetsLabel    = "Broker attachment execution budgets"
)

// BrokerAttachmentExecutionBudgets owns the disjoint control and body budget
// roots for one execution. Usage is accounting evidence; authentication
// success and freshness remain the trusted server's responsibility.
type BrokerAttachmentExecutionBudgets struct {
	control        *domain.ReadBudget
	authentication *domain.ReadBudget
	decision       *domain.ReadBudget
	metadata       *domain.ReadBudget
	body           *domain.ReadBudget
}

func NewBrokerAttachmentExecutionBudgets() (*BrokerAttachmentExecutionBudgets, error) {
	metadataAttempts := brokercontract.MaxAttachmentJiraAttemptsV3 - 1
	if brokercontract.MaxAttachmentAuthenticationAttemptsV3+brokercontract.MaxAttachmentDecisionAttemptsV3+metadataAttempts != brokerAttachmentControlAttempts ||
		int64(brokercontract.MaxAttachmentAuthenticationAttemptsV3+brokercontract.MaxAttachmentDecisionAttemptsV3)*brokercontract.MaxAttachmentAuthorityCallBytesV3+int64(metadataAttempts)*brokercontract.MaxAttachmentMetadataResponseBytesV3 != brokerAttachmentControlBytes {
		return nil, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}
	control, err := domain.NewReadBudget(brokerAttachmentControlAttempts, brokerAttachmentControlBytes)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	authentication, err := domain.NewChildReadBudget(control, brokercontract.MaxAttachmentAuthenticationAttemptsV3, int64(brokercontract.MaxAttachmentAuthenticationAttemptsV3)*brokercontract.MaxAttachmentAuthorityCallBytesV3)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	decision, err := domain.NewChildReadBudget(control, brokercontract.MaxAttachmentDecisionAttemptsV3, int64(brokercontract.MaxAttachmentDecisionAttemptsV3)*brokercontract.MaxAttachmentAuthorityCallBytesV3)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	metadata, err := domain.NewChildReadBudget(control, metadataAttempts, int64(metadataAttempts)*brokercontract.MaxAttachmentMetadataResponseBytesV3)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	body, err := domain.NewReadBudget(1, brokercontract.MaxAttachmentNativeBodyBytesV3)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	return &BrokerAttachmentExecutionBudgets{control: control, authentication: authentication, decision: decision, metadata: metadata, body: body}, nil
}

// AuthenticationContext supplies the shared authentication category budget.
// The authority adapter derives the bounded one-attempt child for each call.
func (b *BrokerAttachmentExecutionBudgets) AuthenticationContext(ctx context.Context) (context.Context, error) {
	if b == nil || b.authentication == nil || ctx == nil {
		return nil, brokerAttachmentStreamError(domain.ErrUsage)
	}
	return domain.WithReadBudget(ctx, b.authentication), nil
}

func (b *BrokerAttachmentExecutionBudgets) validInitialUsage() bool {
	if b == nil || b.control == nil || b.authentication == nil || b.decision == nil || b.metadata == nil || b.body == nil {
		return false
	}
	authentication := b.authentication.Usage()
	return authentication.Attempts == 1 && b.control.Usage() == authentication && b.decision.Usage() == (domain.ReadBudgetUsage{}) &&
		b.metadata.Usage() == (domain.ReadBudgetUsage{}) && b.body.Usage() == (domain.ReadBudgetUsage{})
}

func (b *BrokerAttachmentExecutionBudgets) validUsage(authenticationAttempts, decisionAttempts, metadataAttempts, bodyAttempts int) bool {
	if b == nil || b.control == nil || b.authentication == nil || b.decision == nil || b.metadata == nil || b.body == nil {
		return false
	}
	authentication, decision, metadata, body := b.authentication.Usage(), b.decision.Usage(), b.metadata.Usage(), b.body.Usage()
	control := b.control.Usage()
	return authentication.Attempts == authenticationAttempts && decision.Attempts == decisionAttempts && metadata.Attempts == metadataAttempts && body.Attempts == bodyAttempts &&
		control.Attempts == authentication.Attempts+decision.Attempts+metadata.Attempts && control.ResponseBytes == authentication.ResponseBytes+decision.ResponseBytes+metadata.ResponseBytes
}

func (*BrokerAttachmentExecutionBudgets) String() string   { return brokerAttachmentBudgetsLabel }
func (*BrokerAttachmentExecutionBudgets) GoString() string { return brokerAttachmentBudgetsLabel }
func (*BrokerAttachmentExecutionBudgets) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, brokerAttachmentBudgetsLabel)
}

type BrokerJiraAttachmentStreamReader struct {
	Backend domain.BrokerBackendBinding
	Reader  domain.BrokerJiraAttachmentPortV3
}

type brokerJiraAttachmentOriginPortV3 interface {
	domain.BrokerJiraAttachmentPortV3
	BrokerOriginSHA256() (string, error)
}

type BrokerJiraAttachmentStreamService struct {
	authorizer domain.BrokerAttachmentAuthorizerV3
	jira       BrokerJiraAttachmentStreamReader
	origin     brokerJiraAttachmentOriginPortV3
	now        func() time.Time
}

func NewBrokerJiraAttachmentStreamService(authorizer domain.BrokerAttachmentAuthorizerV3, jira BrokerJiraAttachmentStreamReader) (*BrokerJiraAttachmentStreamService, error) {
	origin, ok := jira.Reader.(brokerJiraAttachmentOriginPortV3)
	if authorizer == nil || jira.Reader == nil || !ok || jira.Backend.Service != "jira" {
		return nil, brokerAttachmentStreamError(domain.ErrUsage)
	}
	return &BrokerJiraAttachmentStreamService{authorizer: authorizer, jira: jira, origin: origin, now: time.Now}, nil
}

type BrokerAttachmentStreamStart struct {
	Request                       domain.BrokerAttachmentRequestV3
	Verified                      domain.BrokerVerifiedContext
	InitialAuthenticationDeadline time.Time
	StreamID                      string
	CorrelationID                 string
	ScannerTailBytes              int
	Budgets                       *BrokerAttachmentExecutionBudgets
}

func (BrokerAttachmentStreamStart) String() string   { return "Broker attachment stream start" }
func (BrokerAttachmentStreamStart) GoString() string { return "Broker attachment stream start" }
func (BrokerAttachmentStreamStart) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker attachment stream start")
}

func (s *BrokerJiraAttachmentStreamService) Start(ctx context.Context, input BrokerAttachmentStreamStart) (*BrokerJiraAttachmentStreamOperation, error) {
	if s == nil || s.authorizer == nil || s.jira.Reader == nil || s.origin == nil || s.now == nil || ctx == nil || input.Budgets == nil ||
		input.ScannerTailBytes < 0 || input.ScannerTailBytes > brokerAttachmentMaxTailBytes || !validBrokerAttachmentStateIdentifier(input.StreamID) || !validBrokerAttachmentStateIdentifier(input.CorrelationID) {
		return nil, brokerAttachmentStreamError(domain.ErrUsage)
	}
	requestBody, err := brokercontract.EncodeAttachmentRequestV3(input.Request)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	request, err := brokercontract.DecodeAttachmentRequestV3(requestBody)
	if err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	definition, ok := brokercontract.DefinitionV3(request.Operation, request.OperationVersion)
	if !ok || request.Operation != domain.BrokerOperationJiraAttachmentDownload || input.Verified.Backend != s.jira.Backend || !input.Budgets.validInitialUsage() {
		return nil, brokerAttachmentStreamError(domain.ErrUsage)
	}
	origin, originErr := s.origin.BrokerOriginSHA256()
	if originErr != nil || origin != s.jira.Backend.OriginSHA256 {
		return nil, brokerAttachmentStreamError(firstBrokerReadError(originErr, domain.ErrCheckFailed))
	}
	if _, err := brokercontract.VerifiedContextSHA256(input.Verified); err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	startedAt := s.now()
	if err := ctx.Err(); err != nil {
		return nil, brokerAttachmentStreamError(err)
	}
	operationDeadline, deadlineMillis, err := brokerAttachmentOperationDeadline(ctx, startedAt, input.Verified)
	matchErr := brokercontract.MatchAttachmentRequestContextV3(request, input.Verified, startedAt.UnixMilli(), deadlineMillis)
	if err != nil || matchErr != nil {
		return nil, brokerAttachmentStreamError(firstBrokerReadError(err, matchErr))
	}
	authenticationLifetime := input.InitialAuthenticationDeadline.Sub(startedAt)
	if authenticationLifetime <= 0 || authenticationLifetime > time.Duration(domain.BrokerMaxDecisionLeaseMillis)*time.Millisecond {
		return nil, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}
	authenticationDeadline := startedAt.Add(authenticationLifetime)
	authenticationDeadlineMillis := authenticationDeadline.UnixMilli()
	if !startedAt.Before(authenticationDeadline) || authenticationDeadline.After(operationDeadline) || authenticationDeadlineMillis-startedAt.UnixMilli() > domain.BrokerMaxDecisionLeaseMillis ||
		authenticationDeadlineMillis > input.Verified.ExecutionExpiresMillis || authenticationDeadlineMillis > input.Verified.GrantExpiresMillis || authenticationDeadlineMillis > input.Verified.CredentialExpiresMillis {
		return nil, brokerAttachmentStreamError(domain.ErrCheckFailed)
	}

	base, cancel := context.WithDeadline(ctx, operationDeadline)
	operation := &BrokerJiraAttachmentStreamOperation{
		base: base, cancel: cancel, now: s.now, startedAt: startedAt, lastMillis: startedAt.UnixMilli(), deadlineMillis: deadlineMillis,
		authorizer: s.authorizer, jira: s.jira.Reader, budgets: input.Budgets, definition: definition, scannerTailBytes: input.ScannerTailBytes,
		state: brokerAttachmentStreamStarting,
	}
	complete := false
	defer func() {
		if !complete {
			operation.closeUnlocked()
		}
	}()
	if err := operation.setup(request, input.Verified, input.StreamID, input.CorrelationID, authenticationDeadlineMillis); err != nil {
		return nil, err
	}
	complete = true
	return operation, nil
}

func (o *BrokerJiraAttachmentStreamOperation) setup(request domain.BrokerAttachmentRequestV3, verified domain.BrokerVerifiedContext, streamID, correlationID string, authenticationDeadlineMillis int64) error {
	argumentsSHA256, err := brokercontract.AttachmentArgumentsSHA256V3(request.Arguments)
	if err != nil {
		return brokerAttachmentStreamError(err)
	}
	requestSHA256, err := brokercontract.AttachmentRequestSHA256V3(request)
	if err != nil {
		return brokerAttachmentStreamError(err)
	}
	plan := domain.BrokerAttachmentMetadataPlanV3{
		SelectorSHA256: argumentsSHA256, MetadataFields: append([]string(nil), o.definition.Definition.QualificationFields...),
		Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: brokercontract.MaxAttachmentMetadataResponseBytesV3},
	}
	if _, err := brokercontract.AttachmentMetadataPlanSHA256V3(plan); err != nil {
		return brokerAttachmentStreamError(err)
	}
	effects := brokerAttachmentEffects(o.definition.Definition.Effects)
	if _, err := brokercontract.AttachmentEffectsSHA256V3(effects); err != nil {
		return brokerAttachmentStreamError(err)
	}
	admission := domain.BrokerAttachmentAdmissionRequestV3{
		Context: verified, Operation: request.Operation, OperationVersion: request.OperationVersion, RequestID: request.RequestID,
		Features: append([]string(nil), request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: o.deadlineMillis,
	}
	admissionStarted := o.currentMillis()
	decisionCtx, decisionCancel, err := o.categoryContext(o.budgets.decision, authenticationDeadlineMillis)
	if err != nil {
		return o.fail(err)
	}
	admissionDecision, callErr := o.authorizer.AdmitAttachment(decisionCtx, admission)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr := brokercontract.ValidateAttachmentAdmissionDecisionV3(admissionDecision, admission, o.currentMillis())
	if callErr != nil || validationErr != nil {
		return o.fail(firstBrokerReadError(callErr, validationErr))
	}
	admissionDeadline := min(brokerLocalDecisionDeadline(admissionDecision.BrokerDecisionCore, admissionStarted), authenticationDeadlineMillis)

	qualification := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationInitial, Initial: &domain.BrokerAttachmentInitialQualificationV3{Admission: admission, AdmissionDecision: admissionDecision, Plan: plan}}
	if err := brokercontract.ValidateAttachmentAdmissionDecisionV3(admissionDecision, admission, o.currentMillis()); err != nil {
		return o.fail(err)
	}
	qualificationStarted := o.currentMillis()
	decisionCtx, decisionCancel, err = o.categoryContext(o.budgets.decision, admissionDeadline)
	if err != nil {
		return o.fail(err)
	}
	qualificationDecision, callErr := o.authorizer.AuthorizeAttachmentQualification(decisionCtx, qualification)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr = brokercontract.ValidateAttachmentQualificationDecisionV3(qualificationDecision, qualification, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		return o.fail(firstBrokerReadError(callErr, validationErr))
	}
	qualificationDeadline := min(brokerLocalDecisionDeadline(qualificationDecision.BrokerDecisionCore, qualificationStarted), admissionDeadline)

	metadataCtx, metadataCancel, err := o.categoryContext(o.budgets.metadata, qualificationDeadline)
	if err != nil {
		return o.fail(err)
	}
	snapshot, callErr := o.jira.QualifyBrokerJiraAttachment(metadataCtx, request.Arguments.IssueKey, request.Arguments.AttachmentID)
	if callErr == nil {
		callErr = metadataCtx.Err()
	}
	metadataCancel()
	if callErr != nil {
		return o.fail(callErr)
	}
	if _, err := brokercontract.AttachmentSnapshotSHA256V3(snapshot); err != nil {
		return o.fail(err)
	}
	if err := brokercontract.ValidateAttachmentQualificationDecisionV3(qualificationDecision, qualification, o.currentMillis(), o.deadlineMillis); err != nil {
		return o.fail(err)
	}
	initialOperation := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationInitial, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: qualification, QualificationDecision: qualificationDecision, Snapshot: snapshot, Effects: effects}}
	initialStarted := o.currentMillis()
	decisionCtx, decisionCancel, err = o.categoryContext(o.budgets.decision, qualificationDeadline)
	if err != nil {
		return o.fail(err)
	}
	initialDecision, callErr := o.authorizer.AuthorizeAttachmentOperation(decisionCtx, initialOperation)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr = brokercontract.ValidateAttachmentOperationDecisionV3(initialDecision, initialOperation, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		return o.fail(firstBrokerReadError(callErr, validationErr))
	}
	initialDeadline := min(brokerLocalDecisionDeadline(initialDecision.BrokerDecisionCore, initialStarted), qualificationDeadline)

	preOpen := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationPreOpen, PreOpen: &domain.BrokerAttachmentPreOpenQualificationV3{InitialOperation: initialOperation, InitialOperationDecision: initialDecision, Plan: plan}}
	preOpenStarted := o.currentMillis()
	decisionCtx, decisionCancel, err = o.categoryContext(o.budgets.decision, initialDeadline)
	if err != nil {
		return o.fail(err)
	}
	preOpenDecision, callErr := o.authorizer.AuthorizeAttachmentQualification(decisionCtx, preOpen)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr = brokercontract.ValidateAttachmentQualificationDecisionV3(preOpenDecision, preOpen, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		return o.fail(firstBrokerReadError(callErr, validationErr))
	}
	preOpenDeadline := min(brokerLocalDecisionDeadline(preOpenDecision.BrokerDecisionCore, preOpenStarted), initialDeadline)

	metadataCtx, metadataCancel, err = o.categoryContext(o.budgets.metadata, preOpenDeadline)
	if err != nil {
		return o.fail(err)
	}
	handle, callErr := o.jira.PrepareBrokerJiraAttachment(metadataCtx, snapshot)
	if callErr == nil {
		callErr = metadataCtx.Err()
	}
	metadataCancel()
	if callErr != nil || handle == nil {
		if handle != nil {
			_ = handle.Close()
		}
		return o.fail(firstBrokerReadError(callErr, domain.ErrCheckFailed))
	}
	o.handle = handle
	if !reflect.DeepEqual(handle.Snapshot(), snapshot) {
		return o.fail(domain.ErrCheckFailed)
	}
	if err := brokercontract.ValidateAttachmentQualificationDecisionV3(preOpenDecision, preOpen, o.currentMillis(), o.deadlineMillis); err != nil {
		return o.fail(err)
	}
	bodyOperation := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationBodyDispatch, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: preOpen, QualificationDecision: preOpenDecision, Snapshot: snapshot, Effects: effects}}
	bodyStarted := o.currentMillis()
	decisionCtx, decisionCancel, err = o.categoryContext(o.budgets.decision, preOpenDeadline)
	if err != nil {
		return o.fail(err)
	}
	bodyDecision, callErr := o.authorizer.AuthorizeAttachmentOperation(decisionCtx, bodyOperation)
	if callErr == nil {
		callErr = decisionCtx.Err()
	}
	decisionCancel()
	validationErr = brokercontract.ValidateAttachmentOperationDecisionV3(bodyDecision, bodyOperation, o.currentMillis(), o.deadlineMillis)
	if callErr != nil || validationErr != nil {
		return o.fail(firstBrokerReadError(callErr, validationErr))
	}
	bodyDeadline := min(brokerLocalDecisionDeadline(bodyDecision.BrokerDecisionCore, bodyStarted), preOpenDeadline)
	if err := o.decisionError(bodyDeadline); err != nil {
		return o.fail(err)
	}
	bodyCtx := domain.WithReadBudget(o.base, o.budgets.body)
	body, callErr := handle.Open(bodyCtx, o.timeForMillis(bodyDeadline))
	if callErr != nil || body == nil || o.budgets.body.Usage().Attempts != 1 {
		if body != nil {
			_ = body.Close()
		}
		return o.fail(firstBrokerReadError(callErr, domain.ErrCheckFailed))
	}
	o.body = body
	if !o.budgets.validUsage(1, 5, 2, 1) {
		return o.fail(domain.ErrCheckFailed)
	}

	resourcesSHA256, err := brokercontract.AttachmentResourcesSHA256V3(snapshot)
	if err != nil {
		return o.fail(err)
	}
	effectsSHA256, err := brokercontract.AttachmentEffectsSHA256V3(effects)
	if err != nil {
		return o.fail(err)
	}
	snapshotSHA256, err := brokercontract.AttachmentSnapshotSHA256V3(snapshot)
	if err != nil {
		return o.fail(err)
	}
	o.anchor = domain.BrokerAttachmentStreamAnchorV3{
		SchemaVersion: 1, ContractFamily: domain.BrokerContractFamilyExecutionV3, Operation: request.Operation, OperationVersion: request.OperationVersion,
		Features: append([]string(nil), request.Features...), Context: verified, RequestSHA256: requestSHA256, ArgumentsSHA256: argumentsSHA256,
		ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256, SnapshotSHA256: snapshotSHA256, StreamID: streamID, CorrelationID: correlationID,
		OverallDeadlineMillis: o.deadlineMillis, AdmissionDecisionSHA256: admissionDecision.DecisionSHA256, InitialQualificationDecisionSHA256: qualificationDecision.DecisionSHA256,
		InitialOperationDecisionSHA256: initialDecision.DecisionSHA256, PreOpenQualificationDecisionSHA256: preOpenDecision.DecisionSHA256, BodyDispatchOperationDecisionSHA256: bodyDecision.DecisionSHA256,
	}
	anchorSHA256, err := brokercontract.AttachmentStreamAnchorSHA256V3(o.anchor)
	if err != nil {
		return o.fail(err)
	}
	o.manifest = brokerAttachmentManifestCore(o.definition, o.anchor, snapshot, anchorSHA256)
	o.manifestSHA256, err = brokercontract.AttachmentManifestCoreSHA256V3(o.manifest)
	if err != nil {
		return o.fail(err)
	}
	o.priorReleaseSHA256, err = brokercontract.AttachmentReleaseRootSHA256V3(anchorSHA256, o.manifestSHA256)
	if err != nil {
		return o.fail(err)
	}
	o.snapshot = snapshot
	o.plan = plan
	o.committedHashState, err = newBrokerAttachmentHashState()
	if err != nil {
		return o.fail(err)
	}
	o.committedSHA256 = brokercontract.AttachmentEmptyBodySHA256V3
	o.state = brokerAttachmentStreamReady
	return nil
}

func brokerAttachmentEffects(values []domain.BrokerEffectDefinition) []domain.BrokerAttachmentEffectV3 {
	result := make([]domain.BrokerAttachmentEffectV3, len(values))
	for index, value := range values {
		result[index] = domain.BrokerAttachmentEffectV3{Kind: value.Kind, ResourceKind: value.ResourceKind, Fields: append([]string(nil), value.Fields...)}
	}
	return result
}

func brokerAttachmentManifestCore(definition domain.BrokerAttachmentOperationDefinitionV3, anchor domain.BrokerAttachmentStreamAnchorV3, snapshot domain.BrokerJiraAttachmentSnapshotV3, anchorSHA256 string) domain.BrokerAttachmentManifestCoreV3 {
	return domain.BrokerAttachmentManifestCoreV3{
		SchemaVersion: brokercontract.ExecutionSchemaVersionV3, FrameVersion: brokercontract.AttachmentFrameVersionV3, StreamID: anchor.StreamID, CorrelationID: anchor.CorrelationID,
		ArgumentsSHA256: anchor.ArgumentsSHA256, AnchorSHA256: anchorSHA256, Snapshot: snapshot, ConsistencyProfile: domain.BrokerAttachmentConsistencyStepSnapshotV1,
		MaxDataFrames: brokercontract.MaxAttachmentDataFramesV3, MaxDecodedFrameBytes: brokercontract.MaxAttachmentDecodedFrameBytesV3, MaxNativeBodyBytes: brokercontract.MaxAttachmentNativeBodyBytesV3,
		MaxMetadataItems: definition.MaxMetadataItems, MaxManifestLineBytes: definition.MaxManifestLineBytes, MaxDataLineBytes: definition.MaxDataLineBytes,
		MaxTerminalLineBytes: definition.MaxTerminalLineBytes, MaxFramedBytes: definition.MaxFramedResponseBytes, MaxJiraAttempts: definition.MaxJiraAttempts,
		MaxAuthenticationAttempts: definition.MaxAuthenticationAttempts, MaxDecisionAttempts: definition.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: definition.MaxTotalHostOutboundAttempts,
		MaxCommandHostOutboundAttempts: definition.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: definition.MaxJiraResponseBytes, MaxAuthorityResponseBytes: definition.MaxAuthorityResponseBytes,
		MaxTotalHostResponseBytes: definition.MaxTotalHostResponseBytes, MaxOperationMillis: definition.Definition.Limits.MaxOperationMillis, MaxDecisionLeaseMillis: definition.Definition.Limits.MaxDecisionLeaseMillis,
	}
}

func brokerAttachmentOperationDeadline(ctx context.Context, startedAt time.Time, verified domain.BrokerVerifiedContext) (time.Time, int64, error) {
	deadlineMillis := brokerReadDeadlineMillis(startedAt.UnixMilli(), verified, domain.BrokerMaxOperationMillis)
	parent, ok := ctx.Deadline()
	if !ok {
		return time.Time{}, 0, domain.ErrUsage
	}
	parent = startedAt.Add(parent.Sub(startedAt))
	if parentMillis := parent.UnixMilli(); parentMillis < deadlineMillis {
		deadlineMillis = parentMillis
	}
	if deadlineMillis <= startedAt.UnixMilli() {
		return time.Time{}, 0, context.DeadlineExceeded
	}
	deadline := startedAt.Add(time.Duration(deadlineMillis-startedAt.UnixMilli()) * time.Millisecond)
	return deadline, deadlineMillis, nil
}

func validBrokerAttachmentStateIdentifier(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes {
		return false
	}
	for _, current := range []byte(value) {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func brokerAttachmentStreamError(err error) error {
	if err == nil {
		return nil
	}
	return brokerOperationError("broker attachment stream failed", err)
}
