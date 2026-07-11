package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const JiraFieldSetValueCap = 64 << 20

// JiraFieldProposal is one explicitly typed custom-field value prepared from a
// bounded file or Markdown input. Value must be a string, object, or array.
type JiraFieldProposal struct {
	Field  string
	Value  any
	Source string // raw|markdown
}

// JiraFieldSetOpts controls guarded custom-field preview/apply.
type JiraFieldSetOpts struct {
	Proposals            []JiraFieldProposal
	AllowFields          []string
	ExpectedUpdated      string
	ExpectedProposalHash string
	Apply                bool
}

type JiraFieldSetResult struct {
	Key             string                `json:"key"`
	Mode            string                `json:"mode"`
	Status          string                `json:"status"`
	ExpectedUpdated string                `json:"expected_updated"`
	ActualUpdated   string                `json:"actual_updated"`
	ProposalHash    string                `json:"proposal_hash"`
	Reconciled      bool                  `json:"reconciled,omitempty"`
	Fields          []JiraFieldSetPreview `json:"fields"`
}

type JiraFieldSetPreview struct {
	Field  string `json:"field"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Value  any    `json:"value"`
}

type jiraFieldWriteError struct {
	message string
	cause   error
}

func (e *jiraFieldWriteError) Error() string { return e.message }
func (e *jiraFieldWriteError) Unwrap() error { return e.cause }

func sanitizedFieldWriteError(message string, cause error) error {
	return &jiraFieldWriteError{message: message, cause: cause}
}

// SetFieldsGuarded previews or applies one atomic custom-field update. Apply
// requires a reviewed Jira updated value and fresh-reads it immediately before
// the write. Jira still has no server-side CAS, so the narrow read/write TOCTOU
// window is inherent and documented.
func (s *JiraService) SetFieldsGuarded(ctx context.Context, key string, opts JiraFieldSetOpts) (*JiraFieldSetResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedUpdated) == "" {
		return nil, fmt.Errorf("%w: --expected-updated is required with --apply; run the dry-run first to capture it", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first to capture it", domain.ErrUsage)
	}
	allowed := exactAllowSet(opts.AllowFields)
	if len(allowed) == 0 {
		return nil, fmt.Errorf("%w: --allow-fields is required", domain.ErrUsage)
	}

	proposals, values, err := normalizeFieldProposals(opts.Proposals, allowed)
	if err != nil {
		return nil, err
	}
	proposalHash, err := jiraFieldProposalHash(proposals)
	if err != nil {
		return nil, err
	}
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		return &JiraFieldSetResult{
			Key: key, Mode: mode, Status: "blocked", ExpectedUpdated: strings.TrimSpace(opts.ExpectedUpdated),
			ProposalHash: proposalHash, Fields: proposals,
		}, fmt.Errorf("%w: field proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	defs, err := s.tr.Fields(ctx)
	if err != nil {
		return nil, err
	}
	custom := make(map[string]bool, len(defs))
	for _, def := range defs {
		custom[def.ID] = def.Custom
	}
	for _, proposal := range proposals {
		if isCustom, exists := custom[proposal.Field]; !exists {
			return nil, fmt.Errorf("%w: Jira field %q does not exist", domain.ErrUsage, proposal.Field)
		} else if !isCustom {
			return nil, fmt.Errorf("%w: field %q is not custom; use its dedicated issue command", domain.ErrUsage, proposal.Field)
		}
	}

	fields := make([]string, 0, len(proposals)+1)
	for _, proposal := range proposals {
		fields = append(fields, proposal.Field)
	}
	fields = append(fields, "updated")
	issue, err := s.tr.GetIssue(ctx, key, fields)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("fresh field read for %s returned no issue", key)
	}
	actualUpdated := fieldString(issue.Fields["updated"])
	if actualUpdated == "" {
		return nil, fmt.Errorf("%w: Jira did not return the updated field for %s", domain.ErrCheckFailed, key)
	}
	expectedUpdated := strings.TrimSpace(opts.ExpectedUpdated)
	if expectedUpdated == "" {
		expectedUpdated = actualUpdated
	}
	result := &JiraFieldSetResult{
		Key: key, Mode: mode, Status: "would_apply",
		ExpectedUpdated: expectedUpdated, ActualUpdated: actualUpdated,
		ProposalHash: proposalHash, Fields: proposals,
	}

	if fieldProposalsSatisfied(issue, proposals, values) {
		result.Status = "already_satisfied"
		return result, nil
	}
	if expectedUpdated != actualUpdated {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: stale issue %s: expected updated %q, got %q", domain.ErrCheckFailed, key, expectedUpdated, actualUpdated)
	}
	if !opts.Apply {
		return result, nil
	}
	if err := s.tr.SetFields(ctx, key, values); err != nil {
		definitive := definitiveWriteRejection(err)
		fresh, reconcileErr := s.tr.GetIssue(ctx, key, fields)
		if reconcileErr != nil || fresh == nil {
			if definitive {
				result.Status = "failed"
				return result, sanitizedFieldWriteError("Jira rejected the custom-field update", err)
			}
			result.Status = "unknown"
			return result, sanitizedFieldWriteError("custom-field update outcome is unknown; reconciliation read failed", err)
		}
		result.Reconciled = true
		result.ActualUpdated = fieldString(fresh.Fields["updated"])
		if fieldProposalsSatisfied(fresh, proposals, values) {
			if definitive {
				// The request was rejected, but another actor may have produced the
				// reviewed end state after preflight. Do not claim our PUT applied it.
				result.Status = "already_satisfied"
			} else {
				result.Status = "applied"
			}
			return result, nil
		}
		if definitive {
			result.Status = "failed"
			return result, sanitizedFieldWriteError("Jira rejected the custom-field update", err)
		}
		result.Status = "unknown"
		return result, sanitizedFieldWriteError("custom-field update outcome remains unknown; proposal is not yet visible", err)
	}
	result.Status = "applied"
	return result, nil
}

// jiraFieldProposalHash binds a review to the complete normalized proposal set,
// independent of CLI input order. The per-field preview hashes remain useful to
// humans, while this aggregate hash is the single apply gate.
func jiraFieldProposalHash(previews []JiraFieldSetPreview) (string, error) {
	type hashEntry struct {
		Field  string `json:"field"`
		Source string `json:"source"`
		Kind   string `json:"kind"`
		Value  any    `json:"value"`
	}
	entries := make([]hashEntry, len(previews))
	for i, preview := range previews {
		entries[i] = hashEntry{Field: preview.Field, Source: preview.Source, Kind: preview.Kind, Value: preview.Value}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Field < entries[j].Field })
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("canonicalize Jira field proposal: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func normalizeFieldProposals(input []JiraFieldProposal, allowed map[string]bool) ([]JiraFieldSetPreview, map[string]any, error) {
	return normalizeFieldProposalsWithLimit(input, allowed, JiraFieldSetValueCap)
}

func normalizeFieldProposalsWithLimit(input []JiraFieldProposal, allowed map[string]bool, limit int) ([]JiraFieldSetPreview, map[string]any, error) {
	if len(input) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one --from-file or --from-md field input is required", domain.ErrUsage)
	}
	seen := map[string]bool{}
	values := make(map[string]any, len(input))
	previews := make([]JiraFieldSetPreview, 0, len(input))
	totalBytes := 0
	for _, proposal := range input {
		field := strings.TrimSpace(proposal.Field)
		if field == "" {
			return nil, nil, fmt.Errorf("%w: field id is empty", domain.ErrUsage)
		}
		if !allowed[field] {
			return nil, nil, fmt.Errorf("%w: field %q is not in --allow-fields", domain.ErrUsage, field)
		}
		if seen[field] {
			return nil, nil, fmt.Errorf("%w: duplicate input for field %q", domain.ErrUsage, field)
		}
		if proposal.Source != "raw" && proposal.Source != "markdown" {
			return nil, nil, fmt.Errorf("%w: field %q has invalid source %q", domain.ErrUsage, field, proposal.Source)
		}
		seen[field] = true
		kind, normalized, encoded, err := normalizeFieldProposalValue(proposal.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: field %q: %v", domain.ErrUsage, field, err)
		}
		if proposal.Source == "markdown" && kind != "string" {
			return nil, nil, fmt.Errorf("%w: field %q: Markdown input must normalize to a string, got %s", domain.ErrUsage, field, kind)
		}
		digest := sha256.Sum256(encoded)
		totalBytes += len(encoded)
		if totalBytes > limit {
			return nil, nil, fmt.Errorf("%w: normalized field values exceed the %d MiB aggregate limit", domain.ErrUsage, limit>>20)
		}
		previews = append(previews, JiraFieldSetPreview{
			Field: field, Source: proposal.Source, Kind: kind, Bytes: len(encoded),
			SHA256: hex.EncodeToString(digest[:]), Value: normalized,
		})
		values[field] = normalized
	}
	sort.Slice(previews, func(i, j int) bool { return previews[i].Field < previews[j].Field })
	return previews, values, nil
}

func normalizeFieldProposalValue(value any) (kind string, normalized any, encoded []byte, err error) {
	switch typed := value.(type) {
	case string:
		return "string", typed, []byte(typed), nil
	case map[string]any:
		encoded, err = json.Marshal(typed)
		return "object", typed, encoded, err
	case []any:
		encoded, err = json.Marshal(typed)
		return "array", typed, encoded, err
	default:
		return "", nil, nil, fmt.Errorf("value must be a string, JSON object, or JSON array (got %T)", value)
	}
}

func jiraFieldProposalEqual(current, desired any) bool {
	if desiredString, ok := desired.(string); ok {
		currentString, ok := current.(string)
		return ok && currentString == desiredString
	}
	return planValueContains(current, desired)
}

func fieldProposalsSatisfied(issue *domain.Issue, proposals []JiraFieldSetPreview, values map[string]any) bool {
	if issue == nil {
		return false
	}
	for _, proposal := range proposals {
		if !jiraFieldProposalEqual(issue.Fields[proposal.Field], values[proposal.Field]) {
			return false
		}
	}
	return true
}
