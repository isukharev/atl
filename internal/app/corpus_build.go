package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type CorpusBuildService struct {
	jira             *JiraService
	confluence       *ConfluenceService
	generatorVersion string
	buildState       corpus.BuildState
	now              func() time.Time
}

func NewCorpusBuildService(dependencies CorpusBuildDependencies) *CorpusBuildService {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	return &CorpusBuildService{
		jira: dependencies.Jira, confluence: dependencies.Confluence,
		generatorVersion: dependencies.GeneratorVersion, buildState: dependencies.BuildState, now: now,
	}
}

// Build captures every selected service sequentially under one shared budget,
// reconciles exact pristine inventories, and publishes one ready generation.
func (service *CorpusBuildService) Build(ctx context.Context, options CorpusBuildOptions) (*CorpusBuildResult, error) {
	if err := service.validateOptions(ctx, options); err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseValidate, err)
	}
	limits := corpusBuildLimits(options)
	workspace, err := openCorpusBuildWorkspace(ctx, options, limits)
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	defer func() { _ = workspace.Close() }()

	optionsDigest, err := confluenceCompleteHashJSON(corpusBuildBinding{
		JiraProject: options.JiraProject, MaxJiraIssues: options.MaxJiraIssues,
		ConfluenceSpace: options.ConfluenceSpace, MaxConfluencePages: options.MaxConfluencePages,
		MaxRequests: options.MaxRequests, MaxResponseBytes: options.MaxResponseBytes,
		MaxMembers: options.MaxMembers, MaxGenerationBytes: options.MaxGenerationBytes,
		DeadlineNanos: options.Deadline.Nanoseconds(), MaxInFlight: options.MaxInFlight,
		RequestsPerSecond: options.RequestsPerSecond,
		GeneratorVersion:  service.generatorVersion, BuildState: service.buildState,
		Evidence: corpusEvidenceBindingFromOptions(options),
	})
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseValidate, err)
	}
	active, source, err := service.prepareAttempt(ctx, workspace, options, optionsDigest)
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseRecover, err)
	}
	started, deadline, err := corpusBuildTimes(active)
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	buildCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	budget, err := domain.NewReadBudgetWithUsage(active.MaxAttempts, active.MaxResponseBytes, domain.ReadBudgetUsage{
		Attempts: active.Usage.Attempts, ResponseBytes: active.Usage.ResponseBytes,
	})
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	buildCtx = domain.WithReadBudget(buildCtx, budget)
	buildCtx = domain.WithRedactedHTTPTrace(buildCtx)

	evidence, err := newCorpusPullEvidenceOptionsWithUsage(options, active.AttachmentBodyBytes)
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhaseRecover, err)
	}
	receipts := make([]corpus.CaptureReceipt, 0, len(active.Services))
	for index := range active.Services {
		receipt, captureErr := service.captureService(buildCtx, workspace, &active, index, options, evidence, budget, limits)
		if captureErr != nil {
			return nil, captureErr
		}
		receipts = append(receipts, receipt)
	}
	var jiraRoot, confluenceRoot string
	for _, state := range active.Services {
		attemptRoot, rootErr := workspace.AttemptRoot(active.AttemptID, state.Service)
		if rootErr != nil {
			return nil, CorpusBuildFailure(CorpusBuildPhaseWorkspace, rootErr)
		}
		switch state.Service {
		case corpus.ServiceJira:
			jiraRoot = attemptRoot
		case corpus.ServiceConfluence:
			confluenceRoot = attemptRoot
		default:
			return nil, CorpusBuildFailure(CorpusBuildPhaseWorkspace, corpus.ErrIntegrity)
		}
	}

	exported, err := ExportCorpus(buildCtx, CorpusExportOptions{
		JiraRoot: jiraRoot, ConfluenceRoot: confluenceRoot,
		StoreRoot: options.Root, GeneratorVersion: service.generatorVersion, BuildState: service.buildState,
		Limits: limits, SnapshotLimits: corpusBuildSnapshotLimits(options), CaptureReceipts: receipts,
	})
	if err != nil {
		return nil, CorpusBuildFailure(CorpusBuildPhasePublish, err)
	}
	if exported.Projection.Readiness != corpus.ProjectionReady &&
		(!options.AllowPartialEvidence || exported.Projection.Readiness != corpus.ProjectionPartial) {
		return nil, CorpusBuildFailure(CorpusBuildPhasePublish, corpus.ErrIntegrity)
	}
	active.Status = corpus.BuildAttemptCompleted
	active.RemoteInFlight = false
	active.RemoteService = ""
	active.GenerationDigest = exported.Generation.GenerationDigest
	active.Usage = corpusUsage(budget.Usage())
	if err := saveCorpusBuildActive(workspace, active); err != nil {
		// The generation is already selected and verified. Failure to make the
		// completed recovery record durable cannot be reported as a definite
		// pre-publication failure; an exact rerun reconciles and reuses it.
		return nil, CorpusBuildFailure(CorpusBuildPhasePublish, errors.Join(corpus.ErrOutcomeUnknown, err))
	}

	now := service.now().UTC()
	result := &CorpusBuildResult{
		SchemaVersion: CorpusBuildSchemaV1, Source: source, Usage: active.Usage,
		ElapsedMS: maxInt64(0, now.Sub(started).Milliseconds()), Reused: exported.Reused,
		Projection: exported.Projection, Generation: exported.Generation,
		Services: make([]CorpusBuildServiceResult, 0, len(receipts)),
	}
	for _, receipt := range receipts {
		result.Services = append(result.Services, CorpusBuildServiceResult{
			Service: receipt.Service, Status: "complete", Count: receipt.Total,
			StartedAt: receipt.StartedAt, CompletedAt: receipt.CompletedAt, Usage: receipt.Usage,
			Dimensions: append([]corpus.CaptureDimensionEvidence(nil), receipt.Dimensions...),
		})
	}
	return result, nil
}

