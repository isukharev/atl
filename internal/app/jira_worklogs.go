package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const JiraWorklogCommentMaxBytes = 1 << 20

type JiraWorklogListResult struct {
	Key      string                `json:"key"`
	Worklogs []domain.IssueWorklog `json:"worklogs"`
	Total    int                   `json:"total"`
	Complete bool                  `json:"complete"`
}

type JiraWorklogAddOpts struct {
	Time                 string
	Comment              string
	Started              string
	Apply                bool
	ExpectedProposalHash string
}

type JiraWorklogAddResult struct {
	Key              string                    `json:"key"`
	Mode             string                    `json:"mode"`
	Status           string                    `json:"status"`
	TimeSpent        string                    `json:"time_spent"`
	TimeSpentSeconds int64                     `json:"time_spent_seconds"`
	Comment          string                    `json:"comment,omitempty"`
	Started          string                    `json:"started,omitempty"`
	Author           domain.IssueWorklogAuthor `json:"author"`
	CurrentCount     int                       `json:"current_count"`
	ProposalHash     string                    `json:"proposal_hash"`
	Created          *domain.IssueWorklog      `json:"created,omitempty"`
	Complete         bool                      `json:"complete"`
	Reconciled       bool                      `json:"reconciled,omitempty"`
}

type jiraWorklogWriteError struct {
	message string
	cause   error
}

func (e *jiraWorklogWriteError) Error() string { return e.message }
func (e *jiraWorklogWriteError) Unwrap() error { return e.cause }

func (s *JiraService) issueWorklogStore() (domain.IssueWorklogStore, error) {
	store, ok := s.tr.(domain.IssueWorklogStore)
	if !ok || store == nil {
		return nil, fmt.Errorf("%w: configured tracker does not support issue worklogs", domain.ErrCheckFailed)
	}
	return store, nil
}

func (s *JiraService) ListWorklogs(ctx context.Context, key string) (*JiraWorklogListResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	store, err := s.issueWorklogStore()
	if err != nil {
		return nil, err
	}
	listed, err := store.ListIssueWorklogs(ctx, key)
	if err != nil {
		return nil, err
	}
	if listed == nil || !listed.Complete || listed.Total != len(listed.Worklogs) {
		return nil, fmt.Errorf("%w: Jira returned an incomplete worklog list", domain.ErrCheckFailed)
	}
	return &JiraWorklogListResult{
		Key: key, Worklogs: sortedIssueWorklogs(listed.Worklogs),
		Total: listed.Total, Complete: true,
	}, nil
}

