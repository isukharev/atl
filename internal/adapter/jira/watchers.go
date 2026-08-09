package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/isukharev/atl/internal/domain"
)

var (
	_ domain.IssueWatcherReader    = (*Jira)(nil)
	_ domain.IssueWatcherWriter    = (*Jira)(nil)
	_ domain.IssueWatcherStore     = (*Jira)(nil)
	_ domain.JiraCurrentUserReader = (*Jira)(nil)
)

func (j *Jira) ListIssueWatchers(ctx context.Context, key string) (*domain.IssueWatcherList, error) {
	var response struct {
		WatchCount int        `json:"watchCount"`
		IsWatching bool       `json:"isWatching"`
		Watchers   *[]userDTO `json:"watchers"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(key) + "/watchers"
	if err := j.c.GetJSON(ctx, path, &response); err != nil {
		return nil, err
	}
	result := &domain.IssueWatcherList{WatchCount: response.WatchCount, IsWatching: response.IsWatching}
	identitiesPresent := response.Watchers != nil
	if response.Watchers != nil {
		for _, watcher := range *response.Watchers {
			result.Watchers = append(result.Watchers, domain.IssueWatcher{
				Name: watcher.Name, Key: watcher.Key, DisplayName: watcher.DisplayName, Active: watcher.Active,
			})
			if watcher.Name == "" {
				identitiesPresent = false
			}
		}
	}
	result.Complete = response.WatchCount == 0 || (identitiesPresent && response.WatchCount == len(result.Watchers))
	result.Truncated = !result.Complete
	return result, nil
}

// AddIssueWatcher sends Jira DC's required raw JSON string body exactly once.
func (j *Jira) AddIssueWatcher(ctx context.Context, key, username string) error {
	var authorizeErr error
	ctx, authorizeErr = j.authorizeIssues(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, "watcher", key)
	if authorizeErr != nil {
		return authorizeErr
	}
	body, err := json.Marshal(username)
	if err != nil {
		return err
	}
	_, err = j.c.Do(domain.WithWriteClearance(ctx), http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(key)+"/watchers", body, map[string]string{"Content-Type": "application/json"})
	return err
}

func (j *Jira) RemoveIssueWatcher(ctx context.Context, key, username string) error {
	var err error
	ctx, err = j.authorizeIssues(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, "watcher", key)
	if err != nil {
		return err
	}
	query := url.Values{}
	query.Set("username", username)
	_, err = j.c.Do(domain.WithWriteClearance(ctx), http.MethodDelete, "/rest/api/2/issue/"+url.PathEscape(key)+"/watchers?"+query.Encode(), nil, nil)
	return err
}