func (service *CorpusBuildService) validateOptions(ctx context.Context, options CorpusBuildOptions) error {
	if ctx == nil || service == nil || strings.TrimSpace(service.generatorVersion) == "" {
		return fmt.Errorf("%w: corpus build requires a context and generator", domain.ErrUsage)
	}
	if service.buildState != corpus.BuildStateClean && service.buildState != corpus.BuildStateModified && service.buildState != corpus.BuildStateUnknown {
		return fmt.Errorf("%w: corpus build state is invalid", domain.ErrUsage)
	}
	if err := ValidateCorpusBuildOptions(options); err != nil {
		return err
	}
	if options.JiraProject != "" && service.jira == nil {
		return fmt.Errorf("%w: selected Jira service is unavailable", domain.ErrUsage)
	}
	if options.ConfluenceSpace != "" && service.confluence == nil {
		return fmt.Errorf("%w: selected Confluence service is unavailable", domain.ErrUsage)
	}
	return nil
}

func openCorpusBuildWorkspace(ctx context.Context, options CorpusBuildOptions, limits corpus.Limits) (*corpus.BuildWorkspace, error) {
	storeOptions := corpus.Options{Limits: limits}
	if options.Initialize {
		return corpus.InitializeBuildWorkspace(ctx, options.Root, storeOptions)
	}
	return corpus.OpenBuildWorkspace(ctx, options.Root, storeOptions)
}

