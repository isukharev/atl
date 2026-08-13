package app

import (
	"context"

	"github.com/isukharev/atl/internal/corpus"
)

func selectCorpusBuildCompletedGeneration(ctx context.Context, workspace *corpus.BuildWorkspace, cacheStore *corpus.Store, active corpus.BuildActive, options CorpusBuildOptions) (*corpus.Generation, bool, error) {
	switch active.SchemaVersion {
	case corpus.BuildActiveSchemaV1, corpus.BuildActiveSchemaV2:
		selected, err := workspace.SelectCurrent(ctx)
		return selected, true, err
	case corpus.BuildActiveSchemaV3:
		switch active.PublicationTarget {
		case corpus.PublicationTargetWorkspace:
			selected, err := workspace.SelectCurrent(ctx)
			return selected, true, err
		case corpus.PublicationTargetCache:
			if cacheStore == nil || !corpusCacheEnabled(options) {
				return nil, false, nil
			}
			digest, err := corpus.RootIdentityDigest(options.CacheRoot)
			if err != nil {
				return nil, false, err
			}
			if digest != active.PublicationRootDigest {
				return nil, false, nil
			}
			selected, err := cacheStore.SelectCurrent(ctx)
			return selected, true, err
		default:
			return nil, false, corpus.ErrIntegrity
		}
	default:
		return nil, false, corpus.ErrIntegrity
	}
}

func validateCorpusBuildActiveBinding(active corpus.BuildActive, options CorpusBuildOptions) error {
	started, deadline, err := corpusBuildTimes(active)
	if err != nil || deadline.Sub(started) != options.Deadline ||
		active.MaxAttempts != options.MaxRequests || active.MaxResponseBytes != options.MaxResponseBytes ||
		active.AttachmentBodyBytes < 0 ||
		options.AttachmentBodies && active.AttachmentBodyBytes > options.MaxTotalAttachmentBytes ||
		!options.AttachmentBodies && active.AttachmentBodyBytes != 0 {
		return corpus.ErrIntegrity
	}
	if corpusCacheEnabled(options) {
		rootDigest, digestErr := corpus.RootIdentityDigest(options.CacheRoot)
		if digestErr != nil || active.SchemaVersion != corpus.BuildActiveSchemaV3 ||
			active.PublicationTarget != corpus.PublicationTargetCache || active.PublicationRootDigest != rootDigest {
			return corpus.ErrIntegrity
		}
	} else if active.SchemaVersion == corpus.BuildActiveSchemaV3 &&
		(active.PublicationTarget != corpus.PublicationTargetWorkspace || active.PublicationRootDigest != "") {
		return corpus.ErrIntegrity
	}
	_, expected, err := corpusBuildServices(options)
	if err != nil || len(active.Services) != len(expected) {
		return corpus.ErrIntegrity
	}
	for index := range expected {
		if active.Services[index].Service != expected[index].Service ||
			active.Services[index].SelectorDigest != expected[index].SelectorDigest {
			return corpus.ErrIntegrity
		}
	}
	return nil
}
