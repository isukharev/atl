package app

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const maxJiraAttachmentBodyMaterializationMediaTypes = 64

// JiraAttachmentBodyMaterializeOpts selects a bounded local continuation of
// qualified Jira attachment inventories already present in a complete mirror.
// It never changes Jira; each accepted body is one independent local durable
// transaction so a large project does not need one unbounded aggregate pull.
type JiraAttachmentBodyMaterializeOpts struct {
	Into                 string
	AttachmentMediaTypes []string
	MaxAttachmentBytes   int64
	MaxTransactions      int
}

// JiraAttachmentBodyMaterializeResult is deliberately content-free. It says
// how much local inventory work was considered and committed, never exposing
// issue keys, attachment names, paths, or bodies on stdout.
type JiraAttachmentBodyMaterializeResult struct {
	SchemaVersion int    `json:"schema_version"`
	Into          string `json:"into"`
	Inventories   int    `json:"inventories"`
	Pending       int    `json:"pending"`
	Captured      int    `json:"captured"`
	Remaining     int    `json:"remaining"`
	Complete      bool   `json:"complete"`
}

// ValidateJiraAttachmentBodyMaterializeOpts rejects an ambiguous, partial, or
// unbounded continuation before it touches a mirror or backend. In particular
// the MIME list must cover every qualified attachment row rather than silently
// leaving a new exclusion behind.
func ValidateJiraAttachmentBodyMaterializeOpts(opts JiraAttachmentBodyMaterializeOpts) error {
	if strings.TrimSpace(opts.Into) == "" || opts.MaxAttachmentBytes <= 0 ||
		opts.MaxAttachmentBytes > mirror.MaxJiraAttachmentBodyMaterializationBytes || opts.MaxTransactions <= 0 ||
		opts.MaxTransactions > mirror.MaxJiraAttachmentBodyMaterializationTransactions ||
		len(opts.AttachmentMediaTypes) == 0 || len(opts.AttachmentMediaTypes) > maxJiraAttachmentBodyMaterializationMediaTypes {
		return fmt.Errorf("%w: Jira attachment body materialization requires a mirror root, exact MIME allowlist, per-body cap, and bounded transaction count", domain.ErrUsage)
	}
	seen := make(map[string]struct{}, len(opts.AttachmentMediaTypes))
	for _, value := range opts.AttachmentMediaTypes {
		mediaType, parameters, err := mime.ParseMediaType(value)
		if err != nil || len(parameters) != 0 || value != strings.ToLower(strings.TrimSpace(value)) || mediaType != value || strings.Contains(value, "*") {
			return fmt.Errorf("%w: Jira attachment MIME allowlist contains a non-canonical exact media type", domain.ErrUsage)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%w: Jira attachment MIME allowlist contains a duplicate", domain.ErrUsage)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// MaterializeAttachmentBodies captures an existing complete Jira mirror's
// remaining attachment bodies. The primary native snapshot, base, metadata,
// backend binding, sidecar and every earlier captured body are proven before a
// new remote request; an interruption resumes the one local staged body before
// making any next request.
func (s *JiraService) MaterializeAttachmentBodies(ctx context.Context, opts JiraAttachmentBodyMaterializeOpts) (*JiraAttachmentBodyMaterializeResult, error) {
	if s == nil || ctx == nil {
		return nil, fmt.Errorf("%w: Jira attachment body materializer is unavailable", domain.ErrCheckFailed)
	}
	if err := ValidateJiraAttachmentBodyMaterializeOpts(opts); err != nil {
		return nil, err
	}
	root := filepath.Clean(strings.TrimSpace(opts.Into))
	result := &JiraAttachmentBodyMaterializeResult{SchemaVersion: 1, Into: root}
	if err := requireInitializedMirror(root); err != nil {
		return result, err
	}
	if err := requireMirrorBackend(root, "jira", s.baseURL); err != nil {
		return result, err
	}
	lock, err := lockJiraPendingFields(root, "attachment-bodies")
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Unlock() }()
	m := mirror.New(root)
	if err := m.RefuseActiveCompletePullState(); err != nil {
		return result, err
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		return result, err
	}
	inventories, err := m.JiraAttachmentBodyInventories()
	if err != nil {
		return result, err
	}
	result.Inventories = len(inventories)
	queue, err := jiraAttachmentBodyMaterializationQueue(inventories, opts)
	if err != nil {
		return result, err
	}
	result.Pending = len(queue)
	for index, target := range queue {
		if index >= opts.MaxTransactions {
			break
		}
		inventory, attachment, candidateErr := m.JiraAttachmentBodyCandidate(target.identity, target.attachmentID)
		if candidateErr != nil {
			return result, candidateErr
		}
		if policyErr := validateJiraAttachmentBodyMaterializationInventory(inventory, opts); policyErr != nil {
			return result, policyErr
		}
		if err := s.revalidateJiraCorpusEvidenceParent(ctx, inventory.Identity, inventory.ParentRevision); err != nil {
			return result, err
		}
		reader, openErr := s.openRevalidatedJiraAttachmentWithLimit(ctx, inventory.Identity, jiraAttachmentFromSidecar(attachment), opts.MaxAttachmentBytes)
		if openErr != nil {
			return result, openErr
		}
		body, _, streamErr := streamCorpusAttachment(ctx, root, inventory.Identity, attachment.ID, attachment.DeclaredSize, reader)
		closeErr := reader.Close()
		if streamErr != nil || closeErr != nil {
			return result, errors.Join(fmt.Errorf("%w: capture bounded Jira attachment body", domain.ErrCheckFailed), streamErr, closeErr)
		}
		if err := s.revalidateJiraCorpusEvidenceParent(ctx, inventory.Identity, inventory.ParentRevision); err != nil {
			return result, err
		}
		if err := m.PublishJiraAttachmentBody(inventory.Identity, attachment.ID, body); err != nil {
			return result, err
		}
		result.Captured++
	}
	verified, err := m.JiraAttachmentBodyInventories()
	if err != nil {
		return result, err
	}
	remainingQueue, err := jiraAttachmentBodyMaterializationQueue(verified, opts)
	if err != nil {
		return result, err
	}
	result.Inventories = len(verified)
	result.Remaining = len(remainingQueue)
	result.Complete = result.Remaining == 0
	return result, nil
}

type jiraAttachmentBodyMaterializationTarget struct {
	identity     string
	attachmentID string
}

func jiraAttachmentBodyMaterializationQueue(inventories []mirror.JiraAttachmentBodyInventory, opts JiraAttachmentBodyMaterializeOpts) ([]jiraAttachmentBodyMaterializationTarget, error) {
	queue := make([]jiraAttachmentBodyMaterializationTarget, 0)
	for _, inventory := range inventories {
		if err := validateJiraAttachmentBodyMaterializationInventory(inventory, opts); err != nil {
			return nil, err
		}
		for _, attachment := range inventory.Attachments {
			if jiraAttachmentBodyMaterializationPending(inventory.BodiesState, attachment.Body) {
				queue = append(queue, jiraAttachmentBodyMaterializationTarget{identity: inventory.Identity, attachmentID: attachment.ID})
			}
		}
	}
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].identity != queue[j].identity {
			return jiraNumericIdentityLess(queue[i].identity, queue[j].identity)
		}
		return jiraNumericIdentityLess(queue[i].attachmentID, queue[j].attachmentID)
	})
	return queue, nil
}

