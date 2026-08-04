package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// JiraPulled is one exported issue. Path points at the rendered derived .md
// staging view; WikiPath points at the sibling <KEY>.wiki substrate — the editable
// native-wiki source of truth — so agents don't have to derive it by swapping
// extensions. Assets counts image attachments mirrored into the issue's
// <KEY>.assets/ directory; it is omitted at zero so the JSON shape is unchanged
// for a default (no --assets) pull.
type JiraPulled struct {
	Key          string `json:"key"`
	Path         string `json:"path"`
	WikiPath     string `json:"wiki_path,omitempty"`
	Status       string `json:"status,omitempty"`
	Assets       int    `json:"assets,omitempty"`
	EpicChildren int    `json:"epic_children,omitempty"`
}

// JiraPullOpts narrows what Pull selects and whether it also mirrors image
// attachments. A zero-value Assets keeps the default metadata/text-only pull.
// Render is the per-run flag override for the markdown view profile; a zero value
// leaves the effective settings (local + global config) untouched.
type JiraPullOpts struct {
	JQL            string
	Into           string
	Limit          int
	Fields         []string
	Assets         bool
	DryRun         bool
	OverwriteLocal bool
	StashLocal     bool
	Render         config.RenderService
}

// JiraPullResult is the pull summary. AssetsSkipped counts image attachments
// that were selected but could not be written (download/stream error, unsafe
// name); it is omitted at zero so the default JSON shape is unchanged.
type JiraPullResult struct {
	Into                    string       `json:"into"`
	Issues                  []JiraPulled `json:"issues"`
	AssetsSkipped           int          `json:"assets_skipped,omitempty"`
	EpicChildrenTruncated   bool         `json:"epic_children_truncated,omitempty"`
	EpicChildrenTruncatedAt int          `json:"epic_children_truncated_at,omitempty"`
	// Warnings carries advisory render-resolution messages (unknown section names
	// in a profile include/exclude, malformed local config). It is omitted when
	// empty so the default pull JSON shape is unchanged; the CLI prints it on
	// stderr, never stdout.
	Warnings    []string         `json:"-"`
	LocalSafety *PullLocalSafety `json:"local_safety,omitempty"`
}

// JiraIssueAsset is one image attachment selected for mirroring. Path is the
// path of the written file relative to the issue directory (empty until the
// bytes land on disk).
type JiraIssueAsset struct {
	ID         string
	Title      string
	MediaType  string
	FileSize   int64
	ContentURL string
	Path       string
}

