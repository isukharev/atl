// Package jira implements domain.Tracker against a Jira Server/Data Center REST
// v2 API with bearer-PAT auth. Issue bodies are native Jira wiki markup; the
// adapter does not convert them.
package jira

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// Jira is the Tracker adapter.
type Jira struct {
	c    *httpx.Client
	base string
}

// New builds a Jira adapter for base URL with a PAT.
func New(base, token, version string) *Jira {
	return &Jira{c: httpx.New(base, token, version), base: strings.TrimRight(base, "/")}
}

var _ domain.Tracker = (*Jira)(nil)

const defaultFields = "summary,description,status,issuetype,project,assignee,reporter,labels,issuelinks,comment,attachment"

// --- DTOs ---

type issueDTO struct {
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields"`
}

func (j *Jira) mapIssue(d issueDTO) *domain.Issue {
	is := &domain.Issue{Key: d.Key, Fields: d.Fields, Raw: d.Fields, FieldText: map[string]string{}}
	f := d.Fields
	is.Summary = str(f["summary"])
	is.Body = str(f["description"])
	is.Status = nestedName(f["status"])
	is.Type = nestedName(f["issuetype"])
	is.Project = nestedKey(f["project"])
	is.Assignee = nestedDisplay(f["assignee"])
	is.Reporter = nestedDisplay(f["reporter"])
	if labels, ok := f["labels"].([]any); ok {
		for _, l := range labels {
			is.Labels = append(is.Labels, str(l))
		}
	}
	// Links.
	if links, ok := f["issuelinks"].([]any); ok {
		for _, raw := range links {
			lm, _ := raw.(map[string]any) // nil map on mismatch reads as zero — safe
			if iw, ok := lm["inwardIssue"].(map[string]any); ok {
				is.Links = append(is.Links, domain.IssueLink{Type: str(typeField(lm["type"], "inward")), Direction: "inward", Key: str(iw["key"])})
			}
			if ow, ok := lm["outwardIssue"].(map[string]any); ok {
				is.Links = append(is.Links, domain.IssueLink{Type: str(typeField(lm["type"], "outward")), Direction: "outward", Key: str(ow["key"])})
			}
		}
	}
	// Comments.
	if cm, ok := f["comment"].(map[string]any); ok {
		if arr, ok := cm["comments"].([]any); ok {
			for _, raw := range arr {
				c, _ := raw.(map[string]any)
				is.Comments = append(is.Comments, domain.Comment{
					ID: str(c["id"]), Author: nestedDisplay(c["author"]),
					Created: str(c["created"]), Body: str(c["body"]),
				})
			}
		}
	}
	return is
}

// GetIssue fetches one issue. If fields is empty a sensible default set is used.
func (j *Jira) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	fq := defaultFields
	if len(fields) > 0 {
		fq = strings.Join(fields, ",")
	}
	var d issueDTO
	if err := j.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"?fields="+url.QueryEscape(fq), &d); err != nil {
		return nil, err
	}
	return j.mapIssue(d), nil
}

