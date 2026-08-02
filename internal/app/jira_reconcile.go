package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/wikimerge"
)

type JiraReconcileField struct {
	ID             string                        `json:"id"`
	Base           NativeReconcileSide           `json:"base"`
	Ours           NativeReconcileSide           `json:"ours"`
	Theirs         NativeReconcileSide           `json:"theirs"`
	Classification NativeReconcileClassification `json:"classification"`
}

type JiraReconcileResult struct {
	SchemaVersion  int                           `json:"schema_version"`
	Service        string                        `json:"service"`
	Mode           string                        `json:"mode"`
	Complete       bool                          `json:"complete"`
	Reconciled     bool                          `json:"reconciled"`
	ID             string                        `json:"id"`
	Key            string                        `json:"key"`
	Updated        string                        `json:"updated"`
	Path           string                        `json:"path"`
	ProposalHash   string                        `json:"proposal_hash"`
	Base           NativeReconcileSide           `json:"base"`
	Ours           NativeReconcileSide           `json:"ours"`
	Theirs         NativeReconcileSide           `json:"theirs"`
	Classification NativeReconcileClassification `json:"classification"`
	BlockSummary   NativeReconcileBlockSummary   `json:"block_summary"`
	Blocks         []NativeReconcileBlock        `json:"blocks"`
	Fields         []JiraReconcileField          `json:"fields,omitempty"`
	Bounds         NativeReconcileBounds         `json:"bounds"`
	Artifacts      *NativeReconcileArtifacts     `json:"artifacts,omitempty"`
}

func (s *JiraService) PreviewJiraReconcile(ctx context.Context, target, into string) (*JiraReconcileResult, error) {
	return s.reconcileJira(ctx, target, into, false)
}

func (s *JiraService) StageJiraReconcile(ctx context.Context, target, into string) (*JiraReconcileResult, error) {
	return s.reconcileJira(ctx, target, into, true)
}