func (service *CorpusBuildService) prepareAttempt(ctx context.Context, workspace *corpus.BuildWorkspace, options CorpusBuildOptions, optionsDigest string) (corpus.BuildActive, string, error) {
	active, found, err := workspace.LoadActive()
	if err != nil {
		return corpus.BuildActive{}, "", err
	}
	if !found {
		if options.Restart {
			return corpus.BuildActive{}, "", fmt.Errorf("%w: no corpus build attempt exists to restart", domain.ErrUsage)
		}
		return service.beginAttempt(workspace, options, optionsDigest, "new", 0)
	}
	if active.OptionsDigest == optionsDigest {
		if err := validateCorpusBuildActiveBinding(active, options); err != nil {
			return corpus.BuildActive{}, "", err
		}
	}
	if active.Status == corpus.BuildAttemptCompleted {
		if options.Restart {
			return corpus.BuildActive{}, "", fmt.Errorf("%w: completed corpus build has no interrupted attempt to restart", domain.ErrUsage)
		}
		selected, err := workspace.SelectCurrent(ctx)
		if err != nil {
			return corpus.BuildActive{}, "", err
		}
		matches := selected.Receipt().GenerationDigest == active.GenerationDigest
		_ = selected.Close()
		if !matches {
			return corpus.BuildActive{}, "", corpus.ErrIntegrity
		}
		return service.beginAttempt(workspace, options, optionsDigest, "new", 0)
	}
	if active.Status == corpus.BuildAttemptFailed && !options.Restart {
		return corpus.BuildActive{}, "", fmt.Errorf("%w: failed corpus build attempt requires --restart", domain.ErrCheckFailed)
	}
	if active.OptionsDigest != optionsDigest && !options.Restart {
		return corpus.BuildActive{}, "", fmt.Errorf("%w: corpus build options changed; use --restart", domain.ErrCheckFailed)
	}
	if active.RemoteInFlight && !options.Restart {
		return corpus.BuildActive{}, "", errors.Join(corpus.ErrOutcomeUnknown, domain.ErrCheckFailed)
	}
	if options.Restart {
		if err := recoverCorpusBuildAttempt(workspace, active); err != nil {
			return corpus.BuildActive{}, "", err
		}
		if _, err := reconcileCorpusBuildAttachmentUsage(workspace, &active, true); err != nil {
			return corpus.BuildActive{}, "", err
		}
		carryAttachmentBytes := int64(0)
		if active.OptionsDigest == optionsDigest {
			if err := validateCorpusBuildActiveBinding(active, options); err != nil {
				return corpus.BuildActive{}, "", err
			}
			carryAttachmentBytes = active.AttachmentBodyBytes
		}
		active.Status = corpus.BuildAttemptFailed
		active.RemoteInFlight = false
		active.RemoteService = ""
		active.GenerationDigest = ""
		if err := saveCorpusBuildActive(workspace, active); err != nil {
			return corpus.BuildActive{}, "", err
		}
		return service.beginAttempt(workspace, options, optionsDigest, "restarted", carryAttachmentBytes)
	}
	if active.OptionsDigest != optionsDigest {
		return corpus.BuildActive{}, "", corpus.ErrIntegrity
	}
	migrated, err := reconcileCorpusBuildAttachmentUsage(workspace, &active, false)
	if err != nil {
		return corpus.BuildActive{}, "", err
	}
	if err := validateCorpusBuildActiveBinding(active, options); err != nil {
		return corpus.BuildActive{}, "", err
	}
	if migrated {
		if err := saveCorpusBuildActive(workspace, active); err != nil {
			return corpus.BuildActive{}, "", err
		}
	}
	return active, "resumed", nil
}

func validateCorpusBuildActiveBinding(active corpus.BuildActive, options CorpusBuildOptions) error {
	started, deadline, err := corpusBuildTimes(active)
	if err != nil || deadline.Sub(started) != options.Deadline ||
		active.MaxAttempts != options.MaxRequests || active.MaxResponseBytes != options.MaxResponseBytes ||
		active.AttachmentBodyBytes < 0 ||
		options.AttachmentBodies && active.AttachmentBodyBytes > options.MaxTotalAttachmentBytes ||
		!options.AttachmentBodies && active.AttachmentBodyBytes != 0 {
		return corpus.ErrIntegrity
	}
	_, expected, err := corpusBuildServices(options)
	if err != nil || len(active.Services) != len(expected) {
		return corpus.ErrIntegrity
	}
	for index := range expected {
		if active.Services[index].Service != expected[index].Service ||
			active.Services[index].SelectorDigest != expected[index].SelectorDigest {
			return corpus.ErrIntegrity
		}
	}
	return nil
}

func (service *CorpusBuildService) beginAttempt(workspace *corpus.BuildWorkspace, options CorpusBuildOptions, optionsDigest, source string, attachmentBodyBytes int64) (corpus.BuildActive, string, error) {
	services, states, err := corpusBuildServices(options)
	if err != nil {
		return corpus.BuildActive{}, "", err
	}
	attemptID, _, err := workspace.BeginAttempt(services)
	if err != nil {
		return corpus.BuildActive{}, "", err
	}
	started := service.now().UTC()
	active := corpus.BuildActive{
		SchemaVersion: corpus.BuildActiveSchemaV2, AttemptID: attemptID, Status: corpus.BuildAttemptActive,
		OptionsDigest: optionsDigest, Services: states,
		StartedAt: corpus.NewBuildActiveTime(started), Deadline: corpus.NewBuildActiveTime(started.Add(options.Deadline)),
		MaxAttempts: options.MaxRequests, MaxResponseBytes: options.MaxResponseBytes,
		Usage: corpus.CaptureUsage{}, AttachmentBodyBytes: attachmentBodyBytes, RemoteInFlight: false,
	}
	if err := saveCorpusBuildActive(workspace, active); err != nil {
		return corpus.BuildActive{}, "", err
	}
	return active, source, nil
}

