package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraGuardedLinkSchemaVersion            = 1
	jiraGuardedLinkMaxRequestsPreview       = 3
	jiraGuardedLinkMaxRequestsApply         = 9
	jiraGuardedLinkMaxResponseBytes   int64 = 16 << 20
	jiraGuardedLinkDeadline                 = 60 * time.Second
	jiraGuardedLinkSelectorMaxBytes         = 1024
	jiraGuardedLinkReferenceMaxBytes        = 64
)

type JiraGuardedLinkOpts struct {
	Operation            string
	From                 string
	To                   string
	Type                 string
	LinkID               string
	Apply                bool
	ExpectedProposalHash string
}

type JiraGuardedLinkEndpoint struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Project string `json:"project"`
	Role    string `json:"role"`
}

type JiraGuardedLinkCandidate struct {
	ID              string `json:"id"`
	OutwardEvidence bool   `json:"outward_evidence"`
	InwardEvidence  bool   `json:"inward_evidence"`
}

type JiraGuardedLinkResult struct {
	SchemaVersion   int                         `json:"schema_version"`
	Operation       string                      `json:"operation"`
	BackendSHA256   string                      `json:"backend_sha256"`
	RequestedFrom   string                      `json:"requested_from"`
	RequestedTo     string                      `json:"requested_to"`
	RequestedType   string                      `json:"requested_type"`
	RequestedLinkID string                      `json:"requested_link_id,omitempty"`
	Outward         JiraGuardedLinkEndpoint     `json:"outward"`
	Inward          JiraGuardedLinkEndpoint     `json:"inward"`
	Type            domain.JiraLinkTypeMetadata `json:"type"`
	ResolvedRole    string                      `json:"resolved_role"`
	CatalogCount    int                         `json:"catalog_count"`
	CatalogSHA256   string                      `json:"catalog_sha256"`
	Candidates      []JiraGuardedLinkCandidate  `json:"candidates"`
	ProposalHash    string                      `json:"proposal_hash"`
	Mode            string                      `json:"mode"`
	Status          string                      `json:"status"`
	WriteAttempted  bool                        `json:"write_attempted"`
	Reconciled      bool                        `json:"reconciled,omitempty"`
	Complete        bool                        `json:"complete"`
}

type jiraGuardedLinkSnapshot struct {
	result  *JiraGuardedLinkResult
	first   domain.JiraStrictLinkEndpoint
	second  domain.JiraStrictLinkEndpoint
	outward domain.JiraStrictLinkEndpoint
	inward  domain.JiraStrictLinkEndpoint
}

// jiraGuardedLinkPrepared is the content-minimized boundary between initial
// qualification and execution. It retains no endpoint link inventory.
type jiraGuardedLinkPrepared struct {
	result               *JiraGuardedLinkResult
	firstID              string
	secondID             string
	sourceUpdated        string
	sourceUpdatedPresent bool
}

type jiraGuardedLinkError struct {
	message   string
	cause     error
	ambiguous bool
}

func (e *jiraGuardedLinkError) Error() string { return e.message }
func (e *jiraGuardedLinkError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, true)
}
func (e *jiraGuardedLinkError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }

// GuardedLink previews or applies one exact link add/delete under a shared
// physical request, aggregate response-byte, and absolute deadline boundary.
func (s *JiraService) GuardedLink(ctx context.Context, opts JiraGuardedLinkOpts) (*JiraGuardedLinkResult, error) {
	opts.Operation = strings.TrimSpace(opts.Operation)
	opts.From = strings.ToUpper(strings.TrimSpace(opts.From))
	opts.To = strings.ToUpper(strings.TrimSpace(opts.To))
	opts.Type = strings.TrimSpace(opts.Type)
	opts.LinkID = strings.TrimSpace(opts.LinkID)
	if err := ValidateJiraGuardedLinkOpts(opts); err != nil {
		return nil, err
	}
	port, ok := s.tr.(domain.JiraGuardedLinkPort)
	if !ok {
		return nil, fmt.Errorf("%w: configured Jira backend does not support guarded links", domain.ErrCheckFailed)
	}
	maxRequests := jiraGuardedLinkMaxRequestsPreview
	if opts.Apply {
		maxRequests = jiraGuardedLinkMaxRequestsApply
	}
	execution, err := newJiraGuardedExecution(ctx, domain.ReadBudgetFromContext(ctx), maxRequests, jiraGuardedLinkMaxResponseBytes, jiraGuardedLinkDeadline)
	if err != nil {
		return nil, fmt.Errorf("%w: guarded link request budget is invalid", domain.ErrCheckFailed)
	}
	defer execution.Close()

	initial, err := s.buildGuardedLinkSnapshot(execution.ctx, port, opts, opts.From, opts.To, "", "")
	if err != nil {
		return nil, guardedLinkSafeFailure("guarded Jira link snapshot failed", err, false)
	}
	initial.result.Mode = "preview"
	if opts.Apply {
		initial.result.Mode = "apply"
	}
	prepared := &jiraGuardedLinkPrepared{
		result: initial.result, firstID: initial.first.ID, secondID: initial.second.ID,
		sourceUpdated: initial.first.Updated, sourceUpdatedPresent: initial.first.UpdatedPresent,
	}
	return s.guardedLinkPreparedCore(execution, port, opts, prepared)
}

func (s *JiraService) guardedLinkPreparedCore(execution *jiraGuardedExecution, port domain.JiraGuardedLinkPort, opts JiraGuardedLinkOpts, prepared *jiraGuardedLinkPrepared) (*JiraGuardedLinkResult, error) {
	result := prepared.result
	if opts.Apply && opts.ExpectedProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, guardedLinkSafeFailure("guarded Jira link proposal changed since review", domain.ErrCheckFailed, false)
	}
	if result.Operation == "add" {
		switch len(result.Candidates) {
		case 0:
			result.Status = "would_apply"
		case 1:
			result.Status = "already_satisfied"
			return result, nil
		default:
			result.Status = "blocked"
			return result, guardedLinkSafeFailure("guarded Jira link candidates are ambiguous", domain.ErrCheckFailed, false)
		}
	} else {
		if len(result.Candidates) != 1 || result.Candidates[0].ID != opts.LinkID {
			result.Status = "blocked"
			return result, guardedLinkSafeFailure("guarded Jira link deletion candidate is absent or ambiguous", domain.ErrCheckFailed, false)
		}
		result.Status = "would_apply"
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.buildGuardedLinkSnapshot(execution.ctx, port, opts, prepared.firstID, prepared.secondID, prepared.firstID, prepared.secondID)
	if err != nil || prewrite.result.ProposalHash != result.ProposalHash ||
		prewrite.first.UpdatedPresent != prepared.sourceUpdatedPresent || prewrite.first.Updated != prepared.sourceUpdated {
		result.Status = "blocked"
		return result, guardedLinkSafeFailure("guarded Jira link proposal changed immediately before dispatch", errors.Join(err, domain.ErrCheckFailed), false)
	}
	if err := execution.ctx.Err(); err != nil {
		result.Status = "blocked"
		return result, guardedLinkSafeFailure("guarded Jira link deadline expired before dispatch", err, false)
	}
	write := domain.JiraGuardedLinkWrite{TypeID: prewrite.result.Type.ID, Outward: prewrite.outward, Inward: prewrite.inward, LinkID: opts.LinkID}
	result.WriteAttempted = true
	var writeErr error
	if opts.Operation == "add" {
		writeErr = port.AddGuardedLink(execution.ctx, write)
	} else {
		writeErr = port.DeleteGuardedLink(execution.ctx, write)
	}
	if writeDefinitelyNotAttempted(writeErr) {
		result.WriteAttempted = false
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, guardedLinkSafeFailure("Jira rejected the reviewed guarded link mutation", sanitizeRemoteWriteCause(writeErr), false)
	}

	closeout, closeCancel := execution.Closeout()
	defer closeCancel()
	readback, readbackErr := s.readGuardedLinkEndpoints(closeout, port, prewrite)
	if readbackErr != nil {
		result.Status, result.Complete = "outcome_unknown", false
		return result, guardedLinkSafeFailure("guarded Jira link outcome is unknown; do not replay automatically", errors.Join(sanitizeRemoteWriteCause(writeErr), readbackErr), true)
	}
	result.Reconciled = true
	candidates, candidateErr := guardedLinkCandidates(readback.outward, readback.inward, result.Type)
	if candidateErr == nil {
		if opts.Operation == "add" && len(candidates) == 1 {
			result.Status = "applied"
			if writeErr != nil {
				result.Status = "recovered"
			}
			return result, nil
		}
		if opts.Operation == "delete" && len(candidates) == 0 && !guardedLinkIDPresent(readback.outward, opts.LinkID) && !guardedLinkIDPresent(readback.inward, opts.LinkID) {
			result.Status = "applied"
			if writeErr != nil {
				result.Status = "recovered"
			}
			return result, nil
		}
	}
	result.Status = "outcome_unknown"
	return result, guardedLinkSafeFailure("guarded Jira link readback did not prove the exact end state; do not replay automatically", errors.Join(sanitizeRemoteWriteCause(writeErr), candidateErr), true)
}

