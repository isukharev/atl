package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/wikimerge"
)

// JiraApplyOpts tunes Apply.
type JiraApplyOpts struct {
	DryRun        bool
	AllowLoss     bool
	RebasePending bool                 // explicitly adopt raw snapshot field values as new pending bases
	Into          string               // mirror root override (defaults to nearest .atl)
	Render        config.RenderService // per-run markdown-view profile override
}

// JiraApplyResult is the JSON contract of `jira apply` — it mirrors conf apply's
// ApplyResult, swapping the CSF-specific fields for the `.wiki` substrate.
type JiraApplyResult struct {
	Path        string             `json:"path"`      // the .md that was applied
	WikiPath    string             `json:"wiki_path"` // the .wiki that was (or would be) written
	PendingPath string             `json:"pending_path,omitempty"`
	DryRun      bool               `json:"dry_run"`
	Rebased     bool               `json:"rebased,omitempty"`
	Report      *wikimerge.Report  `json:"report"`
	Fields      []JiraAppliedField `json:"fields,omitempty"`
	Wrote       bool               `json:"wrote"`
	Warning     string             `json:"warning,omitempty"` // post-write degradation (e.g. .md view not refreshed)
}

// JiraAppliedField reports one configured rich-text field extracted from the
// Markdown view. Pending means the proposed value differs from the last remote
// snapshot and will be included in a later guarded `jira push`.
type JiraAppliedField struct {
	ID      string            `json:"id"`
	Pending bool              `json:"pending"`
	Report  *wikimerge.Report `json:"report"`
}

