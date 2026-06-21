package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
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

func (s *JiraService) Transition(ctx context.Context, key, to, comment string) error {
	return s.tr.Transition(ctx, key, to, comment)
}

func (s *JiraService) Comment(ctx context.Context, key string, body []byte) (*domain.Comment, error) {
	return s.tr.AddComment(ctx, key, body)
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
		data, name, err := s.tr.DownloadAttachment(ctx, key, a.ID)
		if err != nil {
			continue
		}
		// name is a server-supplied attachment filename: reduce to a safe base
		// name and confine the write to dir so it cannot escape via "../".
		safeName, ok := safepath.Base(name)
		if !ok {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return paths, err
		}
		p := filepath.Join(dir, safeName)
		if !safepath.Within(dir, p) {
			continue
		}
		if err := safepath.WriteFile(p, data, 0o644); err != nil {
			return paths, err
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

// Pull exports issues matching jql to one markdown + json file each.
func (s *JiraService) Pull(ctx context.Context, jql, into string, limit int) ([]JiraPulled, error) {
	if into == "" {
		into = "mirror-jira"
	}
	var out []JiraPulled
	cursor := ""
	for len(out) < limit || limit == 0 {
		issues, next, err := s.tr.Search(ctx, jql, nil, 100, cursor)
		if err != nil {
			return out, err
		}
		for _, is := range issues {
			full, err := s.tr.GetIssue(ctx, is.Key, nil)
			if err != nil {
				full = &is
			}
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
			if jb, err := json.MarshalIndent(full.Fields, "", "  "); err == nil {
				_ = safepath.WriteFile(filepath.Join(dir, keySeg+".json"), append(jb, '\n'), 0o644)
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
