package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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

// FieldOptions returns the allowed values for a field on a project/issuetype.
// field may be a field id or display name. issueType may be empty to scan every
// type in the project. It uses the two-step createmeta endpoints
// (/createmeta/{projectKey}/issuetypes then /createmeta/{projectKey}/issuetypes/{id})
// because Jira DC 9.x removed the older expand-based /createmeta query.
func (j *Jira) FieldOptions(ctx context.Context, project, issueType, field string) ([]string, error) {
	base := "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"

	var its struct {
		Values []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"values"`
	}
	if err := j.c.GetJSON(ctx, base+"?maxResults=200", &its); err != nil {
		return nil, err
	}

	// Pick the issue type(s) to inspect: the named one, or all when unspecified.
	var typeIDs []string
	for _, it := range its.Values {
		if issueType == "" || it.Name == issueType {
			typeIDs = append(typeIDs, it.ID)
		}
	}
	if len(typeIDs) == 0 {
		return nil, fmt.Errorf("%w: issue type %q not found in project %q", domain.ErrNotFound, issueType, project)
	}

	var out []string
	seen := map[string]bool{}
	matched := false
	var firstErr error
	okTypes := 0
	for _, tid := range typeIDs {
		var fs struct {
			Values []struct {
				FieldID       string `json:"fieldId"`
				Name          string `json:"name"`
				AllowedValues []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"allowedValues"`
			} `json:"values"`
		}
		if err := j.c.GetJSON(ctx, base+"/"+url.PathEscape(tid)+"?maxResults=200", &fs); err != nil {
			// A named single type was requested: the caller asked for exactly
			// that type, so surface the error. When scanning all types, a
			// restricted or odd type shouldn't sink the whole scan — skip it,
			// remember the error, and keep collecting from healthy types.
			if issueType != "" {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		okTypes++
		for _, fd := range fs.Values {
			if fd.FieldID != field && fd.Name != field {
				continue
			}
			matched = true
			for _, av := range fd.AllowedValues {
				v := av.Name
				if v == "" {
					v = av.Value
				}
				if v != "" && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
	}
	if !matched {
		// Nothing matched. If we couldn't read a single type (e.g. an expired
		// PAT 403s on every detail request), surface that error rather than a
		// misleading "field not found".
		if okTypes == 0 && firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("%w: field %q not found in createmeta for project %q", domain.ErrNotFound, field, project)
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

// DownloadAttachment streams attachment bytes by attachment id (or filename),
// returning the attachment's filename alongside; the caller must Close the
// stream.
func (j *Jira) DownloadAttachment(ctx context.Context, key, attachmentID string) (io.ReadCloser, string, error) {
	atts, err := j.ListAttachments(ctx, key)
	if err != nil {
		return nil, "", err
	}
	for _, a := range atts {
		if a.ID == attachmentID || a.Title == attachmentID {
			rc, derr := j.c.GetStream(ctx, a.DownPath)
			return rc, a.Title, derr
		}
	}
	return nil, "", domain.ErrNotFound
}

// UploadAttachment uploads file bytes as an issue attachment via multipart/form-data.
// Jira DC requires X-Atlassian-Token: no-check and a form field named "file".
func (j *Jira) UploadAttachment(ctx context.Context, key, filename string, data []byte) (*domain.Attachment, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Content-Type":      w.FormDataContentType(),
		"X-Atlassian-Token": "no-check",
	}
	var resp []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		MimeType string `json:"mimeType"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	raw, err := j.c.Do(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/attachments", buf.Bytes(), headers)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("upload attachment: decode response: %w", err)
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("upload attachment: empty response")
	}
	a := resp[0]
	return &domain.Attachment{
		ID: a.ID, Title: a.Filename, MediaType: a.MimeType, FileSize: a.Size, DownPath: a.Content,
	}, nil
}
