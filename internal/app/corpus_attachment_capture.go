package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

var (
	errCorpusAttachmentSizeMismatch       = errors.New("attachment size mismatch")
	errCorpusAttachmentEvidenceIncomplete = errors.New("attachment evidence is incomplete")
)

const confluenceAttachmentSidecarPreflightReserve = 4 << 10

type corpusAttachmentPayload struct {
	path mirror.ArtifactPath
	data []byte
}

type corpusAttachmentCapture struct {
	inventoryComplete bool
	inventoryReason   string
	bodiesState       mirror.AttachmentBodiesState
	bodyBytes         int64
	records           []mirror.AttachmentSidecarRecord
	partialReasons    []mirror.AttachmentPartialReason
	payloads          []corpusAttachmentPayload
}

type corpusAttachmentOpen func(context.Context, domain.Attachment) (io.ReadCloser, error)

func captureCorpusAttachments(
	ctx context.Context,
	root string,
	service string,
	parentID string,
	stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
	open corpusAttachmentOpen,
) (corpusAttachmentCapture, error) {
	limit := 0
	if options != nil {
		limit = options.binding.MaxAttachmentBodiesPerItem
	}
	return captureCorpusAttachmentsWithBodyLimit(ctx, root, service, parentID, stem, inventory, options, limit, limit > 0, 0, false, open)
}

// captureCorpusAttachmentsWithBodyLimit applies a per-parent limit already
// reserved by the complete-pull transaction planner. Other corpus callers use
// captureCorpusAttachments and retain the policy-bound limit above.
func captureCorpusAttachmentsWithBodyLimit(
	ctx context.Context,
	root string,
	service string,
	parentID string,
	stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
	bodyLimit int,
	enforceBodyLimit bool,
	bodyByteLimit int64,
	enforceBodyByteLimit bool,
	open corpusAttachmentOpen,
) (corpusAttachmentCapture, error) {
	return captureCorpusAttachmentsWithBodyLimitMode(
		ctx, root, service, parentID, stem, inventory, options,
		bodyLimit, enforceBodyLimit, bodyByteLimit, enforceBodyByteLimit, false, open,
	)
}

// captureCorpusAttachmentsWithBodyLimitInMemory is the write-free counterpart
// used by complete-pull dry runs. Its bounded in-memory reader preserves the
// same byte/hash qualification as staging while never creating a target-root
// directory or private staging residue.
func captureCorpusAttachmentsWithBodyLimitInMemory(
	ctx context.Context,
	root string,
	service string,
	parentID string,
	stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
	bodyLimit int,
	enforceBodyLimit bool,
	bodyByteLimit int64,
	enforceBodyByteLimit bool,
	open corpusAttachmentOpen,
) (corpusAttachmentCapture, error) {
	return captureCorpusAttachmentsWithBodyLimitMode(
		ctx, root, service, parentID, stem, inventory, options,
		bodyLimit, enforceBodyLimit, bodyByteLimit, enforceBodyByteLimit, true, open,
	)
}

