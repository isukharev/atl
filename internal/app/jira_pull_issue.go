package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

type jiraPullIssueRequest struct {
	root                string
	issue               *domain.Issue
	opts                JiraPullOpts
	render              RenderSettings
	mirror              *mirror.Mirror
	batch               *mirror.SyncBatch
	knownWiki           *mirror.SyncState
	recordedView        *mirror.ViewState
	related             *JiraEpicChildrenSidecar
	epicChildrenEnabled bool
}

type jiraPullIssuePaths struct {
	dir             string
	keySeg          string
	markdown        string
	wiki            string
	snapshot        string
	epicChildren    string
	markdownRel     mirror.ArtifactPath
	wikiRel         mirror.ArtifactPath
	snapshotRel     mirror.ArtifactPath
	epicChildrenRel mirror.ArtifactPath
}

type jiraPullQualifiedIssue struct {
	request           jiraPullIssueRequest
	paths             jiraPullIssuePaths
	identity          string
	pending           *JiraPendingFields
	bodyForView       string
	preserveLocalWiki bool
	rebindPendingWiki bool
	localWiki         []byte
	qualifiedView     []byte
	nativeAction      *PullLocalAction
	nativeExisted     bool
	viewExisted       bool
}

type jiraPullRevalidatedIssue struct {
	qualified *jiraPullQualifiedIssue
}

type jiraPullStagedIssue struct {
	revalidated *jiraPullRevalidatedIssue
	actions     []PullLocalAction
}

type jiraPullFetchedIssue struct {
	staged        *jiraPullStagedIssue
	assets        []JiraIssueAsset
	assetsSkipped int
}

type jiraPullPublishCandidate struct {
	fetched *jiraPullFetchedIssue
}

type jiraPullIssueOutcome struct {
	issue         JiraPulled
	actions       []PullLocalAction
	assetsSkipped int
	state         *mirror.SyncState
	view          *mirror.ViewState
}

func (s *JiraService) qualifyJiraPullView(root, dir, keySeg, mdPath string, pending *JiraPendingFields, localWiki []byte, rs RenderSettings, recordedView *mirror.ViewState) (*PullLocalAction, []byte, error) {
	actual, readErr := safepath.ReadFileWithin(root, mdPath)
	if os.IsNotExist(readErr) {
		return nil, nil, nil
	}
	rel, err := mirror.PublicArtifactPathWithin(root, mdPath)
	if err != nil {
		return nil, nil, err
	}
	blocked := func(reason string) *PullLocalAction {
		return &PullLocalAction{ID: keySeg, Path: rel.String(), Status: pullLocalBlocked, Reason: reason, CurrentSHA256: mirror.Hash(actual)}
	}
	if readErr != nil {
		return blocked("derived_view_unreadable"), nil, nil
	}
	marker := jiraDocumentMarkerLine(string(actual))
	if marker != jiraIssueDocumentMarker {
		// Legacy views can be migrated explicitly with `jira render`. Pull does
		// not guess whether their editable regions are pristine, and future
		// markers must never be downgraded by an older binary.
		return blocked("derived_view_unqualified"), actual, nil
	}

	is, ok := loadIssueSnapshot(root, filepath.Join(dir, keySeg+".json"))
	if !ok {
		return blocked("derived_view_snapshot_unqualified"), actual, nil
	}
	base, present, baseErr := mirror.New(root).ReadBaseBodyExt(keySeg, wikiExt)
	if baseErr != nil || !present {
		return blocked("derived_view_baseline_unqualified"), actual, nil
	}
	display := issueWithPendingFields(is, pending)
	display.Body = string(base)
	if pending != nil && localWiki != nil {
		display.Body = string(localWiki)
	}
	if recordedView != nil {
		rs = settingsFromViewState(*recordedView)
	}
	related := loadEpicChildrenSidecar(root, epicChildrenPath(dir, keySeg))
	if related != nil && !compatibleEpicSidecar(related, display.Key, rs.EpicField) {
		related = nil
	}
	if related != nil && (rs.EpicField == "" || !isDirectEpicFieldID(rs.EpicField)) {
		rs.EpicField = related.EpicField
	}
	expected := renderIssueMarkdownWithRelated(display, assetsOnDisk(root, dir, keySeg), related, rs)
	if string(actual) != string(expected) {
		action := blocked("derived_view_modified")
		action.BaselineSHA256 = mirror.Hash(expected)
		return action, actual, nil
	}
	return nil, actual, nil
}