func (s *JiraService) qualifyJiraPullView(root, dir, keySeg, mdPath string, pending *JiraPendingFields, localWiki []byte, rs RenderSettings, recordedView *mirror.ViewState) (*PullLocalAction, []byte, error) {
	actual, err := safepath.ReadFileWithin(root, mdPath)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	rel, _ := filepath.Rel(root, mdPath)
	blocked := func(reason string) *PullLocalAction {
		return &PullLocalAction{ID: keySeg, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: reason, CurrentSHA256: mirror.Hash(actual)}
	}
	if err != nil {
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

// Pull exports issues matching the JQL to one markdown + json file each. When
// opts.Assets is set it also streams each issue's image attachments into a
// per-issue <KEY>.assets/ directory (best-effort: a failed image is skipped and
// counted, never aborting the pull).
func (s *JiraService) Pull(ctx context.Context, opts JiraPullOpts) (*JiraPullResult, error) {
	if opts.OverwriteLocal && opts.StashLocal {
		return nil, fmt.Errorf("%w: --overwrite-local and --stash-local are mutually exclusive", domain.ErrUsage)
	}
	into := opts.Into
	if into == "" {
		into = "mirror-jira"
	}
	limit := opts.Limit
	res := &JiraPullResult{Into: into, Issues: []JiraPulled{}}
	cursor := ""
	// Bind a service-fresh mirror before any field discovery or search request.
	// Dry-runs perform the same qualification without creating the root or
	// binding file. Existing Jira evidence must be migrated through the explicit
	// reviewed bind workflow; pull never guesses its origin.
	if err := prepareMirrorBackendPopulation(into, "jira", s.baseURL, wikiExt, opts.DryRun); err != nil {
		return res, err
	}
	// Resolve the effective render settings for THIS mirror (local config lives
	// under the pull root) so the API field projection covers every enabled
	// section — `full` never needs a second fetch per issue. The projection only
	// ever widens from the compat base set (the `.json` snapshot keeps its
	// standard shape under smaller profiles; profiles shape the .md view only).
	rs, warns := ResolveRender(s.cfg, into, opts.Render, "jira")
	var resolveErr error
	rs, resolveErr = s.resolveRenderFieldSelectors(ctx, rs)
	if resolveErr != nil {
		return res, resolveErr
	}
	res.Warnings = warns
	epicSelector := strings.TrimSpace(rs.EpicField)
	explicitEpicField := strings.TrimSpace(rs.EpicField) != ""
	extraFields := opts.Fields
	if len(extraFields) > 0 {
		resolvedExtra, fieldErr := s.resolveJiraFieldSelectors(ctx, extraFields)
		if fieldErr != nil {
			return res, fieldErr
		}
		extraFields = fieldDefIDs(resolvedExtra)
	}
	pullFields := jiraPullFields(extraFields, rs)
	// Wire the pull through the mirror sidecar so an edited <KEY>.wiki can later be
	// pushed back under the drift guard. One sidecar load (BeginSync) and one save
	// (Flush) for the whole pull; the deferred flush persists the issues already
	// recorded when an error aborts the loop (Flush is a no-op after the explicit
	// success-path call below), matching conf Pull.
	m := mirror.New(into)
	var batch *mirror.SyncBatch
	if !opts.DryRun {
		if err := m.EnsureScaffold(); err != nil {
			return res, err
		}
		mirrorLock, err := lockJiraPendingFields(into, "pull")
		if err != nil {
			return res, err
		}
		defer func() { _ = mirrorLock.Unlock() }()
		batch, err = m.BeginSync()
		if err != nil {
			return res, err
		}
		defer func() { _ = batch.Flush() }()
	}
	var localActions []PullLocalAction
	states, err := m.SyncStates()
	if err != nil {
		return res, err
	}
	trackedWiki := make(map[string]mirror.SyncState, len(states))
	trackedWikiIDs := make([]string, 0, len(states))
	for _, state := range states {
		if filepath.Ext(state.Path) == wikiExt {
			trackedWiki[state.ID] = state
			trackedWikiIDs = append(trackedWikiIDs, state.ID)
		}
	}
	recordedViews, err := m.ViewStatesOf(trackedWikiIDs)
	if err != nil {
		return res, err
	}
	for len(res.Issues) < limit || limit == 0 {
		issues, next, err := s.tr.Search(ctx, opts.JQL, pullFields, 100, cursor)
		if err != nil {
			return res, err
		}
		selected := issues
		if limit > 0 && len(selected) > limit-len(res.Issues) {
			selected = selected[:limit-len(res.Issues)]
		}
		relatedByEpic := map[string]JiraEpicChildrenSidecar{}
		if rs.On(SecEpicChildren) && len(selected) > 0 && (explicitEpicField || rs.EpicField != "" || hasEpicCandidate(selected)) {
			epicField, resolveErr := s.resolveEpicField(ctx, rs.EpicField)
			if resolveErr != nil {
				return res, resolveErr
			}
			rs.EpicField = epicField
			var truncated bool
			relatedByEpic, truncated, err = s.fetchEpicChildrenPage(ctx, selected, rs.EpicField)
			if err != nil {
				return res, err
			}
			if truncated {
				res.EpicChildrenTruncated = true
				res.EpicChildrenTruncatedAt = jiraEpicChildrenCap
			}
			for key, sidecar := range relatedByEpic {
				sidecar.EpicSelector = epicSelector
				relatedByEpic[key] = sidecar
			}
		}
		for i := range selected {
			// The search projection IS the issue data: the adapter forwards
			// pullFields to the search verbatim and maps through the same DTO as
			// GetIssue, so a per-issue re-fetch would double the HTTP round trips
			// for zero data gain (#65).
			full := &selected[i]
			dir := filepath.Join(into, safepath.Segment(full.Project))
			// full.Key is server-supplied; sanitize it before using it as a
			// filename and assert the result stays inside dir.
			keySeg := safepath.Segment(full.Key)
			mdPath := filepath.Join(dir, keySeg+".md")
			if !safepath.Within(dir, mdPath) {
				return res, fmt.Errorf("refusing unsafe issue key %q", full.Key)
			}
			wikiPath := filepath.Join(dir, keySeg+".wiki")
			stop, issueErr := func() (bool, error) {
				var pending *JiraPendingFields
				var pendingErr error
				if opts.DryRun {
					pending, _, pendingErr = loadJiraPendingFieldsReadOnly(into, keySeg)
				} else {
					pending, _, pendingErr = loadJiraPendingFieldsLocked(into, keySeg)
				}
				if pendingErr != nil {
					return false, pendingErr
				}
				if pendingErr := validatePendingFieldsEditable(pending, rs); pendingErr != nil {
					return false, pendingErr
				}
				bodyForView := full.Body
				preserveLocalWiki := false
				rebindPendingWiki := false
				var localWiki []byte
				if pending != nil {
					lw, loadedWiki, loadErr := m.LoadWiki(wikiPath)
					if loadErr != nil {
						return false, loadErr
					}
					localWiki = loadedWiki
					if bindErr := validatePendingMirrorBinding(into, pending, lw, localWiki); bindErr != nil {
						return false, bindErr
					}
					if lw.Dirty {
						bodyForView = string(localWiki)
						preserveLocalWiki = true
					} else {
						pending.BeforeWikiHash = mirror.Hash(localWiki)
						pending.WikiHash = mirror.Hash([]byte(full.Body))
						pending.WikiBody = full.Body
						rebindPendingWiki = true
					}
				}

				var recordedView *mirror.ViewState
				if view, ok := recordedViews[keySeg]; ok {
					recordedView = &view
				}
				viewAction, qualifiedMD, viewErr := s.qualifyJiraPullView(into, dir, keySeg, mdPath, pending, localWiki, rs, recordedView)
				if viewErr != nil {
					return false, viewErr
				}
				if viewAction != nil {
					localActions = append(localActions, *viewAction)
					rel, _ := filepath.Rel(into, mdPath)
					relWiki, _ := filepath.Rel(into, wikiPath)
					res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: pullLocalBlocked})
					return limit > 0 && len(res.Issues) >= limit, nil
				}

				var nativeAction *PullLocalAction
				if !preserveLocalWiki {
					var known *mirror.SyncState
					if state, ok := trackedWiki[keySeg]; ok {
						known = &state
					}
					nativeAction, localWiki, pendingErr = qualifyPullNative(m, keySeg, wikiPath, wikiExt, opts.OverwriteLocal, opts.StashLocal, known)
					if pendingErr != nil {
						return false, pendingErr
					}
					if nativeAction != nil && nativeAction.Status == pullLocalBlocked {
						localActions = append(localActions, *nativeAction)
						rel, _ := filepath.Rel(into, mdPath)
						relWiki, _ := filepath.Rel(into, wikiPath)
						res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: pullLocalBlocked})
						return limit > 0 && len(res.Issues) >= limit, nil
					}
				}
				nativeExisted := localWiki != nil
				viewExisted := qualifiedMD != nil
				if opts.DryRun {
					if nativeAction != nil {
						localActions = append(localActions, *nativeAction)
					}
					rel, _ := filepath.Rel(into, mdPath)
					relWiki, _ := filepath.Rel(into, wikiPath)
					res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: "would_pull"})
					return limit > 0 && len(res.Issues) >= limit, nil
				}
				if err := safepath.MkdirAllWithin(into, dir, 0o755); err != nil {
					return false, err
				}
				if err := revalidatePullFile(into, wikiPath, localWiki, nativeExisted, keySeg, "native substrate"); err != nil {
					localActions = append(localActions, PullLocalAction{ID: keySeg, Path: pullRelativePath(into, wikiPath), Status: pullLocalBlocked, Reason: "local_artifacts_changed"})
					rel, _ := filepath.Rel(into, mdPath)
					relWiki, _ := filepath.Rel(into, wikiPath)
					res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: pullLocalBlocked})
					return limit > 0 && len(res.Issues) >= limit, nil
				}
				if err := revalidatePullFile(into, mdPath, qualifiedMD, viewExisted, keySeg, "derived view"); err != nil {
					localActions = append(localActions, PullLocalAction{ID: keySeg, Path: pullRelativePath(into, mdPath), Status: pullLocalBlocked, Reason: "local_artifacts_changed"})
					rel, _ := filepath.Rel(into, mdPath)
					relWiki, _ := filepath.Rel(into, wikiPath)
					res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: pullLocalBlocked})
					return limit > 0 && len(res.Issues) >= limit, nil
				}
				if nativeAction != nil && nativeAction.Status == pullLocalWouldStash {
					stashPath, stashErr := m.SaveNativeStash("jira", keySeg, wikiExt, localWiki)
					if stashErr != nil {
						return false, stashErr
					}
					nativeAction.Status = pullLocalStashed
					nativeAction.StashPath = stashPath
					localActions = append(localActions, *nativeAction)
				}
				// Write the native Jira wiki body verbatim as the editable substrate
				// (the .md beside it is a regenerated staging view; the .wiki mirrors
				// the role .csf plays for Confluence). Written even when the body is
				// empty so the substrate file always exists for a later edit/push.
				if !safepath.Within(dir, wikiPath) {
					return false, fmt.Errorf("refusing unsafe issue key %q", full.Key)
				}
				if !preserveLocalWiki {
					if rebindPendingWiki {
						if err := stageJiraPendingTransaction(into, pending); err != nil {
							return false, err
						}
					}
					if err := safepath.WriteFileWithin(into, wikiPath, []byte(full.Body), 0o644); err != nil {
						return false, err
					}
					if nativeAction != nil && nativeAction.Status == pullLocalWouldOverwrite {
						nativeAction.Status = pullLocalOverwritten
						localActions = append(localActions, *nativeAction)
					}
					if rebindPendingWiki {
						if err := commitJiraPendingTransaction(into, pending); err != nil {
							return false, err
						}
					}
				}
				// Mirror image attachments (best-effort) before rendering so the .md
				// links only the images that actually landed on disk.
				var assets []JiraIssueAsset
				if opts.Assets {
					var skipped int
					assets, skipped = s.mirrorIssueImages(ctx, into, dir, keySeg, full.Fields["attachment"])
					res.AssetsSkipped += skipped
				}
				var related *JiraEpicChildrenSidecar
				relatedPath := epicChildrenPath(dir, keySeg)
				if sidecar, ok := relatedByEpic[full.Key]; ok {
					related = &sidecar
					if err := writeEpicChildrenSidecar(into, relatedPath, sidecar); err != nil {
						return false, fmt.Errorf("epic children %s: %w", full.Key, err)
					}
				} else if rs.On(SecEpicChildren) {
					// The issue is no longer an epic (or never was). Do not let a stale
					// sidecar from an earlier pull resurrect an obsolete child list.
					if err := safepath.RemoveWithin(into, relatedPath); err != nil && !os.IsNotExist(err) {
						return false, fmt.Errorf("remove stale epic children %s: %w", full.Key, err)
					}
				}
				viewIssue := issueWithPendingFields(full, pending)
				if viewIssue == full {
					copyIssue := *full
					viewIssue = &copyIssue
				}
				viewIssue.Body = bodyForView
				if err := revalidatePullFile(into, mdPath, qualifiedMD, viewExisted, keySeg, "derived view"); err != nil {
					localActions = append(localActions, PullLocalAction{ID: keySeg, Path: pullRelativePath(into, mdPath), Status: pullLocalBlocked, Reason: "local_artifacts_changed"})
					rel, _ := filepath.Rel(into, mdPath)
					relWiki, _ := filepath.Rel(into, wikiPath)
					res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Status: pullLocalBlocked})
					return limit > 0 && len(res.Issues) >= limit, nil
				}
				if err := safepath.WriteFileWithin(into, mdPath, renderIssueMarkdownWithRelated(viewIssue, assets, related, rs), 0o644); err != nil {
					return false, err
				}
				snap := JiraIssueSnapshot{Key: full.Key, ID: full.ID, Fields: full.Fields}
				if snap.Fields == nil {
					snap.Fields = map[string]any{}
				}
				// The snapshot is part of the pull contract: a failed write must not
				// report the issue as pulled with a missing/stale .json (#65).
				jb, err := json.MarshalIndent(snap, "", "  ")
				if err != nil {
					return false, fmt.Errorf("snapshot %s: %w", full.Key, err)
				}
				if err := safepath.WriteFileWithin(into, filepath.Join(dir, keySeg+".json"), append(jb, '\n'), 0o644); err != nil {
					return false, fmt.Errorf("snapshot %s: %w", full.Key, err)
				}
				// Record the .wiki substrate in the sidecar + a pristine base copy so
				// `jira status`/`jira push` can detect local edits and remote drift.
				// Recorded only AFTER every issue artifact (.wiki/.md/.json) is on disk
				// — a failed write above must not leave the issue marked synced by the
				// deferred flush (conf parity: sidecar state follows the page files).
				// Keyed by the sanitized issue key (the .wiki basename); Version stays 0
				// — Jira has no server-side version gate. Only the .wiki body is tracked;
				// the derived .md/.json/assets stay outside the sync state.
				if err := m.SaveBaseExt(keySeg, []byte(full.Body), ".wiki"); err != nil {
					return false, err
				}
				relWiki, _ := filepath.Rel(into, wikiPath)
				state := mirror.SyncState{ID: keySeg, Version: 0, Hash: mirror.Hash([]byte(full.Body)), Path: relWiki}
				batch.Record(state)
				trackedWiki[keySeg] = state
				// Record the render settings this .md view was written with so
				// `jira apply` can reproduce the exact pristine view later.
				view := viewStateOf(rs)
				batch.RecordView(keySeg, view)
				recordedViews[keySeg] = view
				rel, _ := filepath.Rel(into, mdPath)
				epicChildren := 0
				if related != nil {
					epicChildren = len(related.Children)
				}
				res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, WikiPath: relWiki, Assets: len(assets), EpicChildren: epicChildren})
				return limit > 0 && len(res.Issues) >= limit, nil
			}()
			if issueErr != nil {
				return res, issueErr
			}
			if stop {
				break
			}
		}
		if limit > 0 && len(res.Issues) >= limit {
			break
		}
		if next == "" || len(issues) == 0 {
			break
		}
		cursor = next
	}
	res.LocalSafety = newPullLocalSafety(opts.DryRun, localActions)
	if batch != nil {
		if err := batch.Flush(); err != nil {
			return res, err
		}
	}
	return res, pullLocalSafetyError("Jira", res.LocalSafety)
}

