package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	jiraCompletePullBatch  = 25
	jiraCompletePullMaxIDs = 1_000_000
)

type jiraCompletePullBinding struct {
	Fields         []string         `json:"fields"`
	Render         mirror.ViewState `json:"render"`
	OverwriteLocal bool             `json:"overwrite_local"`
	StashLocal     bool             `json:"stash_local"`
	MaxIssues      int              `json:"max_issues"`
}

type jiraCompletePass struct {
	ids  []string
	keys map[string]string
}

type jiraCompleteIncompleteError struct {
	PartialReason    string
	Source           string
	CheckpointActive bool
}

func (e *jiraCompleteIncompleteError) Error() string {
	return fmt.Sprintf("%v: incomplete complete Jira selection: %s", domain.ErrCheckFailed, e.PartialReason)
}

func (*jiraCompleteIncompleteError) Unwrap() error { return domain.ErrCheckFailed }

type jiraCompleteSelection struct {
	checkpoint mirror.CompletePullCheckpoint
	nextIndex  int
	savedIndex int
	keys       map[string]string
	result     *CompletePullResult
}

func jiraCompleteProject(project string) (string, error) {
	canonical := strings.ToUpper(strings.TrimSpace(project))
	if canonical != project || !domain.ValidJiraIssueKey(canonical+"-1") {
		return "", fmt.Errorf("%w: --project must be a canonical uppercase Jira project key", domain.ErrUsage)
	}
	return canonical, nil
}

func jiraCompleteSelectorHash(project string) (string, error) {
	return confluenceCompleteHashJSON(struct {
		Service string `json:"service"`
		Project string `json:"project"`
	}{Service: "jira", Project: project})
}

func jiraCompleteOptionsHash(opts JiraPullOpts, fields []string, view mirror.ViewState) (string, error) {
	return confluenceCompleteHashJSON(jiraCompletePullBinding{
		Fields: append([]string(nil), fields...), Render: view,
		OverwriteLocal: opts.OverwriteLocal, StashLocal: opts.StashLocal, MaxIssues: opts.MaxIssues,
	})
}

func jiraNumericIdentityLess(left, right string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}

func collectJiraCompletePass(ctx context.Context, searcher domain.QualifiedIssueSearcher, project string, maxIssues int) (jiraCompletePass, error) {
	query := `project = "` + project + `" ORDER BY id ASC`
	cursor := ""
	previousOffset := -1
	seenCursors := map[string]bool{}
	keys := map[string]string{}
	keyOwners := map[string]string{}
	expectedTotal := -1
	for {
		if seenCursors[cursor] {
			return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search repeated a pagination cursor", domain.ErrCheckFailed)
		}
		seenCursors[cursor] = true
		if cursor != "" {
			offset, err := strconv.Atoi(cursor)
			if err != nil || offset <= previousOffset {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search returned a non-advancing cursor", domain.ErrCheckFailed)
			}
			previousOffset = offset
		}
		page, err := searcher.SearchQualified(ctx, query, []string{"project"}, 100, cursor)
		if err != nil {
			return jiraCompletePass{}, err
		}
		if err := validateIssueSearchPage(page); err != nil {
			return jiraCompletePass{}, err
		}
		if page.Next != "" && len(page.Issues) == 0 {
			return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search advertised continuation without progress", domain.ErrCheckFailed)
		}
		if page.Next == "" && !page.Complete {
			return jiraCompletePass{}, &jiraCompleteIncompleteError{PartialReason: page.PartialReason}
		}
		if !page.TotalKnown || page.Total < 0 {
			return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search omitted its qualified exact total", domain.ErrCheckFailed)
		}
		if expectedTotal < 0 {
			expectedTotal = page.Total
		} else if page.Total != expectedTotal {
			return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search total changed across pages", domain.ErrCheckFailed)
		}
		for _, issue := range page.Issues {
			if !canonicalPositiveNumericString(issue.ID) || !domain.ValidJiraIssueKey(issue.Key) || issue.Project != project || !strings.HasPrefix(issue.Key, project+"-") {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search returned an issue outside the canonical project identity", domain.ErrCheckFailed)
			}
			if _, duplicate := keys[issue.ID]; duplicate {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search repeated a stable issue identity", domain.ErrCheckFailed)
			} else if owner, duplicateKey := keyOwners[issue.Key]; duplicateKey && owner != issue.ID {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search mapped one key to multiple stable identities", domain.ErrCheckFailed)
			} else {
				keys[issue.ID] = issue.Key
				keyOwners[issue.Key] = issue.ID
			}
			if len(keys) > maxIssues {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira selection exceeded --max-issues=%d; raise the explicit cap and retry", domain.ErrCheckFailed, maxIssues)
			}
			if len(keys) > jiraCompletePullMaxIDs {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira selection exceeds the %d-identity local safety limit", domain.ErrCheckFailed, jiraCompletePullMaxIDs)
			}
		}
		if len(keys) > expectedTotal {
			return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search returned more identities than its exact total", domain.ErrCheckFailed)
		}
		if page.Next == "" {
			if len(keys) != expectedTotal {
				return jiraCompletePass{}, fmt.Errorf("%w: complete Jira search terminal count differs from its exact total", domain.ErrCheckFailed)
			}
			break
		}
		cursor = page.Next
	}
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return jiraNumericIdentityLess(ids[i], ids[j]) })
	return jiraCompletePass{ids: ids, keys: keys}, nil
}

