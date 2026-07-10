package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/textedit"
	"github.com/isukharev/atl/internal/wikimd"
)

const jiraIssueDocumentMarker = "<!-- atl:document jira-issue v1 -->"

func (s *JiraService) Issue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	return s.tr.GetIssue(ctx, key, fields)
}

func (s *JiraService) Search(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	return s.tr.Search(ctx, jql, fields, limit, cursor)
}

func (s *JiraService) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string) (*domain.Issue, error) {
	return s.tr.Create(ctx, project, issueType, summary, body, fields)
}

func (s *JiraService) Update(ctx context.Context, key, summary string, body []byte, fields map[string]string) error {
	return s.tr.Update(ctx, key, summary, body, fields)
}

// EditDescription performs a targeted description edit in one command:
// fetch the current description, replace old→repl via the layered textedit
// matcher (exact → invisible-tolerant → whitespace-run, the same engine as
// conf edit), and write the spliced text back. It returns the pre-edit
// description alongside the replace result so the caller can render
// before/after regions.
//
// Jira DC updates are last-writer-wins (no version gate), so the old-text
// match is the drift guard: if the description changed underneath, the
// needle misses and the edit is refused instead of overwriting.
func (s *JiraService) EditDescription(ctx context.Context, key, old, repl string, all, dryRun bool) (string, *textedit.Result, error) {
	is, err := s.tr.GetIssue(ctx, key, []string{"description"})
	if err != nil {
		return "", nil, err
	}
	if is.Body == "" {
		return "", nil, fmt.Errorf("%w: issue %s has an empty description; set one with jira issue update --from-file/--from-md", domain.ErrNotFound, key)
	}
	res, err := textedit.Replace(is.Body, old, repl, all)
	if err != nil {
		var nom *textedit.NoMatchError
		if errors.As(err, &nom) {
			return "", nil, fmt.Errorf("%w: %v", domain.ErrNotFound, err)
		}
		return "", nil, fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	// The whitespace-run pass collapses newlines, so a needle written with a
	// space can match across a line break. That is benign in CSF (XML), but
	// Jira wiki is line-sensitive (h2., *, {code} are line-start tokens) — a
	// cross-line splice silently merges lines and changes structure. Refuse
	// instead (exit 8): the caller should copy --old exactly, newlines
	// included.
	if res.Pass == textedit.PassWhitespace {
		for _, m := range res.Matches {
			if strings.Count(is.Body[m.Start:m.End], "\n") != strings.Count(old, "\n") {
				return "", nil, fmt.Errorf("%w: the match spans a line boundary that --old does not (whitespace-tolerant pass); Jira wiki is line-sensitive, so copy --old exactly from the description, including newlines", domain.ErrCheckFailed)
			}
		}
	}
	if dryRun {
		return is.Body, res, nil
	}
	if err := s.tr.Update(ctx, key, "", []byte(res.Text), nil); err != nil {
		return "", nil, err
	}
	return is.Body, res, nil
}

func (s *JiraService) Transition(ctx context.Context, key, to, comment string, fields map[string]string) error {
	return s.tr.Transition(ctx, key, to, comment, fields)
}

func (s *JiraService) DeleteIssue(ctx context.Context, key string, deleteSubtasks bool) error {
	return s.tr.DeleteIssue(ctx, key, deleteSubtasks)
}

func (s *JiraService) UpdateLabels(ctx context.Context, key string, add, remove []string) error {
	return s.tr.UpdateLabels(ctx, key, add, remove)
}

// Assign sets or clears an issue's assignee. me resolves the authenticated
// user's DC username first; otherwise username is used as-is, and an empty
// username unassigns. It returns the username that was set ("" on unassign).
func (s *JiraService) Assign(ctx context.Context, key, username string, me bool) (string, error) {
	if me {
		u, err := s.tr.CurrentUser(ctx)
		if err != nil {
			return "", err
		}
		if u.Name == "" {
			return "", fmt.Errorf("%w: current user has no username to assign", domain.ErrUsage)
		}
		username = u.Name
	}
	if err := s.tr.Assign(ctx, key, username); err != nil {
		return "", err
	}
	return username, nil
}

func (s *JiraService) Me(ctx context.Context) (*domain.User, error) {
	return s.tr.CurrentUser(ctx)
}

func (s *JiraService) SearchUsers(ctx context.Context, query string, limit int) ([]domain.User, error) {
	return s.tr.SearchUsers(ctx, query, limit)
}

func (s *JiraService) GetUser(ctx context.Context, username string) (*domain.User, error) {
	return s.tr.GetUser(ctx, username)
}

// CheckResult reports which audited fields are unset on an issue.
type CheckResult struct {
	Key             string   `json:"key"`
	MissingRequired []string `json:"missing_required,omitempty"`
	MissingWarn     []string `json:"missing_warn,omitempty"`
	OK              bool     `json:"ok"`
}

// DefaultCheckFields are commonly-important fields Jira does not itself enforce;
// `issue check` warns when they are empty unless --warn overrides the set.
var DefaultCheckFields = []string{"assignee", "priority", "components", "fixVersions", "description"}

// Check audits that the given required/warn fields are populated on an issue.
// OK is false when any required field is empty. No network writes occur.
func (s *JiraService) Check(ctx context.Context, key string, require, warn []string) (*CheckResult, error) {
	fields := append(append([]string{}, require...), warn...)
	is, err := s.tr.GetIssue(ctx, key, fields)
	if err != nil {
		return nil, err
	}
	r := &CheckResult{Key: key, OK: true}
	required := make(map[string]bool, len(require))
	for _, f := range require {
		required[f] = true
		if fieldEmpty(is.Fields[f]) {
			r.MissingRequired = append(r.MissingRequired, f)
			r.OK = false
		}
	}
	for _, f := range warn {
		// A field that is already required is reported there, not duplicated here.
		if required[f] {
			continue
		}
		if fieldEmpty(is.Fields[f]) {
			r.MissingWarn = append(r.MissingWarn, f)
		}
	}
	return r, nil
}

// fieldEmpty reports whether a raw Jira field value is unset/blank.
func fieldEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func (s *JiraService) Comment(ctx context.Context, key string, body []byte) (*domain.Comment, error) {
	return s.tr.AddComment(ctx, key, body)
}

func (s *JiraService) Comments(ctx context.Context, key string) ([]domain.Comment, error) {
	return s.tr.ListComments(ctx, key)
}

func (s *JiraService) DeleteComment(ctx context.Context, key, commentID string) error {
	return s.tr.DeleteComment(ctx, key, commentID)
}

// History returns an issue's changelog (who changed what, when).
func (s *JiraService) History(ctx context.Context, key string) ([]domain.ChangelogEntry, error) {
	return s.tr.Changelog(ctx, key)
}

// Links returns an issue's links (each carrying the backend id needed to delete
// it). It reuses GetIssue rather than adding a separate endpoint.
func (s *JiraService) Links(ctx context.Context, key string) ([]domain.IssueLink, error) {
	is, err := s.tr.GetIssue(ctx, key, []string{"issuelinks"})
	if err != nil {
		return nil, err
	}
	return is.Links, nil
}

func (s *JiraService) DeleteLink(ctx context.Context, linkID string) error {
	return s.tr.DeleteLink(ctx, linkID)
}

func (s *JiraService) Link(ctx context.Context, from, to, linkType string) error {
	return s.tr.Link(ctx, from, to, linkType)
}

func (s *JiraService) LinkEpic(ctx context.Context, issue, epic string) error {
	return s.tr.LinkEpic(ctx, issue, epic)
}

func (s *JiraService) Fields(ctx context.Context) ([]domain.FieldDef, error) { return s.tr.Fields(ctx) }

func (s *JiraService) FieldOptions(ctx context.Context, project, issueType, field string) ([]string, error) {
	return s.tr.FieldOptions(ctx, project, issueType, field)
}

func (s *JiraService) Transitions(ctx context.Context, key string) ([]domain.TransitionDef, error) {
	return s.tr.Transitions(ctx, key)
}

func (s *JiraService) LinkTypes(ctx context.Context) ([]string, error) { return s.tr.LinkTypes(ctx) }

func (s *JiraService) Attachments(ctx context.Context, key string) ([]domain.Attachment, error) {
	return s.tr.ListAttachments(ctx, key)
}

// DownloadAttachment streams one Jira issue attachment into outDir and returns
// the written path plus the server-reported filename.
func (s *JiraService) DownloadAttachment(ctx context.Context, key, attachmentID, outDir string) (string, string, error) {
	if outDir == "" {
		outDir = "."
	}
	rc, name, err := s.tr.DownloadAttachment(ctx, key, attachmentID)
	if err != nil {
		return "", "", err
	}
	defer rc.Close()

	// name is server-supplied; reduce it to a safe base and confine the write
	// to the requested directory.
	safeName, ok := safepath.Base(name)
	if !ok {
		return "", "", fmt.Errorf("%w: unsafe attachment filename %q", domain.ErrUsage, name)
	}
	if err := safepath.MkdirAllWithin(outDir, outDir, 0o755); err != nil {
		return "", "", err
	}
	p := filepath.Join(outDir, safeName)
	if !safepath.Within(outDir, p) {
		return "", "", fmt.Errorf("%w: attachment path would escape output directory", domain.ErrUsage)
	}
	if _, err := safepath.WriteReaderAtomicWithin(outDir, p, rc, 0o644); err != nil {
		return "", "", err
	}
	return p, name, nil
}

// UploadAttachment reads file bytes from filePath and uploads them as a Jira
// issue attachment.
func (s *JiraService) UploadAttachment(ctx context.Context, key, filePath string) (*domain.Attachment, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	filename := filepath.Base(filePath)
	return s.tr.UploadAttachment(ctx, key, filename, f, info.Size())
}

// Images downloads image attachments of an issue into dir, returning paths.
func (s *JiraService) Images(ctx context.Context, key, dir string) ([]string, error) {
	atts, err := s.tr.ListAttachments(ctx, key)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = filepath.Join("mirror-jira", key+".assets")
	}
	var paths []string
	for _, a := range atts {
		if !strings.HasPrefix(a.MediaType, "image/") {
			continue
		}
		rc, name, err := s.tr.DownloadAttachment(ctx, key, a.ID)
		if err != nil {
			continue
		}
		// name is a server-supplied attachment filename: reduce to a safe base
		// name and confine the write to dir so it cannot escape via "../".
		safeName, ok := safepath.Base(name)
		if !ok {
			rc.Close()
			continue
		}
		if err := safepath.MkdirAllWithin(dir, dir, 0o755); err != nil {
			rc.Close()
			return paths, err
		}
		p := filepath.Join(dir, safeName)
		if !safepath.Within(dir, p) {
			rc.Close()
			continue
		}
		// Stream to disk atomically: bounded memory, and an interrupted
		// transfer never leaves a truncated image.
		_, werr := safepath.WriteReaderAtomicWithin(dir, p, rc, 0o644)
		rc.Close()
		if werr != nil {
			return paths, werr
		}
		paths = append(paths, p)
	}
	return paths, nil
}

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
	Assets       int    `json:"assets,omitempty"`
	EpicChildren int    `json:"epic_children,omitempty"`
}

