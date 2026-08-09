package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	ConfluenceLabelNameCap  = 255
	ConfluenceLabelBatchCap = 100
)

type ConfluenceLabelListResult struct {
	ID        string                `json:"id"`
	Labels    []domain.ContentLabel `json:"labels"`
	Count     int                   `json:"count"`
	Complete  bool                  `json:"complete"`
	Truncated bool                  `json:"truncated,omitempty"`
}

type ConfluenceLabelMutationOpts struct {
	Operation            string
	Labels               []string
	ExpectedProposalHash string
	Apply                bool
}

type ConfluenceLabelMutationResult struct {
	ID           string                `json:"id"`
	Operation    string                `json:"operation"`
	Mode         string                `json:"mode"`
	Status       string                `json:"status"`
	Requested    []string              `json:"requested"`
	Current      []domain.ContentLabel `json:"current"`
	Final        []domain.ContentLabel `json:"final,omitempty"`
	ProposalHash string                `json:"proposal_hash"`
	Complete     bool                  `json:"complete"`
	Reconciled   bool                  `json:"reconciled,omitempty"`
}

// ConfluenceLabelReferenceResolver preserves the shared exact page-reference
// qualification without making the label feature depend on the broad document
// service or store.
type ConfluenceLabelReferenceResolver func(context.Context, string) (*ConfluencePageResolution, error)

type ConfluenceLabelDependencies struct {
	Reader           domain.ContentLabelReader
	Writer           domain.ContentLabelWriter
	ResolveReference ConfluenceLabelReferenceResolver
}

// ConfluenceLabelService owns only content-label reads and guarded writes.
type ConfluenceLabelService struct {
	reader           domain.ContentLabelReader
	writer           domain.ContentLabelWriter
	resolveReference ConfluenceLabelReferenceResolver
}

func NewConfluenceLabelService(deps ConfluenceLabelDependencies) *ConfluenceLabelService {
	return &ConfluenceLabelService{reader: deps.Reader, writer: deps.Writer, resolveReference: deps.ResolveReference}
}

type confluenceLabelWriteError struct {
	message   string
	cause     error
	ambiguous bool
}

func (e *confluenceLabelWriteError) Error() string                  { return e.message }
func (e *confluenceLabelWriteError) Unwrap() error                  { return e.cause }
func (e *confluenceLabelWriteError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }

func (s *ConfluenceLabelService) contentLabelReader() (domain.ContentLabelReader, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("%w: configured document backend does not support content labels", domain.ErrCheckFailed)
	}
	return s.reader, nil
}

func (s *ConfluenceLabelService) contentLabelMutationPorts() (domain.ContentLabelReader, domain.ContentLabelWriter, error) {
	reader, err := s.contentLabelReader()
	if err != nil || s.writer == nil {
		return nil, nil, fmt.Errorf("%w: configured document backend does not support content labels", domain.ErrCheckFailed)
	}
	return reader, s.writer, nil
}