// ValidateJiraGuardedLinkOpts is the shared pure CLI/app semantic preflight.
func ValidateJiraGuardedLinkOpts(opts JiraGuardedLinkOpts) error {
	if opts.Operation != "add" && opts.Operation != "delete" {
		return fmt.Errorf("%w: guarded link operation must be add or delete", domain.ErrUsage)
	}
	if len(opts.From) > jiraGuardedLinkReferenceMaxBytes || !domain.ValidJiraIssueKey(opts.From) || len(opts.To) > jiraGuardedLinkReferenceMaxBytes || !domain.ValidJiraIssueKey(opts.To) {
		return fmt.Errorf("%w: --from and --to must be canonical Jira issue keys", domain.ErrUsage)
	}
	if opts.From == opts.To {
		return fmt.Errorf("%w: Jira issue links require two distinct endpoints", domain.ErrUsage)
	}
	if opts.Type == "" || len(opts.Type) > jiraGuardedLinkSelectorMaxBytes || !utf8.ValidString(opts.Type) {
		return fmt.Errorf("%w: --type must be non-empty valid UTF-8 within 1024 bytes", domain.ErrUsage)
	}
	if opts.Operation == "delete" {
		if len(opts.LinkID) > jiraGuardedLinkReferenceMaxBytes || !domain.ValidConfluenceContentID(opts.LinkID) {
			return fmt.Errorf("%w: link id must be a canonical positive decimal", domain.ErrUsage)
		}
	} else if opts.LinkID != "" {
		return fmt.Errorf("%w: link id is valid only for delete", domain.ErrUsage)
	}
	if !opts.Apply {
		if opts.ExpectedProposalHash != "" {
			return fmt.Errorf("%w: --expected-proposal-hash requires --apply", domain.ErrUsage)
		}
		return nil
	}
	return ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash)
}

