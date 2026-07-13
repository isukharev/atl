package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
)

var _ domain.IssueWorklogStore = (*Jira)(nil)

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
	startAt := 0
	expectedTotal := -1
	result := &domain.IssueWorklogList{}
	seenIDs := map[string]bool{}
	for page := 0; page < worklogPageGuard; page++ {
		var response struct {
			StartAt  int          `json:"startAt"`
			Total    *int         `json:"total"`
			Worklogs []worklogDTO `json:"worklogs"`
		}
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(startAt))
		query.Set("maxResults", "100")
		path := "/rest/api/2/issue/" + url.PathEscape(key) + "/worklog?" + query.Encode()
		if err := j.c.GetJSON(ctx, path, &response); err != nil {
			return nil, err
		}
		if response.Total == nil {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s omitted total at offset %d", domain.ErrCheckFailed, key, startAt)
		}
		total := *response.Total
		if total < 0 || response.StartAt != startAt {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned invalid pagination at offset %d", domain.ErrCheckFailed, key, startAt)
		}
		if expectedTotal < 0 {
			expectedTotal = total
		} else if total != expectedTotal {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s changed total from %d to %d while paging", domain.ErrCheckFailed, key, expectedTotal, total)
		}
		for _, worklog := range response.Worklogs {
			if worklog.ID == "" || seenIDs[worklog.ID] {
				return nil, fmt.Errorf("%w: Jira worklog listing for %s returned a missing or duplicate worklog id at offset %d", domain.ErrCheckFailed, key, startAt)
			}
			seenIDs[worklog.ID] = true
			result.Worklogs = append(result.Worklogs, mapWorklog(worklog))
		}
		next := response.StartAt + len(response.Worklogs)
		if next > total {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned %d rows through offset %d with total %d", domain.ErrCheckFailed, key, len(response.Worklogs), next, total)
		}
		if next == total {
			result.Total = total
			result.Complete = true
			return result, nil
		}
		if len(response.Worklogs) == 0 {
			return nil, fmt.Errorf("%w: Jira worklog listing for %s returned an empty incomplete page at offset %d", domain.ErrCheckFailed, key, startAt)
		}
		startAt = next
	}
	return nil, fmt.Errorf("%w: Jira worklog listing for %s exceeded the %d-page safety guard", domain.ErrCheckFailed, key, worklogPageGuard)
}

// AddIssueWorklog sends one non-retried POST and explicitly leaves Jira's
// remaining estimate unchanged.
func (j *Jira) AddIssueWorklog(ctx context.Context, key string, input domain.IssueWorklogCreate) (*domain.IssueWorklog, error) {
	query := url.Values{}
	query.Set("adjustEstimate", "leave")
	path := "/rest/api/2/issue/" + url.PathEscape(key) + "/worklog?" + query.Encode()
	payload := struct {
		TimeSpentSeconds int64  `json:"timeSpentSeconds"`
		Comment          string `json:"comment,omitempty"`
		Started          string `json:"started,omitempty"`
	}{TimeSpentSeconds: input.TimeSpentSeconds, Comment: input.Comment, Started: input.Started}
	var response worklogDTO
	if err := j.c.SendJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return nil, err
	}
	mapped := mapWorklog(response)
	return &mapped, nil
}
