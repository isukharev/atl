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
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/textedit"
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

// JiraPulled is one exported issue.
type JiraPulled struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type JiraIssueSnapshot struct {
	Key    string         `json:"key"`
	ID     string         `json:"id,omitempty"`
	Fields map[string]any `json:"fields"`
}

// Pull exports issues matching jql to one markdown + json file each.
func (s *JiraService) Pull(ctx context.Context, jql, into string, limit int, fields []string) ([]JiraPulled, error) {
	if into == "" {
		into = "mirror-jira"
	}
	var out []JiraPulled
	cursor := ""
	pullFields := jiraPullFields(fields)
	for len(out) < limit || limit == 0 {
		issues, next, err := s.tr.Search(ctx, jql, pullFields, 100, cursor)
		if err != nil {
			return out, err
		}
		for i := range issues {
			// The search projection IS the issue data: the adapter forwards
			// pullFields to the search verbatim and maps through the same DTO as
			// GetIssue, so a per-issue re-fetch would double the HTTP round trips
			// for zero data gain (#65).
			full := &issues[i]
			dir := filepath.Join(into, safepath.Segment(full.Project))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return out, err
			}
			// full.Key is server-supplied; sanitize it before using it as a
			// filename and assert the result stays inside dir.
			keySeg := safepath.Segment(full.Key)
			mdPath := filepath.Join(dir, keySeg+".md")
			if !safepath.Within(dir, mdPath) {
				return out, fmt.Errorf("refusing unsafe issue key %q", full.Key)
			}
			if err := safepath.WriteFile(mdPath, renderIssueMarkdown(full), 0o644); err != nil {
				return out, err
			}
			snap := JiraIssueSnapshot{Key: full.Key, ID: full.ID, Fields: full.Fields}
			if snap.Fields == nil {
				snap.Fields = map[string]any{}
			}
			// The snapshot is part of the pull contract: a failed write must not
			// report the issue as pulled with a missing/stale .json (#65).
			jb, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return out, fmt.Errorf("snapshot %s: %w", full.Key, err)
			}
			if err := safepath.WriteFile(filepath.Join(dir, keySeg+".json"), append(jb, '\n'), 0o644); err != nil {
				return out, fmt.Errorf("snapshot %s: %w", full.Key, err)
			}
			rel, _ := filepath.Rel(into, mdPath)
			out = append(out, JiraPulled{Key: full.Key, Path: rel})
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
		if next == "" || len(issues) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
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

// renderIssueMarkdown emits frontmatter + summary + native-wiki body. The body
// is kept verbatim (Jira wiki) so it remains a faithful, editable source.
func renderIssueMarkdown(is *domain.Issue) []byte {
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
		b.WriteString("## Description (Jira wiki)\n\n")
		b.WriteString(is.Body)
		b.WriteString("\n\n")
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
			fmt.Fprintf(&b, "**%s** (%s):\n\n%s\n\n", c.Author, c.Created, c.Body)
		}
	}
	return []byte(b.String())
}

func yamlEscape(s string) string {
	if strings.ContainsAny(s, ":#\n\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