func corpusBuildServices(options CorpusBuildOptions) ([]corpus.Service, []corpus.BuildServiceState, error) {
	services := make([]corpus.Service, 0, 2)
	states := make([]corpus.BuildServiceState, 0, 2)
	if options.ConfluenceSpace != "" {
		selector, _, err := completePullSelector(PullOpts{Space: options.ConfluenceSpace, Complete: true})
		if err != nil {
			return nil, nil, err
		}
		services = append(services, corpus.ServiceConfluence)
		states = append(states, corpus.BuildServiceState{Service: corpus.ServiceConfluence, SelectorDigest: selectorHash(selector)})
	}
	if options.JiraProject != "" {
		selector, err := jiraCompleteSelectorHash(options.JiraProject)
		if err != nil {
			return nil, nil, err
		}
		services = append(services, corpus.ServiceJira)
		states = append(states, corpus.BuildServiceState{Service: corpus.ServiceJira, SelectorDigest: selector})
	}
	return services, states, nil
}

func (service *CorpusBuildService) captureService(ctx context.Context, workspace *corpus.BuildWorkspace, active *corpus.BuildActive, index int, options CorpusBuildOptions, evidence *corpusPullEvidenceOptions, budget *domain.ReadBudget, limits corpus.Limits) (corpus.CaptureReceipt, error) {
	state := &active.Services[index]
	expectedOptionsDigest, err := service.captureOptionsDigest(state.Service, options)
	if err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	root, err := workspace.AttemptRoot(active.AttemptID, state.Service)
	if err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	existing, found, err := workspace.LoadCaptureReceipt(active.AttemptID, state.Service)
	if err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	if found {
		if err := validateAdoptedCorpusCapture(root, *state, existing, expectedOptionsDigest, active.Deadline, options, limits); err != nil {
			return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseSnapshot, err)
		}
	}
	if state.ReceiptDigest != "" && (!found || existing.ReceiptDigest != state.ReceiptDigest) {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, corpus.ErrIntegrity)
	}

	if state.StartedAt == "" {
		state.StartedAt = corpus.NewBuildActiveTime(service.now().UTC())
	}
	active.RemoteInFlight = true
	active.RemoteService = state.Service
	if err := saveCorpusBuildActive(workspace, *active); err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	scope, principalErr := service.currentScope(ctx, state.Service)
	if principalErr == nil && state.ScopeDigest != "" && state.ScopeDigest != scope {
		principalErr = &CorpusBuildError{Phase: CorpusBuildPhasePrincipal, Reason: CorpusBuildReasonDrift, cause: domain.ErrCheckFailed}
	}
	if principalErr == nil && state.ScopeDigest == "" {
		state.ScopeDigest = scope
	}

	var pullResult *CompletePullResult
	remoteErr := principalErr
	pullStartedUsage := budget.Usage()
	failurePhase := CorpusBuildPhaseCapture
	if principalErr != nil {
		failurePhase = CorpusBuildPhasePrincipal
	}
	if remoteErr == nil && !found {
		switch state.Service {
		case corpus.ServiceConfluence:
			pullResult, remoteErr = service.captureConfluence(ctx, root, options, evidence)
		case corpus.ServiceJira:
			pullResult, remoteErr = service.captureJira(ctx, root, options, evidence)
		default:
			remoteErr = corpus.ErrIntegrity
		}
	}
	attachmentBodyBytes := active.AttachmentBodyBytes
	if evidence != nil && evidence.budget != nil {
		attachmentBodyBytes = evidence.budget.usage()
	}
	attachmentUsageErr := reconcileCorpusBuildServiceAttachmentUsage(root, active, index, attachmentBodyBytes)
	if attachmentUsageErr != nil {
		remoteErr = errors.Join(remoteErr, attachmentUsageErr)
	}
	after := budget.Usage()
	delta := corpus.CaptureUsage{Attempts: after.Attempts - pullStartedUsage.Attempts, ResponseBytes: after.ResponseBytes - pullStartedUsage.ResponseBytes}
	active.Usage = corpusUsage(after)
	if !found {
		state.Usage.Attempts += delta.Attempts
		state.Usage.ResponseBytes += delta.ResponseBytes
	}
	active.RemoteInFlight = attachmentUsageErr != nil
	if attachmentUsageErr == nil {
		active.RemoteService = ""
	}
	if saveErr := saveCorpusBuildActive(workspace, *active); saveErr != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, errors.Join(remoteErr, saveErr))
	}
	if remoteErr != nil {
		var closed *CorpusBuildError
		if errors.As(remoteErr, &closed) {
			return corpus.CaptureReceipt{}, remoteErr
		}
		return corpus.CaptureReceipt{}, CorpusBuildFailure(failurePhase, remoteErr)
	}
	if found {
		state.ReceiptDigest = existing.ReceiptDigest
		if err := saveCorpusBuildActive(workspace, *active); err != nil {
			return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
		}
		return existing, nil
	}

	receipt, err := buildCorpusCaptureReceipt(ctx, root, *state, expectedOptionsDigest, pullResult, options, limits, service.now().UTC())
	if err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseSnapshot, err)
	}
	if err := workspace.SaveCaptureReceipt(active.AttemptID, receipt); err != nil {
		if errors.Is(err, corpus.ErrOutcomeUnknown) {
			persisted, persistedFound, loadErr := workspace.LoadCaptureReceipt(active.AttemptID, state.Service)
			if loadErr == nil && persistedFound && reflect.DeepEqual(persisted, receipt) {
				// The exact receipt is visible, but the first call may have
				// stopped before its parent-directory durability barrier.
				err = workspace.SaveCaptureReceipt(active.AttemptID, receipt)
			}
		}
		if err != nil {
			return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
		}
	}
	state.ReceiptDigest = receipt.ReceiptDigest
	if err := saveCorpusBuildActive(workspace, *active); err != nil {
		return corpus.CaptureReceipt{}, CorpusBuildFailure(CorpusBuildPhaseWorkspace, err)
	}
	return receipt, nil
}