func (s *JiraService) pullJiraIssue(ctx context.Context, req jiraPullIssueRequest) (jiraPullIssueOutcome, error) {
	qualified, early, err := s.qualifyJiraPullIssue(req)
	if err != nil {
		return jiraPullIssueOutcome{}, err
	}
	if early != nil {
		return *early, nil
	}
	if req.opts.DryRun {
		out := jiraPullIssueOutcome{issue: qualified.pulled("would_pull")}
		if qualified.nativeAction != nil {
			out.actions = append(out.actions, *qualified.nativeAction)
		}
		return out, nil
	}

	revalidated, early, err := revalidateJiraPullIssue(qualified)
	if err != nil {
		return jiraPullIssueOutcome{}, err
	}
	if early != nil {
		return *early, nil
	}
	staged, err := stageJiraPullNative(revalidated)
	if err != nil {
		return jiraPullIssueOutcome{}, err
	}
	fetched := s.fetchJiraPullAssets(ctx, staged)
	candidate, early, err := stageJiraPullDerived(fetched)
	if err != nil {
		return jiraPullIssueOutcome{actions: staged.actions, assetsSkipped: fetched.assetsSkipped}, err
	}
	if early != nil {
		early.actions = append(staged.actions, early.actions...)
		early.assetsSkipped = fetched.assetsSkipped
		return *early, nil
	}
	out, err := publishJiraPullIssue(candidate)
	if err != nil {
		return jiraPullIssueOutcome{actions: staged.actions, assetsSkipped: fetched.assetsSkipped}, err
	}
	return out, nil
}

