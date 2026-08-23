package domain

import (
	"context"
	"time"
)

// JiraGuardedCommentIssue is the complete immutable issue identity and
// revision evidence used by guarded comment proposals.
type JiraGuardedCommentIssue struct {
	ID       string
	Key      string
	Project  string
	Updated  string
	Complete bool
}

// JiraGuardedCommentActor is the complete stable Jira Data Center identity of
// the authenticated writer. Display names and contact fields are deliberately
// outside the proposal boundary.
type JiraGuardedCommentActor struct {
	Name     string
	Key      string
	Complete bool
}

// JiraGuardedCommentWrite carries already-qualified immutable transport
// identity plus the exact native Jira-wiki bytes reviewed by the caller.
type JiraGuardedCommentWrite struct {
	ID      string
	Key     string
	Project string
	Body    []byte
}

// JiraGuardedCommentAcknowledgement contains only a qualified canonical
// comment identity. Empty means the attempted response was not usable and the
// caller must reconcile without replay.
type JiraGuardedCommentAcknowledgement struct {
	ID string
}

// JiraGuardedCommentPort is intentionally separate from Tracker. Legacy
// comment list/delete, transition, CSV, and MCP paths retain their broad
// Tracker methods; the guarded workflow has no fallback to them.
type JiraGuardedCommentPort interface {
	ReadGuardedCommentIssue(context.Context, string) (JiraGuardedCommentIssue, error)
	ReadGuardedCommentActor(context.Context) (JiraGuardedCommentActor, error)
	ListJiraCommentsQualified(context.Context, string, JiraCommentReadOptions) (JiraCommentInventory, error)
	WriteGuardedComment(context.Context, JiraGuardedCommentWrite) (JiraGuardedCommentAcknowledgement, error)
}

// ValidJiraGuardedCommentInstant admits only the exact timestamp shapes used
// by Jira's RFC3339 and numeric-offset representations. The explicit shape
// check is required because time.Parse accepts comma fractions even when the
// selected layout declares a decimal point.
func ValidJiraGuardedCommentInstant(value string) bool {
	core := ""
	switch {
	case len(value) >= 20 && value[len(value)-1] == 'Z':
		core = value[:len(value)-1]
	case len(value) >= 25 && (value[len(value)-6] == '+' || value[len(value)-6] == '-') && value[len(value)-3] == ':' &&
		jiraGuardedCommentDigits(value[len(value)-5:len(value)-3]) && jiraGuardedCommentDigits(value[len(value)-2:]):
		core = value[:len(value)-6]
	case len(value) >= 24 && (value[len(value)-5] == '+' || value[len(value)-5] == '-') &&
		jiraGuardedCommentDigits(value[len(value)-4:]):
		core = value[:len(value)-5]
	default:
		return false
	}
	if len(core) < 19 || core[4] != '-' || core[7] != '-' || core[10] != 'T' || core[13] != ':' || core[16] != ':' ||
		!jiraGuardedCommentDigits(core[0:4]) || !jiraGuardedCommentDigits(core[5:7]) || !jiraGuardedCommentDigits(core[8:10]) ||
		!jiraGuardedCommentDigits(core[11:13]) || !jiraGuardedCommentDigits(core[14:16]) || !jiraGuardedCommentDigits(core[17:19]) {
		return false
	}
	if len(core) > 19 && (core[19] != '.' || len(core) > 29 || !jiraGuardedCommentDigits(core[20:])) {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func jiraGuardedCommentDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}