func (s *JiraService) AddWorklogGuarded(ctx context.Context, key string, opts JiraWorklogAddOpts) (*JiraWorklogAddResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	seconds, display, err := NormalizeJiraWorklogDuration(opts.Time)
	if err != nil {
		return nil, err
	}
	started, err := NormalizeJiraWorklogStarted(opts.Started)
	if err != nil {
		return nil, err
	}
	comment, err := ValidateJiraWorklogComment(opts.Comment)
	if err != nil {
		return nil, err
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	store, err := s.issueWorklogStore()
	if err != nil {
		return nil, err
	}
	currentUser, err := s.tr.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	author := compactWorklogAuthor(currentUser)
	current, err := store.ListIssueWorklogs(ctx, key)
	if err != nil {
		return nil, err
	}
	if current == nil || !current.Complete || current.Total != len(current.Worklogs) {
		return nil, fmt.Errorf("%w: Jira returned an incomplete worklog baseline; refusing a non-idempotent add", domain.ErrCheckFailed)
	}
	proposalHash := jiraWorklogProposalHash(key, seconds, comment, started, author)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraWorklogAddResult{
		Key: key, Mode: mode, Status: "would_apply", TimeSpent: display,
		TimeSpentSeconds: seconds, Comment: comment, Started: started, Author: author,
		CurrentCount: current.Total, ProposalHash: proposalHash, Complete: true,
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: worklog proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	if !opts.Apply {
		return result, nil
	}

	input := domain.IssueWorklogCreate{TimeSpentSeconds: seconds, Comment: comment, Started: started}
	created, writeErr := store.AddIssueWorklog(ctx, key, input)
	if writeErr == nil && created != nil && strings.TrimSpace(created.ID) != "" {
		result.Status = "applied"
		result.Created = created
		return result, nil
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "failed"
		return result, &jiraWorklogWriteError{message: "Jira rejected the worklog add", cause: writeErr}
	}

	verified, verifyErr := store.ListIssueWorklogs(ctx, key)
	if verifyErr != nil || verified == nil || !verified.Complete || verified.Total != len(verified.Worklogs) {
		result.Status = "unknown"
		cause := writeErr
		if cause == nil {
			cause = verifyErr
		}
		if cause == nil {
			cause = fmt.Errorf("verification worklog list was incomplete")
		}
		return result, &jiraWorklogWriteError{message: "worklog add outcome is unknown; verification was unavailable or incomplete; do not replay automatically", cause: cause}
	}
	baselineIDs := make(map[string]bool, len(current.Worklogs))
	for _, worklog := range current.Worklogs {
		baselineIDs[worklog.ID] = true
	}
	var matches []domain.IssueWorklog
	for _, worklog := range verified.Worklogs {
		if !baselineIDs[worklog.ID] && jiraWorklogMatches(worklog, input, author) {
			matches = append(matches, worklog)
		}
	}
	result.Reconciled = true
	if len(matches) == 1 {
		result.Status = "applied"
		result.Created = &matches[0]
		return result, nil
	}
	result.Status = "unknown"
	cause := writeErr
	if cause == nil {
		cause = fmt.Errorf("worklog response omitted a stable id")
	}
	return result, &jiraWorklogWriteError{message: fmt.Sprintf("worklog add outcome is unknown; reconciliation found %d exact new matches; do not replay automatically", len(matches)), cause: cause}
}

// NormalizeJiraWorklogDuration accepts positive integer h/m/s segments such as
// 1h30m or 90m. Days/weeks are intentionally excluded because their seconds
// depend on instance-specific Jira time-tracking settings.
func NormalizeJiraWorklogDuration(raw string) (seconds int64, display string, err error) {
	if !utf8.ValidString(raw) {
		return 0, "", fmt.Errorf("%w: --time is not valid UTF-8", domain.ErrUsage)
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, "", fmt.Errorf("%w: --time is required (use positive h/m/s segments such as 1h30m)", domain.ErrUsage)
	}
	for offset := 0; offset < len(value); {
		for offset < len(value) {
			r, size := utf8.DecodeRuneInString(value[offset:])
			if !unicode.IsSpace(r) {
				break
			}
			offset += size
		}
		start := offset
		for offset < len(value) && value[offset] >= '0' && value[offset] <= '9' {
			offset++
		}
		if start == offset {
			return 0, "", fmt.Errorf("%w: invalid --time %q; use positive integer h/m/s segments such as 1h30m", domain.ErrUsage, raw)
		}
		amount, parseErr := strconv.ParseInt(value[start:offset], 10, 64)
		if parseErr != nil {
			return 0, "", fmt.Errorf("%w: --time value overflows", domain.ErrUsage)
		}
		for offset < len(value) {
			r, size := utf8.DecodeRuneInString(value[offset:])
			if !unicode.IsSpace(r) {
				break
			}
			offset += size
		}
		if offset >= len(value) {
			return 0, "", fmt.Errorf("%w: --time segment is missing h, m, or s", domain.ErrUsage)
		}
		unit := value[offset]
		offset++
		var multiplier int64
		switch unit {
		case 'h', 'H':
			multiplier = 3600
		case 'm', 'M':
			multiplier = 60
		case 's', 'S':
			multiplier = 1
		default:
			return 0, "", fmt.Errorf("%w: unsupported --time unit %q; use h, m, or s", domain.ErrUsage, string(unit))
		}
		if amount > math.MaxInt64/multiplier {
			return 0, "", fmt.Errorf("%w: --time value overflows", domain.ErrUsage)
		}
		segment := amount * multiplier
		if seconds > math.MaxInt64-segment {
			return 0, "", fmt.Errorf("%w: --time value overflows", domain.ErrUsage)
		}
		seconds += segment
	}
	if seconds <= 0 {
		return 0, "", fmt.Errorf("%w: --time must be greater than zero", domain.ErrUsage)
	}
	return seconds, jiraWorklogDurationDisplay(seconds), nil
}

func jiraWorklogDurationDisplay(seconds int64) string {
	parts := make([]string, 0, 3)
	if hours := seconds / 3600; hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	seconds %= 3600
	if minutes := seconds / 60; minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds %= 60; seconds > 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return strings.Join(parts, " ")
}

func NormalizeJiraWorklogStarted(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("%w: --started must be RFC3339 with an explicit timezone: %v", domain.ErrUsage, err)
	}
	return parsed.Format("2006-01-02T15:04:05.000-0700"), nil
}

func ValidateJiraWorklogComment(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("%w: worklog comment is not valid UTF-8", domain.ErrUsage)
	}
	if len([]byte(raw)) > JiraWorklogCommentMaxBytes {
		return "", fmt.Errorf("%w: worklog comment exceeds %d MiB", domain.ErrUsage, JiraWorklogCommentMaxBytes>>20)
	}
	return raw, nil
}