func (s *JiraService) buildGuardedLinkSnapshot(ctx context.Context, port domain.JiraGuardedLinkPort, opts JiraGuardedLinkOpts, firstRef, secondRef, expectedFirstID, expectedSecondID string) (*jiraGuardedLinkSnapshot, error) {
	catalog, err := port.ReadStrictLinkTypes(ctx)
	if err != nil || !catalog.Complete {
		return nil, errors.Join(err, domain.ErrCheckFailed)
	}
	catalog.Types, err = canonicalGuardedLinkCatalog(catalog.Types)
	if err != nil {
		return nil, err
	}
	selected, role, err := resolveGuardedLinkType(catalog.Types, opts.Type)
	if err != nil {
		return nil, err
	}
	first, err := port.ReadStrictLinkEndpoint(ctx, firstRef)
	if err != nil {
		return nil, err
	}
	second, err := port.ReadStrictLinkEndpoint(ctx, secondRef)
	if err != nil {
		return nil, err
	}
	if err := validateGuardedEndpointPair(first, second, opts.From, opts.To, expectedFirstID, expectedSecondID); err != nil {
		return nil, err
	}
	outward, inward := first, second
	requestedOutward, requestedInward := opts.From, opts.To
	if role == "inward" {
		outward, inward, requestedOutward, requestedInward = second, first, opts.To, opts.From
	}
	if role == "neutral" && guardedDecimalLess(second.ID, first.ID) {
		outward, inward = second, first
	}
	if role == "neutral" && outward.Key == second.Key {
		requestedOutward, requestedInward = opts.To, opts.From
	}
	if err := validateGuardedInventoryCatalog(outward, catalog.Types); err != nil {
		return nil, err
	}
	if err := validateGuardedInventoryCatalog(inward, catalog.Types); err != nil {
		return nil, err
	}
	candidates, err := guardedLinkCandidates(outward, inward, selected)
	if err != nil {
		return nil, err
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	catalogHash := guardedLinkCatalogHash(catalog.Types)
	result := &JiraGuardedLinkResult{
		SchemaVersion: jiraGuardedLinkSchemaVersion, Operation: opts.Operation, BackendSHA256: backendHash,
		RequestedFrom: requestedOutward, RequestedTo: requestedInward, RequestedType: opts.Type, RequestedLinkID: opts.LinkID,
		Outward: guardedLinkResultEndpoint(outward, "outward"), Inward: guardedLinkResultEndpoint(inward, "inward"),
		Type: selected, ResolvedRole: role, CatalogCount: len(catalog.Types), CatalogSHA256: catalogHash,
		Candidates: candidates, Complete: true,
	}
	result.ProposalHash = guardedLinkProposalHash(result)
	return &jiraGuardedLinkSnapshot{result: result, first: first, second: second, outward: outward, inward: inward}, nil
}

func (s *JiraService) readGuardedLinkEndpoints(ctx context.Context, port domain.JiraGuardedLinkPort, expected *jiraGuardedLinkSnapshot) (*jiraGuardedLinkSnapshot, error) {
	outward, err := port.ReadStrictLinkEndpoint(ctx, expected.outward.ID)
	if err != nil {
		return nil, err
	}
	inward, err := port.ReadStrictLinkEndpoint(ctx, expected.inward.ID)
	if err != nil {
		return nil, err
	}
	if !outward.Complete || !inward.Complete || !validGuardedLinkUpdated(outward) || !validGuardedLinkUpdated(inward) || outward.ID != expected.outward.ID || outward.Key != expected.outward.Key || outward.Project != expected.outward.Project || inward.ID != expected.inward.ID || inward.Key != expected.inward.Key || inward.Project != expected.inward.Project {
		return nil, domain.ErrCheckFailed
	}
	return &jiraGuardedLinkSnapshot{outward: outward, inward: inward}, nil
}

func resolveGuardedLinkType(types []domain.JiraLinkTypeMetadata, selector string) (domain.JiraLinkTypeMetadata, string, error) {
	selector = strings.TrimSpace(selector)
	type match struct {
		typ   domain.JiraLinkTypeMetadata
		roles map[string]bool
	}
	var matches []match
	for _, typ := range types {
		roles := map[string]bool{}
		neutral := strings.EqualFold(strings.TrimSpace(typ.Inward), strings.TrimSpace(typ.Outward))
		if strings.EqualFold(selector, strings.TrimSpace(typ.Name)) || strings.EqualFold(selector, strings.TrimSpace(typ.Outward)) {
			roles["outward"] = true
		}
		if strings.EqualFold(selector, strings.TrimSpace(typ.Inward)) {
			roles["inward"] = true
		}
		if len(roles) == 0 {
			continue
		}
		if neutral {
			roles = map[string]bool{"neutral": true}
		}
		matches = append(matches, match{typ: typ, roles: roles})
	}
	if len(matches) != 1 || len(matches[0].roles) != 1 {
		return domain.JiraLinkTypeMetadata{}, "", fmt.Errorf("%w: link type selector is missing, colliding, or directionally contradictory", domain.ErrCheckFailed)
	}
	for role := range matches[0].roles {
		return matches[0].typ, role, nil
	}
	panic("unreachable")
}

func validateGuardedEndpointPair(first, second domain.JiraStrictLinkEndpoint, expectedFirstKey, expectedSecondKey, expectedFirstID, expectedSecondID string) error {
	if !first.Complete || !second.Complete || first.ID == second.ID || first.Key == second.Key || first.Key != expectedFirstKey || second.Key != expectedSecondKey || !domain.ValidConfluenceContentID(first.ID) || !domain.ValidConfluenceContentID(second.ID) || !domain.ValidJiraIssueKey(first.Key) || !domain.ValidJiraIssueKey(second.Key) || !domain.ValidJiraIssueKey(first.Project+"-1") || !domain.ValidJiraIssueKey(second.Project+"-1") || !strings.HasPrefix(first.Key, first.Project+"-") || !strings.HasPrefix(second.Key, second.Project+"-") || len(first.Links) > 4096 || len(second.Links) > 4096 {
		return domain.ErrCheckFailed
	}
	if !validGuardedLinkUpdated(first) || !validGuardedLinkUpdated(second) {
		return domain.ErrCheckFailed
	}
	if expectedFirstID != "" && (first.ID != expectedFirstID || second.ID != expectedSecondID) {
		return domain.ErrCheckFailed
	}
	return nil
}

func validGuardedLinkUpdated(endpoint domain.JiraStrictLinkEndpoint) bool {
	if !endpoint.UpdatedPresent || strings.TrimSpace(endpoint.Updated) != endpoint.Updated {
		return false
	}
	_, err := parseJiraStrictInstant(endpoint.Updated)
	return err == nil
}

func canonicalGuardedLinkCatalog(types []domain.JiraLinkTypeMetadata) ([]domain.JiraLinkTypeMetadata, error) {
	if len(types) > 1024 {
		return nil, domain.ErrCheckFailed
	}
	canonical := append([]domain.JiraLinkTypeMetadata(nil), types...)
	seen := make(map[string]bool, len(canonical))
	for _, typ := range canonical {
		if !domain.ValidConfluenceContentID(typ.ID) || seen[typ.ID] || typ.Name == "" || typ.Inward == "" || typ.Outward == "" || len(typ.Name) > jiraGuardedLinkSelectorMaxBytes || len(typ.Inward) > jiraGuardedLinkSelectorMaxBytes || len(typ.Outward) > jiraGuardedLinkSelectorMaxBytes || !utf8.ValidString(typ.Name) || !utf8.ValidString(typ.Inward) || !utf8.ValidString(typ.Outward) {
			return nil, domain.ErrCheckFailed
		}
		seen[typ.ID] = true
	}
	sort.Slice(canonical, func(i, j int) bool { return guardedDecimalLess(canonical[i].ID, canonical[j].ID) })
	return canonical, nil
}

func validateGuardedInventoryCatalog(endpoint domain.JiraStrictLinkEndpoint, catalog []domain.JiraLinkTypeMetadata) error {
	byID := make(map[string]domain.JiraLinkTypeMetadata, len(catalog))
	for _, typ := range catalog {
		byID[typ.ID] = typ
	}
	seen := make(map[string]bool, len(endpoint.Links))
	for _, link := range endpoint.Links {
		known, ok := byID[link.Type.ID]
		if !ok || known != link.Type || seen[link.ID] || !domain.ValidConfluenceContentID(link.ID) ||
			(link.Role != "inward" && link.Role != "outward") || !domain.ValidConfluenceContentID(link.OtherID) ||
			!domain.ValidJiraIssueKey(link.OtherKey) || len(link.OtherKey) > jiraGuardedLinkReferenceMaxBytes {
			return domain.ErrCheckFailed
		}
		seen[link.ID] = true
	}
	return nil
}

func guardedLinkCandidates(outward, inward domain.JiraStrictLinkEndpoint, typ domain.JiraLinkTypeMetadata) ([]JiraGuardedLinkCandidate, error) {
	out := map[string]domain.JiraStrictIssueLink{}
	in := map[string]domain.JiraStrictIssueLink{}
	neutral := strings.EqualFold(strings.TrimSpace(typ.Inward), strings.TrimSpace(typ.Outward))
	for _, link := range outward.Links {
		if link.OtherID == inward.ID && link.OtherKey == inward.Key && link.Type.ID == typ.ID {
			if link.Type != typ {
				return nil, domain.ErrCheckFailed
			}
			if !neutral && link.Role != "outward" {
				return nil, domain.ErrCheckFailed
			}
			out[link.ID] = link
		}
	}
	for _, link := range inward.Links {
		if link.OtherID == outward.ID && link.OtherKey == outward.Key && link.Type.ID == typ.ID {
			if link.Type != typ {
				return nil, domain.ErrCheckFailed
			}
			if !neutral && link.Role != "inward" {
				return nil, domain.ErrCheckFailed
			}
			in[link.ID] = link
		}
	}
	for id := range out {
		other, ok := in[id]
		if !ok || out[id].Role == other.Role {
			return nil, domain.ErrCheckFailed
		}
	}
	for id := range in {
		if _, ok := out[id]; !ok {
			return nil, domain.ErrCheckFailed
		}
	}
	ids := make([]string, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return guardedDecimalLess(ids[i], ids[j]) })
	result := make([]JiraGuardedLinkCandidate, len(ids))
	for i, id := range ids {
		result[i] = JiraGuardedLinkCandidate{ID: id, OutwardEvidence: true, InwardEvidence: true}
	}
	return result, nil
}