// mirrorIssueImages streams an issue's image attachments (identified from the
// raw `attachment` field value) into <dir>/<keySeg>.assets/ and returns the
// ones that landed on disk plus a count of those skipped. It is best-effort:
// any per-image failure (stream error, unsafe name, write error) is skipped and
// counted, never returned as an error — matching the conf pull --assets and
// `jira issue images` ethos. Non-image attachments are ignored entirely (not
// counted as skips). Bytes stream by the attachment's own content URL via
// StreamAttachment, so no extra ListAttachments round-trip is made per issue.
func (s *JiraService) mirrorIssueImages(ctx context.Context, root, dir, keySeg string, raw any) (downloaded []JiraIssueAsset, skipped int) {
	assetsSeg := keySeg + ".assets"
	assetsDir := filepath.Join(dir, assetsSeg)
	for _, a := range decodeIssueAssets(raw) {
		// Only image/* is mirrored. An empty or application/octet-stream mime
		// type is not an image here and is silently ignored (known limitation,
		// same as `jira issue images`).
		if !strings.HasPrefix(a.MediaType, "image/") {
			continue
		}
		// From here on this is an image we intended to mirror; any failure is a
		// counted skip.
		idSeg := safepath.Segment(a.ID)
		safeName, ok := safepath.Base(a.Title)
		if !ok || a.ContentURL == "" {
			skipped++
			continue
		}
		fname := idSeg + "-" + safeName
		p := filepath.Join(assetsDir, fname)
		if !safepath.Within(assetsDir, p) {
			skipped++
			continue
		}
		rc, err := s.tr.StreamAttachment(ctx, a.ContentURL)
		if err != nil {
			skipped++
			continue
		}
		if mkErr := safepath.MkdirAllWithin(root, assetsDir, 0o755); mkErr != nil {
			rc.Close()
			skipped++
			continue
		}
		_, werr := safepath.WriteReaderAtomicWithin(root, p, rc, 0o644)
		rc.Close()
		if werr != nil {
			skipped++
			continue
		}
		a.Path = assetsSeg + "/" + fname // markdown link, relative to the issue dir
		downloaded = append(downloaded, a)
	}
	return downloaded, skipped
}

