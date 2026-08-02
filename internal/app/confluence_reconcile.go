package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type ConfluenceReconcileResult struct {
	SchemaVersion  int                           `json:"schema_version"`
	Service        string                        `json:"service"`
	Mode           string                        `json:"mode"`
	Complete       bool                          `json:"complete"`
	Reconciled     bool                          `json:"reconciled"`
	ID             string                        `json:"id"`
	Path           string                        `json:"path"`
	BaseVersion    int                           `json:"base_version"`
	RemoteVersion  int                           `json:"remote_version"`
	ProposalHash   string                        `json:"proposal_hash"`
	Base           NativeReconcileSide           `json:"base"`
	Ours           NativeReconcileSide           `json:"ours"`
	Theirs         NativeReconcileSide           `json:"theirs"`
	Classification NativeReconcileClassification `json:"classification"`
	BlockSummary   NativeReconcileBlockSummary   `json:"block_summary"`
	Blocks         []NativeReconcileBlock        `json:"blocks"`
	LocalChanges   []ConfluenceBlockChange       `json:"local_changes,omitempty"`
	RemoteChanges  []ConfluenceBlockChange       `json:"remote_changes,omitempty"`
	Bounds         NativeReconcileBounds         `json:"bounds"`
	Artifacts      *NativeReconcileArtifacts     `json:"artifacts,omitempty"`
}

func (s *ConfluenceService) PreviewConfluenceReconcile(ctx context.Context, target, into string) (*ConfluenceReconcileResult, error) {
	return s.reconcileConfluence(ctx, target, into, false)
}

func (s *ConfluenceService) StageConfluenceReconcile(ctx context.Context, target, into string) (*ConfluenceReconcileResult, error) {
	return s.reconcileConfluence(ctx, target, into, true)
}

