package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/textedit"
	"github.com/isukharev/atl/internal/wikimd"
)

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
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}
	p := filepath.Join(outDir, safeName)
	if !safepath.Within(outDir, p) {
		return "", "", fmt.Errorf("%w: attachment path would escape output directory", domain.ErrUsage)
	}
	if _, err := safepath.WriteReaderAtomic(p, rc, 0o644); err != nil {
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
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
		_, werr := safepath.WriteReaderAtomic(p, rc, 0o644)
		rc.Close()
		if werr != nil {
			return paths, werr
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// JiraPulled is one exported issue. Assets counts image attachments mirrored
// into the issue's <KEY>.assets/ directory; it is omitted at zero so the JSON
// shape is unchanged for a default (no --assets) pull.
type JiraPulled struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Assets int    `json:"assets,omitempty"`
}

type JiraIssueSnapshot struct {
	Key    string         `json:"key"`
	ID     string         `json:"id,omitempty"`
	Fields map[string]any `json:"fields"`
}

// JiraPullOpts narrows what Pull selects and whether it also mirrors image
// attachments. A zero-value Assets keeps the default metadata/text-only pull.
type JiraPullOpts struct {
	JQL    string
	Into   string
	Limit  int
	Fields []string
	Assets bool
}

// JiraPullResult is the pull summary. AssetsSkipped counts image attachments
// that were selected but could not be written (download/stream error, unsafe
// name); it is omitted at zero so the default JSON shape is unchanged.
type JiraPullResult struct {
	Into          string       `json:"into"`
	Issues        []JiraPulled `json:"issues"`
	AssetsSkipped int          `json:"assets_skipped,omitempty"`
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
	pullFields := jiraPullFields(opts.Fields)
	// Wire the pull through the mirror sidecar so an edited <KEY>.wiki can later be
	// pushed back under the drift guard. One sidecar load (BeginSync) and one save
	// (Flush) for the whole pull; the deferred flush persists the issues already
	// recorded when an error aborts the loop (Flush is a no-op after the explicit
	// success-path call below), matching conf Pull.
	m := mirror.New(into)
	if err := m.EnsureScaffold(); err != nil {
		return res, err
	}
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
		for i := range issues {
			// The search projection IS the issue data: the adapter forwards
			// pullFields to the search verbatim and maps through the same DTO as
			// GetIssue, so a per-issue re-fetch would double the HTTP round trips
			// for zero data gain (#65).
			full := &issues[i]
			dir := filepath.Join(into, safepath.Segment(full.Project))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return res, err
			}
			// full.Key is server-supplied; sanitize it before using it as a
			// filename and assert the result stays inside dir.
			keySeg := safepath.Segment(full.Key)
			mdPath := filepath.Join(dir, keySeg+".md")
			if !safepath.Within(dir, mdPath) {
				return res, fmt.Errorf("refusing unsafe issue key %q", full.Key)
			}
			// Write the native Jira wiki body verbatim as the editable substrate
			// (the .md beside it is a regenerated read-only view; the .wiki mirrors
			// the role .csf plays for Confluence). Written even when the body is
			// empty so the substrate file always exists for a later edit/push.
			wikiPath := filepath.Join(dir, keySeg+".wiki")
			if !safepath.Within(dir, wikiPath) {
				return res, fmt.Errorf("refusing unsafe issue key %q", full.Key)
			}
			if err := safepath.WriteFile(wikiPath, []byte(full.Body), 0o644); err != nil {
				return res, err
			}
			// Record the .wiki substrate in the sidecar + a pristine base copy so
			// `jira status`/`jira push` can detect local edits and remote drift.
			// Keyed by the sanitized issue key (the .wiki basename); Version stays 0
			// — Jira has no server-side version gate. Only the .wiki body is tracked;
			// the .md/.json/assets are read-only views, outside the sync state.
			if err := m.SaveBaseExt(keySeg, []byte(full.Body), ".wiki"); err != nil {
				return res, err
			}
			relWiki, _ := filepath.Rel(into, wikiPath)
			batch.Record(mirror.SyncState{ID: keySeg, Version: 0, Hash: mirror.Hash([]byte(full.Body)), Path: relWiki})
			// Mirror image attachments (best-effort) before rendering so the .md
			// links only the images that actually landed on disk.
			var assets []JiraIssueAsset
			if opts.Assets {
				var skipped int
				assets, skipped = s.mirrorIssueImages(ctx, dir, keySeg, full.Fields["attachment"])
				res.AssetsSkipped += skipped
			}
			if err := safepath.WriteFile(mdPath, renderIssueMarkdown(full, assets), 0o644); err != nil {
				return res, err
			}
			snap := JiraIssueSnapshot{Key: full.Key, ID: full.ID, Fields: full.Fields}
			if snap.Fields == nil {
				snap.Fields = map[string]any{}
			}
			// The snapshot is part of the pull contract: a failed write must not
			// report the issue as pulled with a missing/stale .json (#65).
			jb, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return res, fmt.Errorf("snapshot %s: %w", full.Key, err)
			}
			if err := safepath.WriteFile(filepath.Join(dir, keySeg+".json"), append(jb, '\n'), 0o644); err != nil {
				return res, fmt.Errorf("snapshot %s: %w", full.Key, err)
			}
			rel, _ := filepath.Rel(into, mdPath)
			res.Issues = append(res.Issues, JiraPulled{Key: full.Key, Path: rel, Assets: len(assets)})
			if limit > 0 && len(res.Issues) >= limit {
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
func (s *JiraService) mirrorIssueImages(ctx context.Context, dir, keySeg string, raw any) (downloaded []JiraIssueAsset, skipped int) {
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
		if mkErr := os.MkdirAll(assetsDir, 0o755); mkErr != nil {
			rc.Close()
			skipped++
			continue
		}
		_, werr := safepath.WriteReaderAtomic(p, rc, 0o644)
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

func jiraPullFields(extra []string) []string {
	base := []string{"summary", "description", "status", "issuetype", "project", "assignee", "reporter", "labels", "issuelinks", "comment", "attachment"}
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, f := range append(base, extra...) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// jiraDescStub replaces the rendered "## Description" section when the wiki→md
// renderer panics: the read view must never fail a pull, and a stale/partial
// render must never masquerade as the body. It points the reader at the .wiki
// substrate (the source of truth) and, per its contract, never embeds the wiki
// body itself. Mirrors mirror.MDUnavailableStub.
const jiraDescStub = "<!-- atl: markdown view unavailable for this revision (the wiki body did not render); the <KEY>.wiki file is the source of truth -->"

// jiraCommentStub is the same guard for a comment body that fails to render.
const jiraCommentStub = "<!-- atl: comment could not be rendered -->"

// renderIssueMarkdown emits the read-only markdown view: frontmatter + summary +
// a rendered "## Description" (the native wiki body run through wikimd — the
// verbatim body lives in the sibling <KEY>.wiki), the generated "## Image
// Attachments" section (present only when a --assets pull downloaded images),
// links, and rendered comments. It never returns an error; a renderer panic
// degrades one section to a stub (see guardRender) rather than failing the pull.
func renderIssueMarkdown(is *domain.Issue, assets []JiraIssueAsset) []byte {
	images := assetImageMap(assets)
	var b strings.Builder
	fmt.Fprintf(&b, "---\nkey: %s\nsummary: %s\nstatus: %s\ntype: %s\nproject: %s\n",
		is.Key, yamlEscape(is.Summary), is.Status, is.Type, is.Project)
	if is.Assignee != "" {
		fmt.Fprintf(&b, "assignee: %s\n", yamlEscape(is.Assignee))
	}
	if len(is.Labels) > 0 {
		fmt.Fprintf(&b, "labels: [%s]\n", strings.Join(is.Labels, ", "))
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s — %s\n\n", is.Key, is.Summary)
	if is.Body != "" {
		b.WriteString("## Description\n\n")
		b.WriteString(guardRender(jiraDescStub, func() string {
			return wikimd.Render(is.Body, wikimd.Options{Images: images})
		}))
		b.WriteString("\n\n")
	}
	if len(assets) > 0 {
		b.WriteString("## Image Attachments\n\n")
		for _, a := range assets {
			fmt.Fprintf(&b, "![%s](%s)\n", a.Title, a.Path)
		}
		b.WriteString("\n")
	}
	if len(is.Links) > 0 {
		b.WriteString("## Links\n\n")
		for _, l := range is.Links {
			fmt.Fprintf(&b, "- %s %s\n", l.Type, l.Key)
		}
		b.WriteString("\n")
	}
	if len(is.Comments) > 0 {
		b.WriteString("## Comments\n\n")
		for _, c := range is.Comments {
			body := guardRender(jiraCommentStub, func() string {
				return wikimd.Render(c.Body, wikimd.Options{Images: images})
			})
			fmt.Fprintf(&b, "**%s** (%s):\n\n%s\n\n", c.Author, c.Created, body)
		}
	}
	return []byte(b.String())
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

func yamlEscape(s string) string {
	if strings.ContainsAny(s, ":#\n\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
