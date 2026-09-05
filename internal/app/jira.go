package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/wikimd"
)

const (
	jiraIssueDocumentMarkerV1 = "<!-- atl:document jira-issue v1 -->"
	jiraIssueDocumentMarkerV2 = "<!-- atl:document jira-issue v2 -->"
	jiraIssueDocumentMarker   = "<!-- atl:document jira-issue v3 -->"
)

func (s *JiraService) Issue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	return s.tr.GetIssue(ctx, key, fields)
}

func (s *JiraService) IssueResolved(ctx context.Context, key string, selectors []string) (*domain.Issue, error) {
	if len(selectors) == 0 {
		return s.tr.GetIssue(ctx, key, nil)
	}
	defs, err := s.resolveJiraFieldSelectors(ctx, selectors)
	if err != nil {
		return nil, err
	}
	return s.tr.GetIssue(ctx, key, fieldDefIDs(defs))
}

func (s *JiraService) Search(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	return s.tr.Search(ctx, jql, fields, limit, cursor)
}

func (s *JiraService) SearchIssueList(ctx context.Context, jql string, columns []string, limit int, cursor string) (*IssueList, error) {
	return s.SearchIssueListView(ctx, jql, columns, "", limit, cursor)
}

func (s *JiraService) SearchIssueListView(ctx context.Context, jql string, columns []string, view string, limit int, cursor string) (*IssueList, error) {
	return s.searchIssueListSourceView(ctx, jql, columns, view, config.JiraListSourceSearch, limit, cursor)
}

func (s *JiraService) searchIssueListSourceView(ctx context.Context, jql string, columns []string, view, source string, limit int, cursor string) (*IssueList, error) {
	selected, preset, err := s.resolveListColumns(source, view, columns)
	if err != nil {
		return nil, err
	}
	resolved, fields, err := NormalizeIssueListColumns(selected, nil)
	if err != nil {
		return nil, err
	}
	backendFields := issueListBackendFields(fields)
	page := domain.IssueSearchPage{}
	if qualified, ok := s.tr.(domain.QualifiedIssueSearcher); ok {
		page, err = qualified.SearchQualified(ctx, jql, backendFields, limit, cursor)
	} else {
		page.Issues, page.Next, err = s.tr.Search(ctx, jql, backendFields, limit, cursor)
		if page.Next == "" {
			page.PartialReason = domain.IssueSearchPartialLegacyUnqualified
		}
	}
	if err != nil {
		return nil, err
	}
	if err := validateIssueSearchPage(page); err != nil {
		return nil, err
	}
	list := NewIssueList(IssueListSource{Kind: "jql"}, map[string]any{"jql": jql}, resolved, fields, "jql-order", page.Issues, nil, page.Next)
	list.Page.Complete = page.Complete
	list.Page.Truncated = !page.Complete
	list.Page.PartialReason = page.PartialReason
	list.Projection.View = preset
	return list, nil
}

func validateIssueSearchPage(page domain.IssueSearchPage) error {
	if page.Complete {
		if page.Next != "" || page.PartialReason != "" {
			return fmt.Errorf("%w: qualified Jira search returned inconsistent completion metadata", domain.ErrCheckFailed)
		}
		return nil
	}
	if page.Next != "" {
		if page.PartialReason != "" {
			return fmt.Errorf("%w: qualified Jira search returned a continuation cursor with a terminal partial reason", domain.ErrCheckFailed)
		}
		return nil
	}
	if !domain.ValidIssueSearchPartialReason(page.PartialReason) {
		return fmt.Errorf("%w: qualified Jira search did not provide a valid terminal partial reason", domain.ErrCheckFailed)
	}
	return nil
}

func (s *JiraService) Update(ctx context.Context, key, summary string, body []byte, fields map[string]domain.JiraFieldInput) error {
	return s.tr.Update(ctx, key, summary, body, fields)
}

func (s *JiraService) Transition(ctx context.Context, key, to, comment string, fields map[string]domain.JiraFieldInput) error {
	return s.tr.Transition(ctx, key, to, comment, fields)
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
	linker, ok := s.tr.(domain.JiraEpicFieldLinker)
	if !ok {
		return s.tr.LinkEpic(ctx, issue, epic)
	}
	var configured string
	if s.cfg != nil && s.cfg.Render != nil && s.cfg.Render.Jira != nil {
		configured = s.cfg.Render.Jira.EpicField
	}
	fieldID, err := s.resolveEpicField(ctx, configured)
	if err != nil {
		return err
	}
	return linker.LinkEpicWithField(ctx, issue, epic, fieldID)
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
	names, err := jiraImageOutputNames(atts)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = filepath.Join("mirror-jira", key+".assets")
	}
	if err := preflightJiraImageTargets(dir, names); err != nil {
		return nil, err
	}
	var paths []string
	for i, a := range atts {
		if names[i] == "" {
			continue
		}
		// ListAttachments already resolved both the server filename and the
		// attachment's same-origin download path. Stream that exact path instead
		// of resolving the attachment by id again, which would repeat the issue
		// metadata request once per image. Compatible transports may omit the
		// direct path, so retain the attachment-id operation as a narrow fallback.
		var (
			rc  io.ReadCloser
			err error
		)
		if a.DownPath != "" {
			rc, err = s.tr.StreamAttachment(ctx, a.DownPath)
		} else if a.ID != "" {
			rc, _, err = s.tr.DownloadAttachment(ctx, key, a.ID)
		} else {
			continue
		}
		if err != nil {
			continue
		}
		// The whole inventory's identity-qualified names were checked before
		// downloading or replacing any file, including the ID fallback path.
		safeName := names[i]
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
		werr := safepath.WriteReaderExclusiveWithin(dir, p, rc, 0o644)
		rc.Close()
		if werr != nil {
			if os.IsExist(werr) {
				return paths, fmt.Errorf("%w: image output appeared during download; existing file preserved", domain.ErrCheckFailed)
			}
			return paths, werr
		}
		paths = append(paths, p)
	}
	return paths, nil
}