// Apply merges edits made in an issue's `.md` view back into its `.wiki`
// substrate and explicit pending-field state (block-level, non-lossy: untouched
// native wiki blocks keep their exact base bytes). The generated Description and
// field sections configured as editable section+jira_wiki are writable; edits to
// all other generated content are refused, so a stray edit never silently
// vanishes.
//
// It is a local operation — no backend access; `jira push` stays the only write
// path to the server. Field edits never mutate the raw JSON snapshot.
// Preconditions mirror conf apply: the issue was pulled through
// the sidecar (base + snapshot exist) and the local `.wiki` still matches the
// base (direct wiki edits win over the md surface; push or re-pull first).
//
// The pristine view the edit is diffed against is reproduced from the render
// settings recorded when the .md was last written (pull/render/apply), not the
// ambient config — so a profile change between render and apply never causes a
// spurious refusal. Explicit --render-* flags (o.Render) override the recorded
// settings; a pre-upgrade mirror with no recorded view falls back to the ambient
// config (today's behavior). The chosen settings are recorded again after the
// post-write refresh.
//
// CRLF: the base `.wiki` holds the server's bytes verbatim (Jira DC descriptions
// are typically CRLF). wikimerge preserves those bytes byte-for-byte — its block
// scanner treats `\n`, `\r\n`, and lone `\r` as one line break whose bytes live
// in the inter-block gaps, so an untouched view round-trips a CRLF base
// unchanged. The *edited* `.md` is normalized CRLF→LF
// (matching wikimd's own line handling) before section-splitting, comparison, and
// merge, since the pristine view it is diffed against is always LF.
func (s *JiraService) Apply(mdPath string, o JiraApplyOpts) (*JiraApplyResult, error) {
	if !strings.HasSuffix(mdPath, ".md") {
		return nil, fmt.Errorf("%w: jira apply takes the issue's .md view (got %q)", domain.ErrUsage, mdPath)
	}
	// Root discovery walks parent directories; a relative path (the common case
	// when running from inside the mirror) must be absolutized first or the walk
	// terminates immediately at ".".
	if abs, err := filepath.Abs(mdPath); err == nil {
		mdPath = abs
	}
	wikiPath := strings.TrimSuffix(mdPath, ".md") + wikiExt
	dir := filepath.Dir(wikiPath)
	keySeg := strings.TrimSuffix(filepath.Base(wikiPath), wikiExt)
	root := o.Into
	if root == "" {
		root = mirrorRootOf(wikiPath)
	}
	if absRoot, absErr := filepath.Abs(root); absErr == nil {
		root = absRoot
	} else {
		return nil, fmt.Errorf("resolve mirror root %q: %w", root, absErr)
	}
	m := mirror.New(root)
	issueLock, err := lockJiraPendingFields(root, keySeg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = issueLock.Unlock() }()

	// The `.wiki` substrate must exist next to the .md.
	curWiki, err := safepath.ReadFileWithin(root, wikiPath)
	if err != nil {
		return nil, fmt.Errorf("%w: no %s next to %s — is this a mirrored issue? (jira mirror)", domain.ErrNotFound, keySeg+wikiExt, mdPath)
	}
	// A pristine base is required so the merge has a three-way anchor and the
	// divergence check has a baseline.
	baseWiki, ok := m.BaseBodyExt(keySeg, wikiExt)
	if !ok {
		return nil, fmt.Errorf("%w: no pristine base for %s — re-pull it (older mirrors lack .atl/base)", domain.ErrNotFound, keySeg)
	}
	// A direct wiki edit normally wins over the md surface. The explicit
	// --rebase-pending recovery path is the exception: it does not merge the md
	// Description, it binds reviewed pending fields to the exact current wiki.
	wikiDiverged := mirror.Hash(curWiki) != mirror.Hash(baseWiki)
	if wikiDiverged && !o.RebasePending {
		return nil, fmt.Errorf("%w: %s has diverged from the last-synced base (the .wiki was edited directly) — push or re-pull before applying .md edits",
			domain.ErrCheckFailed, keySeg+wikiExt)
	}
	// The `<KEY>.json` snapshot supplies the metadata/section fields needed to
	// reproduce the pristine view. Field edits live separately under .atl and
	// are overlaid only for the derived view; the raw snapshot remains the last
	// remote representation until a successful push refreshes it.
	is, snapOK := loadIssueSnapshot(root, filepath.Join(dir, keySeg+".json"))
	if !snapOK {
		return nil, fmt.Errorf("%w: no %s.json snapshot for %s — re-pull it", domain.ErrNotFound, keySeg, keySeg)
	}
	rawEdited, err := safepath.ReadFileWithin(root, mdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	}
	edited := normalizeMD(string(rawEdited))
	if strings.HasPrefix(edited, "---\n") {
		return nil, fmt.Errorf("%w: this is a legacy YAML-headed Jira view; save any edits outside the derived view, run `jira render` (or pull again), then reapply the reviewed edits", domain.ErrCheckFailed)
	}
	if err := validateJiraIssueDocumentMarker(edited); err != nil {
		return nil, err
	}

	pending, _, err := loadJiraPendingFieldsLocked(root, keySeg)
	if err != nil {
		return nil, err
	}
	displayIssue := issueWithPendingFields(is, pending)
	if o.RebasePending && pending == nil {
		return nil, fmt.Errorf("%w: --rebase-pending requires an existing pending field edit", domain.ErrUsage)
	}

	// Reproduce the pristine view from the base body + snapshot fields under the
	// render settings the .md was actually written with, so an untouched .md
	// compares byte-identical and unchanged-block detection (image embeds
	// included) matches what the reader saw. Resolution order:
	//   1. explicit --render-* flags win (the escape hatch);
	//   2. else the settings recorded when this view was last rendered/pulled,
	//      ignoring the ambient config so a profile change between render and
	//      apply cannot cause a spurious refusal;
	//   3. else (pre-upgrade mirror with no recorded view) the ambient config,
	//      exactly today's behavior.
	displayIssue.Body = string(baseWiki)
	var rs RenderSettings
	switch {
	case renderOverrideSet(o.Render):
		rs, _ = ResolveRender(s.cfg, root, o.Render, "jira")
	default:
		vs, ok, verr := m.ViewStateOf(keySeg)
		if verr != nil {
			// Corrupt sidecar is already ErrCheckFailed — propagate it.
			return nil, verr
		}
		if ok {
			rs = settingsFromViewState(vs)
		} else {
			rs, _ = ResolveRender(s.cfg, root, config.RenderService{}, "jira")
		}
	}
	if err := validatePendingFieldsEditable(pending, rs); err != nil {
		return nil, err
	}
	assets := assetsOnDisk(root, dir, keySeg)
	related := loadEpicChildrenSidecar(root, epicChildrenPath(dir, keySeg))
	if related != nil && !compatibleEpicSidecar(related, displayIssue.Key, rs.EpicField) {
		related = nil
	}
	if related != nil && (rs.EpicField == "" || !isDirectEpicFieldID(rs.EpicField)) {
		rs.EpicField = related.EpicField
	}
	if wikiDiverged && o.RebasePending {
		rebindIssue := issueWithPendingFields(is, pending)
		rebindIssue.Body = pending.WikiBody
		oldPrefix, oldDesc, oldSuffix, oldFieldRegions := renderIssueMarkdownLayout(rebindIssue, assets, related, rs, true, true)
		oldPristine := oldPrefix + oldDesc + oldSuffix
		editedRegions, extractErr := extractJiraEditableRegions(edited, oldPristine, jiraEditableRegions(oldPrefix, oldDesc, oldFieldRegions))
		if extractErr != nil {
			return nil, fmt.Errorf("%w; restore generated/read-only content before rebasing the direct wiki edit", extractErr)
		}
		if editedRegions["description"] != strings.Trim(oldDesc, "\n") {
			return nil, fmt.Errorf("%w: Description has unapplied Markdown edits; apply or discard them before rebasing a direct wiki edit", domain.ErrCheckFailed)
		}
		for _, region := range oldFieldRegions {
			if editedRegions["field."+region.FieldID] != renderFieldSection(region.BaseWiki, "jira_wiki") {
				return nil, fmt.Errorf("%w: field %s has unapplied Markdown edits; apply/review those before rebasing a direct wiki edit", domain.ErrCheckFailed, region.FieldID)
			}
		}
		res := &JiraApplyResult{
			Path: mdPath, WikiPath: wikiPath, DryRun: o.DryRun, Rebased: true,
			Report: &wikimerge.Report{},
		}
		relWiki, relErr := filepath.Rel(root, wikiPath)
		if relErr != nil || relWiki == ".." || strings.HasPrefix(relWiki, ".."+string(filepath.Separator)) {
			return res, fmt.Errorf("%w: wiki path %q is outside mirror root %q", domain.ErrUsage, wikiPath, root)
		}
		next := &JiraPendingFields{
			Key: keySeg, WikiPath: relWiki,
			BeforeWikiHash: mirror.Hash(curWiki), WikiHash: mirror.Hash(curWiki), WikiBody: string(curWiki),
		}
		for _, field := range pending.Fields {
			base, ok := jiraSnapshotStringField(is.Fields, field.ID)
			if !ok {
				return res, fmt.Errorf("%w: pending field %s is no longer a string in the raw snapshot", domain.ErrCheckFailed, field.ID)
			}
			isPending := field.Value != base
			res.Fields = append(res.Fields, JiraAppliedField{ID: field.ID, Pending: isPending, Report: &wikimerge.Report{}})
			if isPending {
				next.Fields = append(next.Fields, JiraPendingField{ID: field.ID, Base: base, Value: field.Value})
			}
		}
		if len(next.Fields) > 0 {
			res.PendingPath = jiraPendingFieldsPath(root, keySeg)
		}
		if o.DryRun {
			return res, nil
		}
		matches, readErr := jiraWikiHasHash(root, wikiPath, next.WikiHash)
		if readErr != nil || !matches {
			return res, fmt.Errorf("%w: %s changed while rebasing pending fields; review the latest wiki and retry", domain.ErrCheckFailed, keySeg+wikiExt)
		}
		if err := stageJiraPendingTransaction(root, next); err != nil {
			return res, err
		}
		if err := commitJiraPendingTransaction(root, next); err != nil {
			return res, err
		}
		res.Wrote = true
		displayIssue = issueWithPendingFields(is, next)
		displayIssue.Body = string(curWiki)
		if werr := safepath.WriteFileWithin(root, mdPath, renderIssueMarkdownWithRelated(displayIssue, assets, related, rs), 0o644); werr != nil {
			res.Warning = "rebased pending fields, but the .md view could not be refreshed: " + werr.Error()
		} else if verr := m.SaveViewStates(map[string]mirror.ViewState{keySeg: viewStateOf(rs)}); verr != nil {
			res.Warning = "rebased pending fields, but the view state could not be recorded: " + verr.Error()
		}
		return res, nil
	}
	prefix, desc, suffix, fieldRegions := renderIssueMarkdownLayout(displayIssue, assets, related, rs, true, true)
	pristine := prefix + desc + suffix
	regions := jiraEditableRegions(prefix, desc, fieldRegions)
	editedValues, err := extractJiraEditableRegions(edited, pristine, regions)
	if err != nil {
		return nil, fmt.Errorf("%w; generated sections are read-only (use the matching Jira field/comment/link/attachment command)", err)
	}

	// Merge the Description bodies. baseWiki stays raw (its own bytes, CRLF or not);
	// editedDesc is LF-normalized. An untouched view yields the base body verbatim.
	mergeOpts := wikimerge.Options{
		AllowLoss:     o.AllowLoss,
		Images:        assetImageMap(assets),
		HeadingOffset: 1,
	}
	merged, rep, err := wikimerge.Merge(baseWiki, editedValues["description"], mergeOpts)
	res := &JiraApplyResult{Path: mdPath, WikiPath: wikiPath, DryRun: o.DryRun, Report: rep}
	if err != nil {
		// Every merge refusal — unconvertible block or removed-construct loss — is a
		// pre-write check failure (exit 8). A LossError carries the report so the
		// caller can show what would be dropped (conf apply parity).
		return res, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
	}

	previousFields := pendingFieldMap(pending)
	nextFields := make(map[string]JiraPendingField, len(previousFields))
	for id, field := range previousFields {
		nextFields[id] = field
	}
	for _, region := range regions {
		if region.FieldID == "" {
			continue
		}
		base, ok := jiraSnapshotStringField(is.Fields, region.FieldID)
		if previous, exists := previousFields[region.FieldID]; exists && !o.RebasePending {
			base = previous.Base
			ok = true
		}
		if !ok {
			return res, fmt.Errorf("%w: editable field %s is no longer a string in the raw snapshot; re-pull or change the render configuration", domain.ErrCheckFailed, region.FieldID)
		}
		fieldMerged, fieldReport, mergeErr := wikimerge.Merge([]byte(region.BaseWiki), editedValues[region.ID], mergeOpts)
		fieldResult := JiraAppliedField{ID: region.FieldID, Report: fieldReport}
		res.Fields = append(res.Fields, fieldResult)
		if mergeErr != nil {
			return res, fmt.Errorf("%w: field %s: %v", domain.ErrCheckFailed, region.FieldID, mergeErr)
		}
		// A touched Jira-wiki block may normalize insignificant source spacing.
		// When the edited Markdown is exactly the original remote base's render,
		// restore those original bytes instead of leaving a semantically empty
		// pending update.
		if editedValues[region.ID] == renderFieldSection(base, "jira_wiki") {
			fieldMerged = []byte(base)
		}
		value := string(fieldMerged)
		if value == base {
			delete(nextFields, region.FieldID)
		} else {
			nextFields[region.FieldID] = JiraPendingField{ID: region.FieldID, Base: base, Value: value}
		}
		res.Fields[len(res.Fields)-1].Pending = value != base
	}
	sort.Slice(res.Fields, func(i, j int) bool { return res.Fields[i].ID < res.Fields[j].ID })
	relWiki, relErr := filepath.Rel(root, wikiPath)
	if relErr != nil || relWiki == ".." || strings.HasPrefix(relWiki, ".."+string(filepath.Separator)) {
		return res, fmt.Errorf("%w: wiki path %q is outside mirror root %q", domain.ErrUsage, wikiPath, root)
	}
	nextPending := &JiraPendingFields{Key: keySeg, WikiPath: relWiki}
	for _, field := range nextFields {
		nextPending.Fields = append(nextPending.Fields, field)
	}
	sort.Slice(nextPending.Fields, func(i, j int) bool { return nextPending.Fields[i].ID < nextPending.Fields[j].ID })
	nextPending.BeforeWikiHash = mirror.Hash(curWiki)
	nextPending.WikiHash = mirror.Hash(merged)
	nextPending.WikiBody = string(merged)
	res.Rebased = o.RebasePending
	if len(nextPending.Fields) > 0 {
		res.PendingPath = jiraPendingFieldsPath(root, keySeg)
	}

	if o.DryRun {
		return res, nil
	}
	// Publish a combined Description+fields write set transactionally. The
	// non-discoverable txn file is the issue-level lock. It is promoted to the
	// pending commit marker only after the atomic .wiki write; status/push recover
	// a crash by comparing its before/after hashes.
	hasFieldTransaction := pending != nil || len(nextPending.Fields) > 0
	if hasFieldTransaction {
		if err := stageJiraPendingTransaction(root, nextPending); err != nil {
			return res, err
		}
		matches, readErr := jiraWikiHasHash(root, wikiPath, nextPending.BeforeWikiHash)
		if readErr != nil || !matches {
			_ = safepath.RemoveWithin(root, jiraPendingFieldsTxnPath(root, keySeg))
			return res, fmt.Errorf("%w: %s changed during jira apply; retry from the refreshed view", domain.ErrCheckFailed, keySeg+wikiExt)
		}
	}
	// Write only the `.wiki`; do NOT touch the sidecar or pristine base, so the
	// issue reads locally_edited (and still synced) afterwards and `jira push`
	// remains the transport under its own drift gate.
	if err := safepath.WriteFileWithin(root, wikiPath, merged, 0o644); err != nil {
		if hasFieldTransaction {
			_ = safepath.RemoveWithin(root, jiraPendingFieldsTxnPath(root, keySeg))
		}
		return res, err
	}
	if hasFieldTransaction {
		if err := commitJiraPendingTransaction(root, nextPending); err != nil {
			return res, fmt.Errorf("%w: applied wiki but could not publish its pending-field transaction; rerun status/apply to recover: %v", domain.ErrCheckFailed, err)
		}
	}
	res.Wrote = true
	// Refresh the .md view from the merged body so the two surfaces agree —
	// best-effort, same contract as pull: renderIssueMarkdown is total, so this
	// only fails on a write error, which is a warning (the .wiki write already
	// succeeded; erroring here would misreport the apply as failed and a retry
	// would refuse on base divergence).
	displayIssue = issueWithPendingFields(is, nextPending)
	displayIssue.Body = string(merged)
	if werr := safepath.WriteFileWithin(root, mdPath, renderIssueMarkdownWithRelated(displayIssue, assets, related, rs), 0o644); werr != nil {
		res.Warning = "applied, but the .md view could not be refreshed and may be stale: " + werr.Error()
	} else if verr := m.SaveViewStates(map[string]mirror.ViewState{keySeg: viewStateOf(rs)}); verr != nil {
		// Record the settings the refreshed view was written with (best-effort,
		// same contract as the refresh itself: the .wiki write already succeeded).
		res.Warning = "applied, but the view state could not be recorded: " + verr.Error()
	}
	return res, nil
}