type JiraIssueSnapshot struct {
	Key    string         `json:"key"`
	ID     string         `json:"id,omitempty"`
	Fields map[string]any `json:"fields"`
}

// JiraPullOpts narrows what Pull selects and whether it also mirrors image
// attachments. A zero-value Assets keeps the default metadata/text-only pull.
// Render is the per-run flag override for the markdown view profile; a zero value
// leaves the effective settings (local + global config) untouched.
type JiraPullOpts struct {
	JQL    string
	Into   string
	Limit  int
	Fields []string
	Assets bool
	Render config.RenderService
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
	Warnings []string `json:"-"`
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

// Pull exports issues matching the JQL to one markdown + json file each. When
// opts.Assets is set it also streams each issue's image attachments into a
// per-issue <KEY>.assets/ directory (best-effort: a failed image is skipped and
// counted, never aborting the pull).
func (s *JiraService) Pull(ctx context.Context, opts JiraPullOpts) (*JiraPullResult, error) {
	into := opts.Into
	if into == "" {
		into = "mirror-jira"
	}
	limit := opts.Limit
	res := &JiraPullResult{Into: into, Issues: []JiraPulled{}}
	cursor := ""
	// Resolve the effective render settings for THIS mirror (local config lives
	// under the pull root) so the API field projection covers every enabled
	// section — `full` never needs a second fetch per issue. The projection only
	// ever widens from the compat base set (the `.json` snapshot keeps its
	// standard shape under smaller profiles; profiles shape the .md view only).
	rs, warns := ResolveRender(s.cfg, into, opts.Render, "jira")
	res.Warnings = warns
	epicSelector := strings.TrimSpace(rs.EpicField)
	explicitEpicField := strings.TrimSpace(rs.EpicField) != ""
	pullFields := jiraPullFields(opts.Fields, rs)
	// Wire the pull through the mirror sidecar so an edited <KEY>.wiki can later be
	// pushed back under the drift guard. One sidecar load (BeginSync) and one save
	// (Flush) for the whole pull; the deferred flush persists the issues already
	// recorded when an error aborts the loop (Flush is a no-op after the explicit
	// success-path call below), matching conf Pull.
	m := mirror.New(into)
	if err := m.EnsureScaffold(); err != nil {
		return res, err
	}
	mirrorLock, err := lockJiraPendingFields(into, "pull")
	if err != nil {
		return res, err
	}
	defer func() { _ = mirrorLock.Unlock() }()
	batch, err := m.BeginSync()
	if err != nil {
		return res, err
	}
	defer func() { _ = batch.Flush() }()
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
			if err := safepath.MkdirAllWithin(into, dir, 0o755); err != nil {
				return res, err
			}
			// full.Key is server-supplied; sanitize it before using it as a
			// filename and assert the result stays inside dir.
			keySeg := safepath.Segment(full.Key)
			mdPath := filepath.Join(dir, keySeg+".md")
			if !safepath.Within(dir, mdPath) {
				return res, fmt.Errorf("refusing unsafe issue key %q", full.Key)
			}
			wikiPath := filepath.Join(dir, keySeg+".wiki")
			stop, issueErr := func() (bool, error) {
				pending, _, pendingErr := loadJiraPendingFieldsLocked(into, keySeg)
				if pendingErr != nil {
					return false, pendingErr
				}
				if pendingErr := validatePendingFieldsEditable(pending, rs); pendingErr != nil {
					return false, pendingErr
				}
				bodyForView := full.Body
				preserveLocalWiki := false
				rebindPendingWiki := false
				if pending != nil {
					lw, localWiki, loadErr := m.LoadWiki(wikiPath)
					if loadErr != nil {
						return false, loadErr
					}
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
				batch.Record(mirror.SyncState{ID: keySeg, Version: 0, Hash: mirror.Hash([]byte(full.Body)), Path: relWiki})
				// Record the render settings this .md view was written with so
				// `jira apply` can reproduce the exact pristine view later.
				batch.RecordView(keySeg, viewStateOf(rs))
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
				return res, nil
			}
		}
		if next == "" || len(issues) == 0 {
			break
		}
		cursor = next
	}
	if err := batch.Flush(); err != nil {
		return res, err
	}
	return res, nil
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

// jiraDescStub replaces the rendered "# Description" section when the wiki→md
// renderer panics: the read view must never fail a pull, and a stale/partial
// render must never masquerade as the body. It points the reader at the .wiki
// substrate (the source of truth) and, per its contract, never embeds the wiki
// body itself. Mirrors mirror.MDUnavailableStub.
const jiraDescStub = "<!-- atl: markdown view unavailable for this revision (the wiki body did not render); the <KEY>.wiki file is the source of truth -->"

// jiraCommentStub is the same guard for a comment body that fails to render.
const jiraCommentStub = "<!-- atl: comment could not be rendered -->"

// renderIssueMarkdown emits the derived Markdown staging view under the resolved
// render settings: a readable metadata table (key + summary always; every other
// field only when its section is enabled AND present), a rendered "# Description" (the native
// wiki body run through wikimd — the verbatim body lives in the sibling
// <KEY>.wiki), and the enabled body sections. It never returns an error; a
// renderer panic degrades one section to a stub (see guardRender) rather than
// failing the pull. Every field accessor is defensive (values come from a
// server), so a hostile field shape can never panic.
func renderIssueMarkdown(is *domain.Issue, assets []JiraIssueAsset, rs RenderSettings) []byte {
	return renderIssueMarkdownWithRelated(is, assets, nil, rs)
}

func renderIssueMarkdownWithRelated(is *domain.Issue, assets []JiraIssueAsset, related *JiraEpicChildrenSidecar, rs RenderSettings) []byte {
	prefix, desc, suffix, _ := renderIssueMarkdownLayout(is, assets, related, rs, true, true)
	return []byte(prefix + desc + suffix)
}

// renderTransientIssueMarkdown uses the shared presentation pipeline but marks
// Description read-only: unlike a mirror view, transient output has no synced
// base that `jira apply` could safely merge.
func renderTransientIssueMarkdown(is *domain.Issue, related *JiraEpicChildrenSidecar, rs RenderSettings) []byte {
	prefix, desc, suffix, _ := renderIssueMarkdownLayout(is, nil, related, rs, false, false)
	return []byte(prefix + desc + suffix)
}

type jiraEditableFieldRegion struct {
	FieldID  string
	Start    int // byte offsets relative to suffix
	End      int
	BaseWiki string
}

func renderIssueMarkdownLayout(is *domain.Issue, assets []JiraIssueAsset, related *JiraEpicChildrenSidecar, rs RenderSettings, descriptionEditable, fieldsEditable bool) (prefix, desc, suffix string, fieldRegions []jiraEditableFieldRegion) {
	images := assetImageMap(assets)
	var b strings.Builder
	metadata := []jiraMetadataEntry{
		{Label: "Key", Value: is.Key},
		{Label: "Summary", Value: is.Summary},
	}
	addMetadataField(&metadata, rs, SecStatus, "Status", is.Status)
	addMetadataField(&metadata, rs, SecType, "Type", is.Type)
	addMetadataField(&metadata, rs, SecProject, "Project", is.Project)
	addMetadataField(&metadata, rs, SecPriority, "Priority", nestedNameField(is.Fields, "priority"))
	addMetadataField(&metadata, rs, SecParent, "Parent", parentKey(is.Fields))
	addMetadataField(&metadata, rs, SecAssignee, "Assignee", is.Assignee)
	addMetadataField(&metadata, rs, SecReporter, "Reporter", is.Reporter)
	addMetadataField(&metadata, rs, SecResolution, "Resolution", nestedNameField(is.Fields, "resolution"))
	addMetadataField(&metadata, rs, SecDuedate, "Due date", renderTemporalField(strField(is.Fields, "duedate"), "date"))
	addMetadataField(&metadata, rs, SecCreated, "Created", renderTemporalField(strField(is.Fields, "created"), "datetime"))
	addMetadataField(&metadata, rs, SecUpdated, "Updated", renderTemporalField(strField(is.Fields, "updated"), "datetime"))
	if rs.On(SecLabels) && len(is.Labels) > 0 {
		metadata = append(metadata, jiraMetadataEntry{Label: "Labels", Value: strings.Join(is.Labels, ", ")})
	}
	if rs.On(SecComponents) {
		if names := namedListField(is.Fields, "components"); len(names) > 0 {
			metadata = append(metadata, jiraMetadataEntry{Label: "Components", Value: strings.Join(names, ", ")})
		}
	}
	if rs.On(SecFixVersions) {
		if names := namedListField(is.Fields, "fixVersions"); len(names) > 0 {
			metadata = append(metadata, jiraMetadataEntry{Label: "Fix versions", Value: strings.Join(names, ", ")})
		}
	}
	if rs.On(SecCustomFields) {
		viewIDs := make(map[string]bool, len(rs.FieldViews))
		for _, fv := range rs.FieldViews {
			viewIDs[fv.ID] = true
			if fv.Placement != "metadata" {
				continue
			}
			v, present := is.Fields[fv.ID]
			if !present || fieldEmpty(v) {
				if fv.ShowEmpty {
					metadata = append(metadata, jiraMetadataEntry{Label: fv.Label, Value: "—"})
				}
				continue
			}
			metadata = append(metadata, jiraMetadataEntry{Label: fv.Label, Value: renderFieldValueForFormat(v, fv.Format)})
		}
		for _, id := range rs.CustomFields {
			if viewIDs[id] {
				continue
			}
			if v, ok := customFieldValue(is.Fields, id); ok {
				metadata = append(metadata, jiraMetadataEntry{Label: id, Value: v})
			}
		}
	}
	b.WriteString(jiraIssueDocumentMarker + "\n")
	fmt.Fprintf(&b, "# %s — %s\n\n", markdownSingleLine(is.Key), markdownSingleLine(is.Summary))
	b.WriteString(renderJiraMetadata(metadata))
	b.WriteString("\n\n")
	writeJiraSectionHeading(&b, "description", "Description", descriptionEditable)
	prefix = b.String()
	if is.Body != "" {
		desc = guardRender(jiraDescStub, func() string {
			return wikimd.Render(is.Body, wikimd.Options{Images: images, HeadingOffset: 1})
		})
	}
	b.Reset()
	if is.Body != "" {
		b.WriteString("\n\n")
	}
	if rs.On(SecCustomFields) {
		for _, fv := range rs.FieldViews {
			if fv.Placement != "section" {
				continue
			}
			v, present := is.Fields[fv.ID]
			baseWiki, stringValue := v.(string)
			canEdit := fieldsEditable && fv.Editable && (!present || v == nil || stringValue)
			if !present || fieldEmpty(v) {
				if canEdit {
					writeJiraSectionHeading(&b, jiraFieldSectionID(fv.ID), fv.Label, true)
					fieldRegions = append(fieldRegions, jiraEditableFieldRegion{FieldID: fv.ID, Start: b.Len(), End: b.Len(), BaseWiki: ""})
				} else if fv.ShowEmpty {
					writeJiraSectionHeading(&b, jiraFieldSectionID(fv.ID), fv.Label, false)
					b.WriteString("_Not set._\n\n")
				}
				continue
			}
			writeJiraSectionHeading(&b, jiraFieldSectionID(fv.ID), fv.Label, canEdit)
			start := b.Len()
			b.WriteString(renderFieldSection(v, fv.Format))
			if canEdit {
				fieldRegions = append(fieldRegions, jiraEditableFieldRegion{FieldID: fv.ID, Start: start, End: b.Len(), BaseWiki: baseWiki})
			}
			b.WriteString("\n\n")
		}
	}
	if rs.On(SecAttachments) && len(assets) > 0 {
		writeJiraSectionHeading(&b, "image-attachments", "Image Attachments", false)
		for _, a := range assets {
			fmt.Fprintf(&b, "![%s](%s)\n", mdEscapeAlt(a.Title), mdEscapeDest(a.Path))
		}
		b.WriteString("\n")
	}
	if rs.On(SecAttachmentsAll) {
		if list := decodeIssueAssets(is.Fields["attachment"]); len(list) > 0 {
			writeJiraSectionHeading(&b, "attachments", "Attachments", false)
			for _, a := range list {
				fmt.Fprintf(&b, "- %s (%s, %s)\n", a.Title, humanSize(a.FileSize), mediaTypeOr(a.MediaType))
			}
			b.WriteString("\n")
		}
	}
	if rs.On(SecLinks) && len(is.Links) > 0 {
		writeJiraSectionHeading(&b, "links", "Links", false)
		for _, l := range is.Links {
			fmt.Fprintf(&b, "- %s %s\n", l.Type, l.Key)
		}
		b.WriteString("\n")
	}
	if rs.On(SecSubtasks) {
		if subs := subtasks(is.Fields); len(subs) > 0 {
			writeJiraSectionHeading(&b, "subtasks", "Subtasks", false)
			for _, st := range subs {
				if st.summary != "" {
					fmt.Fprintf(&b, "- %s — %s\n", st.key, st.summary)
				} else {
					fmt.Fprintf(&b, "- %s\n", st.key)
				}
			}
			b.WriteString("\n")
		}
	}
	if rs.On(SecEpicChildren) && related != nil {
		writeJiraSectionHeading(&b, "epic-children", "Epic Children", false)
		if len(related.Children) == 0 {
			b.WriteString("_None._\n\n")
		} else {
			for _, child := range related.Children {
				fmt.Fprintf(&b, "- %s", markdownSingleLine(child.Key))
				if child.Summary != "" {
					fmt.Fprintf(&b, " — %s", markdownSingleLine(child.Summary))
				}
				var meta []string
				if child.Status != "" {
					meta = append(meta, markdownSingleLine(child.Status))
				}
				if child.Assignee != "" {
					meta = append(meta, markdownSingleLine(child.Assignee))
				}
				if len(meta) > 0 {
					fmt.Fprintf(&b, " (%s)", strings.Join(meta, "; "))
				}
				b.WriteByte('\n')
			}
			b.WriteByte('\n')
		}
		if related.Truncated {
			fmt.Fprintf(&b, "> Warning: epic children truncated at %d issues; this list is incomplete.\n\n", related.TruncatedAt)
		}
	}
	if rs.On(SecSprint) {
		if names := sprintNames(is.Fields); len(names) > 0 {
			writeJiraSectionHeading(&b, "sprint", "Sprint", false)
			for _, n := range names {
				fmt.Fprintf(&b, "- %s\n", n)
			}
			b.WriteString("\n")
		}
	}
	if rs.On(SecComments) && len(is.Comments) > 0 {
		writeJiraSectionHeading(&b, "comments", "Comments", false)
		for _, c := range is.Comments {
			body := guardRender(jiraCommentStub, func() string {
				return wikimd.Render(c.Body, wikimd.Options{Images: images, HeadingOffset: 1})
			})
			fmt.Fprintf(&b, "**%s** (%s):\n\n%s\n\n", c.Author, renderTemporalField(c.Created, "datetime"), body)
		}
	}
	suffix = b.String()
	return prefix, desc, suffix, fieldRegions
}

type jiraMetadataEntry struct {
	Label string
	Value string
}

func addMetadataField(entries *[]jiraMetadataEntry, rs RenderSettings, section, label, value string) {
	if !rs.On(section) || value == "" {
		return
	}
	*entries = append(*entries, jiraMetadataEntry{Label: label, Value: value})
}

func renderJiraMetadata(entries []jiraMetadataEntry) string {
	var b strings.Builder
	writeJiraSectionHeading(&b, "metadata", "Metadata", false)
	b.WriteString("| Field | Value |\n| --- | --- |\n")
	for _, entry := range entries {
		fmt.Fprintf(&b, "| %s | %s |\n", markdownTableValue(entry.Label), markdownTableValue(entry.Value))
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeJiraSectionHeading(b *strings.Builder, id, title string, editable bool) {
	mode := "readonly"
	if editable {
		mode = "editable"
	}
	fmt.Fprintf(b, "<!-- atl:section %s %s -->\n# %s\n\n", id, mode, markdownSingleLine(title))
}

func jiraFieldSectionID(id string) string {
	return "field." + base64.RawURLEncoding.EncodeToString([]byte(id))
}

func markdownTableValue(s string) string {
	s = markdownSingleLine(s)
	// Metadata is server-controlled plain text, not an embedded Markdown
	// surface. Entities keep the rendered text readable while preventing a field
	// value from becoming a remote image/link, raw HTML/comment, emphasis, code,
	// or a structural table delimiter in the local preview.
	return strings.NewReplacer(
		"&", "&amp;",
		"\\", "&#92;",
		"|", "&#124;",
		"<", "&lt;",
		">", "&gt;",
		"`", "&#96;",
		"*", "&#42;",
		"_", "&#95;",
		"~", "&#126;",
		"[", "&#91;",
		"]", "&#93;",
		"!", "&#33;",
	).Replace(s)
}

// nestedNameField reads fields[key].name defensively (Jira wraps priority /
// resolution / status as {name: ...} objects).
func nestedNameField(fields map[string]any, key string) string {
	if m, ok := fields[key].(map[string]any); ok {
		return asString(m["name"])
	}
	return ""
}

// parentKey reads fields["parent"].key (the epic/parent link).
func parentKey(fields map[string]any) string {
	if m, ok := fields["parent"].(map[string]any); ok {
		return asString(m["key"])
	}
	return ""
}

// strField reads a plain string field (e.g. created/updated/duedate).
func strField(fields map[string]any, key string) string {
	return asString(fields[key])
}

// namedListField extracts the `name` of each element of an array-of-objects field
// (components, fixVersions), skipping malformed entries.
func namedListField(fields map[string]any, key string) []string {
	arr, ok := fields[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if n := asString(m["name"]); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// customFieldValue renders one configured custom field's value from the raw
// fields map: a scalar verbatim, an object via name/value/displayName, an array
// comma-joined. Returns ok=false when the field is missing or empty so a caller
// omits the line rather than emitting a blank.
func customFieldValue(fields map[string]any, id string) (string, bool) {
	v, present := fields[id]
	if !present || v == nil {
		return "", false
	}
	s := renderFieldValue(v)
	if s == "" {
		return "", false
	}
	return s, true
}

// renderFieldValue flattens an arbitrary decoded-JSON field value to a compact
// string. It is total: any shape maps to some string.
func renderFieldValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// encoding/json decodes every JSON number as float64; -1 precision keeps
		// integers integral (13, not 13.00) and floats exact (0.5).
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case map[string]any:
		for _, k := range []string{"name", "value", "displayName"} {
			if s := asString(t[k]); s != "" {
				return s
			}
		}
		return ""
	case []any:
		var parts []string
		for _, item := range t {
			if s := renderFieldValue(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return asString(v)
	}
}

func renderFieldValueForFormat(v any, format string) string {
	switch format {
	case "date", "datetime":
		return renderTemporalField(v, format)
	case "list":
		if arr, ok := v.([]any); ok {
			parts := make([]string, 0, len(arr))
			for _, item := range arr {
				if s := renderFieldValue(item); s != "" {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, ", ")
		}
	}
	return renderFieldValue(v)
}

func renderTemporalField(v any, format string) string {
	raw := renderFieldValue(v)
	if raw == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-0700",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, raw)
		if err != nil {
			continue
		}
		if format == "date" {
			return parsed.Format("2006-01-02")
		}
		if layout == "2006-01-02" {
			return parsed.Format("2006-01-02")
		}
		_, offset := parsed.Zone()
		zone := "UTC"
		if offset != 0 {
			sign := "+"
			if offset < 0 {
				sign = "-"
				offset = -offset
			}
			zone = fmt.Sprintf("%s%02d:%02d", sign, offset/3600, (offset%3600)/60)
		}
		return parsed.Format("2006-01-02 15:04") + " " + zone
	}
	return raw // total read view: preserve malformed/unexpected server values
}

// renderFieldSection renders one configured read-only Markdown section. Jira
// wiki is explicitly opt-in; every other format is a compact scalar/list view
// and never mutates the raw snapshot value.
func renderFieldSection(v any, format string) string {
	if format == "jira_wiki" {
		return guardRender("<!-- atl: configured Jira field could not be rendered -->", func() string {
			return wikimd.Render(renderFieldValue(v), wikimd.Options{HeadingOffset: 1})
		})
	}
	if format == "list" || (format == "auto" && isFieldList(v)) {
		arr, ok := v.([]any)
		if !ok {
			arr = []any{v}
		}
		var b strings.Builder
		for _, item := range arr {
			if s := renderFieldValue(item); s != "" {
				fmt.Fprintf(&b, "- %s\n", markdownSingleLine(s))
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return renderFieldValueForFormat(v, format)
}

func isFieldList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// subtask is one child issue for the generated "# Subtasks" section.
type subtask struct {
	key     string
	summary string
}

// subtasks extracts child issues from fields["subtasks"] (an array of {key,
// fields:{summary}}), tolerating missing/odd entries.
func subtasks(fields map[string]any) []subtask {
	arr, ok := fields["subtasks"].([]any)
	if !ok {
		return nil
	}
	var out []subtask
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		st := subtask{key: asString(m["key"])}
		if sf, ok := m["fields"].(map[string]any); ok {
			st.summary = asString(sf["summary"])
		}
		if st.key != "" {
			out = append(out, st)
		}
	}
	return out
}

// sprintNames best-effort extracts sprint names from whichever custom field
// carries them. Jira Agile stores the sprint field either as an array of objects
// ({name, state, ...}) or, on older DC, as an array of GreenHopper-serialized
// strings ("...[...,state=ACTIVE,name=Sprint 1,...]"). Fields are scanned in
// sorted key order for determinism; a field that does not look like a sprint is
// skipped. Returns nil when nothing extractable is present.
func sprintNames(fields map[string]any) []string {
	var out []string
	for _, k := range sortedFieldKeys(fields) {
		arr, ok := fields[k].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		for _, item := range arr {
			switch v := item.(type) {
			case map[string]any:
				// An object sprint carries a name plus a sprint-ish marker.
				if _, hasState := v["state"]; hasState {
					if n := asString(v["name"]); n != "" {
						out = append(out, n)
					}
				} else if _, hasBoard := v["boardId"]; hasBoard {
					if n := asString(v["name"]); n != "" {
						out = append(out, n)
					}
				}
			case string:
				if n := greenhopperSprintName(v); n != "" {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// greenhopperSprintName parses the `name=` value out of a GreenHopper-serialized
// sprint string, but only when it also carries a `state=` marker so a random
// "name=" string is not mistaken for a sprint.
func greenhopperSprintName(s string) string {
	if !strings.Contains(s, "state=") || !strings.Contains(s, "name=") {
		return ""
	}
	i := strings.Index(s, "name=")
	rest := s[i+len("name="):]
	// The value ends at the next comma or the closing bracket.
	end := len(rest)
	for j, r := range rest {
		if r == ',' || r == ']' {
			end = j
			break
		}
	}
	return strings.TrimSpace(rest[:end])
}

// humanSize renders a byte count compactly for the attachment list.
func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "0 B"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func mediaTypeOr(mt string) string {
	if mt == "" {
		return "unknown"
	}
	return mt
}

// assetImageMap indexes downloaded image attachments by filename so the wiki
// renderer can resolve `!name.png!` embeds to their local relative path. Returns
// nil when no assets were downloaded (the renderer then leaves embeds as
// unresolved-image inline code).
func assetImageMap(assets []JiraIssueAsset) map[string]string {
	if len(assets) == 0 {
		return nil
	}
	m := make(map[string]string, len(assets))
	for _, a := range assets {
		if a.Title != "" && a.Path != "" {
			m[a.Title] = a.Path
		}
	}
	return m
}

// guardRender runs the wiki→markdown render behind a recover so a renderer bug
// can never fail a pull: on panic it returns fallback (a stub comment) instead.
// wikimd.Render is a total function, so this is defense in depth, and the seam
// keeps the fallback path unit-testable.
func guardRender(fallback string, render func() string) (out string) {
	defer func() {
		if recover() != nil {
			out = fallback
		}
	}()
	return render()
}

// mdEscapeAlt escapes a server-supplied string for use as markdown image alt
// text / link text: backslashes and square brackets would otherwise close the
// bracket span early and corrupt the read view.
func mdEscapeAlt(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// mdEscapeDest percent-encodes the characters that break a markdown link
// destination (spaces, parentheses, angle brackets, quotes). Filenames pass
// safepath sanitizing before landing on disk, but that deliberately keeps
// spaces/parens — legal in filenames, unsafe in a bare (dest). `%` is encoded
// first so existing percent signs survive round-trip.
func mdEscapeDest(s string) string {
	r := strings.NewReplacer(
		"%", "%25", " ", "%20", "(", "%28", ")", "%29",
		"<", "%3C", ">", "%3E", `"`, "%22",
	)
	return r.Replace(s)
}

func markdownSingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
