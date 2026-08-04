package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const confluencePageCopySchemaVersion = 1

type ConfluencePageCopyOpts struct {
	Title                string
	Space                string
	Parent               string
	Register             bool
	Root                 string
	Apply                bool
	ExpectedVersion      int
	ExpectedProposalHash string
}

type ConfluencePageCopyResult struct {
	SchemaVersion               int                        `json:"schema_version"`
	SourceID                    string                     `json:"source_id"`
	Mode                        string                     `json:"mode"`
	Status                      string                     `json:"status"`
	CurrentVersion              int                        `json:"current_version"`
	ExpectedVersion             int                        `json:"expected_version"`
	SourceBodySHA256            string                     `json:"source_body_sha256"`
	SourceBodyBytes             int                        `json:"source_body_bytes"`
	SourceTitleSHA256           string                     `json:"source_title_sha256"`
	SourceHierarchySHA256       string                     `json:"source_hierarchy_sha256"`
	TargetTitleSHA256           string                     `json:"target_title_sha256"`
	TargetSpace                 string                     `json:"target_space"`
	TargetParent                string                     `json:"target_parent"`
	TargetParentVersion         int                        `json:"target_parent_version,omitempty"`
	TargetParentBodySHA256      string                     `json:"target_parent_body_sha256,omitempty"`
	TargetParentHierarchySHA256 string                     `json:"target_parent_hierarchy_sha256,omitempty"`
	TargetHierarchySHA256       string                     `json:"target_hierarchy_sha256"`
	BackendSHA256               string                     `json:"backend_sha256"`
	Register                    bool                       `json:"register"`
	RegistrationRootSHA256      string                     `json:"registration_root_sha256,omitempty"`
	ProposalHash                string                     `json:"proposal_hash"`
	WriteAttempted              bool                       `json:"write_attempted"`
	Reconciled                  bool                       `json:"reconciled,omitempty"`
	Complete                    bool                       `json:"complete"`
	ID                          string                     `json:"id,omitempty"`
	Title                       string                     `json:"title,omitempty"`
	Version                     int                        `json:"version,omitempty"`
	URL                         string                     `json:"url,omitempty"`
	Registration                *CreatedMirrorRegistration `json:"registration,omitempty"`
	Warning                     string                     `json:"warning"`
}

type confluencePageCopySnapshot struct {
	source              *domain.Resource
	sourceTitleSHA256   string
	sourceHierarchyHash string
	backendSHA256       string
	title               string
	titleSHA256         string
	space               string
	parent              string
	parentVersion       int
	parentBodySHA256    string
	parentHierarchyHash string
	targetHierarchyHash string
	rootSHA256          string
	register            bool
}

type confluencePageCopyWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *confluencePageCopyWriteError) Error() string { return e.message }
func (e *confluencePageCopyWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *confluencePageCopyWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// CopyPageGuarded previews or applies one source-bound page copy. Apply
// revalidates the source and destination parent immediately before one
// non-replayed POST, then requires an exact current-state readback.
func (s *ConfluenceService) CopyPageGuarded(ctx context.Context, sourceID string, opts ConfluencePageCopyOpts) (*ConfluencePageCopyResult, error) {
	sourceID = strings.TrimSpace(sourceID)
	if !canonicalConfluenceContentID(sourceID) {
		return nil, fmt.Errorf("%w: source page id must be a positive numeric content id", domain.ErrUsage)
	}
	title, err := normalizeConfluenceTitle([]byte(opts.Title))
	if err != nil {
		return nil, err
	}
	opts.Title = title
	opts.Space = strings.TrimSpace(opts.Space)
	opts.Parent = strings.TrimSpace(opts.Parent)
	if opts.Parent != "" && !canonicalConfluenceContentID(opts.Parent) {
		return nil, fmt.Errorf("%w: target parent must be a positive numeric content id", domain.ErrUsage)
	}
	if opts.Register != (strings.TrimSpace(opts.Root) != "") {
		return nil, fmt.Errorf("%w: --register and a non-empty --into must be used together", domain.ErrUsage)
	}
	if !opts.Apply && (opts.ExpectedVersion != 0 || strings.TrimSpace(opts.ExpectedProposalHash) != "") {
		return nil, fmt.Errorf("%w: --expected-version and --expected-proposal-hash require --apply", domain.ErrUsage)
	}
	if opts.Apply && opts.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: --expected-version is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Register {
		if err := validateCreatedRegistrationPlatform(runtime.GOOS); err != nil {
			return nil, err
		}
		root, err := createdRegistrationRoot(opts.Root)
		if err != nil {
			return nil, err
		}
		root, err = evalSymlinksAllowMissing(root)
		if err != nil {
			return nil, fmt.Errorf("%w: canonicalize registration root", domain.ErrCheckFailed)
		}
		opts.Root = root
		if err := previewConfluenceCopyRegistration(root, s.baseURL); err != nil {
			return nil, err
		}
	}

	initial, err := s.confluencePageCopySnapshot(ctx, sourceID, opts)
	if err != nil {
		return nil, err
	}
	proposalHash := confluencePageCopyProposalHash(initial)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expectedVersion := initial.source.Version
	if opts.ExpectedVersion > 0 {
		expectedVersion = opts.ExpectedVersion
	}
	result := &ConfluencePageCopyResult{
		SchemaVersion: confluencePageCopySchemaVersion, SourceID: sourceID, Mode: mode,
		Status: "would_apply", CurrentVersion: initial.source.Version, ExpectedVersion: expectedVersion,
		SourceBodySHA256: mirror.Hash(initial.source.Body), SourceBodyBytes: len(initial.source.Body),
		SourceTitleSHA256: initial.sourceTitleSHA256, SourceHierarchySHA256: initial.sourceHierarchyHash,
		TargetTitleSHA256: initial.titleSHA256, TargetSpace: initial.space, TargetParent: initial.parent,
		TargetParentVersion: initial.parentVersion, TargetParentBodySHA256: initial.parentBodySHA256,
		TargetParentHierarchySHA256: initial.parentHierarchyHash, TargetHierarchySHA256: initial.targetHierarchyHash,
		BackendSHA256: initial.backendSHA256,
		Register:      initial.register, RegistrationRootSHA256: initial.rootSHA256,
		ProposalHash: proposalHash, Complete: true,
		Warning: "page creation has no server-side idempotency key; apply sends one POST, never searches by title, and never replays an uncertain write",
	}
	if opts.Apply && expectedVersion != initial.source.Version {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: reviewed source page version changed; run the dry-run again", domain.ErrCheckFailed)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: page copy proposal changed since review; run the dry-run again", domain.ErrCheckFailed)
	}
	if !opts.Apply {
		return result, nil
	}

	var registration *CreatedMirrorRegistration
	var m *mirror.Mirror
	var rs RenderSettings
	var release func() error
	if opts.Register {
		registration, m, rs, release, err = s.prepareConfluenceRegistration(opts.Root)
		if err != nil {
			result.Status = "blocked"
			return result, err
		}
		defer func() { _ = release() }()
		if node, parseErr := csf.Parse(initial.source.Body); parseErr == nil && rs.ExpandJiraMacros {
			if _, bindErr := s.prepareConfluenceJiraMacroPopulation(m.Root, len(mirror.JiraMacroDescriptors(node)) > 0, false); bindErr != nil {
				result.Status = "blocked"
				return result, bindErr
			}
		}
	}

	prewrite, err := s.confluencePageCopySnapshot(ctx, sourceID, opts)
	if err != nil || confluencePageCopyProposalHash(prewrite) != proposalHash {
		result.Status = "blocked"
		result.Complete = err == nil
		return result, &confluencePageCopyWriteError{message: "page copy proposal changed or could not be revalidated immediately before the write", cause: sanitizeConfluenceWriteCause(err), closed: true}
	}

	result.WriteAttempted = true
	created, writeErr := s.store.CreatePage(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), prewrite.space, prewrite.parent, prewrite.title, prewrite.source.Body)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, &confluencePageCopyWriteError{message: "Confluence rejected the page copy; it was not applied", cause: sanitizeConfluenceWriteCause(writeErr)}
	}
	if created == nil || !canonicalConfluenceContentID(created.ID) {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluencePageCopyAmbiguousError("page copy outcome is unknown because the create response did not prove the new page id; do not retry or search by title", writeErr)
	}
	if created.ID == sourceID || created.ID == prewrite.parent {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluencePageCopyAmbiguousError("page copy outcome is unknown because the create response reused the source or target-parent identity; do not retry or search by title", writeErr)
	}
	result.ID = created.ID

	readback, readbackErr := s.readExactCurrentConfluencePageWithRestrictions(ctx, created.ID, opts.Register && confluenceNeedsRestrictions(rs))
	if readbackErr != nil || !confluencePageCopyReadbackMatches(prewrite, readback) {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluencePageCopyAmbiguousError("page copy outcome is unknown because authoritative readback did not match the reviewed copy; preserve the emitted id and do not replay", errors.Join(writeErr, sanitizeConfluenceWriteCause(readbackErr)))
	}
	result.Reconciled = true
	result.ID, result.Title, result.Version, result.URL = readback.ID, readback.Title, readback.Version, readback.URL
	if writeErr != nil {
		result.Status = "recovered"
	} else {
		result.Status = "applied"
	}
	if !opts.Register {
		return result, nil
	}
	registration.ReadbackReconciled = true
	_, registration, registerErr := s.registerConfluenceReadback(ctx, m, rs, registration, readback)
	result.Registration = registration
	if registration != nil {
		result.Complete = registration.Status == "registered"
	}
	if registerErr != nil {
		result.Status = "applied_not_registered"
		return result, registerErr
	}
	return result, nil
}