func newJiraCompleteSelection(checkpoint mirror.CompletePullCheckpoint, keys map[string]string, source string) *jiraCompleteSelection {
	total := len(checkpoint.IDs)
	result := &CompletePullResult{
		SelectorSHA256: checkpoint.SelectorSHA256, SelectionSHA256: checkpoint.SelectionSHA256,
		Source: source, Total: total, Completed: checkpoint.NextIndex,
		Remaining: total - checkpoint.NextIndex, CheckpointActive: source == "resumed",
	}
	return &jiraCompleteSelection{checkpoint: checkpoint, nextIndex: checkpoint.NextIndex, savedIndex: checkpoint.NextIndex, keys: keys, result: result}
}

func (selection *jiraCompleteSelection) advance() {
	selection.nextIndex++
	selection.result.Completed = selection.nextIndex
	selection.result.Remaining = selection.result.Total - selection.nextIndex
}

func (selection *jiraCompleteSelection) commit(m *mirror.Mirror, batch *mirror.SyncBatch) error {
	if err := batch.FlushCompletePull(selection.checkpoint); err != nil {
		return err
	}
	if selection.nextIndex > selection.savedIndex {
		selection.checkpoint.NextIndex = selection.nextIndex
		if err := m.SaveCompletePullCheckpoint(selection.checkpoint); err != nil {
			return err
		}
		selection.savedIndex = selection.nextIndex
	}
	return m.RetireCompletePullJournal(selection.checkpoint)
}

