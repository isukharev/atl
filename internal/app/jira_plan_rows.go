package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) qualifyJiraPlanRow(ctx context.Context, parent *domain.ReadBudget, input jiraPlanDocumentRow, opts JiraPlanRunOpts, row *JiraPlanResultRow, prepared *jiraPlanPreparedRow) error {
	apply := opts.Mode == "apply"
	var maxRequests int
	var maxResponses int64
	switch row.Family {
	case "link":
		maxRequests = jiraGuardedLinkMaxRequestsPreview
		if apply {
			maxRequests = jiraGuardedLinkMaxRequestsApply
		}
		maxResponses = jiraGuardedLinkMaxResponseBytes
	case "label":
		maxRequests = jiraGuardedLabelPreviewRequests
		if apply {
			maxRequests = jiraGuardedLabelMaxRequests
		}
		maxResponses = jiraGuardedLabelMaxResponseBytes
	case "comment":
		maxRequests = jiraGuardedCommentPreviewMaxRequests
		if apply {
			maxRequests = jiraGuardedCommentApplyMaxRequests
		}
		maxResponses = jiraGuardedCommentMaxResponseBytes
	case "field":
		maxRequests = domain.JiraGuardedFieldPreviewMaxRequests
		maxResponses = domain.JiraGuardedFieldPreviewMaxResponseBytes
		if apply {
			maxRequests = domain.JiraGuardedFieldApplyMaxRequests
			maxResponses = domain.JiraGuardedFieldApplyMaxResponseBytes
		}
	}
	execution, err := newJiraGuardedExecution(ctx, parent, maxRequests, maxResponses, 60*time.Second)
	if err != nil {
		return err
	}
	prepared.execution = execution
	switch row.Family {
	case "link":
		return s.qualifyJiraPlanLink(execution, input, opts, row, prepared, apply)
	case "label":
		return s.qualifyJiraPlanLabel(execution, input, row, prepared, apply)
	case "comment":
		return s.qualifyJiraPlanComment(execution, input, row, prepared, apply)
	case "field":
		return s.qualifyJiraPlanField(execution, input, opts, row, prepared, apply)
	}
	return domain.ErrCheckFailed
}