func compactWorklogAuthor(user *domain.User) domain.IssueWorklogAuthor {
	if user == nil {
		return domain.IssueWorklogAuthor{}
	}
	return domain.IssueWorklogAuthor{Name: user.Name, Key: user.Key, DisplayName: user.DisplayName, Active: user.Active}
}

func sortedIssueWorklogs(worklogs []domain.IssueWorklog) []domain.IssueWorklog {
	out := make([]domain.IssueWorklog, len(worklogs))
	copy(out, worklogs)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started < out[j].Started
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func jiraWorklogProposalHash(key string, seconds int64, comment, started string, author domain.IssueWorklogAuthor) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Key           string `json:"key"`
		Seconds       int64  `json:"seconds"`
		Comment       string `json:"comment"`
		Started       string `json:"started"`
		AuthorName    string `json:"author_name"`
		AuthorKey     string `json:"author_key"`
	}{1, key, seconds, comment, started, author.Name, author.Key})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func jiraWorklogMatches(worklog domain.IssueWorklog, input domain.IssueWorklogCreate, author domain.IssueWorklogAuthor) bool {
	if worklog.TimeSpentSeconds != input.TimeSpentSeconds || worklog.Comment != input.Comment {
		return false
	}
	if author.Name == "" && author.Key == "" {
		return false
	}
	if author.Name != "" && worklog.Author.Name != author.Name {
		return false
	}
	if author.Name == "" && author.Key != "" && worklog.Author.Key != author.Key {
		return false
	}
	if input.Started == "" {
		// Jira chooses this timestamp server-side, so the request has no stable
		// value that can distinguish our write from a concurrent identical one.
		return false
	}
	want, err := time.Parse("2006-01-02T15:04:05.000-0700", input.Started)
	if err != nil {
		return false
	}
	got, err := time.Parse("2006-01-02T15:04:05.000-0700", worklog.Started)
	return err == nil && got.Equal(want)
}

func JiraWorklogListMarkdown(result *JiraWorklogListResult) string {
	rows := make([][]string, 0, len(result.Worklogs))
	for _, worklog := range result.Worklogs {
		duration := worklog.TimeSpent
		if strings.TrimSpace(duration) == "" {
			duration = jiraWorklogDurationDisplay(worklog.TimeSpentSeconds)
		}
		rows = append(rows, []string{
			worklog.ID, duration, renderTemporalField(worklog.Started, "datetime"),
			jiraWorklogAuthorLabel(worklog.Author), worklog.Comment,
		})
	}
	return MarkdownTable([]string{"ID", "Time", "Started", "Author", "Comment"}, rows)
}

func JiraWorklogAddMarkdown(result *JiraWorklogAddResult) string {
	createdID := ""
	if result.Created != nil {
		createdID = result.Created.ID
	}
	return MarkdownTable(
		[]string{"Status", "Issue", "Time", "Started", "Author", "Proposal Hash", "Worklog ID"},
		[][]string{{
			result.Status, result.Key, result.TimeSpent, renderTemporalField(result.Started, "datetime"),
			jiraWorklogAuthorLabel(result.Author), result.ProposalHash, createdID,
		}},
	)
}

func jiraWorklogAuthorLabel(author domain.IssueWorklogAuthor) string {
	for _, value := range []string{author.DisplayName, author.Name, author.Key} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
