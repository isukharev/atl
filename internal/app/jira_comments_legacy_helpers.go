package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// JiraCommentActor remains the broad transition-comment projection. The
// standalone guarded comment workflow uses only digests of its separate strict
// domain actor evidence.
type JiraCommentActor struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

func (s *JiraService) jiraCommentActor(ctx context.Context) (JiraCommentActor, error) {
	user, err := s.tr.CurrentUser(ctx)
	if err != nil {
		return JiraCommentActor{}, err
	}
	if user == nil || strings.TrimSpace(user.Name) == "" {
		return JiraCommentActor{}, fmt.Errorf("%w: current Jira Data Center user has no stable username", domain.ErrCheckFailed)
	}
	return JiraCommentActor{Name: strings.TrimSpace(user.Name), Key: strings.TrimSpace(user.Key)}, nil
}

func (s *JiraService) jiraCommentBaseline(ctx context.Context, key string) ([]domain.Comment, []string, string, error) {
	comments, err := s.tr.ListComments(ctx, key)
	if err != nil {
		return nil, nil, "", err
	}
	return normalizeJiraCommentBaseline(comments)
}

func normalizeJiraCommentBaseline(comments []domain.Comment) ([]domain.Comment, []string, string, error) {
	sorted := append([]domain.Comment(nil), comments...)
	seen := make(map[string]bool, len(sorted))
	for _, comment := range sorted {
		id := strings.TrimSpace(comment.ID)
		if id == "" || id != comment.ID || seen[id] {
			return nil, nil, "", fmt.Errorf("%w: Jira returned a comment baseline with a missing or duplicate identity", domain.ErrCheckFailed)
		}
		seen[id] = true
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	ids := make([]string, len(sorted))
	for i := range sorted {
		ids[i] = sorted[i].ID
	}
	canonical, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		IDs           []string `json:"ids"`
	}{1, ids})
	sum := sha256.Sum256(canonical)
	return sorted, ids, hex.EncodeToString(sum[:]), nil
}

func jiraCommentMatches(comment domain.Comment, body []byte, actor JiraCommentActor) bool {
	if comment.Body != string(body) || comment.AuthorName != actor.Name {
		return false
	}
	return actor.Key == "" || comment.AuthorKey == actor.Key
}

func changedCommentBaselineMember(before, after []domain.Comment) string {
	afterByID := make(map[string]domain.Comment, len(after))
	for _, comment := range after {
		afterByID[comment.ID] = comment
	}
	for _, prior := range before {
		current, ok := afterByID[prior.ID]
		if !ok {
			return fmt.Sprintf("baseline comment %s is missing", prior.ID)
		}
		if prior.Body != current.Body || prior.AuthorName != current.AuthorName || prior.AuthorKey != current.AuthorKey || prior.Created != current.Created {
			return fmt.Sprintf("baseline comment %s changed", prior.ID)
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