func (s *ConfluenceLabelService) ListLabels(ctx context.Context, id string) (*ConfluenceLabelListResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: page id is required", domain.ErrUsage)
	}
	reader, err := s.contentLabelReader()
	if err != nil {
		return nil, err
	}
	if s.resolveReference == nil {
		return nil, fmt.Errorf("%w: configured document backend cannot resolve page references", domain.ErrCheckFailed)
	}
	resolved, err := s.resolveReference(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx = resolved.Context(ctx)
	id = resolved.ID
	labels, truncated, err := reader.ListContentLabels(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ConfluenceLabelListResult{ID: id, Labels: labels, Count: len(labels), Complete: !truncated, Truncated: truncated}, nil
}

func (s *ConfluenceLabelService) MutateLabelsGuarded(ctx context.Context, id string, opts ConfluenceLabelMutationOpts) (*ConfluenceLabelMutationResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: page id is required", domain.ErrUsage)
	}
	operation := strings.TrimSpace(opts.Operation)
	if operation != "add" && operation != "remove" {
		return nil, fmt.Errorf("%w: label operation must be add or remove", domain.ErrUsage)
	}
	requested, err := normalizeConfluenceLabelNames(opts.Labels)
	if err != nil {
		return nil, err
	}
	policy, err := newGuardedWritePolicy(opts.Apply, opts.ExpectedProposalHash)
	if err != nil {
		return nil, err
	}
	reader, writer, err := s.contentLabelMutationPorts()
	if err != nil {
		return nil, err
	}
	currentRecords, truncated, err := reader.ListContentLabels(ctx, id)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%w: page label listing was truncated; refusing a mutation against incomplete state", domain.ErrCheckFailed)
	}
	if operation == "remove" {
		requestedSet := make(map[string]bool, len(requested))
		for _, name := range requested {
			requestedSet[name] = true
		}
		for _, label := range currentRecords {
			if requestedSet[label.Name] && label.Prefix != "global" {
				return nil, fmt.Errorf("%w: label %q also exists with non-global prefix %q; Confluence's delete-by-name endpoint cannot target it safely", domain.ErrCheckFailed, label.Name, label.Prefix)
			}
		}
	}
	current := sortedContentLabels(currentRecords)
	proposalHash := confluenceLabelProposalHash(id, operation, requested, currentRecords)
	decision := policy.decide(proposalHash, confluenceLabelGoalSatisfied(operation, requested, currentRecords))
	result := &ConfluenceLabelMutationResult{
		ID: id, Operation: operation, Mode: decision.mode, Status: decision.status,
		Requested: requested, Current: current, ProposalHash: decision.proposalHash, Complete: true,
	}
	if decision.hashMismatch {
		return result, fmt.Errorf("%w: label proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, policy.expectedHash, proposalHash)
	}
	if decision.status == "already_satisfied" {
		result.Final = current
		return result, nil
	}
	if !decision.writeRequired {
		return result, nil
	}

	var writeErr error
	successfulWrites := 0
	if operation == "add" {
		labels := make([]domain.ContentLabel, len(requested))
		for index, name := range requested {
			labels[index] = domain.ContentLabel{Prefix: "global", Name: name}
		}
		writeErr = writer.AddContentLabels(domain.WithSingleAttempt(ctx), id, labels)
		if writeErr == nil {
			successfulWrites = 1
		}
	} else {
		for _, name := range requested {
			if err := writer.RemoveContentLabel(domain.WithSingleAttempt(ctx), id, name); err != nil {
				writeErr = err
				break
			}
			successfulWrites++
		}
	}

	verified, verifyTruncated, verifyErr := reader.ListContentLabels(ctx, id)
	if verifyErr != nil || verifyTruncated {
		result.Status = "unknown"
		cause := writeErr
		if cause == nil {
			cause = verifyErr
		}
		if cause == nil {
			cause = fmt.Errorf("verification listing was truncated")
		}
		return result, &confluenceLabelWriteError{message: "label update outcome is unknown; verification read was unavailable or incomplete; do not replay automatically", cause: cause, ambiguous: true}
	}
	result.Final = sortedContentLabels(verified)
	result.Reconciled = writeErr != nil
	if confluenceLabelGoalSatisfied(operation, requested, verified) {
		result.Status = "applied"
		return result, nil
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) && successfulWrites == 0 {
		result.Status = "failed"
		return result, &confluenceLabelWriteError{message: definitiveWriteMessage("Confluence rejected the label update", writeErr), cause: writeErr}
	}
	result.Status = "unknown"
	cause := writeErr
	if cause == nil {
		cause = fmt.Errorf("verified label state differs from the reviewed proposal")
	}
	return result, &confluenceLabelWriteError{message: "label update outcome is unknown; verified state differs from the reviewed proposal; do not replay automatically", cause: cause, ambiguous: true}
}

func normalizeConfluenceLabelNames(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: at least one label is required", domain.ErrUsage)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(input))
	for _, raw := range input {
		if !utf8.ValidString(raw) {
			return nil, fmt.Errorf("%w: label is not valid UTF-8", domain.ErrUsage)
		}
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("%w: labels must not be empty", domain.ErrUsage)
		}
		if len([]byte(name)) > ConfluenceLabelNameCap {
			return nil, fmt.Errorf("%w: label %q exceeds %d bytes", domain.ErrUsage, name, ConfluenceLabelNameCap)
		}
		for _, char := range name {
			if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
				return nil, fmt.Errorf("%w: labels must not contain control or invisible format characters", domain.ErrUsage)
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) > ConfluenceLabelBatchCap {
		return nil, fmt.Errorf("%w: label batch exceeds %d unique names", domain.ErrUsage, ConfluenceLabelBatchCap)
	}
	sort.Strings(out)
	return out, nil
}

func sortedContentLabels(labels []domain.ContentLabel) []domain.ContentLabel {
	out := append([]domain.ContentLabel(nil), labels...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix < out[j].Prefix
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func confluenceLabelGoalSatisfied(operation string, requested []string, labels []domain.ContentLabel) bool {
	present := map[string]bool{}
	for _, label := range labels {
		if label.Prefix == "global" {
			present[label.Name] = true
		}
	}
	for _, name := range requested {
		if operation == "add" && !present[name] {
			return false
		}
		if operation == "remove" && present[name] {
			return false
		}
	}
	return true
}

func confluenceLabelProposalHash(id, operation string, requested []string, current []domain.ContentLabel) string {
	state := make([]string, 0, len(current))
	for _, label := range current {
		state = append(state, label.Prefix+"\x00"+label.Name)
	}
	sort.Strings(state)
	canonical, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		ID            string   `json:"id"`
		Operation     string   `json:"operation"`
		Requested     []string `json:"requested"`
		Current       []string `json:"current"`
	}{SchemaVersion: 1, ID: id, Operation: operation, Requested: requested, Current: state})
	return guardedProposalDigest(canonical)
}
