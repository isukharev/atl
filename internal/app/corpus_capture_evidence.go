package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/mirror"
)

func corpusCaptureDimensionsForSnapshot(snapshot *mirror.CorpusSnapshot, options CorpusBuildOptions) ([]corpus.CaptureDimensionEvidence, error) {
	comments, err := corpusCapturedDimension(snapshot, corpus.CaptureComments)
	if err != nil {
		return nil, err
	}
	attachments, err := corpusCapturedDimension(snapshot, corpus.CaptureAttachments)
	if err != nil {
		return nil, err
	}
	if !options.Comments {
		if comments.present {
			return nil, corpus.ErrIntegrity
		}
		comments.state = corpus.CaptureNotRequested
	} else if snapshot.Len() == 0 {
		comments.state = corpus.CaptureComplete
	} else if !comments.present {
		return nil, corpus.ErrIntegrity
	}
	if !options.Attachments {
		if attachments.present {
			return nil, corpus.ErrIntegrity
		}
		attachments.state = corpus.CaptureNotRequested
	} else if snapshot.Len() == 0 {
		attachments.state = corpus.CaptureComplete
	} else if !attachments.present {
		return nil, corpus.ErrIntegrity
	}
	if !options.AllowPartialEvidence && (comments.state == corpus.CapturePartial || attachments.state == corpus.CapturePartial) {
		return nil, corpus.ErrIntegrity
	}
	return []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureAttachments, State: attachments.state},
		{Dimension: corpus.CaptureComments, State: comments.state},
		{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
	}, nil
}

type corpusCapturedDimensionState struct {
	state   corpus.CaptureDimensionState
	present bool
}

func corpusCapturedDimension(snapshot *mirror.CorpusSnapshot, dimension corpus.CaptureDimension) (corpusCapturedDimensionState, error) {
	if snapshot == nil {
		return corpusCapturedDimensionState{}, corpus.ErrIntegrity
	}
	state := corpusCapturedDimensionState{state: corpus.CaptureComplete}
	missing := false
	for index := 0; index < snapshot.Len(); index++ {
		item, err := snapshot.ReadItem(index)
		if err != nil {
			return corpusCapturedDimensionState{}, err
		}
		suffix := ".comments.json"
		if dimension == corpus.CaptureAttachments {
			suffix = ".attachments.json"
		}
		sidecar, found := corpusAuxiliaryWithSuffix(item.Auxiliaries, suffix)
		if !found {
			if state.present {
				return corpusCapturedDimensionState{}, corpus.ErrIntegrity
			}
			missing = true
			continue
		}
		if missing {
			return corpusCapturedDimensionState{}, corpus.ErrIntegrity
		}
		state.present = true
		complete, err := validateCorpusEvidenceSidecar(snapshot, item, dimension, sidecar)
		if err != nil {
			return corpusCapturedDimensionState{}, err
		}
		if !complete {
			state.state = corpus.CapturePartial
		}
	}
	return state, nil
}

func validateCorpusEvidenceSidecar(snapshot *mirror.CorpusSnapshot, item mirror.CorpusSnapshotItem, dimension corpus.CaptureDimension, sidecar mirror.CorpusSnapshotFile) (bool, error) {
	service := snapshot.Service()
	if dimension == corpus.CaptureComments {
		switch service {
		case mirror.CorpusSnapshotConfluence:
			decoded, err := mirror.DecodeConfluenceCommentsSidecar(sidecar.Data)
			if err != nil || decoded.Format != mirror.ConfluenceCommentsSidecarFormatV2 || decoded.V2 == nil ||
				decoded.V2.PageID != item.ProviderID || decoded.V2.PageVersion != item.Version {
				return false, corpus.ErrIntegrity
			}
			return decoded.V2.Complete, nil
		case mirror.CorpusSnapshotJira:
			decoded, err := mirror.DecodeJiraCommentsSidecarV1(sidecar.Data)
			if err != nil || decoded.Service != service || decoded.OriginSHA256 != snapshot.OriginSHA256() ||
				decoded.ParentID != item.ProviderID || decoded.NativeSHA256 != item.Native.SHA256 ||
				decoded.MetadataSHA256 != item.Metadata.SHA256 || decoded.ParentRevision != corpusJiraSnapshotRevision(item.Metadata.Data) {
				return false, corpus.ErrIntegrity
			}
			return decoded.Complete, nil
		}
	}
	if dimension == corpus.CaptureAttachments {
		decoded, err := mirror.DecodeAttachmentSidecarV1(sidecar.Data)
		if err != nil || decoded.Service != service || decoded.OriginSHA256 != snapshot.OriginSHA256() ||
			decoded.ParentID != item.ProviderID || decoded.NativeSHA256 != item.Native.SHA256 || decoded.MetadataSHA256 != item.Metadata.SHA256 {
			return false, corpus.ErrIntegrity
		}
		switch service {
		case mirror.CorpusSnapshotConfluence:
			if decoded.ParentVersion != item.Version || decoded.ParentRevision != "" {
				return false, corpus.ErrIntegrity
			}
		case mirror.CorpusSnapshotJira:
			if decoded.ParentVersion != 0 || decoded.ParentRevision != corpusJiraSnapshotRevision(item.Metadata.Data) {
				return false, corpus.ErrIntegrity
			}
		default:
			return false, corpus.ErrIntegrity
		}
		return decoded.Complete, nil
	}
	return false, fmt.Errorf("%w: unsupported capture dimension", corpus.ErrIntegrity)
}

func corpusJiraSnapshotRevision(metadata []byte) string {
	var snapshot struct {
		Fields map[string]any `json:"fields"`
	}
	if json.Unmarshal(metadata, &snapshot) != nil {
		return ""
	}
	revision, _ := snapshot.Fields["updated"].(string)
	if strings.TrimSpace(revision) != revision {
		return ""
	}
	return revision
}

func validateCorpusCaptureDimensions(snapshot *mirror.CorpusSnapshot, dimensions []corpus.CaptureDimensionEvidence) error {
	if len(dimensions) != 4 {
		return corpus.ErrIntegrity
	}
	states := make(map[corpus.CaptureDimension]corpus.CaptureDimensionState, len(dimensions))
	for _, dimension := range dimensions {
		states[dimension.Dimension] = dimension.State
	}
	if states[corpus.CaptureNative] != corpus.CaptureComplete || states[corpus.CaptureMetadata] != corpus.CaptureComplete {
		return corpus.ErrIntegrity
	}
	for _, dimension := range []corpus.CaptureDimension{corpus.CaptureComments, corpus.CaptureAttachments} {
		observed, err := corpusCapturedDimension(snapshot, dimension)
		if err != nil {
			return err
		}
		declared := states[dimension]
		switch {
		case snapshot.Len() == 0 && (declared == corpus.CaptureComplete || declared == corpus.CaptureNotRequested):
		case declared == corpus.CaptureNotRequested && !observed.present:
		case declared == observed.state && observed.present:
		default:
			return corpus.ErrIntegrity
		}
	}
	return nil
}