func validateJiraAttachmentBodyMaterializationInventory(inventory mirror.JiraAttachmentBodyInventory, opts JiraAttachmentBodyMaterializeOpts) error {
	if !canonicalPositiveNumericString(inventory.Identity) || !domain.ValidJiraEvidenceParentRevision(inventory.ParentRevision) {
		return fmt.Errorf("%w: Jira attachment materialization inventory is invalid", domain.ErrCheckFailed)
	}
	for _, attachment := range inventory.Attachments {
		if !canonicalPositiveNumericString(attachment.ID) || attachment.DeclaredSize < 0 || attachment.DeclaredSize > opts.MaxAttachmentBytes ||
			!corpusAttachmentMediaAllowed(opts.AttachmentMediaTypes, attachment.MediaType) {
			return fmt.Errorf("%w: qualified Jira attachment inventory is not covered by the explicit body policy", domain.ErrCheckFailed)
		}
	}
	return nil
}

func jiraAttachmentBodyMaterializationPending(state mirror.AttachmentBodiesState, body mirror.AttachmentSidecarBody) bool {
	return state == mirror.AttachmentBodiesNotRequested && body.State == mirror.AttachmentBodyNotRequested ||
		state == mirror.AttachmentBodiesPartial && body.State == mirror.AttachmentBodyExcluded && body.Reason == mirror.AttachmentBodyReasonAggregateLimit
}

func jiraAttachmentFromSidecar(record mirror.AttachmentSidecarRecord) domain.Attachment {
	return domain.Attachment{
		ID: record.ID, Title: record.Filename, MediaType: record.MediaType, FileSize: record.DeclaredSize,
		Created: record.CreatedAt, Author: record.Author.DisplayName, AuthorName: record.Author.Name, AuthorKey: record.Author.ID,
	}
}