func (s *JiraService) prepareJiraCompleteSelection(ctx context.Context, m *mirror.Mirror, opts JiraPullOpts, project string, fields []string, view mirror.ViewState) (*jiraCompleteSelection, error) {
	selectorSHA256, err := jiraCompleteSelectorHash(project)
	if err != nil {
		return nil, err
	}
	optionsSHA256, err := jiraCompleteOptionsHash(opts, fields, view)
	if err != nil {
		return nil, err
	}
	checkpoint, found, err := m.CompletePullCheckpoint(selectorSHA256)
	if err != nil {
		return nil, err
	}
	if found && (checkpoint.Service != mirror.CompletePullServiceJira || checkpoint.SelectorSHA256 != selectorSHA256) {
		return nil, fmt.Errorf("%w: complete Jira checkpoint does not match its selector", domain.ErrCheckFailed)
	}
	if !opts.DryRun {
		if err := m.RecoverCompletePullPublication(selectorSHA256, checkpoint, found); err != nil {
			return nil, err
		}
		checkpoint, err = m.RecoverCompletePullJournal(selectorSHA256, checkpoint, found)
		if err != nil {
			return nil, err
		}
	}
	if found && !opts.RestartComplete {
		if checkpoint.OptionsSHA256 != optionsSHA256 {
			return nil, fmt.Errorf("%w: complete Jira pull options changed since the checkpoint; rerun the exact command or use --restart-complete after preserving local edits", domain.ErrCheckFailed)
		}
		digest, hashErr := confluenceCompleteHashJSON(checkpoint.IDs)
		if hashErr != nil || digest != checkpoint.SelectionSHA256 || !sort.SliceIsSorted(checkpoint.IDs, func(i, j int) bool { return jiraNumericIdentityLess(checkpoint.IDs[i], checkpoint.IDs[j]) }) {
			return nil, fmt.Errorf("%w: complete Jira checkpoint selection identity is invalid", domain.ErrCheckFailed)
		}
		return newJiraCompleteSelection(checkpoint, nil, "resumed"), nil
	}
	selectionSource := "new"
	if found {
		selectionSource = "restarted"
	}
	searcher, ok := s.tr.(domain.QualifiedIssueSearcher)
	if !ok {
		return nil, fmt.Errorf("%w: backend cannot qualify Jira search completeness", domain.ErrCheckFailed)
	}
	first, err := collectJiraCompletePass(ctx, searcher, project, opts.MaxIssues)
	if err != nil {
		var incomplete *jiraCompleteIncompleteError
		if errors.As(err, &incomplete) {
			incomplete.Source = selectionSource
			incomplete.CheckpointActive = found
		}
		return nil, err
	}
	second, err := collectJiraCompletePass(ctx, searcher, project, opts.MaxIssues)
	if err != nil {
		var incomplete *jiraCompleteIncompleteError
		if errors.As(err, &incomplete) {
			incomplete.Source = selectionSource
			incomplete.CheckpointActive = found
		}
		return nil, err
	}
	if !reflect.DeepEqual(first.ids, second.ids) || !reflect.DeepEqual(first.keys, second.keys) {
		return nil, fmt.Errorf("%w: complete Jira selection changed between exhaustive passes; retry after the backend settles", domain.ErrCheckFailed)
	}
	selectionSHA256, err := confluenceCompleteHashJSON(second.ids)
	if err != nil {
		return nil, err
	}
	checkpoint = mirror.CompletePullCheckpoint{
		Service: mirror.CompletePullServiceJira, SelectorSHA256: selectorSHA256,
		OptionsSHA256: optionsSHA256, SelectionSHA256: selectionSHA256, IDs: second.ids,
	}
	return newJiraCompleteSelection(checkpoint, second.keys, selectionSource), nil
}

func jiraCompleteOldView(m *mirror.Mirror, tracked mirror.JiraCompletePullTrackedState) ([]byte, error) {
	oldWiki := filepath.Join(m.Root, filepath.FromSlash(tracked.State.Path))
	oldDir := filepath.Dir(oldWiki)
	oldKey := tracked.State.ID
	issue, err := loadIssueSnapshotDetailed(m.Root, strings.TrimSuffix(oldWiki, ".wiki")+".json")
	if err != nil {
		return nil, fmt.Errorf("%w: Jira relocation snapshot is unavailable", domain.ErrCheckFailed)
	}
	if issue.ID != tracked.State.Identity && tracked.State.Identity != "" {
		return nil, fmt.Errorf("%w: Jira relocation snapshot identity changed", domain.ErrCheckFailed)
	}
	settings := settingsFromViewState(tracked.View)
	related := loadEpicChildrenSidecar(m.Root, epicChildrenPath(oldDir, oldKey))
	if related != nil && !compatibleEpicSidecar(related, issue.Key, settings.EpicField) {
		related = nil
	}
	return renderIssueMarkdownWithRelated(issue, assetsOnDisk(m.Root, oldDir, oldKey), related, settings), nil
}