func (service *CorpusBuildService) currentScope(ctx context.Context, selected corpus.Service) (string, error) {
	var baseURL, principal string
	var err error
	switch selected {
	case corpus.ServiceConfluence:
		baseURL = service.confluence.baseURL
		principal, err = service.confluence.CurrentPrincipal(ctx)
	case corpus.ServiceJira:
		baseURL = service.jira.baseURL
		principal, err = service.jira.CurrentPrincipal(ctx)
	default:
		return "", corpus.ErrIntegrity
	}
	if err != nil {
		return "", err
	}
	origin, err := backendid.OriginSHA256(baseURL)
	if err != nil {
		return "", err
	}
	return corpus.PrincipalScopeDigest(selected, origin, principal)
}

func (service *CorpusBuildService) captureConfluence(ctx context.Context, root string, options CorpusBuildOptions, evidence *corpusPullEvidenceOptions) (*CompletePullResult, error) {
	pull := corpusBuildConfluencePullOptions(root, options, evidence)
	result, err := service.confluence.Pull(ctx, pull)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Complete == nil {
		return nil, corpus.ErrIntegrity
	}
	return result.Complete, nil
}

func (service *CorpusBuildService) captureJira(ctx context.Context, root string, options CorpusBuildOptions, evidence *corpusPullEvidenceOptions) (*CompletePullResult, error) {
	pull := corpusBuildJiraPullOptions(root, options, evidence)
	result, err := service.jira.Pull(ctx, pull)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Complete == nil {
		return nil, corpus.ErrIntegrity
	}
	return result.Complete, nil
}

