package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const JiraWatcherUsernameCap = 255

type JiraWatcherListResult struct {
	Key        string                `json:"key"`
	WatchCount int                   `json:"watch_count"`
	IsWatching bool                  `json:"is_watching"`
	Watchers   []domain.IssueWatcher `json:"watchers"`
	Complete   bool                  `json:"complete"`
	Truncated  bool                  `json:"truncated,omitempty"`
}

type JiraWatcherMutationOpts struct {
	Operation            string
	Username             string
	Me                   bool
	ExpectedProposalHash string
	Apply                bool
}

type JiraWatcherMutationResult struct {
	Key            string                `json:"key"`
	Operation      string                `json:"operation"`
	Mode           string                `json:"mode"`
	Status         string                `json:"status"`
	Username       string                `json:"username"`
	IdentitySource string                `json:"identity_source"`
	Current        []domain.IssueWatcher `json:"current"`
	Final          []domain.IssueWatcher `json:"final,omitempty"`
	ProposalHash   string                `json:"proposal_hash"`
	Complete       bool                  `json:"complete"`
	Reconciled     bool                  `json:"reconciled,omitempty"`
}

type jiraWatcherWriteError struct {
	message string
	cause   error
}

func (e *jiraWatcherWriteError) Error() string { return e.message }
func (e *jiraWatcherWriteError) Unwrap() error { return e.cause }

func (s *JiraService) issueWatcherStore() (domain.IssueWatcherStore, error) {
	store, ok := s.tr.(domain.IssueWatcherStore)
	if !ok || store == nil {
		return nil, fmt.Errorf("%w: configured tracker does not support issue watchers", domain.ErrCheckFailed)
	}
	return store, nil
}

func (s *JiraService) ListWatchers(ctx context.Context, key string) (*JiraWatcherListResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	store, err := s.issueWatcherStore()
	if err != nil {
		return nil, err
	}
	watchers, err := store.ListIssueWatchers(ctx, key)
	if err != nil {
		return nil, err
	}
	if watchers == nil {
		return nil, fmt.Errorf("%w: watcher endpoint returned no result", domain.ErrCheckFailed)
	}
	return &JiraWatcherListResult{
		Key: key, WatchCount: watchers.WatchCount, IsWatching: watchers.IsWatching,
		Watchers: sortedIssueWatchers(watchers.Watchers), Complete: watchers.Complete, Truncated: watchers.Truncated,
	}, nil
}

func (s *JiraService) MutateWatcherGuarded(ctx context.Context, key string, opts JiraWatcherMutationOpts) (*JiraWatcherMutationResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	operation := strings.TrimSpace(opts.Operation)
	if operation != "add" && operation != "remove" {
		return nil, fmt.Errorf("%w: watcher operation must be add or remove", domain.ErrUsage)
	}
	identityChoices := 0
	if strings.TrimSpace(opts.Username) != "" {
		identityChoices++
	}
	if opts.Me {
		identityChoices++
	}
	if identityChoices != 1 {
		return nil, fmt.Errorf("%w: pass exactly one of --username or --me", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	username := opts.Username
	identitySource := "username"
	if opts.Me {
		currentUser, err := s.tr.CurrentUser(ctx)
		if err != nil {
			return nil, err
		}
		if currentUser == nil || strings.TrimSpace(currentUser.Name) == "" {
			return nil, fmt.Errorf("%w: current Jira Data Center user has no username", domain.ErrCheckFailed)
		}
		username = currentUser.Name
		identitySource = "me"
	}
	username, err := normalizeJiraWatcherUsername(username)
	if err != nil {
		return nil, err
	}
	store, err := s.issueWatcherStore()
	if err != nil {
		return nil, err
	}
	currentState, err := store.ListIssueWatchers(ctx, key)
	if err != nil {
		return nil, err
	}
	if currentState == nil || !currentState.Complete {
		return nil, fmt.Errorf("%w: Jira returned an incomplete watcher identity list; refusing a membership mutation", domain.ErrCheckFailed)
	}
	current := sortedIssueWatchers(currentState.Watchers)
	proposalHash := jiraWatcherProposalHash(key, operation, username, current)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraWatcherMutationResult{
		Key: key, Operation: operation, Mode: mode, Status: "would_apply", Username: username,
		IdentitySource: identitySource, Current: current, ProposalHash: proposalHash, Complete: true,
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: watcher proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	if jiraWatcherGoalSatisfied(operation, username, current) {
		result.Status = "already_satisfied"
		result.Final = current
		return result, nil
	}
	if !opts.Apply {
		return result, nil
	}

	var writeErr error
	if operation == "add" {
		writeErr = store.AddIssueWatcher(ctx, key, username)
	} else {
		writeErr = store.RemoveIssueWatcher(ctx, key, username)
	}
	verifiedState, verifyErr := store.ListIssueWatchers(ctx, key)
	if verifyErr != nil || verifiedState == nil || !verifiedState.Complete {
		result.Status = "unknown"
		cause := writeErr
		if cause == nil {
			cause = verifyErr
		}
		if cause == nil {
			cause = fmt.Errorf("verification watcher list was incomplete")
		}
		return result, &jiraWatcherWriteError{message: "watcher update outcome is unknown; verification read was unavailable or incomplete; do not replay automatically", cause: cause}
	}
	result.Final = sortedIssueWatchers(verifiedState.Watchers)
	result.Reconciled = writeErr != nil
	if jiraWatcherGoalSatisfied(operation, username, result.Final) {
		result.Status = "applied"
		return result, nil
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "failed"
		return result, &jiraWatcherWriteError{message: "Jira rejected the watcher update", cause: writeErr}
	}
	result.Status = "unknown"
	cause := writeErr
	if cause == nil {
		cause = fmt.Errorf("verified watcher membership differs from the reviewed proposal")
	}
	return result, &jiraWatcherWriteError{message: "watcher update outcome is unknown; verified state differs from the reviewed proposal; do not replay automatically", cause: cause}
}

func normalizeJiraWatcherUsername(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: username is not valid UTF-8", domain.ErrUsage)
	}
	username := strings.TrimSpace(raw)
	if username == "" {
		return "", fmt.Errorf("%w: username must not be empty", domain.ErrUsage)
	}
	if len([]byte(username)) > JiraWatcherUsernameCap {
		return "", fmt.Errorf("%w: username exceeds %d bytes", domain.ErrUsage, JiraWatcherUsernameCap)
	}
	for _, char := range username {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return "", fmt.Errorf("%w: username must not contain control or invisible format characters", domain.ErrUsage)
		}
	}
	return username, nil
}

func sortedIssueWatchers(watchers []domain.IssueWatcher) []domain.IssueWatcher {
	out := append([]domain.IssueWatcher(nil), watchers...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func jiraWatcherGoalSatisfied(operation, username string, watchers []domain.IssueWatcher) bool {
	present := false
	for _, watcher := range watchers {
		if watcher.Name == username {
			present = true
			break
		}
	}
	if operation == "add" {
		return present
	}
	return !present
}

func jiraWatcherProposalHash(key, operation, username string, watchers []domain.IssueWatcher) string {
	names := make([]string, 0, len(watchers))
	for _, watcher := range watchers {
		names = append(names, watcher.Name)
	}
	sort.Strings(names)
	canonical, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		Key           string   `json:"key"`
		Operation     string   `json:"operation"`
		Username      string   `json:"username"`
		Current       []string `json:"current"`
	}{SchemaVersion: 1, Key: key, Operation: operation, Username: username, Current: names})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