func jiraCompleteTargetCollision(m *mirror.Mirror, identity string, paths jiraPullIssuePaths, tracked mirror.JiraCompletePullTrackedState, trackedFound bool) error {
	state, keyFound, err := m.SyncStateOf(paths.keySeg)
	if err != nil {
		return err
	}
	if keyFound && (!trackedFound || state != tracked.State) {
		return fmt.Errorf("%w: complete Jira target key is already tracked for another stable identity", domain.ErrCheckFailed)
	}
	if trackedFound && tracked.State.ID != paths.keySeg {
		return nil
	}
	if keyFound {
		if state.Identity != "" && state.Identity != identity {
			return fmt.Errorf("%w: complete Jira target key changed stable identity", domain.ErrCheckFailed)
		}
		base, present, baseErr := m.ReadBaseBodyExt(paths.keySeg, wikiExt)
		if baseErr != nil || !present || mirror.Hash(base) != state.Hash {
			return fmt.Errorf("%w: complete Jira target pristine base is missing or changed", domain.ErrCheckFailed)
		}
		return nil
	}
	for _, path := range []string{paths.snapshot, paths.epicChildren, filepath.Join(paths.dir, paths.keySeg+".assets"), filepath.Join(m.Root, ".atl", "base", paths.keySeg+wikiExt)} {
		if _, statErr := safepath.StatWithin(m.Root, path); statErr == nil {
			return fmt.Errorf("%w: untracked complete Jira target contains local artifacts", domain.ErrCheckFailed)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("%w: inspect complete Jira target", domain.ErrCheckFailed)
		}
	}
	return nil
}

func prepareJiraCompleteArtifacts(issue *domain.Issue, paths jiraPullIssuePaths, settings RenderSettings) (mirror.SyncState, mirror.ViewState, []mirror.CompletePullArtifact, error) {
	body := []byte(issue.Body)
	state := mirror.SyncState{ID: paths.keySeg, Identity: issue.ID, Hash: mirror.Hash(body), Path: paths.wikiRel.String()}
	view := viewStateOf(settings)
	snapshot, err := jiraPullSnapshotBytes(issue)
	if err != nil {
		return mirror.SyncState{}, mirror.ViewState{}, nil, err
	}
	basePath, err := mirror.NewPrivateBaseArtifactPath(filepath.ToSlash(filepath.Join(".atl", "base", paths.keySeg+wikiExt)))
	if err != nil {
		return mirror.SyncState{}, mirror.ViewState{}, nil, err
	}
	md := renderIssueMarkdownWithRelated(issue, nil, nil, settings)
	artifacts := []mirror.CompletePullArtifact{
		{Path: paths.wikiRel, Role: mirror.CompletePullArtifactRoleNative, Data: body, Mode: 0o644},
		{Path: paths.snapshotRel, Role: mirror.CompletePullArtifactRoleMetadata, Data: snapshot, Mode: 0o644},
		{Path: paths.markdownRel, Role: mirror.CompletePullArtifactRoleView, Data: md, Mode: 0o644, BestEffort: true},
		{Path: basePath, Role: mirror.CompletePullArtifactRoleBase, Data: body, Mode: 0o600},
	}
	return state, view, artifacts, nil
}