// jiraSnapshotStringField returns the exact native wiki value used as the
// optimistic baseline for an editable rich-text field. Missing and null mean
// an empty field; structured values are rejected because editing them through a
// wiki-text surface would silently change their Jira type.
func jiraSnapshotStringField(fields map[string]any, id string) (string, bool) {
	v, present := fields[id]
	if !present || v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

// normalizeMD strips a leading BOM and normalizes CRLF / lone CR to LF, matching
// wikimd's own line handling so the edited .md compares against the LF-only
// pristine view on equal terms.
func normalizeMD(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func validateJiraIssueDocumentMarker(edited string) error {
	first, _, _ := strings.Cut(edited, "\n")
	if first == jiraIssueDocumentMarker {
		return nil
	}
	if first == jiraIssueDocumentMarkerV1 || first == "<!-- atl:document jira-issue -->" {
		return fmt.Errorf("%w: this Jira view uses a legacy document format; preserve edits outside the derived view, run `jira render` (or pull again) with this binary, then reapply them", domain.ErrCheckFailed)
	}
	if strings.HasPrefix(first, "<!-- atl:document jira-issue") {
		return fmt.Errorf("%w: unsupported Jira view format marker %q; preserve edits and update atl before opening this view — do not render or downgrade it with this binary", domain.ErrCheckFailed, first)
	}
	return fmt.Errorf("%w: missing Jira view format marker %q; save any edits outside the derived view, run `jira render` (or pull again), then reapply them", domain.ErrCheckFailed, jiraIssueDocumentMarker)
}