func (service *CorpusBuildService) captureOptionsDigest(selected corpus.Service, options CorpusBuildOptions) (string, error) {
	switch selected {
	case corpus.ServiceConfluence:
		pull := corpusBuildConfluencePullOptions("", options, newCorpusPullEvidenceOptions(options))
		return completePullOptionsHash(service.confluence.cfg, pull, *pull.exactRender)
	case corpus.ServiceJira:
		pull := corpusBuildJiraPullOptions("", options, newCorpusPullEvidenceOptions(options))
		return jiraCompleteOptionsHash(pull, jiraCompletePullFields(pull, nil, *pull.exactRender), viewStateOf(*pull.exactRender))
	default:
		return "", corpus.ErrIntegrity
	}
}

func corpusBuildConfluencePullOptions(root string, options CorpusBuildOptions, evidence *corpusPullEvidenceOptions) PullOpts {
	settings := corpusBuildRenderSettings("confluence")
	return PullOpts{
		Space: options.ConfluenceSpace, Into: root, Complete: true, MaxPages: options.MaxConfluencePages,
		PagePrefetch: options.MaxInFlight, RequestsPerSecond: options.RequestsPerSecond,
		exactRender: &settings, Comments: options.Comments, evidence: evidence,
	}
}

func corpusBuildJiraPullOptions(root string, options CorpusBuildOptions, evidence *corpusPullEvidenceOptions) JiraPullOpts {
	settings := corpusBuildRenderSettings("jira")
	return JiraPullOpts{
		Project: options.JiraProject, Into: root, Complete: true, MaxIssues: options.MaxJiraIssues,
		exactRender: &settings,
		exactFields: corpusBuildJiraFields(options), evidence: evidence,
	}
}

func corpusBuildJiraFields(options CorpusBuildOptions) []string {
	fields := []string{"summary", "description", "project"}
	if options.Comments || options.Attachments {
		fields = append(fields, "updated")
	}
	fields = append(fields, "issuelinks")
	return fields
}

func buildCorpusCaptureReceipt(ctx context.Context, root string, state corpus.BuildServiceState, optionsDigest string, complete *CompletePullResult, options CorpusBuildOptions, limits corpus.Limits, completed time.Time) (corpus.CaptureReceipt, error) {
	if complete == nil || !complete.Complete || complete.CheckpointActive || complete.Remaining != 0 ||
		complete.Completed != complete.Total || complete.Total < 0 || complete.SelectorSHA256 != state.SelectorDigest {
		return corpus.CaptureReceipt{}, corpus.ErrIntegrity
	}
	snapshot, err := mirror.New(root).BeginCorpusSnapshot(string(state.Service), mirror.CorpusSnapshotOptions{
		Limits: mirror.CorpusSnapshotLimits{MaxItems: limits.MaxMembers, MaxTotalBytes: limits.MaxTotalBytes},
	})
	if err != nil {
		return corpus.CaptureReceipt{}, err
	}
	if !snapshot.Reconciled() || snapshot.Len() != complete.Total {
		return corpus.CaptureReceipt{}, corpus.ErrIntegrity
	}
	providerIDs := make([]string, 0, snapshot.Len())
	for _, item := range snapshot.Inventory() {
		providerIDs = append(providerIDs, item.ProviderID)
	}
	selection, err := corpus.CaptureSelectionDigest(state.Service, providerIDs)
	if err != nil || selection != complete.SelectionSHA256 {
		return corpus.CaptureReceipt{}, corpus.ErrIntegrity
	}
	if err := snapshot.Revalidate(); err != nil {
		return corpus.CaptureReceipt{}, err
	}
	dimensions, err := corpusCaptureDimensionsForSnapshot(snapshot, options)
	if err != nil {
		return corpus.CaptureReceipt{}, err
	}
	started, err := time.Parse(time.RFC3339Nano, state.StartedAt)
	if err != nil || ctx.Err() != nil {
		return corpus.CaptureReceipt{}, errors.Join(err, ctx.Err())
	}
	return corpus.BuildCaptureReceipt(corpus.CaptureReceiptInput{
		Service: state.Service, ScopeDigest: state.ScopeDigest,
		SelectorDigest: state.SelectorDigest, OptionsDigest: optionsDigest,
		SelectionDigest: selection, SnapshotDigest: snapshot.Fingerprint(),
		StartedAt: started, CompletedAt: completed, Total: complete.Total, Completed: complete.Completed,
		Usage: state.Usage, Dimensions: dimensions,
	}, limits)
}