func (s *JiraService) qualifyJiraPullIssue(req jiraPullIssueRequest) (*jiraPullQualifiedIssue, *jiraPullIssueOutcome, error) {
	paths, err := qualifyJiraPullIssuePaths(req.root, req.issue)
	if err != nil {
		return nil, nil, err
	}
	identity, err := jiraSyncIdentity(req.issue.ID, req.knownWiki)
	if err != nil {
		return nil, nil, err
	}
	var pending *JiraPendingFields
	if req.opts.DryRun {
		pending, _, err = loadJiraPendingFieldsReadOnly(req.root, paths.keySeg)
	} else {
		pending, _, err = loadJiraPendingFieldsLocked(req.root, paths.keySeg)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := validatePendingFieldsEditable(pending, req.render); err != nil {
		return nil, nil, err
	}

	qualified := &jiraPullQualifiedIssue{
		request:     req,
		paths:       paths,
		identity:    identity,
		pending:     pending,
		bodyForView: req.issue.Body,
	}
	if pending != nil {
		loaded, localWiki, loadErr := req.mirror.LoadWiki(paths.wiki)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		qualified.localWiki = localWiki
		if err := validatePendingMirrorBinding(req.root, pending, loaded, localWiki); err != nil {
			return nil, nil, err
		}
		if loaded.Dirty {
			qualified.bodyForView = string(localWiki)
			qualified.preserveLocalWiki = true
		} else {
			pending.BeforeWikiHash = mirror.Hash(localWiki)
			pending.WikiHash = mirror.Hash([]byte(req.issue.Body))
			pending.WikiBody = req.issue.Body
			qualified.rebindPendingWiki = true
		}
	}

	viewAction, viewBytes, err := s.qualifyJiraPullView(req.root, paths.dir, paths.keySeg, paths.markdown, pending, qualified.localWiki, req.render, req.recordedView)
	if err != nil {
		return nil, nil, err
	}
	qualified.qualifiedView = viewBytes
	if viewAction != nil {
		out := blockedJiraPullIssue(qualified, *viewAction)
		return nil, &out, nil
	}
	if !qualified.preserveLocalWiki {
		qualified.nativeAction, qualified.localWiki, err = qualifyPullNative(req.mirror, paths.keySeg, paths.wiki, wikiExt, req.opts.OverwriteLocal, req.opts.StashLocal, req.knownWiki)
		if err != nil {
			return nil, nil, err
		}
		if qualified.nativeAction != nil && qualified.nativeAction.Status == pullLocalBlocked {
			out := blockedJiraPullIssue(qualified, *qualified.nativeAction)
			return nil, &out, nil
		}
	}
	qualified.nativeExisted = qualified.localWiki != nil
	qualified.viewExisted = qualified.qualifiedView != nil
	return qualified, nil, nil
}

func qualifyJiraPullIssuePaths(root string, issue *domain.Issue) (jiraPullIssuePaths, error) {
	dir := filepath.Join(root, safepath.Segment(issue.Project))
	keySeg := safepath.Segment(issue.Key)
	markdown := filepath.Join(dir, keySeg+".md")
	if !safepath.Within(dir, markdown) {
		return jiraPullIssuePaths{}, fmt.Errorf("refusing unsafe issue key %q", issue.Key)
	}
	markdownRel, err := mirror.PublicArtifactPathWithin(root, markdown)
	if err != nil {
		return jiraPullIssuePaths{}, err
	}
	wiki := filepath.Join(dir, keySeg+wikiExt)
	wikiRel, err := mirror.PublicArtifactPathWithin(root, wiki)
	if err != nil {
		return jiraPullIssuePaths{}, err
	}
	snapshot := filepath.Join(dir, keySeg+".json")
	snapshotRel, err := mirror.PublicArtifactPathWithin(root, snapshot)
	if err != nil {
		return jiraPullIssuePaths{}, err
	}
	epicChildren := epicChildrenPath(dir, keySeg)
	epicChildrenRel, err := mirror.PublicArtifactPathWithin(root, epicChildren)
	if err != nil {
		return jiraPullIssuePaths{}, err
	}
	return jiraPullIssuePaths{
		dir:             dir,
		keySeg:          keySeg,
		markdown:        markdown,
		wiki:            wiki,
		snapshot:        snapshot,
		epicChildren:    epicChildren,
		markdownRel:     markdownRel,
		wikiRel:         wikiRel,
		snapshotRel:     snapshotRel,
		epicChildrenRel: epicChildrenRel,
	}, nil
}

func revalidateJiraPullIssue(qualified *jiraPullQualifiedIssue) (*jiraPullRevalidatedIssue, *jiraPullIssueOutcome, error) {
	identity, err := jiraSyncIdentity(qualified.request.issue.ID, qualified.request.knownWiki)
	if err != nil || identity != qualified.identity {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%w: Jira stable identity changed during pull qualification", domain.ErrCheckFailed)
	}
	if err := safepath.MkdirAllWithin(qualified.request.root, qualified.paths.dir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := revalidatePullFile(qualified.request.root, qualified.paths.wiki, qualified.localWiki, qualified.nativeExisted, qualified.paths.keySeg, "native substrate"); err != nil {
		out := changedJiraPullIssue(qualified, qualified.paths.wiki)
		return nil, &out, nil
	}
	if err := revalidatePullFile(qualified.request.root, qualified.paths.markdown, qualified.qualifiedView, qualified.viewExisted, qualified.paths.keySeg, "derived view"); err != nil {
		out := changedJiraPullIssue(qualified, qualified.paths.markdown)
		return nil, &out, nil
	}
	return &jiraPullRevalidatedIssue{qualified: qualified}, nil, nil
}

func stageJiraPullNative(revalidated *jiraPullRevalidatedIssue) (*jiraPullStagedIssue, error) {
	qualified := revalidated.qualified
	staged := &jiraPullStagedIssue{revalidated: revalidated}
	if qualified.nativeAction != nil && qualified.nativeAction.Status == pullLocalWouldStash {
		stashPath, err := qualified.request.mirror.SaveNativeStash("jira", qualified.paths.keySeg, wikiExt, qualified.localWiki)
		if err != nil {
			return nil, err
		}
		qualified.nativeAction.Status = pullLocalStashed
		qualified.nativeAction.StashPath = stashPath
		staged.actions = append(staged.actions, *qualified.nativeAction)
	}
	if !safepath.Within(qualified.paths.dir, qualified.paths.wiki) {
		return nil, fmt.Errorf("refusing unsafe issue key %q", qualified.request.issue.Key)
	}
	if qualified.preserveLocalWiki {
		return staged, nil
	}
	if qualified.rebindPendingWiki {
		if err := stageJiraPendingTransaction(qualified.request.root, qualified.pending); err != nil {
			return nil, err
		}
	}
	if err := safepath.WriteFileWithin(qualified.request.root, qualified.paths.wiki, []byte(qualified.request.issue.Body), 0o644); err != nil {
		return nil, err
	}
	if qualified.nativeAction != nil && qualified.nativeAction.Status == pullLocalWouldOverwrite {
		qualified.nativeAction.Status = pullLocalOverwritten
		staged.actions = append(staged.actions, *qualified.nativeAction)
	}
	if qualified.rebindPendingWiki {
		if err := commitJiraPendingTransaction(qualified.request.root, qualified.pending); err != nil {
			return nil, err
		}
	}
	return staged, nil
}

func (s *JiraService) fetchJiraPullAssets(ctx context.Context, staged *jiraPullStagedIssue) *jiraPullFetchedIssue {
	fetched := &jiraPullFetchedIssue{staged: staged}
	qualified := staged.revalidated.qualified
	if qualified.request.opts.Assets {
		fetched.assets, fetched.assetsSkipped = s.mirrorIssueImages(ctx, qualified.request.root, qualified.paths.dir, qualified.paths.keySeg, qualified.request.issue.Fields["attachment"])
	}
	return fetched
}

func stageJiraPullDerived(fetched *jiraPullFetchedIssue) (*jiraPullPublishCandidate, *jiraPullIssueOutcome, error) {
	qualified := fetched.staged.revalidated.qualified
	req := qualified.request
	if req.related != nil {
		if err := writeEpicChildrenSidecar(req.root, qualified.paths.epicChildren, *req.related); err != nil {
			return nil, nil, fmt.Errorf("epic children %s: %w", req.issue.Key, err)
		}
	} else if req.epicChildrenEnabled {
		if err := safepath.RemoveWithin(req.root, qualified.paths.epicChildren); err != nil && !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("remove stale epic children %s: %w", req.issue.Key, err)
		}
	}
	viewIssue := issueWithPendingFields(req.issue, qualified.pending)
	if viewIssue == req.issue {
		copyIssue := *req.issue
		viewIssue = &copyIssue
	}
	viewIssue.Body = qualified.bodyForView
	if err := revalidatePullFile(req.root, qualified.paths.markdown, qualified.qualifiedView, qualified.viewExisted, qualified.paths.keySeg, "derived view"); err != nil {
		out := changedJiraPullIssue(qualified, qualified.paths.markdown)
		return nil, &out, nil
	}
	if err := safepath.WriteFileWithin(req.root, qualified.paths.markdown, renderIssueMarkdownWithRelated(viewIssue, fetched.assets, req.related, req.render), 0o644); err != nil {
		return nil, nil, err
	}
	jb, err := jiraPullSnapshotBytes(req.issue)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot %s: %w", req.issue.Key, err)
	}
	if err := safepath.WriteFileWithin(req.root, qualified.paths.snapshot, jb, 0o644); err != nil {
		return nil, nil, fmt.Errorf("snapshot %s: %w", req.issue.Key, err)
	}
	return &jiraPullPublishCandidate{fetched: fetched}, nil, nil
}