func captureCorpusAttachmentsWithBodyLimitMode(
	ctx context.Context,
	root string,
	service string,
	parentID string,
	stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
	bodyLimit int,
	enforceBodyLimit bool,
	bodyByteLimit int64,
	enforceBodyByteLimit bool,
	inMemory bool,
	open corpusAttachmentOpen,
) (corpusAttachmentCapture, error) {
	if options == nil || !options.binding.Attachments || ctx == nil || options.binding.AttachmentBodies && options.budget == nil {
		return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment capture policy is unavailable", domain.ErrCheckFailed)
	}
	if inventory.Attachments == nil {
		return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment inventory collection is unavailable", domain.ErrCheckFailed)
	}
	// Confluence attachment capture has a narrower durable identity grammar than
	// the adapter's read-only endpoint grammar. Validate the exact sidecar/body
	// intersection, all sidecar metadata, and its conservative publication size
	// before planning or opening a binary body. A late failure here would make a
	// strict pull consume remote evidence that cannot join its atomic page
	// transaction.
	switch service {
	case mirror.CorpusSnapshotConfluence:
		if err := validateConfluenceAttachmentCapturePreflight(parentID, stem, inventory, options); err != nil {
			return corpusAttachmentCapture{}, err
		}
	case mirror.CorpusSnapshotJira:
		if err := validateJiraAttachmentCapturePreflight(parentID, "", stem, inventory, options); err != nil {
			return corpusAttachmentCapture{}, err
		}
	}
	if bodyLimit < 0 || bodyByteLimit < 0 {
		return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment body count policy is invalid", domain.ErrCheckFailed)
	}
	attachments := append([]domain.Attachment{}, inventory.Attachments...)
	sort.Slice(attachments, func(i, j int) bool { return jiraNumericIdentityLess(attachments[i].ID, attachments[j].ID) })
	capture := corpusAttachmentCapture{
		inventoryComplete: inventory.Complete,
		inventoryReason:   inventory.PartialReason,
		bodiesState:       mirror.AttachmentBodiesNotRequested,
		records:           make([]mirror.AttachmentSidecarRecord, 0, len(attachments)),
		partialReasons:    []mirror.AttachmentPartialReason{},
		payloads:          []corpusAttachmentPayload{},
	}
	if !inventory.Complete {
		reason := corpusAttachmentInventoryPartialReason(service, inventory.PartialReason)
		if reason == "" {
			return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment inventory reason is invalid", domain.ErrCheckFailed)
		}
		capture.partialReasons = append(capture.partialReasons, reason)
		if !options.binding.AllowPartialEvidence {
			return corpusAttachmentCapture{}, fmt.Errorf("%w: requested attachment inventory is incomplete: %w", domain.ErrCheckFailed, errCorpusAttachmentEvidenceIncomplete)
		}
	}
	if options.binding.AttachmentBodies {
		capture.bodiesState = mirror.AttachmentBodiesComplete
		// The complete-pull page envelope is necessary but not sufficient on a
		// resumed run: previously committed attachment prefixes already consume
		// the aggregate policy. Fold the exact remaining aggregate into this
		// pre-download plan so strict mode cannot fetch a prefix and discover
		// the exhausted budget only at the next body.
		remaining := options.budget.remaining()
		if !enforceBodyByteLimit || remaining < bodyByteLimit {
			bodyByteLimit = remaining
			enforceBodyByteLimit = true
		}
	}
	// An oversized allowlisted body is a policy-known partial outcome, not a
	// transport outcome. Refuse it before any earlier sorted sibling can be
	// opened in strict mode.
	if options.binding.AttachmentBodies && !options.binding.AllowPartialEvidence {
		for _, attachment := range attachments {
			if corpusAttachmentMediaAllowed(options.binding.AttachmentMediaTypes, attachment.MediaType) && attachment.FileSize > options.binding.MaxAttachmentBytes {
				return corpusAttachmentCapture{}, fmt.Errorf("%w: requested attachment body exceeds the item policy: %w", domain.ErrCheckFailed, errCorpusAttachmentEvidenceIncomplete)
			}
		}
	}
	// Decide the per-parent publication subset before opening any binary body.
	// A strict pull therefore never transfers a prefix that cannot fit its
	// atomic complete-pull publication. Partial mode records the deterministic
	// first eligible subset and proves why the remainder was not read.
	allowedBodies, countLimited, byteLimited, planErr := planCorpusAttachmentBodiesWithLimit(
		attachments, options, bodyLimit, enforceBodyLimit, bodyByteLimit, enforceBodyByteLimit,
	)
	if planErr != nil {
		return corpusAttachmentCapture{}, planErr
	}
	// Reserve the whole deterministic plan before opening its first body. The
	// budget can be shared by a resumed corpus run, so a second caller cannot
	// turn an otherwise preflighted strict page into an unstaged prefix between
	// planning and its binary reads. Failed partial bodies release their own
	// share below; an aborted strict capture releases the remaining reservation.
	var reserved int64
	if options.binding.AttachmentBodies {
		for _, attachment := range attachments {
			if allowedBodies[attachment.ID] {
				reserved += attachment.FileSize
			}
		}
		if !options.budget.reserve(reserved) {
			return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment body aggregate reservation changed before capture", domain.ErrCheckFailed)
		}
	}
	releaseReserved := func(size int64) {
		if size <= 0 {
			return
		}
		options.budget.release(size)
		reserved -= size
	}
	releaseCaptureReservation := func() {
		if reserved > 0 {
			options.budget.release(reserved)
			reserved = 0
		}
	}
	for _, attachment := range attachments {
		record := corpusAttachmentRecord(service, attachment)
		if !options.binding.AttachmentBodies {
			record.Body.State = mirror.AttachmentBodyNotRequested
			capture.records = append(capture.records, record)
			continue
		}
		if !corpusAttachmentMediaAllowed(options.binding.AttachmentMediaTypes, attachment.MediaType) {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonMediaExcluded}
			capture.records = append(capture.records, record)
			continue
		}
		if attachment.FileSize > options.binding.MaxAttachmentBytes {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonItemLimit}
			if err := capture.notePartial(options, mirror.AttachmentReasonBodyItemLimit); err != nil {
				return corpusAttachmentCapture{}, err
			}
			capture.records = append(capture.records, record)
			continue
		}
		if countLimited[attachment.ID] {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonCountLimit}
			if err := capture.notePartial(options, mirror.AttachmentReasonBodyCountLimit); err != nil {
				return corpusAttachmentCapture{}, err
			}
			capture.records = append(capture.records, record)
			continue
		}
		if byteLimited[attachment.ID] {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}
			if err := capture.notePartial(options, mirror.AttachmentReasonBodyAggregateLimit); err != nil {
				return corpusAttachmentCapture{}, err
			}
			capture.records = append(capture.records, record)
			continue
		}
		if !allowedBodies[attachment.ID] {
			releaseCaptureReservation()
			return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment body plan is incomplete", domain.ErrCheckFailed)
		}
		bodyPath, err := mirror.NewPublicArtifactPath(stem + ".attachments/" + attachment.ID + ".body")
		if err != nil {
			releaseCaptureReservation()
			return corpusAttachmentCapture{}, err
		}
		reader, err := open(ctx, attachment)
		if err != nil {
			releaseReserved(attachment.FileSize)
			reason := mirror.AttachmentBodyReasonFailed
			state := mirror.AttachmentBodyFailed
			partial := mirror.AttachmentReasonBodyFailed
			if errors.Is(err, domain.ErrForbidden) {
				reason = mirror.AttachmentBodyReasonForbidden
				state = mirror.AttachmentBodyForbidden
				partial = mirror.AttachmentReasonBodyForbidden
			}
			record.Body = mirror.AttachmentSidecarBody{State: state, Reason: reason}
			if partialErr := capture.notePartial(options, partial); partialErr != nil {
				releaseCaptureReservation()
				return corpusAttachmentCapture{}, errors.Join(partialErr, err)
			}
			capture.records = append(capture.records, record)
			continue
		}
		var data []byte
		var digest string
		var readErr error
		if inMemory {
			data, digest, readErr = streamCorpusAttachmentInMemory(ctx, parentID, attachment.ID, attachment.FileSize, reader)
		} else {
			data, digest, readErr = streamCorpusAttachment(ctx, root, parentID, attachment.ID, attachment.FileSize, reader)
		}
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			releaseReserved(attachment.FileSize)
			bodyReason := mirror.AttachmentBodyReasonFailed
			partial := mirror.AttachmentReasonBodyFailed
			if errors.Is(readErr, errCorpusAttachmentSizeMismatch) {
				bodyReason = mirror.AttachmentBodyReasonSizeMismatch
				partial = mirror.AttachmentReasonBodySizeMismatch
			}
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyFailed, Reason: bodyReason}
			if partialErr := capture.notePartial(options, partial); partialErr != nil {
				releaseCaptureReservation()
				return corpusAttachmentCapture{}, errors.Join(partialErr, readErr, closeErr)
			}
			capture.records = append(capture.records, record)
			continue
		}
		record.Body = mirror.AttachmentSidecarBody{
			State: mirror.AttachmentBodyCaptured, Path: bodyPath.String(), Size: int64(len(data)), SHA256: digest,
		}
		capture.records = append(capture.records, record)
		capture.bodyBytes += int64(len(data))
		capture.payloads = append(capture.payloads, corpusAttachmentPayload{path: bodyPath, data: data})
	}
	if options.binding.AttachmentBodies && capture.bodiesState == mirror.AttachmentBodiesComplete && !inventory.Complete {
		// Every observed body may be complete, but the requested body set is not:
		// an unobserved attachment cannot be silently treated as excluded.
		capture.bodiesState = mirror.AttachmentBodiesPartial
	}
	sort.Slice(capture.partialReasons, func(i, j int) bool { return capture.partialReasons[i] < capture.partialReasons[j] })
	return capture, nil
}

