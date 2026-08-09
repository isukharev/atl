package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
)

var (
	_ domain.IssueWorklogReader = (*Jira)(nil)
	_ domain.IssueWorklogWriter = (*Jira)(nil)
	_ domain.IssueWorklogStore  = (*Jira)(nil)
)

const worklogPageGuard = 100

type worklogDTO struct {
	ID      string `json:"id"`
	IssueID string `json:"issueId"`
	Author  struct {
		Name        string `json:"name"`
		Key         string `json:"key"`
		DisplayName string `json:"displayName"`
		Active      bool   `json:"active"`
	} `json:"author"`
	Comment          string `json:"comment"`
	Started          string `json:"started"`
	Created          string `json:"created"`
	Updated          string `json:"updated"`
	TimeSpent        string `json:"timeSpent"`
	TimeSpentSeconds int64  `json:"timeSpentSeconds"`
}

func mapWorklog(input worklogDTO) domain.IssueWorklog {
	return domain.IssueWorklog{
		ID: input.ID, IssueID: input.IssueID, Comment: input.Comment,
		Started: input.Started, Created: input.Created, Updated: input.Updated,
		TimeSpent: input.TimeSpent, TimeSpentSeconds: input.TimeSpentSeconds,
		Author: domain.IssueWorklogAuthor{
			Name: input.Author.Name, Key: input.Author.Key,
			DisplayName: input.Author.DisplayName, Active: input.Author.Active,
		},
	}
}

// ListIssueWorklogs consumes every page advertised by Jira. A missing/changing
// total, offset mismatch, empty incomplete page, or page-guard hit fails closed.
func (j *Jira) ListIssueWorklogs(ctx context.Context, key string) (*domain.IssueWorklogList, error) {
	cursor := jiraOffsetCursor{}
	expectedTotal := -1
	result := &domain.IssueWorklogList{Worklogs: []domain.IssueWorklog{}}
	seenIDs := map[string]bool{}
	for page := 0; page < worklogPageGuard; page++ {
		var response struct {
			StartAt  *int         `json:"startAt"`
			Total    *int         `json:"total"`
			Worklogs []worklogDTO `json:"worklogs"`
		}
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(cursor.requested()))
		query.Set("maxResults", "100")
		path := "/rest/api/2/issue/" + url.PathEscape(key) + "/worklog?" + query.Encode()
		if err := j.c.GetJSON(ctx, path, &response); err != nil {
			return nil, err
		}
		if response.Total == nil {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s omitted total at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		}
		total := *response.Total
		if response.StartAt == nil {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s omitted startAt at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		}
		if total < 0 || !cursor.matches(*response.StartAt) {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned invalid pagination at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		}
		if response.Worklogs == nil {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s omitted or nullified worklogs at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		}
		if expectedTotal < 0 {
			expectedTotal = total
		} else if total != expectedTotal {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s changed total from %d to %d while paging", domain.ErrCheckFailed, key, expectedTotal, total)
		}
		for _, worklog := range response.Worklogs {
			if worklog.ID == "" || seenIDs[worklog.ID] {
				return nil, fmt.Errorf("%w: Jira worklog listing for %s returned a missing or duplicate worklog id at offset %d", domain.ErrCheckFailed, key, cursor.requested())
			}
			seenIDs[worklog.ID] = true
			result.Worklogs = append(result.Worklogs, mapWorklog(worklog))
		}
		decision := cursor.advance(len(response.Worklogs), &total)
		switch decision.state {
		case jiraOffsetBeyondTotal:
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned %d rows through offset %d with total %d", domain.ErrCheckFailed, key, len(response.Worklogs), decision.next, total)
		case jiraOffsetComplete:
			result.Total = total
			result.Complete = true
			return result, nil
		case jiraOffsetStalled:
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned an empty incomplete page at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		case jiraOffsetOverflow:
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned invalid pagination at offset %d", domain.ErrCheckFailed, key, cursor.requested())
		}
	}
	return nil, fmt.Errorf("%w: Jira worklog listing for %s exceeded the %d-page safety guard", domain.ErrCheckFailed, key, worklogPageGuard)
}

// AddIssueWorklog sends one non-retried POST and explicitly leaves Jira's
// remaining estimate unchanged.
func (j *Jira) AddIssueWorklog(ctx context.Context, key string, input domain.IssueWorklogCreate) (*domain.IssueWorklog, error) {
	var authorizeErr error
	ctx, authorizeErr = j.authorizeIssues(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, "worklog", key)
	if authorizeErr != nil {
		return nil, authorizeErr
	}
	query := url.Values{}
	query.Set("adjustEstimate", "leave")
	path := "/rest/api/2/issue/" + url.PathEscape(key) + "/worklog?" + query.Encode()
	payload := struct {
		TimeSpentSeconds int64  `json:"timeSpentSeconds"`
		Comment          string `json:"comment,omitempty"`
		Started          string `json:"started,omitempty"`
	}{TimeSpentSeconds: input.TimeSpentSeconds, Comment: input.Comment, Started: input.Started}
	var response worklogDTO
	if err := j.c.SendJSON(domain.WithWriteClearance(ctx), http.MethodPost, path, payload, &response); err != nil {
		return nil, err
	}
	mapped := mapWorklog(response)
	return &mapped, nil
}