type JiraIssueSnapshot struct {
	Key    string         `json:"key"`
	ID     string         `json:"id,omitempty"`
	Fields map[string]any `json:"fields"`
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
	addMetadataField(&metadata, rs, SecDuedate, "Due date", renderTemporalFieldIn(strField(is.Fields, "duedate"), "date", rs.DisplayTimeZone))
	addMetadataField(&metadata, rs, SecCreated, "Created", renderTemporalFieldIn(strField(is.Fields, "created"), "datetime", rs.DisplayTimeZone))
	addMetadataField(&metadata, rs, SecUpdated, "Updated", renderTemporalFieldIn(strField(is.Fields, "updated"), "datetime", rs.DisplayTimeZone))
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
			metadata = append(metadata, jiraMetadataEntry{Label: fv.Label, Value: renderFieldValueForFormatIn(v, fv.Format, rs.DisplayTimeZone)})
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
			b.WriteString(renderFieldSectionIn(v, fv.Format, rs.DisplayTimeZone))
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
			b.WriteString(IssueListMarkdown(subtasksIssueList(is.Key, subs), true))
			b.WriteString("\n")
		}
	}
	if rs.On(SecEpicChildren) && related != nil {
		writeJiraSectionHeading(&b, "epic-children", "Epic Children", false)
		b.WriteString(IssueListMarkdown(epicChildrenSidecarIssueList(related), true))
		b.WriteByte('\n')
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
			fmt.Fprintf(&b, "**%s** (%s):\n\n%s\n\n", c.Author, renderTemporalFieldIn(c.Created, "datetime", rs.DisplayTimeZone), body)
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

func renderFieldValueForFormatIn(v any, format, displayTimeZone string) string {
	switch format {
	case "date", "datetime":
		return renderTemporalFieldIn(v, format, displayTimeZone)
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
	return renderTemporalFieldIn(v, format, "")
}

func renderTemporalFieldIn(v any, format, displayTimeZone string) string {
	raw := renderFieldValue(v)
	if raw == "" {
		return ""
	}
	var displayLocation *time.Location
	if displayTimeZone != "" {
		var err error
		displayLocation, err = time.LoadLocation(displayTimeZone)
		if err != nil {
			// ViewState is local state and can outlive the config that created it.
			// An invalid recorded zone must not invent a partially converted time;
			// fall back to the legacy source-offset representation instead.
			displayTimeZone = ""
		}
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
		if layout == "2006-01-02" {
			return parsed.Format("2006-01-02")
		}
		if displayLocation != nil {
			parsed = parsed.In(displayLocation)
		}
		if format == "date" {
			return parsed.Format("2006-01-02")
		}
		if displayTimeZone != "" {
			return parsed.Format("2006-01-02 15:04 MST")
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
	return renderFieldSectionIn(v, format, "")
}

func renderFieldSectionIn(v any, format, displayTimeZone string) string {
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
	return renderFieldValueForFormatIn(v, format, displayTimeZone)
}

func isFieldList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// subtask is one child issue for the generated "# Subtasks" section.
type subtask struct {
	id       string
	key      string
	summary  string
	status   string
	typeName string
	assignee string
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
		st := subtask{id: asString(m["id"]), key: asString(m["key"])}
		if sf, ok := m["fields"].(map[string]any); ok {
			st.summary = asString(sf["summary"])
			st.status = nestedFieldName(sf["status"])
			st.typeName = nestedFieldName(sf["issuetype"])
			st.assignee = nestedFieldDisplay(sf["assignee"])
		}
		if st.key != "" {
			out = append(out, st)
		}
	}
	return out
}

func subtasksIssueList(parent string, subtasks []subtask) *IssueList {
	issues := make([]domain.Issue, len(subtasks))
	for i, child := range subtasks {
		issues[i] = domain.Issue{ID: child.id, Key: child.key, Summary: child.summary, Status: child.status, Type: child.typeName, Assignee: child.assignee, Fields: map[string]any{}}
	}
	return NewIssueList(IssueListSource{Kind: "subtasks", ID: parent}, map[string]any{"parent": parent}, defaultEpicChildrenColumns, []string{"summary", "status", "issuetype", "assignee"}, "backend-order", issues, nil, "")
}

func nestedFieldName(value any) string {
	object, _ := value.(map[string]any)
	return asString(object["name"])
}

func nestedFieldDisplay(value any) string {
	object, _ := value.(map[string]any)
	if display := asString(object["displayName"]); display != "" {
		return display
	}
	return asString(object["name"])
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