// Search runs JQL. cursor is the startAt offset; returns the next offset or "".
func (j *Jira) Search(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	startAt, _ := strconv.Atoi(cursor)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	fq := "summary,status,issuetype,project,assignee,labels"
	if len(fields) > 0 {
		fq = strings.Join(fields, ",")
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(limit))
	q.Set("fields", fq)
	var resp struct {
		Issues     []issueDTO `json:"issues"`
		StartAt    int        `json:"startAt"`
		MaxResults int        `json:"maxResults"`
		Total      int        `json:"total"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/search?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Issue, 0, len(resp.Issues))
	for _, d := range resp.Issues {
		out = append(out, *j.mapIssue(d))
	}
	next := ""
	if startAt+len(resp.Issues) < resp.Total && len(resp.Issues) > 0 {
		next = strconv.Itoa(startAt + len(resp.Issues))
	}
	return out, next, nil
}

// Create creates an issue. fields are extra string fields set verbatim.
func (j *Jira) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string) (*domain.Issue, error) {
	fl := map[string]any{
		"project":   map[string]string{"key": project},
		"issuetype": map[string]string{"name": issueType},
		"summary":   summary,
	}
	if len(body) > 0 {
		fl["description"] = string(body)
	}
	for k, v := range fields {
		fl[k] = v
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := j.c.SendJSON(ctx, "POST", "/rest/api/2/issue", map[string]any{"fields": fl}, &out); err != nil {
		return nil, err
	}
	return &domain.Issue{Key: out.Key, Summary: summary, Project: project, Type: issueType, Body: string(body)}, nil
}

// Update edits summary/description/extra fields.
func (j *Jira) Update(ctx context.Context, key, summary string, body []byte, fields map[string]string) error {
	fl := map[string]any{}
	if summary != "" {
		fl["summary"] = summary
	}
	if body != nil {
		fl["description"] = string(body)
	}
	for k, v := range fields {
		fl[k] = v
	}
	if len(fl) == 0 {
		return fmt.Errorf("%w: nothing to update", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key), map[string]any{"fields": fl}, nil)
}

// Transition moves an issue to a target status by name, optionally commenting.
func (j *Jira) Transition(ctx context.Context, key, to, comment string) error {
	trs, err := j.Transitions(ctx, key)
	if err != nil {
		return err
	}
	var id string
	for _, t := range trs {
		if strings.EqualFold(t.Name, to) || strings.EqualFold(t.To, to) {
			id = t.ID
			break
		}
	}
	if id == "" {
		names := make([]string, len(trs))
		for i, t := range trs {
			names[i] = t.Name
		}
		return fmt.Errorf("%w: no transition to %q; available: %s", domain.ErrUsage, to, strings.Join(names, ", "))
	}
	payload := map[string]any{"transition": map[string]string{"id": id}}
	if comment != "" {
		payload["update"] = map[string]any{"comment": []any{map[string]any{"add": map[string]string{"body": comment}}}}
	}
	return j.c.SendJSON(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", payload, nil)
}

// AddComment posts a wiki-markup comment.
func (j *Jira) AddComment(ctx context.Context, key string, body []byte) (*domain.Comment, error) {
	var out struct {
		ID      string `json:"id"`
		Created string `json:"created"`
		Author  struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
	}
	if err := j.c.SendJSON(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/comment",
		map[string]string{"body": string(body)}, &out); err != nil {
		return nil, err
	}
	return &domain.Comment{ID: out.ID, Author: out.Author.DisplayName, Created: out.Created, Body: string(body)}, nil
}

// Link creates a typed link from→to.
func (j *Jira) Link(ctx context.Context, from, to, linkType string) error {
	payload := map[string]any{
		"type":         map[string]string{"name": linkType},
		"inwardIssue":  map[string]string{"key": to},
		"outwardIssue": map[string]string{"key": from},
	}
	return j.c.SendJSON(ctx, "POST", "/rest/api/2/issueLink", payload, nil)
}

// LinkEpic sets the Epic Link field (DC classic) on an issue.
func (j *Jira) LinkEpic(ctx context.Context, issue, epic string) error {
	fields, err := j.Fields(ctx)
	if err != nil {
		return err
	}
	var epicField string
	for _, f := range fields {
		if strings.EqualFold(f.Name, "Epic Link") {
			epicField = f.ID
			break
		}
	}
	if epicField == "" {
		return fmt.Errorf("%w: no 'Epic Link' field on this Jira (team-managed projects use the parent field)", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(issue),
		map[string]any{"fields": map[string]any{epicField: epic}}, nil)
}

// --- small helpers for untyped field access ---

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func nestedName(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["name"])
	}
	return ""
}

func nestedKey(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["key"])
	}
	return ""
}

func nestedDisplay(v any) string {
	if m, ok := v.(map[string]any); ok {
		if d := str(m["displayName"]); d != "" {
			return d
		}
		return str(m["name"])
	}
	return ""
}

func typeField(v any, field string) any {
	if m, ok := v.(map[string]any); ok {
		if s := str(m[field]); s != "" {
			return s
		}
		return str(m["name"])
	}
	return ""
}