func (s *JiraService) qualifyJiraPlanLink(execution *jiraGuardedExecution, input jiraPlanDocumentRow, opts JiraPlanRunOpts, row *JiraPlanResultRow, prepared *jiraPlanPreparedRow, apply bool) error {
	port, ok := s.tr.(domain.JiraGuardedLinkPort)
	if !ok {
		return domain.ErrConfig
	}
	linkOpts := JiraGuardedLinkOpts{Operation: "add", From: input.source, To: input.target, Type: input.typeName, Apply: apply}
	snapshot, catalog, err := s.buildJiraPlanLinkSnapshot(execution.ctx, port, linkOpts)
	if err != nil {
		return err
	}
	allowed := false
	resolved := make([]jiraPlanResolvedSelector, 0, len(opts.AllowLinkTypes))
	resolvedPairs := make(map[string]bool, len(opts.AllowLinkTypes))
	for _, selector := range opts.AllowLinkTypes {
		typ, role, resolveErr := resolveGuardedLinkType(catalog.Types, selector)
		if resolveErr != nil {
			return resolveErr
		}
		pair := typ.ID + "\x00" + role
		if resolvedPairs[pair] {
			return fmt.Errorf("%w: --allow-link-types resolves duplicate type and role", domain.ErrUsage)
		}
		resolvedPairs[pair] = true
		resolved = append(resolved, jiraPlanResolvedSelector{Selector: selector, TypeID: typ.ID, Role: role})
		if typ.ID == snapshot.result.Type.ID && role == snapshot.result.ResolvedRole {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("%w: link type is not admitted by --allow-link-types", domain.ErrUsage)
	}
	prepared.link = &jiraGuardedLinkPrepared{result: snapshot.result, firstID: snapshot.first.ID, secondID: snapshot.second.ID, sourceUpdated: snapshot.first.Updated, sourceUpdatedPresent: snapshot.first.UpdatedPresent}
	linkOpts.From, linkOpts.To = snapshot.first.Key, snapshot.second.Key
	prepared.linkOpts = linkOpts
	prepared.linkSelectors = resolved
	prepared.sourceKey, prepared.sourceProject = snapshot.first.Key, snapshot.first.Project
	prepared.targetKey, prepared.targetProject = snapshot.second.Key, snapshot.second.Project
	effect := row.Effect.(JiraPlanLinkEffect)
	effect.ResolvedTypeID, effect.ResolvedRole = snapshot.result.Type.ID, snapshot.result.ResolvedRole
	row.Effect = effect
	row.Qualified = JiraPlanLinkQualified{SourceID: snapshot.first.ID, TargetID: snapshot.second.ID, SourceProject: snapshot.first.Project, TargetProject: snapshot.second.Project, SourceUpdatedSHA256: jiraPlanUpdatedDigest(snapshot.first.Updated)}
	row.ProposalHash = snapshot.result.ProposalHash
	row.Status = "would_apply"
	row.Complete = true
	setJiraPlanRowUsage(row, execution)
	return nil
}

func (s *JiraService) buildJiraPlanLinkSnapshot(ctx context.Context, port domain.JiraGuardedLinkPort, opts JiraGuardedLinkOpts) (*jiraGuardedLinkSnapshot, domain.JiraStrictLinkCatalog, error) {
	catalog, err := port.ReadStrictLinkTypes(ctx)
	if err != nil || !catalog.Complete {
		return nil, catalog, errors.Join(err, domain.ErrCheckFailed)
	}
	catalog.Types, err = canonicalGuardedLinkCatalog(catalog.Types)
	if err != nil {
		return nil, catalog, err
	}
	selected, role, err := resolveGuardedLinkType(catalog.Types, opts.Type)
	if err != nil {
		return nil, catalog, err
	}
	first, err := port.ReadStrictLinkEndpoint(ctx, opts.From)
	if err != nil {
		return nil, catalog, err
	}
	second, err := port.ReadStrictLinkEndpoint(ctx, opts.To)
	if err != nil {
		return nil, catalog, err
	}
	if err = validateGuardedEndpointPair(first, second, first.Key, second.Key, "", ""); err != nil {
		return nil, catalog, err
	}
	outward, inward := first, second
	requestedOutward, requestedInward := first.Key, second.Key
	if role == "inward" {
		outward, inward, requestedOutward, requestedInward = second, first, second.Key, first.Key
	}
	if role == "neutral" && guardedDecimalLess(second.ID, first.ID) {
		outward, inward = second, first
	}
	if role == "neutral" && outward.Key == second.Key {
		requestedOutward, requestedInward = second.Key, first.Key
	}
	if err = validateGuardedInventoryCatalog(outward, catalog.Types); err != nil {
		return nil, catalog, err
	}
	if err = validateGuardedInventoryCatalog(inward, catalog.Types); err != nil {
		return nil, catalog, err
	}
	candidates, err := guardedLinkCandidates(outward, inward, selected)
	if err != nil {
		return nil, catalog, err
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, catalog, err
	}
	result := &JiraGuardedLinkResult{SchemaVersion: jiraGuardedLinkSchemaVersion, Operation: "add", BackendSHA256: backendHash, RequestedFrom: requestedOutward, RequestedTo: requestedInward, RequestedType: opts.Type, Outward: guardedLinkResultEndpoint(outward, "outward"), Inward: guardedLinkResultEndpoint(inward, "inward"), Type: selected, ResolvedRole: role, CatalogCount: len(catalog.Types), CatalogSHA256: guardedLinkCatalogHash(catalog.Types), Candidates: candidates, Complete: true, Mode: map[bool]string{true: "apply", false: "preview"}[opts.Apply]}
	result.ProposalHash = guardedLinkProposalHash(result)
	return &jiraGuardedLinkSnapshot{result: result, first: first, second: second, outward: outward, inward: inward}, catalog, nil
}

func (s *JiraService) qualifyJiraPlanLabel(execution *jiraGuardedExecution, input jiraPlanDocumentRow, row *JiraPlanResultRow, prepared *jiraPlanPreparedRow, apply bool) error {
	port, ok := s.tr.(domain.JiraGuardedLabelPort)
	if !ok {
		return domain.ErrConfig
	}
	labelOpts := JiraGuardedLabelOpts{}
	if input.operation == "label_add" {
		labelOpts.Add = []string{input.value}
	} else {
		labelOpts.Remove = []string{input.value}
	}
	labelOpts, err := NormalizeJiraGuardedLabelOpts(labelOpts)
	if err != nil {
		return err
	}
	labelOpts.Apply = apply
	initial, err := s.buildGuardedLabelSnapshot(execution.ctx, port, input.source, "", "", labelOpts)
	if err != nil {
		return err
	}
	prepared.label = &jiraGuardedLabelPrepared{result: initial.result, issueID: initial.evidence.ID}
	prepared.labelOpts = labelOpts
	prepared.sourceKey, prepared.sourceProject = initial.evidence.Key, initial.evidence.Project
	row.Qualified = JiraPlanSourceQualified{SourceID: initial.evidence.ID, Project: initial.evidence.Project, UpdatedSHA256: jiraPlanUpdatedDigest(initial.evidence.Updated)}
	row.ProposalHash = initial.result.ProposalHash
	row.Status = "would_apply"
	if initial.result.EffectiveAdd.Count+initial.result.EffectiveRemove.Count == 0 {
		row.Status = "already_satisfied"
	}
	row.Complete = true
	setJiraPlanRowUsage(row, execution)
	return nil
}

func (s *JiraService) qualifyJiraPlanComment(execution *jiraGuardedExecution, input jiraPlanDocumentRow, row *JiraPlanResultRow, prepared *jiraPlanPreparedRow, apply bool) error {
	port, ok := s.tr.(domain.JiraGuardedCommentPort)
	if !ok {
		return domain.ErrConfig
	}
	commentOpts := JiraCommentAddOpts{Body: []byte(input.value), Apply: apply, SatisfactionPolicy: jiraCommentSatisfactionExactBodyPresent}
	initial, err := s.buildGuardedCommentSnapshot(execution.ctx, port, input.source, "", "", commentOpts)
	if err != nil {
		return err
	}
	prepared.comment = &jiraGuardedCommentPrepared{result: initial.result, issueID: initial.issue.ID, body: append([]byte(nil), commentOpts.Body...)}
	prepared.commentOpts = commentOpts
	prepared.sourceKey, prepared.sourceProject = initial.issue.Key, initial.issue.Project
	row.Qualified = JiraPlanCommentQualified{SourceID: initial.issue.ID, Project: initial.issue.Project, UpdatedSHA256: jiraPlanUpdatedDigest(initial.issue.Updated), BaselineCount: initial.result.CurrentCount, BaselineSHA256: initial.result.BaselineSHA256, ActorSHA256: initial.result.ActorSHA256}
	row.ProposalHash = initial.result.ProposalHash
	row.Status = "would_apply"
	if initial.result.ExactBodyCount > 0 {
		row.Status = "already_satisfied"
	}
	row.Complete = true
	setJiraPlanRowUsage(row, execution)
	return nil
}

func (s *JiraService) qualifyJiraPlanField(execution *jiraGuardedExecution, input jiraPlanDocumentRow, opts JiraPlanRunOpts, row *JiraPlanResultRow, prepared *jiraPlanPreparedRow, apply bool) error {
	port, ok := s.tr.(domain.JiraGuardedFieldPort)
	if !ok {
		return domain.ErrConfig
	}
	proposals, values, allow, inputBytes, err := normalizeGuardedFieldInputs([]JiraFieldProposal{{Field: input.field, Value: cloneGuardedFieldValue(input.fieldJSON), Source: "raw", InputBytes: len(input.value)}}, opts.AllowFields)
	if err != nil {
		return err
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return err
	}
	initial, err := s.buildGuardedFieldSnapshot(execution.ctx, port, input.source, "", "", proposals, values, allow, backendHash, apply, inputBytes)
	if err != nil {
		return err
	}
	prepared.field = &jiraGuardedFieldPrepared{result: initial.result, issueID: initial.issue.ID, backendHash: backendHash, inputBytes: inputBytes, satisfied: guardedFieldValuesSatisfied(initial.issue.Fields, initial.values)}
	prepared.fieldOpts = jiraGuardedFieldExecutionOpts{apply: apply, expectedUpdated: initial.result.ActualUpdated, expectedProposalHash: initial.result.ProposalHash}
	prepared.sourceKey, prepared.sourceProject = initial.issue.Key, initial.issue.Project
	catalogData, _ := json.Marshal(initial.result.Catalog)
	effect := row.Effect.(JiraPlanFieldEffect)
	effect.PreparedBytes, effect.PreparedSHA256 = initial.result.Prepared.Bytes, initial.result.Prepared.SHA256
	row.Effect = effect
	row.Qualified = JiraPlanFieldQualified{SourceID: initial.issue.ID, Project: initial.issue.Project, UpdatedSHA256: jiraPlanUpdatedDigest(initial.issue.Updated), CatalogCount: len(initial.result.Catalog), CatalogSHA256: sha256Hex(catalogData)}
	row.ProposalHash = initial.result.ProposalHash
	row.Status = "would_apply"
	if prepared.field.satisfied {
		row.Status = "already_satisfied"
	}
	row.Complete = true
	setJiraPlanRowUsage(row, execution)
	return nil
}

func (s *JiraService) executeJiraPlanRow(row *JiraPlanResultRow, prepared *jiraPlanPreparedRow) error {
	if prepared.execution.ctx.Err() != nil {
		row.Status, row.Reason, row.Complete = "blocked", "deadline_expired", true
		setJiraPlanRowUsage(row, prepared.execution)
		return jiraPlanFailure()
	}
	var err error
	switch row.Family {
	case "link":
		prepared.linkOpts.ExpectedProposalHash = prepared.link.result.ProposalHash
		var result *JiraGuardedLinkResult
		result, err = s.guardedLinkPreparedCore(prepared.execution, s.tr.(domain.JiraGuardedLinkPort), prepared.linkOpts, prepared.link)
		row.Status, row.WriteAttempted, row.Reconciled, row.Complete = result.Status, result.WriteAttempted, result.Reconciled, result.Complete
	case "label":
		prepared.labelOpts.ExpectedProposalHash = prepared.label.result.ProposalHash
		var result *JiraGuardedLabelResult
		result, err = s.guardedLabelsPreparedCore(prepared.execution, s.tr.(domain.JiraGuardedLabelPort), prepared.sourceKey, prepared.labelOpts, prepared.label)
		row.Status, row.WriteAttempted, row.Reconciled, row.Complete = result.Status, result.WriteAttempted, result.Reconciled, result.Complete
	case "comment":
		prepared.commentOpts.ExpectedProposalHash = prepared.comment.result.ProposalHash
		var result *JiraCommentAddResult
		result, err = s.addCommentGuardedPreparedCore(prepared.execution, s.tr.(domain.JiraGuardedCommentPort), prepared.sourceKey, prepared.commentOpts, prepared.comment)
		row.Status, row.WriteAttempted, row.Reconciled, row.Complete = result.Status, result.WriteAttempted, result.Reconciled, result.Complete
	case "field":
		var result *JiraFieldSetResult
		result, err = s.setFieldsGuardedPreparedCore(prepared.execution, s.tr.(domain.JiraGuardedFieldPort), prepared.sourceKey, prepared.fieldOpts, prepared.field)
		row.Status, row.WriteAttempted, row.Reconciled, row.Complete = result.Status, result.WriteAttempted, result.Reconciled, result.Complete
		if row.Status == "unknown" {
			row.Status = "outcome_unknown"
		}
		if row.Status == "failed" {
			row.Status = "not_applied"
		}
	}
	switch row.Status {
	case "blocked":
		row.Complete = true
		if prepared.execution.ctx.Err() != nil {
			row.Reason = "deadline_expired"
		} else {
			row.Reason = "write_rejected"
		}
	case "not_applied":
		row.Reason, row.Complete = "write_rejected", true
	case "outcome_unknown":
		row.Reason = "ambiguous_outcome"
		row.Complete = false
	}
	if jiraPlanErrorAmbiguous(err) {
		row.Status, row.Reason, row.Complete = "outcome_unknown", "ambiguous_outcome", false
	}
	setJiraPlanRowUsage(row, prepared.execution)
	return err
}

func setJiraPlanRowUsage(row *JiraPlanResultRow, execution *jiraGuardedExecution) {
	usage := execution.Usage()
	row.Usage = JiraPlanUsage{usage.Attempts, usage.ResponseBytes}
}

// planValueContains is retained for the guarded field owner's structural
// comparison. Jira plan v2 itself always uses the prepared field core.
func planValueContains(current, desired any) bool {
	switch want := desired.(type) {
	case map[string]any:
		got, ok := current.(map[string]any)
		if !ok || len(got) < len(want) {
			return false
		}
		for key, value := range want {
			gotValue, exists := got[key]
			if !exists || !planValueContains(gotValue, value) {
				return false
			}
		}
		return true
	case []any:
		got, ok := current.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !planValueContains(got[i], want[i]) {
				return false
			}
		}
		return true
	default:
		currentJSON, currentErr := json.Marshal(current)
		desiredJSON, desiredErr := json.Marshal(desired)
		return currentErr == nil && desiredErr == nil && bytes.Equal(currentJSON, desiredJSON)
	}
}