func (s *ConfluenceService) reconcileConfluence(ctx context.Context, target, into string, stage bool) (*ConfluenceReconcileResult, error) {
	root, target, err := canonicalConfluenceDiffPaths(target, into)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(target, ".md") {
		target = strings.TrimSuffix(target, ".md") + ".csf"
	}
	if !strings.HasSuffix(target, ".csf") {
		return nil, fmt.Errorf("%w: conf reconcile requires one .csf or neighboring .md file", domain.ErrUsage)
	}

	var snapshot *mirrorSnapshotLock
	var exclusive interface{ Unlock() error }
	if stage {
		lock, lockErr := lockConfluenceMutations(root, false)
		if lockErr != nil {
			return nil, lockErr
		}
		exclusive = lock
		defer func() { _ = exclusive.Unlock() }()
	} else {
		snapshot, err = beginMirrorSnapshotLock(root, filepath.Join(root, ".atl", confluenceMutationLockName))
		if err != nil {
			return nil, err
		}
	}
	snapshotFinished := false
	defer func() {
		if snapshot != nil && !snapshotFinished {
			_, _ = snapshot.finish()
		}
	}()

	m := mirror.New(root)
	lc, ours, err := m.LoadCSFWithinLimit(target, nativeReconcileMaxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: load reconcile page: %v", domain.ErrCheckFailed, err)
	}
	if lc.TrackedElsewhere || lc.Synced == nil || lc.Meta.ID != lc.Synced.ID {
		return nil, fmt.Errorf("%w: reconcile requires the canonical tracked page path", domain.ErrCheckFailed)
	}
	if lc.Synced.Version <= 0 || lc.Meta.Version != lc.Synced.Version || lc.Meta.Hash != lc.Synced.Hash {
		return nil, fmt.Errorf("%w: reconcile page metadata/version does not match the synced state", domain.ErrCheckFailed)
	}
	base, present, err := m.ReadBaseBodyWithinLimit(lc.Meta.ID, nativeReconcileMaxBodyBytes)
	if err != nil || !present {
		return nil, fmt.Errorf("%w: reconcile requires a readable pristine page baseline", domain.ErrCheckFailed)
	}
	if mirror.Hash(base) != lc.Synced.Hash {
		return nil, fmt.Errorf("%w: reconcile pristine page baseline does not match the synced state", domain.ErrCheckFailed)
	}
	if err := checkNativeReconcileBodies(base, ours, nil); err != nil {
		return nil, err
	}
	baseRoot, baseErr := csf.Parse(base)
	oursRoot, oursErr := csf.Parse(ours)
	if baseRoot == nil || oursRoot == nil || baseErr != nil || oursErr != nil || csf.HasErrors(csf.Validate(base)) || csf.HasErrors(csf.Validate(ours)) {
		return nil, fmt.Errorf("%w: reconcile requires valid base and local CSF", domain.ErrCheckFailed)
	}
	baseBlocks, oursBlocks := semanticBlocks(baseRoot), semanticBlocks(oursRoot)
	if len(baseBlocks) > nativeReconcileMaxBlocks || len(oursBlocks) > nativeReconcileMaxBlocks {
		return nil, fmt.Errorf("%w: reconcile page exceeds the %d-block safety bound", domain.ErrCheckFailed, nativeReconcileMaxBlocks)
	}
	if err := checkNativeReconcileLocalAlignment(uint64(len(baseBlocks)), uint64(len(oursBlocks))); err != nil {
		return nil, err
	}

	budget, err := domain.NewReadBudget(1, nativeReconcileResponseBytes)
	if err != nil {
		return nil, err
	}
	probeCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
	remote, err := s.store.GetPage(probeCtx, lc.Meta.ID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if remote == nil || remote.ID != lc.Meta.ID || remote.Type != "page" || remote.Version <= 0 || !remote.BodyPresent {
		return nil, fmt.Errorf("%w: remote page response is missing exact identity, type, version, or body", domain.ErrCheckFailed)
	}
	if remote.Version < lc.Synced.Version || (remote.Version == lc.Synced.Version && mirror.Hash(remote.Body) != lc.Synced.Hash) {
		return nil, fmt.Errorf("%w: remote page evidence is inconsistent with the synced version", domain.ErrCheckFailed)
	}
	if err := checkNativeReconcileBodies(base, ours, remote.Body); err != nil {
		return nil, err
	}
	theirsRoot, theirsErr := csf.Parse(remote.Body)
	if theirsRoot == nil || theirsErr != nil || csf.HasErrors(csf.Validate(remote.Body)) {
		return nil, fmt.Errorf("%w: remote page body is invalid CSF", domain.ErrCheckFailed)
	}
	theirsBlocks := semanticBlocks(theirsRoot)
	if len(theirsBlocks) > nativeReconcileMaxBlocks {
		return nil, fmt.Errorf("%w: reconcile page exceeds the %d-block safety bound", domain.ErrCheckFailed, nativeReconcileMaxBlocks)
	}
	blocks, blockSummary, err := classifyNativeReconcileBlocks(confluenceNativeBlocks(baseBlocks), confluenceNativeBlocks(oursBlocks), confluenceNativeBlocks(theirsBlocks))
	if err != nil {
		return nil, err
	}

	classification := classifyNativeReconcile(base, ours, remote.Body)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return nil, err
	}
	result := &ConfluenceReconcileResult{
		SchemaVersion: nativeReconcileSchemaVersion, Service: "confluence", Mode: map[bool]string{false: "preview", true: "stage"}[stage],
		Complete: true, Reconciled: !classification.Conflict, ID: lc.Meta.ID, Path: filepath.ToSlash(rel),
		BaseVersion: lc.Synced.Version, RemoteVersion: remote.Version,
		Base: nativeReconcileSide(base, true), Ours: nativeReconcileSide(ours, true), Theirs: nativeReconcileSide(remote.Body, true),
		Classification: classification, BlockSummary: blockSummary, Blocks: blocks,
		LocalChanges: confluenceBlockChanges(baseRoot, oursRoot), RemoteChanges: confluenceBlockChanges(baseRoot, theirsRoot),
		Bounds: NativeReconcileBounds{MaxBodyBytes: nativeReconcileMaxBodyBytes, MaxBlocks: nativeReconcileMaxBlocks, MaxAlignmentCells: 1_000_000},
	}
	result.ProposalHash, err = nativeReconcileProposal(struct {
		Service, ID, Path          string
		BaseVersion, RemoteVersion int
		Base, Ours, Theirs         string
	}{result.Service, result.ID, result.Path, result.BaseVersion, result.RemoteVersion, result.Base.SHA256, result.Ours.SHA256, result.Theirs.SHA256})
	if err != nil {
		return nil, err
	}
	if err := revalidateConfluenceReconcile(m, target, lc.Meta.ID, result.BaseVersion, result.Ours.SHA256, result.Base.SHA256); err != nil {
		return nil, err
	}
	if snapshot != nil {
		retry, finishErr := snapshot.finish()
		snapshotFinished = true
		if finishErr != nil {
			return nil, finishErr
		}
		if retry {
			return nil, fmt.Errorf("%w: mirror changed during reconcile preview", domain.ErrCheckFailed)
		}
	}
	if stage {
		basePath, theirsPath, stageErr := m.StageReconcileArtifacts("confluence", target, base, remote.Body)
		if stageErr != nil {
			return nil, stageErr
		}
		result.Artifacts = &NativeReconcileArtifacts{BasePath: basePath, TheirsPath: theirsPath, Cleanup: nativeReconcileCleanup}
		if err := revalidateConfluenceReconcile(m, target, lc.Meta.ID, result.BaseVersion, result.Ours.SHA256, result.Base.SHA256); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func confluenceNativeBlocks(blocks []semanticBlock) []nativeSemanticBlock {
	out := make([]nativeSemanticBlock, len(blocks))
	for i, block := range blocks {
		out[i] = nativeSemanticBlock(block)
	}
	return out
}

func revalidateConfluenceReconcile(m *mirror.Mirror, target, id string, baseVersion int, oursHash, baseHash string) error {
	lc, ours, err := m.LoadCSFWithinLimit(target, nativeReconcileMaxBodyBytes)
	if err != nil || lc.TrackedElsewhere || lc.Synced == nil || lc.Meta.ID != id || lc.Synced.ID != id ||
		lc.Synced.Version != baseVersion || lc.Meta.Version != baseVersion || lc.Meta.Hash != baseHash || lc.Synced.Hash != baseHash || mirror.Hash(ours) != oursHash {
		return fmt.Errorf("%w: local page changed during reconcile", domain.ErrCheckFailed)
	}
	base, present, err := m.ReadBaseBodyWithinLimit(id, nativeReconcileMaxBodyBytes)
	if err != nil || !present || mirror.Hash(base) != baseHash {
		return fmt.Errorf("%w: pristine page baseline changed during reconcile", domain.ErrCheckFailed)
	}
	return nil
}

func ConfluenceReconcileMarkdown(result *ConfluenceReconcileResult) string {
	return strings.TrimRight(MarkdownTable([]string{"Mode", "State", "Reconciled", "Base", "Remote", "Proposal"}, [][]string{{result.Mode, result.Classification.State, fmt.Sprint(result.Reconciled), fmt.Sprint(result.BaseVersion), fmt.Sprint(result.RemoteVersion), result.ProposalHash}}), "\n")
}