func previewConfluenceCopyRegistration(root, baseURL string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect registration root", domain.ErrCheckFailed)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: registration root is not a directory", domain.ErrCheckFailed)
	}
	return prepareMirrorBackendPopulation(root, "confluence", baseURL, ".csf", true)
}

func (s *ConfluenceService) confluencePageCopySnapshot(ctx context.Context, sourceID string, opts ConfluencePageCopyOpts) (confluencePageCopySnapshot, error) {
	source, err := s.readExactCurrentConfluencePage(ctx, sourceID)
	if err != nil {
		return confluencePageCopySnapshot{}, err
	}
	space := strings.TrimSpace(opts.Space)
	if space == "" {
		space = source.SpaceKey
	}
	parent := strings.TrimSpace(opts.Parent)
	if parent == "" {
		parent = source.Parent
	}
	backendSHA256, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return confluencePageCopySnapshot{}, fmt.Errorf("%w: invalid Confluence backend identity", domain.ErrCheckFailed)
	}
	titleSum := sha256.Sum256([]byte(opts.Title))
	sourceTitleSum := sha256.Sum256([]byte(source.Title))
	snapshot := confluencePageCopySnapshot{
		source: source, backendSHA256: backendSHA256, title: opts.Title,
		titleSHA256: hex.EncodeToString(titleSum[:]), space: space, parent: parent,
		sourceTitleSHA256:   hex.EncodeToString(sourceTitleSum[:]),
		sourceHierarchyHash: confluencePageHierarchyHash(source.AncestorIDs, source.Ancestors),
		register:            opts.Register,
	}
	if opts.Register {
		sum := sha256.Sum256([]byte(filepath.Clean(opts.Root)))
		snapshot.rootSHA256 = hex.EncodeToString(sum[:])
	}
	if parent == "" {
		snapshot.targetHierarchyHash = confluencePageHierarchyHash(nil, nil)
		return snapshot, nil
	}
	parentPage := source
	if parent != sourceID {
		parentPage, err = s.readExactCurrentConfluencePage(ctx, parent)
		if err != nil {
			return confluencePageCopySnapshot{}, err
		}
	}
	if parentPage.SpaceKey != space {
		return confluencePageCopySnapshot{}, fmt.Errorf("%w: target parent is not in the target space", domain.ErrCheckFailed)
	}
	snapshot.parentVersion = parentPage.Version
	snapshot.parentBodySHA256 = mirror.Hash(parentPage.Body)
	snapshot.parentHierarchyHash = confluencePageHierarchyHash(parentPage.AncestorIDs, parentPage.Ancestors)
	targetIDs := append(append([]string(nil), parentPage.AncestorIDs...), parentPage.ID)
	targetTitles := append(append([]string(nil), parentPage.Ancestors...), parentPage.Title)
	snapshot.targetHierarchyHash = confluencePageHierarchyHash(targetIDs, targetTitles)
	return snapshot, nil
}

func (s *ConfluenceService) readExactCurrentConfluencePage(ctx context.Context, id string) (*domain.Resource, error) {
	return s.readExactCurrentConfluencePageWithRestrictions(ctx, id, false)
}