// validateJiraAttachmentCapturePreflight validates the durable Jira sidecar
// and streamed-body identity intersection before the first revalidation or
// binary request. The read-only attachment field accepts broader provider
// identifiers than the content-addressed capture layout, so this boundary
// rejects an unrepresentable inventory rather than downloading bytes that can
// never join the complete-pull transaction.
func validateJiraAttachmentCapturePreflight(
	parentID, parentRevision, stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
) error {
	if options == nil || !options.binding.Attachments || !canonicalPositiveNumericString(parentID) {
		return fmt.Errorf("%w: Jira attachment capture identity is invalid", domain.ErrCheckFailed)
	}
	if parentRevision == "" {
		// Generic capture tests and compatibility callers do not carry an issue
		// revision. Production Jira complete pulls pass the exact validated value
		// below before any body request.
		parentRevision = "revision"
	}
	if !domain.ValidJiraEvidenceParentRevision(parentRevision) {
		return fmt.Errorf("%w: Jira attachment capture parent revision is invalid", domain.ErrCheckFailed)
	}
	capture := corpusAttachmentCapture{
		inventoryComplete: inventory.Complete,
		inventoryReason:   inventory.PartialReason,
		bodiesState:       mirror.AttachmentBodiesNotRequested,
		records:           make([]mirror.AttachmentSidecarRecord, 0, len(inventory.Attachments)),
		partialReasons:    []mirror.AttachmentPartialReason{},
	}
	if !inventory.Complete {
		reason := corpusAttachmentInventoryPartialReason(mirror.CorpusSnapshotJira, inventory.PartialReason)
		if reason == "" {
			return fmt.Errorf("%w: Jira attachment capture inventory reason is invalid", domain.ErrCheckFailed)
		}
		capture.partialReasons = append(capture.partialReasons, reason)
	}
	if options.binding.AttachmentBodies {
		capture.bodiesState = mirror.AttachmentBodiesComplete
		if !inventory.Complete {
			capture.bodiesState = mirror.AttachmentBodiesPartial
		}
	}
	for _, attachment := range inventory.Attachments {
		if !canonicalPositiveNumericString(attachment.ID) {
			return fmt.Errorf("%w: Jira attachment capture requires canonical positive attachment identities", domain.ErrCheckFailed)
		}
		record := corpusAttachmentRecord(mirror.CorpusSnapshotJira, attachment)
		if options.binding.AttachmentBodies {
			bodyPath, err := mirror.NewPublicArtifactPath(stem + ".attachments/" + attachment.ID + ".body")
			if err != nil {
				return fmt.Errorf("%w: Jira attachment body destination is invalid", domain.ErrCheckFailed)
			}
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyCaptured, Path: bodyPath.String(), Size: attachment.FileSize, SHA256: mirror.Hash(nil)}
		} else {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyNotRequested}
		}
		capture.records = append(capture.records, record)
	}
	provisional := mirror.AttachmentSidecarV1{
		SchemaVersion: mirror.AttachmentSidecarSchemaV1,
		Service:       mirror.CorpusSnapshotJira, OriginSHA256: "sha256:" + mirror.Hash(nil),
		ParentID: parentID, ParentRevision: parentRevision, NativeSHA256: mirror.Hash(nil), MetadataSHA256: mirror.Hash(nil),
		InventoryComplete: capture.inventoryComplete, InventoryPartialReason: capture.inventoryReason,
		BodiesState: capture.bodiesState, Complete: capture.inventoryComplete && capture.bodiesState != mirror.AttachmentBodiesPartial,
		Count: len(capture.records), PartialReasons: capture.partialReasons, Attachments: capture.records,
	}
	if _, err := mirror.EncodeAttachmentSidecarV1(provisional); err != nil {
		return fmt.Errorf("%w: Jira attachment capture sidecar is invalid", domain.ErrCheckFailed)
	}
	return nil
}