func jiraPullSnapshotBytes(issue *domain.Issue) ([]byte, error) {
	snap := JiraIssueSnapshot{Key: issue.Key, ID: issue.ID, Fields: issue.Fields}
	if snap.Fields == nil {
		snap.Fields = map[string]any{}
	}
	jb, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(jb, '\n'), nil
}

func jiraSyncIdentity(observed string, previous *mirror.SyncState) (string, error) {
	identity := ""
	if previous != nil && previous.Identity != "" {
		if !canonicalPositiveNumericString(previous.Identity) {
			return "", fmt.Errorf("%w: Jira mirror has an invalid stable identity", domain.ErrCheckFailed)
		}
		identity = previous.Identity
	}
	if !canonicalPositiveNumericString(observed) {
		return identity, nil
	}
	if identity != "" && identity != observed {
		return "", fmt.Errorf("%w: Jira stable identity changed for the tracked issue key", domain.ErrCheckFailed)
	}
	return observed, nil
}

func publishJiraPullIssue(candidate *jiraPullPublishCandidate) (jiraPullIssueOutcome, error) {
	fetched := candidate.fetched
	qualified := fetched.staged.revalidated.qualified
	req := qualified.request
	if err := req.mirror.SaveBaseExt(qualified.paths.keySeg, []byte(req.issue.Body), wikiExt); err != nil {
		return jiraPullIssueOutcome{}, err
	}
	state := mirror.SyncState{ID: qualified.paths.keySeg, Identity: qualified.identity, Version: 0, Hash: mirror.Hash([]byte(req.issue.Body)), Path: qualified.paths.wikiRel.String()}
	req.batch.Record(state)
	view := viewStateOf(req.render)
	req.batch.RecordView(qualified.paths.keySeg, view)

	epicChildren := 0
	if req.related != nil {
		epicChildren = len(req.related.Children)
	}
	return jiraPullIssueOutcome{
		issue: JiraPulled{
			Key:          req.issue.Key,
			Path:         qualified.paths.markdownRel.String(),
			WikiPath:     qualified.paths.wikiRel.String(),
			Assets:       len(fetched.assets),
			EpicChildren: epicChildren,
		},
		actions:       fetched.staged.actions,
		assetsSkipped: fetched.assetsSkipped,
		state:         &state,
		view:          &view,
	}, nil
}

func (qualified *jiraPullQualifiedIssue) pulled(status string) JiraPulled {
	return JiraPulled{
		Key: qualified.request.issue.Key, Path: qualified.paths.markdownRel.String(),
		WikiPath: qualified.paths.wikiRel.String(), Status: status,
	}
}

func blockedJiraPullIssue(qualified *jiraPullQualifiedIssue, action PullLocalAction) jiraPullIssueOutcome {
	return jiraPullIssueOutcome{issue: qualified.pulled(pullLocalBlocked), actions: []PullLocalAction{action}}
}

func changedJiraPullIssue(qualified *jiraPullQualifiedIssue, path string) jiraPullIssueOutcome {
	action := PullLocalAction{ID: qualified.paths.keySeg, Path: pullRelativePath(qualified.request.root, path), Status: pullLocalBlocked, Reason: "local_artifacts_changed"}
	return blockedJiraPullIssue(qualified, action)
}