func (s *JiraService) reconcileJira(ctx context.Context, target, into string, stage bool) (*JiraReconcileResult, error) {
	root, target, err := canonicalJiraReconcilePaths(target, into)
	if err != nil {
		return nil, err
	}
	var snapshot *mirrorSnapshotLock
	var exclusive interface{ Unlock() error }
	if stage {
		lock, lockErr := lockJiraPendingFields(root, strings.TrimSuffix(filepath.Base(target), wikiExt))
		if lockErr != nil {
			return nil, lockErr
		}
		exclusive = lock
		defer func() { _ = exclusive.Unlock() }()
	} else {
		snapshot, err = beginMirrorSnapshotLock(root, jiraPendingFieldsLockPath(root))
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
	lw, ours, err := m.LoadWikiWithinLimit(target, nativeReconcileMaxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: load reconcile issue: %v", domain.ErrCheckFailed, err)
	}
	if lw.TrackedElsewhere || lw.Synced == nil || lw.Key != lw.Synced.ID {
		return nil, fmt.Errorf("%w: reconcile requires the canonical tracked issue path", domain.ErrCheckFailed)
	}
	base, present, err := m.ReadBaseBodyExtWithinLimit(lw.Key, wikiExt, nativeReconcileMaxBodyBytes)
	if err != nil || !present {
		return nil, fmt.Errorf("%w: reconcile requires a readable pristine issue baseline", domain.ErrCheckFailed)
	}
	if mirror.Hash(base) != lw.Synced.Hash {
		return nil, fmt.Errorf("%w: reconcile pristine issue baseline does not match the synced state", domain.ErrCheckFailed)
	}
	if err := checkNativeReconcileBodies(base, ours, nil); err != nil {
		return nil, err
	}
	pending, _, err := loadJiraPendingFieldsReadOnlyWithinLimit(root, lw.Key, nativeReconcileMaxPendingBytes)
	if err != nil {
		return nil, err
	}
	if err := validatePendingMirrorBinding(root, pending, lw, ours); err != nil {
		return nil, err
	}
	baseBlocks, err := wikimerge.SemanticBlocks(base, nativeReconcileMaxBlocks)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect base Jira wiki blocks: %v", domain.ErrCheckFailed, err)
	}
	oursBlocks, err := wikimerge.SemanticBlocks(ours, nativeReconcileMaxBlocks)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect local Jira wiki blocks: %v", domain.ErrCheckFailed, err)
	}
	if err := checkNativeReconcileLocalAlignment(uint64(len(baseBlocks)), uint64(len(oursBlocks))); err != nil {
		return nil, err
	}
	if pending != nil {
		if len(pending.Fields) > nativeReconcileMaxPendingFields {
			return nil, fmt.Errorf("%w: reconcile pending field count exceeds the %d-field safety bound", domain.ErrCheckFailed, nativeReconcileMaxPendingFields)
		}
		for _, field := range pending.Fields {
			if err := checkNativeReconcileBodies([]byte(field.Base), []byte(field.Value), nil); err != nil {
				return nil, err
			}
		}
	}

	fields := []string{"description", "updated"}
	fields = append(fields, jiraPendingFieldIDs(pending)...)
	budget, err := domain.NewReadBudget(1, nativeReconcileResponseBytes)
	if err != nil {
		return nil, err
	}
	probeCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
	remote, err := s.tr.GetIssue(probeCtx, lw.Key, fields)
	if err != nil {
		return nil, err
	}
	if remote == nil || remote.Key != lw.Key || !canonicalJiraTransitionIdentity(remote.ID) {
		return nil, fmt.Errorf("%w: remote issue response is missing exact identity", domain.ErrCheckFailed)
	}
	if _, present := remote.Fields["description"]; !present {
		return nil, fmt.Errorf("%w: remote issue response omitted description", domain.ErrCheckFailed)
	}
	remoteDescription, valid := jiraSnapshotStringField(remote.Fields, "description")
	if !valid {
		return nil, fmt.Errorf("%w: remote issue Description is not native wiki text", domain.ErrCheckFailed)
	}
	updatedValue, present := remote.Fields["updated"]
	updated, ok := updatedValue.(string)
	if !present || !ok || !canonicalJiraTransitionIdentity(updated) {
		return nil, fmt.Errorf("%w: remote issue response omitted a canonical updated marker", domain.ErrCheckFailed)
	}
	if _, err := parseJiraUpdatedTime(updated); err != nil {
		return nil, fmt.Errorf("%w: remote issue returned an unsupported updated datetime", domain.ErrCheckFailed)
	}
	if err := checkNativeReconcileBodies(base, ours, []byte(remoteDescription)); err != nil {
		return nil, err
	}

	classification := classifyNativeReconcile(base, ours, []byte(remoteDescription))
	theirsBlocks, err := wikimerge.SemanticBlocks([]byte(remoteDescription), nativeReconcileMaxBlocks)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect remote Jira wiki blocks: %v", domain.ErrCheckFailed, err)
	}
	blocks, blockSummary, err := classifyNativeReconcileBlocks(jiraNativeBlocks(baseBlocks), jiraNativeBlocks(oursBlocks), jiraNativeBlocks(theirsBlocks))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return nil, err
	}
	result := &JiraReconcileResult{
		SchemaVersion: nativeReconcileSchemaVersion, Service: "jira", Mode: map[bool]string{false: "preview", true: "stage"}[stage],
		Complete: true, Reconciled: !classification.Conflict, ID: remote.ID, Key: lw.Key, Updated: updated, Path: filepath.ToSlash(rel),
		Base: nativeReconcileSide(base, true), Ours: nativeReconcileSide(ours, true), Theirs: nativeReconcileSide([]byte(remoteDescription), true),
		Classification: classification, BlockSummary: blockSummary, Blocks: blocks,
		Bounds: NativeReconcileBounds{MaxBodyBytes: nativeReconcileMaxBodyBytes, MaxBlocks: nativeReconcileMaxBlocks, MaxAlignmentCells: nativeReconcileMaxAlignmentCells, MaxPendingRecordBytes: nativeReconcileMaxPendingBytes, MaxPendingFields: nativeReconcileMaxPendingFields},
	}
	if pending != nil {
		for _, field := range pending.Fields {
			theirs, valid := jiraSnapshotStringField(remote.Fields, field.ID)
			if !valid {
				return nil, fmt.Errorf("%w: remote Jira field %s is not native wiki text", domain.ErrCheckFailed, field.ID)
			}
			if err := checkNativeReconcileBodies([]byte(field.Base), []byte(field.Value), []byte(theirs)); err != nil {
				return nil, err
			}
			fieldClassification := classifyNativeReconcile([]byte(field.Base), []byte(field.Value), []byte(theirs))
			result.Fields = append(result.Fields, JiraReconcileField{
				ID: field.ID, Base: nativeReconcileSide([]byte(field.Base), true), Ours: nativeReconcileSide([]byte(field.Value), true), Theirs: nativeReconcileSide([]byte(theirs), true), Classification: fieldClassification,
			})
			if fieldClassification.Conflict {
				result.Reconciled = false
			}
		}
		sort.Slice(result.Fields, func(i, j int) bool { return result.Fields[i].ID < result.Fields[j].ID })
	}
	result.ProposalHash, err = nativeReconcileProposal(struct {
		Service, ID, Key, Updated, Path string
		Base, Ours, Theirs              string
		Fields                          []JiraReconcileField
	}{result.Service, result.ID, result.Key, result.Updated, result.Path, result.Base.SHA256, result.Ours.SHA256, result.Theirs.SHA256, result.Fields})
	if err != nil {
		return nil, err
	}
	if err := revalidateJiraReconcile(m, target, lw.Key, result.Ours.SHA256, result.Base.SHA256, pending); err != nil {
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
		basePath, theirsPath, stageErr := m.StageReconcileArtifacts("jira", target, base, []byte(remoteDescription))
		if stageErr != nil {
			return nil, stageErr
		}
		result.Artifacts = &NativeReconcileArtifacts{BasePath: basePath, TheirsPath: theirsPath, Cleanup: nativeReconcileCleanup}
		if err := revalidateJiraReconcile(m, target, lw.Key, result.Ours.SHA256, result.Base.SHA256, pending); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func jiraNativeBlocks(blocks []wikimerge.SemanticBlock) []nativeSemanticBlock {
	out := make([]nativeSemanticBlock, len(blocks))
	for i, block := range blocks {
		out[i] = nativeSemanticBlock{kind: block.Kind, hash: block.SHA256}
	}
	return out
}

func canonicalJiraReconcilePaths(target, into string) (root, canonicalTarget string, err error) {
	if target == "" {
		return "", "", fmt.Errorf("%w: jira reconcile requires one .wiki or neighboring .md file", domain.ErrUsage)
	}
	if strings.HasSuffix(target, ".md") {
		target = strings.TrimSuffix(target, ".md") + wikiExt
	}
	if !strings.HasSuffix(target, wikiExt) {
		return "", "", fmt.Errorf("%w: jira reconcile requires one .wiki or neighboring .md file", domain.ErrUsage)
	}
	root = into
	if root == "" {
		root = mirrorRootOf(target)
	}
	root, err = evalSymlinksAbsolute(root)
	if err != nil {
		return "", "", fmt.Errorf("%w: no Jira mirror found at %q", domain.ErrNotFound, root)
	}
	canonicalTarget, err = evalSymlinksAllowMissing(target)
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve Jira reconcile target: %v", domain.ErrCheckFailed, err)
	}
	if !within(root, canonicalTarget) {
		return "", "", fmt.Errorf("%w: Jira reconcile target is outside mirror root", domain.ErrUsage)
	}
	return root, canonicalTarget, nil
}