// validateConfluenceAttachmentCapturePreflight validates the subset of an
// adapter inventory that can be made into a durable Confluence sidecar and (if
// requested) passed to the streamed body path. The adapter deliberately admits
// opaque IDs for read-only routes, while the existing sidecar and staging path
// require canonical uint64 identities. Keeping that boundary here prevents an
// otherwise valid backend read from being misrepresented or fetched and then
// discarded.
func validateConfluenceAttachmentCapturePreflight(
	parentID, stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
) error {
	_, err := confluenceAttachmentCapturePublicationReservation(parentID, stem, inventory, options)
	return err
}

// confluenceAttachmentCapturePublicationReservation validates every durable
// sidecar/body field before a binary request and returns the exact provisional
// sidecar bytes plus the bounded status suffix that a final capture may add.
// The page transaction planner reserves this amount alongside its known core,
// macro, and asset bytes before it opens attachment bodies.
func confluenceAttachmentCapturePublicationReservation(
	parentID, stem string,
	inventory domain.AttachmentInventory,
	options *corpusPullEvidenceOptions,
) (int64, error) {
	if options == nil || !options.binding.Attachments || !canonicalPositiveNumericString(parentID) {
		return 0, fmt.Errorf("%w: Confluence attachment capture identity is invalid", domain.ErrCheckFailed)
	}
	capture := corpusAttachmentCapture{
		inventoryComplete: inventory.Complete,
		inventoryReason:   inventory.PartialReason,
		bodiesState:       mirror.AttachmentBodiesNotRequested,
		records:           make([]mirror.AttachmentSidecarRecord, 0, len(inventory.Attachments)),
		partialReasons:    []mirror.AttachmentPartialReason{},
	}
	if !inventory.Complete {
		reason := corpusAttachmentInventoryPartialReason(mirror.CorpusSnapshotConfluence, inventory.PartialReason)
		if reason == "" {
			return 0, fmt.Errorf("%w: Confluence attachment inventory reason is invalid", domain.ErrCheckFailed)
		}
		capture.partialReasons = append(capture.partialReasons, reason)
	}
	if options.binding.AttachmentBodies {
		capture.bodiesState = mirror.AttachmentBodiesComplete
		if !inventory.Complete {
			capture.bodiesState = mirror.AttachmentBodiesPartial
		}
	}
	for _, attachment := range inventory.Attachments {
		if attachment.Version <= 0 || !canonicalPositiveNumericString(attachment.ID) || attachment.ID == parentID {
			return 0, fmt.Errorf("%w: Confluence attachment capture requires a canonical positive attachment selector", domain.ErrCheckFailed)
		}
		// Attachment binaries are addressed by a title/version selector rather
		// than their opaque inventory id. Sidecar grammar intentionally permits
		// a broader human filename field, but a body-enabled capture must prove
		// that this exact title can reach the bounded backend selector before it
		// plans a body or creates partial evidence.
		if options.binding.AttachmentBodies && !ValidConfluenceAttachmentDownloadFilename(attachment.Title) {
			return 0, fmt.Errorf("%w: Confluence attachment capture filename selector is invalid", domain.ErrCheckFailed)
		}
		record := corpusAttachmentRecord(mirror.CorpusSnapshotConfluence, attachment)
		if options.binding.AttachmentBodies {
			bodyPath, err := mirror.NewPublicArtifactPath(stem + ".attachments/" + attachment.ID + ".body")
			if err != nil {
				return 0, fmt.Errorf("%w: Confluence attachment body destination is invalid", domain.ErrCheckFailed)
			}
			record.Body = mirror.AttachmentSidecarBody{
				State: mirror.AttachmentBodyCaptured, Path: bodyPath.String(), Size: attachment.FileSize, SHA256: mirror.Hash(nil),
			}
		} else {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyNotRequested}
		}
		capture.records = append(capture.records, record)
	}
	// A canonical placeholder binding validates the complete sidecar grammar
	// without depending on a mutable backend binding. The real origin/native/
	// metadata hashes are still taken from the revalidated page at publication.
	provisional := mirror.AttachmentSidecarV1{
		SchemaVersion: mirror.AttachmentSidecarSchemaV1,
		Service:       mirror.CorpusSnapshotConfluence, OriginSHA256: "sha256:" + mirror.Hash(nil),
		ParentID: parentID, ParentVersion: 1,
		NativeSHA256: mirror.Hash(nil), MetadataSHA256: mirror.Hash(nil),
		InventoryComplete: capture.inventoryComplete, InventoryPartialReason: capture.inventoryReason,
		BodiesState: capture.bodiesState, Complete: capture.inventoryComplete && capture.bodiesState != mirror.AttachmentBodiesPartial,
		Count: len(capture.records), PartialReasons: capture.partialReasons, Attachments: capture.records,
	}
	encoded, err := mirror.EncodeAttachmentSidecarV1(provisional)
	if err != nil {
		return 0, fmt.Errorf("%w: Confluence attachment capture sidecar is invalid", domain.ErrCheckFailed)
	}
	if err := mirror.ValidateAttachmentSidecarPublicationData(encoded, confluenceAttachmentSidecarPreflightReserve); err != nil {
		return 0, fmt.Errorf("%w: Confluence attachment capture sidecar exceeds its pre-download publication bound", domain.ErrCheckFailed)
	}
	return int64(len(encoded)) + confluenceAttachmentSidecarPreflightReserve, nil
}

