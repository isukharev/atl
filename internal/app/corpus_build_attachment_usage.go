package app

import (
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/mirror"
)

// reconcileCorpusBuildAttachmentUsage proves that every body in a validated
// attempt snapshot was charged to durable usage. allowAdvance is used only
// after explicit recovery, where a previously in-flight publication may prove
// successful body streams that did not reach the active-record barrier.
// Aggregate, carry, and per-service usage never shrink.
func reconcileCorpusBuildAttachmentUsage(
	workspace *corpus.BuildWorkspace,
	active *corpus.BuildActive,
	allowAdvance bool,
) (bool, error) {
	if workspace == nil || active == nil {
		return false, corpus.ErrIntegrity
	}
	legacy := active.SchemaVersion == corpus.BuildActiveSchemaV1
	if !legacy && active.SchemaVersion != corpus.BuildActiveSchemaV2 && active.SchemaVersion != corpus.BuildActiveSchemaV3 {
		return false, corpus.ErrIntegrity
	}
	carry, err := corpusBuildAttachmentCarry(*active)
	if err != nil {
		return false, err
	}
	changed := legacy
	var serviceTotal int64
	for index := range active.Services {
		root, err := workspace.AttemptRoot(active.AttemptID, active.Services[index].Service)
		if err != nil {
			return false, err
		}
		observed, err := corpusServiceAttachmentBodyBytes(root, active.Services[index].Service)
		if err != nil {
			return false, err
		}
		stored := active.Services[index].AttachmentBodyBytes
		if legacy {
			changed = true
			active.Services[index].AttachmentBodyBytes = observed
		} else if observed > stored {
			if !allowAdvance {
				return false, corpus.ErrIntegrity
			}
			changed = true
			active.Services[index].AttachmentBodyBytes = observed
		}
		serviceUsage := active.Services[index].AttachmentBodyBytes
		if serviceUsage > corpusBuildMaxAttachmentTotalBytes-serviceTotal {
			return false, corpus.ErrIntegrity
		}
		serviceTotal += serviceUsage
	}
	if carry > corpusBuildMaxAttachmentTotalBytes-serviceTotal {
		return false, corpus.ErrIntegrity
	}
	total := carry + serviceTotal
	if total != active.AttachmentBodyBytes {
		changed = true
		active.AttachmentBodyBytes = total
	}
	if legacy {
		active.SchemaVersion = corpus.BuildActiveSchemaV2
	}
	return changed, nil
}

// reconcileCorpusBuildServiceAttachmentUsage persists the successful-stream
// high-water observed by the shared in-memory budget at every remote return,
// including errors. Snapshot bytes are a lower bound on that usage, not the
// owner of the counter: a successfully streamed body may fail before its
// sidecar/publication becomes visible and must still remain charged.
func reconcileCorpusBuildServiceAttachmentUsage(root string, active *corpus.BuildActive, index int, attachmentBodyBytes int64) error {
	if active == nil || active.SchemaVersion != corpus.BuildActiveSchemaV2 && active.SchemaVersion != corpus.BuildActiveSchemaV3 || index < 0 || index >= len(active.Services) {
		return corpus.ErrIntegrity
	}
	if attachmentBodyBytes < active.AttachmentBodyBytes || attachmentBodyBytes > corpusBuildMaxAttachmentTotalBytes {
		return corpus.ErrIntegrity
	}
	delta := attachmentBodyBytes - active.AttachmentBodyBytes
	if delta > corpusBuildMaxAttachmentTotalBytes-active.Services[index].AttachmentBodyBytes {
		return corpus.ErrIntegrity
	}
	active.Services[index].AttachmentBodyBytes += delta
	active.AttachmentBodyBytes = attachmentBodyBytes
	observed, err := corpusServiceAttachmentBodyBytes(root, active.Services[index].Service)
	if err != nil {
		return err
	}
	if observed > active.Services[index].AttachmentBodyBytes {
		return corpus.ErrIntegrity
	}
	return nil
}

func corpusBuildAttachmentCarry(active corpus.BuildActive) (int64, error) {
	if active.SchemaVersion == corpus.BuildActiveSchemaV1 {
		return 0, nil
	}
	var serviceTotal int64
	for _, state := range active.Services {
		if state.AttachmentBodyBytes < 0 || state.AttachmentBodyBytes > active.AttachmentBodyBytes-serviceTotal {
			return 0, corpus.ErrIntegrity
		}
		serviceTotal += state.AttachmentBodyBytes
	}
	return active.AttachmentBodyBytes - serviceTotal, nil
}

func corpusServiceAttachmentBodyBytes(root string, service corpus.Service) (int64, error) {
	m := mirror.New(root)
	_, configured, err := m.BackendBinding(string(service))
	if err != nil {
		return 0, err
	}
	if !configured {
		return 0, nil
	}
	snapshot, err := m.BeginCorpusSnapshot(string(service), mirror.CorpusSnapshotOptions{
		Limits: mirror.DefaultCorpusSnapshotLimits(),
	})
	if err != nil {
		return 0, err
	}
	var total int64
	for index := 0; index < snapshot.Len(); index++ {
		item, err := snapshot.ReadItem(index)
		if err != nil {
			return 0, err
		}
		sidecar, found := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".attachments.json")
		if !found {
			continue
		}
		if _, err := validateCorpusEvidenceSidecar(snapshot, item, corpus.CaptureAttachments, sidecar); err != nil {
			return 0, err
		}
		decoded, err := mirror.DecodeAttachmentSidecarV1(sidecar.Data)
		if err != nil {
			return 0, err
		}
		for _, attachment := range decoded.Attachments {
			if attachment.Body.State != mirror.AttachmentBodyCaptured {
				continue
			}
			if attachment.Body.Size < 0 || attachment.Body.Size > corpusBuildMaxAttachmentTotalBytes-total {
				return 0, corpus.ErrIntegrity
			}
			total += attachment.Body.Size
		}
	}
	return total, nil
}