func validateAdoptedCorpusCapture(root string, state corpus.BuildServiceState, receipt corpus.CaptureReceipt, expectedOptionsDigest, attemptDeadline string, options CorpusBuildOptions, limits corpus.Limits) error {
	completed, completedErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	deadline, deadlineErr := time.Parse(time.RFC3339Nano, attemptDeadline)
	if receipt.Service != state.Service || receipt.SelectorDigest != state.SelectorDigest ||
		state.ScopeDigest == "" || receipt.ScopeDigest != state.ScopeDigest || receipt.StartedAt != state.StartedAt ||
		receipt.Usage != state.Usage || receipt.OptionsDigest != expectedOptionsDigest ||
		completedErr != nil || deadlineErr != nil || completed.After(deadline) {
		return corpus.ErrIntegrity
	}
	snapshot, err := mirror.New(root).BeginCorpusSnapshot(string(state.Service), mirror.CorpusSnapshotOptions{
		Limits: mirror.CorpusSnapshotLimits{MaxItems: limits.MaxMembers, MaxTotalBytes: limits.MaxTotalBytes},
	})
	if err != nil {
		return err
	}
	dimensions, err := corpusCaptureDimensionsForSnapshot(snapshot, options)
	if err != nil || !reflect.DeepEqual(receipt.Dimensions, dimensions) {
		return corpus.ErrIntegrity
	}
	return validateCorpusCaptureSource(snapshot, receipt, limits)
}

func recoverCorpusBuildAttempt(workspace *corpus.BuildWorkspace, active corpus.BuildActive) error {
	for _, state := range active.Services {
		root, err := workspace.AttemptRoot(active.AttemptID, state.Service)
		if err != nil {
			return err
		}
		m := mirror.New(root)
		checkpoint, found, err := m.CompletePullCheckpoint(state.SelectorDigest)
		if err != nil {
			return err
		}
		if err := m.RecoverCompletePullPublication(state.SelectorDigest, checkpoint, found); err != nil {
			return err
		}
		if _, err := m.RecoverCompletePullJournal(state.SelectorDigest, checkpoint, found); err != nil {
			return err
		}
	}
	return nil
}

func saveCorpusBuildActive(workspace *corpus.BuildWorkspace, active corpus.BuildActive) error {
	err := workspace.SaveActive(active)
	if !errors.Is(err, corpus.ErrOutcomeUnknown) {
		return err
	}
	loaded, found, loadErr := workspace.LoadActive()
	if loadErr == nil && found && reflect.DeepEqual(loaded, active) {
		// An exact readback resolves identity, not directory durability.
		// Replacing the same content once more is a bounded local-only
		// reconciliation that reaches the normal fsync barrier or keeps the
		// outcome unknown.
		return workspace.SaveActive(active)
	}
	return errors.Join(err, loadErr)
}

func corpusBuildTimes(active corpus.BuildActive) (time.Time, time.Time, error) {
	started, err := time.Parse(time.RFC3339Nano, active.StartedAt)
	if err != nil {
		return time.Time{}, time.Time{}, corpus.ErrIntegrity
	}
	deadline, err := time.Parse(time.RFC3339Nano, active.Deadline)
	if err != nil || !deadline.After(started) {
		return time.Time{}, time.Time{}, corpus.ErrIntegrity
	}
	return started, deadline, nil
}

func corpusBuildLimits(options CorpusBuildOptions) corpus.Limits {
	memberBytes := options.MaxGenerationBytes
	if maximum := corpus.DefaultLimits().MaxMemberBytes; memberBytes > maximum {
		memberBytes = maximum
	}
	return corpus.Limits{
		MaxMembers: options.MaxMembers, MaxMemberBytes: memberBytes, MaxTotalBytes: options.MaxGenerationBytes,
	}
}

func corpusBuildSnapshotLimits(options CorpusBuildOptions) mirror.CorpusSnapshotLimits {
	return mirror.CorpusSnapshotLimits{MaxItems: options.MaxMembers, MaxTotalBytes: options.MaxGenerationBytes}
}

func corpusUsage(usage domain.ReadBudgetUsage) corpus.CaptureUsage {
	return corpus.CaptureUsage{Attempts: usage.Attempts, ResponseBytes: usage.ResponseBytes}
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