func (s *JiraService) pullJiraComplete(ctx context.Context, opts JiraPullOpts) (result *JiraPullResult, retErr error) {
	project, err := jiraCompleteProject(opts.Project)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.JQL) != "" || opts.Limit != 0 {
		return nil, fmt.Errorf("%w: --complete uses only --project and cannot be combined with --jql or --limit", domain.ErrUsage)
	}
	if opts.MaxIssues <= 0 || opts.MaxIssues > jiraCompletePullMaxIDs {
		return nil, fmt.Errorf("%w: --max-issues must be between 1 and %d in complete mode", domain.ErrUsage, jiraCompletePullMaxIDs)
	}
	if opts.Assets {
		return nil, fmt.Errorf("%w: --assets is not yet supported by qualified complete Jira pulls", domain.ErrUsage)
	}
	root := opts.Into
	if root == "" {
		root = "mirror-jira"
	}
	res := &JiraPullResult{Into: root, Issues: []JiraPulled{}}
	if err := prepareMirrorBackendPopulation(root, "jira", s.baseURL, wikiExt, opts.DryRun); err != nil {
		return res, err
	}
	settings, warnings := ResolveRender(s.cfg, root, opts.Render, "jira")
	settings, err = s.resolveRenderFieldSelectors(ctx, settings)
	if err != nil {
		return res, err
	}
	if settings.On(SecEpicChildren) {
		return res, fmt.Errorf("%w: epic_children is not yet supported by qualified complete Jira pulls", domain.ErrUsage)
	}
	extra := opts.Fields
	if len(extra) > 0 {
		resolved, resolveErr := s.resolveJiraFieldSelectors(ctx, extra)
		if resolveErr != nil {
			return res, resolveErr
		}
		extra = fieldDefIDs(resolved)
	}
	fields := jiraPullFields(extra, settings)
	res.Warnings = warnings
	m := mirror.New(root)
	if !opts.DryRun {
		if err := m.EnsureScaffold(); err != nil {
			return res, err
		}
		lock, lockErr := lockJiraPendingFields(root, "complete-pull")
		if lockErr != nil {
			return res, lockErr
		}
		defer func() { _ = lock.Unlock() }()
	}
	selection, err := s.prepareJiraCompleteSelection(ctx, m, opts, project, fields, viewStateOf(settings))
	if err != nil {
		var incomplete *jiraCompleteIncompleteError
		if errors.As(err, &incomplete) {
			selectorSHA256, hashErr := jiraCompleteSelectorHash(project)
			if hashErr != nil {
				return res, errors.Join(err, hashErr)
			}
			res.Complete = &CompletePullResult{
				SelectorSHA256: selectorSHA256, Source: incomplete.Source,
				PartialReason: incomplete.PartialReason, CheckpointActive: incomplete.CheckpointActive,
			}
		}
		return res, err
	}
	res.Complete = selection.result
	if !opts.DryRun && selection.result.Source != "resumed" {
		if err := m.SaveCompletePullCheckpoint(selection.checkpoint); err != nil {
			return res, err
		}
		selection.result.CheckpointActive = true
	}
	var batch *mirror.SyncBatch
	if !opts.DryRun {
		batch, err = m.BeginSync()
		if err != nil {
			return res, err
		}
		finished := false
		retiring := false
		defer func() {
			if finished {
				return
			}
			if retiring {
				retErr = errors.Join(retErr, fmt.Errorf("complete Jira pull completion cleanup was interrupted; rerun the exact command"))
				return
			}
			if commitErr := selection.commit(m, batch); commitErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("save complete Jira pull progress: %w", commitErr))
			}
			retErr = errors.Join(retErr, fmt.Errorf("complete Jira checkpoint is at %d/%d; rerun the exact command to resume", selection.savedIndex, selection.result.Total))
		}()
		defer func() {
			if selection.nextIndex == len(selection.checkpoint.IDs) && retErr == nil {
				retiring = true
				if err := selection.commit(m, batch); err != nil {
					retErr = err
					return
				}
				if err := m.RemoveCompletePullCheckpoint(selection.checkpoint.SelectorSHA256); err != nil {
					retErr = err
					return
				}
				selection.result.Complete = true
				selection.result.CheckpointActive = false
				finished = true
			}
		}()
	}

	localActions := []PullLocalAction{}
	for selection.nextIndex < len(selection.checkpoint.IDs) {
		identity := selection.checkpoint.IDs[selection.nextIndex]
		issue, fetchErr := s.tr.GetIssue(ctx, identity, fields)
		if fetchErr != nil {
			return res, fetchErr
		}
		if issue == nil || issue.ID != identity || !domain.ValidJiraIssueKey(issue.Key) || issue.Project != project || !strings.HasPrefix(issue.Key, project+"-") {
			return res, fmt.Errorf("%w: fetched Jira issue no longer matches the selected stable identity and project", domain.ErrCheckFailed)
		}
		if selectedKey := selection.keys[identity]; selectedKey != "" && selectedKey != issue.Key {
			return res, fmt.Errorf("%w: Jira issue key changed after the qualified selection passes", domain.ErrCheckFailed)
		}
		paths, pathErr := qualifyJiraPullIssuePaths(root, issue)
		if pathErr != nil {
			return res, pathErr
		}
		tracked, trackedFound, trackErr := m.JiraCompletePullStateByIdentity(identity)
		if trackErr != nil {
			return res, trackErr
		}
		if collisionErr := jiraCompleteTargetCollision(m, identity, paths, tracked, trackedFound); collisionErr != nil {
			return res, collisionErr
		}
		var known *mirror.SyncState
		var recorded *mirror.ViewState
		if trackedFound && tracked.State.ID == paths.keySeg {
			state := tracked.State
			known = &state
			if tracked.ViewFound {
				view := tracked.View
				recorded = &view
			}
		}
		qualified, early, qualifyErr := s.qualifyJiraPullIssue(jiraPullIssueRequest{
			root: root, issue: issue, opts: opts, render: settings, mirror: m,
			knownWiki: known, recordedView: recorded,
		})
		if qualifyErr != nil {
			return res, qualifyErr
		}
		if early != nil {
			localActions = append(localActions, early.actions...)
			res.Issues = append(res.Issues, early.issue)
			res.LocalSafety = newPullLocalSafety(opts.DryRun, localActions)
			return res, pullLocalSafetyError("Jira", res.LocalSafety)
		}
		if qualified.pending != nil {
			return res, fmt.Errorf("%w: complete Jira pull cannot replace an issue with pending field edits", domain.ErrCheckFailed)
		}
		if err := revalidatePullFile(root, paths.wiki, qualified.localWiki, qualified.nativeExisted, paths.keySeg, "native substrate"); err != nil {
			return res, err
		}
		if err := revalidatePullFile(root, paths.markdown, qualified.qualifiedView, qualified.viewExisted, paths.keySeg, "derived view"); err != nil {
			return res, err
		}
		state, view, artifacts, prepareErr := prepareJiraCompleteArtifacts(issue, paths, settings)
		if prepareErr != nil {
			return res, prepareErr
		}
		var relocation *mirror.JiraIssueRelocation
		if trackedFound && tracked.State.ID != paths.keySeg {
			oldMD, oldErr := jiraCompleteOldView(m, tracked)
			if oldErr != nil {
				return res, oldErr
			}
			relocation, oldErr = m.PlanJiraIssueRelocation(identity, state, oldMD)
			if oldErr != nil {
				return res, oldErr
			}
			if relocation == nil {
				return res, fmt.Errorf("%w: Jira key relocation lost its tracked predecessor", domain.ErrCheckFailed)
			}
		}
		status := ""
		stashPath := ""
		if qualified.nativeAction != nil {
			switch qualified.nativeAction.Status {
			case pullLocalWouldStash:
				if !opts.DryRun {
					stashPath, err = m.SaveNativeStash("jira", paths.keySeg, wikiExt, qualified.localWiki)
					if err != nil {
						return res, err
					}
					status = pullLocalStashed
				} else {
					status = pullLocalWouldStash
				}
			case pullLocalWouldOverwrite:
				if opts.DryRun {
					status = pullLocalWouldOverwrite
				} else {
					status = pullLocalOverwritten
				}
			}
			action := *qualified.nativeAction
			action.Status = status
			action.StashPath = stashPath
			localActions = append(localActions, action)
		}
		if opts.DryRun {
			res.Issues = append(res.Issues, JiraPulled{Key: issue.Key, Path: paths.markdownRel.String(), WikiPath: paths.wikiRel.String(), Status: "would_pull"})
			selection.nextIndex++
			continue
		}
		entry := mirror.CompletePullJournalEntry{Identity: identity, State: state, View: view}
		if err := m.PrepareJiraCompletePullPublication(selection.checkpoint, selection.nextIndex, entry, true, artifacts, relocation); err != nil {
			return res, err
		}
		if err := m.RecoverCompletePullPublication(selection.checkpoint.SelectorSHA256, selection.checkpoint, true); err != nil {
			return res, err
		}
		batch.Record(state)
		batch.RecordView(state.ID, view)
		res.Issues = append(res.Issues, JiraPulled{Key: issue.Key, Path: paths.markdownRel.String(), WikiPath: paths.wikiRel.String(), Status: status})
		selection.advance()
		if selection.nextIndex-selection.savedIndex >= jiraCompletePullBatch {
			if err := selection.commit(m, batch); err != nil {
				return res, err
			}
		}
	}
	res.LocalSafety = newPullLocalSafety(opts.DryRun, localActions)
	if opts.DryRun {
		selection.result.Complete = true
		selection.result.Completed = len(selection.checkpoint.IDs)
		selection.result.Remaining = 0
	}
	return res, pullLocalSafetyError("Jira", res.LocalSafety)
}
