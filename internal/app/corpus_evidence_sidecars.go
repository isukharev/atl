package app

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func corpusMirrorOrigin(m *mirror.Mirror, service string) (string, error) {
	binding, found, err := m.BackendBinding(service)
	if err != nil {
		return "", err
	}
	if !found || binding.OriginSHA256 == "" {
		return "", fmt.Errorf("%w: evidence mirror has no backend binding", domain.ErrCheckFailed)
	}
	return binding.OriginSHA256, nil
}

func finalizeCorpusAttachmentCapture(
	m *mirror.Mirror,
	service string,
	stem string,
	parentID string,
	parentVersion int,
	parentRevision string,
	nativeSHA256 string,
	metadataSHA256 string,
	capture corpusAttachmentCapture,
) ([]mirror.CompletePullArtifact, error) {
	origin, err := corpusMirrorOrigin(m, service)
	if err != nil {
		return nil, err
	}
	complete := capture.inventoryComplete && capture.bodiesState != mirror.AttachmentBodiesPartial
	sidecar := mirror.AttachmentSidecarV1{
		SchemaVersion: mirror.AttachmentSidecarSchemaV1,
		Service:       service, OriginSHA256: origin, ParentID: parentID,
		ParentVersion: parentVersion, ParentRevision: parentRevision,
		NativeSHA256: nativeSHA256, MetadataSHA256: metadataSHA256,
		InventoryComplete: capture.inventoryComplete, InventoryPartialReason: capture.inventoryReason,
		BodiesState: capture.bodiesState, Complete: complete, Count: len(capture.records),
		PartialReasons: append([]mirror.AttachmentPartialReason{}, capture.partialReasons...),
		Attachments:    append([]mirror.AttachmentSidecarRecord{}, capture.records...),
	}
	encoded, err := mirror.EncodeAttachmentSidecarV1(sidecar)
	if err != nil {
		return nil, err
	}
	if err := mirror.ValidateAttachmentSidecarPublicationData(encoded, 0); err != nil {
		return nil, err
	}
	sidecarPath, err := mirror.NewPublicArtifactPath(stem + ".attachments.json")
	if err != nil {
		return nil, err
	}
	role := mirror.CompletePullArtifactRole("")
	if service == mirror.CorpusSnapshotJira {
		role = mirror.CompletePullArtifactRoleAuxiliary
	}
	artifacts := []mirror.CompletePullArtifact{{Path: sidecarPath, Role: role, Data: encoded, Mode: 0o600}}
	wantPrefix := stem + ".attachments/"
	for _, payload := range capture.payloads {
		if !strings.HasPrefix(payload.path.String(), wantPrefix) {
			return nil, fmt.Errorf("%w: attachment payload is outside its parent", domain.ErrCheckFailed)
		}
		artifacts = append(artifacts, mirror.CompletePullArtifact{
			Path: payload.path, Role: role, Data: append([]byte(nil), payload.data...), Mode: 0o600,
		})
	}
	return artifacts, nil
}

func completePullArtifactData(artifacts []mirror.CompletePullArtifact, path string) ([]byte, error) {
	for _, artifact := range artifacts {
		if artifact.Path.String() == path && !artifact.Remove {
			return artifact.Data, nil
		}
	}
	return nil, fmt.Errorf("%w: complete-pull artifact is missing", domain.ErrCheckFailed)
}
