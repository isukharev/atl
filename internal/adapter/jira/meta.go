package jira

import (
	"context"
	"net/url"

	"github.com/isukharev/atl/internal/domain"
)

// Fields lists all Jira fields (system + custom).
func (j *Jira) Fields(ctx context.Context) ([]domain.FieldDef, error) {
	var raw []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Custom bool   `json:"custom"`
		Schema struct {
			Type string `json:"type"`
		} `json:"schema"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/field", &raw); err != nil {
		return nil, err
	}
	out := make([]domain.FieldDef, 0, len(raw))
	for _, f := range raw {
		out = append(out, domain.FieldDef{ID: f.ID, Name: f.Name, Custom: f.Custom, Schema: f.Schema.Type})
	}
	return out, nil
}

// FieldOptions returns the allowed values for a field on a project/issuetype,
// via createmeta. field may be a field id or display name.
func (j *Jira) FieldOptions(ctx context.Context, project, issueType, field string) ([]string, error) {
	q := url.Values{}
	q.Set("projectKeys", project)
	if issueType != "" {
		q.Set("issuetypeNames", issueType)
	}
	q.Set("expand", "projects.issuetypes.fields")
	var resp struct {
		Projects []struct {
			IssueTypes []struct {
				Fields map[string]struct {
					Name          string `json:"name"`
					AllowedValues []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"allowedValues"`
				} `json:"fields"`
			} `json:"issuetypes"`
		} `json:"projects"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/issue/createmeta?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range resp.Projects {
		for _, it := range p.IssueTypes {
			for id, fd := range it.Fields {
				if id == field || fd.Name == field {
					for _, av := range fd.AllowedValues {
						if av.Name != "" {
							out = append(out, av.Name)
						} else if av.Value != "" {
							out = append(out, av.Value)
						}
					}
				}
			}
		}
	}
	return out, nil
}

// Transitions returns the workflow transitions available on an issue now.
func (j *Jira) Transitions(ctx context.Context, key string) ([]domain.TransitionDef, error) {
	var resp struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", &resp); err != nil {
		return nil, err
	}
	out := make([]domain.TransitionDef, 0, len(resp.Transitions))
	for _, t := range resp.Transitions {
		out = append(out, domain.TransitionDef{ID: t.ID, Name: t.Name, To: t.To.Name})
	}
	return out, nil
}

// LinkTypes returns the configured issue link type names.
func (j *Jira) LinkTypes(ctx context.Context) ([]string, error) {
	var resp struct {
		IssueLinkTypes []struct {
			Name string `json:"name"`
		} `json:"issueLinkTypes"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/issueLinkType", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.IssueLinkTypes))
	for _, t := range resp.IssueLinkTypes {
		out = append(out, t.Name)
	}
	return out, nil
}

// ListAttachments returns an issue's attachments.
func (j *Jira) ListAttachments(ctx context.Context, key string) ([]domain.Attachment, error) {
	var d struct {
		Fields struct {
			Attachment []struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
				MimeType string `json:"mimeType"`
				Size     int64  `json:"size"`
				Content  string `json:"content"`
			} `json:"attachment"`
		} `json:"fields"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"?fields=attachment", &d); err != nil {
		return nil, err
	}
	out := make([]domain.Attachment, 0, len(d.Fields.Attachment))
	for _, a := range d.Fields.Attachment {
		out = append(out, domain.Attachment{
			ID: a.ID, Title: a.Filename, MediaType: a.MimeType, FileSize: a.Size, DownPath: a.Content,
		})
	}
	return out, nil
}

// DownloadAttachment fetches attachment bytes by attachment id, returning the
// content type's filename.
func (j *Jira) DownloadAttachment(ctx context.Context, key, attachmentID string) ([]byte, string, error) {
	atts, err := j.ListAttachments(ctx, key)
	if err != nil {
		return nil, "", err
	}
	for _, a := range atts {
		if a.ID == attachmentID || a.Title == attachmentID {
			data, derr := j.c.GetBytes(ctx, a.DownPath)
			return data, a.Title, derr
		}
	}
	return nil, "", domain.ErrNotFound
}