func (s *ConfluenceService) readExactCurrentConfluencePageWithRestrictions(ctx context.Context, id string, includeRestrictions bool) (*domain.Resource, error) {
	reader, ok := s.store.(domain.ConfluencePageStatusReader)
	if !ok {
		return nil, fmt.Errorf("%w: Confluence backend does not expose exact current page reads", domain.ErrCheckFailed)
	}
	page, err := reader.GetPageByStatus(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), id, "current", domain.PullOpts{Format: "csf", IncludeRestrictions: includeRestrictions})
	if err != nil {
		return nil, err
	}
	if err := validateExactConfluencePageRead(page, id, "current"); err != nil {
		return nil, err
	}
	return page, nil
}

func confluencePageCopyProposalHash(snapshot confluencePageCopySnapshot) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion       int    `json:"schema_version"`
		Operation           string `json:"operation"`
		BackendSHA256       string `json:"backend_sha256"`
		SourceID            string `json:"source_id"`
		SourceType          string `json:"source_type"`
		SourceStatus        string `json:"source_status"`
		SourceVersion       int    `json:"source_version"`
		SourceBodySHA256    string `json:"source_body_sha256"`
		SourceBodyBytes     int    `json:"source_body_bytes"`
		SourceTitleSHA256   string `json:"source_title_sha256"`
		SourceSpace         string `json:"source_space"`
		SourceParent        string `json:"source_parent"`
		SourceHierarchyHash string `json:"source_hierarchy_hash"`
		TitleSHA256         string `json:"title_sha256"`
		TargetSpace         string `json:"target_space"`
		TargetParent        string `json:"target_parent"`
		ParentVersion       int    `json:"parent_version"`
		ParentBodySHA256    string `json:"parent_body_sha256"`
		ParentHierarchyHash string `json:"parent_hierarchy_hash"`
		TargetHierarchyHash string `json:"target_hierarchy_hash"`
		Register            bool   `json:"register"`
		RootSHA256          string `json:"root_sha256"`
	}{
		SchemaVersion: confluencePageCopySchemaVersion, Operation: "copy",
		BackendSHA256: snapshot.backendSHA256, SourceID: snapshot.source.ID,
		SourceType: snapshot.source.Type, SourceStatus: snapshot.source.Status,
		SourceVersion: snapshot.source.Version, SourceBodySHA256: mirror.Hash(snapshot.source.Body),
		SourceBodyBytes: len(snapshot.source.Body), SourceTitleSHA256: snapshot.sourceTitleSHA256,
		SourceSpace:  snapshot.source.SpaceKey,
		SourceParent: snapshot.source.Parent, TitleSHA256: snapshot.titleSHA256,
		SourceHierarchyHash: snapshot.sourceHierarchyHash,
		TargetSpace:         snapshot.space, TargetParent: snapshot.parent,
		ParentVersion: snapshot.parentVersion, ParentBodySHA256: snapshot.parentBodySHA256,
		ParentHierarchyHash: snapshot.parentHierarchyHash, Register: snapshot.register,
		TargetHierarchyHash: snapshot.targetHierarchyHash, RootSHA256: snapshot.rootSHA256,
	})
	return guardedProposalDigest(canonical)
}

func confluencePageCopyReadbackMatches(snapshot confluencePageCopySnapshot, page *domain.Resource) bool {
	if page == nil || page.Version != 1 || page.Title != snapshot.title || page.SpaceKey != snapshot.space || page.Parent != snapshot.parent ||
		confluencePageHierarchyHash(page.AncestorIDs, page.Ancestors) != snapshot.targetHierarchyHash {
		return false
	}
	return len(page.Body) == len(snapshot.source.Body) && mirror.Hash(page.Body) == mirror.Hash(snapshot.source.Body)
}

func confluencePageHierarchyHash(ids, titles []string) string {
	canonical, _ := json.Marshal(struct {
		IDs    []string `json:"ids"`
		Titles []string `json:"titles"`
	}{IDs: ids, Titles: titles})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func confluencePageCopyAmbiguousError(message string, cause error) error {
	return &confluencePageCopyWriteError{message: message, cause: sanitizeConfluenceWriteCause(cause), closed: true, ambiguous: true}
}

func ConfluencePageCopyText(result *ConfluencePageCopyResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\nsource_id: %s\ncreated_id: %s\nsource_version: %d\nproposal_hash: %s", result.Status, result.SourceID, result.ID, result.CurrentVersion, result.ProposalHash)
}