func planCorpusAttachmentBodiesWithLimit(
	attachments []domain.Attachment,
	options *corpusPullEvidenceOptions,
	limit int,
	enforceLimit bool,
	byteLimit int64,
	enforceByteLimit bool,
) (map[string]bool, map[string]bool, map[string]bool, error) {
	allowed := make(map[string]bool, len(attachments))
	limited := make(map[string]bool)
	byteLimited := make(map[string]bool)
	if options == nil || !options.binding.AttachmentBodies || limit < 0 || byteLimit < 0 {
		return allowed, limited, byteLimited, nil
	}
	eligible := 0
	var plannedBytes int64
	for _, attachment := range attachments {
		if !corpusAttachmentMediaAllowed(options.binding.AttachmentMediaTypes, attachment.MediaType) || attachment.FileSize > options.binding.MaxAttachmentBytes {
			continue
		}
		eligible++
		if !enforceLimit || eligible <= limit {
			if !enforceByteLimit || attachment.FileSize <= byteLimit-plannedBytes {
				allowed[attachment.ID] = true
				plannedBytes += attachment.FileSize
				continue
			}
			byteLimited[attachment.ID] = true
			continue
		}
		limited[attachment.ID] = true
	}
	if (len(limited) != 0 || len(byteLimited) != 0) && !options.binding.AllowPartialEvidence {
		return nil, nil, nil, fmt.Errorf("%w: requested attachment bodies exceed the per-parent publication bound: %w", domain.ErrCheckFailed, errCorpusAttachmentEvidenceIncomplete)
	}
	return allowed, limited, byteLimited, nil
}