func revalidateJiraReconcile(m *mirror.Mirror, target, key, oursHash, baseHash string, pending *JiraPendingFields) error {
	lw, ours, err := m.LoadWikiWithinLimit(target, nativeReconcileMaxBodyBytes)
	if err != nil || lw.TrackedElsewhere || lw.Synced == nil || lw.Key != key || lw.Synced.ID != key || lw.Synced.Hash != baseHash || mirror.Hash(ours) != oursHash {
		return fmt.Errorf("%w: local issue changed during reconcile", domain.ErrCheckFailed)
	}
	base, present, err := m.ReadBaseBodyExtWithinLimit(key, wikiExt, nativeReconcileMaxBodyBytes)
	if err != nil || !present || mirror.Hash(base) != baseHash {
		return fmt.Errorf("%w: pristine issue baseline changed during reconcile", domain.ErrCheckFailed)
	}
	currentPending, currentPresent, err := loadJiraPendingFieldsReadOnlyWithinLimit(m.Root, key, nativeReconcileMaxPendingBytes)
	if err != nil {
		return err
	}
	wantPresent := pending != nil
	if currentPresent != wantPresent {
		return fmt.Errorf("%w: pending Jira fields changed during reconcile", domain.ErrCheckFailed)
	}
	if currentPresent {
		wantHash, _ := nativeReconcileProposal(pending)
		gotHash, _ := nativeReconcileProposal(currentPending)
		if wantHash != gotHash {
			return fmt.Errorf("%w: pending Jira fields changed during reconcile", domain.ErrCheckFailed)
		}
	}
	return nil
}

func JiraReconcileMarkdown(result *JiraReconcileResult) string {
	return strings.TrimRight(MarkdownTable([]string{"Mode", "State", "Reconciled", "Fields", "Proposal"}, [][]string{{result.Mode, result.Classification.State, fmt.Sprint(result.Reconciled), fmt.Sprint(len(result.Fields)), result.ProposalHash}}), "\n")
}