// decodeIssueAssets extracts attachment metadata from a raw Jira `attachment`
// field value (a []any of maps carrying id/filename/mimeType/size/content).
// Missing, non-array, or oddly-typed entries are tolerated and skipped rather
// than erroring — the field mirrors Jira's response and may be absent or sparse.
func decodeIssueAssets(raw any) []JiraIssueAsset {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]JiraIssueAsset, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, JiraIssueAsset{
			ID:         asString(m["id"]),
			Title:      asString(m["filename"]),
			MediaType:  asString(m["mimeType"]),
			ContentURL: asString(m["content"]),
			FileSize:   asInt64(m["size"]),
		})
	}
	return out
}

// asString returns v as a string when it is one, else "".
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asInt64 coerces a JSON-decoded numeric value to int64, tolerating the
// float64 that encoding/json produces (and json.Number). Non-numeric values
// yield 0.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

// jiraPullFields builds the API `fields=` projection for a pull. The base set
// covers the default profile; rs widens it so an enabled section's data is
// fetched in the SAME search projection — `full` therefore needs no per-issue
// re-fetch. extra is the caller's explicit --fields (always included). The sprint
// section is intentionally NOT widened here: its custom field id is
// backend-specific, so it renders only when the field is already present (pulled
// via --fields or a configured custom_field), matching its best-effort contract.
func jiraPullFields(extra []string, rs RenderSettings) []string {
	base := []string{"summary", "description", "status", "issuetype", "project", "assignee", "reporter", "labels", "issuelinks", "comment", "attachment"}
	// API field name per enabled section (only those not already in base).
	sectionField := map[string]string{
		SecPriority:    "priority",
		SecParent:      "parent",
		SecCreated:     "created",
		SecUpdated:     "updated",
		SecResolution:  "resolution",
		SecDuedate:     "duedate",
		SecComponents:  "components",
		SecFixVersions: "fixVersions",
		SecSubtasks:    "subtasks",
	}
	var widen []string
	for sec, field := range sectionField {
		if rs.On(sec) {
			widen = append(widen, field)
		}
	}
	sort.Strings(widen) // deterministic projection order
	widen = append(widen, rs.CustomFields...)
	for _, fv := range rs.FieldViews {
		widen = append(widen, fv.ID)
	}

	seen := make(map[string]bool, len(base)+len(widen)+len(extra))
	out := make([]string, 0, len(base)+len(widen)+len(extra))
	for _, group := range [][]string{base, widen, extra} {
		for _, f := range group {
			f = strings.TrimSpace(f)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