func (capture *corpusAttachmentCapture) notePartial(options *corpusPullEvidenceOptions, reason mirror.AttachmentPartialReason) error {
	if !options.binding.AllowPartialEvidence {
		return fmt.Errorf("%w: requested attachment body evidence is incomplete: %w", domain.ErrCheckFailed, errCorpusAttachmentEvidenceIncomplete)
	}
	capture.bodiesState = mirror.AttachmentBodiesPartial
	for _, existing := range capture.partialReasons {
		if existing == reason {
			return nil
		}
	}
	capture.partialReasons = append(capture.partialReasons, reason)
	return nil
}

func corpusAttachmentRecord(service string, attachment domain.Attachment) mirror.AttachmentSidecarRecord {
	version := attachment.Version
	if service == mirror.CorpusSnapshotJira {
		version = 0
	}
	return mirror.AttachmentSidecarRecord{
		ID: attachment.ID, Version: version, Filename: attachment.Title,
		MediaType: attachment.MediaType, DeclaredSize: attachment.FileSize, CreatedAt: attachment.Created,
		Author: mirror.AttachmentSidecarAuthor{
			ID: attachment.AuthorKey, Name: attachment.AuthorName, DisplayName: attachment.Author,
		},
	}
}

func corpusAttachmentInventoryPartialReason(service, reason string) mirror.AttachmentPartialReason {
	switch reason {
	case domain.AttachmentPartialPageLimit:
		return mirror.AttachmentReasonInventoryPageLimit
	case domain.AttachmentPartialItemLimit:
		return mirror.AttachmentReasonInventoryItemLimit
	case domain.AttachmentPartialPaginationStalled:
		return mirror.AttachmentReasonInventoryStalled
	case domain.AttachmentPartialLegacyUnqualified:
		return mirror.AttachmentReasonInventoryLegacy
	case domain.JiraAttachmentPartialFieldUnavailable:
		return mirror.AttachmentReasonInventoryField
	case mirror.AttachmentInventoryForbidden:
		return mirror.AttachmentReasonInventoryForbidden
	case mirror.AttachmentInventoryUnsupported:
		return mirror.AttachmentReasonInventoryUnsupported
	default:
		_ = service
		return ""
	}
}

