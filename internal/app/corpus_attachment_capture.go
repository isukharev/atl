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

var errCorpusAttachmentSizeMismatch = errors.New("attachment size mismatch")

type corpusAttachmentPayload struct {
	path mirror.ArtifactPath
	data []byte
}

type corpusAttachmentCapture struct {
	inventoryComplete bool
	inventoryReason   string
	bodiesState       mirror.AttachmentBodiesState
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
	if options == nil || !options.binding.Attachments || ctx == nil || options.binding.AttachmentBodies && options.budget == nil {
		return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment capture policy is unavailable", domain.ErrCheckFailed)
	}
	if inventory.Attachments == nil {
		return corpusAttachmentCapture{}, fmt.Errorf("%w: attachment inventory collection is unavailable", domain.ErrCheckFailed)
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
			return corpusAttachmentCapture{}, fmt.Errorf("%w: requested attachment inventory is incomplete", domain.ErrCheckFailed)
		}
	}
	if options.binding.AttachmentBodies {
		capture.bodiesState = mirror.AttachmentBodiesComplete
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
		if !options.budget.reserve(attachment.FileSize) {
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}
			if err := capture.notePartial(options, mirror.AttachmentReasonBodyAggregateLimit); err != nil {
				return corpusAttachmentCapture{}, err
			}
			capture.records = append(capture.records, record)
			continue
		}
		bodyPath, err := mirror.NewPublicArtifactPath(stem + ".attachments/" + attachment.ID + ".body")
		if err != nil {
			options.budget.release(attachment.FileSize)
			return corpusAttachmentCapture{}, err
		}
		reader, err := open(ctx, attachment)
		if err != nil {
			options.budget.release(attachment.FileSize)
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
				return corpusAttachmentCapture{}, errors.Join(partialErr, err)
			}
			capture.records = append(capture.records, record)
			continue
		}
		data, digest, readErr := streamCorpusAttachment(ctx, root, parentID, attachment.ID, attachment.FileSize, reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			options.budget.release(attachment.FileSize)
			bodyReason := mirror.AttachmentBodyReasonFailed
			partial := mirror.AttachmentReasonBodyFailed
			if errors.Is(readErr, errCorpusAttachmentSizeMismatch) {
				bodyReason = mirror.AttachmentBodyReasonSizeMismatch
				partial = mirror.AttachmentReasonBodySizeMismatch
			}
			record.Body = mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyFailed, Reason: bodyReason}
			if partialErr := capture.notePartial(options, partial); partialErr != nil {
				return corpusAttachmentCapture{}, errors.Join(partialErr, readErr, closeErr)
			}
			capture.records = append(capture.records, record)
			continue
		}
		record.Body = mirror.AttachmentSidecarBody{
			State: mirror.AttachmentBodyCaptured, Path: bodyPath.String(), Size: int64(len(data)), SHA256: digest,
		}
		capture.records = append(capture.records, record)
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

func (capture *corpusAttachmentCapture) notePartial(options *corpusPullEvidenceOptions, reason mirror.AttachmentPartialReason) error {
	if !options.binding.AllowPartialEvidence {
		return fmt.Errorf("%w: requested attachment body evidence is incomplete", domain.ErrCheckFailed)
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