func guardedLinkIDPresent(endpoint domain.JiraStrictLinkEndpoint, id string) bool {
	for _, link := range endpoint.Links {
		if link.ID == id {
			return true
		}
	}
	return false
}
func guardedLinkResultEndpoint(value domain.JiraStrictLinkEndpoint, role string) JiraGuardedLinkEndpoint {
	return JiraGuardedLinkEndpoint{ID: value.ID, Key: value.Key, Project: value.Project, Role: role}
}
func guardedDecimalLess(left, right string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}

func guardedLinkCatalogHash(types []domain.JiraLinkTypeMetadata) string {
	data, _ := json.Marshal(types)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func guardedLinkProposalHash(result *JiraGuardedLinkResult) string {
	payload := struct {
		SchemaVersion   int                         `json:"schema_version"`
		Operation       string                      `json:"operation"`
		BackendSHA256   string                      `json:"backend_sha256"`
		RequestedFrom   string                      `json:"requested_from"`
		RequestedTo     string                      `json:"requested_to"`
		RequestedType   string                      `json:"requested_type"`
		RequestedLinkID string                      `json:"requested_link_id,omitempty"`
		Outward         JiraGuardedLinkEndpoint     `json:"outward"`
		Inward          JiraGuardedLinkEndpoint     `json:"inward"`
		Type            domain.JiraLinkTypeMetadata `json:"type"`
		ResolvedRole    string                      `json:"resolved_role"`
		CatalogCount    int                         `json:"catalog_count"`
		CatalogSHA256   string                      `json:"catalog_sha256"`
		Candidates      []JiraGuardedLinkCandidate  `json:"candidates"`
	}{result.SchemaVersion, result.Operation, result.BackendSHA256, result.RequestedFrom, result.RequestedTo, result.RequestedType, result.RequestedLinkID, result.Outward, result.Inward, result.Type, result.ResolvedRole, result.CatalogCount, result.CatalogSHA256, result.Candidates}
	canonical, _ := json.Marshal(payload)
	return guardedProposalDigest(canonical)
}

func guardedLinkSafeFailure(message string, cause error, ambiguous bool) error {
	return &jiraGuardedLinkError{message: message, cause: preserveGuardedBudgetCause(cause, sanitizeRemoteWriteCause(cause)), ambiguous: ambiguous}
}