type corpusAttachmentBoundedReader struct {
	source    io.Reader
	hash      io.Writer
	remaining int64
}

func (reader *corpusAttachmentBoundedReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		var probe [1]byte
		n, err := reader.source.Read(probe[:])
		if n > 0 {
			return 0, errCorpusAttachmentSizeMismatch
		}
		return 0, err
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	n, err := reader.source.Read(buffer)
	if n > 0 {
		reader.remaining -= int64(n)
		_, _ = reader.hash.Write(buffer[:n])
	}
	return n, err
}

func streamCorpusAttachment(ctx context.Context, root, parentID, attachmentID string, declaredSize int64, source io.Reader) ([]byte, string, error) {
	if ctx == nil || declaredSize < 0 || !canonicalPositiveNumericString(parentID) || !canonicalPositiveNumericString(attachmentID) {
		return nil, "", fmt.Errorf("%w: attachment stream identity is invalid", domain.ErrCheckFailed)
	}
	directory := filepath.Join(root, ".atl", "corpus-attachment-staging", parentID)
	if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
		return nil, "", err
	}
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, "", fmt.Errorf("%w: attachment staging directory is unsafe", domain.ErrCheckFailed)
	}
	target := filepath.Join(directory, attachmentID+".body")
	if existing, statErr := safepath.StatWithin(root, target); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode().Perm() != 0o600 {
			return nil, "", fmt.Errorf("%w: attachment staging residue is unsafe", domain.ErrCheckFailed)
		}
		if removeErr := safepath.RemoveWithin(root, target); removeErr != nil {
			return nil, "", removeErr
		}
	} else if !os.IsNotExist(statErr) {
		return nil, "", statErr
	}
	hash := sha256.New()
	reader := &corpusAttachmentBoundedReader{source: source, hash: hash, remaining: declaredSize}
	written, err := safepath.WriteReaderAtomicWithin(root, target, reader, 0o600)
	if err == nil && (written != declaredSize || reader.remaining != 0) {
		err = errCorpusAttachmentSizeMismatch
	}
	if ctxErr := ctx.Err(); err == nil && ctxErr != nil {
		err = ctxErr
	}
	if err != nil {
		_ = safepath.RemoveWithin(root, target)
		return nil, "", err
	}
	staged, readErr := safepath.ReadFileWithinLimit(root, target, declaredSize)
	if readErr != nil || int64(len(staged)) != declaredSize || hex.EncodeToString(hash.Sum(nil)) != mirror.Hash(staged) {
		_ = safepath.RemoveWithin(root, target)
		return nil, "", errors.Join(errCorpusAttachmentSizeMismatch, readErr)
	}
	if err := safepath.RemoveWithin(root, target); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	return staged, mirror.Hash(staged), nil
}

func streamCorpusAttachmentInMemory(ctx context.Context, parentID, attachmentID string, declaredSize int64, source io.Reader) ([]byte, string, error) {
	if ctx == nil || declaredSize < 0 || !canonicalPositiveNumericString(parentID) || !canonicalPositiveNumericString(attachmentID) {
		return nil, "", fmt.Errorf("%w: attachment stream identity is invalid", domain.ErrCheckFailed)
	}
	reader := &corpusAttachmentBoundedReader{source: source, hash: sha256.New(), remaining: declaredSize}
	data, err := io.ReadAll(reader)
	if err == nil && (int64(len(data)) != declaredSize || reader.remaining != 0) {
		err = errCorpusAttachmentSizeMismatch
	}
	if ctxErr := ctx.Err(); err == nil && ctxErr != nil {
		err = ctxErr
	}
	if err != nil {
		return nil, "", err
	}
	return data, mirror.Hash(data), nil
}
